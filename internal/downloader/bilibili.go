// Package downloader provides video downloading functionality for various platforms.
//
// This file implements the Bilibili video parser and downloader, which supports:
//   - Parsing Bilibili video URLs (BV/AV format)
//   - Fetching video metadata including multi-part (分P) information
//   - QR code login authentication for accessing higher quality streams
//   - Downloading DASH format videos with separate video/audio streams
//   - Resumable downloads with progress tracking
//   - FFmpeg integration for merging video and audio streams
//
// Bilibili API Overview:
// The downloader interacts with several Bilibili APIs:
//   - Video info API: /x/web-interface/view - fetches video metadata
//   - Play URL API: /x/player/playurl - fetches stream URLs with quality options
//   - QR Login API: /x/passport-login/web/qrcode/* - handles QR code authentication
//   - User info API: /x/web-interface/nav - fetches logged-in user information
//
// Authentication:
// Higher quality streams (1080P+, 4K, etc.) require user authentication via SESSDATA cookie.
// The downloader supports QR code login flow and securely stores credentials.
package downloader

import (
	"EasyDownload/internal/config"
	"EasyDownload/internal/credential"
	"EasyDownload/internal/logger"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"
)

// BilibiliVideo represents complete information about a Bilibili video.
// It contains metadata like title, author, and cover image, as well as
// the list of video parts (分P) and available stream qualities.
type BilibiliVideo struct {
	BV       string           `json:"bv"`       // BV ID (e.g., "BV1xx411c7mD") - the primary video identifier
	AV       string           `json:"av"`       // AV ID (e.g., "av170001") - legacy video identifier
	Title    string           `json:"title"`    // Video title
	Cover    string           `json:"cover"`    // Cover image URL
	Author   string           `json:"author"`   // Video uploader's username
	Duration int              `json:"duration"` // Total video duration in seconds
	Desc     string           `json:"desc"`     // Video description
	Parts    []BilibiliPart   `json:"parts"`    // Multi-part video list (分P), at least one part exists
	Streams  []BilibiliStream `json:"streams"`  // Available streams for the first part (for backward compatibility)
}

// BilibiliPart represents a single part (分P) of a Bilibili video.
// Bilibili videos can have multiple parts, each with its own CID and stream URLs.
// Each part is essentially an independent video segment that can be downloaded separately.
type BilibiliPart struct {
	CID      int64            `json:"cid"`               // Content ID - unique identifier for this part's media content
	Page     int              `json:"page"`              // Part number (1-indexed)
	PartName string           `json:"partName"`          // Part title/name
	Duration int              `json:"duration"`          // Part duration in seconds
	Streams  []BilibiliStream `json:"streams,omitempty"` // Available streams for this part (lazy-loaded on demand)
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
	Quality     int    `json:"quality"`     // Quality ID (qn value), higher means better quality
	QualityName string `json:"qualityName"` // Human-readable quality name (e.g., "1080P", "4K")
	Format      string `json:"format"`      // Stream format, typically "dash" for modern videos
	Size        int64  `json:"size"`        // Estimated file size in bytes (video + audio)
	VideoURL    string `json:"videoUrl"`    // Direct URL to video stream (m4s format for DASH)
	AudioURL    string `json:"audioUrl"`    // Direct URL to audio stream (m4s format for DASH)
}

// FFmpegManagerInterface defines the interface for FFmpeg management.
// It provides methods to check FFmpeg availability and merge video/audio streams.
// DASH format videos require FFmpeg to combine separate video and audio streams.
type FFmpegManagerInterface interface {
	GetPath() string                                    // Returns the path to FFmpeg executable
	IsAvailable() bool                                  // Returns true if FFmpeg is usable
	Merge(videoPath, audioPath, outputPath string) error // Merges video and audio into a single file
}

// BilibiliDownloader handles Bilibili video parsing and downloading.
// It supports authenticated downloads for higher quality streams and provides
// resumable download capabilities with progress tracking.
//
// Authentication Flow:
// 1. Call GetQRCode() to generate a QR code for login
// 2. User scans QR code with Bilibili mobile app
// 3. Poll PollQRCodeStatus() until login succeeds
// 4. SESSDATA cookie is automatically saved to secure credential storage
// 5. Subsequent API calls include SESSDATA for authenticated access
//
// Download Flow:
// 1. Parse URL to extract video ID (BV/AV)
// 2. Fetch video info including available qualities
// 3. Select quality and download DASH streams (video + audio separately)
// 4. Merge streams using FFmpeg
type BilibiliDownloader struct {
	sessData      string       // SESSDATA cookie for authenticated requests (enables higher quality)
	sessDataMu    sync.RWMutex // Mutex for thread-safe sessData access
	ffmpegPath    string       // Path to FFmpeg executable (cached after first lookup)
	ffmpegManager FFmpegManagerInterface // Optional FFmpeg manager for advanced merging
	configManager ConfigManagerInterface // Optional config manager for settings persistence
	rateLimiter   *QRCodeRateLimiter     // Rate limiter to prevent excessive QR code polling
}

// QRCodeRateLimiter implements rate limiting for QR code login status polling.
// It prevents excessive API calls during the QR code login process by enforcing
// a minimum interval between polls for the same QR code key.
type QRCodeRateLimiter struct {
	lastPoll map[string]time.Time // Maps QR code key to last poll timestamp
	mu       sync.Mutex           // Mutex for thread-safe map access
}

// NewQRCodeRateLimiter creates a new rate limiter for QR code login polling.
// The rate limiter helps prevent excessive API calls during the login process.
func NewQRCodeRateLimiter() *QRCodeRateLimiter {
	return &QRCodeRateLimiter{
		lastPoll: make(map[string]time.Time),
	}
}

// Allow checks if a poll request is allowed based on the minimum interval.
// It returns true if the request is allowed, false if rate limited.
// The minInterval specifies the minimum time between polls for the same key.
// Old entries (>10 minutes) are automatically cleaned up.
func (rl *QRCodeRateLimiter) Allow(key string, minInterval time.Duration) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	lastTime, exists := rl.lastPoll[key]

	if exists && now.Sub(lastTime) < minInterval {
		return false
	}

	rl.lastPoll[key] = now

	// Clean up old entries (older than 10 minutes)
	for k, t := range rl.lastPoll {
		if now.Sub(t) > 10*time.Minute {
			delete(rl.lastPoll, k)
		}
	}

	return true
}

