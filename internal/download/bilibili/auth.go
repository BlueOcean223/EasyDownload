package bilibili

import (
	"EasyDownload/internal/infra/logger"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

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
	resp, err := bd.do(req)
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
	resp, err := bd.do(req)
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
		var sessData string
		// Login successful, extract SESSDATA from cookies in response header
		for _, cookie := range resp.Cookies() {
			if cookie.Name == "SESSDATA" {
				sessData = cookie.Value
				break
			}
		}

		// If SESSDATA not in cookies, try to parse from URL
		if sessData == "" && result.Data.URL != "" {
			// URL format: https://passport.bilibili.com/...?SESSDATA=xxx&...
			if strings.Contains(result.Data.URL, "SESSDATA=") {
				parts := strings.Split(result.Data.URL, "SESSDATA=")
				if len(parts) > 1 {
					sessData = strings.Split(parts[1], "&")[0]
				}
			}
		}

		// Auto-save SESSDATA if obtained
		if sessData != "" {
			bd.SaveSessData(sessData)
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
	if bd.GetSessData() == "" {
		return &BilibiliUserInfo{IsLogin: false}, nil
	}

	apiURL := "https://api.bilibili.com/x/web-interface/nav"

	req, err := http.NewRequest("GET", apiURL, nil)
	if err != nil {
		return nil, err
	}

	bd.setHeaders(req)
	resp, err := bd.do(req)
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
