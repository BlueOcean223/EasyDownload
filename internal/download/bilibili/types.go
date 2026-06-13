package bilibili

import "EasyDownload/internal/config"

// BilibiliVideo represents complete information about a Bilibili video.
// It contains metadata like title, author, and cover image, as well as
// the list of video parts (分P) and available stream qualities.
type BilibiliVideo struct {
	BV               string           `json:"bv"`                   // BV ID (e.g., "BV1xx411c7mD") - the primary video identifier/current episode BV for bangumi
	AV               string           `json:"av"`                   // AV ID (e.g., "av170001") - legacy video identifier/current episode AV for bangumi
	Title            string           `json:"title"`                // Video title or bangumi season title
	Cover            string           `json:"cover"`                // Cover image URL
	Author           string           `json:"author"`               // Video uploader's username, or content source for bangumi
	Duration         int              `json:"duration"`             // Total/current video duration in seconds
	Desc             string           `json:"desc"`                 // Video description
	Parts            []BilibiliPart   `json:"parts"`                // Multi-part video list (分P) or bangumi episodes, at least one part exists
	Streams          []BilibiliStream `json:"streams"`              // Available streams for the first/current part (for backward compatibility)
	SeasonID         int              `json:"seasonId,omitempty"`   // Bangumi season_id
	MediaID          int              `json:"mediaId,omitempty"`    // Bangumi media_id
	EpID             int64            `json:"epId,omitempty"`       // Current bangumi episode id
	Badge            string           `json:"badge,omitempty"`      // Current episode badge: "" | "限免" | "会员" | "预告"
	SeasonType       int              `json:"seasonType,omitempty"` // 1=番剧 2=电影 3=纪录片 4=国创 5=电视剧 7=综艺
	IsBangumi        bool             `json:"isBangumi"`            // Whether this video uses PGC/bangumi APIs
	TotalEps         int              `json:"totalEps,omitempty"`   // Total episode count returned by season API
	CurrentPartIndex int              `json:"currentPartIndex"`     // 0-based current part/episode index
}

// BilibiliPart represents a single part (分P) of a Bilibili video.
// Bilibili videos can have multiple parts, each with its own CID and stream URLs.
// Each part is essentially an independent video segment that can be downloaded separately.
type BilibiliPart struct {
	CID         int64            `json:"cid"`                   // Content ID - unique identifier for this part's media content
	Page        int              `json:"page"`                  // Part/episode number (1-indexed)
	PartName    string           `json:"partName"`              // Part title/name
	Duration    int              `json:"duration"`              // Part duration in seconds
	Streams     []BilibiliStream `json:"streams,omitempty"`     // Available streams for this part (lazy-loaded on demand)
	BV          string           `json:"bv,omitempty"`          // Bangumi episode BV ID (episodes can have different BVs)
	AID         int64            `json:"aid,omitempty"`         // Bangumi episode AV/AID
	EpID        int64            `json:"epId,omitempty"`        // Bangumi episode id
	Badge       string           `json:"badge,omitempty"`       // "" | "限免" | "会员" | "预告"
	BadgeType   int              `json:"badgeType,omitempty"`   // 0=free, 1=limited free, 3=VIP
	SectionType int              `json:"sectionType,omitempty"` // 0=formal, 1=preview, 2=extra/SP
	Cover       string           `json:"cover,omitempty"`       // Episode cover image URL
}

