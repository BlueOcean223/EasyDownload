package proxy

import (
	"EasyDownload/internal/download/wechat"
	"EasyDownload/internal/infra/logger"
	"bytes"
	"compress/gzip"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/andybalholm/brotli"
	"github.com/elazarl/goproxy"
)

// ProxyServer represents the MITM proxy server using goproxy
type ProxyServer struct {
	proxy       *goproxy.ProxyHttpServer
	certManager *CertManager
	listener    net.Listener
	port        int
	running     bool
	mu          sync.RWMutex

	// Callback for detected videos
	onVideoDetected func(wechat.VideoInfo)

	// Upstream proxy support
	upstreamProxy string

	// Diagnostics
	debug atomic.Bool

	// WeChat video capture components
	jsInjector    *JSInjector
	wechatHandler *wechat.Handler

	// Limit how many res.wx.qq.com JS files we modify per session to reduce breakage risk.
	wechatJSInjected int32
}

// NewProxyServer creates a new proxy server instance
func NewProxyServer(certManager *CertManager, port int) *ProxyServer {
	ps := &ProxyServer{
		certManager: certManager,
		port:        port,
		running:     false,
	}

	// Initialize WeChat video capture components
	ps.jsInjector = NewJSInjector()
	ps.wechatHandler = wechat.NewHandler()

	// Forward the boundary model to the composition root. It is converted to
	// detection.Video before entering the DetectionStore.
	ps.wechatHandler.SetVideoCallback(func(info wechat.VideoInfo) {
		if callback := ps.videoCallback(); callback != nil {
			callback(info)
		}
	})

	return ps
}

// SetVideoCallback sets the callback function for detected videos
func (ps *ProxyServer) SetVideoCallback(callback func(wechat.VideoInfo)) {
	ps.mu.Lock()
	ps.onVideoDetected = callback
	ps.mu.Unlock()
}

func (ps *ProxyServer) videoCallback() func(wechat.VideoInfo) {
	ps.mu.RLock()
	callback := ps.onVideoDetected
	ps.mu.RUnlock()
	return callback
}

// SetUpstreamProxy sets the upstream proxy URL for proxy chaining.
// When the proxy is already running, the outbound transport is rebuilt so the
// change takes effect immediately for new upstream requests.
func (ps *ProxyServer) SetUpstreamProxy(proxyURL string) {
	ps.mu.Lock()
	defer ps.mu.Unlock()

	ps.upstreamProxy = strings.TrimSpace(proxyURL)
	if ps.running && ps.proxy != nil && ps.proxy.Tr != nil {
		// The transport's Proxy func reads ps.upstreamProxy for every new request;
		// close idle connections so future requests pick up the new routing promptly.
		ps.proxy.Tr.CloseIdleConnections()
	}
}

// SetDebug enables/disables verbose proxy diagnostics logging.
func (ps *ProxyServer) SetDebug(enabled bool) {
	ps.debug.Store(enabled)
}

// setupTransport configures the HTTP transport with optimized settings.
// This is always called regardless of upstream proxy configuration.
// Key optimizations:
// - Disable HTTP/2 to avoid multiplexing issues with video streaming
// - Connection pooling for better performance
// - Reasonable timeouts for stability
func (ps *ProxyServer) setupTransport() {
	transport := &http.Transport{
		DisableKeepAlives: false,
		ForceAttemptHTTP2: false, // Disable HTTP/2 to avoid video streaming issues
		TLSNextProto:      make(map[string]func(authority string, c *tls.Conn) http.RoundTripper),
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSHandshakeTimeout: 10 * time.Second,
		IdleConnTimeout:     90 * time.Second,
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 10,
	}

	// The proxy function reads the latest upstream setting per request, allowing
	// runtime changes without swapping ps.proxy.Tr while requests are in flight.
	transport.Proxy = ps.upstreamProxyForRequest
	if ps.upstreamProxy != "" {
		logger.Info("Using upstream proxy: %s", ps.upstreamProxy)
	}

	if oldTransport := ps.proxy.Tr; oldTransport != nil {
		oldTransport.CloseIdleConnections()
	}
	ps.proxy.Tr = transport
	logger.Info("Transport configured: HTTP/2 disabled, connection pooling enabled")
}

