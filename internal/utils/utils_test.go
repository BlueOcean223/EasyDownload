package utils

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/leanovate/gopter"
	"github.com/leanovate/gopter/gen"
	"github.com/leanovate/gopter/prop"
)

// TestGetAppDataDir tests app data directory retrieval
func TestGetAppDataDir(t *testing.T) {
	dir := GetAppDataDir()
	if dir == "" {
		t.Error("GetAppDataDir returned empty string")
	}

	// Should end with EasyDownload or .easydownload
	if runtime.GOOS == "windows" {
		if !contains(dir, "EasyDownload") {
			t.Errorf("GetAppDataDir = %s, expected to contain EasyDownload", dir)
		}
	} else {
		if !contains(dir, ".easydownload") {
			t.Errorf("GetAppDataDir = %s, expected to contain .easydownload", dir)
		}
	}
}

// TestGetDownloadDir tests download directory retrieval
func TestGetDownloadDir(t *testing.T) {
	dir := GetDownloadDir()
	if dir == "" {
		t.Error("GetDownloadDir returned empty string")
	}

	// Should contain Downloads and EasyDownload
	if !contains(dir, "Downloads") || !contains(dir, "EasyDownload") {
		t.Errorf("GetDownloadDir = %s, expected to contain Downloads/EasyDownload", dir)
	}
}

// TestEnsureDir tests directory creation
func TestEnsureDir(t *testing.T) {
	testDir := filepath.Join(os.TempDir(), "easydownload_test_ensure")
	defer os.RemoveAll(testDir)

	err := EnsureDir(testDir)
	if err != nil {
		t.Fatalf("EnsureDir failed: %v", err)
	}

	if _, err := os.Stat(testDir); os.IsNotExist(err) {
		t.Error("EnsureDir did not create directory")
	}

	// Should not fail if directory already exists
	err = EnsureDir(testDir)
	if err != nil {
		t.Errorf("EnsureDir failed on existing directory: %v", err)
	}
}

// TestFileExists tests file existence check
func TestFileExists(t *testing.T) {
	// Create a temp file
	tmpFile, err := os.CreateTemp("", "easydownload_test_*")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	tmpFile.Close()
	defer os.Remove(tmpFile.Name())

	if !FileExists(tmpFile.Name()) {
		t.Error("FileExists returned false for existing file")
	}

	if FileExists("/non/existent/path/file.txt") {
		t.Error("FileExists returned true for non-existent file")
	}
}

// TestFormatBytes tests byte formatting
func TestFormatBytes(t *testing.T) {
	tests := []struct {
		bytes    int64
		expected string
	}{
		{0, "0 B"},
		{100, "100 B"},
		{1023, "1023 B"},
		{1024, "1.00 KB"},
		{1536, "1.50 KB"},
		{1048576, "1.00 MB"},
		{1073741824, "1.00 GB"},
	}

	for _, tt := range tests {
		result := FormatBytes(tt.bytes)
		if result != tt.expected {
			t.Errorf("FormatBytes(%d) = %s, want %s", tt.bytes, result, tt.expected)
		}
	}
}

// **Feature: easydownload-improvements, Property: 字节格式化一致性**
// For any non-negative byte value, FormatBytes should produce a valid formatted string
func TestFormatBytesProperty(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("FormatBytes produces non-empty string for non-negative values", prop.ForAll(
		func(bytes int64) bool {
			if bytes < 0 {
				return true // Skip negative values
			}
			result := FormatBytes(bytes)
			return len(result) > 0
		},
		gen.Int64Range(0, 1<<50), // Up to 1 PB
	))

	properties.Property("FormatBytes result contains unit suffix", prop.ForAll(
		func(bytes int64) bool {
			if bytes < 0 {
				return true
			}
			result := FormatBytes(bytes)
			// Should end with B, KB, MB, GB, TB, PB, or EB
			validSuffixes := []string{" B", "KB", "MB", "GB", "TB", "PB", "EB"}
			for _, suffix := range validSuffixes {
				if len(result) >= len(suffix) && result[len(result)-len(suffix):] == suffix {
					return true
				}
			}
			return false
		},
		gen.Int64Range(0, 1<<50),
	))

	properties.TestingRun(t)
}

