package proxy

import (
	"EasyDownload/internal/logger"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// WeChatVideoInfo represents video information extracted from WeChat
type WeChatVideoInfo struct {
	ID             string            `json:"id"`
	URL            string            `json:"url"` // Full URL (url + urlToken)
	CoverURL       string            `json:"coverUrl"`
	Title          string            `json:"title"` // From description field
	FileSize       float64           `json:"fileSize"`
	DecodeKey      string            `json:"decodeKey"`      // Decryption key
	MediaType      int               `json:"mediaType"`      // 9=image, others=video
	FileFormats    []string          `json:"fileFormats"`    // Available quality formats (for backward compatibility)
	Specs          []WeChatVideoSpec `json:"specs"`          // Detailed spec info for each quality
	Author         string            `json:"author"`         // Author nickname from contact
	AuthorAvatar   string            `json:"authorAvatar"`   // Author avatar URL from contact
	Duration       int               `json:"duration"`       // Video duration in milliseconds
	Width          int               `json:"width"`          // Video width
	Height         int               `json:"height"`         // Video height
	IsCurrentVideo bool              `json:"isCurrentVideo"` // Whether this is the current playing video
	// Diagnostics / context from injected JS (optional)
	PageKey string `json:"pageKey,omitempty"`
	Href    string `json:"href,omitempty"`
	TS      int64  `json:"ts,omitempty"`
	Source  string `json:"source,omitempty"`
}

// WeChatVideoSpec represents video specification for a specific quality
type WeChatVideoSpec struct {
	FileFormat string `json:"fileFormat"`
	Width      int    `json:"width"`
	Height     int    `json:"height"`
	DurationMs int    `json:"durationMs"`
}

// objectDescMedia represents a single media item in objectDesc
type objectDescMedia struct {
	URL       string           `json:"url"`
	URLToken  string           `json:"urlToken"`
	CoverURL  string           `json:"coverUrl"`
	FileSize  interface{}      `json:"fileSize"` // Can be string or number
	DecodeKey string           `json:"decodeKey"`
	MediaType int              `json:"mediaType"`
	Spec      []objectDescSpec `json:"spec"`
}

// objectDescSpec represents video specification in objectDesc
type objectDescSpec struct {
	FileFormat string `json:"fileFormat"`
	DurationMs int    `json:"durationMs"`
	Width      int    `json:"width"`
	Height     int    `json:"height"`
}

// objectDescContact represents author contact information in objectDesc
type objectDescContact struct {
	Username string `json:"username"`
	Nickname string `json:"nickname"`
	HeadURL  string `json:"head_url"`
}

// videoPayload represents the new payload format from injected JS
// This format includes contact at the top level (not nested in objectDesc)
type videoPayload struct {
	ID          string             `json:"id"`
	Media       []objectDescMedia  `json:"media"`
	Description string             `json:"description"`
	Title       string             `json:"title,omitempty"`
	DomTitle    string             `json:"domTitle,omitempty"`
	Contact     *objectDescContact `json:"contact"`
	PageKey     string             `json:"pageKey"`
	Href        string             `json:"href"`
	TS          int64              `json:"ts"`
	Source      string             `json:"source"`
}

// wxProfilePayload represents an alternative payload shape used by wx_channels_download style injectors.
// It typically contains the already-concatenated URL and decode key at the top level.
type wxProfilePayload struct {
	Type       string             `json:"type"` // "media" or "picture"
	Title      string             `json:"title"`
	CoverURL   string             `json:"coverUrl"`
	URL        string             `json:"url"`
	Size       float64            `json:"size"`
	Key        string             `json:"key"`
	ID         string             `json:"id"`
	NonceID    string             `json:"nonce_id"`
	Nickname   string             `json:"nickname"`
	Duration   int                `json:"duration"`   // ms (wx_channels_download)
	DurationMs int                `json:"durationMs"` // ms (some variants)
	Width      int                `json:"width"`
	Height     int                `json:"height"`
	Spec       []objectDescSpec   `json:"spec"`
	FileFormat []string           `json:"fileFormat"`
	Contact    *objectDescContact `json:"contact"`
}

// objectDesc represents the WeChat video objectDesc structure (legacy format)
type objectDesc struct {
	Media       []objectDescMedia  `json:"media"`
	Description string             `json:"description"`
	Contact     *objectDescContact `json:"contact"`
}

// WeChatHandler handles WeChat video-related requests
type WeChatHandler struct {
	detectedURLs      sync.Map
	onVideoDetected   func(WeChatVideoInfo)
	onDownloadRequest func(WeChatVideoInfo)
	recentVideos      sync.Map

	// Current video tracking - only keeps the current video
	currentVideo   *WeChatVideoInfo
	currentVideoMu sync.RWMutex

	// avoid probing the same URL repeatedly
	probedSizes sync.Map
}

type recentVideoCache struct {
	Video WeChatVideoInfo
	Ts    time.Time
}

var badDurationPattern = regexp.MustCompile(`\d{1,2}:\d{2}\s*/\s*\d{1,2}:\d{2}`)

// NewWeChatHandler creates a new WeChatHandler instance
func NewWeChatHandler() *WeChatHandler {
	return &WeChatHandler{}
}

// SetVideoCallback sets the callback function for detected videos
func (wh *WeChatHandler) SetVideoCallback(callback func(WeChatVideoInfo)) {
	wh.onVideoDetected = callback
}

// SetDownloadCallback sets the callback function for download requests
func (wh *WeChatHandler) SetDownloadCallback(callback func(WeChatVideoInfo)) {
	wh.onDownloadRequest = callback
}

// GetCurrentVideo returns the current video info
func (wh *WeChatHandler) GetCurrentVideo() *WeChatVideoInfo {
	wh.currentVideoMu.RLock()
	defer wh.currentVideoMu.RUnlock()
	return wh.currentVideo
}

// HandleWeChatRequest handles POST requests to /res-downloader/wechat
// Supports both type=1/2 (video detection) and type=download (trigger download)
func (wh *WeChatHandler) HandleWeChatRequest(body []byte) error {
	return wh.HandleWeChatRequestWithType(body, "1")
}

// HandleWeChatRequestWithType handles POST requests with specific type
func (wh *WeChatHandler) HandleWeChatRequestWithType(body []byte, reqType string) error {
	// Handle download request
	if reqType == "download" {
		return wh.handleDownloadRequest(body)
	}

	// Handle video detection
	videoInfo, err := wh.ParseVideoPayload(body)
	if err != nil {
		return err
	}
	sanitizeWeChatVideo(videoInfo)
	if wh.shouldSkipDuplicate(videoInfo) {
		logger.Debug("WeChat duplicate skipped: %s", videoInfo.Title)
		return nil
	}

	// Update current video (replace, not accumulate)
	wh.updateCurrentVideo(videoInfo)

	// Notify callback if set
	if wh.onVideoDetected != nil {
		wh.onVideoDetected(*videoInfo)
	}

	// FileSize from worker messages is often chunk-sized. Probe total size via HTTP Range if needed.
	wh.maybeProbeAndUpdateFileSize(videoInfo)

	logger.Debug("WeChat video detected: id=%q title=%q author=%q stable=%q url=%q pageKey=%q source=%q href=%q ts=%d",
		shortenForLog(videoInfo.ID, 120),
		shortenForLog(videoInfo.Title, 160),
		shortenForLog(videoInfo.Author, 80),
		shortenForLog(extractStableURLParams(videoInfo.URL), 220),
		shortenForLog(videoInfo.URL, 220),
		shortenForLog(videoInfo.PageKey, 120),
		shortenForLog(videoInfo.Source, 40),
		shortenForLog(videoInfo.Href, 200),
		videoInfo.TS,
	)
	return nil
}

func (wh *WeChatHandler) maybeProbeAndUpdateFileSize(videoInfo *WeChatVideoInfo) {
	if videoInfo == nil || videoInfo.URL == "" {
		return
	}

	lu := strings.ToLower(strings.TrimSpace(videoInfo.URL))
	if !strings.Contains(lu, "finder.video.qq.com") && !strings.Contains(lu, "findermp.video.qq.com") {
		return
	}

	// Ensure we only probe each video once per session (use stable key, not volatile URL tokens).
	probeKey := canonicalKeyForVideo(*videoInfo)
	if strings.TrimSpace(probeKey) == "" {
		probeKey = videoInfo.URL
	}
	if _, loaded := wh.probedSizes.LoadOrStore(probeKey, struct{}{}); loaded {
		return
	}

	urlToProbe := videoInfo.URL
	base := *videoInfo
	go func() {
		size, err := probeContentLengthByRange(urlToProbe)
		if err != nil || size <= 0 {
			// Allow retry on later events if the probe failed (URL token may have expired).
			wh.probedSizes.Delete(probeKey)
			return
		}

		// Update current video if still the same URL.
		wh.currentVideoMu.Lock()
		if wh.currentVideo != nil && wh.currentVideo.URL == urlToProbe {
			old := wh.currentVideo.FileSize
			wh.currentVideo.FileSize = float64(size)
			base.FileSize = float64(size)
			base.IsCurrentVideo = true
			if old <= 0 || float64(size) > old*1.2 {
				logger.Debug("[WeChat Probe] FileSize updated: old=%.0f new=%d stable=%q url=%q",
					old,
					size,
					shortenForLog(extractStableURLParams(urlToProbe), 220),
					shortenForLog(urlToProbe, 160),
				)
			}
		} else {
			wh.currentVideoMu.Unlock()
			return
		}
		wh.currentVideoMu.Unlock()

		// Notify UI with updated size
		if wh.onVideoDetected != nil {
			wh.onVideoDetected(base)
		}
	}()
}

func probeContentLengthByRange(rawURL string) (int64, error) {
	client := &http.Client{
		Timeout: 6 * time.Second,
	}
	req, err := http.NewRequest("GET", rawURL, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Range", "bytes=0-0")
	req.Header.Set("Accept-Encoding", "identity")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120 Safari/537.36")
	req.Header.Set("Referer", "https://channels.weixin.qq.com/")

	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		return 0, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	// Prefer Content-Range: bytes 0-0/12345
	cr := resp.Header.Get("Content-Range")
	if cr != "" {
		slash := strings.LastIndex(cr, "/")
		if slash != -1 && slash+1 < len(cr) {
			totalStr := strings.TrimSpace(cr[slash+1:])
			if totalStr != "*" && totalStr != "" {
				if n, e := strconv.ParseInt(totalStr, 10, 64); e == nil && n > 0 {
					return n, nil
				}
			}
		}
	}

	// Fallback to Content-Length (may be full length or 1 byte depending on server behavior)
	cl := resp.Header.Get("Content-Length")
	if cl != "" {
		if n, e := strconv.ParseInt(strings.TrimSpace(cl), 10, 64); e == nil && n > 0 {
			// If server honored range, Content-Length could be 1; ignore that.
			if n > 1 {
				return n, nil
			}
		}
	}

	return 0, fmt.Errorf("unable to probe content length")
}

// handleDownloadRequest handles download button click
func (wh *WeChatHandler) handleDownloadRequest(body []byte) error {
	videoInfo, err := wh.ParseVideoPayload(body)
	if err != nil {
		// Try to use current video if parsing fails
		wh.currentVideoMu.RLock()
		current := wh.currentVideo
		wh.currentVideoMu.RUnlock()

		if current != nil {
			logger.Warn("[WeChat Download] Payload parse failed, falling back to current video: %v", err)
			videoInfo = current
		} else {
			return err
		}
	}
	sanitizeWeChatVideo(videoInfo)

	key := extractStableURLParams(videoInfo.URL)
	logger.Debug("[WeChat Download] Requested id=%q title=%q author=%q stable=%q url=%q pageKey=%q source=%q href=%q ts=%d",
		shortenForLog(videoInfo.ID, 120),
		shortenForLog(videoInfo.Title, 160),
		shortenForLog(videoInfo.Author, 80),
		shortenForLog(key, 220),
		shortenForLog(videoInfo.URL, 220),
		shortenForLog(videoInfo.PageKey, 120),
		shortenForLog(videoInfo.Source, 40),
		shortenForLog(videoInfo.Href, 200),
		videoInfo.TS,
	)
	wh.currentVideoMu.RLock()
	cur := wh.currentVideo
	wh.currentVideoMu.RUnlock()
	if cur != nil && strings.TrimSpace(cur.ID) != "" && strings.TrimSpace(videoInfo.ID) != "" && strings.TrimSpace(cur.ID) != strings.TrimSpace(videoInfo.ID) {
		logger.Warn("[WeChat Download] Mismatch vs current: currentId=%q currentTitle=%q currentStable=%q currentPageKey=%q currentSource=%q currentHref=%q",
			shortenForLog(cur.ID, 120),
			shortenForLog(cur.Title, 160),
			shortenForLog(extractStableURLParams(cur.URL), 220),
			shortenForLog(cur.PageKey, 120),
			shortenForLog(cur.Source, 40),
			shortenForLog(cur.Href, 200),
		)
	}

	if wh.onDownloadRequest != nil {
		wh.onDownloadRequest(*videoInfo)
	}

	return nil
}

// updateCurrentVideo updates the current video (replaces previous one)
func (wh *WeChatHandler) updateCurrentVideo(video *WeChatVideoInfo) {
	wh.currentVideoMu.Lock()
	defer wh.currentVideoMu.Unlock()

	// Mark as current video
	video.IsCurrentVideo = true
	wh.currentVideo = video
}

// ParseVideoPayload parses the new payload format from injected JS
func (wh *WeChatHandler) ParseVideoPayload(data []byte) (*WeChatVideoInfo, error) {
	var payload videoPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		// Try wx_channels_download profile shape
		if info, pErr := wh.ParseWxProfile(data); pErr == nil {
			return info, nil
		}
		// Try legacy format
		return wh.ParseObjectDesc(data)
	}

	// Check if media array is empty (may indicate profile payload shape)
	if len(payload.Media) == 0 {
		if info, pErr := wh.ParseWxProfile(data); pErr == nil {
			return info, nil
		}
		return nil, fmt.Errorf("media array is empty")
	}

	// Get the first media item
	media := payload.Media[0]

	// Build full URL
	fullURL := BuildFullURL(media.URL, media.URLToken)
	if !IsValidVideoURL(fullURL) {
		return nil, fmt.Errorf("invalid video URL: %s", fullURL)
	}
	if IsLiveStreamURL(fullURL) {
		return nil, fmt.Errorf("live/stream url ignored: %s", fullURL)
	}

	// Parse file size (can be string or number)
	var fileSize float64
	switch v := media.FileSize.(type) {
	case float64:
		fileSize = v
	case string:
		fmt.Sscanf(v, "%f", &fileSize)
	}

	// Extract file formats and video metadata from spec
	var formats []string
	var specs []WeChatVideoSpec
	var duration, width, height int
	for _, spec := range media.Spec {
		if spec.FileFormat != "" {
			formats = append(formats, spec.FileFormat)
			specs = append(specs, WeChatVideoSpec{
				FileFormat: spec.FileFormat,
				Width:      spec.Width,
				Height:     spec.Height,
				DurationMs: spec.DurationMs,
			})
		}
		if duration == 0 && spec.DurationMs > 0 {
			duration = spec.DurationMs
		}
		if width == 0 && spec.Width > 0 {
			width = spec.Width
		}
		if height == 0 && spec.Height > 0 {
			height = spec.Height
		}
	}

	// Build video info with default values
	titleFrom := "description"
	title := strings.TrimSpace(payload.Description)
	if title == "" {
		title = strings.TrimSpace(payload.Title)
		if title != "" {
			titleFrom = "title"
		}
	}
	if title == "" {
		title = strings.TrimSpace(payload.DomTitle)
		if title != "" {
			titleFrom = "domTitle"
		}
	}
	// Keep empty titles as-is; frontend will show fallback and allow later updates.

	// Extract author info from contact (now at top level)
	author := ""
	authorAvatar := ""
	if payload.Contact != nil {
		if payload.Contact.Nickname != "" {
			author = payload.Contact.Nickname
		}
		authorAvatar = payload.Contact.HeadURL
	}

	// Stable ID: prefer injected per-video id; fall back to pageKey; then timestamp.
	// This keeps history while still allowing meta refresh updates for the same video.
	id := strings.TrimSpace(payload.ID)
	if id == "" {
		id = strings.TrimSpace(payload.PageKey)
	}
	if id == "" {
		id = fmt.Sprintf("%d", time.Now().UnixNano())
	}

	if title != "" && titleFrom != "description" {
		logger.Debug("[WeChat Meta] Title fallback used: from=%s title=%q id=%q source=%q",
			titleFrom,
			shortenForLog(title, 120),
			shortenForLog(payload.ID, 120),
			shortenForLog(payload.Source, 40),
		)
	}

	return &WeChatVideoInfo{
		ID:           id,
		URL:          fullURL,
		CoverURL:     media.CoverURL,
		Title:        title,
		FileSize:     fileSize,
		DecodeKey:    media.DecodeKey,
		MediaType:    media.MediaType,
		FileFormats:  formats,
		Specs:        specs,
		Author:       author,
		AuthorAvatar: authorAvatar,
		Duration:     duration,
		Width:        width,
		Height:       height,
		PageKey:      strings.TrimSpace(payload.PageKey),
		Href:         strings.TrimSpace(payload.Href),
		TS:           payload.TS,
		Source:       strings.TrimSpace(payload.Source),
	}, nil
}

// ParseWxProfile parses the wx_channels_download-like profile payload.
func (wh *WeChatHandler) ParseWxProfile(data []byte) (*WeChatVideoInfo, error) {
	var p wxProfilePayload
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("failed to parse profile JSON: %w", err)
	}
	if strings.TrimSpace(p.URL) == "" {
		return nil, fmt.Errorf("profile url is empty")
	}
	if !IsValidVideoURL(p.URL) {
		return nil, fmt.Errorf("invalid profile url: %s", p.URL)
	}
	if IsLiveStreamURL(p.URL) {
		return nil, fmt.Errorf("live/stream url ignored: %s", p.URL)
	}

	// Title
	title := strings.TrimSpace(p.Title)
	// Keep empty titles as-is; frontend will show fallback and allow later updates.

	// File formats and specs
	formats := make([]string, 0)
	specs := make([]WeChatVideoSpec, 0)
	if len(p.FileFormat) > 0 {
		formats = append(formats, p.FileFormat...)
		// No detailed spec info available in this format
	}
	if len(p.Spec) > 0 {
		for _, sp := range p.Spec {
			if sp.FileFormat != "" {
				if len(formats) == 0 || !contains(formats, sp.FileFormat) {
					formats = append(formats, sp.FileFormat)
				}
				specs = append(specs, WeChatVideoSpec{
					FileFormat: sp.FileFormat,
					Width:      sp.Width,
					Height:     sp.Height,
					DurationMs: sp.DurationMs,
				})
			}
		}
	}

	// Duration/size/wh
	duration := p.Duration
	if duration == 0 {
		duration = p.DurationMs
	}
	width := p.Width
	height := p.Height
	if len(p.Spec) > 0 {
		if duration == 0 && p.Spec[0].DurationMs > 0 {
			duration = p.Spec[0].DurationMs
		}
		if width == 0 && p.Spec[0].Width > 0 {
			width = p.Spec[0].Width
		}
		if height == 0 && p.Spec[0].Height > 0 {
			height = p.Spec[0].Height
		}
	}

	// Author info
	author := ""
	authorAvatar := ""
	if p.Contact != nil {
		if strings.TrimSpace(p.Contact.Nickname) != "" {
			author = p.Contact.Nickname
		}
		authorAvatar = p.Contact.HeadURL
	} else if strings.TrimSpace(p.Nickname) != "" {
		author = p.Nickname
	}

	// ID: prefer payload IDs
	id := strings.TrimSpace(p.ID)
	if id == "" {
		id = strings.TrimSpace(p.NonceID)
	}
	if id == "" {
		id = fmt.Sprintf("%d", time.Now().UnixNano())
	}

	mediaType := 4
	if strings.TrimSpace(p.Type) == "picture" {
		mediaType = 9
	}

	return &WeChatVideoInfo{
		ID:           id,
		URL:          p.URL,
		CoverURL:     p.CoverURL,
		Title:        title,
		FileSize:     p.Size,
		DecodeKey:    strings.TrimSpace(p.Key),
		MediaType:    mediaType,
		FileFormats:  formats,
		Specs:        specs,
		Author:       author,
		AuthorAvatar: authorAvatar,
		Duration:     duration,
		Width:        width,
		Height:       height,
	}, nil
}

