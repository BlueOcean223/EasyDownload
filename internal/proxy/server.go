package proxy

import (
	"EasyDownload/internal/logger"
	"bytes"
	"compress/gzip"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
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

// VideoInfo represents detected video information
type VideoInfo struct {
	ID           string  `json:"id"`
	Title        string  `json:"title"`
	Cover        string  `json:"cover"`
	URL          string  `json:"url"`
	Source       string  `json:"source"` // "wechat" or "bilibili"
	Quality      string  `json:"quality"`
	Duration     int     `json:"duration"`
	Author       string  `json:"author"`
	AuthorAvatar string  `json:"authorAvatar"`
	Timestamp    int64   `json:"timestamp"`
	DecodeKey    string  `json:"decodeKey"` // Decryption key for WeChat videos
	FileSize     float64 `json:"fileSize"`  // File size in bytes
	Width        int     `json:"width"`
	Height       int     `json:"height"`
	IsCurrent    bool    `json:"isCurrentVideo"`
}

// ProxyServer represents the MITM proxy server using goproxy
type ProxyServer struct {
	proxy       *goproxy.ProxyHttpServer
	certManager *CertManager
	listener    net.Listener
	port        int
	running     bool
	mu          sync.RWMutex

	// Callback for detected videos
	onVideoDetected func(VideoInfo)

	// Video URL deduplication
	detectedURLs sync.Map

	// Upstream proxy support
	upstreamProxy string

	// Diagnostics
	debug bool

	// WeChat video capture components
	jsInjector    *JSInjector
	wechatHandler *WeChatHandler

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
	ps.wechatHandler = NewWeChatHandler()

	// Set up WeChat handler callback to convert to VideoInfo
	ps.wechatHandler.SetVideoCallback(func(info WeChatVideoInfo) {
		if ps.onVideoDetected != nil {
			videoInfo := VideoInfo{
				ID:           info.ID,
				Title:        info.Title,
				Cover:        info.CoverURL,
				URL:          info.URL,
				Source:       "wechat",
				Duration:     info.Duration,
				Author:       info.Author,
				AuthorAvatar: info.AuthorAvatar,
				// Frontend expects milliseconds
				Timestamp: time.Now().UnixMilli(),
				DecodeKey: info.DecodeKey,
				FileSize:  info.FileSize,
				Width:     info.Width,
				Height:    info.Height,
				IsCurrent: info.IsCurrentVideo,
			}
			ps.onVideoDetected(videoInfo)
		}
	})

	return ps
}

// SetVideoCallback sets the callback function for detected videos
func (ps *ProxyServer) SetVideoCallback(callback func(VideoInfo)) {
	ps.onVideoDetected = callback
}

// SetUpstreamProxy sets the upstream proxy URL for proxy chaining
func (ps *ProxyServer) SetUpstreamProxy(proxyURL string) {
	ps.upstreamProxy = proxyURL
}

// SetDebug enables/disables verbose proxy diagnostics logging.
func (ps *ProxyServer) SetDebug(enabled bool) {
	ps.debug = enabled
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

	// Configure upstream proxy if set
	if ps.upstreamProxy != "" {
		proxyURL, err := url.Parse(ps.upstreamProxy)
		if err != nil {
			logger.Error("Invalid upstream proxy URL: %v", err)
		} else {
			transport := &http.Transport{
				Proxy: http.ProxyURL(proxyURL),
				DialContext: (&net.Dialer{
					Timeout:   60 * time.Second,
					KeepAlive: 30 * time.Second,
				}).DialContext,
				TLSHandshakeTimeout:   60 * time.Second,
				ResponseHeaderTimeout: 60 * time.Second,
				IdleConnTimeout:       30 * time.Second,
			}
			ps.proxy.Tr = transport
			logger.Info("Using upstream proxy: %s", ps.upstreamProxy)
		}
	}

	// Start listening
	listener, err := net.Listen("tcp", fmt.Sprintf(":%d", ps.port))
	if err != nil {
		return fmt.Errorf("failed to start listener: %w", err)
	}
	ps.listener = listener
	ps.running = true

	// Start serving in a goroutine
	go func() {
		http.Serve(listener, ps.proxy)
	}()

	logger.Info("Proxy server started on port %d", ps.port)
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

// GetPort returns the proxy port
func (ps *ProxyServer) GetPort() int {
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
	// Unified CONNECT handler:
	// - MITM only for allowlisted page/static domains (channels.weixin.qq.com, res.wx.qq.com, wxapp.tc.qq.com)
	// - Direct pass-through for video streaming domains to avoid slow playback
	// - Default pass-through for everything else to reduce overhead
	videoStreamingPattern := regexp.MustCompile(`(finder\.video\.qq\.com|findermp\.video\.qq\.com|szextshort\.weixin\.qq\.com|mpvideo\.qpic\.cn)`)
	ps.proxy.OnRequest().HandleConnectFunc(func(host string, ctx *goproxy.ProxyCtx) (*goproxy.ConnectAction, string) {
		start := time.Now()
		action := "OK"
		switch {
		case videoStreamingPattern.MatchString(host):
			action = "OK(stream)"
			if ps.debug {
				logger.Info("[proxy-debug] CONNECT %s -> %s (%s)", host, action, time.Since(start))
			}
			return goproxy.OkConnect, host
		case ps.shouldInterceptHost(host):
			action = "MITM"
			if ps.debug {
				logger.Info("[proxy-debug] CONNECT %s -> %s (%s)", host, action, time.Since(start))
			}
			return goproxy.MitmConnect, host
		default:
			if ps.debug {
				logger.Info("[proxy-debug] CONNECT %s -> %s (%s)", host, action, time.Since(start))
			}
			return goproxy.OkConnect, host
		}
	})
	logger.Info("Configured CONNECT handling: MITM allowlist + streaming passthrough")

	// Set up WeChat-specific handlers
	ps.setupWeChatHandlers()
}

// shouldInterceptHost checks if we should intercept traffic for this host.
// This only returns true for MITM domains where we need to inject scripts.
// Video streaming domains are NOT intercepted to ensure smooth playback.
func (ps *ProxyServer) shouldInterceptHost(host string) bool {
	// Only intercept MITM domains (page content domains)
	for _, h := range MITMDomains() {
		if strings.Contains(host, h) || strings.HasSuffix(host, h) {
			return true
		}
	}
	return false
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

// addVideoURL adds a video URL to the detected set, returns true if it's new
func (ps *ProxyServer) addVideoURL(url string) bool {
	_, loaded := ps.detectedURLs.LoadOrStore(url, true)
	return !loaded
}

// ClearDetectedURLs clears the detected video URLs cache
func (ps *ProxyServer) ClearDetectedURLs() {
	ps.detectedURLs = sync.Map{}
}

// setupWeChatHandlers configures WeChat-specific request and response handlers
func (ps *ProxyServer) setupWeChatHandlers() {
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
	if !ps.debug || r == nil {
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

	if ps.debug {
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
		if ps.debug {
			logger.Info("[proxy-debug] WECHAT_PING host=%q len=%d", r.Host, len(body))
		}
		return r, goproxy.NewResponse(r, goproxy.ContentTypeText, http.StatusOK, "OK")
	}

	// Low-frequency trace from injected scripts (throttled on client side).
	// Useful to confirm whether injected hooks are being executed and what they see.
	if reqType == "trace" {
		if ps.debug {
			logger.Info("[proxy-debug] WECHAT_TRACE host=%q len=%d body=%s", r.Host, len(body), string(body))
		}
		return r, goproxy.NewResponse(r, goproxy.ContentTypeText, http.StatusOK, "OK")
	}

	// Process the request through WeChatHandler with type
	if err := ps.wechatHandler.HandleWeChatRequestWithType(body, reqType); err != nil {
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
	if ps.debug {
		logger.Info("[proxy-debug] RESP res.wx.qq.com %s status=%d enc=%q ct=%q", requestPath, resp.StatusCode, resp.Header.Get("Content-Encoding"), resp.Header.Get("Content-Type"))
	}

	contentType := resp.Header.Get("Content-Type")
	// Only process JavaScript files
	if !strings.Contains(contentType, "javascript") && !strings.Contains(contentType, "text/plain") {
		if ps.debug {
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
		if ps.debug {
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
		if ps.debug {
			logger.Info("[proxy-debug] SKIP js (pass-through): %s", requestPath)
		}
		return resp
	}

	// Enforce injection budget for non-target bundles to avoid reintroducing playback issues.
	const maxWeChatJSInjections = int32(4)
	if !isTargetFile && atomic.LoadInt32(&ps.wechatJSInjected) >= maxWeChatJSInjections {
		if ps.debug {
			logger.Info("[proxy-debug] SKIP js (budget reached %d): %s", maxWeChatJSInjections, requestPath)
		}
		return resp
	}

	modifiedJS := ps.jsInjector.InjectAll(jsContent)
	if modifiedJS == jsContent {
		// No changes (regex didn't match); keep original bytes/headers
		if ps.debug {
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
	if ps.debug {
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
	if ps.debug && ctx != nil && ctx.Req != nil {
		logger.Info("[proxy-debug] RESP channels.weixin.qq.com %s status=%d enc=%q ct=%q", ctx.Req.URL.String(), resp.StatusCode, resp.Header.Get("Content-Encoding"), contentType)
	}
	// Only process HTML responses
	if !strings.Contains(contentType, "text/html") {
		if ps.debug && ctx != nil && ctx.Req != nil {
			logger.Info("[proxy-debug] SKIP html (content-type): %s ct=%q", ctx.Req.URL.String(), contentType)
		}
		return resp
	}

	// Read response body (decode gzip/br if possible)
	body, decoded, err := readResponseBody(resp)
	if err != nil {
		logger.Error("Failed to read HTML response body: %v", err)
		return resp
	}
	// If Content-Encoding exists but we couldn't decode it, do NOT modify this response
	if !decoded && strings.TrimSpace(resp.Header.Get("Content-Encoding")) != "" {
		if ps.debug && ctx != nil && ctx.Req != nil {
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
	if ps.debug && ctx != nil && ctx.Req != nil {
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
func (ps *ProxyServer) GetWeChatHandler() *WeChatHandler {
	return ps.wechatHandler
}

// GetJSInjector returns the JS injector instance
func (ps *ProxyServer) GetJSInjector() *JSInjector {
	return ps.jsInjector
}
