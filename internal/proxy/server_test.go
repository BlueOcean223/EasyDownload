package proxy

import (
	"strings"
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

// **Feature: wechat-video-optimization, Property 1: MITM 域名配置正确性**
// **Validates: Requirements 1.1, 1.4**
// For any video streaming domain (finder.video.qq.com, findermp.video.qq.com, szextshort.weixin.qq.com),
// that domain should NOT appear in the MITM configuration list.
// This ensures video streaming domains pass through directly without MITM to avoid slow video loading.
func TestMITMDomainConfigurationProperty(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	// Get the actual domain lists
	mitmDomains := MITMDomains()
	passThroughDomains := PassThroughDomains()

	// Property: Video streaming domains should NOT be in MITM list
	properties.Property("video streaming domains are not in MITM list", prop.ForAll(
		func(domainIndex int) bool {
			// Pick a pass-through domain
			domain := passThroughDomains[domainIndex%len(passThroughDomains)]

			// Check that this domain is NOT in the MITM list
			for _, mitmDomain := range mitmDomains {
				if strings.Contains(mitmDomain, domain) || strings.Contains(domain, mitmDomain) {
					return false
				}
			}
			return true
		},
		gen.IntRange(0, len(passThroughDomains)-1),
	))

	// Property: MITM domains should only contain page content domains
	properties.Property("MITM domains are page content domains only", prop.ForAll(
		func(domainIndex int) bool {
			domain := mitmDomains[domainIndex%len(mitmDomains)]

			// MITM domains should NOT contain video streaming patterns
			videoPatterns := []string{
				"finder.video",
				"findermp.video",
				"szextshort",
			}

			for _, pattern := range videoPatterns {
				if strings.Contains(domain, pattern) {
					return false
				}
			}
			return true
		},
		gen.IntRange(0, len(mitmDomains)-1),
	))

	// Property: Pass-through domains should contain video streaming domains
	properties.Property("pass-through domains are video streaming domains", prop.ForAll(
		func(domainIndex int) bool {
			domain := passThroughDomains[domainIndex%len(passThroughDomains)]

			// Pass-through domains should contain video streaming patterns
			videoPatterns := []string{
				"finder.video",
				"findermp.video",
				"szextshort",
				"mpvideo.qpic.cn",
			}

			for _, pattern := range videoPatterns {
				if strings.Contains(domain, pattern) {
					return true
				}
			}
			return false
		},
		gen.IntRange(0, len(passThroughDomains)-1),
	))

	// Property: MITM and pass-through domains should be disjoint
	properties.Property("MITM and pass-through domains are disjoint", prop.ForAll(
		func(mitmIdx, passIdx int) bool {
			mitmDomain := mitmDomains[mitmIdx%len(mitmDomains)]
			passDomain := passThroughDomains[passIdx%len(passThroughDomains)]

			// They should not be equal or contain each other
			return mitmDomain != passDomain &&
				!strings.Contains(mitmDomain, passDomain) &&
				!strings.Contains(passDomain, mitmDomain)
		},
		gen.IntRange(0, len(mitmDomains)-1),
		gen.IntRange(0, len(passThroughDomains)-1),
	))

	properties.TestingRun(t)
}
