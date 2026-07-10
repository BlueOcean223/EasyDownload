package api

import (
	"EasyDownload/internal/detection"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
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
	if api.detectionStore == nil {
		t.Error("detectionStore is nil")
	}
}

// TestGetPort tests port retrieval
func TestGetPort(t *testing.T) {
	api := NewInternalAPI(12345)
	if api.GetPort() != 12345 {
		t.Errorf("GetPort() = %d, want 12345", api.GetPort())
	}
}

func TestInternalAPIAuthMiddleware(t *testing.T) {
	api := NewInternalAPI(18899)

	handler := api.authHandler(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	unauthorizedReq := httptest.NewRequest(http.MethodGet, "/api/videos", nil)
	unauthorizedW := httptest.NewRecorder()
	handler(unauthorizedW, unauthorizedReq)
	if unauthorizedW.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status=%d, want %d", unauthorizedW.Code, http.StatusUnauthorized)
	}

	authorizedReq := httptest.NewRequest(http.MethodGet, "/api/videos", nil)
	authorizedReq.Header.Set("X-EasyDownload-Token", api.GetToken())
	authorizedW := httptest.NewRecorder()
	handler(authorizedW, authorizedReq)
	if authorizedW.Code != http.StatusNoContent {
		t.Fatalf("authorized status=%d, want %d", authorizedW.Code, http.StatusNoContent)
	}
}

func TestInternalAPIAllowedOriginNoWildcard(t *testing.T) {
	api := NewInternalAPI(18899)
	if !api.isAllowedOrigin("http://127.0.0.1:34115") {
		t.Fatal("localhost dev origin should be allowed")
	}
	if api.isAllowedOrigin("https://evil.example") {
		t.Fatal("arbitrary origin should not be allowed")
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

	video := ProxyDetectedVideoRequest{
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
	videos := api.getDetectedVideos()
	if len(videos) != 1 {
		t.Errorf("detected videos count = %d, want 1", len(videos))
	}
	wantID := detection.StableID(detection.Identity{
		Source: detection.SourceWeChatProxy, Platform: "wechat", PlatformContentID: "test-id",
		PrimaryURL: "http://example.com/video.mp4",
	})
	if videos[0].ID != wantID {
		t.Errorf("video ID = %s, want %s", videos[0].ID, wantID)
	}
}

// TestHandleDetectDuplicate tests that duplicate videos are not added
func TestHandleDetectDuplicate(t *testing.T) {
	api := NewInternalAPI(18899)

	video := ProxyDetectedVideoRequest{
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
	videos := api.getDetectedVideos()
	if len(videos) != 1 {
		t.Errorf("detected videos count = %d, want 1 (no duplicates)", len(videos))
	}
}

func TestProxyRequestKeepsExecutionSecretsPrivateAndPublishesCandidates(t *testing.T) {
	api := NewInternalAPI(18899)
	request := ProxyDetectedVideoRequest{
		ID: "private-1", Title: "Private", URL: "https://private.example/video.mp4?token=secret",
		Source: "wechat", DecodeKey: "decode-secret", Width: 1920, Height: 1080,
		FileFormats: []string{"hd", "sd"},
		Specs:       []ProxyVideoSpec{{FileFormat: "hd", Width: 1280, Height: 720, DurationMs: 1000}},
	}
	body, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	api.handleDetect(recorder, httptest.NewRequest(http.MethodPost, "/api/detect", bytes.NewReader(body)))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}

	snapshot := api.GetDetectionSnapshot()
	if len(snapshot.Videos) != 1 || len(snapshot.Videos[0].Candidates) != 3 {
		t.Fatalf("unexpected public snapshot: %#v", snapshot)
	}
	if !snapshot.Videos[0].Candidates[0].Default || !snapshot.Videos[0].Candidates[0].Encrypted {
		t.Fatalf("default/encrypted hint missing: %#v", snapshot.Videos[0].Candidates[0])
	}
	publicJSON, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	serialized := strings.ToLower(string(publicJSON))
	for _, forbidden := range []string{"private.example", "decode-secret", "headers", "decodekey", `"url"`} {
		if strings.Contains(serialized, forbidden) {
			t.Fatalf("public projection leaked %q: %s", forbidden, serialized)
		}
	}

	videoID := snapshot.Videos[0].ID
	formatCandidate := snapshot.Videos[0].Candidates[1]
	_, privateCandidate, err := api.detectionStore.ResolveCandidate(context.Background(), videoID, formatCandidate.ID)
	if err != nil {
		t.Fatal(err)
	}
	if privateCandidate.URL != request.URL || privateCandidate.DecodeKey != request.DecodeKey || privateCandidate.FileFormat != "hd" {
		t.Fatalf("private candidate was not preserved: %#v", privateCandidate)
	}
}

func TestProxyRequestStableIDDoesNotDependOnPresentationFields(t *testing.T) {
	now := time.Date(2026, 7, 10, 8, 0, 0, 0, time.UTC)
	first := (ProxyDetectedVideoRequest{PageURL: "https://example.test/watch/7", URL: "https://cdn.test/a?token=1"}).toDomain(now)
	second := (ProxyDetectedVideoRequest{PageURL: "https://example.test/watch/7", URL: "https://cdn.test/a?token=2", Title: "filled", CoverURL: "https://cover.test/a"}).toDomain(now)
	if first.ID != second.ID {
		t.Fatalf("presentation/signed media changes changed stable page identity: %q != %q", first.ID, second.ID)
	}
}

func TestVideoCallbackCanBeReplacedConcurrentlyWithIngest(t *testing.T) {
	api := NewInternalAPI(0, detection.NewMemoryStore(256))
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			api.SetVideoCallback(func(detection.Change) {})
		}()
		go func(index int) {
			defer wg.Done()
			_, err := api.Ingest(context.Background(), detectedFixture(
				fmt.Sprintf("race-%d", index), "Race", fmt.Sprintf("https://example.test/%d.mp4", index),
			))
			if err != nil {
				t.Errorf("ingest failed: %v", err)
			}
		}(i)
	}
	wg.Wait()
}

