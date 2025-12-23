// Package douyin provides functionality for parsing Douyin (TikTok China) share links
// and extracting video/album information. It supports resolving short links, extracting
// aweme_id from various URL formats, and fetching video metadata from Douyin's API.
package douyin

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// Error definitions for URL parsing operations.
var (
	// ErrNoDouyinURL indicates that no valid Douyin URL was found in the input text.
	ErrNoDouyinURL = errors.New("no douyin url found in input")
	// ErrAwemeIDNotFound indicates that the aweme_id could not be extracted from the URL.
	ErrAwemeIDNotFound = errors.New("aweme_id not found")
	// ErrShortLinkResolution indicates that a short link (e.g., v.douyin.com) could not be resolved.
	ErrShortLinkResolution = errors.New("douyin short link resolution failed")
	// ErrInvalidDouyinURL indicates that the URL is not a valid Douyin URL.
	ErrInvalidDouyinURL = errors.New("invalid douyin url")
)

// shareURLPattern matches any HTTP/HTTPS URL in text.
// Used to extract URLs from share text that may contain additional content like descriptions.
// Example: "Check this out https://v.douyin.com/abc123/ #funny" -> extracts the URL.
var shareURLPattern = regexp.MustCompile(`https?://[^\s]+`)

// Parser resolves Douyin share links and extracts the aweme_id (unique video/note identifier).
// It handles multiple URL formats:
//   - Short links: https://v.douyin.com/xxxxx/
//   - Direct video URLs: https://www.douyin.com/video/1234567890
//   - Note URLs: https://www.douyin.com/note/1234567890
//   - Slides share URLs: https://www.iesdouyin.com/share/slides/1234567890/
//   - Modal URLs with query params: https://www.douyin.com/discover?modal_id=1234567890
type Parser struct {
	// client is configured to NOT follow redirects automatically,
	// allowing manual extraction of Location headers from short link responses.
	client *http.Client
	// shortDomains contains known Douyin short-link domains (e.g., "v.douyin.com").
	// URLs from these domains require resolution to get the actual video URL.
	shortDomains map[string]struct{}
}

// NewParser creates a Parser with a default HTTP client that has redirect following disabled.
// Redirects are disabled because short links (v.douyin.com) respond with 3xx status codes,
// and we need to capture the Location header to extract the actual video URL.
func NewParser() *Parser {
	client := &http.Client{
		Timeout: 10 * time.Second,
		// CheckRedirect returns ErrUseLastResponse to prevent automatic redirect following.
		// This allows us to manually read the Location header from 3xx responses.
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	return &Parser{
		client:       client,
		shortDomains: map[string]struct{}{"v.douyin.com": {}},
	}
}

// NewParserWithClient creates a Parser using a provided HTTP client.
// If the client is nil, falls back to NewParser() with default settings.
// If the client lacks a CheckRedirect function, a shallow copy is made
// and configured to disable automatic redirects (required for short link resolution).
func NewParserWithClient(client *http.Client) *Parser {
	if client == nil {
		return NewParser()
	}
	if client.CheckRedirect == nil {
		client = cloneClient(client)
		client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		}
	}

	return &Parser{
		client:       client,
		shortDomains: map[string]struct{}{"v.douyin.com": {}},
	}
}

// cloneClient creates a shallow copy of an HTTP client.
// Used when we need to modify client settings (like CheckRedirect) without affecting the original.
func cloneClient(c *http.Client) *http.Client {
	copy := *c
	return &copy
}

// SetShortDomains overrides the known short-link domains.
// By default, only "v.douyin.com" is recognized as a short-link domain.
// This method is primarily useful for testing with mock servers.
// If an empty list is provided, defaults back to "v.douyin.com".
func (p *Parser) SetShortDomains(domains []string) {
	p.shortDomains = make(map[string]struct{})
	for _, d := range domains {
		d = strings.TrimSpace(strings.ToLower(d))
		if d != "" {
			p.shortDomains[d] = struct{}{}
		}
	}
	if len(p.shortDomains) == 0 {
		p.shortDomains["v.douyin.com"] = struct{}{}
	}
}