// ConfigManagerInterface defines the interface for configuration management.
// It allows the downloader to persist settings like SESSDATA across sessions.
type ConfigManagerInterface interface {
	Get() *config.Config         // Returns the current configuration
	Set(key string, value any) error // Updates a configuration value
}

// NewBilibiliDownloader creates a new BilibiliDownloader instance.
// The downloader is initialized with a rate limiter for QR code polling.
// Call SetSessData(), LoadSessData(), or use QR code login to authenticate.
func NewBilibiliDownloader() *BilibiliDownloader {
	return &BilibiliDownloader{
		rateLimiter: NewQRCodeRateLimiter(),
	}
}

// SetConfigManager sets the configuration manager for settings persistence.
// This enables saving SESSDATA and other settings across sessions.
func (bd *BilibiliDownloader) SetConfigManager(cm ConfigManagerInterface) {
	bd.configManager = cm
}

// SaveSessData saves the SESSDATA cookie to secure credential storage.
// On Windows, this uses Windows Credential Manager; on macOS, the Keychain.
// The SESSDATA is also cached in memory for immediate use.
// Returns an error if secure storage is unavailable.
func (bd *BilibiliDownloader) SaveSessData(sessData string) error {
	bd.sessDataMu.Lock()
	bd.sessData = sessData
	bd.sessDataMu.Unlock()

	// Store to secure credential storage (Windows Credential Manager, macOS Keychain, etc.)
	if err := credential.StoreBilibiliSessData(sessData); err != nil {
		logger.Error("Failed to store SESSDATA: credential storage unavailable")
		return fmt.Errorf("failed to store credential securely")
	}
	logger.Info("SESSDATA stored to secure credential storage")
	return nil
}

// LoadSessData loads the SESSDATA cookie from secure credential storage.
// If found, the SESSDATA is cached in memory for use in subsequent requests.
// Returns the loaded SESSDATA and an error if storage is unavailable.
func (bd *BilibiliDownloader) LoadSessData() (string, error) {
	// Load from secure credential storage
	sessData, err := credential.GetBilibiliSessData()
	if err != nil {
		logger.Error("Failed to load SESSDATA: credential storage unavailable")
		return "", fmt.Errorf("failed to load credential securely")
	}
	if sessData != "" {
		bd.sessDataMu.Lock()
		bd.sessData = sessData
		bd.sessDataMu.Unlock()
		logger.Debug("SESSDATA loaded from secure credential storage")
	}
	return sessData, nil
}

// GetSessData returns the current SESSDATA cookie value.
// This method is thread-safe and can be called concurrently.
func (bd *BilibiliDownloader) GetSessData() string {
	bd.sessDataMu.RLock()
	defer bd.sessDataMu.RUnlock()
	return bd.sessData
}

// SetSessData sets the SESSDATA cookie for authenticated API requests.
// This enables access to higher quality streams (1080P+, 4K, etc.).
// This method is thread-safe and can be called concurrently.
func (bd *BilibiliDownloader) SetSessData(sessData string) {
	bd.sessDataMu.Lock()
	defer bd.sessDataMu.Unlock()
	bd.sessData = sessData
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

// GetQRCode generates a QR code for Bilibili login.
// The returned BilibiliQRCode contains a URL that should be displayed as a QR code
// for users to scan with the Bilibili mobile app.
// After displaying the QR code, call PollQRCodeStatus() with the QRCodeKey to
// check login progress.
//
// API: POST https://passport.bilibili.com/x/passport-login/web/qrcode/generate
func (bd *BilibiliDownloader) GetQRCode() (*BilibiliQRCode, error) {
	apiURL := "https://passport.bilibili.com/x/passport-login/web/qrcode/generate"

	req, err := http.NewRequest("GET", apiURL, nil)
	if err != nil {
		return nil, err
	}

	bd.setHeaders(req)

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var result struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    struct {
			URL       string `json:"url"`
			QRCodeKey string `json:"qrcode_key"`
		} `json:"data"`
	}

	if err := json.Unmarshal(body, &result); err != nil {
		return nil, err
	}

	if result.Code != 0 {
		return nil, fmt.Errorf("failed to generate QR code: %s", result.Message)
	}

	return &BilibiliQRCode{
		URL:       result.Data.URL,
		QRCodeKey: result.Data.QRCodeKey,
	}, nil
}

// PollQRCodeStatus checks the QR code login status.
// Call this method periodically after displaying the QR code to check if
// the user has scanned and confirmed the login.
//
// Rate limiting: This method enforces a minimum 1.5-second interval between polls
// for the same QR code key to avoid excessive API calls.
//
// Login Flow:
// 1. Generate QR code with GetQRCode()
// 2. Display QR code to user
// 3. Poll this method every 2-3 seconds
// 4. Check returned Code: 0=success, 86038=expired, 86090=scanned, 86101=not scanned
// 5. On success (Code=0), SESSDATA is automatically saved
//
// API: GET https://passport.bilibili.com/x/passport-login/web/qrcode/poll
func (bd *BilibiliDownloader) PollQRCodeStatus(qrcodeKey string) (*BilibiliLoginStatus, error) {
	// Rate limiting: minimum 1.5 seconds between polls for the same QR code
	if !bd.rateLimiter.Allow(qrcodeKey, 1500*time.Millisecond) {
		return nil, fmt.Errorf("polling too frequently, please wait")
	}

	apiURL := fmt.Sprintf("https://passport.bilibili.com/x/passport-login/web/qrcode/poll?qrcode_key=%s", qrcodeKey)

	req, err := http.NewRequest("GET", apiURL, nil)
	if err != nil {
		return nil, err
	}

	bd.setHeaders(req)

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var result struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    struct {
			URL          string `json:"url"`
			RefreshToken string `json:"refresh_token"`
			Timestamp    int64  `json:"timestamp"`
			Code         int    `json:"code"`
			Message      string `json:"message"`
		} `json:"data"`
	}

	if err := json.Unmarshal(body, &result); err != nil {
		return nil, err
	}

	status := &BilibiliLoginStatus{
		Code:    result.Data.Code,
		Message: result.Data.Message,
	}

	// Code meanings:
	// 0 = success
	// 86038 = QR code expired
	// 86090 = QR code scanned, waiting for confirmation
	// 86101 = QR code not scanned

	if result.Data.Code == 0 {
		// Login successful, extract SESSDATA from cookies in response header
		for _, cookie := range resp.Cookies() {
			if cookie.Name == "SESSDATA" {
				status.SessData = cookie.Value
				break
			}
		}

		// If SESSDATA not in cookies, try to parse from URL
		if status.SessData == "" && result.Data.URL != "" {
			// URL format: https://passport.bilibili.com/...?SESSDATA=xxx&...
			if strings.Contains(result.Data.URL, "SESSDATA=") {
				parts := strings.Split(result.Data.URL, "SESSDATA=")
				if len(parts) > 1 {
					sessData := strings.Split(parts[1], "&")[0]
					status.SessData = sessData
				}
			}
		}

		// Auto-save SESSDATA if obtained
		if status.SessData != "" {
			bd.SaveSessData(status.SessData)
			logger.Info("Bilibili login successful, SESSDATA saved")
		}
	}

	return status, nil
}