func (ps *ProxyServer) upstreamProxyForRequest(req *http.Request) (*url.URL, error) {
	ps.mu.RLock()
	proxyURL := strings.TrimSpace(ps.upstreamProxy)
	ps.mu.RUnlock()
	if proxyURL == "" {
		return nil, nil
	}
	parsed, err := url.Parse(proxyURL)
	if err != nil {
		logger.Error("Invalid upstream proxy URL: %v", err)
		return nil, nil
	}
	return parsed, nil
}

// Start starts the proxy server
func (ps *ProxyServer) Start() error {
	ps.mu.Lock()
	defer ps.mu.Unlock()

	if ps.running {
		return fmt.Errorf("proxy server is already running")
	}

	// Load CA certificate
	caCert, caKey, err := ps.certManager.LoadCACert()
	if err != nil {
		return fmt.Errorf("failed to load CA certificate: %w", err)
	}

	// Create goproxy instance
	ps.proxy = goproxy.NewProxyHttpServer()
	ps.proxy.Verbose = false

	// Set up CA certificate for MITM
	goproxyCa, err := tls.X509KeyPair(
		pemEncodeCert(caCert.Raw),
		pemEncodeKey(caKey),
	)
	if err != nil {
		return fmt.Errorf("failed to create TLS certificate: %w", err)
	}

	// Configure goproxy to use our CA
	goproxyCa.Leaf = caCert
	goproxy.GoproxyCa = goproxyCa
	goproxy.OkConnect = &goproxy.ConnectAction{Action: goproxy.ConnectAccept, TLSConfig: goproxy.TLSConfigFromCA(&goproxyCa)}
	goproxy.MitmConnect = &goproxy.ConnectAction{Action: goproxy.ConnectMitm, TLSConfig: goproxy.TLSConfigFromCA(&goproxyCa)}
	goproxy.HTTPMitmConnect = &goproxy.ConnectAction{Action: goproxy.ConnectHTTPMitm, TLSConfig: goproxy.TLSConfigFromCA(&goproxyCa)}
	goproxy.RejectConnect = &goproxy.ConnectAction{Action: goproxy.ConnectReject, TLSConfig: goproxy.TLSConfigFromCA(&goproxyCa)}

	// Set up request/response handlers
	ps.setupHandlers()

	// Configure optimized transport (always, regardless of upstream proxy)
	ps.setupTransport()

	// Start listening on localhost only. This proxy is intended for the local
	// desktop app/system proxy and must not become reachable from the LAN.
	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", ps.port))
	if err != nil {
		return fmt.Errorf("failed to start listener: %w", err)
	}
	ps.listener = listener
	ps.running = true

	// Start serving in a goroutine
	go func() {
		if err := http.Serve(listener, ps.proxy); err != nil && !errors.Is(err, net.ErrClosed) {
			logger.Error("Proxy server stopped with error: %v", err)
		}
	}()

	logger.Info("Proxy server started on 127.0.0.1:%d", ps.port)
	return nil
}

// Stop stops the proxy server
func (ps *ProxyServer) Stop() error {
	ps.mu.Lock()
	defer ps.mu.Unlock()

	if !ps.running {
		return nil
	}

	ps.running = false
	if ps.listener != nil {
		ps.listener.Close()
	}

	logger.Info("Proxy server stopped")
	return nil
}

// IsRunning returns whether the proxy is running
func (ps *ProxyServer) IsRunning() bool {
	ps.mu.RLock()
	defer ps.mu.RUnlock()
	return ps.running
}

// SetPort updates the port used by the next Start. Changing a live listener is
// deliberately rejected; callers can persist the setting and apply it after
// Stop instead of creating a second listener or an ambiguous partial switch.
func (ps *ProxyServer) SetPort(port int) error {
	if port < 1 || port > 65535 {
		return fmt.Errorf("proxy port must be between 1 and 65535")
	}
	ps.mu.Lock()
	defer ps.mu.Unlock()
	if ps.running && port != ps.port {
		return fmt.Errorf("cannot change proxy port while proxy is running")
	}
	ps.port = port
	return nil
}

// GetPort returns the proxy port
func (ps *ProxyServer) GetPort() int {
	ps.mu.RLock()
	defer ps.mu.RUnlock()
	return ps.port
}