// ParseObjectDesc parses objectDesc JSON data and extracts video information (legacy format)
func (wh *WeChatHandler) ParseObjectDesc(data []byte) (*WeChatVideoInfo, error) {
	var desc objectDesc
	if err := json.Unmarshal(data, &desc); err != nil {
		return nil, fmt.Errorf("failed to parse objectDesc JSON: %w", err)
	}

	// Check if media array is empty
	if len(desc.Media) == 0 {
		return nil, fmt.Errorf("media array is empty")
	}

	// Get the first media item
	media := desc.Media[0]

	// Build full URL
	fullURL := BuildFullURL(media.URL, media.URLToken)
	if !IsValidVideoURL(fullURL) {
		return nil, fmt.Errorf("invalid video URL: %s", fullURL)
	}
	if IsLiveStreamURL(fullURL) {
		return nil, fmt.Errorf("live/stream url ignored: %s", fullURL)
	}

	// Parse file size (can be string or number)
	var fileSize float64
	switch v := media.FileSize.(type) {
	case float64:
		fileSize = v
	case string:
		fmt.Sscanf(v, "%f", &fileSize)
	}

	// Extract file formats and video metadata from spec
	var formats []string
	var specs []WeChatVideoSpec
	var duration, width, height int
	for _, spec := range media.Spec {
		if spec.FileFormat != "" {
			formats = append(formats, spec.FileFormat)
			specs = append(specs, WeChatVideoSpec{
				FileFormat: spec.FileFormat,
				Width:      spec.Width,
				Height:     spec.Height,
				DurationMs: spec.DurationMs,
			})
		}
		// Use the first spec's metadata (they should be the same across specs)
		if duration == 0 && spec.DurationMs > 0 {
			duration = spec.DurationMs
		}
		if width == 0 && spec.Width > 0 {
			width = spec.Width
		}
		if height == 0 && spec.Height > 0 {
			height = spec.Height
		}
	}

	// Build video info with default values
	title := desc.Description
	// Keep empty titles as-is; frontend will show fallback and allow later updates.

	// Extract author info with default value
	author := ""
	authorAvatar := ""
	if desc.Contact != nil {
		if desc.Contact.Nickname != "" {
			author = desc.Contact.Nickname
		}
		authorAvatar = desc.Contact.HeadURL
	}

	return &WeChatVideoInfo{
		ID:           fmt.Sprintf("%d", time.Now().UnixNano()),
		URL:          fullURL,
		CoverURL:     media.CoverURL,
		Title:        title,
		FileSize:     fileSize,
		DecodeKey:    media.DecodeKey,
		MediaType:    media.MediaType,
		FileFormats:  formats,
		Specs:        specs,
		Author:       author,
		AuthorAvatar: authorAvatar,
		Duration:     duration,
		Width:        width,
		Height:       height,
	}, nil
}

