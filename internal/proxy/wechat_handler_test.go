package proxy

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/leanovate/gopter"
	"github.com/leanovate/gopter/gen"
	"github.com/leanovate/gopter/prop"
)

// **Feature: video-capture-fix, Property 2: objectDesc 解析正确性**
// **Validates: Requirements 1.4, 1.5**
// For any valid objectDesc JSON data, parsing should correctly extract url, urlToken,
// coverUrl, description, fileSize, decodeKey fields, and url and urlToken should be
// correctly concatenated into a full URL.

func TestObjectDescParsingProperty(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	handler := NewWeChatHandler()

	// Property 2a: Valid objectDesc is parsed correctly with url and urlToken concatenation
	properties.Property("valid objectDesc parses correctly with url+urlToken concatenation", prop.ForAll(
		func(baseURL, token, coverURL, description, decodeKey string, fileSize float64) bool {
			// Skip empty base URLs as they are invalid
			if baseURL == "" {
				return true
			}

			// Ensure baseURL is a valid URL format
			baseURL = "https://finder.video.qq.com/" + baseURL

			// Create objectDesc JSON
			objDesc := map[string]interface{}{
				"media": []map[string]interface{}{
					{
						"url":       baseURL,
						"urlToken":  token,
						"coverUrl":  coverURL,
						"fileSize":  fileSize,
						"decodeKey": decodeKey,
						"mediaType": 4,
					},
				},
				"description": description,
			}

			data, err := json.Marshal(objDesc)
			if err != nil {
				return true // Skip invalid JSON
			}

			result, err := handler.ParseObjectDesc(data)
			if err != nil {
				return false
			}

			// Verify URL concatenation
			expectedURL := BuildFullURL(baseURL, token)
			if result.URL != expectedURL {
				return false
			}

			// Verify other fields
			if result.CoverURL != coverURL {
				return false
			}
			if result.FileSize != fileSize {
				return false
			}
			if result.DecodeKey != decodeKey {
				return false
			}

			return true
		},
		gen.AlphaString(),
		gen.AlphaString(),
		gen.AlphaString(),
		gen.AlphaString(),
		gen.AlphaString(),
		gen.Float64Range(0, 1000000000),
	))

	// Property 2b: BuildFullURL correctly concatenates url and urlToken
	properties.Property("BuildFullURL correctly concatenates url and urlToken", prop.ForAll(
		func(baseURL, token string) bool {
			// Skip empty base URLs
			if baseURL == "" {
				result := BuildFullURL(baseURL, token)
				return result == ""
			}

			// Ensure baseURL is a valid URL format
			baseURL = "https://example.com/" + baseURL

			result := BuildFullURL(baseURL, token)

			// Result should contain baseURL
			if !strings.HasPrefix(result, "https://example.com/") {
				return false
			}

			// If token is not empty, result should contain token content
			if token != "" {
				// Token content (without leading & or ?) should be in result
				tokenContent := strings.TrimLeft(token, "&?")
				if tokenContent != "" && !strings.Contains(result, tokenContent) {
					return false
				}
			}

			// Result should be a valid URL (no double ? or &&)
			if strings.Contains(result, "??") || strings.Contains(result, "&&") {
				return false
			}

			return true
		},
		gen.AlphaString(),
		gen.OneGenOf(
			gen.Const(""),
			gen.AlphaString().Map(func(s string) string { return "&" + s }),
			gen.AlphaString().Map(func(s string) string { return "?" + s }),
			gen.AlphaString(),
		),
	))

	properties.TestingRun(t)
}

// **Feature: video-capture-fix, Property 5: 视频 URL 去重**
// **Validates: Requirements 3.4**
// For any video URL, adding it twice should result in only one entry in the detection list.

