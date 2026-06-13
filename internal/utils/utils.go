package utils

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// GetAppDataDir returns the application data directory
func GetAppDataDir() string {
	if runtime.GOOS == "windows" {
		appData := os.Getenv("APPDATA")
		if appData != "" {
			return filepath.Join(appData, "EasyDownload")
		}
	}

	homeDir, _ := os.UserHomeDir()
	return filepath.Join(homeDir, ".easydownload")
}

// GetDownloadDir returns the default download directory
func GetDownloadDir() string {
	homeDir, _ := os.UserHomeDir()
	return filepath.Join(homeDir, "Downloads", "EasyDownload")
}

// EnsureDir ensures a directory exists
func EnsureDir(dir string) error {
	return os.MkdirAll(dir, 0755)
}

// FileExists checks if a file exists
func FileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// OpenFolder opens a folder in the system file manager
func OpenFolder(path string) error {
	var cmd *exec.Cmd

	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("explorer", path)
	case "darwin":
		cmd = exec.Command("open", path)
	default:
		cmd = exec.Command("xdg-open", path)
	}

	return cmd.Start()
}

// OpenFile opens a file with the default application
func OpenFile(path string) error {
	var cmd *exec.Cmd

	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", "", path)
	case "darwin":
		cmd = exec.Command("open", path)
	default:
		cmd = exec.Command("xdg-open", path)
	}

	return cmd.Start()
}

// FormatBytes formats bytes to human readable string
func FormatBytes(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return formatInt(bytes) + " B"
	}

	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}

	return formatFloat(float64(bytes)/float64(div)) + " " + string("KMGTPE"[exp]) + "B"
}

// FormatDuration formats seconds to human readable duration
func FormatDuration(seconds int) string {
	if seconds < 60 {
		return formatInt(int64(seconds)) + "s"
	}

	minutes := seconds / 60
	secs := seconds % 60

	if minutes < 60 {
		return formatInt(int64(minutes)) + ":" + padZero(secs)
	}

	hours := minutes / 60
	mins := minutes % 60

	return formatInt(int64(hours)) + ":" + padZero(mins) + ":" + padZero(secs)
}

func formatInt(n int64) string {
	s := ""
	if n == 0 {
		return "0"
	}
	for n > 0 {
		s = string(rune('0'+n%10)) + s
		n /= 10
	}
	return s
}

func formatFloat(f float64) string {
	intPart := int64(f)
	decPart := int64((f - float64(intPart)) * 100)
	return formatInt(intPart) + "." + padZero(int(decPart))
}

func padZero(n int) string {
	if n < 10 {
		return "0" + formatInt(int64(n))
	}
	return formatInt(int64(n))
}

// GetInjectScript returns the JavaScript code to inject into WeChat pages.
// If apiToken is provided, requests include the internal API auth header.
func GetInjectScript(apiPort int, apiToken ...string) string {
	token := ""
	if len(apiToken) > 0 {
		token = apiToken[0]
	}
	return `
(function() {
	'use strict';
	
	const API_URL = 'http://127.0.0.1:` + formatInt(int64(apiPort)) + `/api/detect';
	const API_TOKEN = '` + token + `';
	const sentVideos = new Set();
	
	// Send video info to desktop app
	function sendVideoInfo(info) {
		if (sentVideos.has(info.url)) return;
		sentVideos.add(info.url);
		
		const headers = { 'Content-Type': 'application/json' };
		if (API_TOKEN) headers['X-EasyDownload-Token'] = API_TOKEN;
		fetch(API_URL, {
			method: 'POST',
			headers: headers,
			body: JSON.stringify(info)
		}).catch(console.error);
	}
	
	// Extract video info from various sources
	function extractVideoInfo() {
		// Try to find video elements
		const videos = document.querySelectorAll('video');
		videos.forEach(video => {
			if (video.src && video.src.includes('video')) {
				const info = {
					id: Date.now().toString(),
					url: video.src,
					title: document.title || 'Unknown Video',
					cover: video.poster || '',
					source: 'wechat',
					timestamp: Date.now()
				};
				sendVideoInfo(info);
			}
		});
		
		// Try to intercept XHR responses
	}
	
	// Intercept XMLHttpRequest
	const originalXHROpen = XMLHttpRequest.prototype.open;
	const originalXHRSend = XMLHttpRequest.prototype.send;
	
	XMLHttpRequest.prototype.open = function(method, url) {
		this._url = url;
		return originalXHROpen.apply(this, arguments);
	};
	
	XMLHttpRequest.prototype.send = function() {
		this.addEventListener('load', function() {
			try {
				if (this._url && (this._url.includes('finder') || this._url.includes('channels'))) {
					const data = JSON.parse(this.responseText);
					processAPIResponse(data);
				}
			} catch (e) {}
		});
		return originalXHRSend.apply(this, arguments);
	};
	
	// Intercept fetch
	const originalFetch = window.fetch;
	window.fetch = function(url, options) {
		return originalFetch.apply(this, arguments).then(response => {
			const clonedResponse = response.clone();
			
			if (typeof url === 'string' && (url.includes('finder') || url.includes('channels'))) {
				clonedResponse.json().then(data => {
					processAPIResponse(data);
				}).catch(() => {});
			}
			
			return response;
		});
	};
	
	// Process API responses to extract video info
	function processAPIResponse(data) {
		if (!data) return;
		
		const str = JSON.stringify(data);
		
		// Look for video URLs
		const urlPatterns = [
			/"url"\s*:\s*"(https?:\/\/[^"]*video[^"]*\.mp4[^"]*)"/g,
			/"url"\s*:\s*"(https?:\/\/[^"]*finder\.video\.qq\.com[^"]*)"/g,
			/"videoUrl"\s*:\s*"([^"]*)"/g
		];
		
		urlPatterns.forEach(pattern => {
			let match;
			while ((match = pattern.exec(str)) !== null) {
				const videoUrl = match[1];
				if (videoUrl && !sentVideos.has(videoUrl)) {
					// Try to extract more info
					let title = '';
					let cover = '';
					let author = '';
					
					const titleMatch = str.match(/"title"\s*:\s*"([^"]*)"/);
					if (titleMatch) title = titleMatch[1];
					
					const coverMatch = str.match(/"thumbUrl"\s*:\s*"([^"]*)"/) || str.match(/"coverUrl"\s*:\s*"([^"]*)"/);
					if (coverMatch) cover = coverMatch[1];
					
					const authorMatch = str.match(/"nickname"\s*:\s*"([^"]*)"/);
					if (authorMatch) author = authorMatch[1];
					
					sendVideoInfo({
						id: Date.now().toString() + Math.random().toString(36).substr(2, 9),
						url: videoUrl,
						title: title || document.title || 'WeChat Video',
						cover: cover,
						author: author,
						source: 'wechat',
						timestamp: Date.now()
					});
				}
			}
		});
	}
	
	// Observer for dynamic content
	const observer = new MutationObserver(() => {
		extractVideoInfo();
	});
	
	observer.observe(document.body, { childList: true, subtree: true });
	
	// Initial extraction
	setTimeout(extractVideoInfo, 1000);
	
	console.log('[EasyDownload] Video sniffer initialized');
})();
`
}