// GetUserInfo retrieves information about the currently logged-in user.
// Returns user details including username, avatar, and VIP membership status.
// If no SESSDATA is set, returns an empty BilibiliUserInfo with IsLogin=false.
//
// VIP membership determines available video qualities:
//   - Non-VIP: Up to 1080P (some videos may be limited to 480P)
//   - VIP: Access to 1080P+, 4K, HDR, and Dolby Vision qualities
//
// API: GET https://api.bilibili.com/x/web-interface/nav
func (bd *BilibiliDownloader) GetUserInfo() (*BilibiliUserInfo, error) {
	if bd.sessData == "" {
		return &BilibiliUserInfo{IsLogin: false}, nil
	}

	apiURL := "https://api.bilibili.com/x/web-interface/nav"

	req, err := http.NewRequest("GET", apiURL, nil)
	if err != nil {
		return nil, err
	}

	bd.setHeaders(req)

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var result struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    struct {
			IsLogin bool   `json:"isLogin"`
			Mid     int64  `json:"mid"`
			Uname   string `json:"uname"`
			Face    string `json:"face"`
			VipType int    `json:"vipType"`
			VIP     struct {
				Type   int `json:"type"`
				Status int `json:"status"`
			} `json:"vip"`
		} `json:"data"`
	}

	if err := json.Unmarshal(body, &result); err != nil {
		return nil, err
	}

	// VIP status: type > 0 AND status == 1 means active VIP
	// type: 0=无会员, 1=月度大会员, 2=年度大会员
	// status: 0=无效/过期, 1=有效
	isVIP := result.Data.VIP.Type > 0 && result.Data.VIP.Status == 1

	userInfo := &BilibiliUserInfo{
		IsLogin:   result.Data.IsLogin,
		UID:       result.Data.Mid,
		Username:  result.Data.Uname,
		Face:      result.Data.Face,
		VIPType:   result.Data.VIP.Type,
		VIPStatus: result.Data.VIP.Status,
		IsVIP:     isVIP,
	}

	logger.Debug("Bilibili user info: uid=%d, username=%s, vipType=%d, vipStatus=%d, isVIP=%v",
		userInfo.UID, userInfo.Username, userInfo.VIPType, userInfo.VIPStatus, userInfo.IsVIP)

	return userInfo, nil
}

// Logout clears the SESSDATA from both memory and secure storage.
// After logout, API requests will be unauthenticated and limited to lower qualities.
// Returns an error if credential deletion fails.
func (bd *BilibiliDownloader) Logout() error {
	bd.sessDataMu.Lock()
	bd.sessData = ""
	bd.sessDataMu.Unlock()

	if err := credential.DeleteBilibiliSessData(); err != nil {
		logger.Error("Failed to delete SESSDATA: credential storage unavailable")
		return fmt.Errorf("failed to delete credential securely")
	}
	logger.Info("SESSDATA deleted from secure credential storage")
	return nil
}

// SetFFmpegPath manually sets the path to the FFmpeg executable.
// FFmpeg is required for downloading DASH format videos (video + audio streams).
func (bd *BilibiliDownloader) SetFFmpegPath(path string) {
	bd.ffmpegPath = path
}

// SetFFmpegManager sets the FFmpeg manager interface for video/audio merging.
// The manager provides more advanced control over the merge process.
func (bd *BilibiliDownloader) SetFFmpegManager(fm FFmpegManagerInterface) {
	bd.ffmpegManager = fm
}

// GetFFmpegPath returns the path to the FFmpeg executable.
// It checks in the following order:
// 1. FFmpegManager (if set)
// 2. Manually set path via SetFFmpegPath
// 3. Common system locations (PATH, ./ffmpeg.exe, C:\ffmpeg\bin\, etc.)
// Returns an empty string if FFmpeg is not found.
func (bd *BilibiliDownloader) GetFFmpegPath() string {
	// First check if FFmpegManager is set
	if bd.ffmpegManager != nil {
		path := bd.ffmpegManager.GetPath()
		if path != "" {
			return path
		}
	}

	if bd.ffmpegPath != "" {
		return bd.ffmpegPath
	}

	// Check common locations
	paths := []string{
		"ffmpeg",
		"ffmpeg.exe",
		filepath.Join(".", "ffmpeg.exe"),
		filepath.Join(".", "bin", "ffmpeg.exe"),
	}

	if runtime.GOOS == "windows" {
		paths = append(paths,
			`C:\ffmpeg\bin\ffmpeg.exe`,
			`C:\Program Files\ffmpeg\bin\ffmpeg.exe`,
		)
	}

	for _, p := range paths {
		if _, err := exec.LookPath(p); err == nil {
			bd.ffmpegPath = p
			return p
		}
	}

	return ""
}

// IsFFmpegAvailable checks if FFmpeg is available on the system.
// FFmpeg is required for downloading DASH format videos where video and audio
// are separate streams that need to be merged.
func (bd *BilibiliDownloader) IsFFmpegAvailable() bool {
	// First check FFmpegManager
	if bd.ffmpegManager != nil && bd.ffmpegManager.IsAvailable() {
		return true
	}
	return bd.GetFFmpegPath() != ""
}