// pemEncodeCert encodes a certificate to PEM format
func pemEncodeCert(certDER []byte) []byte {
	return []byte("-----BEGIN CERTIFICATE-----\n" +
		base64Encode(certDER) +
		"\n-----END CERTIFICATE-----\n")
}

// pemEncodeKey encodes an RSA private key to PEM format
func pemEncodeKey(key interface{}) []byte {
	switch k := key.(type) {
	case *rsa.PrivateKey:
		keyDER := x509.MarshalPKCS1PrivateKey(k)
		return []byte("-----BEGIN RSA PRIVATE KEY-----\n" +
			base64Encode(keyDER) +
			"\n-----END RSA PRIVATE KEY-----\n")
	default:
		return nil
	}
}

// base64Encode encodes bytes to base64 with line breaks
func base64Encode(data []byte) string {
	encoded := base64.StdEncoding.EncodeToString(data)
	var result strings.Builder
	for i := 0; i < len(encoded); i += 64 {
		end := i + 64
		if end > len(encoded) {
			end = len(encoded)
		}
		result.WriteString(encoded[i:end])
		if end < len(encoded) {
			result.WriteString("\n")
		}
	}
	return result.String()
}

// MITMDomains returns the list of domains that should have MITM enabled.
// These are page content domains where we need to inject scripts.
// Video streaming domains are NOT included to ensure smooth video playback.
func MITMDomains() []string {
	return []string{
		"channels.weixin.qq.com", // WeChat video channel pages
		"res.wx.qq.com",          // WeChat static resources (JS files for injection)
		"wxapp.tc.qq.com",        // Fake domain for injected JS to send data
	}
}

// PassThroughDomains returns the list of video streaming domains that should
// NOT have MITM enabled. These domains pass through directly to avoid
// performance issues with video loading.
func PassThroughDomains() []string {
	return []string{
		"finder.video.qq.com",      // WeChat video streaming
		"findermp.video.qq.com",    // WeChat video streaming (alternate)
		"szextshort.weixin.qq.com", // WeChat short video streaming
		"mpvideo.qpic.cn",          // WeChat video CDN
	}
}

// setupHandlers configures the goproxy request and response handlers
func (ps *ProxyServer) setupHandlers() {
	// Use strict selective MITM: only domains listed in MITMDomains are decrypted.
	// Everything else is passed through as a direct CONNECT tunnel to avoid
	// breaking unrelated traffic and to keep the privacy boundary narrow.
	ps.proxy.OnRequest().HandleConnectFunc(func(host string, ctx *goproxy.ProxyCtx) (*goproxy.ConnectAction, string) {
		if shouldMITMHost(host) {
			if ps.debug.Load() {
				logger.Info("[proxy-debug] CONNECT MITM: %s", host)
			}
			return goproxy.MitmConnect, host
		}
		if ps.debug.Load() {
			logger.Info("[proxy-debug] CONNECT pass-through (outside MITM allowlist): %s", host)
		}
		return goproxy.OkConnect, host
	})
	logger.Info("Configured CONNECT handling: strict MITM allowlist")

	// Set up WeChat-specific handlers
	ps.setupWeChatHandlers()
}

func shouldMITMHost(host string) bool {
	for _, domain := range MITMDomains() {
		if hostMatchesDomain(host, domain) {
			return true
		}
	}
	return false
}

func hostMatchesDomain(host, domain string) bool {
	host = strings.TrimSpace(strings.ToLower(host))
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.TrimSuffix(host, ".")
	domain = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(domain)), ".")
	return host == domain || strings.HasSuffix(host, "."+domain)
}

// readResponseBody reads and (if possible) decompresses the response body.
// Returns:
// - body: response bytes (decoded if supported encoding)
// - decoded: true if Content-Encoding was decoded (gzip/br) and body is now plain
func readResponseBody(resp *http.Response) (body []byte, decoded bool, err error) {
	if resp.Body == nil {
		return []byte{}, false, nil
	}

	encoding := strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Encoding")))
	switch encoding {
	case "gzip":
		gzReader, gzErr := gzip.NewReader(resp.Body)
		if gzErr != nil {
			// If gzip fails, fall back to raw (but mark as not decoded)
			body, err = io.ReadAll(resp.Body)
			return body, false, err
		}
		defer gzReader.Close()
		body, err = io.ReadAll(gzReader)
		return body, err == nil, err
	case "br":
		brReader := brotli.NewReader(resp.Body)
		body, err = io.ReadAll(brReader)
		return body, err == nil, err
	default:
		body, err = io.ReadAll(resp.Body)
		return body, false, err
	}
}