// Version represents a semantic version
type Version struct {
	Major int
	Minor int
	Patch int
}

// ParseVersion parses a semantic version string (e.g., "1.2.3" or "v1.2.3")
// Returns a Version struct and an error if parsing fails
func ParseVersion(versionStr string) (Version, error) {
	v := Version{}

	// Remove leading 'v' or 'V' if present
	versionStr = strings.TrimPrefix(versionStr, "v")
	versionStr = strings.TrimPrefix(versionStr, "V")

	// Split by '.'
	parts := strings.Split(versionStr, ".")
	if len(parts) < 1 || len(parts) > 3 {
		return v, &VersionParseError{Input: versionStr, Message: "invalid version format"}
	}

	// Parse major version
	major, err := strconv.Atoi(parts[0])
	if err != nil || major < 0 {
		return v, &VersionParseError{Input: versionStr, Message: "invalid major version"}
	}
	v.Major = major

	// Parse minor version (default to 0 if not present)
	if len(parts) >= 2 {
		minor, err := strconv.Atoi(parts[1])
		if err != nil || minor < 0 {
			return v, &VersionParseError{Input: versionStr, Message: "invalid minor version"}
		}
		v.Minor = minor
	}

	// Parse patch version (default to 0 if not present)
	if len(parts) >= 3 {
		patch, err := strconv.Atoi(parts[2])
		if err != nil || patch < 0 {
			return v, &VersionParseError{Input: versionStr, Message: "invalid patch version"}
		}
		v.Patch = patch
	}

	return v, nil
}

// VersionParseError represents an error during version parsing
type VersionParseError struct {
	Input   string
	Message string
}

func (e *VersionParseError) Error() string {
	return "version parse error: " + e.Message + " (input: " + e.Input + ")"
}

// CompareVersions compares two semantic version strings
// Returns:
//
//	-1 if v1 < v2
//	 0 if v1 == v2
//	 1 if v1 > v2
//
// Returns an error if either version string is invalid
func CompareVersions(v1Str, v2Str string) (int, error) {
	v1, err := ParseVersion(v1Str)
	if err != nil {
		return 0, err
	}

	v2, err := ParseVersion(v2Str)
	if err != nil {
		return 0, err
	}

	return v1.Compare(v2), nil
}

// Compare compares this version with another version
// Returns:
//
//	-1 if v < other
//	 0 if v == other
//	 1 if v > other
func (v Version) Compare(other Version) int {
	// Compare major version
	if v.Major < other.Major {
		return -1
	}
	if v.Major > other.Major {
		return 1
	}

	// Compare minor version
	if v.Minor < other.Minor {
		return -1
	}
	if v.Minor > other.Minor {
		return 1
	}

	// Compare patch version
	if v.Patch < other.Patch {
		return -1
	}
	if v.Patch > other.Patch {
		return 1
	}

	return 0
}

// String returns the string representation of the version
func (v Version) String() string {
	return formatInt(int64(v.Major)) + "." + formatInt(int64(v.Minor)) + "." + formatInt(int64(v.Patch))
}

// IsNewerThan returns true if v1 is newer than v2
func IsNewerThan(v1Str, v2Str string) (bool, error) {
	result, err := CompareVersions(v1Str, v2Str)
	if err != nil {
		return false, err
	}
	return result > 0, nil
}

// IsOlderThan returns true if v1 is older than v2
func IsOlderThan(v1Str, v2Str string) (bool, error) {
	result, err := CompareVersions(v1Str, v2Str)
	if err != nil {
		return false, err
	}
	return result < 0, nil
}

// IsEqual returns true if v1 equals v2
func IsEqual(v1Str, v2Str string) (bool, error) {
	result, err := CompareVersions(v1Str, v2Str)
	if err != nil {
		return false, err
	}
	return result == 0, nil
}