// TestFormatDuration tests duration formatting
func TestFormatDuration(t *testing.T) {
	tests := []struct {
		seconds  int
		expected string
	}{
		{0, "0s"},
		{30, "30s"},
		{59, "59s"},
		{60, "1:00"},
		{90, "1:30"},
		{3599, "59:59"},
		{3600, "1:00:00"},
		{3661, "1:01:01"},
		{7322, "2:02:02"},
	}

	for _, tt := range tests {
		result := FormatDuration(tt.seconds)
		if result != tt.expected {
			t.Errorf("FormatDuration(%d) = %s, want %s", tt.seconds, result, tt.expected)
		}
	}
}

// **Feature: easydownload-improvements, Property: 时长格式化一致性**
// For any non-negative duration, FormatDuration should produce a valid formatted string
func TestFormatDurationProperty(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("FormatDuration produces non-empty string", prop.ForAll(
		func(seconds int) bool {
			if seconds < 0 {
				return true
			}
			result := FormatDuration(seconds)
			return len(result) > 0
		},
		gen.IntRange(0, 86400), // Up to 24 hours
	))

	properties.Property("FormatDuration for < 60s ends with 's'", prop.ForAll(
		func(seconds int) bool {
			result := FormatDuration(seconds)
			return result[len(result)-1] == 's'
		},
		gen.IntRange(0, 59),
	))

	properties.Property("FormatDuration for >= 60s contains ':'", prop.ForAll(
		func(seconds int) bool {
			result := FormatDuration(seconds)
			for _, c := range result {
				if c == ':' {
					return true
				}
			}
			return false
		},
		gen.IntRange(60, 86400),
	))

	properties.TestingRun(t)
}

