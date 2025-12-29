package wechat

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"
)

// ParseVideoPayload parses the new payload format sent by injected JS and returns a VideoInfo.
// It falls back to ParseWxProfile and then ParseObjectDesc for older/alternative payload shapes.
func ParseVideoPayload(data []byte) (*VideoInfo, error) {
	var payload videoPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		if info, pErr := ParseWxProfile(data); pErr == nil {
			return info, nil
		}
		return ParseObjectDesc(data)
	}

	if len(payload.Media) == 0 {
		if info, pErr := ParseWxProfile(data); pErr == nil {
			return info, nil
		}
		return nil, fmt.Errorf("media array is empty")
	}

	// Use first media item (WeChat typically sends single video per payload)
	media := payload.Media[0]

	// Combine base URL with token parameters to form complete download URL
	fullURL := BuildFullURL(media.URL, media.URLToken)
	if !IsValidVideoURL(fullURL) {
		return nil, fmt.Errorf("invalid video URL: %s", fullURL)
	}
	if IsLiveStreamURL(fullURL) {
		return nil, fmt.Errorf("live/stream url ignored: %s", fullURL)
	}

	// FileSize can be either number or string in JSON, handle both
	var fileSize float64
	switch v := media.FileSize.(type) {
	case float64:
		fileSize = v
	case string:
		fmt.Sscanf(v, "%f", &fileSize)
	}

	// Extract video specs (quality variants) and use first available values for dimensions/duration
	var formats []string
	var specs []VideoSpec
	var duration, width, height int
	for _, spec := range media.Spec {
		if spec.FileFormat != "" {
			formats = append(formats, spec.FileFormat)
			specs = append(specs, VideoSpec{
				FileFormat: spec.FileFormat,
				Width:      spec.Width,
				Height:     spec.Height,
				DurationMs: spec.DurationMs,
			})
		}
		// Use first non-zero values for video metadata
		if duration == 0 && spec.DurationMs > 0 {
			duration = spec.DurationMs
		}
		if width == 0 && spec.Width > 0 {
			width = spec.Width
		}
		if height == 0 && spec.Height > 0 {
			height = spec.Height
		}
	}

	// Title priority: description > title > domTitle (DOM document title)
	title := strings.TrimSpace(payload.Description)
	if title == "" {
		title = strings.TrimSpace(payload.Title)
	}
	if title == "" {
		title = strings.TrimSpace(payload.DomTitle)
	}

	// Extract author info from contact object
	author := ""
	authorAvatar := ""
	if payload.Contact != nil {
		if payload.Contact.Nickname != "" {
			author = payload.Contact.Nickname
		}
		authorAvatar = payload.Contact.HeadURL
	}

	// ID priority: explicit ID > pageKey > generated timestamp
	id := strings.TrimSpace(payload.ID)
	if id == "" {
		id = strings.TrimSpace(payload.PageKey)
	}
	if id == "" {
		id = fmt.Sprintf("%d", time.Now().UnixNano())
	}

	return &VideoInfo{
		ID:           id,
		URL:          fullURL,
		CoverURL:     media.CoverURL,
		Title:        title,
		FileSize:     fileSize,
		DecodeKey:    media.DecodeKey,
		MediaType:    media.MediaType,
		FileFormats:  formats,
		Specs:        specs,
		Author:       author,
		AuthorAvatar: authorAvatar,
		Duration:     duration,
		Width:        width,
		Height:       height,
		PageKey:      strings.TrimSpace(payload.PageKey),
		Href:         strings.TrimSpace(payload.Href),
		TS:           payload.TS,
		Source:       strings.TrimSpace(payload.Source),
	}, nil
}