// setupWeChatHandlers configures WeChat-specific request and response handlers
func (ps *ProxyServer) setupWeChatHandlers() {
	// Video streaming domains - pass through without any modification
	// This is critical for smooth video playback with AlwaysMitm strategy
	videoStreamingPattern := regexp.MustCompile(`(finder\.video\.qq\.com|findermp\.video\.qq\.com|szextshort\.weixin\.qq\.com|mpvideo\.qpic\.cn)`)
	ps.proxy.OnResponse(goproxy.ReqHostMatches(videoStreamingPattern)).
		DoFunc(func(resp *http.Response, ctx *goproxy.ProxyCtx) *http.Response {
			// Return response as-is without any modification
			return resp
		})

	// Handle requests to /res-downloader/wechat endpoint (from injected JS)
	ps.proxy.OnRequest(goproxy.ReqHostMatches(regexp.MustCompile(`wxapp\.tc\.qq\.com`))).
		DoFunc(ps.handleWeChatAPIRequest)

	// Handle same-origin requests to /res-downloader/wechat from channels.weixin.qq.com
	// (We inject a script into Channels pages and post back to the same origin.)
	ps.proxy.OnRequest(goproxy.ReqHostMatches(regexp.MustCompile(`channels\.weixin\.qq\.com`))).
		DoFunc(ps.handleWeChatAPIRequest)

	// Diagnostics: capture WeChat page error reports (contains useful JS error details)
	ps.proxy.OnRequest(goproxy.ReqHostMatches(regexp.MustCompile(`channels\.weixin\.qq\.com`))).
		DoFunc(ps.handleWeChatReportRequest)

	// Handle JS responses from res.wx.qq.com for injection
	ps.proxy.OnResponse(goproxy.ReqHostMatches(regexp.MustCompile(`res\.wx\.qq\.com`))).
		DoFunc(ps.handleWeChatJSResponse)

	// Handle HTML responses from channels.weixin.qq.com for version injection
	ps.proxy.OnResponse(goproxy.ReqHostMatches(regexp.MustCompile(`channels\.weixin\.qq\.com`))).
		DoFunc(ps.handleWeChatHTMLResponse)
}

// handleWeChatReportRequest logs request bodies for /web/report-error and /web/report-perf
// to help diagnose why the Channels page fails under MITM.
func (ps *ProxyServer) handleWeChatReportRequest(r *http.Request, ctx *goproxy.ProxyCtx) (*http.Request, *http.Response) {
	if !ps.debug.Load() || r == nil {
		return r, nil
	}
	if !strings.Contains(r.URL.Path, "/web/report-") {
		return r, nil
	}

	// Only log the known endpoints to reduce noise
	if !strings.Contains(r.URL.Path, "/web/report-error") && !strings.Contains(r.URL.Path, "/web/report-perf") {
		return r, nil
	}

	var bodyBytes []byte
	if r.Body != nil {
		b, err := io.ReadAll(r.Body)
		if err == nil {
			bodyBytes = b
		}
		_ = r.Body.Close()
		r.Body = io.NopCloser(bytes.NewReader(bodyBytes))
	}

	// Avoid dumping huge bodies; keep first 8KB
	const maxLog = 8 * 1024
	logBody := bodyBytes
	truncated := false
	if len(logBody) > maxLog {
		logBody = logBody[:maxLog]
		truncated = true
	}
	logger.Info("[proxy-debug] REPORT %s %s ct=%q len=%d truncated=%v body=%s",
		r.Method,
		r.URL.String(),
		r.Header.Get("Content-Type"),
		len(bodyBytes),
		truncated,
		string(logBody),
	)

	return r, nil
}

