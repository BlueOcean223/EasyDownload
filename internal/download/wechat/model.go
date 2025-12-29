package wechat

import "time"

// VideoInfo represents video information extracted from WeChat.
type VideoInfo struct {
	ID             string      `json:"id"`
	URL            string      `json:"url"` // Full URL (url + urlToken)
	CoverURL       string      `json:"coverUrl"`
	Title          string      `json:"title"` // From description field
	FileSize       float64     `json:"fileSize"`
	DecodeKey      string      `json:"decodeKey"`      // Decryption key
	MediaType      int         `json:"mediaType"`      // 9=image, others=video
	FileFormats    []string    `json:"fileFormats"`    // Available quality formats (for backward compatibility)
	Specs          []VideoSpec `json:"specs"`          // Detailed spec info for each quality
	Author         string      `json:"author"`         // Author nickname from contact
	AuthorAvatar   string      `json:"authorAvatar"`   // Author avatar URL from contact
	Duration       int         `json:"duration"`       // Video duration in milliseconds
	Width          int         `json:"width"`          // Video width
	Height         int         `json:"height"`         // Video height
	IsCurrentVideo bool        `json:"isCurrentVideo"` // Whether this is the current playing video
	// Diagnostics / context from injected JS (optional)
	PageKey string `json:"pageKey,omitempty"`
	Href    string `json:"href,omitempty"`
	TS      int64  `json:"ts,omitempty"`
	Source  string `json:"source,omitempty"`
}

// VideoSpec represents video specification for a specific quality.
type VideoSpec struct {
	FileFormat string `json:"fileFormat"`
	Width      int    `json:"width"`
	Height     int    `json:"height"`
	DurationMs int    `json:"durationMs"`
}

// objectDescMedia represents a single media item in objectDesc.
type objectDescMedia struct {
	URL       string           `json:"url"`
	URLToken  string           `json:"urlToken"`
	CoverURL  string           `json:"coverUrl"`
	FileSize  interface{}      `json:"fileSize"` // Can be string or number
	DecodeKey string           `json:"decodeKey"`
	MediaType int              `json:"mediaType"`
	Spec      []objectDescSpec `json:"spec"`
}

// objectDescSpec represents video specification in objectDesc.
type objectDescSpec struct {
	FileFormat string `json:"fileFormat"`
	DurationMs int    `json:"durationMs"`
	Width      int    `json:"width"`
	Height     int    `json:"height"`
}

// objectDescContact represents author contact information in objectDesc.
type objectDescContact struct {
	Username string `json:"username"`
	Nickname string `json:"nickname"`
	HeadURL  string `json:"head_url"`
}

// videoPayload represents the new payload format from injected JS.
// This format includes contact at the top level (not nested in objectDesc).
type videoPayload struct {
	ID          string             `json:"id"`
	Media       []objectDescMedia  `json:"media"`
	Description string             `json:"description"`
	Title       string             `json:"title,omitempty"`
	DomTitle    string             `json:"domTitle,omitempty"`
	Contact     *objectDescContact `json:"contact"`
	PageKey     string             `json:"pageKey"`
	Href        string             `json:"href"`
	TS          int64              `json:"ts"`
	Source      string             `json:"source"`
}

// wxProfilePayload represents an alternative payload shape used by wx_channels_download style injectors.
// It typically contains the already-concatenated URL and decode key at the top level.
type wxProfilePayload struct {
	Type       string             `json:"type"` // "media" or "picture"
	Title      string             `json:"title"`
	CoverURL   string             `json:"coverUrl"`
	URL        string             `json:"url"`
	Size       float64            `json:"size"`
	Key        string             `json:"key"`
	ID         string             `json:"id"`
	NonceID    string             `json:"nonce_id"`
	Nickname   string             `json:"nickname"`
	Duration   int                `json:"duration"`   // ms (wx_channels_download)
	DurationMs int                `json:"durationMs"` // ms (some variants)
	Width      int                `json:"width"`
	Height     int                `json:"height"`
	Spec       []objectDescSpec   `json:"spec"`
	FileFormat []string           `json:"fileFormat"`
	Contact    *objectDescContact `json:"contact"`
}

// objectDesc represents the WeChat video objectDesc structure (legacy format).
type objectDesc struct {
	Media       []objectDescMedia  `json:"media"`
	Description string             `json:"description"`
	Contact     *objectDescContact `json:"contact"`
}

// recentVideoCache stores a recently seen video with its timestamp.
// Used for deduplication to avoid processing the same video multiple times.
type recentVideoCache struct {
	Video VideoInfo // The cached video information
	Ts    time.Time // When this video was last seen
}