// contains checks if a string slice contains a specific string
func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}

// BuildFullURL concatenates url and urlToken to create the full video URL
func BuildFullURL(baseURL, urlToken string) string {
	if baseURL == "" {
		return ""
	}
	if urlToken == "" {
		return baseURL
	}
	// urlToken typically starts with & or ?, handle both cases
	if strings.HasPrefix(urlToken, "&") || strings.HasPrefix(urlToken, "?") {
		// Check if baseURL already has query params
		if strings.Contains(baseURL, "?") {
			if strings.HasPrefix(urlToken, "?") {
				return baseURL + "&" + urlToken[1:]
			}
			return baseURL + urlToken
		}
		// No query params in baseURL
		if strings.HasPrefix(urlToken, "&") {
			return baseURL + "?" + urlToken[1:]
		}
		return baseURL + urlToken
	}
	// urlToken doesn't start with & or ?, add separator
	if strings.Contains(baseURL, "?") {
		return baseURL + "&" + urlToken
	}
	return baseURL + "?" + urlToken
}

// IsValidVideoURL validates if the given URL is a valid video URL
func IsValidVideoURL(videoURL string) bool {
	if videoURL == "" {
		return false
	}

	// Parse URL to validate format
	parsed, err := url.Parse(videoURL)
	if err != nil {
		return false
	}

	// Must have scheme and host
	if parsed.Scheme == "" || parsed.Host == "" {
		return false
	}

	// Must be http or https
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return false
	}

	return true
}

