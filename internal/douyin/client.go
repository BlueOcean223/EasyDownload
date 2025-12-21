package douyin

import (
	"EasyDownload/internal/logger"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"sort"
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
)

// Default values for API requests.
const (
	// defaultUserAgent mimics an iPhone Safari browser for API compatibility.
	defaultUserAgent = "Mozilla/5.0 (iPhone; CPU iPhone OS 16_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/16.5 Mobile/15E148 Safari/604.1"
	// defaultBaseURL is the primary API endpoint for fetching video details.
	defaultBaseURL = "https://www.iesdouyin.com/aweme/v1/web/aweme/detail/"
)

// shareRouterDataPattern matches the embedded JSON data in Douyin share pages.
// The share page embeds video info in a <script> tag as window._ROUTER_DATA = {...};
// This regex captures the JSON object for parsing.
var shareRouterDataPattern = regexp.MustCompile(`(?s)window\._ROUTER_DATA\s*=\s*({.*?})\s*;?\s*</script>`)

// Client is an API client for fetching Douyin video/album information.
// It supports two methods of fetching data:
//  1. Direct API endpoint (primary method)
//  2. Share page HTML parsing (fallback method)
type Client struct {
	httpClient *http.Client // HTTP client for making requests
	userAgent  string       // User-Agent header for requests
	baseURL    string       // Base URL for the video detail API
	shareBase  string       // Base URL for share page fallback
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
	}
	return &Client{
		httpClient: client,
		userAgent:  defaultUserAgent,
		baseURL:    defaultBaseURL,
		shareBase:  "https://www.iesdouyin.com/share",
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
	c.httpClient = client
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
// It first attempts to use the direct API endpoint, then falls back to
// parsing the share page HTML if the API fails.
//
// Returns a DouyinItem containing video/album metadata and download URLs.
// For videos, it also fetches file sizes for each quality stream via HEAD requests.
func (c *Client) GetItemInfo(awemeID string) (*DouyinItem, error) {
	awemeID = strings.TrimSpace(awemeID)
	if awemeID == "" {
		return nil, ErrInvalidAwemeID
	}

	// Try the direct API endpoint first (faster and more reliable).
	item, apiErr := c.fetchItemInfoAPI(awemeID)
	if apiErr == nil {
		c.fetchStreamSizes(item)
		return item, nil
	}

	// Fallback to parsing the share page HTML.
	// This is slower but works when the API is blocked or rate-limited.
	item, shareErr := c.fetchItemInfoFromSharePage(awemeID)
	if shareErr == nil {
		c.fetchStreamSizes(item)
		return item, nil
	}

	// Return the more specific error if available.
	if shouldReturnAPIError(apiErr) {
		return nil, apiErr
	}
	return nil, fmt.Errorf("douyin api error: %w; share page error: %v", apiErr, shareErr)
}

// fetchStreamSizes fetches file sizes for all video streams via HEAD requests.
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
		size := c.fetchContentLength(client, item.Streams[i].URL)
		item.Streams[i].Size = size
		if size > 0 {
			logger.Debug("[Douyin] Stream %s size: %d bytes", item.Streams[i].QualityKey, size)
		}
	}
}

// fetchContentLength gets file size via a HEAD request.
// Returns 0 if the size cannot be determined.
func (c *Client) fetchContentLength(client *http.Client, url string) int64 {
	if url == "" {
		return 0
	}

	req, err := http.NewRequest("HEAD", url, nil)
	if err != nil {
		return 0
	}
	c.applyHeaders(req)

	resp, err := client.Do(req)
	if err != nil {
		return 0
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0
	}

	return resp.ContentLength
}

// fetchItemInfoAPI fetches video/album info from the direct API endpoint.
// This is the primary method for getting video details.
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
			return buildDouyinItem(*detailPayload.AwemeDetail), nil
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
	return buildDouyinItem(payload.ItemList[0]), nil
}

