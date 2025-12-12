package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/leanovate/gopter"
	"github.com/leanovate/gopter/gen"
	"github.com/leanovate/gopter/prop"
)

// TestInternalAPICreation tests InternalAPI creation
func TestInternalAPICreation(t *testing.T) {
	api := NewInternalAPI(18899)
	if api == nil {
		t.Fatal("NewInternalAPI returned nil")
	}
	if api.port != 18899 {
		t.Errorf("port = %d, want 18899", api.port)
	}
	if api.detectedVideos == nil {
		t.Error("detectedVideos is nil")
	}
}

// TestGetPort tests port retrieval
func TestGetPort(t *testing.T) {
	api := NewInternalAPI(12345)
	if api.GetPort() != 12345 {
		t.Errorf("GetPort() = %d, want 12345", api.GetPort())
	}
}

// TestHandleHealth tests health endpoint
func TestHandleHealth(t *testing.T) {
	api := NewInternalAPI(18899)

	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	w := httptest.NewRecorder()

	api.handleHealth(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status code = %d, want %d", w.Code, http.StatusOK)
	}

	var response map[string]string
	if err := json.NewDecoder(w.Body).Decode(&response); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	if response["status"] != "healthy" {
		t.Errorf("status = %s, want healthy", response["status"])
	}
}