// Parse extracts the aweme_id from Douyin share text or URLs.
// It accepts various input formats:
//   - Raw URLs: "https://www.douyin.com/video/1234567890"
//   - Share text with URL: "Check this video 7.65 Ohm:/e 点击进入直播间 https://v.douyin.com/xxxxx/"
//   - Short links that need resolution: "https://v.douyin.com/xxxxx/"
//
// Returns the numeric aweme_id string on success, or an error if parsing fails.
func (p *Parser) Parse(input string) (string, error) {
	link, err := p.extractShareURL(input)
	if err != nil {
		return "", err
	}
	return p.parseURL(link, 0)
}

// maxRedirectDepth limits the number of redirect hops when resolving short links.
// This prevents infinite redirect loops.
const maxRedirectDepth = 5

// parseURL recursively parses a URL to extract the aweme_id.
// It handles short links by resolving them first, then extracts the ID from the final URL.
// The depth parameter tracks redirect count to prevent infinite loops.
func (p *Parser) parseURL(raw string, depth int) (string, error) {
	if depth > maxRedirectDepth {
		return "", fmt.Errorf("%w: too many redirects", ErrShortLinkResolution)
	}

	cleaned := strings.TrimSpace(raw)
	u, err := url.Parse(cleaned)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("%w: %v", ErrInvalidDouyinURL, err)
	}

	hostname := strings.ToLower(u.Hostname())

	// If the URL is from a short-link domain (e.g., v.douyin.com),
	// resolve it to get the actual video URL, then recursively parse.
	if p.isShortDomain(hostname) {
		next, err := p.resolveShortLink(u.String())
		if err != nil {
			return "", err
		}
		return p.parseURL(next, depth+1)
	}

	// Verify this is a Douyin domain before attempting to extract aweme_id.
	if !strings.Contains(hostname, "douyin.com") {
		return "", ErrInvalidDouyinURL
	}

	// Extract aweme_id from the URL path or query parameters.
	if id := extractAwemeID(u); id != "" {
		return id, nil
	}

	return "", ErrAwemeIDNotFound
}

// isShortDomain checks if the given hostname is a known Douyin short-link domain.
// Short-link domains (like v.douyin.com) serve redirect responses that point to full video URLs.
func (p *Parser) isShortDomain(host string) bool {
	if host == "" {
		return false
	}
	_, ok := p.shortDomains[strings.ToLower(host)]
	return ok
}

// resolveShortLink sends a GET request to the short URL and extracts the redirect Location.
// Douyin short links respond with HTTP 3xx status codes and a Location header
// containing the full video URL (e.g., https://www.douyin.com/video/1234567890).
// Returns the resolved URL or an error if resolution fails.
func (p *Parser) resolveShortLink(shortURL string) (string, error) {
	req, err := http.NewRequest("GET", shortURL, nil)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrShortLinkResolution, err)
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrShortLinkResolution, err)
	}
	defer resp.Body.Close()

	// Short link resolution expects a 3xx redirect status code.
	// Status < 300 means no redirect; status >= 400 is an error.
	if resp.StatusCode < 300 || resp.StatusCode >= 400 {
		return "", fmt.Errorf("%w: unexpected status %d", ErrShortLinkResolution, resp.StatusCode)
	}

	// Extract the Location header which contains the redirect target URL.
	loc, err := resp.Location()
	if err != nil {
		return "", fmt.Errorf("%w: missing location header", ErrShortLinkResolution)
	}

	// Handle relative URLs by resolving them against the request URL.
	target := loc
	if !loc.IsAbs() {
		target = req.URL.ResolveReference(loc)
	}

	return target.String(), nil
}