// ParseURL extracts the video ID from a Bilibili URL.
// Supports both BV format (e.g., "https://www.bilibili.com/video/BV1xx411c7mD")
// and legacy AV format (e.g., "https://www.bilibili.com/video/av170001").
// Returns the video ID (e.g., "BV1xx411c7mD" or "av170001") or an error if invalid.
func (bd *BilibiliDownloader) ParseURL(url string) (string, error) {
	// BV format
	bvRegex := regexp.MustCompile(`BV[a-zA-Z0-9]+`)
	if matches := bvRegex.FindString(url); matches != "" {
		return matches, nil
	}

	// AV format
	avRegex := regexp.MustCompile(`av(\d+)`)
	if matches := avRegex.FindStringSubmatch(url); len(matches) > 1 {
		return "av" + matches[1], nil
	}

	return "", fmt.Errorf("invalid Bilibili URL")
}

// ParseURLMust extracts the video ID from a Bilibili URL without returning an error.
// Returns an empty string if the URL is invalid instead of panicking.
// For production code, prefer ParseURL to handle errors explicitly.
func (bd *BilibiliDownloader) ParseURLMust(url string) string {
	bvid, err := bd.ParseURL(url)
	if err != nil {
		return ""
	}
	return bvid
}

// GetVideoInfo fetches video metadata from Bilibili.
// This is a convenience method that returns video info with stream data for
// the first part only (for backward compatibility with single-part videos).
// For multi-part videos, use GetVideoInfoWithParts and GetPartStreams.
//
// API: GET https://api.bilibili.com/x/web-interface/view
func (bd *BilibiliDownloader) GetVideoInfo(bvid string) (*BilibiliVideo, error) {
	video, err := bd.GetVideoInfoWithParts(bvid)
	if err != nil {
		return nil, err
	}

	// Get stream info for the first part
	if len(video.Parts) > 0 {
		aid := int64(0)
		fmt.Sscanf(video.AV, "av%d", &aid)
		streams, err := bd.getStreamInfo(bvid, aid, video.Parts[0].CID, video.Parts[0].Duration)
		if err == nil {
			video.Streams = streams
		}
	}

	return video, nil
}

// GetVideoInfoWithParts fetches video metadata including all parts (分P) from Bilibili.
// Returns a BilibiliVideo containing the video's metadata and list of parts.
// Stream information for each part is NOT included; call GetPartStreams or
// GetAllPartsStreams to fetch stream URLs when needed.
//
// API: GET https://api.bilibili.com/x/web-interface/view?bvid={bvid}
func (bd *BilibiliDownloader) GetVideoInfoWithParts(bvid string) (*BilibiliVideo, error) {
	// API endpoint
	apiURL := fmt.Sprintf("https://api.bilibili.com/x/web-interface/view?bvid=%s", bvid)

	req, err := http.NewRequest("GET", apiURL, nil)
	if err != nil {
		return nil, err
	}

	bd.setHeaders(req)

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var result struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    struct {
			BVID     string `json:"bvid"`
			AID      int64  `json:"aid"`
			Title    string `json:"title"`
			Pic      string `json:"pic"`
			Desc     string `json:"desc"`
			Duration int    `json:"duration"`
			Owner    struct {
				Name string `json:"name"`
			} `json:"owner"`
			CID   int64 `json:"cid"`
			Pages []struct {
				CID      int64  `json:"cid"`
				Page     int    `json:"page"`
				Part     string `json:"part"`
				Duration int    `json:"duration"`
			} `json:"pages"`
		} `json:"data"`
	}

	if err := json.Unmarshal(body, &result); err != nil {
		return nil, err
	}

	if result.Code != 0 {
		return nil, fmt.Errorf("API error: %s", result.Message)
	}

	video := &BilibiliVideo{
		BV:       result.Data.BVID,
		AV:       fmt.Sprintf("av%d", result.Data.AID),
		Title:    result.Data.Title,
		Cover:    result.Data.Pic,
		Author:   result.Data.Owner.Name,
		Duration: result.Data.Duration,
		Desc:     result.Data.Desc,
		Parts:    make([]BilibiliPart, 0, len(result.Data.Pages)),
	}

	// Parse parts (分P)
	if len(result.Data.Pages) > 0 {
		for _, page := range result.Data.Pages {
			part := BilibiliPart{
				CID:      page.CID,
				Page:     page.Page,
				PartName: page.Part,
				Duration: page.Duration,
			}
			video.Parts = append(video.Parts, part)
		}
	} else {
		// Single part video - create a default part
		video.Parts = append(video.Parts, BilibiliPart{
			CID:      result.Data.CID,
			Page:     1,
			PartName: result.Data.Title,
			Duration: result.Data.Duration,
		})
	}

	return video, nil
}

