package wechat

import (
	"EasyDownload/internal/infra/logger"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	recentVideoTTL        = 2 * time.Minute
	recentCleanupInterval = 30 * time.Second
	probeCacheTTL         = 15 * time.Minute
	probeCleanupInterval  = 2 * time.Minute
)

// Handler handles WeChat Channels payloads for video detection and download triggers.
type Handler struct {
	onVideoDetected   func(VideoInfo)
	onDownloadRequest func(VideoInfo)
	recentVideos      sync.Map

	currentVideo   *VideoInfo
	currentVideoMu sync.RWMutex

	probedSizes sync.Map

	recentCleanupAt atomic.Int64
	probedCleanupAt atomic.Int64
}

// NewHandler creates a new Handler instance.
func NewHandler() *Handler {
	return &Handler{}
}

// SetVideoCallback sets the callback function for detected videos.
func (h *Handler) SetVideoCallback(callback func(VideoInfo)) {
	h.onVideoDetected = callback
}

// SetDownloadCallback sets the callback function for download requests.
func (h *Handler) SetDownloadCallback(callback func(VideoInfo)) {
	h.onDownloadRequest = callback
}

// GetCurrentVideo returns the current video info.
func (h *Handler) GetCurrentVideo() *VideoInfo {
	h.currentVideoMu.RLock()
	defer h.currentVideoMu.RUnlock()
	return h.currentVideo
}

// HandleRequest handles POST requests to /res-downloader/wechat.
func (h *Handler) HandleRequest(body []byte) error {
	return h.HandleRequestWithType(body, "1")
}

// HandleRequestWithType handles POST requests with a specific type.
func (h *Handler) HandleRequestWithType(body []byte, reqType string) error {
	if reqType == "download" {
		return h.handleDownloadRequest(body)
	}

	videoInfo, err := ParseVideoPayload(body)
	if err != nil {
		return err
	}
	SanitizeVideo(videoInfo)
	if h.shouldSkipDuplicate(videoInfo) {
		logger.Debug("WeChat duplicate skipped: %s", videoInfo.Title)
		return nil
	}

	h.updateCurrentVideo(videoInfo)

	if h.onVideoDetected != nil {
		h.onVideoDetected(*videoInfo)
	}

	h.maybeProbeAndUpdateFileSize(videoInfo)

	logger.Debug("WeChat video detected: id=%q title=%q author=%q stable=%q url=%q pageKey=%q source=%q href=%q ts=%d",
		shortenForLog(videoInfo.ID, 120),
		shortenForLog(videoInfo.Title, 160),
		shortenForLog(videoInfo.Author, 80),
		shortenForLog(ExtractStableURLParams(videoInfo.URL), 220),
		shortenForLog(videoInfo.URL, 220),
		shortenForLog(videoInfo.PageKey, 120),
		shortenForLog(videoInfo.Source, 40),
		shortenForLog(videoInfo.Href, 200),
		videoInfo.TS,
	)
	return nil
}

// maybeProbeAndUpdateFileSize asynchronously probes the video URL to get accurate file size.
// It uses HTTP Range requests to determine the total content length without downloading
// the entire file. Results are cached to avoid redundant probes.
func (h *Handler) maybeProbeAndUpdateFileSize(videoInfo *VideoInfo) {
	if videoInfo == nil || videoInfo.URL == "" {
		return
	}

	parsedURL, err := url.Parse(strings.TrimSpace(videoInfo.URL))
	if err != nil || !isFinderVideoHost(parsedURL.Hostname()) {
		return
	}

	probeKey := CanonicalKeyForVideo(*videoInfo)
	if strings.TrimSpace(probeKey) == "" {
		probeKey = videoInfo.URL
	}

	now := time.Now()
	if v, ok := h.probedSizes.Load(probeKey); ok {
		if ts, ok2 := v.(int64); ok2 {
			if now.Sub(time.Unix(0, ts)) < probeCacheTTL {
				return
			}
		}
	}
	h.probedSizes.Store(probeKey, now.UnixNano())
	h.maybeCleanupProbedSizes(now)

	urlToProbe := videoInfo.URL
	base := *videoInfo
	go func() {
		size, err := probeContentLengthByRange(urlToProbe)
		if err != nil || size <= 0 {
			h.probedSizes.Delete(probeKey)
			return
		}

		h.currentVideoMu.Lock()
		if h.currentVideo != nil && h.currentVideo.URL == urlToProbe {
			old := h.currentVideo.FileSize
			h.currentVideo.FileSize = float64(size)
			base.FileSize = float64(size)
			base.IsCurrentVideo = true
			if old <= 0 || float64(size) > old*1.2 {
				logger.Debug("[WeChat Probe] FileSize updated: old=%.0f new=%d stable=%q url=%q",
					old,
					size,
					shortenForLog(ExtractStableURLParams(urlToProbe), 220),
					shortenForLog(urlToProbe, 160),
				)
			}
		} else {
			h.currentVideoMu.Unlock()
			return
		}
		h.currentVideoMu.Unlock()

		if h.onVideoDetected != nil {
			h.onVideoDetected(base)
		}
	}()
}

// probeContentLengthByRange determines the total file size by making an HTTP Range request.
// It requests only the first byte (bytes=0-0) and parses the Content-Range header
// to extract the total size. Falls back to Content-Length if Content-Range is unavailable.
func probeContentLengthByRange(rawURL string) (int64, error) {
	client := &http.Client{Timeout: 6 * time.Second}
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

	cl := resp.Header.Get("Content-Length")
	if cl != "" {
		if n, e := strconv.ParseInt(strings.TrimSpace(cl), 10, 64); e == nil && n > 0 {
			if n > 1 {
				return n, nil
			}
		}
	}

	return 0, fmt.Errorf("unable to probe content length")
}