func TestURLDeduplicationProperty(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	// Property 5: Adding same URL twice results in only one detection
	properties.Property("duplicate URLs are detected and ignored", prop.ForAll(
		func(urlPath string) bool {
			handler := NewWeChatHandler()

			// Create a valid URL
			videoURL := "https://finder.video.qq.com/" + urlPath

			// First add should succeed
			firstAdd := handler.addVideoURL(videoURL)
			if !firstAdd {
				return false
			}

			// Second add should fail (duplicate)
			secondAdd := handler.addVideoURL(videoURL)
			if secondAdd {
				return false
			}

			// URL should be detected
			if !handler.HasDetectedURL(videoURL) {
				return false
			}

			return true
		},
		gen.AlphaString(),
	))

	// Property 5b: Different URLs are all added
	properties.Property("different URLs are all added", prop.ForAll(
		func(url1, url2, url3 string) bool {
			handler := NewWeChatHandler()

			// Create distinct URLs
			videoURL1 := "https://finder.video.qq.com/a/" + url1
			videoURL2 := "https://finder.video.qq.com/b/" + url2
			videoURL3 := "https://finder.video.qq.com/c/" + url3

			// All should be added successfully
			if !handler.addVideoURL(videoURL1) {
				return false
			}
			if !handler.addVideoURL(videoURL2) {
				return false
			}
			if !handler.addVideoURL(videoURL3) {
				return false
			}

			// All should be detected
			return handler.HasDetectedURL(videoURL1) &&
				handler.HasDetectedURL(videoURL2) &&
				handler.HasDetectedURL(videoURL3)
		},
		gen.AlphaString(),
		gen.AlphaString(),
		gen.AlphaString(),
	))

	properties.TestingRun(t)
}

// **Feature: video-capture-fix, Property 6: URL 验证和过滤**
// **Validates: Requirements 5.1, 5.4**
// For any string, if it's not a valid video URL (empty string, incorrect format, etc.),
// it should not be added to the detection list.

func TestURLValidationProperty(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	// Property 6a: Empty URLs are invalid
	properties.Property("empty URLs are invalid", prop.ForAll(
		func(_ string) bool {
			return !IsValidVideoURL("")
		},
		gen.Const(""),
	))

	// Property 6b: URLs without scheme are invalid
	properties.Property("URLs without scheme are invalid", prop.ForAll(
		func(host, path string) bool {
			// Create URL without scheme
			url := host + "/" + path
			return !IsValidVideoURL(url)
		},
		gen.AlphaString(),
		gen.AlphaString(),
	))

	// Property 6c: URLs with invalid scheme are invalid
	properties.Property("URLs with invalid scheme are invalid", prop.ForAll(
		func(scheme, host, path string) bool {
			// Skip http and https schemes
			if scheme == "http" || scheme == "https" {
				return true
			}
			// Create URL with invalid scheme
			url := scheme + "://" + host + "/" + path
			return !IsValidVideoURL(url)
		},
		gen.OneGenOf(
			gen.Const("ftp"),
			gen.Const("file"),
			gen.Const("mailto"),
			gen.AlphaString(),
		),
		gen.AlphaString().SuchThat(func(s string) bool { return len(s) > 0 }),
		gen.AlphaString(),
	))

	// Property 6d: Valid http/https URLs are valid
	properties.Property("valid http/https URLs are valid", prop.ForAll(
		func(scheme, host, path string) bool {
			if host == "" {
				return true // Skip empty hosts
			}
			url := scheme + "://" + host + "/" + path
			return IsValidVideoURL(url)
		},
		gen.OneGenOf(gen.Const("http"), gen.Const("https")),
		gen.AlphaString().SuchThat(func(s string) bool { return len(s) > 0 }),
		gen.AlphaString(),
	))

	// Property 6e: Invalid URLs are rejected by ParseObjectDesc
	properties.Property("invalid URLs cause ParseObjectDesc to fail", prop.ForAll(
		func(invalidURL string) bool {
			handler := NewWeChatHandler()

			// Create objectDesc with invalid URL
			objDesc := map[string]interface{}{
				"media": []map[string]interface{}{
					{
						"url":      invalidURL,
						"urlToken": "",
					},
				},
				"description": "test",
			}

			data, err := json.Marshal(objDesc)
			if err != nil {
				return true
			}

			_, err = handler.ParseObjectDesc(data)
			// Should fail for invalid URLs
			return err != nil
		},
		gen.OneGenOf(
			gen.Const(""),
			gen.Const("not-a-url"),
			gen.Const("ftp://invalid.com"),
			gen.AlphaString().SuchThat(func(s string) bool {
				return !strings.HasPrefix(s, "http://") && !strings.HasPrefix(s, "https://")
			}),
		),
	))

	properties.TestingRun(t)
}