// fetchItemInfoFromSharePage fetches video/album info by parsing the share page HTML.
// This is a fallback method when the direct API fails.
// It tries both /video/ and /note/ URL patterns.
func (c *Client) fetchItemInfoFromSharePage(awemeID string) (*DouyinItem, error) {
	shareBase := strings.TrimRight(strings.TrimSpace(c.shareBase), "/")
	if shareBase == "" {
		shareBase = "https://www.iesdouyin.com/share"
	}
	// Try both video and note share page URLs.
	// Videos use /share/video/, notes (image posts) use /share/note/.
	for _, kind := range []string{"video", "note"} {
		endpoint := fmt.Sprintf("%s/%s/%s/?app=aweme", shareBase, kind, awemeID)
		item, err := c.fetchSharePageItem(endpoint)
		if err == nil {
			return buildDouyinItem(*item), nil
		}
		if !errors.Is(err, ErrItemNotFound) {
			return nil, err
		}
	}
	return nil, ErrItemNotFound
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

// shouldReturnAPIError checks if an API error should be returned directly
// instead of attempting fallback methods.
// Specific errors like ErrItemNotFound indicate the video doesn't exist,
// so fallback wouldn't help.
func shouldReturnAPIError(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, ErrItemNotFound) ||
		errors.Is(err, ErrAPIError) ||
		errors.Is(err, ErrRateLimited) ||
		errors.Is(err, ErrRequestTimeout)
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

// imageInfo represents a single image in an album.
type imageInfo struct {
	URLList []string `json:"url_list"` // List of image URLs (different sizes)
	Width   int      `json:"width"`    // Image width
	Height  int      `json:"height"`   // Image height
}

// buildDouyinItem converts an API response item to a DouyinItem.
// It determines the content type (video vs album) and extracts relevant fields.
// For albums (AwemeType 68 or items with images), it extracts image URLs.
// For videos, it builds stream information with multiple quality options.
func buildDouyinItem(item itemInfoItem) *DouyinItem {
	// Determine content type: AwemeType 68 indicates an album (image collection).
	// Also treat items with images array as albums.
	isAlbum := item.AwemeType == 68 || len(item.Images) > 0

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
		result.Images = buildImages(item.Images)
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
// Falls back to constructing URLs using the video URI if BitRate is empty.
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

		// Fallback: construct URLs using video URI.
		// The URI can be used to build quality-specific playback URLs.
		uri := video.PlayAddr.URI
		if uri != "" {
			logger.Debug("[Douyin] Using URI to construct quality URLs: %s", uri)
			// Build streams for common quality levels: 540p, 720p, 1080p.
			// URL format: https://aweme.snssdk.com/aweme/v1/play/?video_id={uri}&ratio={quality}&line=0
			qualityOptions := []struct {
				ratio  string
				width  int
				height int
			}{
				{"1080p", 1920, 1080},
				{"720p", 1280, 720},
				{"540p", 960, 540},
			}

			for _, opt := range qualityOptions {
				constructedURL := fmt.Sprintf("https://aweme.snssdk.com/aweme/v1/play/?video_id=%s&ratio=%s&line=0", uri, opt.ratio)
				streams = append(streams, Stream{
					QualityKey:  opt.ratio,
					QualityName: opt.ratio,
					Width:       opt.width,
					Height:      opt.height,
					URL:         constructedURL,
				})
				logger.Debug("[Douyin] Constructed stream: %s (%dx%d)", opt.ratio, opt.width, opt.height)
			}
		}

		// Final fallback: use the original PlayAddr URL if URI is empty.
		if len(streams) == 0 {
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
// Used for album content to extract image URLs and dimensions.
func buildImages(images []imageInfo) []Image {
	out := make([]Image, 0, len(images))
	for _, img := range images {
		url := firstURL(img.URLList)
		if url == "" {
			continue
		}
		out = append(out, Image{
			URL:    url,
			Width:  img.Width,
			Height: img.Height,
		})
	}
	return out
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

// sortStreamsByResolution sorts streams by resolution (highest first).
// For equal resolutions, sorts by bitrate (highest first).
func sortStreamsByResolution(streams []Stream) {
	sort.SliceStable(streams, func(i, j int) bool {
		ri := resolutionValue(streams[i].Width, streams[i].Height)
		rj := resolutionValue(streams[j].Width, streams[j].Height)
		if ri == rj {
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
	// Second pass: return any non-empty URL.
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
