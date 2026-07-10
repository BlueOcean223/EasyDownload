// Package detection owns the domain model and merge semantics for media
// discovered by sniffers and proxy adapters.
package detection

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"
)

// Source identifies the boundary that discovered a media item. It is kept
// separate from Platform so independent detectors never merge accidentally.
type Source string

const (
	SourceWeChatProxy Source = "wechat_proxy"
)

// Resource is a private, backend-only downloadable candidate. URL, request
// headers, and decryption material must never be serialized to Wails.
type Resource struct {
	ID string
	// ExplicitID records that the adapter supplied a stable rendition identity.
	// It remains backend-only and lets Store prefer that identity when an older
	// URL-derived candidate is upgraded by a richer callback shape.
	ExplicitID bool
	URL        string
	Headers    map[string]string
	DecodeKey  string
	FileFormat string
	MimeType   string
	Quality    string
	Width      int
	Height     int
	DurationMS int
	SizeBytes  int64
	Default    bool
}

// Video is the sole backend domain model for detected media.
type Video struct {
	ID           string
	Source       Source
	Platform     string
	Title        string
	Author       string
	PageURL      string
	CoverURL     string
	Candidates   []Resource
	DetectedAt   time.Time
	LastSeenAt   time.Time
	Metadata     map[string]string
	AuthorAvatar string
	DurationMS   int
	Width        int
	Height       int
	IsCurrent    bool
}

// ResourceDTO is the public candidate projection. ID is opaque to the
// frontend; the backend resolves it back to the private Resource.
type ResourceDTO struct {
	ID         string `json:"id"`
	Label      string `json:"label"`
	Quality    string `json:"quality,omitempty"`
	FileFormat string `json:"fileFormat,omitempty"`
	MimeType   string `json:"mimeType,omitempty"`
	Width      int    `json:"width,omitempty"`
	Height     int    `json:"height,omitempty"`
	DurationMS int    `json:"durationMs,omitempty"`
	SizeBytes  int64  `json:"sizeBytes,omitempty"`
	Encrypted  bool   `json:"encrypted,omitempty"`
	Default    bool   `json:"default,omitempty"`
}

// VideoDTO is the public Wails/HTTP projection. It intentionally excludes
// media URLs, request headers, decode keys, and unreviewed metadata.
type VideoDTO struct {
	ID           string        `json:"id"`
	Source       Source        `json:"source"`
	Platform     string        `json:"platform"`
	Title        string        `json:"title"`
	Author       string        `json:"author,omitempty"`
	PageURL      string        `json:"pageUrl,omitempty"`
	CoverURL     string        `json:"coverUrl,omitempty"`
	AuthorAvatar string        `json:"authorAvatar,omitempty"`
	DurationMS   int           `json:"durationMs,omitempty"`
	Width        int           `json:"width,omitempty"`
	Height       int           `json:"height,omitempty"`
	IsCurrent    bool          `json:"isCurrent,omitempty"`
	Candidates   []ResourceDTO `json:"candidates"`
	DetectedAt   time.Time     `json:"detectedAt"`
	LastSeenAt   time.Time     `json:"lastSeenAt"`
}

// PublicSnapshot is the authoritative, revisioned list consumed by clients.
type PublicSnapshot struct {
	Revision uint64     `json:"revision"`
	Videos   []VideoDTO `json:"videos"`
}

// PublicChange is emitted for every store mutation. Clients replace their
// list with Snapshot when Revision is newer than the one they have applied.
type PublicChange struct {
	Type      ChangeType     `json:"type"`
	ChangedID string         `json:"changedId,omitempty"`
	Snapshot  PublicSnapshot `json:"snapshot"`
}

// Identity contains stable inputs used to assign a source-scoped video ID.
type Identity struct {
	Source            Source
	Platform          string
	PlatformContentID string
	PageURL           string
	PrimaryURL        string
}

// StableID creates an identity without mutable presentation fields.
func StableID(identity Identity) string {
	source := normalizedToken(string(identity.Source), "unknown_source")
	platform := normalizedToken(identity.Platform, "unknown_platform")

	if contentID := strings.TrimSpace(identity.PlatformContentID); contentID != "" {
		// Platform identifiers are identity material, not presentation data. Some
		// adapters derive them from signed URL parameters, so the public opaque ID
		// must never embed the original value.
		return source + ":" + platform + ":content:" + shortHash(contentID)
	}
	if pageURL := normalizeURL(identity.PageURL); pageURL != "" {
		return source + ":" + platform + ":page:" + shortHash(pageURL)
	}
	if primaryURL := normalizeURL(identity.PrimaryURL); primaryURL != "" {
		return source + ":" + platform + ":media:" + shortHash(primaryURL)
	}
	return source + ":" + platform + ":raw:" + shortHash(strings.TrimSpace(identity.PrimaryURL))
}

