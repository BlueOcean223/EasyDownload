package api

import (
	"EasyDownload/internal/detection"
	"EasyDownload/internal/detection/wechatadapter"
	"EasyDownload/internal/download/wechat"
	"EasyDownload/internal/infra/logger"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// ProxyVideoSpec is the legacy browser callback representation. It is a
// boundary DTO; detection.Video is the only detected-media domain model.
type ProxyVideoSpec struct {
	FileFormat string `json:"fileFormat"`
	Width      int    `json:"width"`
	Height     int    `json:"height"`
	DurationMs int    `json:"durationMs"`
}

// ProxyDetectedVideoRequest is the input accepted from existing injected
// scripts. It is converted immediately at the API boundary.
type ProxyDetectedVideoRequest struct {
	ID           string           `json:"id"`
	PageKey      string           `json:"pageKey"`
	PageURL      string           `json:"pageUrl"`
	Href         string           `json:"href"`
	Title        string           `json:"title"`
	Cover        string           `json:"cover"`
	CoverURL     string           `json:"coverUrl"`
	URL          string           `json:"url"`
	Source       string           `json:"source"`
	Quality      string           `json:"quality"`
	Duration     int              `json:"duration"`
	Author       string           `json:"author"`
	AuthorAvatar string           `json:"authorAvatar"`
	Timestamp    int64            `json:"timestamp"`
	DecodeKey    string           `json:"decodeKey"`
	FileSize     float64          `json:"fileSize"`
	Width        int              `json:"width"`
	Height       int              `json:"height"`
	IsCurrent    bool             `json:"isCurrentVideo"`
	FileFormats  []string         `json:"fileFormats"`
	Specs        []ProxyVideoSpec `json:"specs"`
}

// InternalAPI provides an HTTP API for receiving data from injected scripts
type InternalAPI struct {
	server *http.Server
	port   int
	token  string
	mu     sync.RWMutex

	detectionStore detection.Store

	// Callback for new videos
	onVideoDetected func(detection.Change)

	// Image proxy handler
	imageProxy *ImageProxyHandler
}

// NewInternalAPI creates a new internal API server

func NewInternalAPI(port int, stores ...detection.Store) *InternalAPI {
	var store detection.Store
	if len(stores) > 0 {
		store = stores[0]
	}
	if store == nil {
		store = detection.NewMemoryStore(100)
	}
	return &InternalAPI{
		port:           port,
		token:          generateAPIToken(),
		detectionStore: store,
		imageProxy:     NewImageProxyHandler(),
	}
}

func generateAPIToken() string {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err == nil {
		return hex.EncodeToString(buf)
	}
	// Extremely unlikely fallback; still avoids a hard-coded token.
	return fmt.Sprintf("fallback-%d", time.Now().UnixNano())
}

// GetToken returns the per-process token required by browser-facing API routes.
func (api *InternalAPI) GetToken() string {
	api.mu.RLock()
	defer api.mu.RUnlock()
	return api.token
}

func (api *InternalAPI) isAuthorized(r *http.Request) bool {
	token := api.GetToken()
	if token == "" {
		return true
	}
	provided := r.Header.Get("X-EasyDownload-Token")
	if provided == "" {
		provided = r.URL.Query().Get("token")
	}
	if provided == "" || len(provided) != len(token) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(provided), []byte(token)) == 1
}

func (api *InternalAPI) authHandler(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !api.isAuthorized(r) {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		h(w, r)
	}
}