func TestWeChatHTTPIngressSharesSignedURLIdentityRule(t *testing.T) {
	now := time.Date(2026, 7, 10, 8, 0, 0, 0, time.UTC)
	first := (ProxyDetectedVideoRequest{
		Source: "wechat",
		URL:    "https://finder.video.qq.com/7/stodownload?encfilekey=private-stable&m=media&token=one",
	}).toDomain(now)
	second := (ProxyDetectedVideoRequest{
		Source: "wechat",
		URL:    "https://finder.video.qq.com/7/stodownload?encfilekey=private-stable&m=media&token=two",
	}).toDomain(now.Add(time.Minute))
	if first.ID != second.ID {
		t.Fatalf("HTTP and proxy adapter identity rule split signed URL rotation: %q != %q", first.ID, second.ID)
	}
	if strings.Contains(first.ID, "private-stable") {
		t.Fatalf("opaque public identity leaked WeChat identity material: %q", first.ID)
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

	_, _ = api.Ingest(context.Background(), detectedFixture("1", "Video 1", "http://example.com/1.mp4"))
	_, _ = api.Ingest(context.Background(), detectedFixture("2", "Video 2", "http://example.com/2.mp4"))

	req := httptest.NewRequest(http.MethodGet, "/api/videos", nil)
	w := httptest.NewRecorder()

	api.handleGetVideos(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status code = %d, want %d", w.Code, http.StatusOK)
	}

	responseBody := w.Body.Bytes()
	serialized := strings.ToLower(string(responseBody))
	for _, forbidden := range []string{"example.com", "decodekey", "headers", `"url"`} {
		if strings.Contains(serialized, forbidden) {
			t.Fatalf("public videos response leaked forbidden value %q: %s", forbidden, serialized)
		}
	}
	var snapshot detection.PublicSnapshot
	if err := json.Unmarshal(responseBody, &snapshot); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	if len(snapshot.Videos) != 2 {
		t.Errorf("videos count = %d, want 2", len(snapshot.Videos))
	}
	if snapshot.Revision != 2 {
		t.Errorf("revision = %d, want 2", snapshot.Revision)
	}
}

// TestHandleClear tests clear endpoint
func TestHandleClear(t *testing.T) {
	api := NewInternalAPI(18899)

	_, _ = api.Ingest(context.Background(), detectedFixture("1", "Video 1", "http://example.com/1.mp4"))

	req := httptest.NewRequest(http.MethodPost, "/api/clear", nil)
	w := httptest.NewRecorder()

	api.handleClear(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status code = %d, want %d", w.Code, http.StatusOK)
	}

	videos := api.getDetectedVideos()
	if len(videos) != 0 {
		t.Errorf("videos count after clear = %d, want 0", len(videos))
	}
}

// TestClearVideos tests ClearVideos method
func TestClearVideos(t *testing.T) {
	api := NewInternalAPI(18899)

	_, _ = api.Ingest(context.Background(), detectedFixture("1", "Video 1", "http://example.com/1.mp4"))
	_, _ = api.Ingest(context.Background(), detectedFixture("2", "Video 2", "http://example.com/2.mp4"))

	api.ClearVideos()

	videos := api.getDetectedVideos()
	if len(videos) != 0 {
		t.Errorf("videos count after ClearVideos = %d, want 0", len(videos))
	}
}

// TestRemoveVideo tests RemoveVideo method
func TestRemoveVideo(t *testing.T) {
	api := NewInternalAPI(18899)

	for _, video := range []detection.Video{
		detectedFixture("1", "Video 1", "http://example.com/1.mp4"),
		detectedFixture("2", "Video 2", "http://example.com/2.mp4"),
		detectedFixture("3", "Video 3", "http://example.com/3.mp4"),
	} {
		_, _ = api.Ingest(context.Background(), video)
	}

	api.RemoveVideo("2")

	videos := api.getDetectedVideos()
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
	api.SetVideoCallback(func(v detection.Change) {
		callbackCalled = true
	})

	video := ProxyDetectedVideoRequest{
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
			for _, url := range urls {
				video := ProxyDetectedVideoRequest{
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
				if strings.TrimSpace(url) != "" {
					uniqueURLs[url] = true
				}
			}

			videos := api.getDetectedVideos()
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
				id := string(rune('a' + i%26))
				_, _ = api.Ingest(context.Background(), detectedFixture(id, "", "http://example.com/"+id+".mp4"))
			}

			api.ClearVideos()

			return len(api.getDetectedVideos()) == 0
		},
		gen.IntRange(0, 100),
	))

	properties.TestingRun(t)
}

