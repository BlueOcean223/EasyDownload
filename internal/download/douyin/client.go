package douyin

import (
	"EasyDownload/internal/infra/logger"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Error definitions for API client operations.
var (
	// ErrInvalidAwemeID indicates that the provided aweme_id is empty or invalid.
	ErrInvalidAwemeID = errors.New("invalid aweme_id")
	// ErrItemNotFound indicates that the video/note was not found on Douyin.
	ErrItemNotFound = errors.New("douyin item not found")
	// ErrRateLimited indicates that the API returned a rate limit response (403 or 429).
	ErrRateLimited = errors.New("douyin api rate limited")
	// ErrRequestTimeout indicates that the API request timed out.
	ErrRequestTimeout = errors.New("douyin api request timeout")
	// ErrAPIError indicates a general API error with additional details.
	ErrAPIError = errors.New("douyin api error")
	// errIncompleteItem indicates that one fetch path found the item but did not expose usable media.
	errIncompleteItem = errors.New("douyin item incomplete")
)

// Default values for API requests.
const (
	// defaultUserAgent mimics an iPhone Safari browser for API compatibility.
	defaultUserAgent = "Mozilla/5.0 (iPhone; CPU iPhone OS 16_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/16.5 Mobile/15E148 Safari/604.1"
	// defaultBaseURL is the primary API endpoint for fetching video details.
	defaultBaseURL = "https://www.iesdouyin.com/aweme/v1/web/aweme/detail/"
	// defaultPlayURL is the play endpoint used for ratio probing.
	defaultPlayURL = "https://aweme.snssdk.com/aweme/v1/play/"
)

// shareRouterDataPattern matches the embedded JSON data in Douyin share pages.
// The share page embeds video info in a <script> tag as window._ROUTER_DATA = {...};
// This regex captures the JSON object for parsing.
var shareRouterDataPattern = regexp.MustCompile(`(?s)window\._ROUTER_DATA\s*=\s*({.*?})\s*;?\s*</script>`)

// Client is an API client for fetching Douyin video/album information.
// It supports multiple methods of fetching data:
//  1. Share page HTML parsing (preferred method)
//  2. Direct API endpoint (fallback method)
//  3. Slidesinfo endpoint (final fallback for slide/mixed posts)
type Client struct {
	httpClient  *http.Client // HTTP client for making requests
	userAgent   string       // User-Agent header for requests
	baseURL     string       // Base URL for the video detail API
	shareBase   string       // Base URL for share page fallback
	playBaseURL string       // Base URL for ratio probing
}

// NewClient creates a new Client with default settings.
func NewClient() *Client {
	return NewClientWithClient(nil)
}

// NewClientWithClient creates a new Client with a custom HTTP client.
// If client is nil, a default client with 10-second timeout is used.
func NewClientWithClient(client *http.Client) *Client {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	} else {
		copied := *client
		client = &copied
	}
	if client.Jar == nil {
		if jar, err := cookiejar.New(nil); err == nil {
			client.Jar = jar
		}
	}
	return &Client{
		httpClient:  client,
		userAgent:   defaultUserAgent,
		baseURL:     defaultBaseURL,
		shareBase:   "https://www.iesdouyin.com/share",
		playBaseURL: defaultPlayURL,
	}
}

// SetUserAgent sets a custom User-Agent header for API requests.
// Empty strings are ignored to prevent breaking requests.
func (c *Client) SetUserAgent(ua string) {
	ua = strings.TrimSpace(ua)
	if ua == "" {
		return
	}
	c.userAgent = ua
}

// SetHTTPClient sets a custom HTTP client for API requests.
// Nil clients are ignored.
func (c *Client) SetHTTPClient(client *http.Client) {
	if client == nil {
		return
	}
	copied := *client
	if copied.Jar == nil {
		if jar, err := cookiejar.New(nil); err == nil {
			copied.Jar = jar
		}
	}
	c.httpClient = &copied
}

// SetBaseURL sets a custom base URL for the video detail API.
// Empty strings are ignored.
func (c *Client) SetBaseURL(baseURL string) {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		return
	}
	c.baseURL = baseURL
}

// SetShareBaseURL sets a custom base URL for the share page fallback.
// Empty strings are ignored.
func (c *Client) SetShareBaseURL(baseURL string) {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		return
	}
	c.shareBase = baseURL
}

// GetItemInfo fetches detailed information about a Douyin video or album.
// It first attempts to parse the SSR share page, then falls back to the
// detail API and slidesinfo endpoint if needed.
//
// Returns a DouyinItem containing video/album metadata and download URLs.
// For videos, it also fetches file sizes for streams that do not already have one.
func (c *Client) GetItemInfo(awemeID string) (*DouyinItem, error) {
	awemeID = strings.TrimSpace(awemeID)
	if awemeID == "" {
		return nil, ErrInvalidAwemeID
	}

	// Prefer the SSR share page. It is designed for webview/share scenarios and
	// naturally sets anonymous cookies such as ttwid into the client's CookieJar.
	item, shareErr := c.fetchItemInfoFromSharePage(awemeID)
	if shareErr == nil {
		if isUsableItem(item) {
			c.fetchStreamSizes(item)
			return item, nil
		}
		shareErr = errIncompleteItem
	}

	// Fall back to the direct API endpoint when the share page is empty, blocked,
	// or its HTML structure changes.
	item, apiErr := c.fetchItemInfoAPI(awemeID)
	if apiErr == nil {
		if isUsableItem(item) {
			c.fetchStreamSizes(item)
			return item, nil
		}
		apiErr = errIncompleteItem
	}

	// Final fallback for slide/mixed posts where slidesinfo may expose per-page
	// video URLs that are absent from share SSR.
	item, slidesErr := c.fetchItemInfoSlidesInfo(awemeID)
	if slidesErr == nil {
		if isUsableItem(item) {
			c.fetchStreamSizes(item)
			return item, nil
		}
		slidesErr = errIncompleteItem
	}

	if allErrorsAre(ErrItemNotFound, shareErr, apiErr, slidesErr) {
		return nil, ErrItemNotFound
	}
	if allErrorsAre(ErrRateLimited, shareErr, apiErr, slidesErr) {
		return nil, ErrRateLimited
	}
	if allErrorsAre(ErrRequestTimeout, shareErr, apiErr, slidesErr) {
		return nil, ErrRequestTimeout
	}

	joined := errors.Join(shareErr, apiErr, slidesErr)
	return nil, fmt.Errorf("douyin item fetch failed: share page error: %v; api error: %v; slidesinfo error: %v: %w", shareErr, apiErr, slidesErr, joined)
}