// TestGetInjectScript tests inject script generation
func TestGetInjectScript(t *testing.T) {
	script := GetInjectScript(18899)
	if script == "" {
		t.Error("GetInjectScript returned empty string")
	}

	// Should contain the API port
	if !contains(script, "18899") {
		t.Error("GetInjectScript does not contain the API port")
	}

	// Should contain key function names
	if !contains(script, "sendVideoInfo") {
		t.Error("GetInjectScript does not contain sendVideoInfo function")
	}
	if !contains(script, "extractVideoInfo") {
		t.Error("GetInjectScript does not contain extractVideoInfo function")
	}
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

// TestParseVersion tests version parsing
func TestParseVersion(t *testing.T) {
	tests := []struct {
		input    string
		expected Version
		hasError bool
	}{
		{"1.0.0", Version{1, 0, 0}, false},
		{"v1.0.0", Version{1, 0, 0}, false},
		{"V1.0.0", Version{1, 0, 0}, false},
		{"2.3.4", Version{2, 3, 4}, false},
		{"10.20.30", Version{10, 20, 30}, false},
		{"1.0", Version{1, 0, 0}, false},
		{"1", Version{1, 0, 0}, false},
		{"0.0.0", Version{0, 0, 0}, false},
		{"", Version{}, true},
		{"invalid", Version{}, true},
		{"1.2.3.4", Version{}, true},
		{"-1.0.0", Version{}, true},
		{"1.-1.0", Version{}, true},
		{"1.0.-1", Version{}, true},
	}

	for _, tt := range tests {
		result, err := ParseVersion(tt.input)
		if tt.hasError {
			if err == nil {
				t.Errorf("ParseVersion(%s) expected error, got nil", tt.input)
			}
		} else {
			if err != nil {
				t.Errorf("ParseVersion(%s) unexpected error: %v", tt.input, err)
			}
			if result != tt.expected {
				t.Errorf("ParseVersion(%s) = %v, want %v", tt.input, result, tt.expected)
			}
		}
	}
}

// TestCompareVersions tests version comparison
func TestCompareVersions(t *testing.T) {
	tests := []struct {
		v1       string
		v2       string
		expected int
	}{
		{"1.0.0", "1.0.0", 0},
		{"1.0.0", "2.0.0", -1},
		{"2.0.0", "1.0.0", 1},
		{"1.1.0", "1.0.0", 1},
		{"1.0.0", "1.1.0", -1},
		{"1.0.1", "1.0.0", 1},
		{"1.0.0", "1.0.1", -1},
		{"v1.0.0", "1.0.0", 0},
		{"1.0.0", "v1.0.0", 0},
		{"10.0.0", "9.0.0", 1},
		{"1.10.0", "1.9.0", 1},
		{"1.0.10", "1.0.9", 1},
	}

	for _, tt := range tests {
		result, err := CompareVersions(tt.v1, tt.v2)
		if err != nil {
			t.Errorf("CompareVersions(%s, %s) unexpected error: %v", tt.v1, tt.v2, err)
		}
		if result != tt.expected {
			t.Errorf("CompareVersions(%s, %s) = %d, want %d", tt.v1, tt.v2, result, tt.expected)
		}
	}
}

// TestIsNewerThan tests the IsNewerThan function
func TestIsNewerThan(t *testing.T) {
	tests := []struct {
		v1       string
		v2       string
		expected bool
	}{
		{"2.0.0", "1.0.0", true},
		{"1.0.0", "2.0.0", false},
		{"1.0.0", "1.0.0", false},
		{"1.1.0", "1.0.0", true},
		{"1.0.1", "1.0.0", true},
	}

	for _, tt := range tests {
		result, err := IsNewerThan(tt.v1, tt.v2)
		if err != nil {
			t.Errorf("IsNewerThan(%s, %s) unexpected error: %v", tt.v1, tt.v2, err)
		}
		if result != tt.expected {
			t.Errorf("IsNewerThan(%s, %s) = %v, want %v", tt.v1, tt.v2, result, tt.expected)
		}
	}
}

// **Feature: easydownload-improvements, Property 12: 版本号比较正确性**
// **Validates: Requirements 9.5**
// For any two semantic version numbers, the comparison result should correctly reflect the version ordering
func TestVersionComparisonProperty(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	// Property 1: Reflexivity - a version equals itself
	properties.Property("version comparison is reflexive (v == v)", prop.ForAll(
		func(major, minor, patch int) bool {
			v := Version{Major: major, Minor: minor, Patch: patch}
			return v.Compare(v) == 0
		},
		gen.IntRange(0, 100),
		gen.IntRange(0, 100),
		gen.IntRange(0, 100),
	))

	// Property 2: Antisymmetry - if v1 < v2 then v2 > v1
	properties.Property("version comparison is antisymmetric", prop.ForAll(
		func(major1, minor1, patch1, major2, minor2, patch2 int) bool {
			v1 := Version{Major: major1, Minor: minor1, Patch: patch1}
			v2 := Version{Major: major2, Minor: minor2, Patch: patch2}
			cmp1 := v1.Compare(v2)
			cmp2 := v2.Compare(v1)
			return cmp1 == -cmp2
		},
		gen.IntRange(0, 100),
		gen.IntRange(0, 100),
		gen.IntRange(0, 100),
		gen.IntRange(0, 100),
		gen.IntRange(0, 100),
		gen.IntRange(0, 100),
	))

	// Property 3: Transitivity - if v1 < v2 and v2 < v3 then v1 < v3
	properties.Property("version comparison is transitive", prop.ForAll(
		func(major1, minor1, patch1, major2, minor2, patch2, major3, minor3, patch3 int) bool {
			v1 := Version{Major: major1, Minor: minor1, Patch: patch1}
			v2 := Version{Major: major2, Minor: minor2, Patch: patch2}
			v3 := Version{Major: major3, Minor: minor3, Patch: patch3}

			cmp12 := v1.Compare(v2)
			cmp23 := v2.Compare(v3)
			cmp13 := v1.Compare(v3)

			// If v1 < v2 and v2 < v3, then v1 < v3
			if cmp12 < 0 && cmp23 < 0 {
				return cmp13 < 0
			}
			// If v1 > v2 and v2 > v3, then v1 > v3
			if cmp12 > 0 && cmp23 > 0 {
				return cmp13 > 0
			}
			// If v1 == v2 and v2 == v3, then v1 == v3
			if cmp12 == 0 && cmp23 == 0 {
				return cmp13 == 0
			}
			return true // Other cases don't have strict transitivity requirements
		},
		gen.IntRange(0, 50),
		gen.IntRange(0, 50),
		gen.IntRange(0, 50),
		gen.IntRange(0, 50),
		gen.IntRange(0, 50),
		gen.IntRange(0, 50),
		gen.IntRange(0, 50),
		gen.IntRange(0, 50),
		gen.IntRange(0, 50),
	))

	// Property 4: Major version dominates - higher major always wins
	properties.Property("major version dominates comparison", prop.ForAll(
		func(major1, minor1, patch1, minor2, patch2 int) bool {
			v1 := Version{Major: major1, Minor: minor1, Patch: patch1}
			v2 := Version{Major: major1 + 1, Minor: minor2, Patch: patch2}
			return v1.Compare(v2) < 0
		},
		gen.IntRange(0, 99),
		gen.IntRange(0, 100),
		gen.IntRange(0, 100),
		gen.IntRange(0, 100),
		gen.IntRange(0, 100),
	))

	// Property 5: Minor version dominates when major is equal
	properties.Property("minor version dominates when major is equal", prop.ForAll(
		func(major, minor1, patch1, patch2 int) bool {
			v1 := Version{Major: major, Minor: minor1, Patch: patch1}
			v2 := Version{Major: major, Minor: minor1 + 1, Patch: patch2}
			return v1.Compare(v2) < 0
		},
		gen.IntRange(0, 100),
		gen.IntRange(0, 99),
		gen.IntRange(0, 100),
		gen.IntRange(0, 100),
	))

	// Property 6: Patch version determines order when major and minor are equal
	properties.Property("patch version determines order when major and minor are equal", prop.ForAll(
		func(major, minor, patch1 int) bool {
			v1 := Version{Major: major, Minor: minor, Patch: patch1}
			v2 := Version{Major: major, Minor: minor, Patch: patch1 + 1}
			return v1.Compare(v2) < 0
		},
		gen.IntRange(0, 100),
		gen.IntRange(0, 100),
		gen.IntRange(0, 99),
	))

	// Property 7: Parse and String round-trip
	properties.Property("version parse and string round-trip", prop.ForAll(
		func(major, minor, patch int) bool {
			v := Version{Major: major, Minor: minor, Patch: patch}
			str := v.String()
			parsed, err := ParseVersion(str)
			if err != nil {
				return false
			}
			return parsed == v
		},
		gen.IntRange(0, 100),
		gen.IntRange(0, 100),
		gen.IntRange(0, 100),
	))

	// Property 8: CompareVersions string function matches Version.Compare
	properties.Property("CompareVersions matches Version.Compare", prop.ForAll(
		func(major1, minor1, patch1, major2, minor2, patch2 int) bool {
			v1 := Version{Major: major1, Minor: minor1, Patch: patch1}
			v2 := Version{Major: major2, Minor: minor2, Patch: patch2}

			expected := v1.Compare(v2)
			result, err := CompareVersions(v1.String(), v2.String())
			if err != nil {
				return false
			}
			return result == expected
		},
		gen.IntRange(0, 100),
		gen.IntRange(0, 100),
		gen.IntRange(0, 100),
		gen.IntRange(0, 100),
		gen.IntRange(0, 100),
		gen.IntRange(0, 100),
	))

	properties.TestingRun(t)
}
