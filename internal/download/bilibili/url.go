package bilibili

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

var (
	bvIDRegex = regexp.MustCompile(`BV[a-zA-Z0-9]+`)
	avIDRegex = regexp.MustCompile(`av(\d+)`)
)

// ParseURL extracts the video ID from a Bilibili URL.
// Supports both BV format (e.g., "https://www.bilibili.com/video/BV1xx411c7mD")
// and legacy AV format (e.g., "https://www.bilibili.com/video/av170001").
// Returns the video ID (e.g., "BV1xx411c7mD" or "av170001") or an error if invalid.
func (bd *BilibiliDownloader) ParseURL(url string) (string, error) {
	// BV format
	if matches := bvIDRegex.FindString(url); matches != "" {
		return matches, nil
	}

	// AV format
	if matches := avIDRegex.FindStringSubmatch(url); len(matches) > 1 {
		return "av" + matches[1], nil
	}

	return "", fmt.Errorf("invalid Bilibili URL")
}

// ParseURLMust extracts the video ID from a Bilibili URL without returning an error.
// Returns an empty string if the URL is invalid instead of panicking.
// For production code, prefer ParseURL to handle errors explicitly.
func (bd *BilibiliDownloader) ParseURLMust(url string) string {
	bvid, err := bd.ParseURL(url)
	if err != nil {
		return ""
	}
	return bvid
}

// IsBilibiliURL checks if a URL belongs to a Bilibili or b23 short-link domain.
func IsBilibiliURL(raw string) bool {
	host := parsedHostname(raw)
	return isDomainOrSubdomain(host, "bilibili.com") || isDomainOrSubdomain(host, "b23.tv")
}

func parsedHostname(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	if u.Hostname() == "" && !strings.Contains(raw, "://") {
		u, err = url.Parse("//" + raw)
		if err != nil {
			return ""
		}
	}
	return strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
}

func isDomainOrSubdomain(host, domain string) bool {
	host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	domain = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(domain), "."))
	return host == domain || strings.HasSuffix(host, "."+domain)
}
