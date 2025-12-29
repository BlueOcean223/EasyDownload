package proxy

import (
	"strings"
	"testing"

	"github.com/leanovate/gopter"
	"github.com/leanovate/gopter/gen"
	"github.com/leanovate/gopter/prop"
)

// For any video streaming domain (finder.video.qq.com, findermp.video.qq.com, szextshort.weixin.qq.com),
// that domain should NOT appear in the MITM configuration list.
// This ensures video streaming domains pass through directly without MITM to avoid slow video loading.
func TestMITMDomainConfigurationProperty(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	mitmDomains := MITMDomains()
	passThroughDomains := PassThroughDomains()

	properties.Property("video streaming domains are not in MITM list", prop.ForAll(
		func(domainIndex int) bool {
			domain := passThroughDomains[domainIndex%len(passThroughDomains)]
			for _, mitmDomain := range mitmDomains {
				if strings.Contains(mitmDomain, domain) || strings.Contains(domain, mitmDomain) {
					return false
				}
			}
			return true
		},
		gen.IntRange(0, len(passThroughDomains)-1),
	))

	properties.Property("MITM domains are page content domains only", prop.ForAll(
		func(domainIndex int) bool {
			domain := mitmDomains[domainIndex%len(mitmDomains)]
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

	properties.Property("pass-through domains are video streaming domains", prop.ForAll(
		func(domainIndex int) bool {
			domain := passThroughDomains[domainIndex%len(passThroughDomains)]
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

	properties.Property("MITM and pass-through domains are disjoint", prop.ForAll(
		func(mitmIdx, passIdx int) bool {
			mitmDomain := mitmDomains[mitmIdx%len(mitmDomains)]
			passDomain := passThroughDomains[passIdx%len(passThroughDomains)]
			return mitmDomain != passDomain &&
				!strings.Contains(mitmDomain, passDomain) &&
				!strings.Contains(passDomain, mitmDomain)
		},
		gen.IntRange(0, len(mitmDomains)-1),
		gen.IntRange(0, len(passThroughDomains)-1),
	))

	properties.TestingRun(t)
}
