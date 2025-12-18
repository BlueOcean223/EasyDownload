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

var (
	ErrInvalidAwemeID = errors.New("invalid aweme_id")
	ErrItemNotFound   = errors.New("douyin item not found")
	ErrRateLimited    = errors.New("douyin api rate limited")
	ErrRequestTimeout = errors.New("douyin api request timeout")
	ErrAPIError       = errors.New("douyin api error")
)

const (
	defaultUserAgent = "Mozilla/5.0 (iPhone; CPU iPhone OS 16_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/16.5 Mobile/15E148 Safari/604.1"
	defaultBaseURL   = "https://www.iesdouyin.com/aweme/v1/web/aweme/detail/"
)

var shareRouterDataPattern = regexp.MustCompile(`(?s)window\._ROUTER_DATA\s*=\s*({.*?})\s*;?\s*</script>`)

type Client struct {
	httpClient *http.Client
	userAgent  string
	baseURL    string
	shareBase  string
}

func NewClient() *Client {
	return NewClientWithClient(nil)
}

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

func (c *Client) SetUserAgent(ua string) {
	ua = strings.TrimSpace(ua)
	if ua == "" {
		return
	}
	c.userAgent = ua
}

func (c *Client) SetHTTPClient(client *http.Client) {
	if client == nil {
		return
	}
	c.httpClient = client
}

func (c *Client) SetBaseURL(baseURL string) {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		return
	}
	c.baseURL = baseURL
}

func (c *Client) SetShareBaseURL(baseURL string) {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		return
	}
	c.shareBase = baseURL
}

func (c *Client) GetItemInfo(awemeID string) (*DouyinItem, error) {
	awemeID = strings.TrimSpace(awemeID)
	if awemeID == "" {
		return nil, ErrInvalidAwemeID
	}

	item, apiErr := c.fetchItemInfoAPI(awemeID)
	if apiErr == nil {
		c.fetchStreamSizes(item)
		return item, nil
	}

	item, shareErr := c.fetchItemInfoFromSharePage(awemeID)
	if shareErr == nil {
		c.fetchStreamSizes(item)
		return item, nil
	}

	if shouldReturnAPIError(apiErr) {
		return nil, apiErr
	}
	return nil, fmt.Errorf("douyin api error: %w; share page error: %v", apiErr, shareErr)
}

// fetchStreamSizes fetches file sizes for all streams via HEAD requests
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
			logger.Info("[Douyin] Stream %s size: %d bytes", item.Streams[i].QualityKey, size)
		}
	}
}

// fetchContentLength gets file size via HEAD request
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

	// 先尝试新API格式 (aweme_detail)
	var detailPayload awemeDetailResponse
	if err := json.Unmarshal(body, &detailPayload); err == nil {
		if detailPayload.StatusCode == 0 && detailPayload.AwemeDetail != nil {
			logger.Info("[Douyin] Using new API format (aweme_detail)")
			return buildDouyinItem(*detailPayload.AwemeDetail), nil
		}
	}

	// 降级到旧API格式 (item_list)
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

	logger.Info("[Douyin] Using legacy API format (item_list)")
	return buildDouyinItem(payload.ItemList[0]), nil
}

func (c *Client) fetchItemInfoFromSharePage(awemeID string) (*DouyinItem, error) {
	shareBase := strings.TrimRight(strings.TrimSpace(c.shareBase), "/")
	if shareBase == "" {
		shareBase = "https://www.iesdouyin.com/share"
	}
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

func shouldReturnAPIError(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, ErrItemNotFound) ||
		errors.Is(err, ErrAPIError) ||
		errors.Is(err, ErrRateLimited) ||
		errors.Is(err, ErrRequestTimeout)
}

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