// handleWeChatAPIRequest handles POST requests to /res-downloader/wechat
// Supports type=1/2 (video detection) and type=download (trigger download)
func (ps *ProxyServer) handleWeChatAPIRequest(r *http.Request, ctx *goproxy.ProxyCtx) (*http.Request, *http.Response) {
	// Only handle requests to our fake endpoint
	if !strings.Contains(r.URL.Path, "/res-downloader/wechat") {
		return r, nil
	}

	// Only handle POST requests
	if r.Method != "POST" {
		return r, nil
	}

	// Get request type from query parameter
	reqType := r.URL.Query().Get("type")
	if reqType == "" {
		reqType = "1" // Default to video detection
	}

	// Read request body
	body, err := io.ReadAll(r.Body)
	if err != nil {
		logger.Error("Failed to read WeChat request body: %v", err)
		return r, goproxy.NewResponse(r, goproxy.ContentTypeText, http.StatusBadRequest, "Bad Request")
	}
	r.Body.Close()

	if ps.debug.Load() {
		// Avoid dumping huge bodies; keep first 2KB
		const maxLog = 2 * 1024
		logBody := body
		truncated := false
		if len(logBody) > maxLog {
			logBody = logBody[:maxLog]
			truncated = true
		}
		logger.Info("[proxy-debug] WECHAT_API %s %s host=%q type=%q len=%d truncated=%v body=%s",
			r.Method,
			r.URL.String(),
			r.Host,
			reqType,
			len(body),
			truncated,
			string(logBody),
		)
	}

	// Heartbeat from injected scripts to help diagnose whether script is running at all.
	if reqType == "ping" {
		if ps.debug.Load() {
			logger.Info("[proxy-debug] WECHAT_PING host=%q len=%d", r.Host, len(body))
		}
		return r, goproxy.NewResponse(r, goproxy.ContentTypeText, http.StatusOK, "OK")
	}

	// Low-frequency trace from injected scripts (throttled on client side).
	// Useful to confirm whether injected hooks are being executed and what they see.
	if reqType == "trace" {
		if ps.debug.Load() {
			logger.Info("[proxy-debug] WECHAT_TRACE host=%q len=%d body=%s", r.Host, len(body), string(body))
		}
		return r, goproxy.NewResponse(r, goproxy.ContentTypeText, http.StatusOK, "OK")
	}

	// Process the request through WeChatHandler with type
	if err := ps.wechatHandler.HandleRequestWithType(body, reqType); err != nil {
		logger.Debug("WeChat request processing (type=%s): %v", reqType, err)
	}

	// Return a success response to the injected JS
	return r, goproxy.NewResponse(r, goproxy.ContentTypeText, http.StatusOK, "OK")
}

