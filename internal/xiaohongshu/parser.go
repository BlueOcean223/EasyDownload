package xiaohongshu

import (
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

var (
	// ErrNoXHSURL indicates that no xiaohongshu url was found in input.
	ErrNoXHSURL = errors.New("no xiaohongshu url found in input")
	// ErrNoteIDNotFound indicates that a note ID could not be extracted.
	ErrNoteIDNotFound = errors.New("note id not found")
	// ErrShortLinkResolution indicates short link resolution failed.
	ErrShortLinkResolution = errors.New("xiaohongshu short link resolution failed")
	// ErrInvalidXHSURL indicates an invalid xiaohongshu url.
	ErrInvalidXHSURL = errors.New("invalid xiaohongshu url")
	// ErrInvalidScheme indicates the URL scheme is not http or https.
	ErrInvalidScheme = errors.New("invalid url scheme: only http and https are allowed")
)

var (
	shareURLPattern = regexp.MustCompile(`https?://[^\s]+`)
	noteIDExact     = regexp.MustCompile(`(?i)^[0-9a-f]{24}$`)
	noteIDPattern   = regexp.MustCompile(`(?i)[0-9a-f]{24}`)
)

// Parser resolves XiaoHongShu share links and extracts note IDs.
type Parser struct {
	client       *http.Client
	shortDomains map[string]struct{}
}

// NewParser creates a Parser with a default HTTP client.
func NewParser() *Parser {
	client := &http.Client{
		Timeout: 10 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	return &Parser{
		client:       client,
		shortDomains: map[string]struct{}{"xhslink.com": {}, "www.xhslink.com": {}},
	}
}

// NewParserWithClient creates a Parser with a custom HTTP client.
func NewParserWithClient(client *http.Client) *Parser {
	if client == nil {
		return NewParser()
	}
	if client.CheckRedirect == nil {
		copy := *client
		client = &copy
		client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		}
	}
	return &Parser{
		client:       client,
		shortDomains: map[string]struct{}{"xhslink.com": {}, "www.xhslink.com": {}},
	}
}

// SetShortDomains overrides the known short-link domains.
func (p *Parser) SetShortDomains(domains []string) {
	p.shortDomains = make(map[string]struct{})
	for _, d := range domains {
		d = strings.TrimSpace(strings.ToLower(d))
		if d != "" {
			p.shortDomains[d] = struct{}{}
		}
	}
	if len(p.shortDomains) == 0 {
		p.shortDomains["xhslink.com"] = struct{}{}
		p.shortDomains["www.xhslink.com"] = struct{}{}
	}
}

// Parse parses XiaoHongShu input and returns the note ID.
func (p *Parser) Parse(input string) (string, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return "", ErrNoXHSURL
	}
	if noteIDExact.MatchString(input) {
		return strings.ToLower(input), nil
	}

	link, err := p.extractShareURL(input)
	if err != nil {
		return "", err
	}
	return p.parseURL(link, 0)
}

// ParseResult contains both the noteID and the full URL with query params
type ParseResult struct {
	NoteID    string
	FullURL   string
	XsecToken string
}

// ParseWithURL parses XiaoHongShu input and returns both noteID and full URL.
// This is needed because xsec_token in query params is required for fetching note details.
func (p *Parser) ParseWithURL(input string) (*ParseResult, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return nil, ErrNoXHSURL
	}
	if noteIDExact.MatchString(input) {
		return &ParseResult{NoteID: strings.ToLower(input)}, nil
	}

	link, err := p.extractShareURL(input)
	if err != nil {
		return nil, err
	}
	return p.parseURLInternal(link, 0)
}

func (p *Parser) parseURLInternal(raw string, depth int) (*ParseResult, error) {
	if depth > maxRedirectDepth {
		return nil, fmt.Errorf("%w: too many redirects", ErrShortLinkResolution)
	}
	raw = trimURL(raw)
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidXHSURL, err)
	}

	// Validate URL scheme - only http and https are allowed
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return nil, ErrInvalidScheme
	}

	host := strings.ToLower(u.Hostname())
	if p.isShortDomain(host) {
		resolved, err := p.resolveShortLink(raw)
		if err != nil {
			return nil, err
		}
		return p.parseURLInternal(resolved, depth+1)
	}
	if !p.isAcceptableHost(host) {
		return nil, ErrInvalidXHSURL
	}

	var noteID string
	if id := strings.ToLower(noteIDPattern.FindString(u.Path)); id != "" {
		noteID = id
	}
	if noteID == "" {
		for _, vs := range u.Query() {
			for _, v := range vs {
				if id := strings.ToLower(noteIDPattern.FindString(v)); id != "" {
					noteID = id
					break
				}
			}
			if noteID != "" {
				break
			}
		}
	}
	if noteID == "" {
		return nil, ErrNoteIDNotFound
	}

	result := &ParseResult{
		NoteID:    noteID,
		FullURL:   raw,
		XsecToken: u.Query().Get("xsec_token"),
	}
	return result, nil
}