// BilibiliStream represents an available video stream quality option.
// Modern Bilibili videos use DASH format where video and audio are separate streams
// that need to be downloaded independently and merged using FFmpeg.
//
// Quality values (qn parameter):
//   - 127: 8K Ultra HD
//   - 126: Dolby Vision (杜比视界)
//   - 125: HDR True Color
//   - 120: 4K Ultra HD (requires login + VIP)
//   - 116: 1080P60 High Frame Rate (requires login + VIP)
//   - 112: 1080P+ High Bitrate (requires login + VIP)
//   - 80:  1080P Full HD (requires login for some videos)
//   - 74:  720P60 High Frame Rate
//   - 64:  720P HD
//   - 32:  480P Standard Definition
//   - 16:  360P Low Definition
type BilibiliStream struct {
	Quality         int      `json:"quality"`               // Quality ID (qn value), higher means better quality
	QualityName     string   `json:"qualityName"`           // Human-readable quality name (e.g., "1080P", "4K")
	Format          string   `json:"format"`                // Stream format, typically "dash" for modern videos
	Size            int64    `json:"size"`                  // Estimated file size in bytes (video + audio)
	VideoURL        string   `json:"videoUrl"`              // Direct URL to video stream (m4s format for DASH)
	AudioURL        string   `json:"audioUrl"`              // Direct URL to audio stream (m4s format for DASH)
	Width           int      `json:"width"`                 // Video width in pixels
	Height          int      `json:"height"`                // Video height in pixels
	FrameRate       string   `json:"frameRate"`             // Frame rate, e.g. "30.000", "29.412"
	Codecs          string   `json:"codecs"`                // Codec string, e.g. "avc1.640033", "hev1.1.6.L120.90"
	CodecID         int      `json:"codecId"`               // Codec ID: 7=H.264, 12=HEVC, 13=AV1
	MimeType        string   `json:"mimeType"`              // MIME type, e.g. "video/mp4"
	BackupURLs      []string `json:"backupUrls"`            // Fallback CDN URLs for video stream
	AudioBackupURLs []string `json:"audioBackupUrls"`       // Fallback CDN URLs for audio stream
	DRMKey          string   `json:"drmKey,omitempty"`      // Decryption key for DRM streams when available
	DRMTechType     int      `json:"drmTechType,omitempty"` // 0=none, 2=Bilibili DRM
	KID             string   `json:"kid,omitempty"`         // DRM key id extracted from stream metadata
	BiliDRMURI      string   `json:"biliDrmUri,omitempty"`  // Raw bilidrm URI from API when present
}

// FFmpegManagerInterface defines the interface for FFmpeg management.
// It provides methods to check FFmpeg availability and merge video/audio streams.
// DASH format videos require FFmpeg to combine separate video and audio streams.
type FFmpegManagerInterface interface {
	GetPath() string                                     // Returns the path to FFmpeg executable
	IsAvailable() bool                                   // Returns true if FFmpeg is usable
	Merge(videoPath, audioPath, outputPath string) error // Merges video and audio into a single file
}

// ConfigManagerInterface defines the interface for configuration management.
// It allows the downloader to persist settings like SESSDATA across sessions.
type ConfigManagerInterface interface {
	Get() *config.Config             // Returns the current configuration
	Set(key string, value any) error // Updates a configuration value
}

// BilibiliQRCode represents QR code login information.
// The URL should be displayed as a QR code for users to scan with the Bilibili mobile app.
// The QRCodeKey is used to poll the login status after the QR code is displayed.
type BilibiliQRCode struct {
	URL       string `json:"url"`       // QR code content URL - encode this as a QR code image
	QRCodeKey string `json:"qrcodeKey"` // Unique key for polling login status via PollQRCodeStatus()
}

// BilibiliLoginStatus represents the result of polling QR code login status.
// This struct is returned by PollQRCodeStatus() to indicate the current state
// of the QR code login process.
//
// Status Codes:
//   - 0:     Login successful - SessData field contains the authentication cookie
//   - 86038: QR code expired - generate a new QR code
//   - 86090: QR code scanned - waiting for user to confirm on mobile app
//   - 86101: QR code not scanned - user hasn't scanned the QR code yet
type BilibiliLoginStatus struct {
	Code     int    `json:"code"`     // Status code indicating login progress (see above)
	Message  string `json:"message"`  // Human-readable status message from API
	SessData string `json:"sessData"` // SESSDATA authentication cookie (only set when Code=0)
}

// BilibiliUserInfo represents information about the currently logged-in Bilibili user.
// This is retrieved via GetUserInfo() using the stored SESSDATA cookie.
// VIP status affects available video qualities - VIP users can access 1080P+, 4K, etc.
type BilibiliUserInfo struct {
	IsLogin   bool   `json:"isLogin"`   // True if user is logged in with valid SESSDATA
	UID       int64  `json:"uid"`       // User's unique ID (Mid)
	Username  string `json:"username"`  // User's display name (Uname)
	Face      string `json:"face"`      // Avatar image URL
	IsVIP     bool   `json:"isVip"`     // True if user has active 大会员 membership (VIPType>0 AND VIPStatus==1)
	VIPType   int    `json:"vipType"`   // VIP type: 0=none, 1=monthly (月度大会员), 2=annual (年度大会员)
	VIPStatus int    `json:"vipStatus"` // VIP status: 0=inactive/expired, 1=active
}