// IsLiveStreamURL returns true for obvious live/stream playlist URLs that we should skip.
// WeChat Channels live pages may expose m3u8/flv/mpd streams which are not supported by this downloader.
func IsLiveStreamURL(videoURL string) bool {
	u := strings.ToLower(strings.TrimSpace(videoURL))
	if u == "" {
		return false
	}
	// strip hash
	if i := strings.Index(u, "#"); i != -1 {
		u = u[:i]
	}
	// m3u8/flv/dash manifests
	if strings.Contains(u, ".m3u8") || strings.Contains(u, ".flv") || strings.Contains(u, ".mpd") {
		return true
	}
	return false
}

func sanitizeWeChatVideo(v *WeChatVideoInfo) {
	if v == nil {
		return
	}
	if isBadWeChatTitle(v.Title) {
		v.Title = ""
	}
	if isBadWeChatAuthor(v.Author) {
		v.Author = ""
	}
}

func isBadWeChatTitle(t string) bool {
	s := strings.TrimSpace(t)
	if s == "" {
		return true
	}
	low := strings.ToLower(s)
	if strings.Contains(low, "this is a modal window") {
		return true
	}
	if strings.Contains(low, "modal window") && strings.Contains(low, "this is") {
		return true
	}
	if strings.Contains(low, "restore all settings to the default values") {
		return true
	}
	if strings.Contains(low, "video player is loading") {
		return true
	}
	// Player UI option text sometimes leaks into DOM capture
	if strings.Contains(low, "transparency") && strings.Contains(low, "opaque") {
		return true
	}
	if low == "transparency" || low == "opaque" || strings.Contains(low, "semi-transparent") || strings.Contains(low, "semi transparent") {
		return true
	}
	if strings.Contains(low, "自动续播") || strings.Contains(low, "小窗模式") || strings.Contains(low, "倍速") {
		return true
	}
	if badDurationPattern.MatchString(low) {
		return true
	}
	return false
}