// TestHandleDetect tests video detection endpoint
func TestHandleDetect(t *testing.T) {
	api := NewInternalAPI(18899)

	video := DetectedVideo{
		ID:        "test-id",
		Title:     "Test Video",
		URL:       "http://example.com/video.mp4",
		Source:    "wechat",
		Timestamp: time.Now().Unix(),
	}

	body, _ := json.Marshal(video)
	req := httptest.NewRequest(http.MethodPost, "/api/detect", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	api.handleDetect(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status code = %d, want %d", w.Code, http.StatusOK)
	}

	// Verify video was added
	videos := api.GetDetectedVideos()
	if len(videos) != 1 {
		t.Errorf("detected videos count = %d, want 1", len(videos))
	}
	if videos[0].ID != "test-id" {
		t.Errorf("video ID = %s, want test-id", videos[0].ID)
	}
}

// TestHandleDetectDuplicate tests that duplicate videos are not added
func TestHandleDetectDuplicate(t *testing.T) {
	api := NewInternalAPI(18899)

	video := DetectedVideo{
		ID:        "test-id",
		Title:     "Test Video",
		URL:       "http://example.com/video.mp4",
		Source:    "wechat",
		Timestamp: time.Now().Unix(),
	}

	body, _ := json.Marshal(video)

	// Add first time
	req1 := httptest.NewRequest(http.MethodPost, "/api/detect", bytes.NewReader(body))
	req1.Header.Set("Content-Type", "application/json")
	w1 := httptest.NewRecorder()
	api.handleDetect(w1, req1)

	// Add second time with same URL
	req2 := httptest.NewRequest(http.MethodPost, "/api/detect", bytes.NewReader(body))
	req2.Header.Set("Content-Type", "application/json")
	w2 := httptest.NewRecorder()
	api.handleDetect(w2, req2)

	// Should still have only 1 video
	videos := api.GetDetectedVideos()
	if len(videos) != 1 {
		t.Errorf("detected videos count = %d, want 1 (no duplicates)", len(videos))
	}
}

// TestHandleDetectMethodNotAllowed tests method validation
func TestHandleDetectMethodNotAllowed(t *testing.T) {
	api := NewInternalAPI(18899)

	req := httptest.NewRequest(http.MethodGet, "/api/detect", nil)
	w := httptest.NewRecorder()

	api.handleDetect(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status code = %d, want %d", w.Code, http.StatusMethodNotAllowed)
	}
}

// TestHandleGetVideos tests video list endpoint
func TestHandleGetVideos(t *testing.T) {
	api := NewInternalAPI(18899)

	// Add some videos
	api.videosMu.Lock()
	api.detectedVideos = []DetectedVideo{
		{ID: "1", Title: "Video 1", URL: "http://example.com/1.mp4"},
		{ID: "2", Title: "Video 2", URL: "http://example.com/2.mp4"},
	}
	api.videosMu.Unlock()

	req := httptest.NewRequest(http.MethodGet, "/api/videos", nil)
	w := httptest.NewRecorder()

	api.handleGetVideos(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status code = %d, want %d", w.Code, http.StatusOK)
	}

	var videos []DetectedVideo
	if err := json.NewDecoder(w.Body).Decode(&videos); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	if len(videos) != 2 {
		t.Errorf("videos count = %d, want 2", len(videos))
	}
}

// TestHandleClear tests clear endpoint
func TestHandleClear(t *testing.T) {
	api := NewInternalAPI(18899)

	// Add some videos
	api.videosMu.Lock()
	api.detectedVideos = []DetectedVideo{
		{ID: "1", Title: "Video 1", URL: "http://example.com/1.mp4"},
	}
	api.videosMu.Unlock()

	req := httptest.NewRequest(http.MethodPost, "/api/clear", nil)
	w := httptest.NewRecorder()

	api.handleClear(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status code = %d, want %d", w.Code, http.StatusOK)
	}

	videos := api.GetDetectedVideos()
	if len(videos) != 0 {
		t.Errorf("videos count after clear = %d, want 0", len(videos))
	}
}

// TestClearVideos tests ClearVideos method
func TestClearVideos(t *testing.T) {
	api := NewInternalAPI(18899)

	api.videosMu.Lock()
	api.detectedVideos = []DetectedVideo{
		{ID: "1", Title: "Video 1", URL: "http://example.com/1.mp4"},
		{ID: "2", Title: "Video 2", URL: "http://example.com/2.mp4"},
	}
	api.videosMu.Unlock()

	api.ClearVideos()

	videos := api.GetDetectedVideos()
	if len(videos) != 0 {
		t.Errorf("videos count after ClearVideos = %d, want 0", len(videos))
	}
}

// TestRemoveVideo tests RemoveVideo method
func TestRemoveVideo(t *testing.T) {
	api := NewInternalAPI(18899)

	api.videosMu.Lock()
	api.detectedVideos = []DetectedVideo{
		{ID: "1", Title: "Video 1", URL: "http://example.com/1.mp4"},
		{ID: "2", Title: "Video 2", URL: "http://example.com/2.mp4"},
		{ID: "3", Title: "Video 3", URL: "http://example.com/3.mp4"},
	}
	api.videosMu.Unlock()

	api.RemoveVideo("2")

	videos := api.GetDetectedVideos()
	if len(videos) != 2 {
		t.Errorf("videos count after RemoveVideo = %d, want 2", len(videos))
	}

	// Verify video 2 was removed
	for _, v := range videos {
		if v.ID == "2" {
			t.Error("Video 2 should have been removed")
		}
	}
}

// TestSetVideoCallback tests callback setting
func TestSetVideoCallback(t *testing.T) {
	api := NewInternalAPI(18899)

	callbackCalled := false
	api.SetVideoCallback(func(v DetectedVideo) {
		callbackCalled = true
	})

	video := DetectedVideo{
		ID:    "test-id",
		Title: "Test Video",
		URL:   "http://example.com/video.mp4",
	}

	body, _ := json.Marshal(video)
	req := httptest.NewRequest(http.MethodPost, "/api/detect", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	api.handleDetect(w, req)

	if !callbackCalled {
		t.Error("Video callback was not called")
	}
}

// **Feature: easydownload-improvements, Property: 视频去重一致性**
// For any set of videos with the same URL, only one should be stored
func TestVideoDuplicationProperty(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("duplicate URLs are not stored", prop.ForAll(
		func(urls []string) bool {
			api := NewInternalAPI(18899)

			// Add all videos
			for i, url := range urls {
				video := DetectedVideo{
					ID:    string(rune('0' + i%10)),
					Title: "Video",
					URL:   url,
				}
				body, _ := json.Marshal(video)
				req := httptest.NewRequest(http.MethodPost, "/api/detect", bytes.NewReader(body))
				req.Header.Set("Content-Type", "application/json")
				w := httptest.NewRecorder()
				api.handleDetect(w, req)
			}

			// Count unique URLs
			uniqueURLs := make(map[string]bool)
			for _, url := range urls {
				uniqueURLs[url] = true
			}

			videos := api.GetDetectedVideos()
			return len(videos) == len(uniqueURLs)
		},
		gen.SliceOf(gen.AnyString()),
	))

	properties.TestingRun(t)
}

// **Feature: easydownload-improvements, Property: 清除后状态重置**
// After clearing, all videos should be removed
func TestClearResetProperty(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("ClearVideos removes all videos", prop.ForAll(
		func(count int) bool {
			api := NewInternalAPI(18899)

			// Add videos
			for i := 0; i < count; i++ {
				api.videosMu.Lock()
				api.detectedVideos = append(api.detectedVideos, DetectedVideo{
					ID:  string(rune('a' + i%26)),
					URL: "http://example.com/" + string(rune('a'+i%26)) + ".mp4",
				})
				api.videosMu.Unlock()
			}

			api.ClearVideos()

			return len(api.GetDetectedVideos()) == 0
		},
		gen.IntRange(0, 100),
	))

	properties.TestingRun(t)
}

// **Feature: video-capture-fix, Property 8: 图片代理请求头**
// For any Bilibili image proxy request, the HTTP request should contain correct User-Agent and Referer headers
// **Validates: Requirements 4.3**
func TestImageProxyBilibiliHeadersProperty(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	// Generate Bilibili-like URLs
	bilibiliDomains := []string{
		"hdslb.com",
		"bilibili.com",
		"bilivideo.com",
		"biliimg.com",
	}

	properties.Property("Bilibili URLs get correct headers", prop.ForAll(
		func(domainIdx int, path string) bool {
			domain := bilibiliDomains[domainIdx%len(bilibiliDomains)]
			imageURL := "https://i0." + domain + "/" + path + ".jpg"

			handler := NewImageProxyHandler()
			req, err := http.NewRequest(http.MethodGet, imageURL, nil)
			if err != nil {
				return true // Skip invalid URLs
			}

			handler.SetRequestHeaders(req, imageURL)

			// Check User-Agent is set
			userAgent := req.Header.Get("User-Agent")
			if userAgent == "" {
				t.Logf("User-Agent is empty for URL: %s", imageURL)
				return false
			}

			// Check Referer is set for Bilibili URLs
			referer := req.Header.Get("Referer")
			if referer != "https://www.bilibili.com/" {
				t.Logf("Referer is not correct for Bilibili URL: %s, got: %s", imageURL, referer)
				return false
			}

			// Check Origin is set for Bilibili URLs
			origin := req.Header.Get("Origin")
			if origin != "https://www.bilibili.com" {
				t.Logf("Origin is not correct for Bilibili URL: %s, got: %s", imageURL, origin)
				return false
			}

			return true
		},
		gen.IntRange(0, 3),
		gen.AlphaString(),
	))

	properties.TestingRun(t)
}

// Test that non-Bilibili URLs don't get Bilibili-specific headers
func TestImageProxyNonBilibiliHeadersProperty(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("Non-Bilibili URLs don't get Bilibili headers", prop.ForAll(
		func(path string) bool {
			// Use a non-Bilibili domain
			imageURL := "https://example.com/" + path + ".jpg"

			handler := NewImageProxyHandler()
			req, err := http.NewRequest(http.MethodGet, imageURL, nil)
			if err != nil {
				return true // Skip invalid URLs
			}

			handler.SetRequestHeaders(req, imageURL)

			// Check User-Agent is still set (common header)
			userAgent := req.Header.Get("User-Agent")
			if userAgent == "" {
				t.Logf("User-Agent should be set for all URLs")
				return false
			}

			// Check Referer is NOT set to Bilibili for non-Bilibili URLs
			referer := req.Header.Get("Referer")
			if referer == "https://www.bilibili.com/" {
				t.Logf("Referer should not be Bilibili for non-Bilibili URL: %s", imageURL)
				return false
			}

			return true
		},
		gen.AlphaString(),
	))

	properties.TestingRun(t)
}

// Test IsBilibiliURL function
func TestIsBilibiliURLProperty(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	bilibiliDomains := []string{
		"hdslb.com",
		"bilibili.com",
		"bilivideo.com",
		"biliimg.com",
	}

	properties.Property("URLs containing Bilibili domains are detected", prop.ForAll(
		func(domainIdx int, prefix, suffix string) bool {
			domain := bilibiliDomains[domainIdx%len(bilibiliDomains)]
			url := "https://" + prefix + "." + domain + "/" + suffix

			return IsBilibiliURL(url)
		},
		gen.IntRange(0, 3),
		gen.AlphaString(),
		gen.AlphaString(),
	))

	properties.Property("URLs not containing Bilibili domains are not detected", prop.ForAll(
		func(url string) bool {
			// Skip if the random string happens to contain a Bilibili domain
			for _, domain := range bilibiliDomains {
				if strings.Contains(url, domain) {
					return true // Skip this case
				}
			}

			return !IsBilibiliURL(url)
		},
		gen.AnyString(),
	))

	properties.TestingRun(t)
}