func parseSharePageItem(body []byte) (*itemInfoItem, error) {
	match := shareRouterDataPattern.FindSubmatch(body)
	if len(match) < 2 {
		return nil, fmt.Errorf("douyin share page router data not found")
	}

	var payload struct {
		LoaderData map[string]json.RawMessage `json:"loaderData"`
	}
	if err := json.Unmarshal(match[1], &payload); err != nil {
		return nil, err
	}

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

type itemInfoResponse struct {
	StatusCode int            `json:"status_code"`
	StatusMsg  string         `json:"status_msg"`
	ItemList   []itemInfoItem `json:"item_list"`
}

type awemeDetailResponse struct {
	StatusCode  int           `json:"status_code"`
	StatusMsg   string        `json:"status_msg"`
	AwemeDetail *itemInfoItem `json:"aweme_detail"`
}

type itemInfoItem struct {
	AwemeID   string      `json:"aweme_id"`
	Desc      string      `json:"desc"`
	AwemeType int         `json:"aweme_type"`
	Duration  int         `json:"duration"`
	Author    authorInfo  `json:"author"`
	Video     videoInfo   `json:"video"`
	Images    []imageInfo `json:"images"`
}

type authorInfo struct {
	Nickname string `json:"nickname"`
	UID      string `json:"uid"`
	SecUID   string `json:"sec_uid"`
	UniqueID string `json:"unique_id"`
}

type videoInfo struct {
	Duration int           `json:"duration"`
	Cover    coverInfo     `json:"cover"`
	PlayAddr playAddr      `json:"play_addr"`
	BitRate  []bitRateInfo `json:"bit_rate"`
	Width    int           `json:"width"`
	Height   int           `json:"height"`
}

type coverInfo struct {
	URLList []string `json:"url_list"`
}

type playAddr struct {
	URLList []string `json:"url_list"`
	Width   int      `json:"width"`
	Height  int      `json:"height"`
	URI     string   `json:"uri"` // video_id for constructing high quality URLs
}

type bitRateInfo struct {
	GearName    string   `json:"gear_name"`
	QualityType int      `json:"quality_type"`
	BitRate     int      `json:"bit_rate"`
	PlayAddr    playAddr `json:"play_addr"`
}

type imageInfo struct {
	URLList []string `json:"url_list"`
	Width   int      `json:"width"`
	Height  int      `json:"height"`
}

func buildDouyinItem(item itemInfoItem) *DouyinItem {
	isAlbum := item.AwemeType == 68 || len(item.Images) > 0

	result := &DouyinItem{
		ID:       item.AwemeID,
		Title:    item.Desc,
		Author:   item.Author.Nickname,
		AuthorID: firstNonEmpty(item.Author.UID, item.Author.SecUID, item.Author.UniqueID),
	}

	logger.Info("[Douyin] Parsing item: ID=%s, Title=%s, Author=%s, AwemeType=%d",
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
		logger.Info("[Douyin] Album detected with %d images", len(result.Images))
		return result
	}

	result.Duration = normalizeDuration(firstNonZero(item.Video.Duration, item.Duration))
	result.Streams = buildStreams(item.Video)

	// Log available streams for quality debugging
	logger.Info("[Douyin] Video streams available: %d", len(result.Streams))
	for i, s := range result.Streams {
		logger.Info("[Douyin]   Stream[%d]: QualityKey=%s, Resolution=%dx%d, Bitrate=%d, URL=%s",
			i, s.QualityKey, s.Width, s.Height, s.Bitrate, truncateURL(s.URL, 80))
	}

	return result
}

func buildStreams(video videoInfo) []Stream {
	logger.Info("[Douyin] Building streams from API response: BitRate count=%d, PlayAddr.Width=%d, PlayAddr.Height=%d",
		len(video.BitRate), video.PlayAddr.Width, video.PlayAddr.Height)

	streams := make([]Stream, 0, len(video.BitRate))
	fallbackWidth := video.Width
	fallbackHeight := video.Height

	// Log raw BitRate info from API
	for i, br := range video.BitRate {
		logger.Info("[Douyin]   Raw BitRate[%d]: GearName=%s, BitRate=%d, QualityType=%d, PlayAddr.Width=%d, PlayAddr.Height=%d",
			i, br.GearName, br.BitRate, br.QualityType, br.PlayAddr.Width, br.PlayAddr.Height)
	}

	for _, br := range video.BitRate {
		url := pickNoWatermarkURL(br.PlayAddr.URLList)
		if url == "" {
			continue
		}

		width := br.PlayAddr.Width
		height := br.PlayAddr.Height
		if width == 0 && height == 0 {
			width = fallbackWidth
			height = fallbackHeight
		}
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

		// Try to use URI to construct multiple quality URLs
		uri := video.PlayAddr.URI
		if uri != "" {
			logger.Info("[Douyin] Using URI to construct quality URLs: %s", uri)
			// Build multiple quality streams using video_id
			// Available ratios: 540p, 720p, 1080p
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
				logger.Info("[Douyin] Constructed stream: %s (%dx%d)", opt.ratio, opt.width, opt.height)
			}
		}

		// If URI is empty or as additional fallback, use the original PlayAddr URL
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
				logger.Info("[Douyin] Fallback stream from PlayAddr: %dx%d, key=%s", width, height, qualityKey)
			}
		}
	}

	sortStreamsByResolution(streams)
	return streams
}

// truncateURL truncates URL for logging purposes
func truncateURL(url string, maxLen int) string {
	if len(url) <= maxLen {
		return url
	}
	return url[:maxLen] + "..."
}

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

var gearResolutionPattern = regexp.MustCompile(`(\d{3,4})p?`)

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

func resolutionKey(width, height int) string {
	res := resolutionValue(width, height)
	if res == 0 {
		return ""
	}
	return fmt.Sprintf("%dp", res)
}

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

func pickNoWatermarkURL(urls []string) string {
	for _, raw := range urls {
		u := strings.TrimSpace(raw)
		if u == "" {
			continue
		}
		if strings.Contains(u, "playwm") {
			return strings.Replace(u, "playwm", "play", 1)
		}
	}
	for _, raw := range urls {
		u := strings.TrimSpace(raw)
		if u != "" {
			return u
		}
	}
	return ""
}

func firstURL(urls []string) string {
	for _, raw := range urls {
		u := strings.TrimSpace(raw)
		if u != "" {
			return u
		}
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func firstNonZero(values ...int) int {
	for _, v := range values {
		if v > 0 {
			return v
		}
	}
	return 0
}

func normalizeDuration(value int) int {
	if value <= 0 {
		return 0
	}
	if value > 1000 {
		return value / 1000
	}
	return value
}

func looksNotFound(msg string) bool {
	msg = strings.ToLower(msg)
	return strings.Contains(msg, "not") && (strings.Contains(msg, "exist") || strings.Contains(msg, "found"))
}

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