func isBadWeChatAuthor(a string) bool {
	s := strings.TrimSpace(a)
	if s == "" {
		return true
	}
	low := strings.ToLower(s)
	if strings.Contains(low, "video player is loading") {
		return true
	}
	if strings.Contains(low, "restore all settings to the default values") {
		return true
	}
	if strings.Contains(low, "个朋友") || strings.Contains(low, "朋友") {
		return true
	}
	return false
}

// extractStableURLParams extracts stable parameters from WeChat video URL for deduplication.
// Based on url.md analysis: encfilekey and m are stable across sessions.
// Volatile params (token, sign, svrbypass, svrnonce) change on each access.
func extractStableURLParams(videoURL string) string {
	videoURL = strings.TrimSpace(videoURL)
	if videoURL == "" {
		return ""
	}

	parsed, err := url.Parse(videoURL)
	if err != nil {
		return videoURL
	}

	host := strings.ToLower(parsed.Host)
	// Only process WeChat finder download hosts
	if !strings.Contains(host, "finder.video.qq.com") && !strings.Contains(host, "findermp.video.qq.com") {
		return videoURL
	}

	// Extract only the most stable params: encfilekey and m
	encfilekey := parsed.Query().Get("encfilekey")
	m := parsed.Query().Get("m")

	// If we have encfilekey, use it as primary identifier (most stable)
	if encfilekey != "" {
		result := "encfilekey:" + encfilekey
		if m != "" {
			result += ":m:" + m
		}
		return result
	}

	// Fallback: use pathname + m if available
	if m != "" {
		return parsed.Path + ":m:" + m
	}

	return parsed.Scheme + "://" + parsed.Host + parsed.Path
}