// handleDownloadRequest processes a download request from the injected JS.
// It parses the payload, falls back to current video if parsing fails,
// and triggers the download callback.
func (h *Handler) handleDownloadRequest(body []byte) error {
	videoInfo, err := ParseVideoPayload(body)
	if err != nil {
		h.currentVideoMu.RLock()
		current := h.currentVideo
		h.currentVideoMu.RUnlock()

		if current != nil {
			logger.Warn("[WeChat Download] Payload parse failed, falling back to current video: %v", err)
			videoInfo = current
		} else {
			return err
		}
	}
	SanitizeVideo(videoInfo)

	key := ExtractStableURLParams(videoInfo.URL)
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

	h.currentVideoMu.RLock()
	cur := h.currentVideo
	h.currentVideoMu.RUnlock()
	if cur != nil && strings.TrimSpace(cur.ID) != "" && strings.TrimSpace(videoInfo.ID) != "" && strings.TrimSpace(cur.ID) != strings.TrimSpace(videoInfo.ID) {
		logger.Warn("[WeChat Download] Mismatch vs current: currentId=%q currentTitle=%q currentStable=%q currentPageKey=%q currentSource=%q currentHref=%q",
			shortenForLog(cur.ID, 120),
			shortenForLog(cur.Title, 160),
			shortenForLog(ExtractStableURLParams(cur.URL), 220),
			shortenForLog(cur.PageKey, 120),
			shortenForLog(cur.Source, 40),
			shortenForLog(cur.Href, 200),
		)
	}

	if h.onDownloadRequest != nil {
		h.onDownloadRequest(*videoInfo)
	}
	return nil
}

// updateCurrentVideo sets the given video as the current playing video.
// Thread-safe via mutex protection.
func (h *Handler) updateCurrentVideo(video *VideoInfo) {
	h.currentVideoMu.Lock()
	defer h.currentVideoMu.Unlock()

	video.IsCurrentVideo = true
	h.currentVideo = video
}

// hasServerImprovement checks if the next video info has better metadata than prev.
// Returns true if next has improvements like: valid title replacing bad title,
// longer title, new cover URL, valid author, larger file size, or new dimensions.
// Used to decide whether to accept a "duplicate" video with better metadata.
func hasServerImprovement(prev, next VideoInfo) bool {
	pt := strings.TrimSpace(prev.Title)
	nt := strings.TrimSpace(next.Title)
	if !IsBadTitle(nt) {
		if IsBadTitle(pt) && nt != "" {
			return true
		}
		if len(nt) > len(pt) && pt != nt {
			return true
		}
	}
	if strings.TrimSpace(prev.CoverURL) == "" && strings.TrimSpace(next.CoverURL) != "" {
		return true
	}
	if IsBadAuthor(prev.Author) && !IsBadAuthor(next.Author) && strings.TrimSpace(next.Author) != "" {
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

// shouldSkipDuplicate checks if a video should be skipped as a duplicate.
// Returns true if the same video was seen recently (within 12 seconds)
// and the new payload doesn't have improved metadata.
func (h *Handler) shouldSkipDuplicate(v *VideoInfo) bool {
	if v == nil {
		return true
	}
	key := CanonicalKeyForVideo(*v)
	if key == "" {
		key = strings.TrimSpace(v.URL)
	}
	if key == "" {
		return false
	}
	now := time.Now()
	if cached, ok := h.recentVideos.Load(key); ok {
		if cv, ok2 := cached.(recentVideoCache); ok2 {
			if !hasServerImprovement(cv.Video, *v) && now.Sub(cv.Ts) < 12*time.Second {
				return true
			}
		}
	}
	h.recentVideos.Store(key, recentVideoCache{Video: *v, Ts: now})
	h.maybeCleanupRecentVideos(now)
	return false
}

// maybeCleanupRecentVideos periodically removes stale entries from the recent videos cache.
// Uses atomic CAS to ensure only one goroutine performs cleanup at a time.
func (h *Handler) maybeCleanupRecentVideos(now time.Time) {
	prev := h.recentCleanupAt.Load()
	if prev != 0 && now.Sub(time.Unix(0, prev)) < recentCleanupInterval {
		return
	}
	if !h.recentCleanupAt.CompareAndSwap(prev, now.UnixNano()) {
		return
	}
	h.recentVideos.Range(func(key, value any) bool {
		cv, ok := value.(recentVideoCache)
		if !ok || now.Sub(cv.Ts) > recentVideoTTL {
			h.recentVideos.Delete(key)
		}
		return true
	})
}

// maybeCleanupProbedSizes periodically removes stale entries from the probed sizes cache.
// Uses atomic CAS to ensure only one goroutine performs cleanup at a time.
func (h *Handler) maybeCleanupProbedSizes(now time.Time) {
	prev := h.probedCleanupAt.Load()
	if prev != 0 && now.Sub(time.Unix(0, prev)) < probeCleanupInterval {
		return
	}
	if !h.probedCleanupAt.CompareAndSwap(prev, now.UnixNano()) {
		return
	}
	h.probedSizes.Range(func(key, value any) bool {
		ts, ok := value.(int64)
		if !ok || now.Sub(time.Unix(0, ts)) > probeCacheTTL {
			h.probedSizes.Delete(key)
		}
		return true
	})
}

// shortenForLog truncates a string to max runes for logging purposes.
// Appends "..." if truncation occurs. Handles Unicode correctly.
func shortenForLog(s string, max int) string {
	s = strings.TrimSpace(s)
	if max <= 0 || s == "" {
		return s
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "..."
}
