package bilibili

import (
	"fmt"
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

// IsBilibiliURL checks if a URL is a Bilibili video URL.
// Returns true for URLs containing "bilibili.com" or "b23.tv" (short link domain).
func IsBilibiliURL(url string) bool {
	return strings.Contains(url, "bilibili.com") || strings.Contains(url, "b23.tv")
}