// CandidateID assigns a stable, video-scoped opaque ID. Platform adapters may
// supply a stronger ID explicitly; otherwise format and normalized URL form the
// conservative identity.
func CandidateID(videoID string, resource Resource) string {
	if id := strings.TrimSpace(resource.ID); id != "" {
		prefix := videoID + ":candidate:"
		if strings.HasPrefix(id, prefix) {
			return id
		}
		return prefix + shortHash("adapter|"+id)
	}
	identity := strings.Join([]string{
		strings.TrimSpace(resource.FileFormat),
		normalizeURL(resource.URL),
	}, "|")
	return videoID + ":candidate:" + shortHash(identity)
}

func (video Video) publicDTO() VideoDTO {
	candidates := make([]ResourceDTO, 0, len(video.Candidates))
	for _, resource := range video.Candidates {
		candidates = append(candidates, ResourceDTO{
			ID:         resource.ID,
			Label:      candidateLabel(resource),
			Quality:    resource.Quality,
			FileFormat: resource.FileFormat,
			MimeType:   resource.MimeType,
			Width:      resource.Width,
			Height:     resource.Height,
			DurationMS: resource.DurationMS,
			SizeBytes:  resource.SizeBytes,
			Encrypted:  strings.TrimSpace(resource.DecodeKey) != "",
			Default:    resource.Default,
		})
	}
	return VideoDTO{
		ID:           video.ID,
		Source:       video.Source,
		Platform:     video.Platform,
		Title:        video.Title,
		Author:       video.Author,
		PageURL:      video.PageURL,
		CoverURL:     video.CoverURL,
		AuthorAvatar: video.AuthorAvatar,
		DurationMS:   video.DurationMS,
		Width:        video.Width,
		Height:       video.Height,
		IsCurrent:    video.IsCurrent,
		Candidates:   candidates,
		DetectedAt:   video.DetectedAt,
		LastSeenAt:   video.LastSeenAt,
	}
}

func candidateLabel(resource Resource) string {
	if resource.Default && strings.TrimSpace(resource.FileFormat) == "" {
		return "原始画质"
	}
	if label := strings.TrimSpace(resource.Quality); label != "" {
		return label
	}
	if resource.Height > 0 {
		return fmt.Sprintf("%dP", resource.Height)
	}
	if format := strings.TrimSpace(resource.FileFormat); format != "" {
		return format
	}
	return "可用资源"
}

func normalizedToken(value, fallback string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return fallback
	}
	return strings.NewReplacer(":", "_", "/", "_", "\\", "_").Replace(value)
}

func shortHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:12])
}

// normalizeURL is deliberately conservative: it normalizes syntax but keeps
// all query values. Platform adapters may provide a stronger content ID when
// signed URLs rotate; this helper must not guess which parameters are volatile.
func normalizeURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return raw
	}
	u.Scheme = strings.ToLower(u.Scheme)
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	if port != "" && !((u.Scheme == "http" && port == "80") || (u.Scheme == "https" && port == "443")) {
		host += ":" + port
	}
	u.Host = host
	u.Fragment = ""
	query := u.Query()
	for key, values := range query {
		sort.Strings(values)
		query[key] = values
	}
	u.RawQuery = query.Encode()
	return u.String()
}

func normalizeVideo(video Video, now time.Time) Video {
	video.Platform = strings.ToLower(strings.TrimSpace(video.Platform))
	if video.Platform == "" {
		video.Platform = "unknown"
	}
	if video.Source == "" {
		video.Source = Source("unknown")
	}
	primaryURL := ""
	if len(video.Candidates) > 0 {
		primaryURL = video.Candidates[0].URL
	}
	if video.ID == "" {
		video.ID = StableID(Identity{Source: video.Source, Platform: video.Platform, PageURL: video.PageURL, PrimaryURL: primaryURL})
	}
	if video.DetectedAt.IsZero() {
		video.DetectedAt = now
	}
	if video.LastSeenAt.IsZero() || video.LastSeenAt.Before(video.DetectedAt) {
		video.LastSeenAt = now
	}
	for i := range video.Candidates {
		video.Candidates[i].URL = strings.TrimSpace(video.Candidates[i].URL)
		if strings.TrimSpace(video.Candidates[i].ID) != "" {
			video.Candidates[i].ExplicitID = true
		}
		video.Candidates[i].ID = CandidateID(video.ID, video.Candidates[i])
	}
	video.Candidates = mergeResources(nil, video.Candidates)
	ensureDefaultCandidate(video.Candidates)
	return video
}

func ensureDefaultCandidate(resources []Resource) {
	if len(resources) == 0 {
		return
	}
	defaultIndex := -1
	for i := range resources {
		if resources[i].Default && defaultIndex == -1 {
			defaultIndex = i
			continue
		}
		resources[i].Default = false
	}
	if defaultIndex == -1 {
		resources[0].Default = true
	}
}
