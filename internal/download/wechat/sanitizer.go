package wechat

import (
	"regexp"
	"strings"
)

var (
	// badDurationPattern matches video player duration display like "1:23 / 4:56"
	badDurationPattern = regexp.MustCompile(`\d{1,2}:\d{2}\s*/\s*\d{1,2}:\d{2}`)
	// badFriendsPattern matches WeChat social indicator like "5个朋友" (5 friends)
	badFriendsPattern = regexp.MustCompile(`^\d+个朋友$`)
)

// SanitizeVideo sanitizes a video info instance by clearing obviously-bad title/author values.
func SanitizeVideo(v *VideoInfo) {
	if v == nil {
		return
	}
	if IsBadTitle(v.Title) {
		v.Title = ""
	}
	if IsBadAuthor(v.Author) {
		v.Author = ""
	}
}

// IsBadTitle returns true if the title is empty or looks like WeChat player UI text noise.
func IsBadTitle(t string) bool {
	s := strings.TrimSpace(t)
	if s == "" {
		return true
	}
	low := strings.ToLower(s)
	// Video.js modal dialog accessibility text
	if strings.Contains(low, "this is a modal window") {
		return true
	}
	if strings.Contains(low, "modal window") && strings.Contains(low, "this is") {
		return true
	}
	// Video.js settings panel text
	if strings.Contains(low, "restore all settings to the default values") {
		return true
	}
	// Video player loading state text
	if strings.Contains(low, "video player is loading") {
		return true
	}
	// Video.js transparency/opacity settings
	if strings.Contains(low, "transparency") && strings.Contains(low, "opaque") {
		return true
	}
	if low == "transparency" || low == "opaque" || strings.Contains(low, "semi-transparent") || strings.Contains(low, "semi transparent") {
		return true
	}
	// Chinese WeChat player UI elements
	if strings.Contains(low, "自动续播") || strings.Contains(low, "小窗模式") || strings.Contains(low, "倍速") {
		return true
	}
	// Video duration display (e.g., "1:23 / 4:56")
	if badDurationPattern.MatchString(low) {
		return true
	}
	return false
}

// IsBadAuthor returns true if the author is empty or looks like WeChat player UI text noise.
func IsBadAuthor(a string) bool {
	s := strings.TrimSpace(a)
	if s == "" {
		return true
	}
	low := strings.ToLower(s)
	// Video player loading state text
	if strings.Contains(low, "video player is loading") {
		return true
	}
	// Video.js settings panel text
	if strings.Contains(low, "restore all settings to the default values") {
		return true
	}
	// WeChat social indicators (generic "朋友" or "N个朋友")
	if low == "朋友" || badFriendsPattern.MatchString(s) {
		return true
	}
	return false
}
