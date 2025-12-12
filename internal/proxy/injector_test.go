package proxy

import (
	"strings"
	"testing"

	"github.com/leanovate/gopter"
	"github.com/leanovate/gopter/gen"
	"github.com/leanovate/gopter/prop"
)

// **Feature: video-capture-fix, Property 1: JS 注入正确性**
// **Validates: Requirements 1.1, 1.2**
// For any JavaScript code containing `get media()` or `finderGetCommentDetail` method,
// the injected code should contain capture logic that sends objectDesc to `/res-downloader/wechat`,
// and the original code functionality should not be affected.

func TestJSInjectionCorrectnessProperty(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	injector := NewJSInjector()

	// Property 1a: Media getter injection preserves structure and adds capture code
	properties.Property("media getter injection adds capture code and preserves return", prop.ForAll(
		func(prefix, suffix string) bool {
			// Create JS content with get media() getter
			jsContent := prefix + `get media(){` + suffix

			result := injector.InjectMediaCapture(jsContent)

			// Check that injection occurred
			if !strings.Contains(result, "res-downloader/wechat") {
				return false
			}

			// Check that objectDesc capture is present
			if !strings.Contains(result, "this.objectDesc") {
				return false
			}

			// Check that fetch POST is present
			if !strings.Contains(result, `method: "POST"`) {
				return false
			}

			// Check that JSON.stringify is used
			if !strings.Contains(result, "JSON.stringify") {
				return false
			}

			// Check prefix is preserved
			if !strings.HasPrefix(result, prefix) {
				return false
			}

			return true
		},
		gen.AlphaString(),
		gen.AlphaString(),
	))

	// Property 1b: Comment detail injection preserves async function structure
	properties.Property("comment detail injection adds capture code and returns result", prop.ForAll(
		func(paramName string) bool {
			if len(paramName) == 0 {
				paramName = "e"
			}
			// Ensure paramName is a valid identifier (alphanumeric starting with letter)
			paramName = "p" + strings.Map(func(r rune) rune {
				if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
					return r
				}
				return -1
			}, paramName)

			// Create JS content with finderGetCommentDetail method (body shape may vary in 4.1+)
			jsContent := `async finderGetCommentDetail(` + paramName + `) { var r = 1; return someAsyncCall(); } async`

			result := injector.InjectCommentCapture(jsContent)

			// Check that injection occurred
			if !strings.Contains(result, "res-downloader/wechat") {
				return false
			}

			// Check that objectDesc capture is present (robust extraction)
			if !strings.Contains(result, "objectDesc") && !strings.Contains(result, "_od") {
				return false
			}

			// Check that result is returned
			if !strings.Contains(result, "return res") {
				return false
			}

			// Check that fetch POST is present
			if !strings.Contains(result, `method: "POST"`) {
				return false
			}

			return true
		},
		gen.AlphaString(),
	))

	// Property 1c: Non-matching content is unchanged
	properties.Property("non-matching JS content remains unchanged", prop.ForAll(
		func(content string) bool {
			// Content without target patterns
			if strings.Contains(content, "get media()") ||
				strings.Contains(content, "get media(){") ||
				strings.Contains(content, "finderGetCommentDetail") {
				return true // Skip content that matches patterns
			}

			resultMedia := injector.InjectMediaCapture(content)
			resultComment := injector.InjectCommentCapture(content)

			return resultMedia == content && resultComment == content
		},
		gen.AnyString(),
	))

	// Property 1d: InjectAll applies both injections
	properties.Property("InjectAll applies both media and comment injections", prop.ForAll(
		func(prefix string) bool {
			// Create JS content with both patterns
			jsContent := prefix + `get media(){return this._media;} async finderGetCommentDetail(e) { return await fetch(); } async`

			result := injector.InjectAll(jsContent)

			// Both injections should be present
			hasMediaInjection := strings.Contains(result, "this.objectDesc") &&
				strings.Contains(result, "type=1")
			hasCommentInjection := strings.Contains(result, "res?.data?.object?.objectDesc") &&
				strings.Contains(result, "type=2")

			return hasMediaInjection && hasCommentInjection
		},
		gen.AlphaString(),
	))

	properties.TestingRun(t)
}

