package douyin

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"
)

// TestDouyinRangeSupport tests if Douyin CDN supports HTTP Range requests
// Run with: go test -v -run TestDouyinRangeSupport ./internal/download/douyin/
// This is an integration test that requires network access
func TestDouyinRangeSupport(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	// Get a real Douyin video URL first
	client := NewClient()

	// Test with a known public video ID (you may need to update this)
	// Using a short video for faster testing
	testIDs := []string{
		"7584929704994458907", // from the log file
	}

	for _, awemeID := range testIDs {
		t.Run(awemeID, func(t *testing.T) {
			item, err := client.GetItemInfo(awemeID)
			if err != nil {
				t.Skipf("Failed to get item info (may be rate limited): %v", err)
				return
			}

			if len(item.Streams) == 0 {
				t.Skip("No streams available")
				return
			}

			videoURL := item.Streams[0].URL
			t.Logf("Testing URL: %s", videoURL)

			// Test Range support
			result := checkRangeSupport(videoURL)

			t.Logf("=== Range Support Test Results ===")
			t.Logf("HEAD Status: %d", result.HeadStatus)
			t.Logf("Accept-Ranges header: %q", result.AcceptRanges)
			t.Logf("Content-Length: %d bytes (%.2f MB)", result.ContentLength, float64(result.ContentLength)/1024/1024)
			t.Logf("Range Request Status: %d", result.RangeStatus)
			t.Logf("Supports Range: %v", result.SupportsRange)

			if result.Error != nil {
				t.Logf("Error: %v", result.Error)
			}

			// Summary
			if result.SupportsRange {
				t.Logf("✅ Douyin CDN SUPPORTS Range requests - multipart download is possible!")
			} else {
				t.Logf("❌ Douyin CDN does NOT support Range requests")
			}
		})
	}
}

type rangeTestResult struct {
	HeadStatus    int
	AcceptRanges  string
	ContentLength int64
	RangeStatus   int
	SupportsRange bool
	Error         error
}

func checkRangeSupport(videoURL string) rangeTestResult {
	result := rangeTestResult{}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	httpClient := &http.Client{
		Timeout: 30 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			// Follow redirects but preserve headers
			if len(via) >= 10 {
				return fmt.Errorf("too many redirects")
			}
			return nil
		},
	}

	// Step 1: HEAD request to check Accept-Ranges header
	headReq, err := http.NewRequestWithContext(ctx, "HEAD", videoURL, nil)
	if err != nil {
		result.Error = fmt.Errorf("create HEAD request: %w", err)
		return result
	}
	applyDouyinHeaders(headReq)

	headResp, err := httpClient.Do(headReq)
	if err != nil {
		result.Error = fmt.Errorf("HEAD request: %w", err)
		return result
	}
	defer headResp.Body.Close()

	result.HeadStatus = headResp.StatusCode
	result.AcceptRanges = headResp.Header.Get("Accept-Ranges")
	result.ContentLength = headResp.ContentLength

	// Step 2: Actual Range request to verify
	rangeReq, err := http.NewRequestWithContext(ctx, "GET", videoURL, nil)
	if err != nil {
		result.Error = fmt.Errorf("create Range request: %w", err)
		return result
	}
	applyDouyinHeaders(rangeReq)
	rangeReq.Header.Set("Range", "bytes=0-1023") // Request first 1KB

	rangeResp, err := httpClient.Do(rangeReq)
	if err != nil {
		result.Error = fmt.Errorf("Range request: %w", err)
		return result
	}
	defer rangeResp.Body.Close()

	result.RangeStatus = rangeResp.StatusCode

	// Drain the response body
	_, _ = io.Copy(io.Discard, rangeResp.Body)

	// Check if Range is supported
	// 206 Partial Content = Range supported
	// 200 OK with full content = Range NOT supported (server ignores Range header)
	result.SupportsRange = rangeResp.StatusCode == http.StatusPartialContent

	// Also check Content-Range header for 206 responses
	if result.SupportsRange {
		contentRange := rangeResp.Header.Get("Content-Range")
		if contentRange == "" {
			// Some servers return 206 but without Content-Range, still counts as support
			result.SupportsRange = true
		}
	}

	return result
}

func applyDouyinHeaders(req *http.Request) {
	req.Header.Set("User-Agent", defaultUserAgent)
	req.Header.Set("Referer", defaultReferer)
	req.Header.Set("Origin", "https://www.douyin.com")
	req.Header.Set("Accept", "*/*")
}
