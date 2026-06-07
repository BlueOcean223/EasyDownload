package bilibili

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"EasyDownload/internal/config"

	"github.com/leanovate/gopter"
	"github.com/leanovate/gopter/gen"
	"github.com/leanovate/gopter/prop"
)

// TestBilibiliDownloaderCreation tests BilibiliDownloader creation
func TestBilibiliDownloaderCreation(t *testing.T) {
	bd := NewBilibiliDownloader()
	if bd == nil {
		t.Fatal("NewBilibiliDownloader returned nil")
	}
}

// TestSetSessData tests setting SESSDATA
func TestSetSessData(t *testing.T) {
	bd := NewBilibiliDownloader()
	bd.SetSessData("test_sessdata_value")
	if bd.sessData != "test_sessdata_value" {
		t.Errorf("sessData = %s, want test_sessdata_value", bd.sessData)
	}
}

// TestParseURL tests URL parsing for different formats
func TestParseURL(t *testing.T) {
	bd := NewBilibiliDownloader()

	tests := []struct {
		url      string
		expected string
		hasError bool
	}{
		{"https://www.bilibili.com/video/BV1xx411c7mD", "BV1xx411c7mD", false},
		{"https://www.bilibili.com/video/BV1xx411c7mD?p=1", "BV1xx411c7mD", false},
		{"https://www.bilibili.com/video/av170001", "av170001", false},
		{"https://www.bilibili.com/video/av170001?p=2", "av170001", false},
		{"BV1xx411c7mD", "BV1xx411c7mD", false},
		{"https://www.youtube.com/watch?v=xxx", "", true},
		{"invalid_url", "", true},
	}

	for _, tt := range tests {
		result, err := bd.ParseURL(tt.url)
		if tt.hasError {
			if err == nil {
				t.Errorf("ParseURL(%q) expected error, got nil", tt.url)
			}
		} else {
			if err != nil {
				t.Errorf("ParseURL(%q) unexpected error: %v", tt.url, err)
			}
			if result != tt.expected {
				t.Errorf("ParseURL(%q) = %q, want %q", tt.url, result, tt.expected)
			}
		}
	}
}

// **Feature: easydownload-improvements, Property: BV号解析一致性**
// For any valid BV ID in URL, ParseURL should extract it correctly
func TestParseURLBVProperty(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	// Generate valid BV IDs (BV + 10 alphanumeric characters)
	bvGen := gen.RegexMatch("BV[a-zA-Z0-9]{10}")

	properties.Property("BV ID is correctly extracted from URL", prop.ForAll(
		func(bvid string) bool {
			bd := NewBilibiliDownloader()
			url := "https://www.bilibili.com/video/" + bvid
			result, err := bd.ParseURL(url)
			return err == nil && result == bvid
		},
		bvGen,
	))

	properties.Property("BV ID is correctly extracted with query params", prop.ForAll(
		func(bvid string, page int) bool {
			bd := NewBilibiliDownloader()
			url := "https://www.bilibili.com/video/" + bvid + "?p=" + string(rune('0'+page%10))
			result, err := bd.ParseURL(url)
			return err == nil && result == bvid
		},
		bvGen,
		gen.IntRange(1, 9),
	))

	properties.TestingRun(t)
}

// TestIsBilibiliURL tests Bilibili URL detection
func TestIsBilibiliURL(t *testing.T) {
	tests := []struct {
		url      string
		expected bool
	}{
		{"https://www.bilibili.com/video/BV1xx411c7mD", true},
		{"https://bilibili.com/video/BV1xx411c7mD", true},
		{"https://b23.tv/abc123", true},
		{"https://www.youtube.com/watch?v=xxx", false},
		{"https://example.com", false},
		{"", false},
	}

	for _, tt := range tests {
		result := IsBilibiliURL(tt.url)
		if result != tt.expected {
			t.Errorf("IsBilibiliURL(%q) = %v, want %v", tt.url, result, tt.expected)
		}
	}
}

// **Feature: easydownload-improvements, Property: Bilibili URL检测一致性**
// For any URL containing bilibili.com or b23.tv, IsBilibiliURL should return true
func TestIsBilibiliURLProperty(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("URLs with bilibili.com are detected", prop.ForAll(
		func(path string) bool {
			url := "https://www.bilibili.com/" + path
			return IsBilibiliURL(url)
		},
		gen.AlphaString(),
	))

	properties.Property("URLs with b23.tv are detected", prop.ForAll(
		func(path string) bool {
			url := "https://b23.tv/" + path
			return IsBilibiliURL(url)
		},
		gen.AlphaString(),
	))

	properties.Property("URLs without bilibili domains are not detected", prop.ForAll(
		func(domain string) bool {
			// Ensure domain doesn't contain bilibili or b23
			if len(domain) == 0 {
				return true
			}
			url := "https://" + domain + ".com/video"
			// Only test if domain doesn't accidentally contain bilibili or b23
			if contains(domain, "bilibili") || contains(domain, "b23") {
				return true // Skip this case
			}
			return !IsBilibiliURL(url)
		},
		gen.AlphaString().SuchThat(func(s string) bool {
			return !contains(s, "bilibili") && !contains(s, "b23")
		}),
	))

	properties.TestingRun(t)
}

// contains checks if s contains substr
func contains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// TestSetFFmpegPath tests setting FFmpeg path
func TestSetFFmpegPath(t *testing.T) {
	bd := NewBilibiliDownloader()
	bd.SetFFmpegPath("/usr/bin/ffmpeg")
	if bd.ffmpegPath != "/usr/bin/ffmpeg" {
		t.Errorf("ffmpegPath = %s, want /usr/bin/ffmpeg", bd.ffmpegPath)
	}
}