func canonicalKeyForVideo(v WeChatVideoInfo) string {
	// Priority 1: Use video ID (most stable, from WeChat API)
	if strings.TrimSpace(v.ID) != "" {
		return strings.TrimSpace(v.ID)
	}

	// Priority 2: Extract stable URL params
	// NOTE: Do NOT include DecodeKey - it changes across sessions!
	// Based on url.md analysis: decodeKey values change for the SAME video
	if strings.TrimSpace(v.URL) != "" {
		return extractStableURLParams(v.URL)
	}

	return ""
}

func hasServerImprovement(prev, next WeChatVideoInfo) bool {
	pt := strings.TrimSpace(prev.Title)
	nt := strings.TrimSpace(next.Title)
	if !isBadWeChatTitle(nt) {
		if isBadWeChatTitle(pt) && nt != "" {
			return true
		}
		if len(nt) > len(pt) && pt != nt {
			return true
		}
	}
	if strings.TrimSpace(prev.CoverURL) == "" && strings.TrimSpace(next.CoverURL) != "" {
		return true
	}
	if isBadWeChatAuthor(prev.Author) && !isBadWeChatAuthor(next.Author) && strings.TrimSpace(next.Author) != "" {
		return true
	}
	if prev.FileSize <= 0 && next.FileSize > 0 {
		return true
	}
	if prev.FileSize > 0 && next.FileSize > prev.FileSize*1.2 {
		return true
	}
	if prev.Duration <= 0 && next.Duration > 0 {
		return true
	}
	if prev.Width == 0 && next.Width > 0 {
		return true
	}
	if prev.Height == 0 && next.Height > 0 {
		return true
	}
	return false
}

