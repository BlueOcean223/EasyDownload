package wechat

import "testing"

func TestParseVideoPayload_NewFormat(t *testing.T) {
	data := []byte(`{
		"id":"vid1",
		"media":[{"url":"https://finder.video.qq.com/123/stodownload","urlToken":"?encfilekey=key123&m=mmm","coverUrl":"https://example.com/cover.jpg","fileSize":"456","decodeKey":"0x10","mediaType":4,"spec":[{"fileFormat":"mp4","durationMs":1000,"width":720,"height":1280}]}],
		"description":"  title from desc  ",
		"contact":{"nickname":"nick","head_url":"https://example.com/avatar.jpg"},
		"pageKey":"pk",
		"href":"https://channels.weixin.qq.com/abc",
		"ts":123,
		"source":"injector"
	}`)

	info, err := ParseVideoPayload(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info.ID != "vid1" {
		t.Fatalf("expected id=vid1, got %q", info.ID)
	}
	if info.URL != "https://finder.video.qq.com/123/stodownload?encfilekey=key123&m=mmm" {
		t.Fatalf("unexpected url: %q", info.URL)
	}
	if info.Title != "title from desc" {
		t.Fatalf("unexpected title: %q", info.Title)
	}
	if info.Author != "nick" {
		t.Fatalf("unexpected author: %q", info.Author)
	}
	if info.FileSize != 456 {
		t.Fatalf("unexpected fileSize: %v", info.FileSize)
	}
	if len(info.FileFormats) != 1 || info.FileFormats[0] != "mp4" {
		t.Fatalf("unexpected formats: %+v", info.FileFormats)
	}
	if info.Duration != 1000 || info.Width != 720 || info.Height != 1280 {
		t.Fatalf("unexpected spec summary: duration=%d width=%d height=%d", info.Duration, info.Width, info.Height)
	}
}

func TestParseVideoPayload_FallsBackToWxProfile(t *testing.T) {
	data := []byte(`{
		"type":"media",
		"title":"t",
		"coverUrl":"https://example.com/cover.jpg",
		"url":"https://example.com/video.mp4",
		"size":123,
		"key":"123",
		"id":"profile1",
		"fileFormat":["mp4"]
	}`)

	info, err := ParseVideoPayload(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info.ID != "profile1" {
		t.Fatalf("expected id=profile1, got %q", info.ID)
	}
	if info.URL != "https://example.com/video.mp4" {
		t.Fatalf("unexpected url: %q", info.URL)
	}
	if info.Title != "t" {
		t.Fatalf("unexpected title: %q", info.Title)
	}
	if len(info.FileFormats) != 1 || info.FileFormats[0] != "mp4" {
		t.Fatalf("unexpected formats: %+v", info.FileFormats)
	}
}

func TestParseVideoPayload_EmptyMediaErrors(t *testing.T) {
	_, err := ParseVideoPayload([]byte(`{"media":[]}`))
	if err == nil {
		t.Fatal("expected error for empty media")
	}
}
