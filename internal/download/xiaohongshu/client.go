package xiaohongshu

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/andybalholm/brotli"
)

var (
	// ErrInitialStateNotFound indicates __INITIAL_STATE__ was not found.
	ErrInitialStateNotFound = errors.New("xhs initial state not found")
	// ErrNoteNotFound indicates the note was not found.
	ErrNoteNotFound = errors.New("xhs note not found")
	// ErrNoteBlocked indicates the note is blocked or requires login.
	ErrNoteBlocked = errors.New("xhs note blocked or login required")
	// ErrRateLimited indicates rate limiting.
	ErrRateLimited = errors.New("xhs request rate limited")
	// ErrResponseTooLarge indicates the response body exceeded the safety limit.
	ErrResponseTooLarge = errors.New("xhs response too large")
)

const (
	defaultUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0 Safari/537.36"
	defaultBaseURL   = "https://www.xiaohongshu.com"
	defaultReferer   = "https://www.xiaohongshu.com/"
	// maxHTMLBytes is a safety cap for the explore page HTML after decompression.
	maxHTMLBytes = 10 * 1024 * 1024
)

// Client fetches XiaoHongShu note info by scraping the explore page HTML.
type Client struct {
	httpClient *http.Client
	userAgent  string
	baseURL    string
	referer    string
}

// NewClient creates a new client with defaults.
func NewClient() *Client {
	return NewClientWithClient(nil)
}

// NewClientWithClient creates a new client with a custom HTTP client.
func NewClientWithClient(client *http.Client) *Client {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return &Client{
		httpClient: client,
		userAgent:  defaultUserAgent,
		baseURL:    defaultBaseURL,
		referer:    defaultReferer,
	}
}

// SetBaseURL sets a custom base URL.
func (c *Client) SetBaseURL(baseURL string) {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		return
	}
	c.baseURL = strings.TrimRight(baseURL, "/")
}

// SetUserAgent sets a custom User-Agent.
func (c *Client) SetUserAgent(ua string) {
	ua = strings.TrimSpace(ua)
	if ua == "" {
		return
	}
	c.userAgent = ua
}

// SetHTTPClient sets a custom HTTP client.
func (c *Client) SetHTTPClient(client *http.Client) {
	if client == nil {
		return
	}
	c.httpClient = client
}

// GetNoteInfo fetches note information by scraping the explore page.
func (c *Client) GetNoteInfo(noteID string) (*XHSItem, error) {
	return c.GetNoteInfoWithToken(noteID, "")
}