// fetchStreamSizes fetches missing file sizes for all video streams via ranged GET requests.
// This allows displaying accurate file sizes before download.
// Only applies to video items, not albums.
func (c *Client) fetchStreamSizes(item *DouyinItem) {
	if item == nil || item.Type != "video" || len(item.Streams) == 0 {
		return
	}

	client := c.httpClient
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}

	for i := range item.Streams {
		if item.Streams[i].Size > 0 {
			continue
		}
		size := c.fetchContentSize(client, item.Streams[i].URL)
		if size <= 0 {
			continue
		}
		item.Streams[i].Size = size
		logger.Debug("[Douyin] Stream %s size: %d bytes", item.Streams[i].QualityKey, size)
	}
}

// fetchContentSize gets file size via a small ranged GET request.
// Returns 0 if the size cannot be determined.
func (c *Client) fetchContentSize(client *http.Client, rawURL string) int64 {
	probe := c.fetchContentSizeProbe(client, rawURL)
	if probe.statusCode >= http.StatusMultipleChoices && probe.statusCode < http.StatusBadRequest {
		return 0
	}
	if probe.contentRangeTotal > 0 {
		return probe.contentRangeTotal
	}
	if probe.statusCode == http.StatusOK && probe.contentLength > 0 {
		return probe.contentLength
	}
	return 0
}