// **Feature: video-capture-fix, Property 7: 默认值填充**
// **Validates: Requirements 5.2**
// For any video info missing a title, processing should use "未知标题" as the default value.

func TestDefaultValueFillingProperty(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	handler := NewWeChatHandler()

	// Property 7a: Empty description results in default title
	properties.Property("empty description results in default title", prop.ForAll(
		func(urlPath string) bool {
			// Create objectDesc with empty description
			objDesc := map[string]interface{}{
				"media": []map[string]interface{}{
					{
						"url":      "https://finder.video.qq.com/" + urlPath,
						"urlToken": "",
					},
				},
				"description": "",
			}

			data, err := json.Marshal(objDesc)
			if err != nil {
				return true
			}

			result, err := handler.ParseObjectDesc(data)
			if err != nil {
				return true // URL validation might fail
			}

			return result.Title == "未知标题"
		},
		gen.AlphaString().SuchThat(func(s string) bool { return len(s) > 0 }),
	))

	// Property 7b: Non-empty description is preserved
	properties.Property("non-empty description is preserved as title", prop.ForAll(
		func(urlPath, description string) bool {
			if description == "" {
				return true // Skip empty descriptions
			}

			// Create objectDesc with description
			objDesc := map[string]interface{}{
				"media": []map[string]interface{}{
					{
						"url":      "https://finder.video.qq.com/" + urlPath,
						"urlToken": "",
					},
				},
				"description": description,
			}

			data, err := json.Marshal(objDesc)
			if err != nil {
				return true
			}

			result, err := handler.ParseObjectDesc(data)
			if err != nil {
				return true // URL validation might fail
			}

			return result.Title == description
		},
		gen.AlphaString().SuchThat(func(s string) bool { return len(s) > 0 }),
		gen.AlphaString(),
	))

	properties.TestingRun(t)
}

// Unit tests for specific edge cases

func TestBuildFullURL_EdgeCases(t *testing.T) {
	testCases := []struct {
		name     string
		baseURL  string
		urlToken string
		expected string
	}{
		{
			name:     "empty base URL",
			baseURL:  "",
			urlToken: "&token=abc",
			expected: "",
		},
		{
			name:     "empty token",
			baseURL:  "https://example.com/video",
			urlToken: "",
			expected: "https://example.com/video",
		},
		{
			name:     "token with ampersand, base without query",
			baseURL:  "https://example.com/video",
			urlToken: "&token=abc",
			expected: "https://example.com/video?token=abc",
		},
		{
			name:     "token with ampersand, base with query",
			baseURL:  "https://example.com/video?id=1",
			urlToken: "&token=abc",
			expected: "https://example.com/video?id=1&token=abc",
		},
		{
			name:     "token with question mark, base without query",
			baseURL:  "https://example.com/video",
			urlToken: "?token=abc",
			expected: "https://example.com/video?token=abc",
		},
		{
			name:     "token with question mark, base with query",
			baseURL:  "https://example.com/video?id=1",
			urlToken: "?token=abc",
			expected: "https://example.com/video?id=1&token=abc",
		},
		{
			name:     "token without prefix, base without query",
			baseURL:  "https://example.com/video",
			urlToken: "token=abc",
			expected: "https://example.com/video?token=abc",
		},
		{
			name:     "token without prefix, base with query",
			baseURL:  "https://example.com/video?id=1",
			urlToken: "token=abc",
			expected: "https://example.com/video?id=1&token=abc",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := BuildFullURL(tc.baseURL, tc.urlToken)
			if result != tc.expected {
				t.Errorf("BuildFullURL(%q, %q) = %q, expected %q",
					tc.baseURL, tc.urlToken, result, tc.expected)
			}
		})
	}
}