// GetNoteInfoWithToken fetches note information with an optional xsec_token.
// The xsec_token is required by XiaoHongShu for most shared links.
func (c *Client) GetNoteInfoWithToken(noteID, xsecToken string) (*XHSItem, error) {
	noteID = strings.ToLower(strings.TrimSpace(noteID))
	if noteID == "" || !noteIDExact.MatchString(noteID) {
		return nil, ErrInvalidNoteID
	}

	noteURL, err := c.buildExploreURL(noteID, xsecToken)
	if err != nil {
		return nil, err
	}

	req, err := c.newExploreRequest(noteURL)
	if err != nil {
		return nil, err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if err := mapNoteStatus(resp.StatusCode); err != nil {
		return nil, err
	}

	body, err := readResponseBody(resp, maxHTMLBytes)
	if err != nil {
		return nil, err
	}

	// Detect typical error pages embedded in HTML.
	if bytes.Contains(body, []byte("error_code")) || bytes.Contains(body, []byte("当前笔记暂时无法浏览")) {
		return nil, ErrNoteBlocked
	}

	stateJSON, err := extractJSONObjectAfterMarker(body, []byte("window.__INITIAL_STATE__"))
	if err != nil {
		return nil, err
	}

	// Convert JavaScript object notation to valid JSON.
	// Only a small subset is handled (undefined/void 0 and trailing commas).
	stateJSON = jsToJSON(stateJSON)

	var state map[string]any
	if err := json.Unmarshal(stateJSON, &state); err != nil {
		return nil, err
	}

	noteObj, ok := lookupNote(state, noteID)
	if !ok {
		return nil, ErrNoteNotFound
	}

	// Validate required fields exist in parsed data
	if err := validateNoteData(noteObj); err != nil {
		return nil, fmt.Errorf("invalid note data: %w", err)
	}

	return buildItemFromNote(noteID, noteObj), nil
}

func (c *Client) buildExploreURL(noteID, xsecToken string) (string, error) {
	// Use /explore/ path for fetching note details.
	noteURL, err := url.JoinPath(strings.TrimRight(c.baseURL, "/"), "/explore", noteID)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(xsecToken) != "" {
		noteURL = noteURL + "?xsec_token=" + url.QueryEscape(strings.TrimSpace(xsecToken))
	}
	return noteURL, nil
}

func (c *Client) newExploreRequest(noteURL string) (*http.Request, error) {
	req, err := http.NewRequest(http.MethodGet, noteURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Referer", c.referer)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
	req.Header.Set("Accept-Encoding", "gzip, deflate, br")
	req.Header.Set("Connection", "keep-alive")
	req.Header.Set("Upgrade-Insecure-Requests", "1")
	req.Header.Set("Sec-Fetch-Dest", "document")
	req.Header.Set("Sec-Fetch-Mode", "navigate")
	req.Header.Set("Sec-Fetch-Site", "none")
	req.Header.Set("Sec-Fetch-User", "?1")
	req.Header.Set("Cache-Control", "max-age=0")
	return req, nil
}

func mapNoteStatus(statusCode int) error {
	switch statusCode {
	case http.StatusOK:
		return nil
	case http.StatusNotFound:
		return ErrNoteNotFound
	case http.StatusTooManyRequests:
		return ErrRateLimited
	case http.StatusUnauthorized, http.StatusForbidden:
		return ErrNoteBlocked
	default:
		if statusCode >= 200 && statusCode < 300 {
			return nil
		}
		return fmt.Errorf("unexpected status %d", statusCode)
	}
}

func readResponseBody(resp *http.Response, maxBytes int64) ([]byte, error) {
	// Handle Content-Encoding for compressed responses.
	enc := strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Encoding")))
	if i := strings.Index(enc, ","); i >= 0 {
		enc = strings.TrimSpace(enc[:i])
	}
	switch enc {
	case "gzip":
		gz, err := gzip.NewReader(resp.Body)
		if err != nil {
			return nil, err
		}
		defer gz.Close()
		return readLimited(gz, maxBytes)
	case "br":
		return readLimited(brotli.NewReader(resp.Body), maxBytes)
	case "deflate":
		// Servers vary between raw deflate (RFC 1951) and zlib-wrapped (RFC 1950).
		// We read the compressed payload first, then try zlib and fall back to raw.
		compressed, err := readLimited(resp.Body, maxBytes)
		if err != nil {
			return nil, err
		}
		return inflateDeflate(compressed, maxBytes)
	default:
		return readLimited(resp.Body, maxBytes)
	}
}

func readLimited(r io.Reader, maxBytes int64) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > maxBytes {
		return nil, fmt.Errorf("%w: limit %d bytes", ErrResponseTooLarge, maxBytes)
	}
	return b, nil
}

func inflateDeflate(compressed []byte, maxOutputBytes int64) ([]byte, error) {
	if len(compressed) == 0 {
		return nil, nil
	}

	zr, err := zlib.NewReader(bytes.NewReader(compressed))
	if err == nil {
		defer zr.Close()
		return readLimited(zr, maxOutputBytes)
	}

	fr := flate.NewReader(bytes.NewReader(compressed))
	defer fr.Close()
	return readLimited(fr, maxOutputBytes)
}

func buildItemFromNote(noteID string, noteObj map[string]any) *XHSItem {
	item := &XHSItem{ID: noteID}
	item.Title = firstNonEmpty(getString(noteObj, "title"), getString(noteObj, "desc"))
	item.Desc = strings.TrimSpace(getString(noteObj, "desc"))
	item.Timestamp = firstNonZeroInt64(getInt64(noteObj, "time"), getInt64(noteObj, "lastUpdateTime"))

	if user := getMap(noteObj, "user"); user != nil {
		item.Author = getString(user, "nickname")
		item.AuthorID = getString(user, "userId")
		item.AuthorAvatar = firstNonEmpty(getString(user, "avatar"), getString(user, "avatarUrl"))
	}

	images := parseImages(noteObj)
	streams := parseVideoStreams(noteObj)
	noteType := strings.ToLower(getString(noteObj, "type"))
	isVideo := noteType == "video" || len(streams) > 0

	if isVideo {
		item.Type = "video"
		item.Streams = streams
		if len(images) > 0 {
			item.Cover = images[0].URL
		} else {
			item.Cover = getString(noteObj, "cover")
		}
		return item
	}

	if len(images) > 0 {
		item.Type = "image"
		item.Images = images
		item.Cover = images[0].URL
		return item
	}

	item.Type = "unknown"
	item.Cover = getString(noteObj, "cover")
	return item
}

func lookupNote(state map[string]any, noteID string) (map[string]any, bool) {
	note := getMap(state, "note")
	if note == nil {
		return nil, false
	}
	detailMap := getMap(note, "noteDetailMap")
	if detailMap == nil {
		return nil, false
	}
	entryAny, ok := detailMap[noteID]
	if !ok {
		return nil, false
	}
	entry, ok := entryAny.(map[string]any)
	if !ok {
		return nil, false
	}
	noteObj := getMap(entry, "note")
	if noteObj == nil {
		return nil, false
	}
	return noteObj, true
}

// validateNoteData checks that required fields exist and have valid types.
// Returns an error if the note data appears malformed or incomplete.
func validateNoteData(noteObj map[string]any) error {
	if noteObj == nil {
		return errors.New("note object is nil")
	}
	// At minimum, we need either title/desc or some content indicator
	title := getString(noteObj, "title")
	desc := getString(noteObj, "desc")
	noteType := getString(noteObj, "type")
	if title == "" && desc == "" && noteType == "" {
		return errors.New("note missing title, desc, and type")
	}
	// Validate user field if present
	if userAny, ok := noteObj["user"]; ok && userAny != nil {
		if _, ok := userAny.(map[string]any); !ok {
			return errors.New("user field has invalid type")
		}
	}
	// Validate imageList if present
	if imgListAny, ok := noteObj["imageList"]; ok && imgListAny != nil {
		if _, ok := imgListAny.([]any); !ok {
			return errors.New("imageList field has invalid type")
		}
	}
	// Validate video if present
	if videoAny, ok := noteObj["video"]; ok && videoAny != nil {
		if _, ok := videoAny.(map[string]any); !ok {
			return errors.New("video field has invalid type")
		}
	}
	return nil
}

func parseImages(noteObj map[string]any) []XHSImage {
	listAny, ok := noteObj["imageList"]
	if !ok {
		return nil
	}
	list, ok := listAny.([]any)
	if !ok {
		return nil
	}
	out := make([]XHSImage, 0, len(list))
	for _, el := range list {
		m, ok := el.(map[string]any)
		if !ok {
			continue
		}
		u := firstNonEmpty(
			getString(m, "urlDefault"),
			getString(m, "urlPre"),
			getString(m, "url"),
			firstURLFromInfoList(m["infoList"]),
		)
		if u == "" {
			continue
		}
		img := XHSImage{URL: u, TraceId: getString(m, "traceId")}
		if w, ok := m["width"].(float64); ok {
			img.Width = int(w)
		}
		if h, ok := m["height"].(float64); ok {
			img.Height = int(h)
		}
		out = append(out, img)
	}
	return out
}

func parseVideoStreams(noteObj map[string]any) []XHSStream {
	video := getMap(noteObj, "video")
	if video == nil {
		return nil
	}
	media := getMap(video, "media")
	if media == nil {
		return nil
	}
	streamAny, ok := media["stream"]
	if !ok {
		return nil
	}

	candidates := make([]map[string]any, 0, 8)

	// Old schema: stream is an array.
	if streamList, ok := streamAny.([]any); ok {
		for _, el := range streamList {
			m, ok := el.(map[string]any)
			if ok {
				candidates = append(candidates, m)
			}
		}
	}

	// New schema: stream is an object keyed by codec (e.g. h264/h265/av1).
	if streamMap, ok := streamAny.(map[string]any); ok {
		for _, codecAny := range streamMap {
			list, ok := codecAny.([]any)
			if !ok {
				continue
			}
			for _, el := range list {
				m, ok := el.(map[string]any)
				if ok {
					candidates = append(candidates, m)
				}
			}
		}
	}

	if len(candidates) == 0 {
		return nil
	}

	bestByKey := make(map[string]XHSStream)
	for _, m := range candidates {
		stream, ok := parseStreamCandidate(m)
		if !ok {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(stream.QualityKey))
		if key == "" {
			key = "default"
			stream.QualityKey = key
		}
		if existing, ok := bestByKey[key]; !ok || streamBetterThan(stream, existing) {
			bestByKey[key] = stream
		}
	}

	if len(bestByKey) == 0 {
		return nil
	}

	out := make([]XHSStream, 0, len(bestByKey))
	for _, s := range bestByKey {
		out = append(out, s)
	}
	sortStreams(out)
	return out
}

func extractJSONObjectAfterMarker(html []byte, marker []byte) ([]byte, error) {
	idx := bytes.Index(html, marker)
	if idx < 0 {
		return nil, ErrInitialStateNotFound
	}
	start := bytes.IndexByte(html[idx:], '{')
	if start < 0 {
		return nil, ErrInitialStateNotFound
	}
	start += idx

	depth := 0
	inString := false
	escape := false
	for i := start; i < len(html); i++ {
		b := html[i]
		if inString {
			if escape {
				escape = false
				continue
			}
			if b == '\\' {
				escape = true
				continue
			}
			if b == '"' {
				inString = false
			}
			continue
		}

		switch b {
		case '"':
			inString = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return html[start : i+1], nil
			}
		}
	}
	return nil, ErrInitialStateNotFound
}

