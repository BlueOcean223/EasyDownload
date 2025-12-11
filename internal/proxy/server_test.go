package proxy

import (
	"testing"

	"github.com/leanovate/gopter"
	"github.com/leanovate/gopter/gen"
	"github.com/leanovate/gopter/prop"
)

// **Feature: easydownload-improvements, Property 1: 视频 URL 去重一致性**
// **Validates: Requirements 2.3**
// For any video detection list and newly detected video URL,
// after adding, the list should not contain duplicate URLs.
func TestVideoURLDeduplicationProperty(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("adding duplicate URLs should not create duplicates", prop.ForAll(
		func(urls []string) bool {
			ps := &ProxyServer{}

			// Add all URLs
			for _, url := range urls {
				ps.addVideoURL(url)
			}

			// Count unique URLs in the sync.Map
			uniqueCount := 0
			ps.detectedURLs.Range(func(key, value interface{}) bool {
				uniqueCount++
				return true
			})

			// Count unique URLs in input
			uniqueURLs := make(map[string]bool)
			for _, url := range urls {
				uniqueURLs[url] = true
			}

			// The number of stored URLs should equal the number of unique input URLs
			return uniqueCount == len(uniqueURLs)
		},
		gen.SliceOf(gen.AnyString()),
	))

	properties.Property("addVideoURL returns true only for new URLs", prop.ForAll(
		func(url string, repeatCount int) bool {
			ps := &ProxyServer{}

			// First add should return true
			firstResult := ps.addVideoURL(url)
			if !firstResult {
				return false
			}

			// Subsequent adds should return false
			for i := 0; i < repeatCount; i++ {
				if ps.addVideoURL(url) {
					return false
				}
			}

			return true
		},
		gen.AnyString(),
		gen.IntRange(1, 10),
	))

	properties.Property("ClearDetectedURLs resets deduplication state", prop.ForAll(
		func(urls []string) bool {
			ps := &ProxyServer{}

			// Add URLs
			for _, url := range urls {
				ps.addVideoURL(url)
			}

			// Clear
			ps.ClearDetectedURLs()

			// All URLs should be considered new again
			for _, url := range urls {
				if !ps.addVideoURL(url) {
					return false
				}
			}

			return true
		},
		gen.SliceOf(gen.AnyString()).SuchThat(func(urls []string) bool {
			// Ensure unique URLs for this test
			seen := make(map[string]bool)
			for _, url := range urls {
				if seen[url] {
					return false
				}
				seen[url] = true
			}
			return true
		}),
	))

	properties.TestingRun(t)
}
