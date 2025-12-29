package wechat

import "testing"

func TestIsBadTitle(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"", true},
		{"   ", true},
		{"Video player is loading", true},
		{"自动续播", true},
		{"小窗模式", true},
		{"hello world", false},
	}

	for _, tc := range cases {
		if got := IsBadTitle(tc.in); got != tc.want {
			t.Fatalf("IsBadTitle(%q) = %v, expected %v", tc.in, got, tc.want)
		}
	}
}

func TestIsBadAuthor(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"", true},
		{"朋友", true},
		{"123个朋友", true},
		{"nick", false},
	}

	for _, tc := range cases {
		if got := IsBadAuthor(tc.in); got != tc.want {
			t.Fatalf("IsBadAuthor(%q) = %v, expected %v", tc.in, got, tc.want)
		}
	}
}