// fetchItemInfoAPI fetches video/album info from the direct API endpoint.
// API endpoint: https://www.iesdouyin.com/aweme/v1/web/aweme/detail/?aweme_id=xxx
func (c *Client) fetchItemInfoAPI(awemeID string) (*DouyinItem, error) {
	client := c.httpClient
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}

	endpoint, err := c.buildItemInfoURL(awemeID)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequest("GET", endpoint, nil)
	if err != nil {
		return nil, err
	}
	c.applyHeaders(req)

	resp, err := client.Do(req)
	if err != nil {
		if isTimeout(err) {
			return nil, ErrRequestTimeout
		}
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests {
		return nil, ErrRateLimited
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("douyin api unexpected status: %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	// Try the new API format first (aweme_detail field).
	// This is the current format used by the Douyin web API.
	var detailPayload awemeDetailResponse
	if err := json.Unmarshal(body, &detailPayload); err == nil {
		if detailPayload.StatusCode == 0 && detailPayload.AwemeDetail != nil {
			logger.Debug("[Douyin] Using new API format (aweme_detail)")
			return c.buildDouyinItem(*detailPayload.AwemeDetail), nil
		}
	}

	// Fallback to the legacy API format (item_list array).
	// This format was used by older versions of the API.
	var payload itemInfoResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}

	if payload.StatusCode != 0 {
		if looksNotFound(payload.StatusMsg) {
			return nil, ErrItemNotFound
		}
		msg := strings.TrimSpace(payload.StatusMsg)
		if msg == "" {
			msg = "unknown error"
		}
		return nil, fmt.Errorf("%w: %s", ErrAPIError, msg)
	}

	if len(payload.ItemList) == 0 {
		return nil, ErrItemNotFound
	}

	logger.Debug("[Douyin] Using legacy API format (item_list)")
	return c.buildDouyinItem(payload.ItemList[0]), nil
}

// fetchItemInfoFromSharePage fetches video/album info by parsing the share page HTML.
// It tries both /video/ and /note/ URL patterns.
func (c *Client) fetchItemInfoFromSharePage(awemeID string) (*DouyinItem, error) {
	shareBase := strings.TrimRight(strings.TrimSpace(c.shareBase), "/")
	if shareBase == "" {
		shareBase = "https://www.iesdouyin.com/share"
	}
	// Try both video and note share page URLs.
	// Videos use /share/video/, notes (image posts) use /share/note/.
	var firstErr error
	for _, kind := range []string{"video", "note"} {
		endpoint := fmt.Sprintf("%s/%s/%s/?app=aweme", shareBase, kind, awemeID)
		item, err := c.fetchSharePageItem(endpoint)
		if err == nil {
			return c.buildDouyinItem(*item), nil
		}
		if firstErr == nil || errors.Is(firstErr, ErrItemNotFound) {
			firstErr = err
		}
	}
	if firstErr != nil {
		return nil, firstErr
	}
	return nil, ErrItemNotFound
}

type slidesInfoResponse struct {
	StatusCode   int            `json:"status_code"`   // 0 indicates success
	StatusMsg    string         `json:"status_msg"`    // Error message if status_code != 0 (may be absent)
	AwemeDetails []itemInfoItem `json:"aweme_details"` // Array of item details
}

// fetchItemInfoSlidesInfo fetches item info from:
//   - https://www.iesdouyin.com/web/api/v2/aweme/slidesinfo/
//
// This endpoint is used by the official share "slides" page and can include
// per-image video sources in `images[].video.play_addr` even when share SSR does not.
func (c *Client) fetchItemInfoSlidesInfo(awemeID string) (*DouyinItem, error) {
	client := c.httpClient
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}

	origin, err := c.shareOrigin()
	if err != nil {
		return nil, err
	}

	u, err := url.Parse(origin + "/web/api/v2/aweme/slidesinfo/")
	if err != nil {
		return nil, err
	}
	q := u.Query()
	q.Set("aweme_ids", fmt.Sprintf("[%s]", awemeID))
	q.Set("aweme_type", "0")
	q.Set("aid", "6383")
	q.Set("request_source", "200")
	u.RawQuery = q.Encode()

	req, err := http.NewRequest("GET", u.String(), nil)
	if err != nil {
		return nil, err
	}
	c.applyHeaders(req)

	resp, err := client.Do(req)
	if err != nil {
		if isTimeout(err) {
			return nil, ErrRequestTimeout
		}
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests {
		return nil, ErrRateLimited
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("douyin slidesinfo unexpected status: %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var payload slidesInfoResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	if payload.StatusCode != 0 {
		if looksNotFound(payload.StatusMsg) {
			return nil, ErrItemNotFound
		}
		msg := strings.TrimSpace(payload.StatusMsg)
		if msg == "" {
			msg = "unknown error"
		}
		return nil, fmt.Errorf("%w: %s", ErrAPIError, msg)
	}
	if len(payload.AwemeDetails) == 0 {
		return nil, ErrItemNotFound
	}

	return c.buildDouyinItem(payload.AwemeDetails[0]), nil
}

func (c *Client) shareOrigin() (string, error) {
	shareBase := strings.TrimSpace(c.shareBase)
	if shareBase == "" {
		shareBase = "https://www.iesdouyin.com/share"
	}
	u, err := url.Parse(shareBase)
	if err != nil {
		return "", err
	}
	if u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("invalid share base url: %s", shareBase)
	}
	return u.Scheme + "://" + u.Host, nil
}

// fetchSharePageItem fetches and parses a single share page.
// The share page contains embedded JSON data in a script tag that we parse.
func (c *Client) fetchSharePageItem(endpoint string) (*itemInfoItem, error) {
	client := c.httpClient
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}

	req, err := http.NewRequest("GET", endpoint, nil)
	if err != nil {
		return nil, err
	}
	c.applyHeaders(req)

	resp, err := client.Do(req)
	if err != nil {
		if isTimeout(err) {
			return nil, ErrRequestTimeout
		}
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests {
		return nil, ErrRateLimited
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("douyin share page unexpected status: %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	item, err := parseSharePageItem(body)
	if err != nil {
		return nil, err
	}
	return item, nil
}

// buildItemInfoURL constructs the full API URL with required query parameters.
// Parameters like aid, version_name, device_platform, and os_version are required
// by the Douyin API to return proper responses.
func (c *Client) buildItemInfoURL(awemeID string) (string, error) {
	baseURL := strings.TrimSpace(c.baseURL)
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	u, err := url.Parse(baseURL)
	if err != nil {
		return "", err
	}
	q := u.Query()
	q.Set("aweme_id", awemeID)
	q.Set("aid", "6383")
	q.Set("version_name", "23.5.0")
	q.Set("device_platform", "webapp")
	q.Set("os_version", "2333")
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// applyHeaders sets standard HTTP headers required for Douyin API requests.
// These headers help bypass some API restrictions and mimic a real browser.
func (c *Client) applyHeaders(req *http.Request) {
	ua := strings.TrimSpace(c.userAgent)
	if ua == "" {
		ua = defaultUserAgent
	}
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
	req.Header.Set("Origin", "https://www.douyin.com")
	req.Header.Set("Referer", "https://www.douyin.com/")
}

type contentSizeProbe struct {
	statusCode        int
	contentRangeTotal int64
	contentLength     int64
	contentType       string
}

func (c *Client) fetchContentSizeProbe(client *http.Client, rawURL string) contentSizeProbe {
	if strings.TrimSpace(rawURL) == "" {
		return contentSizeProbe{}
	}
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}

	req, err := http.NewRequest("GET", rawURL, nil)
	if err != nil {
		return contentSizeProbe{}
	}
	c.applyHeaders(req)
	req.Header.Set("Range", "bytes=0-1")
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Referer", "https://www.douyin.com/")

	resp, err := client.Do(req)
	if err != nil {
		return contentSizeProbe{}
	}
	defer resp.Body.Close()

	return contentSizeProbe{
		statusCode:        resp.StatusCode,
		contentRangeTotal: parseTotalFromContentRange(resp.Header.Get("Content-Range")),
		contentLength:     resp.ContentLength,
		contentType:       resp.Header.Get("Content-Type"),
	}
}

func parseTotalFromContentRange(cr string) int64 {
	cr = strings.TrimSpace(cr)
	if cr == "" {
		return 0
	}
	slash := strings.LastIndex(cr, "/")
	if slash < 0 || slash == len(cr)-1 {
		return 0
	}
	total := strings.TrimSpace(cr[slash+1:])
	if total == "" || total == "*" {
		return 0
	}
	n, err := strconv.ParseInt(total, 10, 64)
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

func isVideoLikeContentType(contentType string) bool {
	contentType = strings.ToLower(strings.TrimSpace(contentType))
	if contentType == "" {
		return true
	}
	return strings.Contains(contentType, "video") || strings.Contains(contentType, "octet-stream")
}

// parseSharePageItem extracts video info from share page HTML.
// The share page embeds JSON data in a script tag as:
//
//	window._ROUTER_DATA = {"loaderData": {"video_id_xxx": {"videoInfoRes": {...}}}}
//
// This function parses that embedded JSON to extract video information.
func parseSharePageItem(body []byte) (*itemInfoItem, error) {
	// Use regex to extract the JSON object from the script tag.
	match := shareRouterDataPattern.FindSubmatch(body)
	if len(match) < 2 {
		return nil, fmt.Errorf("douyin share page router data not found")
	}

	// Parse the outer structure containing loaderData.
	var payload struct {
		LoaderData map[string]json.RawMessage `json:"loaderData"`
	}
	if err := json.Unmarshal(match[1], &payload); err != nil {
		return nil, err
	}

	// Search through loaderData entries for videoInfoRes.
	// The key is dynamic (contains video ID), so we iterate over all entries.
	for _, raw := range payload.LoaderData {
		var container struct {
			VideoInfoRes *itemInfoResponse `json:"videoInfoRes"`
		}
		if err := json.Unmarshal(raw, &container); err != nil {
			continue
		}
		if container.VideoInfoRes != nil && len(container.VideoInfoRes.ItemList) > 0 {
			item := container.VideoInfoRes.ItemList[0]
			return &item, nil
		}
	}

	return nil, ErrItemNotFound
}

// itemInfoResponse represents the legacy API response format with item_list array.
type itemInfoResponse struct {
	StatusCode int            `json:"status_code"` // 0 indicates success
	StatusMsg  string         `json:"status_msg"`  // Error message if status_code != 0
	ItemList   []itemInfoItem `json:"item_list"`   // Array of video/album items
}

// awemeDetailResponse represents the new API response format with aweme_detail object.
type awemeDetailResponse struct {
	StatusCode  int           `json:"status_code"`  // 0 indicates success
	StatusMsg   string        `json:"status_msg"`   // Error message if status_code != 0
	AwemeDetail *itemInfoItem `json:"aweme_detail"` // Video/album details
}

// itemInfoItem represents a single video or album from the API response.
type itemInfoItem struct {
	AwemeID   string      `json:"aweme_id"`   // Unique identifier for the video/album
	Desc      string      `json:"desc"`       // Video description/title
	AwemeType int         `json:"aweme_type"` // Content type: 68 = album, others = video
	Duration  int         `json:"duration"`   // Video duration in milliseconds or seconds
	Author    authorInfo  `json:"author"`     // Author information
	Video     videoInfo   `json:"video"`      // Video playback information
	Images    []imageInfo `json:"images"`     // Album images (for AwemeType 68)
	// ImgBitrate may contain per-image video info for slide posts where each "image"
	// is actually a short video clip (swipeable like an album).
	ImgBitrate json.RawMessage `json:"img_bitrate"`
}

// authorInfo contains information about the content creator.
type authorInfo struct {
	Nickname string `json:"nickname"`  // Display name
	UID      string `json:"uid"`       // User ID
	SecUID   string `json:"sec_uid"`   // Secure user ID
	UniqueID string `json:"unique_id"` // Custom username (douyin handle)
}

// videoInfo contains video playback and quality information.
type videoInfo struct {
	Duration int           `json:"duration"`  // Video duration
	Cover    coverInfo     `json:"cover"`     // Video cover/thumbnail
	PlayAddr playAddr      `json:"play_addr"` // Default playback URL
	BitRate  []bitRateInfo `json:"bit_rate"`  // Available quality options
	Width    int           `json:"width"`     // Video width (fallback)
	Height   int           `json:"height"`    // Video height (fallback)
}

// coverInfo contains video thumbnail URLs.
type coverInfo struct {
	URLList []string `json:"url_list"` // List of cover image URLs (different sizes)
}

// playAddr contains video playback URL information.
type playAddr struct {
	URLList []string `json:"url_list"` // List of playback URLs
	Width   int      `json:"width"`    // Video width
	Height  int      `json:"height"`   // Video height
	URI     string   `json:"uri"`      // Video ID for constructing quality-specific URLs
}

// bitRateInfo represents a single quality option for video playback.
type bitRateInfo struct {
	GearName    string   `json:"gear_name"`    // Quality identifier (e.g., "720p", "1080p")
	QualityType int      `json:"quality_type"` // Quality level number
	BitRate     int      `json:"bit_rate"`     // Bitrate in bits per second
	PlayAddr    playAddr `json:"play_addr"`    // Playback URL for this quality
}

// imageVideoInfo represents video information embedded in an image/album item.
// This is used for mixed content where images array contains video items.
type imageVideoInfo struct {
	PlayAddr playAddr `json:"play_addr"` // Video playback URL
}

// imageInfo represents a single media item in an album.
// For mixed content (aweme_type 68), items can be images or videos.
// If Video field is non-nil, the item contains a video.
type imageInfo struct {
	URLList         []string        `json:"url_list"`          // List of image URLs (different sizes)
	DownloadURLList []string        `json:"download_url_list"` // Download URLs (may include watermark or alternate formats)
	URI             string          `json:"uri"`               // Media URI
	ClipType        int             `json:"clip_type"`         // Clip type (slides video uses this)
	Video           *imageVideoInfo `json:"video"`             // Video info (non-nil for video items)
	Width           int             `json:"width"`             // Media width
	Height          int             `json:"height"`            // Media height
}

func (c *Client) buildDouyinItem(item itemInfoItem) *DouyinItem {
	result := buildDouyinItem(item)
	if result == nil || result.Type != "video" || len(item.Video.BitRate) > 0 {
		return result
	}

	uri := strings.TrimSpace(item.Video.PlayAddr.URI)
	if uri == "" {
		return result
	}

	sourceW := firstNonZero(item.Video.PlayAddr.Width, item.Video.Width)
	sourceH := firstNonZero(item.Video.PlayAddr.Height, item.Video.Height)
	probed := c.probeRatioStreams(uri, sourceW, sourceH)
	if len(probed) > 0 {
		result.Streams = probed
	}
	return result
}

func (c *Client) probeRatioStreams(uri string, sourceW, sourceH int) []Stream {
	uri = strings.TrimSpace(uri)
	if uri == "" {
		return nil
	}

	client := c.httpClient
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}

	ratios := []string{"1080p", "720p", "540p", "480p", "360p"}
	streams := make([]Stream, 0, len(ratios))
	seenSizes := make(map[int64]struct{}, len(ratios))
	for _, ratio := range ratios {
		playURL := c.buildPlayURL(uri, ratio)
		if playURL == "" {
			continue
		}
		probe := c.fetchContentSizeProbe(client, playURL)
		if probe.statusCode != http.StatusPartialContent || probe.contentRangeTotal <= 2 || !isVideoLikeContentType(probe.contentType) {
			logger.Debug("[Douyin] Ratio probe skipped: ratio=%s status=%d size=%d content-type=%s", ratio, probe.statusCode, probe.contentRangeTotal, probe.contentType)
			continue
		}
		if _, ok := seenSizes[probe.contentRangeTotal]; ok {
			logger.Debug("[Douyin] Ratio probe duplicate: ratio=%s size=%d", ratio, probe.contentRangeTotal)
			continue
		}
		seenSizes[probe.contentRangeTotal] = struct{}{}
		width, height := dimensionsForRatio(ratio, sourceW, sourceH)
		streams = append(streams, Stream{
			QualityKey:  ratio,
			QualityName: ratio,
			Width:       width,
			Height:      height,
			URL:         playURL,
			Size:        probe.contentRangeTotal,
		})
		logger.Debug("[Douyin] Ratio probe accepted: ratio=%s size=%d", ratio, probe.contentRangeTotal)
	}
	sortStreamsByResolution(streams)
	return streams
}

func (c *Client) buildPlayURL(uri, ratio string) string {
	uri = strings.TrimSpace(uri)
	if uri == "" {
		return ""
	}
	baseURL := strings.TrimSpace(c.playBaseURL)
	if baseURL == "" {
		baseURL = defaultPlayURL
	}
	u, err := url.Parse(baseURL)
	if err != nil {
		return ""
	}
	q := u.Query()
	q.Set("video_id", uri)
	if strings.TrimSpace(ratio) != "" {
		q.Set("ratio", strings.TrimSpace(ratio))
	}
	q.Set("line", "0")
	u.RawQuery = q.Encode()
	return u.String()
}

func dimensionsForRatio(ratio string, sourceW, sourceH int) (int, int) {
	res := qualityRank(ratio)
	if res <= 0 || sourceW <= 0 || sourceH <= 0 {
		return 0, 0
	}
	if sourceW < sourceH {
		return res, (res*sourceH + sourceW/2) / sourceW
	}
	return (res*sourceW + sourceH/2) / sourceH, res
}

func isUsableItem(item *DouyinItem) bool {
	if item == nil {
		return false
	}
	switch item.Type {
	case "video":
		return len(item.Streams) > 0
	case "album":
		return len(item.Images) > 0
	default:
		return true
	}
}

func allErrorsAre(target error, errs ...error) bool {
	if target == nil || len(errs) == 0 {
		return false
	}
	for _, err := range errs {
		if !errors.Is(err, target) {
			return false
		}
	}
	return true
}

// buildDouyinItem converts an API response item to a DouyinItem.
// It determines the content type (video vs album) and extracts relevant fields.
// For albums (AwemeType 68 or items with images), it extracts image URLs.
// For videos, it builds stream information with multiple quality options.
func buildDouyinItem(item itemInfoItem) *DouyinItem {
	// Determine content type: AwemeType 68 indicates an album (image collection).
	// Also treat items with images array as albums.
	isAlbum := item.AwemeType == 68 || len(item.Images) > 0 || hasImgBitrate(item.ImgBitrate)

	result := &DouyinItem{
		ID:       item.AwemeID,
		Title:    item.Desc,
		Author:   item.Author.Nickname,
		AuthorID: firstNonEmpty(item.Author.UID, item.Author.SecUID, item.Author.UniqueID),
	}

	logger.Debug("[Douyin] Parsing item: ID=%s, Title=%s, Author=%s, AwemeType=%d",
		item.AwemeID, item.Desc, item.Author.Nickname, item.AwemeType)

	if isAlbum {
		result.Type = "album"
	} else {
		result.Type = "video"
	}

	result.Cover = firstURL(item.Video.Cover.URLList)
	if result.Cover == "" && len(item.Images) > 0 {
		result.Cover = firstURL(item.Images[0].URLList)
	}

	if isAlbum {
		result.Duration = 0
		result.Images = buildImages(item.Images, item.ImgBitrate)
		// Some album-like posts are actually multi-video collections where images entries
		// contain only video.play_addr (no url_list). Ensure we still have a usable cover.
		if strings.TrimSpace(result.Cover) == "" && len(result.Images) > 0 {
			for _, media := range result.Images {
				if u := strings.TrimSpace(media.URL); u != "" {
					result.Cover = u
					break
				}
			}
			if strings.TrimSpace(result.Cover) == "" {
				// Last resort: fall back to the first video URL so the UI isn't blank.
				result.Cover = strings.TrimSpace(result.Images[0].VideoURL)
			}
		}
		logger.Debug("[Douyin] Album detected with %d images", len(result.Images))
		return result
	}

	result.Duration = normalizeDuration(firstNonZero(item.Video.Duration, item.Duration))
	result.Streams = buildStreams(item.Video)

	// Log available streams for quality debugging
	logger.Debug("[Douyin] Video streams available: %d", len(result.Streams))
	for i, s := range result.Streams {
		logger.Debug("[Douyin]   Stream[%d]: QualityKey=%s, Resolution=%dx%d, Bitrate=%d, URL=%s",
			i, s.QualityKey, s.Width, s.Height, s.Bitrate, truncateURL(s.URL, 80))
	}

	return result
}

// buildStreams extracts available video quality streams from the API response.
// It prioritizes the BitRate array which contains multiple quality options.
// Falls back only to explicit PlayAddr URLs if BitRate is empty.
func buildStreams(video videoInfo) []Stream {
	logger.Debug("[Douyin] Building streams from API response: BitRate count=%d, PlayAddr.Width=%d, PlayAddr.Height=%d",
		len(video.BitRate), video.PlayAddr.Width, video.PlayAddr.Height)

	streams := make([]Stream, 0, len(video.BitRate))
	fallbackWidth := video.Width
	fallbackHeight := video.Height

	// Log raw BitRate info from API for debugging quality issues.
	for i, br := range video.BitRate {
		logger.Debug("[Douyin]   Raw BitRate[%d]: GearName=%s, BitRate=%d, QualityType=%d, PlayAddr.Width=%d, PlayAddr.Height=%d",
			i, br.GearName, br.BitRate, br.QualityType, br.PlayAddr.Width, br.PlayAddr.Height)
	}

	// Build streams from BitRate array (preferred method).
	// Each entry represents a different quality level.
	for _, br := range video.BitRate {
		// Prefer no-watermark URLs by replacing "playwm" with "play".
		url := pickNoWatermarkURL(br.PlayAddr.URLList)
		if url == "" {
			continue
		}

		width := br.PlayAddr.Width
		height := br.PlayAddr.Height
		// Use fallback dimensions if PlayAddr doesn't have them.
		if width == 0 && height == 0 {
			width = fallbackWidth
			height = fallbackHeight
		}
		// Determine quality key from gear name (e.g., "720p", "1080p").
		qualityKey := qualityFromGear(br.GearName)
		if qualityKey == "" {
			qualityKey = resolutionKey(width, height)
		}
		if qualityKey == "" {
			qualityKey = "source"
		}
		qualityName := qualityKey

		streams = append(streams, Stream{
			QualityKey:  qualityKey,
			QualityName: qualityName,
			Width:       width,
			Height:      height,
			Bitrate:     br.BitRate,
			URL:         url,
		})
	}

	if len(streams) == 0 {
		logger.Warn("[Douyin] No streams from BitRate array, falling back to PlayAddr")

		// Final fallback: use the original PlayAddr URL. Ratio probing is handled by
		// Client.probeRatioStreams so this pure builder does not blindly expose
		// constructed URLs.
		url := pickNoWatermarkURL(video.PlayAddr.URLList)
		if url != "" {
			width := video.PlayAddr.Width
			height := video.PlayAddr.Height
			if width == 0 && height == 0 {
				width = fallbackWidth
				height = fallbackHeight
			}
			qualityKey := resolutionKey(width, height)
			if qualityKey == "" {
				qualityKey = "source"
			}
			streams = append(streams, Stream{
				QualityKey:  qualityKey,
				QualityName: qualityKey,
				Width:       width,
				Height:      height,
				URL:         url,
			})
			logger.Debug("[Douyin] Fallback stream from PlayAddr: %dx%d, key=%s", width, height, qualityKey)
		}
	}

	// Sort streams by resolution (highest first) for consistent ordering.
	sortStreamsByResolution(streams)
	return streams
}

// truncateURL truncates a URL for logging purposes.
// Adds "..." suffix if truncated.
func truncateURL(url string, maxLen int) string {
	if len(url) <= maxLen {
		return url
	}
	return url[:maxLen] + "..."
}

// buildImages converts API image info to the internal Image format.
// For mixed content (aweme_type 68), extracts both image URLs and video URLs.
// If an item has a video field, its VideoURL will be populated.
func buildImages(images []imageInfo, imgBitrate json.RawMessage) []Image {
	imgBitrates := parseImgBitrateEntries(imgBitrate)

	// Some slide/video-collection posts may not populate the `images` array but still
	// provide per-item video sources in `img_bitrate`.
	if len(images) == 0 && len(imgBitrates) > 0 {
		return buildImagesFromBitrate(imgBitrates)
	}

	out := make([]Image, 0, len(images))
	for idx, img := range images {
		media := extractMediaFromImageInfo(img, imgBitrates, idx)
		if media.URL == "" && media.VideoURL == "" {
			continue
		}
		out = append(out, media)
	}
	return out
}

// buildImagesFromBitrate builds Image entries when only img_bitrate is available.
func buildImagesFromBitrate(imgBitrates []imgBitrateEntry) []Image {
	out := make([]Image, 0, len(imgBitrates))
	for _, br := range imgBitrates {
		width := firstNonZero(br.PlayAddr.Width, br.Width)
		height := firstNonZero(br.PlayAddr.Height, br.Height)
		videoURL := pickVideoURLFromImgBitrate(br, width, height)
		if strings.TrimSpace(videoURL) == "" {
			continue
		}
		out = append(out, Image{
			VideoURL: videoURL,
			Width:    width,
			Height:   height,
		})
	}
	return out
}

// extractMediaFromImageInfo extracts image/video URLs and dimensions from a single imageInfo.
func extractMediaFromImageInfo(img imageInfo, imgBitrates []imgBitrateEntry, idx int) Image {
	imageURL := firstURL(img.URLList)
	if strings.TrimSpace(imageURL) == "" {
		imageURL = firstURL(img.DownloadURLList)
	}

	videoURL := extractVideoURLFromImageInfo(img, imgBitrates, idx)

	width := img.Width
	height := img.Height
	if width == 0 && height == 0 && idx < len(imgBitrates) {
		width = firstNonZero(imgBitrates[idx].PlayAddr.Width, imgBitrates[idx].Width)
		height = firstNonZero(imgBitrates[idx].PlayAddr.Height, imgBitrates[idx].Height)
	}

	return Image{
		URL:      imageURL,
		VideoURL: videoURL,
		Width:    width,
		Height:   height,
	}
}

// extractVideoURLFromImageInfo extracts video URL from an imageInfo entry.
// Tries multiple sources: embedded video field, download_url_list, and img_bitrate.
func extractVideoURLFromImageInfo(img imageInfo, imgBitrates []imgBitrateEntry, idx int) string {
	// Try embedded video field first
	if img.Video != nil {
		if url := pickNoWatermarkURL(img.Video.PlayAddr.URLList); url != "" {
			return url
		}
		if url := firstURL(img.Video.PlayAddr.URLList); url != "" {
			return url
		}
		if url := constructVideoURLFromURI(img.Video.PlayAddr.URI, img.Width, img.Height); url != "" {
			return url
		}
	}

	// Fallback: sometimes "download_url_list" contains a playable clip URL
	if u := firstVideoLikeURL(img.DownloadURLList); u != "" {
		if url := pickNoWatermarkURL([]string{u}); url != "" {
			return url
		}
	}

	// Fallback: some slide posts provide per-image video sources in img_bitrate
	if idx < len(imgBitrates) {
		return pickVideoURLFromImgBitrate(imgBitrates[idx], img.Width, img.Height)
	}

	return ""
}

func firstVideoLikeURL(urls []string) string {
	for _, raw := range urls {
		u := strings.TrimSpace(raw)
		if u == "" {
			continue
		}
		lu := strings.ToLower(u)
		if strings.Contains(lu, "mime_type=video") ||
			strings.Contains(lu, "video_mp4") ||
			strings.HasSuffix(lu, ".mp4") ||
			strings.HasSuffix(lu, ".mov") ||
			strings.HasSuffix(lu, ".m4v") {
			return u
		}
	}
	return ""
}

// constructVideoURLFromURI builds a direct play URL from a Douyin play_addr URI.
// Some album media items provide only `play_addr.uri` without `play_addr.url_list`.
func constructVideoURLFromURI(uri string, width, height int) string {
	uri = strings.TrimSpace(uri)
	if uri == "" {
		return ""
	}
	// Some responses may already provide a full URL in `uri`.
	if strings.HasPrefix(uri, "http://") || strings.HasPrefix(uri, "https://") {
		return pickNoWatermarkURL([]string{uri})
	}
	ratio := resolutionKey(width, height)
	if ratio != "" {
		return fmt.Sprintf("https://aweme.snssdk.com/aweme/v1/play/?video_id=%s&ratio=%s&line=0", uri, ratio)
	}
	return fmt.Sprintf("https://aweme.snssdk.com/aweme/v1/play/?video_id=%s&line=0", uri)
}

type imgBitrateEntry struct {
	BitRate  []bitRateInfo `json:"bit_rate"`
	PlayAddr playAddr      `json:"play_addr"`
	Width    int           `json:"width"`
	Height   int           `json:"height"`
}

func hasImgBitrate(raw json.RawMessage) bool {
	raw = bytes.TrimSpace(raw)
	return len(raw) > 0 && !bytes.Equal(raw, []byte("null")) && !bytes.Equal(raw, []byte("[]"))
}

func parseImgBitrateEntries(raw json.RawMessage) []imgBitrateEntry {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil
	}

	// Fast path: expected shape is an array aligned to images indices.
	var out []imgBitrateEntry
	if err := json.Unmarshal(raw, &out); err == nil && len(out) > 0 {
		return out
	}

	// Fallback: try to decode loosely to tolerate shape changes.
	var anyArr []any
	if err := json.Unmarshal(raw, &anyArr); err != nil {
		return nil
	}

	out = make([]imgBitrateEntry, 0, len(anyArr))
	for _, v := range anyArr {
		switch vv := v.(type) {
		case map[string]any:
			entry := imgBitrateEntry{}
			if pa, ok := vv["play_addr"]; ok {
				if buf, err := json.Marshal(pa); err == nil {
					_ = json.Unmarshal(buf, &entry.PlayAddr)
				}
			}
			if br, ok := vv["bit_rate"]; ok {
				if buf, err := json.Marshal(br); err == nil {
					_ = json.Unmarshal(buf, &entry.BitRate)
				}
			}
			if w, ok := vv["width"].(float64); ok {
				entry.Width = int(w)
			}
			if h, ok := vv["height"].(float64); ok {
				entry.Height = int(h)
			}
			out = append(out, entry)
		case []any:
			entry := imgBitrateEntry{}
			if buf, err := json.Marshal(vv); err == nil {
				_ = json.Unmarshal(buf, &entry.BitRate)
			}
			out = append(out, entry)
		default:
			out = append(out, imgBitrateEntry{})
		}
	}
	return out
}

func pickVideoURLFromImgBitrate(entry imgBitrateEntry, fallbackWidth, fallbackHeight int) string {
	// Prefer explicit play_addr URLs.
	if url := pickNoWatermarkURL(entry.PlayAddr.URLList); url != "" {
		return url
	}
	if url := firstURL(entry.PlayAddr.URLList); url != "" {
		return url
	}

	// Prefer highest-quality bit_rate URLs when present.
	if len(entry.BitRate) > 0 {
		vi := videoInfo{
			PlayAddr: entry.PlayAddr,
			BitRate:  entry.BitRate,
			Width:    firstNonZero(entry.Width, fallbackWidth),
			Height:   firstNonZero(entry.Height, fallbackHeight),
		}
		streams := buildStreams(vi)
		if len(streams) > 0 {
			return streams[0].URL
		}
	}

	// Last resort: construct from URI.
	width := firstNonZero(entry.PlayAddr.Width, entry.Width, fallbackWidth)
	height := firstNonZero(entry.PlayAddr.Height, entry.Height, fallbackHeight)
	return constructVideoURLFromURI(entry.PlayAddr.URI, width, height)
}

// gearResolutionPattern matches resolution numbers in gear names.
// Examples: "540p" -> "540", "720p_h265" -> "720", "normal_1080p" -> "1080"
var gearResolutionPattern = regexp.MustCompile(`(\d{3,4})p?`)

// qualityFromGear extracts a quality key from a gear name.
// Converts gear names like "540p_h265" to "540p".
func qualityFromGear(gear string) string {
	gear = strings.ToLower(strings.TrimSpace(gear))
	if gear == "" {
		return ""
	}
	if match := gearResolutionPattern.FindStringSubmatch(gear); len(match) > 1 {
		return match[1] + "p"
	}
	return ""
}

// resolutionKey generates a quality key string from video dimensions.
// Returns format like "720p" or "1080p" based on the smaller dimension.
func resolutionKey(width, height int) string {
	res := resolutionValue(width, height)
	if res == 0 {
		return ""
	}
	return fmt.Sprintf("%dp", res)
}

// resolutionValue returns the smaller of width/height for resolution comparison.
// For vertical videos, width is typically smaller (e.g., 720x1280).
// For horizontal videos, height is typically smaller (e.g., 1920x1080).
func resolutionValue(width, height int) int {
	if width > 0 && height > 0 {
		if width < height {
			return width
		}
		return height
	}
	if width > 0 {
		return width
	}
	return height
}

// qualityRank extracts a numeric rank from quality keys such as "1080p".
func qualityRank(quality string) int {
	key := qualityFromGear(quality)
	if key == "" {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSuffix(key, "p"))
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

// sortStreamsByResolution sorts streams by resolution (highest first).
// For equal resolutions, sorts by quality key and then bitrate (highest first).
func sortStreamsByResolution(streams []Stream) {
	sort.SliceStable(streams, func(i, j int) bool {
		ri := resolutionValue(streams[i].Width, streams[i].Height)
		rj := resolutionValue(streams[j].Width, streams[j].Height)
		if ri == rj {
			qi := qualityRank(streams[i].QualityKey)
			qj := qualityRank(streams[j].QualityKey)
			if qi != qj {
				return qi > qj
			}
			return streams[i].Bitrate > streams[j].Bitrate
		}
		return ri > rj
	})
}

// pickNoWatermarkURL selects the best URL from a list, preferring no-watermark versions.
// Douyin URLs containing "playwm" have watermarks; replacing with "play" removes them.
func pickNoWatermarkURL(urls []string) string {
	// First pass: look for URLs with "playwm" and convert to no-watermark.
	for _, raw := range urls {
		u := strings.TrimSpace(raw)
		if u == "" {
			continue
		}
		if strings.Contains(u, "playwm") {
			return strings.Replace(u, "playwm", "play", 1)
		}
	}
	// Second pass: disable watermark query param when present.
	for _, raw := range urls {
		u := strings.TrimSpace(raw)
		if u == "" {
			continue
		}
		if strings.Contains(u, "watermark=1") {
			return strings.Replace(u, "watermark=1", "watermark=0", 1)
		}
	}
	// Final pass: return any non-empty URL.
	for _, raw := range urls {
		u := strings.TrimSpace(raw)
		if u != "" {
			return u
		}
	}
	return ""
}

// firstURL returns the first non-empty URL from a list.
func firstURL(urls []string) string {
	for _, raw := range urls {
		u := strings.TrimSpace(raw)
		if u != "" {
			return u
		}
	}
	return ""
}

// firstNonEmpty returns the first non-empty string from the provided values.
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// firstNonZero returns the first non-zero integer from the provided values.
func firstNonZero(values ...int) int {
	for _, v := range values {
		if v > 0 {
			return v
		}
	}
	return 0
}

// normalizeDuration converts duration to seconds.
// Douyin API may return duration in milliseconds (>1000) or seconds.
// This function ensures consistent second-based values.
func normalizeDuration(value int) int {
	if value <= 0 {
		return 0
	}
	if value > 1000 {
		return value / 1000
	}
	return value
}

// looksNotFound checks if an error message indicates the content was not found.
// Used to determine if a "not found" error should be returned.
func looksNotFound(msg string) bool {
	msg = strings.ToLower(msg)
	return strings.Contains(msg, "not") && (strings.Contains(msg, "exist") || strings.Contains(msg, "found"))
}

// isTimeout checks if an error represents a timeout condition.
// Handles both context.DeadlineExceeded and net.Error timeouts.
func isTimeout(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}