func getMap(m map[string]any, key string) map[string]any {
	if m == nil {
		return nil
	}
	v, ok := m[key]
	if !ok {
		return nil
	}
	out, _ := v.(map[string]any)
	return out
}

func getString(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	v, ok := m[key]
	if !ok {
		return ""
	}
	s, _ := v.(string)
	return strings.TrimSpace(s)
}

func getInt64(m map[string]any, key string) int64 {
	if m == nil {
		return 0
	}
	v, ok := m[key]
	if !ok || v == nil {
		return 0
	}
	switch n := v.(type) {
	case int64:
		return n
	case int:
		return int64(n)
	case float64:
		return int64(n)
	case json.Number:
		if i, err := n.Int64(); err == nil {
			return i
		}
	}
	return 0
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func firstStringFromSlice(v any) string {
	list, ok := v.([]any)
	if !ok {
		return ""
	}
	for _, el := range list {
		s, ok := el.(string)
		if ok && strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

func firstNonZeroInt64(values ...int64) int64 {
	for _, v := range values {
		if v != 0 {
			return v
		}
	}
	return 0
}

func firstURLFromInfoList(v any) string {
	list, ok := v.([]any)
	if !ok {
		return ""
	}
	for _, el := range list {
		m, ok := el.(map[string]any)
		if !ok {
			continue
		}
		if u := strings.TrimSpace(getString(m, "url")); u != "" {
			return u
		}
	}
	return ""
}

func parseStreamCandidate(m map[string]any) (XHSStream, bool) {
	qualityName := firstNonEmpty(getString(m, "qualityType"), getString(m, "quality"))
	qualityKey := strings.ToLower(strings.TrimSpace(qualityName))
	u := getString(m, "masterUrl")
	if u == "" {
		u = firstStringFromSlice(m["backupUrls"])
	}
	if strings.TrimSpace(u) == "" {
		return XHSStream{}, false
	}

	return XHSStream{
		QualityKey:  firstNonEmpty(qualityKey, qualityName),
		QualityName: qualityName,
		Width:       int(getInt64(m, "width")),
		Height:      int(getInt64(m, "height")),
		URL:         strings.TrimSpace(u),
		Size:        getInt64(m, "size"),
		Format:      getString(m, "format"),
	}, true
}

func streamBetterThan(a, b XHSStream) bool {
	aPixels := int64(a.Width) * int64(a.Height)
	bPixels := int64(b.Width) * int64(b.Height)
	if aPixels != bPixels {
		return aPixels > bPixels
	}
	if a.Size != b.Size {
		return a.Size > b.Size
	}
	return a.URL != "" && b.URL == ""
}

func sortStreams(streams []XHSStream) {
	if len(streams) < 2 {
		return
	}
	// Simple in-place sort: larger resolution/size first.
	for i := 0; i < len(streams)-1; i++ {
		for j := i + 1; j < len(streams); j++ {
			if streamBetterThan(streams[j], streams[i]) {
				streams[i], streams[j] = streams[j], streams[i]
			}
		}
	}
}

// jsToJSON converts JavaScript object notation to valid JSON.
// It handles common JavaScript-specific syntax that isn't valid JSON:
// - undefined -> null
// - void 0 -> null
// - trailing commas in arrays/objects
func jsToJSON(js []byte) []byte {
	s := string(js)

	// Replace undefined and void 0 with null (outside of strings)
	// This is a simplified approach that handles common cases
	s = replaceJSKeywords(s)

	return []byte(s)
}

// replaceJSKeywords replaces JavaScript keywords with JSON equivalents.
// It's careful to only replace keywords that are values, not inside strings.
func replaceJSKeywords(s string) string {
	// Pattern to match undefined or void 0 as a value (not inside a string)
	// We look for these patterns after : or , or [ or at the start
	patterns := []struct {
		re   *regexp.Regexp
		repl string
	}{
		// undefined as a value
		{regexp.MustCompile(`([:,\[{]\s*)undefined(\s*[,}\]])`), "${1}null${2}"},
		{regexp.MustCompile(`([:,\[{]\s*)undefined(\s*)$`), "${1}null${2}"},
		// void 0 as a value
		{regexp.MustCompile(`([:,\[{]\s*)void\s+0(\s*[,}\]])`), "${1}null${2}"},
		{regexp.MustCompile(`([:,\[{]\s*)void\s+0(\s*)$`), "${1}null${2}"},
	}

	result := s
	for _, p := range patterns {
		// Apply multiple times to handle consecutive occurrences
		for i := 0; i < 10; i++ {
			newResult := p.re.ReplaceAllString(result, p.repl)
			if newResult == result {
				break
			}
			result = newResult
		}
	}

	// Handle trailing commas before } or ]
	trailingComma := regexp.MustCompile(`,(\s*[}\]])`)
	for i := 0; i < 10; i++ {
		newResult := trailingComma.ReplaceAllString(result, "${1}")
		if newResult == result {
			break
		}
		result = newResult
	}

	return result
}
