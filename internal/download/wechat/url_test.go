package wechat

import "testing"

func TestBuildFullURL(t *testing.T) {
	cases := []struct {
		base  string
		token string
		want  string
	}{
		{"https://example.com/v", "", "https://example.com/v"},
		{"https://example.com/v", "&a=1", "https://example.com/v?a=1"},
		{"https://example.com/v", "?a=1", "https://example.com/v?a=1"},
		{"https://example.com/v?x=1", "&a=1", "https://example.com/v?x=1&a=1"},
		{"https://example.com/v?x=1", "?a=1", "https://example.com/v?x=1&a=1"},
		{"https://example.com/v", "a=1", "https://example.com/v?a=1"},
		{"https://example.com/v?x=1", "a=1", "https://example.com/v?x=1&a=1"},
	}

	for _, tc := range cases {
		got := BuildFullURL(tc.base, tc.token)
		if got != tc.want {
			t.Fatalf("BuildFullURL(%q,%q) = %q, expected %q", tc.base, tc.token, got, tc.want)
		}
	}
}

func TestExtractStableURLParams(t *testing.T) {
	u := "https://finder.video.qq.com/123/stodownload?token=aaa&encfilekey=key123&m=mmm&svrbypass=bbb&svrnonce=ccc"
	got := ExtractStableURLParams(u)
	want := "encfilekey:key123:m:mmm"
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}

	evil := "https://finder.video.qq.com.evil.example/123/stodownload?encfilekey=key123&m=mmm"
	if got := ExtractStableURLParams(evil); got != evil {
		t.Fatalf("lookalike host should be left unchanged, got %q", got)
	}
}

func TestCanonicalKeyForVideo(t *testing.T) {
	if got := CanonicalKeyForVideo(VideoInfo{ID: "id1", URL: "https://example.com/v"}); got != "id1" {
		t.Fatalf("expected id to win, got %q", got)
	}

	u := "https://finder.video.qq.com/123/stodownload?encfilekey=key123&m=mmm"
	if got := CanonicalKeyForVideo(VideoInfo{URL: u}); got != "encfilekey:key123:m:mmm" {
		t.Fatalf("unexpected canonical key: %q", got)
	}
}

func TestIsValidVODURL(t *testing.T) {
	cases := []struct {
		in    string
		valid bool
	}{
		{"", false},
		{"https://example.com/live.m3u8", false},
		{"https://finder.video.qq.com/123/stodownload?encfilekey=key123&m=mmm&startIdx=0", false},
		{"https://finder.video.qq.com/123/other?encfilekey=key123&m=mmm", false},
		{"https://finder.video.qq.com/123/stodownload?m=mmm", false},
		{"https://finder.video.qq.com/123/stodownload?encfilekey=key123&m=mmm", true},
		{"https://finder.video.qq.com.evil.example/123/stodownload?m=mmm", true},
	}

	for _, tc := range cases {
		got, _ := IsValidVODURL(tc.in)
		if got != tc.valid {
			t.Fatalf("IsValidVODURL(%q) = %v, expected %v", tc.in, got, tc.valid)
		}
	}
}