// extractShareURL extracts the first valid Douyin URL from input text.
// Input can be:
//   - A raw URL: "https://www.douyin.com/video/1234567890"
//   - Share text containing a URL mixed with other content
//
// Returns the cleaned URL or ErrNoDouyinURL if no valid URL is found.
func (p *Parser) extractShareURL(input string) (string, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return "", ErrNoDouyinURL
	}

	// Fast path: if input looks like a URL and is from an acceptable host, use it directly.
	if looksLikeURL(input) {
		u, err := url.Parse(trimURL(input))
		if err == nil && p.isAcceptableHost(u.Hostname()) {
			return u.String(), nil
		}
	}

	// Slow path: search for URLs in the text using regex.
	// This handles share text like "Check this out https://v.douyin.com/xxx/ #funny"
	matches := shareURLPattern.FindAllString(input, -1)
	for _, m := range matches {
		trimmed := trimURL(m)
		parsed, err := url.Parse(trimmed)
		if err != nil {
			continue
		}
		if p.isAcceptableHost(parsed.Hostname()) {
			return parsed.String(), nil
		}
	}

	return "", ErrNoDouyinURL
}

// isAcceptableHost checks if a hostname is valid for Douyin content.
// Accepts any domain containing "douyin.com" or known short-link domains.
func (p *Parser) isAcceptableHost(host string) bool {
	host = strings.ToLower(host)
	if host == "" {
		return false
	}
	if strings.Contains(host, "douyin.com") {
		return true
	}
	return p.isShortDomain(host)
}

// looksLikeURL performs a quick check if a string appears to be a URL.
// Only checks for http:// or https:// prefix without full URL validation.
func looksLikeURL(v string) bool {
	v = strings.TrimSpace(v)
	return strings.HasPrefix(v, "http://") || strings.HasPrefix(v, "https://")
}

// trimURL removes common surrounding characters from URLs.
// Handles characters that may be included when URLs are copied from text,
// such as quotes, brackets, parentheses, and Chinese punctuation.
func trimURL(v string) string {
	return strings.Trim(v, " \t\r\n\"'<>[]{}（）()，,。!?")
}

// extractAwemeID extracts the numeric aweme_id from a parsed Douyin URL.
// It checks multiple locations where the ID might appear:
//
// Path-based extraction (highest priority):
//   - /video/1234567890 -> extracts "1234567890"
//   - /note/1234567890 -> extracts "1234567890"
//
// Query parameter extraction (fallback):
//   - ?modal_id=1234567890 -> extracts "1234567890"
//   - ?aweme_id=1234567890 -> extracts "1234567890"
//
// Returns empty string if no valid ID is found.
func extractAwemeID(u *url.URL) string {
	// Remove trailing slash and split path into segments.
	path := strings.TrimSuffix(u.Path, "/")
	segments := strings.Split(path, "/")

	// Look for known content segments followed by the aweme_id.
	// Examples:
	//   - /video/1234567890 -> segments = ["", "video", "1234567890"]
	//   - /note/1234567890 -> segments = ["", "note", "1234567890"]
	//   - /share/slides/1234567890 -> segments = ["", "share", "slides", "1234567890"]
	for i := 0; i < len(segments)-1; i++ {
		if segments[i] == "video" || segments[i] == "note" || segments[i] == "slides" {
			if id := keepDigits(segments[i+1]); id != "" {
				return id
			}
		}
	}

	// Fallback: check query parameters for modal_id or aweme_id.
	// These are used in discovery pages and share links.
	q := u.Query()
	for _, key := range []string{"modal_id", "aweme_id"} {
		if id := keepDigits(q.Get(key)); id != "" {
			return id
		}
	}

	return ""
}

// keepDigits extracts only numeric digits from a string.
// Used to clean aweme_id values that may contain non-numeric characters.
// Returns empty string if no digits are found.
func keepDigits(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}

	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return ""
	}
	return b.String()
}
