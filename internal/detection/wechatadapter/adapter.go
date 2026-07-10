// Package wechatadapter owns the single WeChat callback-to-detection mapping.
// Both the in-process proxy callback and the authenticated HTTP callback use it
// so identity and candidate rules cannot drift between ingress paths.
package wechatadapter

import (
	"strings"
	"time"

	"EasyDownload/internal/detection"
	"EasyDownload/internal/download/wechat"
)

// FromVideoInfo converts a WeChat boundary DTO into the private detection
// domain model. Media URLs, request headers and decode keys remain private.
func FromVideoInfo(video wechat.VideoInfo, observedAt time.Time) detection.Video {
	seenAt := observedAt.UTC()
	if seenAt.IsZero() {
		seenAt = time.Now().UTC()
	}
	if video.TS > 0 {
		if video.TS > 1_000_000_000_000 {
			seenAt = time.UnixMilli(video.TS).UTC()
		} else {
			seenAt = time.Unix(video.TS, 0).UTC()
		}
	}
	contentID := strings.TrimSpace(video.ID)
	if contentID == "" {
		contentID = strings.TrimSpace(video.PageKey)
	}
	if contentID == "" {
		// A page URL outranks media identity in the domain policy. Only fall back
		// to WeChat's reviewed volatile-parameter rule when no page identity is
		// available.
		if strings.TrimSpace(video.Href) == "" {
			contentID = wechat.CanonicalKeyForVideo(video)
		}
	}
	detected := detection.Video{
		Source:       detection.SourceWeChatProxy,
		Platform:     "wechat",
		Title:        video.Title,
		Author:       video.Author,
		PageURL:      video.Href,
		CoverURL:     video.CoverURL,
		DetectedAt:   seenAt,
		LastSeenAt:   seenAt,
		AuthorAvatar: video.AuthorAvatar,
		DurationMS:   video.Duration,
		Width:        video.Width,
		Height:       video.Height,
		IsCurrent:    video.IsCurrentVideo,
	}
	detected.ID = detection.StableID(detection.Identity{
		Source: detection.SourceWeChatProxy, Platform: "wechat",
		PlatformContentID: contentID, PageURL: video.Href, PrimaryURL: video.URL,
	})
	if strings.TrimSpace(video.URL) == "" {
		return detected
	}

	headers := map[string]string{"Referer": "https://channels.weixin.qq.com/"}
	detected.Candidates = append(detected.Candidates, detection.Resource{
		ID: "original", URL: video.URL, Headers: headers, DecodeKey: video.DecodeKey,
		Width: video.Width, Height: video.Height, DurationMS: video.Duration,
		SizeBytes: int64(video.FileSize), Default: true,
	})
	seenFormats := make(map[string]struct{}, len(video.Specs)+len(video.FileFormats))
	for _, spec := range video.Specs {
		format := strings.TrimSpace(spec.FileFormat)
		if format == "" {
			continue
		}
		seenFormats[format] = struct{}{}
		detected.Candidates = append(detected.Candidates, detection.Resource{
			ID: "format:" + format, URL: video.URL, Headers: headers, DecodeKey: video.DecodeKey,
			FileFormat: format, Width: spec.Width, Height: spec.Height,
			DurationMS: spec.DurationMs,
		})
	}
	for _, rawFormat := range video.FileFormats {
		format := strings.TrimSpace(rawFormat)
		if format == "" {
			continue
		}
		if _, exists := seenFormats[format]; exists {
			continue
		}
		seenFormats[format] = struct{}{}
		detected.Candidates = append(detected.Candidates, detection.Resource{
			ID: "format:" + format, URL: video.URL, Headers: headers,
			DecodeKey: video.DecodeKey, FileFormat: format,
		})
	}
	return detected
}