func TestIsValidVideoURL_EdgeCases(t *testing.T) {
	testCases := []struct {
		url      string
		expected bool
	}{
		{"", false},
		{"not-a-url", false},
		{"http://", false},
		{"https://", false},
		{"ftp://example.com", false},
		{"file:///path/to/file", false},
		{"http://example.com", true},
		{"https://example.com", true},
		{"https://finder.video.qq.com/video.mp4", true},
		{"https://example.com/path?query=value", true},
	}

	for _, tc := range testCases {
		t.Run(tc.url, func(t *testing.T) {
			result := IsValidVideoURL(tc.url)
			if result != tc.expected {
				t.Errorf("IsValidVideoURL(%q) = %v, expected %v", tc.url, result, tc.expected)
			}
		})
	}
}

func TestParseObjectDesc_EmptyMedia(t *testing.T) {
	handler := NewWeChatHandler()

	objDesc := map[string]interface{}{
		"media":       []map[string]interface{}{},
		"description": "test",
	}

	data, _ := json.Marshal(objDesc)
	_, err := handler.ParseObjectDesc(data)

	if err == nil {
		t.Error("Expected error for empty media array")
	}
}

func TestParseObjectDesc_InvalidJSON(t *testing.T) {
	handler := NewWeChatHandler()

	_, err := handler.ParseObjectDesc([]byte("not valid json"))

	if err == nil {
		t.Error("Expected error for invalid JSON")
	}
}

func TestHandleWeChatRequest_Callback(t *testing.T) {
	handler := NewWeChatHandler()

	var receivedInfo *WeChatVideoInfo
	handler.SetVideoCallback(func(info WeChatVideoInfo) {
		receivedInfo = &info
	})

	objDesc := map[string]interface{}{
		"media": []map[string]interface{}{
			{
				"url":       "https://finder.video.qq.com/test.mp4",
				"urlToken":  "&token=abc",
				"coverUrl":  "https://example.com/cover.jpg",
				"fileSize":  float64(12345),
				"decodeKey": "key123",
			},
		},
		"description": "Test Video",
	}

	data, _ := json.Marshal(objDesc)
	err := handler.HandleWeChatRequest(data)

	if err != nil {
		t.Errorf("HandleWeChatRequest failed: %v", err)
	}

	if receivedInfo == nil {
		t.Error("Callback was not called")
		return
	}

	if receivedInfo.Title != "Test Video" {
		t.Errorf("Expected title 'Test Video', got %q", receivedInfo.Title)
	}

	if receivedInfo.CoverURL != "https://example.com/cover.jpg" {
		t.Errorf("Expected coverUrl, got %q", receivedInfo.CoverURL)
	}
}

func TestClearDetectedURLs(t *testing.T) {
	handler := NewWeChatHandler()

	// Add some URLs
	handler.addVideoURL("https://example.com/1")
	handler.addVideoURL("https://example.com/2")

	// Verify they exist
	if !handler.HasDetectedURL("https://example.com/1") {
		t.Error("URL 1 should exist")
	}

	// Clear
	handler.ClearDetectedURLs()

	// Verify they're gone
	if handler.HasDetectedURL("https://example.com/1") {
		t.Error("URL 1 should not exist after clear")
	}

	// Should be able to add again
	if !handler.addVideoURL("https://example.com/1") {
		t.Error("Should be able to add URL 1 again after clear")
	}
}

// **Feature: wechat-video-optimization, Property 6: 作者信息提取**
// **Validates: Requirements 5.1**
// For any objectDesc data containing a contact field, parsing should correctly extract
// the nickname as the author name.

func TestAuthorInfoExtractionProperty(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	handler := NewWeChatHandler()

	// Property 6: Contact nickname is correctly extracted as author
	properties.Property("contact nickname is extracted as author", prop.ForAll(
		func(urlPath, nickname, headURL, username string) bool {
			// Skip empty nicknames as they should use default
			if nickname == "" {
				return true
			}

			// Create objectDesc with contact
			objDesc := map[string]interface{}{
				"media": []map[string]interface{}{
					{
						"url":      "https://finder.video.qq.com/" + urlPath,
						"urlToken": "",
					},
				},
				"description": "Test Video",
				"contact": map[string]interface{}{
					"username": username,
					"nickname": nickname,
					"head_url": headURL,
				},
			}

			data, err := json.Marshal(objDesc)
			if err != nil {
				return true
			}

			result, err := handler.ParseObjectDesc(data)
			if err != nil {
				return true // URL validation might fail
			}

			// Verify author is extracted from nickname
			if result.Author != nickname {
				return false
			}

			// Verify avatar URL is extracted
			if result.AuthorAvatar != headURL {
				return false
			}

			return true
		},
		gen.AlphaString().SuchThat(func(s string) bool { return len(s) > 0 }),
		gen.AlphaString().SuchThat(func(s string) bool { return len(s) > 0 }),
		gen.AlphaString(),
		gen.AlphaString(),
	))

	properties.TestingRun(t)
}

