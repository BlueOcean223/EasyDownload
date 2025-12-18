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

var (
	ErrNoDouyinURL         = errors.New("no douyin url found in input")
	ErrAwemeIDNotFound     = errors.New("aweme_id not found")
	ErrShortLinkResolution = errors.New("douyin short link resolution failed")
	ErrInvalidDouyinURL    = errors.New("invalid douyin url")
)

var shareURLPattern = regexp.MustCompile(`https?://[^\s]+`)

// Parser resolves Douyin share links and extracts aweme_id.
type Parser struct {
	client       *http.Client
	shortDomains map[string]struct{}
}

// NewParser creates a parser with a redirect-disabled HTTP client.
func NewParser() *Parser {
	client := &http.Client{
		Timeout: 10 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	return &Parser{
		client:       client,
		shortDomains: map[string]struct{}{"v.douyin.com": {}},
	}
}

// NewParserWithClient allows providing a custom HTTP client.
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

func cloneClient(c *http.Client) *http.Client {
	copy := *c
	return &copy
}

// SetShortDomains overrides known short-link domains (used for testing).
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

// Parse extracts aweme_id from Douyin share text or URLs.
func (p *Parser) Parse(input string) (string, error) {
	link, err := p.extractShareURL(input)
	if err != nil {
		return "", err
	}
	return p.parseURL(link, 0)
}

const maxRedirectDepth = 5

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
	if p.isShortDomain(hostname) {
		next, err := p.resolveShortLink(u.String())
		if err != nil {
			return "", err
		}
		return p.parseURL(next, depth+1)
	}

	if !strings.Contains(hostname, "douyin.com") {
		return "", ErrInvalidDouyinURL
	}

	if id := extractAwemeID(u); id != "" {
		return id, nil
	}

	return "", ErrAwemeIDNotFound
}

func (p *Parser) isShortDomain(host string) bool {
	if host == "" {
		return false
	}
	_, ok := p.shortDomains[strings.ToLower(host)]
	return ok
}

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

	if resp.StatusCode < 300 || resp.StatusCode >= 400 {
		return "", fmt.Errorf("%w: unexpected status %d", ErrShortLinkResolution, resp.StatusCode)
	}

	loc, err := resp.Location()
	if err != nil {
		return "", fmt.Errorf("%w: missing location header", ErrShortLinkResolution)
	}

	target := loc
	if !loc.IsAbs() {
		target = req.URL.ResolveReference(loc)
	}

	return target.String(), nil
}

func (p *Parser) extractShareURL(input string) (string, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return "", ErrNoDouyinURL
	}

	if looksLikeURL(input) {
		u, err := url.Parse(trimURL(input))
		if err == nil && p.isAcceptableHost(u.Hostname()) {
			return u.String(), nil
		}
	}

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

func looksLikeURL(v string) bool {
	v = strings.TrimSpace(v)
	return strings.HasPrefix(v, "http://") || strings.HasPrefix(v, "https://")
}

func trimURL(v string) string {
	return strings.Trim(v, " \t\r\n\"'<>[]{}（）()，,。!?")
}

func extractAwemeID(u *url.URL) string {
	path := strings.TrimSuffix(u.Path, "/")
	segments := strings.Split(path, "/")
	for i := 0; i < len(segments)-1; i++ {
		if segments[i] == "video" || segments[i] == "note" {
			if id := keepDigits(segments[i+1]); id != "" {
				return id
			}
		}
	}

	q := u.Query()
	for _, key := range []string{"modal_id", "aweme_id"} {
		if id := keepDigits(q.Get(key)); id != "" {
			return id
		}
	}

	return ""
}

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