const maxRedirectDepth = 5

func (p *Parser) parseURL(raw string, depth int) (string, error) {
	res, err := p.parseURLInternal(raw, depth)
	if err != nil {
		return "", err
	}
	return res.NoteID, nil
}

func (p *Parser) resolveShortLink(shortURL string) (string, error) {
	tryURLs := []string{shortURL}
	if u, err := url.Parse(shortURL); err == nil {
		if strings.EqualFold(u.Scheme, "http") {
			clone := *u
			clone.Scheme = "https"
			tryURLs = append(tryURLs, clone.String())
		}
	}

	var lastErr error
	for _, u := range tryURLs {
		resolved, err := p.resolveShortLinkOnce(u)
		if err == nil {
			return resolved, nil
		}
		lastErr = err
	}
	if lastErr != nil {
		return "", lastErr
	}
	return "", ErrShortLinkResolution
}

func (p *Parser) resolveShortLinkOnce(shortURL string) (string, error) {
	req, err := http.NewRequest("GET", shortURL, nil)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrShortLinkResolution, err)
	}
	// Some xhslink responses rely on a browser-like UA and may not use 3xx,
	// instead returning an HTML body that contains the real note URL.
	req.Header.Set("User-Agent", defaultUserAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")

	resp, err := p.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrShortLinkResolution, err)
	}
	defer resp.Body.Close()

	// Prefer standard 3xx Location redirect.
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		loc, err := resp.Location()
		if err == nil {
			target := loc
			if !loc.IsAbs() {
				target = req.URL.ResolveReference(loc)
			}
			return target.String(), nil
		}
		// Fall through to body parsing.
	}

	// Fallback: parse HTML body for an embedded xiaohongshu URL.
	// xhslink can return non-3xx (even 404) with a "Found" anchor.
	const maxBody = 256 * 1024
	b, _ := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if len(b) == 0 {
		return "", fmt.Errorf("%w: unexpected status %d", ErrShortLinkResolution, resp.StatusCode)
	}
	body := html.UnescapeString(string(b))
	for _, m := range shareURLPattern.FindAllString(body, -1) {
		parsed, err := url.Parse(trimURL(m))
		if err != nil {
			continue
		}
		if p.isAcceptableHost(strings.ToLower(parsed.Hostname())) {
			return parsed.String(), nil
		}
	}

	return "", fmt.Errorf("%w: unexpected status %d", ErrShortLinkResolution, resp.StatusCode)
}

func (p *Parser) extractShareURL(input string) (string, error) {
	if looksLikeURL(input) {
		u, err := url.Parse(trimURL(input))
		if err == nil && p.isAcceptableHost(strings.ToLower(u.Hostname())) {
			return u.String(), nil
		}
	}
	matches := shareURLPattern.FindAllString(input, -1)
	for _, m := range matches {
		u, err := url.Parse(trimURL(m))
		if err != nil {
			continue
		}
		if p.isAcceptableHost(strings.ToLower(u.Hostname())) {
			return u.String(), nil
		}
	}
	return "", ErrNoXHSURL
}

func (p *Parser) isAcceptableHost(host string) bool {
	if host == "" {
		return false
	}
	// Host must be xiaohongshu.com itself or one of its subdomains.
	// Avoid substring checks like "xiaohongshu.com.evil.com".
	if isDomainOrSubdomain(host, "xiaohongshu.com") {
		return true
	}
	return p.isShortDomain(host)
}

func (p *Parser) isShortDomain(host string) bool {
	_, ok := p.shortDomains[host]
	return ok
}

func looksLikeURL(v string) bool {
	v = strings.TrimSpace(v)
	return strings.HasPrefix(v, "http://") || strings.HasPrefix(v, "https://")
}

func trimURL(v string) string {
	return strings.Trim(v, " \t\r\n\"'<>[]{}()")
}

func isDomainOrSubdomain(host, domain string) bool {
	host = strings.TrimSpace(strings.ToLower(host))
	domain = strings.TrimSpace(strings.ToLower(domain))
	host = strings.TrimSuffix(host, ".")
	domain = strings.TrimSuffix(domain, ".")
	if host == "" || domain == "" {
		return false
	}
	if host == domain {
		return true
	}
	return strings.HasSuffix(host, "."+domain)
}