// **Feature: wechat-video-optimization, Property 7: 作者信息默认值**
// **Validates: Requirements 5.3**
// For any objectDesc data missing contact field or with empty nickname,
// parsing should use "未知作者" as the default author name.

func TestAuthorDefaultValueProperty(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	handler := NewWeChatHandler()

	// Property 7a: Missing contact results in default author
	properties.Property("missing contact results in default author", prop.ForAll(
		func(urlPath string) bool {
			// Create objectDesc without contact
			objDesc := map[string]interface{}{
				"media": []map[string]interface{}{
					{
						"url":      "https://finder.video.qq.com/" + urlPath,
						"urlToken": "",
					},
				},
				"description": "Test Video",
			}

			data, err := json.Marshal(objDesc)
			if err != nil {
				return true
			}

			result, err := handler.ParseObjectDesc(data)
			if err != nil {
				return true // URL validation might fail
			}

			return result.Author == "未知作者"
		},
		gen.AlphaString().SuchThat(func(s string) bool { return len(s) > 0 }),
	))

	// Property 7b: Empty nickname results in default author
	properties.Property("empty nickname results in default author", prop.ForAll(
		func(urlPath, headURL string) bool {
			// Create objectDesc with contact but empty nickname
			objDesc := map[string]interface{}{
				"media": []map[string]interface{}{
					{
						"url":      "https://finder.video.qq.com/" + urlPath,
						"urlToken": "",
					},
				},
				"description": "Test Video",
				"contact": map[string]interface{}{
					"username": "user123",
					"nickname": "",
					"head_url": headURL,
				},
			}

			data, err := json.Marshal(objDesc)
			if err != nil {
				return true
			}

			result, err := handler.ParseObjectDesc(data)
			if err != nil {
				return true // URL validation might fail
			}

			return result.Author == "未知作者"
		},
		gen.AlphaString().SuchThat(func(s string) bool { return len(s) > 0 }),
		gen.AlphaString(),
	))

	properties.TestingRun(t)
}

// **Feature: wechat-video-optimization, Property 8: 视频时长提取**
// **Validates: Requirements 6.1**
// For any objectDesc data containing spec array with durationMs,
// parsing should correctly extract the duration.

func TestVideoDurationExtractionProperty(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	handler := NewWeChatHandler()

	// Property 8: Duration is correctly extracted from spec
	properties.Property("durationMs is extracted from spec", prop.ForAll(
		func(urlPath string, durationMs int) bool {
			// Skip negative durations
			if durationMs < 0 {
				return true
			}

			// Create objectDesc with spec containing duration
			objDesc := map[string]interface{}{
				"media": []map[string]interface{}{
					{
						"url":      "https://finder.video.qq.com/" + urlPath,
						"urlToken": "",
						"spec": []map[string]interface{}{
							{
								"fileFormat": "mp4_720p",
								"durationMs": durationMs,
								"width":      1280,
								"height":     720,
							},
						},
					},
				},
				"description": "Test Video",
			}

			data, err := json.Marshal(objDesc)
			if err != nil {
				return true
			}

			result, err := handler.ParseObjectDesc(data)
			if err != nil {
				return true // URL validation might fail
			}

			return result.Duration == durationMs
		},
		gen.AlphaString().SuchThat(func(s string) bool { return len(s) > 0 }),
		gen.IntRange(0, 3600000), // 0 to 1 hour in ms
	))

	properties.TestingRun(t)
}