// handleWeChatJSResponse handles JS responses from res.wx.qq.com for injection
// It processes two types of JS files:
// 1. Target files (virtual_svg-icons-register): Apply both injection AND version rewriting
// 2. Other versioned JS files: Apply only version rewriting to their internal imports
func (ps *ProxyServer) handleWeChatJSResponse(resp *http.Response, ctx *goproxy.ProxyCtx) *http.Response {
	if resp == nil || ctx.Req == nil {
		return resp
	}

	requestPath := ctx.Req.URL.String()
	if ps.debug.Load() {
		logger.Info("[proxy-debug] RESP res.wx.qq.com %s status=%d enc=%q ct=%q", requestPath, resp.StatusCode, resp.Header.Get("Content-Encoding"), resp.Header.Get("Content-Type"))
	}

	contentType := resp.Header.Get("Content-Type")
	// Only process JavaScript files
	if !strings.Contains(contentType, "javascript") && !strings.Contains(contentType, "text/plain") {
		if ps.debug.Load() {
			logger.Info("[proxy-debug] SKIP js (content-type): %s ct=%q", requestPath, contentType)
		}
		return resp
	}

	// Read raw bytes first so we can keep pass-through responses unchanged if we decide not to inject.
	rawBody, err := readResponseBodyRaw(resp)
	if err != nil {
		logger.Error("Failed to read JS response body: %v", err)
		return resp
	}

	encoding := strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Encoding")))
	decodedBody, decoded, derr := decodeBodyBytes(rawBody, encoding)
	if derr != nil {
		// Can't inspect/modify; keep original body/headers intact
		if ps.debug.Load() {
			logger.Info("[proxy-debug] SKIP js (decode failed enc=%q): %s", encoding, requestPath)
		}
		return resp
	}

	jsContent := string(decodedBody)

	// Decide whether to inject:
	// - Always inject into virtual_svg-icons-register (known-safe)
	// - Additionally, inject into a SMALL number of finder bundles that actually contain our target patterns.
	isTargetFile := ps.jsInjector.IsTargetJSFile(requestPath)
	isFinderBundle := strings.Contains(requestPath, "/t/wx_fed/finder/web/web-finder/res/js/") &&
		strings.HasSuffix(strings.Split(requestPath, "?")[0], ".js") &&
		strings.Contains(requestPath, "publish")
	hasCandidate := strings.Contains(jsContent, "finderGetCommentDetail") || strings.Contains(jsContent, "get media")

	if !isTargetFile && !(isFinderBundle && hasCandidate) {
		if ps.debug.Load() {
			logger.Info("[proxy-debug] SKIP js (pass-through): %s", requestPath)
		}
		return resp
	}

	// Enforce injection budget for non-target bundles to avoid reintroducing playback issues.
	const maxWeChatJSInjections = int32(4)
	if !isTargetFile && atomic.LoadInt32(&ps.wechatJSInjected) >= maxWeChatJSInjections {
		if ps.debug.Load() {
			logger.Info("[proxy-debug] SKIP js (budget reached %d): %s", maxWeChatJSInjections, requestPath)
		}
		return resp
	}

	modifiedJS := ps.jsInjector.InjectAll(jsContent)
	if modifiedJS == jsContent {
		// No changes (regex didn't match); keep original bytes/headers
		if ps.debug.Load() {
			logger.Info("[proxy-debug] SKIP js (no injection match): %s", requestPath)
		}
		return resp
	}

	if !isTargetFile {
		atomic.AddInt32(&ps.wechatJSInjected, 1)
	}
	logger.Info("Injected capture code into WeChat JS file: %s", ctx.Req.URL.Path)

	// Create new response with modified body
	modifiedBody := []byte(modifiedJS)
	resp.Body = io.NopCloser(bytes.NewReader(modifiedBody))
	resp.ContentLength = int64(len(modifiedBody))
	resp.Header.Set("Content-Length", fmt.Sprintf("%d", len(modifiedBody)))
	// We are serving plain JS bytes now; remove encoding to avoid mismatches.
	if decoded || strings.TrimSpace(resp.Header.Get("Content-Encoding")) != "" {
		resp.Header.Del("Content-Encoding")
	}
	if ps.debug.Load() {
		logger.Info("[proxy-debug] REWRITE js done: %s decoded=%v in=%d out=%d", requestPath, decoded, len(jsContent), len(modifiedBody))
	}

	return resp
}

// readResponseBodyRaw reads the response body as-is and restores it, so we can decide later whether to modify.
func readResponseBodyRaw(resp *http.Response) ([]byte, error) {
	if resp.Body == nil {
		return []byte{}, nil
	}
	raw, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	// Restore the original bytes for pass-through
	resp.Body = io.NopCloser(bytes.NewReader(raw))
	return raw, err
}

// decodeBodyBytes decodes gzip/br response bodies from raw bytes for inspection/modification.
// Returns decoded bytes, a decoded flag (true if encoding was decoded), and error if decoding failed.
func decodeBodyBytes(raw []byte, encoding string) ([]byte, bool, error) {
	switch encoding {
	case "gzip":
		gr, err := gzip.NewReader(bytes.NewReader(raw))
		if err != nil {
			return nil, false, err
		}
		defer gr.Close()
		b, err := io.ReadAll(gr)
		return b, err == nil, err
	case "br":
		br := brotli.NewReader(bytes.NewReader(raw))
		b, err := io.ReadAll(br)
		return b, err == nil, err
	default:
		// no encoding
		return raw, false, nil
	}
}