func detectedFixture(id, title, rawURL string) detection.Video {
	return detection.Video{
		ID: id, Source: detection.SourceWeChatProxy, Platform: "wechat", Title: title,
		Candidates: []detection.Resource{{ID: "original", URL: rawURL, Default: true}},
	}
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

// Test isAllowedMediaDomain function for SSRF prevention
func TestIsAllowedMediaDomain(t *testing.T) {
	tests := []struct {
		name     string
		url      string
		expected bool
	}{
		{"allowed aweme domain", "https://aweme.snssdk.com/aweme/v1/play/?video_id=123", true},
		{"allowed subdomain", "https://v1-cold.douyinvod.com/video.mp4", true},
		{"allowed bytecdntp", "https://p1.bytecdntp.com/img.jpg", true},
		{"blocked internal IP", "http://127.0.0.1:8080/secret", false},
		{"blocked private IP", "http://192.168.1.1/admin", false},
		{"blocked arbitrary domain", "https://evil.com/video.mp4", false},
		{"blocked localhost", "http://localhost/secret", false},
		{"invalid URL", "not-a-url", false},
		{"empty URL", "", false},
		{"allowed snssdk subdomain", "https://v.snssdk.com/path", true},
		{"allowed douyincdn", "https://cdn.douyincdn.com/video.mp4", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isAllowedMediaDomain(tt.url)
			if result != tt.expected {
				t.Errorf("isAllowedMediaDomain(%q) = %v, want %v", tt.url, result, tt.expected)
			}
		})
	}
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