func (api *InternalAPI) isAllowedOrigin(origin string) bool {
	origin = strings.TrimSpace(origin)
	if origin == "" {
		return false
	}
	if origin == "null" {
		return false
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	scheme := strings.ToLower(u.Scheme)
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if scheme == "wails" && (host == "wails.localhost" || host == "localhost") {
		return true
	}
	if scheme == "http" || scheme == "https" {
		if host == "localhost" || host == "127.0.0.1" || host == "::1" || host == "wails.localhost" || strings.HasSuffix(host, ".localhost") {
			return true
		}
		// Legacy injected scripts post detections from WeChat pages.
		if host == "channels.weixin.qq.com" {
			return true
		}
	}
	return false
}

// SetVideoCallback sets the callback for new detected videos
func (api *InternalAPI) SetVideoCallback(callback func(detection.Change)) {
	api.mu.Lock()
	api.onVideoDetected = callback
	api.mu.Unlock()
}

func (api *InternalAPI) videoCallback() func(detection.Change) {
	api.mu.RLock()
	callback := api.onVideoDetected
	api.mu.RUnlock()
	return callback
}

// Ingest is the common adapter entry point used by both the HTTP callback and
// in-process proxy callbacks.
func (api *InternalAPI) Ingest(ctx context.Context, video detection.Video) (detection.Change, error) {
	change, err := api.detectionStore.Upsert(ctx, video)
	if err != nil {
		return detection.Change{}, err
	}
	if callback := api.videoCallback(); callback != nil {
		callback(change)
	}
	return change, nil
}

// Start starts the internal API server
func (api *InternalAPI) Start() error {
	api.mu.Lock()
	defer api.mu.Unlock()

	mux := http.NewServeMux()

	// CORS middleware. Do not use '*' here: only the desktop frontend,
	// localhost development server and the known WeChat origin are allowed.
	corsHandler := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Add("Vary", "Origin")
			if origin := r.Header.Get("Origin"); api.isAllowedOrigin(origin) {
				w.Header().Set("Access-Control-Allow-Origin", origin)
			}
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Range, X-EasyDownload-Token")
			w.Header().Set("Access-Control-Expose-Headers", "Content-Length, Content-Range, Accept-Ranges")

			if r.Method == "OPTIONS" {
				w.WriteHeader(http.StatusOK)
				return
			}

			h(w, r)
		}
	}
	secure := func(h http.HandlerFunc) http.HandlerFunc {
		return corsHandler(api.authHandler(h))
	}

	// Routes
	mux.HandleFunc("/api/detect", secure(api.handleDetect))
	mux.HandleFunc("/api/videos", secure(api.handleGetVideos))
	mux.HandleFunc("/api/clear", secure(api.handleClear))
	mux.HandleFunc("/api/health", corsHandler(api.handleHealth))
	mux.HandleFunc("/api/proxy-image", secure(api.handleProxyImage))
	mux.HandleFunc("/api/proxy-media", secure(api.handleProxyMedia))

	api.server = &http.Server{
		Addr:    fmt.Sprintf("127.0.0.1:%d", api.port),
		Handler: mux,
	}

	go func() {
		logger.Debug("Internal API server started on 127.0.0.1:%d", api.port)
		if err := api.server.ListenAndServe(); err != http.ErrServerClosed {
			logger.Error("Internal API server error: %v", err)
		}
	}()

	return nil
}

// Stop stops the internal API server
func (api *InternalAPI) Stop() error {
	api.mu.Lock()
	defer api.mu.Unlock()

	if api.server != nil {
		return api.server.Close()
	}
	return nil
}

// GetPort returns the API server port
func (api *InternalAPI) GetPort() int {
	return api.port
}

// handleDetect handles POST /api/detect - receives detected videos from injected script
func (api *InternalAPI) handleDetect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var request ProxyDetectedVideoRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}
	video := request.toDomain(time.Now().UTC())
	if _, err := api.Ingest(r.Context(), video); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	logger.Debug("Detected video upserted: %s", video.Title)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// handleGetVideos handles GET /api/videos - returns all detected videos
func (api *InternalAPI) handleGetVideos(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	snapshot, err := api.detectionStore.List(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(snapshot.Public())
}

// handleClear handles POST /api/clear - clears all detected videos
func (api *InternalAPI) handleClear(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if _, err := api.clearVideos(r.Context(), true); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// handleHealth handles GET /api/health - health check
func (api *InternalAPI) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "healthy"})
}

// handleProxyImage handles GET /api/proxy-image - proxies external images
func (api *InternalAPI) handleProxyImage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	imageURL := r.URL.Query().Get("url")
	if imageURL == "" {
		http.Error(w, "Missing url parameter", http.StatusBadRequest)
		return
	}

	data, contentType, err := api.imageProxy.ProxyImage(imageURL)
	if err != nil {
		logger.Debug("Image proxy error: %v", err)
		http.Error(w, "Failed to fetch image", http.StatusBadGateway)
		return
	}

	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "public, max-age=86400") // Cache for 24 hours
	w.Write(data)
}