// getStreamInfo fetches available stream URLs and qualities for a video part.
// This is an internal method that calls the Bilibili playurl API.
//
// API: GET https://api.bilibili.com/x/player/playurl
//
// Request Parameters:
//   - bvid: Video BV ID (e.g., "BV1xx411c7mD")
//   - cid:  Content ID for the specific part
//   - fnval: Video stream format flags (bitwise OR):
//       - 1:    MP4 format (legacy)
//       - 16:   DASH format (modern, separate video/audio streams)
//       - 64:   Require HDR video
//       - 128:  Require 4K video
//       - 256:  Require Dolby Audio
//       - 512:  Require Dolby Vision
//       - 1024: Require 8K video
//       - 2048: Require AV1 codec
//       Value 4048 = 16|64|128|256|512|1024|2048 (request all high-quality formats)
//   - fnver: Format version, always 0
//   - fourk: Enable 4K quality (1=enable, 0=disable)
//
// Quality (qn) values returned in response:
//   - 127: 8K Ultra HD
//   - 126: Dolby Vision
//   - 125: HDR True Color
//   - 120: 4K Ultra HD (requires VIP)
//   - 116: 1080P60 High Frame Rate (requires VIP)
//   - 112: 1080P+ High Bitrate (requires VIP)
//   - 80:  1080P Full HD
//   - 74:  720P60 High Frame Rate
//   - 64:  720P HD
//   - 32:  480P Standard Definition
//   - 16:  360P Low Definition
func (bd *BilibiliDownloader) getStreamInfo(bvid string, _ int64, cid int64, duration int) ([]BilibiliStream, error) {
	// Play URL API - fnval=4048 requests DASH format with all quality options
	apiURL := fmt.Sprintf("https://api.bilibili.com/x/player/playurl?bvid=%s&cid=%d&fnval=4048&fnver=0&fourk=1", bvid, cid)

	req, err := http.NewRequest("GET", apiURL, nil)
	if err != nil {
		return nil, err
	}

	bd.setHeaders(req)

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var result struct {
		Code int `json:"code"`
		Data struct {
			Quality       int      `json:"quality"`
			AcceptQuality []int    `json:"accept_quality"`
			AcceptDesc    []string `json:"accept_description"`
			Dash          struct {
				Video []struct {
					ID        int    `json:"id"`
					BaseURL   string `json:"baseUrl"`
					Bandwidth int64  `json:"bandwidth"`
					Codecs    string `json:"codecs"`
				} `json:"video"`
				Audio []struct {
					ID        int    `json:"id"`
					BaseURL   string `json:"baseUrl"`
					Bandwidth int64  `json:"bandwidth"`
				} `json:"audio"`
			} `json:"dash"`
		} `json:"data"`
	}

	if err := json.Unmarshal(body, &result); err != nil {
		return nil, err
	}

	if result.Code != 0 {
		return nil, fmt.Errorf("failed to get stream info")
	}

	qualityNames := map[int]string{
		127: "8K",
		126: "杜比视界",
		125: "HDR",
		120: "4K",
		116: "1080P60",
		112: "1080P+",
		80:  "1080P",
		74:  "720P60",
		64:  "720P",
		32:  "480P",
		16:  "360P",
	}

	var streams []BilibiliStream

	// Get best audio bandwidth for size estimation
	var audioBandwidth int64
	if len(result.Data.Dash.Audio) > 0 {
		audioBandwidth = result.Data.Dash.Audio[0].Bandwidth
	}

	// Create stream entries for each quality
	for i, q := range result.Data.AcceptQuality {
		stream := BilibiliStream{
			Quality:     q,
			QualityName: qualityNames[q],
			Format:      "dash",
		}

		if i < len(result.Data.AcceptDesc) {
			stream.QualityName = result.Data.AcceptDesc[i]
		}

		// Find matching video stream
		var videoBandwidth int64
		for _, v := range result.Data.Dash.Video {
			if v.ID == q {
				stream.VideoURL = v.BaseURL
				videoBandwidth = v.Bandwidth
				break
			}
		}

		// Get first audio stream
		if len(result.Data.Dash.Audio) > 0 {
			stream.AudioURL = result.Data.Dash.Audio[0].BaseURL
		}

		// Estimate file size: (video_bandwidth + audio_bandwidth) * duration / 8
		// bandwidth is in bits per second, duration is in seconds
		if duration > 0 && videoBandwidth > 0 {
			stream.Size = (videoBandwidth + audioBandwidth) * int64(duration) / 8
		}

		if stream.VideoURL != "" {
			streams = append(streams, stream)
		}
	}

	return streams, nil
}

// GetPartStreams fetches available stream qualities for a specific video part.
// The partIndex is 0-based (0 for first part, 1 for second, etc.).
// Returns a list of available stream qualities with their URLs and estimated sizes.
func (bd *BilibiliDownloader) GetPartStreams(video *BilibiliVideo, partIndex int) ([]BilibiliStream, error) {
	if partIndex < 0 || partIndex >= len(video.Parts) {
		return nil, fmt.Errorf("invalid part index: %d (total parts: %d)", partIndex, len(video.Parts))
	}

	aid := int64(0)
	fmt.Sscanf(video.AV, "av%d", &aid)

	return bd.getStreamInfo(video.BV, aid, video.Parts[partIndex].CID, video.Parts[partIndex].Duration)
}

// GetAllPartsStreams fetches stream information for all parts of a video concurrently.
// This populates the Streams field for each BilibiliPart in the video.
// Useful for displaying size estimates in a part selector UI before downloading.
// Uses up to 4 concurrent API requests to speed up fetching.
func (bd *BilibiliDownloader) GetAllPartsStreams(video *BilibiliVideo) error {
	if video == nil || len(video.Parts) == 0 {
		return fmt.Errorf("no parts available")
	}

	aid := int64(0)
	fmt.Sscanf(video.AV, "av%d", &aid)

	// Parallel fetch with concurrency limit
	var wg sync.WaitGroup
	var mu sync.Mutex
	sem := make(chan struct{}, 4) // Limit to 4 concurrent requests

	for i := range video.Parts {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			streams, err := bd.getStreamInfo(video.BV, aid, video.Parts[i].CID, video.Parts[i].Duration)
			if err != nil {
				logger.Debug("Failed to get streams for part %d: %v", i, err)
				return
			}

			mu.Lock()
			video.Parts[i].Streams = streams
			mu.Unlock()
		}()
	}

	wg.Wait()
	return nil
}

// DownloadPart downloads a specific part of a Bilibili video.
// Parameters:
//   - video: Video metadata obtained from GetVideoInfo or GetVideoInfoWithParts
//   - partIndex: 0-based index of the part to download
//   - quality: Desired quality level (qn value, e.g., 80 for 1080P, 120 for 4K)
//   - outputPath: Full output file path including filename and .mp4 extension
//   - onProgress: Callback for progress updates (0-100%)
//   - onSizeKnown: Callback when total download size is determined
//
// Returns the final output path and any error encountered.
func (bd *BilibiliDownloader) DownloadPart(video *BilibiliVideo, partIndex int, quality int, outputPath string, onProgress func(float64), onSizeKnown func(int64)) (string, error) {
	return bd.DownloadPartWithContext(context.Background(), video, partIndex, quality, outputPath, onProgress, onSizeKnown)
}

// DownloadPartWithContext downloads a specific part with cancellation support.
// The context can be used to cancel or pause the download.
// When cancelled, temporary files are preserved for resume capability.
// See DownloadPart for parameter descriptions.
func (bd *BilibiliDownloader) DownloadPartWithContext(ctx context.Context, video *BilibiliVideo, partIndex int, quality int, outputPath string, onProgress func(float64), onSizeKnown func(int64)) (string, error) {
	if partIndex < 0 || partIndex >= len(video.Parts) {
		return "", fmt.Errorf("invalid part index: %d (total parts: %d)", partIndex, len(video.Parts))
	}

	part := video.Parts[partIndex]
	logger.Info("Starting download for video: %s, Part %d: %s (quality: %d)", video.Title, part.Page, part.PartName, quality)

	return bd.downloadCore(ctx, video, partIndex, quality, outputPath, onProgress, onSizeKnown)
}