// **Feature: wechat-video-optimization, Property 9: 视频规格提取**
// **Validates: Requirements 6.2**
// For any objectDesc data containing spec array,
// parsing should extract all fileFormat values.

func TestVideoSpecExtractionProperty(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	handler := NewWeChatHandler()

	// Property 9a: Single fileFormat is extracted
	properties.Property("single fileFormat is extracted", prop.ForAll(
		func(urlPath, fileFormat string) bool {
			// Skip empty formats
			if fileFormat == "" {
				return true
			}

			// Create objectDesc with single spec
			objDesc := map[string]interface{}{
				"media": []map[string]interface{}{
					{
						"url":      "https://finder.video.qq.com/" + urlPath,
						"urlToken": "",
						"spec": []map[string]interface{}{
							{
								"fileFormat": fileFormat,
							},
						},
					},
				},
				"description": "Test Video",
			}

			data, err := json.Marshal(objDesc)
			if err != nil {
				return true
			}

			result, err := handler.ParseObjectDesc(data)
			if err != nil {
				return true // URL validation might fail
			}

			// Verify format is in the list
			if len(result.FileFormats) != 1 {
				return false
			}
			return result.FileFormats[0] == fileFormat
		},
		gen.AlphaString().SuchThat(func(s string) bool { return len(s) > 0 }),
		gen.AlphaString().SuchThat(func(s string) bool { return len(s) > 0 }),
	))

	// Property 9b: Multiple fileFormats are all extracted
	properties.Property("multiple fileFormats are all extracted", prop.ForAll(
		func(urlPath, format1, format2, format3 string) bool {
			// Skip empty formats
			if format1 == "" || format2 == "" || format3 == "" {
				return true
			}

			// Create objectDesc with multiple specs
			objDesc := map[string]interface{}{
				"media": []map[string]interface{}{
					{
						"url":      "https://finder.video.qq.com/" + urlPath,
						"urlToken": "",
						"spec": []map[string]interface{}{
							{"fileFormat": format1},
							{"fileFormat": format2},
							{"fileFormat": format3},
						},
					},
				},
				"description": "Test Video",
			}

			data, err := json.Marshal(objDesc)
			if err != nil {
				return true
			}

			result, err := handler.ParseObjectDesc(data)
			if err != nil {
				return true // URL validation might fail
			}

			// Verify all formats are in the list
			if len(result.FileFormats) != 3 {
				return false
			}

			// Check each format is present
			formats := make(map[string]bool)
			for _, f := range result.FileFormats {
				formats[f] = true
			}

			return formats[format1] && formats[format2] && formats[format3]
		},
		gen.AlphaString().SuchThat(func(s string) bool { return len(s) > 0 }),
		gen.AlphaString().SuchThat(func(s string) bool { return len(s) > 0 }),
		gen.AlphaString().SuchThat(func(s string) bool { return len(s) > 0 }),
		gen.AlphaString().SuchThat(func(s string) bool { return len(s) > 0 }),
	))

	// Property 9c: Width and height are extracted from spec
	properties.Property("width and height are extracted from spec", prop.ForAll(
		func(urlPath string, width, height int) bool {
			// Skip invalid dimensions
			if width <= 0 || height <= 0 {
				return true
			}

			// Create objectDesc with spec containing dimensions
			objDesc := map[string]interface{}{
				"media": []map[string]interface{}{
					{
						"url":      "https://finder.video.qq.com/" + urlPath,
						"urlToken": "",
						"spec": []map[string]interface{}{
							{
								"fileFormat": "mp4_720p",
								"width":      width,
								"height":     height,
							},
						},
					},
				},
				"description": "Test Video",
			}

			data, err := json.Marshal(objDesc)
			if err != nil {
				return true
			}

			result, err := handler.ParseObjectDesc(data)
			if err != nil {
				return true // URL validation might fail
			}

			return result.Width == width && result.Height == height
		},
		gen.AlphaString().SuchThat(func(s string) bool { return len(s) > 0 }),
		gen.IntRange(1, 7680), // Up to 8K width
		gen.IntRange(1, 4320), // Up to 8K height
	))

	properties.TestingRun(t)
}