func (wh *WeChatHandler) shouldSkipDuplicate(v *WeChatVideoInfo) bool {
	if v == nil {
		return true
	}
	key := canonicalKeyForVideo(*v)
	if key == "" {
		key = strings.TrimSpace(v.URL)
	}
	if key == "" {
		return false
	}
	now := time.Now()
	if cached, ok := wh.recentVideos.Load(key); ok {
		if cv, ok2 := cached.(recentVideoCache); ok2 {
			if !hasServerImprovement(cv.Video, *v) && now.Sub(cv.Ts) < 12*time.Second {
				return true
			}
		}
	}
	wh.recentVideos.Store(key, recentVideoCache{Video: *v, Ts: now})
	return false
}

// addVideoURL adds a video URL to the detected set, returns true if it's new
func (wh *WeChatHandler) addVideoURL(videoURL string) bool {
	_, loaded := wh.detectedURLs.LoadOrStore(videoURL, true)
	return !loaded
}

// ClearDetectedURLs clears the detected video URLs cache
func (wh *WeChatHandler) ClearDetectedURLs() {
	wh.detectedURLs = sync.Map{}
}

// HasDetectedURL checks if a URL has already been detected
func (wh *WeChatHandler) HasDetectedURL(videoURL string) bool {
	_, exists := wh.detectedURLs.Load(videoURL)
	return exists
}

func shortenForLog(s string, max int) string {
	s = strings.TrimSpace(s)
	if max <= 0 || s == "" {
		return s
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}