// downloadCore is the core download method that handles DASH format downloads.
// It downloads video and audio streams separately, then merges them using FFmpeg.
//
// Parameters:
//   - ctx: Context for cancellation support
//   - video: Video metadata
//   - partIndex: Part index (-1 uses video.Streams for backward compatibility, >=0 for specific part)
//   - quality: Desired quality level (qn value)
//   - outputPath: Full output path including filename (used to derive temp file paths)
//   - onProgress: Progress callback (0-100%)
//   - onSizeKnown: Callback when total size is known
//
// Download process:
// 1. Fetch stream URLs for the requested quality
// 2. Download video stream to {basePath}_video.m4s
// 3. Download audio stream to {basePath}_audio.m4s
// 4. Merge using FFmpeg to create final .mp4 file
// 5. Clean up temporary files
func (bd *BilibiliDownloader) downloadCore(
	ctx context.Context,
	video *BilibiliVideo,
	partIndex int,
	quality int,
	outputPath string,
	onProgress func(float64),
	onSizeKnown func(int64),
) (string, error) {
	// 获取流信息
	var streams []BilibiliStream
	var err error

	if partIndex == -1 {
		// 使用第一分P的流（向后兼容）
		streams = video.Streams
	} else {
		// 获取指定分P的流
		streams, err = bd.GetPartStreams(video, partIndex)
		if err != nil {
			return "", fmt.Errorf("failed to get streams for part %d: %w", partIndex, err)
		}
	}

	// 查找匹配质量的 stream
	var stream *BilibiliStream
	for i := range streams {
		if streams[i].Quality == quality {
			stream = &streams[i]
			break
		}
	}

	if stream == nil && len(streams) > 0 {
		stream = &streams[0]
		logger.Debug("Requested quality %d not found, using quality %d", quality, stream.Quality)
	}

	if stream == nil {
		logger.Error("No available stream for video: %s", video.Title)
		return "", fmt.Errorf("no available stream")
	}

	// 校验 outputPath 必须以 .mp4 结尾
	if !strings.HasSuffix(outputPath, ".mp4") {
		logger.Warn("outputPath does not end with .mp4, appending: %s", outputPath)
		outputPath = outputPath + ".mp4"
	}

	// 使用传入的 outputPath，确保与 task.FilePath 一致
	// 从 outputPath 派生临时文件路径
	basePath := strings.TrimSuffix(outputPath, ".mp4")
	logger.Debug("Using output path: %s, base path: %s", outputPath, basePath)

	// 处理旧格式（无音频）
	if stream.AudioURL == "" {
		return bd.downloadFileWithContext(ctx, stream.VideoURL, outputPath, 0, onProgress)
	}

	// DASH 格式 - 需要分别下载视频和音频，然后合并
	if !bd.IsFFmpegAvailable() {
		logger.Error("FFmpeg not found, cannot download DASH format video")
		return "", fmt.Errorf("ffmpeg is required for DASH format but not found")
	}
	logger.Debug("Using DASH format, will merge video and audio")

	// 获取内容长度以进行精确的进度计算
	videoSize, _ := bd.getContentLength(stream.VideoURL)
	audioSize, _ := bd.getContentLength(stream.AudioURL)
	logger.Debug("Video size: %d, Audio size: %d", videoSize, audioSize)

	// 通知调用者总文件大小
	if onSizeKnown != nil {
		onSizeKnown(videoSize + audioSize)
	}

	// 创建进度追踪器
	tracker := NewDASHProgressTracker(videoSize, audioSize, onProgress)

	// 临时文件路径 - 使用 basePath 确保与 task.FilePath 一致
	videoPath := basePath + "_video.m4s"
	audioPath := basePath + "_audio.m4s"
	logger.Debug("Temp files: video=%s, audio=%s", videoPath, audioPath)

	// 清理临时文件的辅助函数
	cleanupTempFiles := func() {
		os.Remove(videoPath)
		os.Remove(audioPath)
		os.Remove(videoPath + ".edstate.json")
		os.Remove(audioPath + ".edstate.json")
		logger.Debug("Cleaned up temp files: %s, %s", videoPath, audioPath)
	}

	// 下载视频
	logger.Debug("Downloading video stream to: %s", videoPath)
	if _, err := bd.downloadFileWithContext(ctx, stream.VideoURL, videoPath, videoSize, func(p float64) {
		tracker.UpdateVideoProgress(p)
	}); err != nil {
		// 如果上下文被取消（暂停/取消），保留临时文件以便恢复
		if ctx.Err() != nil {
			logger.Info("Video download interrupted, keeping temp files for resume")
			return "", err
		}
		logger.Error("Failed to download video stream: %v", err)
		cleanupTempFiles()
		return "", fmt.Errorf("failed to download video: %w", err)
	}

	// 在音频下载前检查取消
	select {
	case <-ctx.Done():
		logger.Info("Download paused/cancelled after video, keeping temp files")
		return "", ctx.Err()
	default:
	}

	// 下载音频
	logger.Debug("Downloading audio stream to: %s", audioPath)
	if _, err := bd.downloadFileWithContext(ctx, stream.AudioURL, audioPath, audioSize, func(p float64) {
		tracker.UpdateAudioProgress(p)
	}); err != nil {
		// 如果上下文被取消（暂停/取消），保留临时文件以便恢复
		if ctx.Err() != nil {
			logger.Info("Audio download interrupted, keeping temp files for resume")
			return "", err
		}
		logger.Error("Failed to download audio stream: %v", err)
		cleanupTempFiles()
		return "", fmt.Errorf("failed to download audio: %w", err)
	}

	// 在合并前检查取消
	select {
	case <-ctx.Done():
		logger.Info("Download paused/cancelled before merge, keeping temp files")
		return "", ctx.Err()
	default:
	}

	// 使用 ffmpeg 合并
	logger.Debug("Merging video and audio with FFmpeg")
	tracker.SetMergeProgress(0)

	// 如果可用，使用 FFmpegManager，否则回退到直接执行
	if bd.ffmpegManager != nil {
		if err := bd.ffmpegManager.Merge(videoPath, audioPath, outputPath); err != nil {
			logger.Error("FFmpeg merge failed: %v", err)
			cleanupTempFiles()
			return "", fmt.Errorf("failed to merge video and audio: %w", err)
		}
	} else {
		cmd := exec.CommandContext(ctx, bd.GetFFmpegPath(),
			"-i", videoPath,
			"-i", audioPath,
			"-c", "copy",
			"-y",
			outputPath,
		)

		if err := cmd.Run(); err != nil {
			// 如果在合并期间上下文被取消，保留临时文件
			if ctx.Err() != nil {
				logger.Info("Merge interrupted, keeping temp files")
				return "", ctx.Err()
			}
			logger.Error("FFmpeg merge failed: %v", err)
			cleanupTempFiles()
			return "", fmt.Errorf("failed to merge video and audio: %w", err)
		}
	}

	tracker.SetMergeProgress(100)

	// 成功合并后清理临时文件
	cleanupTempFiles()

	logger.Info("Download completed: %s", outputPath)
	return outputPath, nil
}

