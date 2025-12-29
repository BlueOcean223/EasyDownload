package downloader

import (
	"strconv"
	"strings"
)

// totalSizeFromContentRange parses "bytes start-end/total" and returns total.
// Returns 0 if parsing fails or total is unknown ("*").
func totalSizeFromContentRange(contentRange string) int64 {
	cr := strings.TrimSpace(contentRange)
	if cr == "" {
		return 0
	}
	slash := strings.LastIndex(cr, "/")
	if slash == -1 || slash+1 >= len(cr) {
		return 0
	}
	totalStr := strings.TrimSpace(cr[slash+1:])
	if totalStr == "" || totalStr == "*" {
		return 0
	}
	n, err := strconv.ParseInt(totalStr, 10, 64)
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

// TotalSizeFromContentRange parses "bytes start-end/total" and returns total.
// Returns 0 if parsing fails or total is unknown ("*").
func TotalSizeFromContentRange(contentRange string) int64 {
	return totalSizeFromContentRange(contentRange)
}

func shortenForLog(s string, max int) string {
	if max <= 0 {
		return ""
	}
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	if max <= 3 {
		return s[:max]
	}
	return s[:max-3] + "..."
}
