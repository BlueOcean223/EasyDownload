package wechat

import "testing"

func TestHandler_DedupSkipsSameVideo(t *testing.T) {
	h := NewHandler()

	detected := 0
	h.SetVideoCallback(func(VideoInfo) {
		detected++
	})

	payload := []byte(`{
		"id":"vid1",
		"media":[{"url":"https://example.com/video.mp4","urlToken":"","coverUrl":"https://example.com/cover.jpg","fileSize":123,"decodeKey":"","mediaType":4,"spec":[{"fileFormat":"mp4","durationMs":1000,"width":720,"height":1280}]}],
		"description":"hello"
	}`)

	if err := h.HandleRequestWithType(payload, "1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := h.HandleRequestWithType(payload, "1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if detected != 1 {
		t.Fatalf("expected 1 detection callback, got %d", detected)
	}

	cur := h.GetCurrentVideo()
	if cur == nil || cur.ID != "vid1" || !cur.IsCurrentVideo {
		t.Fatalf("expected current video to be set, got %+v", cur)
	}
}

func TestHandler_DownloadFallsBackToCurrentVideoOnParseError(t *testing.T) {
	h := NewHandler()

	h.SetVideoCallback(func(VideoInfo) {})

	gotID := ""
	h.SetDownloadCallback(func(v VideoInfo) {
		gotID = v.ID
	})

	payload := []byte(`{
		"id":"vid1",
		"media":[{"url":"https://example.com/video.mp4","urlToken":"","coverUrl":"https://example.com/cover.jpg","fileSize":123,"decodeKey":"","mediaType":4,"spec":[{"fileFormat":"mp4","durationMs":1000,"width":720,"height":1280}]}],
		"description":"hello"
	}`)
	if err := h.HandleRequestWithType(payload, "1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if err := h.HandleRequestWithType([]byte("not json"), "download"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotID != "vid1" {
		t.Fatalf("expected download callback to receive current video id vid1, got %q", gotID)
	}
}