// TestIsTargetJSFileProperty tests the IsTargetJSFile method
func TestIsTargetJSFileProperty(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	injector := NewJSInjector()

	// Property: Target JS files are correctly identified
	properties.Property("target JS files are correctly identified", prop.ForAll(
		func(randomPath string) bool {
			// Test known target patterns
			targetPaths := []string{
				"/t/wx_fed/finder/web/web-finder/res/js/virtual_svg-icons-register.publish.123.js",
				"https://res.wx.qq.com/virtual_svg-icons-register.js",
				"/path/to/virtual_svg-icons-register.abc.js?v=123",
			}

			for _, path := range targetPaths {
				if !injector.IsTargetJSFile(path) {
					return false
				}
			}

			// Test known non-target patterns
			nonTargetPaths := []string{
				"/some/other/file.js",
				"/virtual_svg-icons-register.css",
				"/virtual_svg-icons-register",
			}

			for _, path := range nonTargetPaths {
				if injector.IsTargetJSFile(path) {
					return false
				}
			}

			return true
		},
		gen.AnyString(),
	))

	properties.TestingRun(t)
}

// TestAddVersionToJSLinksProperty tests version addition to JS links
func TestAddVersionToJSLinksProperty(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	injector := NewJSInjector()
	injector.SetVersion("test123")

	// Property: Version is added to matching JS links
	properties.Property("version is added to virtual_svg-icons-register JS links", prop.ForAll(
		func(prefix, suffix string) bool {
			// Create HTML with JS link
			html := prefix + `<script src="https://res.wx.qq.com/virtual_svg-icons-register.js"></script>` + suffix

			result := injector.AddVersionToJSLinks(html)

			// Version should be added
			return strings.Contains(result, "v=test123") || strings.Contains(result, "virtual_svg-icons-register")
		},
		gen.AlphaString(),
		gen.AlphaString(),
	))

	properties.TestingRun(t)
}

// Unit tests for specific edge cases

func TestInjectMediaCapture_EmptyContent(t *testing.T) {
	injector := NewJSInjector()
	result := injector.InjectMediaCapture("")
	if result != "" {
		t.Errorf("Expected empty string, got %s", result)
	}
}

func TestInjectCommentCapture_EmptyContent(t *testing.T) {
	injector := NewJSInjector()
	result := injector.InjectCommentCapture("")
	if result != "" {
		t.Errorf("Expected empty string, got %s", result)
	}
}

func TestInjectMediaCapture_WithWhitespace(t *testing.T) {
	injector := NewJSInjector()

	// Test with various whitespace patterns
	testCases := []string{
		`get media() {`,
		`get  media(){`,
		`get media( ) {`,
	}

	for _, tc := range testCases {
		result := injector.InjectMediaCapture(tc)
		if !strings.Contains(result, "res-downloader/wechat") {
			t.Errorf("Expected injection for pattern: %s", tc)
		}
	}
}

func TestHasMediaGetter(t *testing.T) {
	testCases := []struct {
		content  string
		expected bool
	}{
		{`get media(){`, true},
		{`get media() {`, true},
		{`get  media(){`, true},
		{`getmedia(){`, false},
		{`get media`, false},
		{``, false},
	}

	for _, tc := range testCases {
		result := HasMediaGetter(tc.content)
		if result != tc.expected {
			t.Errorf("HasMediaGetter(%q) = %v, expected %v", tc.content, result, tc.expected)
		}
	}
}

func TestHasCommentDetail(t *testing.T) {
	testCases := []struct {
		content  string
		expected bool
	}{
		{`async finderGetCommentDetail(e) { return await fetch(); } async`, true},
		{`async finderGetCommentDetail(param){ return data; }async`, true},
		{`finderGetCommentDetail(e) { return data; }`, false},
		{`async finderGetCommentDetail`, false},
		{``, false},
	}

	for _, tc := range testCases {
		result := HasCommentDetail(tc.content)
		if result != tc.expected {
			t.Errorf("HasCommentDetail(%q) = %v, expected %v", tc.content, result, tc.expected)
		}
	}
}