// Download downloads the first part of a Bilibili video (for single-part videos).
// For multi-part videos, use DownloadPart to download specific parts.
// Parameters:
//   - video: Video metadata obtained from GetVideoInfo
//   - quality: Desired quality level (qn value)
//   - outputPath: Full output file path including filename and .mp4 extension
//   - onProgress: Callback for progress updates (0-100%)
//   - onSizeKnown: Callback when total download size is determined
func (bd *BilibiliDownloader) Download(video *BilibiliVideo, quality int, outputPath string, onProgress func(progress float64), onSizeKnown func(totalSize int64)) (string, error) {
	return bd.DownloadWithContext(context.Background(), video, quality, outputPath, onProgress, onSizeKnown)
}

// DownloadWithContext downloads the first part of a video with cancellation support.
// The context can be used to cancel or pause the download.
// See Download for parameter descriptions.
func (bd *BilibiliDownloader) DownloadWithContext(ctx context.Context, video *BilibiliVideo, quality int, outputPath string, onProgress func(progress float64), onSizeKnown func(totalSize int64)) (string, error) {
	logger.Info("Starting download for video: %s (quality: %d)", video.Title, quality)
	return bd.downloadCore(ctx, video, -1, quality, outputPath, onProgress, onSizeKnown)
}

// getContentLength fetches the content length of a URL using a HEAD request.
// This is used to determine file sizes before downloading for progress estimation.
func (bd *BilibiliDownloader) getContentLength(url string) (int64, error) {
	req, err := http.NewRequest("HEAD", url, nil)
	if err != nil {
		return 0, err
	}

	bd.setHeaders(req)
	req.Header.Set("Referer", "https://www.bilibili.com/")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	return resp.ContentLength, nil
}

// downloadFileWithContext downloads a file with cancellation and resume support.
// It automatically chooses between multipart download (for large files with range support)
// and sequential download based on file size and server capabilities.
// Resume is supported through partial file detection and range requests.
func (bd *BilibiliDownloader) downloadFileWithContext(ctx context.Context, url, path string, knownSize int64, onProgress func(float64)) (string, error) {
	// Check if file already exists for resume
	var existingSize int64 = 0
	if fileInfo, err := os.Stat(path); err == nil {
		existingSize = fileInfo.Size()
	}
	stateExists := multipartStateExists(path)

	// If we already have the full file, skip download
	if knownSize > 0 && existingSize >= knownSize && !stateExists {
		logger.Info("File already complete: %s (%d bytes)", path, existingSize)
		if onProgress != nil {
			onProgress(100)
		}
		return path, nil
	}

	// If resuming a multipart download (resume state exists), continue with multipart.
	if stateExists {
		md := NewMultipartDownloader()
		md.SetHeaders(bd.bilibiliHeaders())
		totalSize := knownSize
		if totalSize <= 0 {
			if st, err := loadMultipartState(path); err == nil && st != nil {
				totalSize = st.TotalSize
			}
		}
		if totalSize <= 0 {
			checkResult := md.CheckRangeSupport(ctx, url)
			if checkResult.Error != nil {
				logger.Debug("Failed to check range support: %v, falling back to sequential", checkResult.Error)
				return bd.downloadFileSequential(ctx, url, path, 0, 0, onProgress)
			}
			totalSize = checkResult.ContentLength
		}
		return bd.downloadFileMultipart(ctx, url, path, totalSize, md, onProgress)
	}

	// If resuming, use sequential download
	if existingSize > 0 {
		logger.Debug("Resuming download from %d bytes, using sequential download", existingSize)
		return bd.downloadFileSequential(ctx, url, path, knownSize, existingSize, onProgress)
	}

	// For new downloads, check if multipart download is beneficial
	md := NewMultipartDownloader()
	md.SetHeaders(bd.bilibiliHeaders())

	// Determine file size - use knownSize if provided, otherwise check
	totalSize := knownSize
	supportsRange := true

	if totalSize <= 0 {
		checkResult := md.CheckRangeSupport(ctx, url)
		if checkResult.Error != nil {
			logger.Debug("Failed to check range support: %v, falling back to sequential", checkResult.Error)
			return bd.downloadFileSequential(ctx, url, path, 0, 0, onProgress)
		}
		totalSize = checkResult.ContentLength
		supportsRange = checkResult.SupportsRange
	}

	// Decide whether to use multipart download
	if ShouldUseMultipart(supportsRange, totalSize) {
		logger.Info("Using multipart download for Bilibili (size: %d bytes)", totalSize)
		return bd.downloadFileMultipart(ctx, url, path, totalSize, md, onProgress)
	}

	// Fall back to sequential download
	logger.Debug("Using sequential download (size: %d, supports range: %v)", totalSize, supportsRange)
	return bd.downloadFileSequential(ctx, url, path, totalSize, 0, onProgress)
}

// bilibiliHeaders returns HTTP headers required for Bilibili API requests.
// Includes Referer, Origin, and SESSDATA cookie (if authenticated).
func (bd *BilibiliDownloader) bilibiliHeaders() map[string]string {
	headers := map[string]string{
		"Referer":         "https://www.bilibili.com/",
		"Origin":          "https://www.bilibili.com",
		"Accept":          "application/json, text/plain, */*",
		"Accept-Language": "zh-CN,zh;q=0.9,en;q=0.8",
	}
	if bd.sessData != "" {
		headers["Cookie"] = fmt.Sprintf("SESSDATA=%s", bd.sessData)
	}
	return headers
}

