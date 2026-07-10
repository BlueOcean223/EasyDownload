package proxy

import (
	"EasyDownload/internal/download/wechat"
	"bytes"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/leanovate/gopter"
	"github.com/leanovate/gopter/gen"
	"github.com/leanovate/gopter/prop"
)

func TestProxyServerSetPortOnlyChangesStoppedListener(t *testing.T) {
	server := NewProxyServer(nil, 8899)
	if err := server.SetPort(9999); err != nil {
		t.Fatalf("SetPort while stopped returned error: %v", err)
	}
	if got := server.GetPort(); got != 9999 {
		t.Fatalf("GetPort = %d, want 9999", got)
	}
	if err := server.SetPort(0); err == nil {
		t.Fatal("SetPort accepted an invalid port")
	}

	server.mu.Lock()
	server.running = true
	server.mu.Unlock()
	if err := server.SetPort(10000); err == nil {
		t.Fatal("SetPort changed a running listener")
	}
	if got := server.GetPort(); got != 9999 {
		t.Fatalf("running server port changed to %d", got)
	}
	if err := server.SetPort(9999); err != nil {
		t.Fatalf("idempotent SetPort on running listener returned error: %v", err)
	}
}

func TestProxyVideoCallbackCanBeReplacedConcurrently(t *testing.T) {
	server := NewProxyServer(nil, 8899)
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			server.SetVideoCallback(func(wechat.VideoInfo) {})
		}()
		go func() {
			defer wg.Done()
			if callback := server.videoCallback(); callback != nil {
				callback(wechat.VideoInfo{})
			}
		}()
	}
	wg.Wait()
}

// For any video streaming domain (finder.video.qq.com, findermp.video.qq.com, szextshort.weixin.qq.com),
// that domain should NOT appear in the MITM configuration list.
// This ensures video streaming domains pass through directly without MITM to avoid slow video loading.
func TestHostMatchesDomain(t *testing.T) {
	cases := []struct {
		name   string
		host   string
		domain string
		want   bool
	}{
		{"exact", "finder.video.qq.com", "finder.video.qq.com", true},
		{"subdomain", "a.finder.video.qq.com", "finder.video.qq.com", true},
		{"with port", "finder.video.qq.com:443", "finder.video.qq.com", true},
		{"lookalike", "finder.video.qq.com.evil.example", "finder.video.qq.com", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := hostMatchesDomain(tc.host, tc.domain); got != tc.want {
				t.Fatalf("hostMatchesDomain(%q, %q) = %v, want %v", tc.host, tc.domain, got, tc.want)
			}
		})
	}
}

func TestStrictMITMAllowlist(t *testing.T) {
	if !shouldMITMHost("channels.weixin.qq.com:443") {
		t.Fatal("expected WeChat page domain to be MITM'ed")
	}
	if shouldMITMHost("example.com:443") {
		t.Fatal("non-allowlisted domain must pass through")
	}
	if shouldMITMHost("finder.video.qq.com:443") {
		t.Fatal("video streaming domain must pass through")
	}
}

func TestHTMLUnsupportedEncodingPreservesBody(t *testing.T) {
	ps := NewProxyServer(nil, 0)
	original := []byte("<html><body>unchanged</body></html>")
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type":     []string{"text/html; charset=utf-8"},
			"Content-Encoding": []string{"zstd"},
		},
		Body:          io.NopCloser(bytes.NewReader(original)),
		ContentLength: int64(len(original)),
	}

	gotResp := ps.handleWeChatHTMLResponse(resp, nil)
	got, err := io.ReadAll(gotResp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, original) {
		t.Fatalf("body=%q, want %q", got, original)
	}
	if enc := gotResp.Header.Get("Content-Encoding"); enc != "zstd" {
		t.Fatalf("Content-Encoding=%q, want zstd", enc)
	}
}

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