// handleWeChatHTMLResponse handles HTML responses from channels.weixin.qq.com
// to add version parameters to JS links for cache busting and inject download button script
func (ps *ProxyServer) handleWeChatHTMLResponse(resp *http.Response, ctx *goproxy.ProxyCtx) *http.Response {
	if resp == nil {
		return resp
	}

	contentType := resp.Header.Get("Content-Type")
	if ps.debug.Load() && ctx != nil && ctx.Req != nil {
		logger.Info("[proxy-debug] RESP channels.weixin.qq.com %s status=%d enc=%q ct=%q", ctx.Req.URL.String(), resp.StatusCode, resp.Header.Get("Content-Encoding"), contentType)
	}
	// Only process HTML responses
	if !strings.Contains(contentType, "text/html") {
		if ps.debug.Load() && ctx != nil && ctx.Req != nil {
			logger.Info("[proxy-debug] SKIP html (content-type): %s ct=%q", ctx.Req.URL.String(), contentType)
		}
		return resp
	}

	// Read raw bytes first and restore the original body. If the response uses an
	// unsupported/invalid Content-Encoding, returning resp below still preserves
	// the original body for pass-through.
	rawBody, err := readResponseBodyRaw(resp)
	if err != nil {
		logger.Error("Failed to read HTML response body: %v", err)
		return resp
	}
	encoding := strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Encoding")))
	body, decoded, err := decodeBodyBytes(rawBody, encoding)
	if err != nil {
		if ps.debug.Load() && ctx != nil && ctx.Req != nil {
			logger.Info("[proxy-debug] SKIP html (decode failed enc=%q): %s", resp.Header.Get("Content-Encoding"), ctx.Req.URL.String())
		}
		return resp
	}
	// If Content-Encoding exists but we didn't decode it, do NOT modify this response.
	if !decoded && encoding != "" {
		if ps.debug.Load() && ctx != nil && ctx.Req != nil {
			logger.Info("[proxy-debug] SKIP html (cannot decode enc=%q): %s", resp.Header.Get("Content-Encoding"), ctx.Req.URL.String())
		}
		return resp
	}

	// Add version to JS links
	htmlContent := string(body)
	modifiedHTML := ps.jsInjector.AddVersionToJSLinks(htmlContent)

	// Inject download button script into HTML
	modifiedHTML = ps.injectDownloadButtonScript(modifiedHTML)

	// Create new response with modified body
	modifiedBody := []byte(modifiedHTML)
	resp.Body = io.NopCloser(bytes.NewReader(modifiedBody))
	resp.ContentLength = int64(len(modifiedBody))
	resp.Header.Set("Content-Length", fmt.Sprintf("%d", len(modifiedBody)))
	// Remove encoding only if we decoded it
	if decoded {
		resp.Header.Del("Content-Encoding")
	}
	if ps.debug.Load() && ctx != nil && ctx.Req != nil {
		logger.Info("[proxy-debug] REWRITE html done: %s decoded=%v out=%d", ctx.Req.URL.String(), decoded, len(modifiedBody))
	}

	return resp
}

// injectDownloadButtonScript injects the download button JavaScript into HTML content
func (ps *ProxyServer) injectDownloadButtonScript(htmlContent string) string {
	downloadScript := ps.jsInjector.GetDownloadButtonScript()
	if downloadScript == "" {
		return htmlContent
	}

	scriptTag := fmt.Sprintf("<script>%s</script>", downloadScript)

	// Prefer injecting early (inside <head>) so fetch/XHR hooks are installed
	// before the page's main bundles execute.
	lowerHTML := strings.ToLower(htmlContent)

	// Insert right after <head ...> if present
	if headIdx := strings.Index(lowerHTML, "<head"); headIdx != -1 {
		if gtIdx := strings.Index(lowerHTML[headIdx:], ">"); gtIdx != -1 {
			insertAt := headIdx + gtIdx + 1
			return htmlContent[:insertAt] + scriptTag + htmlContent[insertAt:]
		}
	}

	// Fallback: insert before the first <script ...> to be as early as possible
	if scriptIdx := strings.Index(lowerHTML, "<script"); scriptIdx != -1 {
		return htmlContent[:scriptIdx] + scriptTag + htmlContent[scriptIdx:]
	}

	// Last resorts: before </body> / </html> / append
	if idx := strings.LastIndex(lowerHTML, "</body>"); idx != -1 {
		return htmlContent[:idx] + scriptTag + htmlContent[idx:]
	}
	if idx := strings.LastIndex(lowerHTML, "</html>"); idx != -1 {
		return htmlContent[:idx] + scriptTag + htmlContent[idx:]
	}

	// If no closing tags found, append to end
	return htmlContent + scriptTag
}

// GetWeChatHandler returns the WeChat handler instance
func (ps *ProxyServer) GetWeChatHandler() *wechat.Handler {
	return ps.wechatHandler
}

// GetJSInjector returns the JS injector instance
func (ps *ProxyServer) GetJSInjector() *JSInjector {
	return ps.jsInjector
}