// downloadFileMultipart performs concurrent multipart download for faster speeds.
// Splits the file into chunks and downloads them in parallel using multiple connections.
func (bd *BilibiliDownloader) downloadFileMultipart(ctx context.Context, url, path string, totalSize int64, md *MultipartDownloader, onProgress func(float64)) (string, error) {
	result := md.Download(ctx, url, path, totalSize, func(downloaded, total int64) {
		if onProgress != nil && total > 0 {
			progress := float64(downloaded) / float64(total) * 100
			onProgress(progress)
		}
	})

	if result.Error != nil {
		return "", result.Error
	}

	return path, nil
}

// downloadFileSequential performs traditional sequential download.
// Supports resume through HTTP Range requests if a partial file exists.
// Used as fallback when multipart download is not suitable or supported.
func (bd *BilibiliDownloader) downloadFileSequential(ctx context.Context, url, path string, knownSize int64, existingSize int64, onProgress func(float64)) (string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return "", err
	}

	bd.setHeaders(req)
	req.Header.Set("Referer", "https://www.bilibili.com/")

	// Set Range header for resume
	if existingSize > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", existingSize))
		logger.Info("Resuming download from %d bytes: %s", existingSize, path)
	}

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	// Check response status
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		return "", fmt.Errorf("unexpected status: %d", resp.StatusCode)
	}

	// Determine total size
	totalSize := knownSize
	if totalSize <= 0 {
		if resp.StatusCode == http.StatusPartialContent {
			// Parse Content-Range header
			totalSize = existingSize + resp.ContentLength
		} else {
			totalSize = resp.ContentLength
		}
	}

	// Open file for append or create
	os.MkdirAll(filepath.Dir(path), 0755)
	var file *os.File
	if existingSize > 0 && resp.StatusCode == http.StatusPartialContent {
		file, err = os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0644)
	} else {
		// Server doesn't support range, start from beginning
		existingSize = 0
		file, err = os.Create(path)
	}
	if err != nil {
		return "", err
	}
	defer file.Close()

	// Download with progress
	downloaded := existingSize
	buf := make([]byte, 32*1024)

	for {
		// Check for cancellation
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		default:
		}

		n, err := resp.Body.Read(buf)
		if n > 0 {
			file.Write(buf[:n])
			downloaded += int64(n)

			if totalSize > 0 && onProgress != nil {
				progress := float64(downloaded) / float64(totalSize) * 100
				onProgress(progress)
			}
		}

		if err != nil {
			if err == io.EOF {
				break
			}
			return "", err
		}
	}

	return path, nil
}

// DASHProgressTracker tracks combined download progress for DASH format videos.
// DASH videos have separate video and audio streams that are downloaded independently.
// This tracker combines both streams' progress into a unified percentage (0-100%).
//
// Progress allocation:
//   - 0-95%:   Download progress (video + audio combined by size ratio)
//   - 95-100%: FFmpeg merge operation
type DASHProgressTracker struct {
	videoSize       int64          // Expected video stream size in bytes
	audioSize       int64          // Expected audio stream size in bytes
	totalSize       int64          // Combined size (videoSize + audioSize)
	videoDownloaded int64          // Bytes downloaded for video stream
	audioDownloaded int64          // Bytes downloaded for audio stream
	onProgress      func(float64)  // Callback to report progress percentage
	lastProgress    float64        // Last reported progress (ensures monotonic increase)
}

// NewDASHProgressTracker creates a new progress tracker for DASH format downloads.
// The tracker combines video and audio download progress based on their relative sizes.
func NewDASHProgressTracker(videoSize, audioSize int64, onProgress func(float64)) *DASHProgressTracker {
	return &DASHProgressTracker{
		videoSize:  videoSize,
		audioSize:  audioSize,
		totalSize:  videoSize + audioSize,
		onProgress: onProgress,
	}
}

// UpdateVideoProgress updates the video stream download progress.
// The progress parameter is a percentage (0-100) of the video stream completion.
func (t *DASHProgressTracker) UpdateVideoProgress(progress float64) {
	if t.videoSize > 0 {
		t.videoDownloaded = int64(progress / 100 * float64(t.videoSize))
	}
	t.reportProgress()
}

// UpdateAudioProgress updates the audio stream download progress.
// The progress parameter is a percentage (0-100) of the audio stream completion.
func (t *DASHProgressTracker) UpdateAudioProgress(progress float64) {
	if t.audioSize > 0 {
		t.audioDownloaded = int64(progress / 100 * float64(t.audioSize))
	}
	t.reportProgress()
}

// reportProgress calculates and reports the combined download progress.
// Progress is weighted by stream sizes and reserves 5% for the merge phase.
// Ensures progress only increases (never decreases) for smooth UI display.
func (t *DASHProgressTracker) reportProgress() {
	if t.onProgress == nil || t.totalSize == 0 {
		return
	}
	// Reserve 5% for merge operation
	downloadProgress := float64(t.videoDownloaded+t.audioDownloaded) / float64(t.totalSize) * 95
	// Ensure progress is monotonically increasing
	if downloadProgress > t.lastProgress {
		t.lastProgress = downloadProgress
		t.onProgress(downloadProgress)
	}
}

// SetMergeProgress sets progress during the FFmpeg merge phase.
// The progress parameter is 0-100% of the merge operation.
// This maps to 95-100% of the overall download progress.
func (t *DASHProgressTracker) SetMergeProgress(progress float64) {
	if t.onProgress == nil {
		return
	}
	mergeProgress := 95 + progress*0.05
	if mergeProgress > t.lastProgress {
		t.lastProgress = mergeProgress
		t.onProgress(mergeProgress)
	}
}

// setHeaders sets common HTTP headers required for Bilibili API requests.
// This includes User-Agent, Accept headers, and SESSDATA cookie if authenticated.
// The headers mimic a modern browser to avoid being blocked by Bilibili's anti-bot measures.
func (bd *BilibiliDownloader) setHeaders(req *http.Request) {
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
	req.Header.Set("Origin", "https://www.bilibili.com")
	req.Header.Set("Referer", "https://www.bilibili.com/")

	// Thread-safe access to sessData
	bd.sessDataMu.RLock()
	sessData := bd.sessData
	bd.sessDataMu.RUnlock()

	if sessData != "" {
		req.Header.Set("Cookie", fmt.Sprintf("SESSDATA=%s", sessData))
	}
}

// IsBilibiliURL checks if a URL is a Bilibili video URL.
// Returns true for URLs containing "bilibili.com" or "b23.tv" (short link domain).
func IsBilibiliURL(url string) bool {
	return strings.Contains(url, "bilibili.com") || strings.Contains(url, "b23.tv")
}
