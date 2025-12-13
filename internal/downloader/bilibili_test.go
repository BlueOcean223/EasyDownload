package downloader

import (
	"EasyDownload/internal/config"
	"testing"

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

// MockConfigManager implements ConfigManagerInterface for testing
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
	if key == "bilibiliSessData" {
		if v, ok := value.(string); ok {
			m.config.BilibiliSessData = v
		}
	}
	return nil
}

// **Feature: easydownload-improvements, Property 2: SESSDATA 持久化往返**
// For any valid SESSDATA string, saving then loading should return the same value
// **Validates: Requirements 3.2, 3.3**
func TestSessDataRoundTripProperty(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	// Generate valid SESSDATA strings (alphanumeric with some special chars)
	sessDataGen := gen.RegexMatch("[a-zA-Z0-9%*_-]{1,100}")

	properties.Property("SESSDATA round trip preserves value", prop.ForAll(
		func(sessData string) bool {
			bd := NewBilibiliDownloader()
			mockCM := NewMockConfigManager()
			bd.SetConfigManager(mockCM)

			// Save the SESSDATA
			err := bd.SaveSessData(sessData)
			if err != nil {
				return false
			}

			// Create a new downloader with the same config manager to simulate app restart
			bd2 := NewBilibiliDownloader()
			bd2.SetConfigManager(mockCM)

			// Load the SESSDATA
			loaded, err := bd2.LoadSessData()
			if err != nil {
				return false
			}

			// Verify round trip
			return loaded == sessData
		},
		sessDataGen,
	))

	properties.Property("Empty SESSDATA round trip works", prop.ForAll(
		func(_ int) bool {
			bd := NewBilibiliDownloader()
			mockCM := NewMockConfigManager()
			bd.SetConfigManager(mockCM)

			// Save empty SESSDATA
			err := bd.SaveSessData("")
			if err != nil {
				return false
			}

			// Load should return empty
			loaded, err := bd.LoadSessData()
			if err != nil {
				return false
			}

			return loaded == ""
		},
		gen.Int(),
	))

	properties.TestingRun(t)
}

// **Feature: easydownload-improvements, Property 3: 下载进度单调递增**
// Progress should be monotonically increasing and between 0-100
// **Validates: Requirements 3.4**
func TestDownloadProgressMonotonicProperty(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	// Generate valid file sizes (positive integers)
	sizeGen := gen.Int64Range(1, 1000000000) // 1 byte to 1GB

	properties.Property("Progress is between 0 and 100", prop.ForAll(
		func(videoSize, audioSize, videoDownloaded, audioDownloaded int64) bool {
			// Ensure downloaded doesn't exceed size
			if videoDownloaded > videoSize {
				videoDownloaded = videoSize
			}
			if audioDownloaded > audioSize {
				audioDownloaded = audioSize
			}

			progress := CalculateProgress(videoDownloaded, audioDownloaded, videoSize, audioSize)
			return progress >= 0 && progress <= 100
		},
		sizeGen,
		sizeGen,
		sizeGen,
		sizeGen,
	))

	properties.Property("Progress increases as download progresses", prop.ForAll(
		func(videoSize, audioSize int64) bool {
			// Simulate download progress
			var lastProgress float64 = -1

			// Test multiple progress points
			for i := 0; i <= 10; i++ {
				videoDownloaded := videoSize * int64(i) / 10
				audioDownloaded := audioSize * int64(i) / 10

				progress := CalculateProgress(videoDownloaded, audioDownloaded, videoSize, audioSize)

				// Progress should be monotonically increasing
				if progress < lastProgress {
					return false
				}
				lastProgress = progress
			}
			return true
		},
		sizeGen,
		sizeGen,
	))

	properties.Property("Complete download reaches 95% (before merge)", prop.ForAll(
		func(videoSize, audioSize int64) bool {
			progress := CalculateProgress(videoSize, audioSize, videoSize, audioSize)
			// Should be exactly 95% when download is complete (5% reserved for merge)
			return progress == 95
		},
		sizeGen,
		sizeGen,
	))

	properties.Property("Zero download is 0%", prop.ForAll(
		func(videoSize, audioSize int64) bool {
			progress := CalculateProgress(0, 0, videoSize, audioSize)
			return progress == 0
		},
		sizeGen,
		sizeGen,
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