// ParseWxProfile parses the wx_channels_download-like profile payload and returns a VideoInfo.
func ParseWxProfile(data []byte) (*VideoInfo, error) {
	var p wxProfilePayload
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("failed to parse profile JSON: %w", err)
	}
	if strings.TrimSpace(p.URL) == "" {
		return nil, fmt.Errorf("profile url is empty")
	}
	if !IsValidVideoURL(p.URL) {
		return nil, fmt.Errorf("invalid profile url: %s", p.URL)
	}
	if IsLiveStreamURL(p.URL) {
		return nil, fmt.Errorf("live/stream url ignored: %s", p.URL)
	}

	title := strings.TrimSpace(p.Title)

	formats := make([]string, 0)
	specs := make([]VideoSpec, 0)
	if len(p.FileFormat) > 0 {
		formats = append(formats, p.FileFormat...)
	}
	if len(p.Spec) > 0 {
		for _, sp := range p.Spec {
			if sp.FileFormat != "" {
				if len(formats) == 0 || !contains(formats, sp.FileFormat) {
					formats = append(formats, sp.FileFormat)
				}
				specs = append(specs, VideoSpec{
					FileFormat: sp.FileFormat,
					Width:      sp.Width,
					Height:     sp.Height,
					DurationMs: sp.DurationMs,
				})
			}
		}
	}

	duration := p.Duration
	if duration == 0 {
		duration = p.DurationMs
	}
	width := p.Width
	height := p.Height
	if len(p.Spec) > 0 {
		if duration == 0 && p.Spec[0].DurationMs > 0 {
			duration = p.Spec[0].DurationMs
		}
		if width == 0 && p.Spec[0].Width > 0 {
			width = p.Spec[0].Width
		}
		if height == 0 && p.Spec[0].Height > 0 {
			height = p.Spec[0].Height
		}
	}

	author := ""
	authorAvatar := ""
	if p.Contact != nil {
		if strings.TrimSpace(p.Contact.Nickname) != "" {
			author = p.Contact.Nickname
		}
		authorAvatar = p.Contact.HeadURL
	} else if strings.TrimSpace(p.Nickname) != "" {
		author = p.Nickname
	}

	id := strings.TrimSpace(p.ID)
	if id == "" {
		id = strings.TrimSpace(p.NonceID)
	}
	if id == "" {
		id = fmt.Sprintf("%d", time.Now().UnixNano())
	}

	mediaType := 4
	if strings.TrimSpace(p.Type) == "picture" {
		mediaType = 9
	}

	return &VideoInfo{
		ID:           id,
		URL:          p.URL,
		CoverURL:     p.CoverURL,
		Title:        title,
		FileSize:     p.Size,
		DecodeKey:    strings.TrimSpace(p.Key),
		MediaType:    mediaType,
		FileFormats:  formats,
		Specs:        specs,
		Author:       author,
		AuthorAvatar: authorAvatar,
		Duration:     duration,
		Width:        width,
		Height:       height,
	}, nil
}

// ParseObjectDesc parses objectDesc JSON data and extracts a VideoInfo (legacy payload format).
func ParseObjectDesc(data []byte) (*VideoInfo, error) {
	var desc objectDesc
	if err := json.Unmarshal(data, &desc); err != nil {
		return nil, fmt.Errorf("failed to parse objectDesc JSON: %w", err)
	}

	if len(desc.Media) == 0 {
		return nil, fmt.Errorf("media array is empty")
	}

	media := desc.Media[0]

	fullURL := BuildFullURL(media.URL, media.URLToken)
	if !IsValidVideoURL(fullURL) {
		return nil, fmt.Errorf("invalid video URL: %s", fullURL)
	}
	if IsLiveStreamURL(fullURL) {
		return nil, fmt.Errorf("live/stream url ignored: %s", fullURL)
	}

	var fileSize float64
	switch v := media.FileSize.(type) {
	case float64:
		fileSize = v
	case string:
		fmt.Sscanf(v, "%f", &fileSize)
	}

	var formats []string
	var specs []VideoSpec
	var duration, width, height int
	for _, spec := range media.Spec {
		if spec.FileFormat != "" {
			formats = append(formats, spec.FileFormat)
			specs = append(specs, VideoSpec{
				FileFormat: spec.FileFormat,
				Width:      spec.Width,
				Height:     spec.Height,
				DurationMs: spec.DurationMs,
			})
		}
		if duration == 0 && spec.DurationMs > 0 {
			duration = spec.DurationMs
		}
		if width == 0 && spec.Width > 0 {
			width = spec.Width
		}
		if height == 0 && spec.Height > 0 {
			height = spec.Height
		}
	}

	title := desc.Description

	author := ""
	authorAvatar := ""
	if desc.Contact != nil {
		if desc.Contact.Nickname != "" {
			author = desc.Contact.Nickname
		}
		authorAvatar = desc.Contact.HeadURL
	}

	return &VideoInfo{
		ID:           fmt.Sprintf("%d", time.Now().UnixNano()),
		URL:          fullURL,
		CoverURL:     media.CoverURL,
		Title:        title,
		FileSize:     fileSize,
		DecodeKey:    media.DecodeKey,
		MediaType:    media.MediaType,
		FileFormats:  formats,
		Specs:        specs,
		Author:       author,
		AuthorAvatar: authorAvatar,
		Duration:     duration,
		Width:        width,
		Height:       height,
	}, nil
}

// contains checks if a string slice contains the given item.
func contains(slice []string, item string) bool {
	return slices.Contains(slice, item)
}
