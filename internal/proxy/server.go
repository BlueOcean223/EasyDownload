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
	"time"

	"github.com/elazarl/goproxy"
)

// VideoInfo represents detected video information
type VideoInfo struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Cover     string `json:"cover"`
	URL       string `json:"url"`
	Source    string `json:"source"` // "wechat" or "bilibili"
	Quality   string `json:"quality"`
	Duration  int    `json:"duration"`
	Author    string `json:"author"`
	Timestamp int64  `json:"timestamp"`
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

	// Injection script
	injectScript string

	// Video URL deduplication
	detectedURLs sync.Map

	// Upstream proxy support
	upstreamProxy string
}

// NewProxyServer creates a new proxy server instance
func NewProxyServer(certManager *CertManager, port int) *ProxyServer {
	return &ProxyServer{
		certManager: certManager,
		port:        port,
		running:     false,
	}
}

// SetVideoCallback sets the callback function for detected videos
func (ps *ProxyServer) SetVideoCallback(callback func(VideoInfo)) {
	ps.onVideoDetected = callback
}

// SetInjectScript sets the JavaScript to inject into pages
func (ps *ProxyServer) SetInjectScript(script string) {
	ps.injectScript = script
}

// SetUpstreamProxy sets the upstream proxy URL for proxy chaining
func (ps *ProxyServer) SetUpstreamProxy(proxyURL string) {
	ps.upstreamProxy = proxyURL
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

// setupHandlers configures the goproxy request and response handlers
func (ps *ProxyServer) setupHandlers() {
	// Configure HTTPS MITM only for WeChat page/API domains (not video streaming domains)
	// Video streaming domains (finder.video.qq.com, findermp.video.qq.com) should pass through
	// directly to avoid slow video loading

	// MITM for page content and API responses
	ps.proxy.OnRequest(goproxy.ReqHostMatches(regexp.MustCompile(`channels\.weixin\.qq\.com`))).
		HandleConnect(goproxy.AlwaysMitm)
	ps.proxy.OnRequest(goproxy.ReqHostMatches(regexp.MustCompile(`szextshort\.weixin\.qq\.com`))).
		HandleConnect(goproxy.AlwaysMitm)
	ps.proxy.OnRequest(goproxy.ReqHostMatches(regexp.MustCompile(`res\.wx\.qq\.com`))).
		HandleConnect(goproxy.AlwaysMitm)

	// Handle responses for video detection and JS injection (only for MITM'd domains)
	ps.proxy.OnResponse(goproxy.ReqHostMatches(regexp.MustCompile(`(channels\.weixin\.qq\.com|szextshort\.weixin\.qq\.com|res\.wx\.qq\.com)`))).
		DoFunc(ps.handleResponse)
}

// shouldInterceptHost checks if we should intercept traffic for this host
func (ps *ProxyServer) shouldInterceptHost(host string) bool {
	interceptHosts := []string{
		"channels.weixin.qq.com",
		"finder.video.qq.com",
		"szextshort.weixin.qq.com",
		"findermp.video.qq.com",
	}

	for _, h := range interceptHosts {
		if strings.Contains(host, h) || strings.HasSuffix(host, h) {
			return true
		}
	}
	return false
}

// handleResponse processes HTTP responses for video detection and JS injection
func (ps *ProxyServer) handleResponse(resp *http.Response, ctx *goproxy.ProxyCtx) *http.Response {
	if resp == nil {
		return resp
	}

	contentType := resp.Header.Get("Content-Type")

	// Skip processing for video/binary content to avoid slow loading
	if strings.Contains(contentType, "video/") ||
		strings.Contains(contentType, "audio/") ||
		strings.Contains(contentType, "application/octet-stream") {
		return resp
	}

	url := ""
	if ctx.Req != nil {
		url = ctx.Req.URL.String()
	}

	// Read response body
	body, err := readResponseBody(resp)
	if err != nil {
		logger.Error("Failed to read response body: %v", err)
		return resp
	}

	// Inject script into HTML responses
	if strings.Contains(contentType, "text/html") && ps.injectScript != "" {
		body = ps.injectScriptIntoHTML(body)
	}

	// Detect video URLs in JSON responses
	if strings.Contains(contentType, "application/json") || strings.Contains(contentType, "text/plain") {
		ps.detectVideoInResponse(url, body, contentType)
	}

	// Create new response with modified body
	resp.Body = io.NopCloser(bytes.NewReader(body))
	resp.ContentLength = int64(len(body))
	resp.Header.Set("Content-Length", fmt.Sprintf("%d", len(body)))
	resp.Header.Del("Content-Encoding") // Remove encoding since we decoded it

	return resp
}

// readResponseBody reads and decompresses the response body
func readResponseBody(resp *http.Response) ([]byte, error) {
	if resp.Body == nil {
		return []byte{}, nil
	}

	var body []byte
	var err error

	encoding := resp.Header.Get("Content-Encoding")
	switch encoding {
	case "gzip":
		gzReader, gzErr := gzip.NewReader(resp.Body)
		if gzErr != nil {
			// If gzip fails, try reading raw
			return io.ReadAll(resp.Body)
		}
		defer gzReader.Close()
		body, err = io.ReadAll(gzReader)
	default:
		body, err = io.ReadAll(resp.Body)
	}

	return body, err
}

// injectScriptIntoHTML injects JavaScript into HTML content
func (ps *ProxyServer) injectScriptIntoHTML(body []byte) []byte {
	html := string(body)

	// Find </body> or </html> and inject script before it
	scriptTag := fmt.Sprintf("<script>%s</script>", ps.injectScript)

	if idx := strings.LastIndex(strings.ToLower(html), "</body>"); idx != -1 {
		html = html[:idx] + scriptTag + html[idx:]
	} else if idx := strings.LastIndex(strings.ToLower(html), "</html>"); idx != -1 {
		html = html[:idx] + scriptTag + html[idx:]
	} else {
		html = html + scriptTag
	}

	return []byte(html)
}

// detectVideoInResponse detects video URLs in response content
func (ps *ProxyServer) detectVideoInResponse(url string, body []byte, contentType string) {
	bodyStr := string(body)

	// WeChat video URL patterns
	videoPatterns := []string{
		`"url"\s*:\s*"(https?://[^"]*finder\.video\.qq\.com[^"]*)"`,
		`"url"\s*:\s*"(https?://[^"]*findermp\.video\.qq\.com[^"]*)"`,
		`"url"\s*:\s*"(https?://[^"]*\.video\.qq\.com[^"]*\.mp4[^"]*)"`,
	}

	for _, pattern := range videoPatterns {
		re := regexp.MustCompile(pattern)
		matches := re.FindAllStringSubmatch(bodyStr, -1)
		for _, match := range matches {
			if len(match) > 1 {
				videoURL := match[1]
				// Check for duplicate URLs
				if ps.addVideoURL(videoURL) {
					// Extract more info if available
					info := ps.extractVideoInfo(bodyStr, videoURL)
					if ps.onVideoDetected != nil {
						ps.onVideoDetected(info)
					}
				}
			}
		}
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

// extractVideoInfo extracts video metadata from JSON response
func (ps *ProxyServer) extractVideoInfo(jsonStr, videoURL string) VideoInfo {
	info := VideoInfo{
		ID:        fmt.Sprintf("%d", time.Now().UnixNano()),
		URL:       videoURL,
		Source:    "wechat",
		Timestamp: time.Now().Unix(),
	}

	// Try to extract title
	titlePattern := regexp.MustCompile(`"title"\s*:\s*"([^"]*)"`)
	if matches := titlePattern.FindStringSubmatch(jsonStr); len(matches) > 1 {
		info.Title = matches[1]
	}

	// Try to extract cover
	coverPattern := regexp.MustCompile(`"thumbUrl"\s*:\s*"([^"]*)"`)
	if matches := coverPattern.FindStringSubmatch(jsonStr); len(matches) > 1 {
		info.Cover = matches[1]
	}
	if info.Cover == "" {
		coverPattern = regexp.MustCompile(`"coverUrl"\s*:\s*"([^"]*)"`)
		if matches := coverPattern.FindStringSubmatch(jsonStr); len(matches) > 1 {
			info.Cover = matches[1]
		}
	}

	// Try to extract author
	authorPattern := regexp.MustCompile(`"nickname"\s*:\s*"([^"]*)"`)
	if matches := authorPattern.FindStringSubmatch(jsonStr); len(matches) > 1 {
		info.Author = matches[1]
	}

	return info
}