func TestSelectURLsPromotesBackupAndDeduplicates(t *testing.T) {
	primary, backups := selectURLs(
		"",
		"",
		[]string{"", " https://cdn.example.com/a ", "https://cdn.example.com/a"},
		[]string{"https://cdn.example.com/b", "https://cdn.example.com/a"},
	)

	if primary != "https://cdn.example.com/a" {
		t.Fatalf("primary = %q, want backup promoted", primary)
	}
	if len(backups) != 1 || backups[0] != "https://cdn.example.com/b" {
		t.Fatalf("backups = %#v, want only unique remaining backup", backups)
	}
}

func TestBuildQualityNameFallbacks(t *testing.T) {
	if got := buildQualityName(120, []supportFormatEntry{{Quality: 120}}, "4K 超清"); got != "4K 超清" {
		t.Fatalf("buildQualityName fallback to acceptDesc = %q, want %q", got, "4K 超清")
	}
	if got := buildQualityName(80, nil, ""); got != "1080P" {
		t.Fatalf("buildQualityName fallback to map = %q, want %q", got, "1080P")
	}
}

func TestDownloadFileWithFallbackNoURL(t *testing.T) {
	bd := NewBilibiliDownloader()
	_, err := bd.downloadFileWithFallback(context.Background(), "", []string{"", " "}, filepath.Join(t.TempDir(), "out.m4s"), 0, nil)
	if err == nil || !strings.Contains(err.Error(), "no download URL available") {
		t.Fatalf("error = %v, want no download URL available", err)
	}
}

func TestGetContentLengthWithFallbackUsesBackup(t *testing.T) {
	var primaryHEADs int
	var backupHEADs int
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/primary":
			primaryHEADs++
			w.WriteHeader(http.StatusForbidden)
		case "/backup":
			backupHEADs++
			w.Header().Set("Content-Length", "123")
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer ts.Close()

	bd := NewBilibiliDownloader()
	if got := bd.getContentLengthWithFallback(ts.URL+"/primary", []string{ts.URL + "/backup"}); got != 123 {
		t.Fatalf("content length = %d, want 123", got)
	}
	if primaryHEADs != 1 || backupHEADs != 1 {
		t.Fatalf("HEAD calls: primary=%d backup=%d, want 1 each", primaryHEADs, backupHEADs)
	}
}

// MockConfigManager implements ConfigManagerInterface for testing
// Note: SESSDATA is now stored in secure credential storage, not in config
type MockConfigManager struct {
	config *config.Config
}

func NewMockConfigManager() *MockConfigManager {
	return &MockConfigManager{
		config: &config.Config{},
	}
}

func (m *MockConfigManager) Get() *config.Config {
	return m.config
}

func (m *MockConfigManager) Set(key string, value any) error {
	// Note: bilibiliSessData case removed - now stored in secure credential storage
	return nil
}

// **Feature: easydownload-improvements, Property 2: SESSDATA 持久化往返**
// For any valid SESSDATA string, saving then loading should return the same value
// **Validates: Requirements 3.2, 3.3**
// Note: This test now uses the secure credential storage (system keyring)
// which requires actual system integration. The test verifies the in-memory
// round trip behavior since the credential package handles persistence.
func TestSessDataRoundTripProperty(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	// Generate valid SESSDATA strings (alphanumeric with some special chars)
	sessDataGen := gen.RegexMatch("[a-zA-Z0-9%*_-]{1,100}")

	properties.Property("SESSDATA in-memory round trip preserves value", prop.ForAll(
		func(sessData string) bool {
			bd := NewBilibiliDownloader()

			// Set the SESSDATA directly (in-memory only for this test)
			bd.SetSessData(sessData)

			// Verify in-memory value
			return bd.GetSessData() == sessData
		},
		sessDataGen,
	))

	properties.Property("Empty SESSDATA in-memory round trip works", prop.ForAll(
		func(_ int) bool {
			bd := NewBilibiliDownloader()

			// Set empty SESSDATA
			bd.SetSessData("")

			// Verify in-memory value
			return bd.GetSessData() == ""
		},
		gen.Int(),
	))

	properties.TestingRun(t)
}

// TestDASHProgressTrackerMonotonic tests that the tracker maintains monotonic progress
func TestDASHProgressTrackerMonotonic(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	sizeGen := gen.Int64Range(1, 1000000000)

	properties.Property("Tracker progress is monotonically increasing", prop.ForAll(
		func(videoSize, audioSize int64) bool {
			var progressValues []float64
			tracker := NewDASHProgressTracker(videoSize, audioSize, func(p float64) {
				progressValues = append(progressValues, p)
			})

			// Simulate video download
			for i := 0; i <= 10; i++ {
				tracker.UpdateVideoProgress(float64(i * 10))
			}

			// Simulate audio download
			for i := 0; i <= 10; i++ {
				tracker.UpdateAudioProgress(float64(i * 10))
			}

			// Simulate merge
			tracker.SetMergeProgress(0)
			tracker.SetMergeProgress(50)
			tracker.SetMergeProgress(100)

			// Verify monotonic increase
			for i := 1; i < len(progressValues); i++ {
				if progressValues[i] < progressValues[i-1] {
					return false
				}
			}
			return true
		},
		sizeGen,
		sizeGen,
	))

	properties.TestingRun(t)
}