// allowedMediaDomains contains domains allowed for media proxy.
// This whitelist prevents SSRF attacks by restricting proxy to known Douyin CDN domains.
var allowedMediaDomains = []string{
	"aweme.snssdk.com",
	"v.douyin.com",
	"douyinvod.com",
	"bytecdntp.com",
	"bytecdn.cn",
	"douyincdn.com",
	"ixigua.com",
	"pstatp.com",
	"snssdk.com",
	"toutiaovod.com",
	"zjcdn.com",
	"amemv.com",
}

// isAllowedMediaDomain checks if the given URL's host is in the allowed domains list.
func isAllowedMediaDomain(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil || u.Hostname() == "" {
		return false
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return false
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	for _, domain := range allowedMediaDomains {
		if host == domain || strings.HasSuffix(host, "."+domain) {
			return true
		}
	}
	return false
}

// handleProxyMedia handles GET /api/proxy-media - proxies external media (videos)
// Supports Range requests for video streaming
func (api *InternalAPI) handleProxyMedia(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	mediaURL := r.URL.Query().Get("url")
	if mediaURL == "" {
		http.Error(w, "Missing url parameter", http.StatusBadRequest)
		return
	}

	// Validate domain to prevent SSRF
	if !isAllowedMediaDomain(mediaURL) {
		logger.Debug("Media proxy blocked: domain not allowed: %s", mediaURL)
		http.Error(w, "Domain not allowed", http.StatusForbidden)
		return
	}

	// Create request to fetch media
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, mediaURL, nil)
	if err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}

	// Forward Range header for video seeking support
	if rng := r.Header.Get("Range"); rng != "" {
		req.Header.Set("Range", rng)
	}

	// Set appropriate headers for Douyin
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Referer", "https://www.douyin.com/")
	req.Header.Set("Origin", "https://www.douyin.com")

	// Use a client with no timeout for streaming
	client := &http.Client{Timeout: 0}
	resp, err := client.Do(req)
	if err != nil {
		logger.Debug("Media proxy error: %v", err)
		http.Error(w, "Failed to fetch media", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	// Forward response headers
	for _, k := range []string{"Content-Type", "Content-Length", "Content-Range", "Accept-Ranges"} {
		if v := resp.Header.Get(k); v != "" {
			w.Header().Set(k, v)
		}
	}
	w.Header().Set("Access-Control-Expose-Headers", "Content-Length, Content-Range, Accept-Ranges")
	w.WriteHeader(resp.StatusCode)

	// Stream the response
	buf := make([]byte, 32*1024) // 32KB buffer
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			w.Write(buf[:n])
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
		if err != nil {
			break
		}
	}
}

// getDetectedVideos returns the private domain list for package-local tests.
// Public callers must use GetDetectionSnapshot.
func (api *InternalAPI) getDetectedVideos() []detection.Video {
	snapshot, err := api.detectionStore.List(context.Background())
	if err != nil {
		logger.Warn("Failed to list detected videos: %v", err)
		return []detection.Video{}
	}
	return snapshot.Videos
}

// GetDetectionSnapshot returns the secret-free authoritative projection used
// by Wails clients. Private candidate data never crosses this boundary.
func (api *InternalAPI) GetDetectionSnapshot() detection.PublicSnapshot {
	snapshot, err := api.detectionStore.List(context.Background())
	if err != nil {
		logger.Warn("Failed to list detected videos: %v", err)
		return detection.PublicSnapshot{Videos: []detection.VideoDTO{}}
	}
	return snapshot.Public()
}

// ClearVideos clears all detected videos and returns the authoritative public
// change so callers do not need to make speculative local edits.
func (api *InternalAPI) ClearVideos() (detection.PublicChange, error) {
	change, err := api.clearVideos(context.Background(), true)
	if err != nil {
		return detection.PublicChange{}, err
	}
	return change.Public(), nil
}

// RemoveVideo removes a video by ID
func (api *InternalAPI) RemoveVideo(id string) (detection.PublicChange, error) {
	change, err := api.detectionStore.Remove(context.Background(), id)
	if err != nil {
		return detection.PublicChange{}, err
	}
	if callback := api.videoCallback(); callback != nil {
		callback(change)
	}
	return change.Public(), nil
}

func (api *InternalAPI) clearVideos(ctx context.Context, publish bool) (detection.Change, error) {
	change, err := api.detectionStore.Clear(ctx)
	if callback := api.videoCallback(); err == nil && publish && callback != nil {
		callback(change)
	}
	return change, err
}

