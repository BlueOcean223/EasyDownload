package wechat

import (
	"net/url"
	"strings"
)

// BuildFullURL concatenates url and urlToken to create the full video URL.
func BuildFullURL(baseURL, urlToken string) string {
	if baseURL == "" {
		return ""
	}
	if urlToken == "" {
		return baseURL
	}
	// urlToken typically starts with & or ?, handle both cases
	if strings.HasPrefix(urlToken, "&") || strings.HasPrefix(urlToken, "?") {
		// Check if baseURL already has query params
		if strings.Contains(baseURL, "?") {
			if strings.HasPrefix(urlToken, "?") {
				return baseURL + "&" + urlToken[1:]
			}
			return baseURL + urlToken
		}
		// No query params in baseURL
		if strings.HasPrefix(urlToken, "&") {
			return baseURL + "?" + urlToken[1:]
		}
		return baseURL + urlToken
	}
	// urlToken doesn't start with & or ?, add separator
	if strings.Contains(baseURL, "?") {
		return baseURL + "&" + urlToken
	}
	return baseURL + "?" + urlToken
}

// IsValidVideoURL validates if the given URL is a valid video URL.
func IsValidVideoURL(videoURL string) bool {
	if videoURL == "" {
		return false
	}

	// Parse URL to validate format
	parsed, err := url.Parse(videoURL)
	if err != nil {
		return false
	}

	// Must have scheme and host
	if parsed.Scheme == "" || parsed.Host == "" {
		return false
	}

	// Must be http or https
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return false
	}

	return true
}

// IsLiveStreamURL returns true for obvious live/stream playlist URLs that we should skip.
// WeChat Channels live pages may expose m3u8/flv/mpd streams which are not supported by this downloader.
func IsLiveStreamURL(videoURL string) bool {
	u := strings.ToLower(strings.TrimSpace(videoURL))
	if u == "" {
		return false
	}
	// strip hash
	if i := strings.Index(u, "#"); i != -1 {
		u = u[:i]
	}
	// m3u8/flv/dash manifests
	if strings.Contains(u, ".m3u8") || strings.Contains(u, ".flv") || strings.Contains(u, ".mpd") {
		return true
	}
	return false
}

// ExtractStableURLParams extracts stable parameters from WeChat video URL for deduplication.
// Based on url.md analysis: encfilekey and m are stable across sessions.
// Volatile params (token, sign, svrbypass, svrnonce) change on each access.
func ExtractStableURLParams(videoURL string) string {
	videoURL = strings.TrimSpace(videoURL)
	if videoURL == "" {
		return ""
	}

	parsed, err := url.Parse(videoURL)
	if err != nil {
		return videoURL
	}

	// Only process WeChat finder download hosts.
	if !isFinderVideoHost(parsed.Hostname()) {
		return videoURL
	}

	// Extract only the most stable params: encfilekey and m
	encfilekey := parsed.Query().Get("encfilekey")
	m := parsed.Query().Get("m")

	// If we have encfilekey, use it as primary identifier (most stable)
	if encfilekey != "" {
		result := "encfilekey:" + encfilekey
		if m != "" {
			result += ":m:" + m
		}
		return result
	}

	// Fallback: use pathname + m if available
	if m != "" {
		return parsed.Path + ":m:" + m
	}

	return parsed.Scheme + "://" + parsed.Host + parsed.Path
}

// CanonicalKeyForVideo returns a stable, dedup-friendly identifier for a WeChat video.
func CanonicalKeyForVideo(v VideoInfo) string {
	// Priority 1: Use video ID (most stable, from WeChat API)
	if strings.TrimSpace(v.ID) != "" {
		return strings.TrimSpace(v.ID)
	}

	// Priority 2: Extract stable URL params
	// NOTE: Do NOT include DecodeKey - it changes across sessions!
	// Based on url.md analysis: decodeKey values change for the SAME video
	if strings.TrimSpace(v.URL) != "" {
		return ExtractStableURLParams(v.URL)
	}

	return ""
}

// IsValidVODURL validates if a URL appears to be a valid WeChat Video-on-Demand URL.
// It performs several checks to reject invalid URLs:
//   - Empty URLs
//   - Live/streaming URLs (HLS, FLV, DASH)
//   - Chunked URLs with startIdx/size parameters (partial content)
//   - WeChat finder URLs missing required "stodownload" path or "encfilekey" parameter
//
// Returns (true, "") if the URL appears valid, or (false, reason) if invalid.
func IsValidVODURL(raw string) (bool, string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return false, "empty url"
	}
	// Reject live/streaming format URLs (HLS, FLV, DASH manifests)
	if IsLiveStreamURL(raw) {
		return false, "live/stream url"
	}
	lu := strings.ToLower(raw)
	// Reject chunked/partial download URLs (used for progressive streaming)
	if strings.Contains(lu, "startidx=") || strings.Contains(lu, "size=") {
		return false, "chunked url (startIdx/size)"
	}
	// Validate WeChat-specific URL requirements
	pu, err := url.Parse(raw)
	if err == nil {
		// WeChat video finder URLs must have specific path and parameters
		if isFinderVideoHost(pu.Hostname()) {
			// Must be a storage download URL, not a streaming endpoint
			if !strings.Contains(lu, "stodownload") {
				return false, "not stodownload"
			}
			// Must have encfilekey parameter for authentication
			if !strings.Contains(lu, "encfilekey=") {
				return false, "missing encfilekey"
			}
		}
	}
	return true, ""
}

func isFinderVideoHost(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	return host == "finder.video.qq.com" || strings.HasSuffix(host, ".finder.video.qq.com") ||
		host == "findermp.video.qq.com" || strings.HasSuffix(host, ".findermp.video.qq.com")
}