func (request ProxyDetectedVideoRequest) toDomain(now time.Time) detection.Video {
	platform := strings.ToLower(strings.TrimSpace(request.Source))
	if platform == "" {
		platform = "wechat"
	}
	source := detection.Source(platform + "_proxy")
	if platform == "wechat" {
		coverURL := request.CoverURL
		if coverURL == "" {
			coverURL = request.Cover
		}
		pageURL := strings.TrimSpace(request.PageURL)
		if pageURL == "" {
			pageURL = strings.TrimSpace(request.Href)
		}
		specs := make([]wechat.VideoSpec, 0, len(request.Specs))
		for _, spec := range request.Specs {
			specs = append(specs, wechat.VideoSpec{
				FileFormat: spec.FileFormat, Width: spec.Width, Height: spec.Height, DurationMs: spec.DurationMs,
			})
		}
		return wechatadapter.FromVideoInfo(wechat.VideoInfo{
			ID: request.ID, PageKey: request.PageKey, Href: pageURL, URL: request.URL,
			CoverURL: coverURL, Title: request.Title, FileSize: request.FileSize,
			DecodeKey: request.DecodeKey, FileFormats: append([]string(nil), request.FileFormats...),
			Specs: specs, Author: request.Author, AuthorAvatar: request.AuthorAvatar,
			Duration: request.Duration, Width: request.Width, Height: request.Height,
			IsCurrentVideo: request.IsCurrent, TS: request.Timestamp, Source: request.Source,
		}, now)
	}
	pageURL := strings.TrimSpace(request.PageURL)
	if pageURL == "" {
		pageURL = strings.TrimSpace(request.Href)
	}
	contentID := strings.TrimSpace(request.ID)
	if contentID == "" {
		contentID = strings.TrimSpace(request.PageKey)
	}
	seenAt := timestampTime(request.Timestamp, now)
	coverURL := request.CoverURL
	if coverURL == "" {
		coverURL = request.Cover
	}
	size := int64(0)
	if request.FileSize > 0 {
		size = int64(request.FileSize)
	}
	video := detection.Video{
		Source: source, Platform: platform, Title: request.Title, Author: request.Author,
		PageURL: pageURL, CoverURL: coverURL,
		DetectedAt: seenAt, LastSeenAt: seenAt,
		AuthorAvatar: request.AuthorAvatar,
		DurationMS:   request.Duration, Width: request.Width, Height: request.Height,
		IsCurrent: request.IsCurrent,
	}
	video.ID = detection.StableID(detection.Identity{
		Source: source, Platform: platform, PlatformContentID: contentID,
		PageURL: pageURL, PrimaryURL: request.URL,
	})
	if request.URL != "" {
		headers := map[string]string(nil)
		if platform == "wechat" {
			headers = map[string]string{"Referer": "https://channels.weixin.qq.com/"}
		}
		video.Candidates = append(video.Candidates, detection.Resource{
			ID: "original", URL: request.URL, Headers: headers, DecodeKey: request.DecodeKey,
			Quality: request.Quality, Width: request.Width, Height: request.Height,
			DurationMS: request.Duration, SizeBytes: size, Default: true,
		})
		seenFormats := make(map[string]struct{}, len(request.Specs)+len(request.FileFormats))
		for _, spec := range request.Specs {
			format := strings.TrimSpace(spec.FileFormat)
			if format == "" {
				continue
			}
			seenFormats[format] = struct{}{}
			video.Candidates = append(video.Candidates, detection.Resource{
				ID: "format:" + format, URL: request.URL, Headers: headers, DecodeKey: request.DecodeKey,
				FileFormat: format, Width: spec.Width, Height: spec.Height,
				DurationMS: spec.DurationMs,
			})
		}
		for _, rawFormat := range request.FileFormats {
			format := strings.TrimSpace(rawFormat)
			if format == "" {
				continue
			}
			if _, exists := seenFormats[format]; exists {
				continue
			}
			seenFormats[format] = struct{}{}
			video.Candidates = append(video.Candidates, detection.Resource{
				ID: "format:" + format, URL: request.URL, Headers: headers, DecodeKey: request.DecodeKey,
				FileFormat: format,
			})
		}
	}
	return video
}

func timestampTime(value int64, fallback time.Time) time.Time {
	if value <= 0 {
		return fallback
	}
	if value > 1_000_000_000_000 {
		return time.UnixMilli(value).UTC()
	}
	return time.Unix(value, 0).UTC()
}
