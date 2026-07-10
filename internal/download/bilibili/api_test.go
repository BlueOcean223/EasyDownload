package bilibili

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSelectURLsPromotesBackupAndDeduplicates(t *testing.T) {
	primary, backups := selectURLs(
		"",
		"",
		[]string{"", " https://cdn.example.com/a ", "https://cdn.example.com/a"},
		[]string{"https://cdn.example.com/b", "https://cdn.example.com/a"},
	)

	assert.Equal(t, "https://cdn.example.com/a", primary)
	assert.Equal(t, []string{"https://cdn.example.com/b"}, backups)
}

func TestQRCodeLoginStoresSessionWithoutReturningIt(t *testing.T) {
	const secret = "qr-session-secret"
	doer := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		assert.Equal(t, "/x/passport-login/web/qrcode/poll", req.URL.Path)
		header := make(http.Header)
		header.Add("Set-Cookie", "SESSDATA="+secret+"; Path=/; HttpOnly; Secure")
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     header,
			Body: io.NopCloser(strings.NewReader(`{
				"code":0,
				"data":{"code":0,"message":"success","url":"https://www.bilibili.com/"}
			}`)),
		}, nil
	})
	downloader := NewBilibiliDownloader(doer)
	downloader.storeSessData = func(value string) error {
		require.Equal(t, secret, value)
		return nil
	}
	status, err := downloader.PollQRCodeStatus("qr-key")
	require.NoError(t, err)
	require.Equal(t, 0, status.Code)
	require.Equal(t, secret, downloader.GetSessData())
	encoded, err := json.Marshal(status)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), secret)
	require.NotContains(t, string(encoded), "sessData")
}

func TestBuildVideoViewAPIURLSupportsAV(t *testing.T) {
	assert.Equal(t, "https://api.bilibili.com/x/web-interface/view?aid=170001", buildVideoViewAPIURL("av170001"))
	assert.Equal(t, "https://api.bilibili.com/x/web-interface/view?bvid=BV1xx411c7mD", buildVideoViewAPIURL("BV1xx411c7mD"))
}

func TestBilibiliAuthUsesInjectedHTTPDoer(t *testing.T) {
	doer := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		assert.Equal(t, "passport.bilibili.com", req.URL.Host)
		assert.NotEmpty(t, req.Header.Get("User-Agent"))
		assert.Equal(t, "SESSDATA=session-token", req.Header.Get("Cookie"))
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body: io.NopCloser(strings.NewReader(`{
				"code": 0,
				"message": "0",
				"data": {
					"url": "https://passport.bilibili.com/qrcode",
					"qrcode_key": "key"
				}
			}`)),
		}, nil
	})
	bd := NewBilibiliDownloader()
	bd.SetHTTPDoer(doer)
	bd.SetSessData("session-token")

	qr, err := bd.GetQRCode()
	require.NoError(t, err)
	require.Equal(t, "key", qr.QRCodeKey)
}

type roundTripFunc func(req *http.Request) (*http.Response, error)

func (f roundTripFunc) Do(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestSelectCurrentBangumiEpisode(t *testing.T) {
	episodes := []bangumiEpisode{
		{EpID: 1, SectionType: 1, Status: 2},
		{EpID: 2, SectionType: 0, Status: 13},
		{EpID: 3, SectionType: 0, Status: 2},
	}

	assert.Equal(t, 2, selectCurrentBangumiEpisode(episodes, 0))
	assert.Equal(t, 1, selectCurrentBangumiEpisode(episodes, 2))
}

func TestBangumiEpisodeToPart(t *testing.T) {
	part := bangumiEpisodeToPart(bangumiEpisode{
		AID:       123,
		BVID:      "BVbangumi",
		CID:       456,
		EpID:      789,
		Title:     "1",
		LongTitle: "开端",
		Badge:     "会员",
		Duration:  125000,
	}, 0)

	assert.Equal(t, int64(456), part.CID)
	assert.Equal(t, "第1话 开端", part.PartName)
	assert.Equal(t, 125, part.Duration)
	assert.Equal(t, "会员", part.Badge)
	assert.Equal(t, int64(789), part.EpID)
}

func TestStreamsFromPlayURLDataFallbackQualitiesAndDRM(t *testing.T) {
	data := playURLData{DRMTechType: 2}
	data.Dash.Video = []rawDashVideo{
		{
			ID:         80,
			BaseURL:    "https://video.example.com/80.m4s?kid=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			Bandwidth:  8_000_000,
			CodecID:    7,
			Codecs:     "avc1",
			Width:      1920,
			Height:     1080,
			BiliDRMURI: "uri:bili://bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		},
		{
			ID:        80,
			BaseURL:   "https://video.example.com/80-av1.m4s",
			Bandwidth: 7_000_000,
			CodecID:   13,
			Codecs:    "av01",
			Width:     1920,
			Height:    1080,
		},
	}
	data.Dash.Audio = []rawDashAudio{{BaseURL: "https://audio.example.com/a.m4s", Bandwidth: 200_000}}

	streams := streamsFromPlayURLData(data, 10)

	assert.Len(t, streams, 1)
	assert.Equal(t, 80, streams[0].Quality)
	assert.Equal(t, 13, streams[0].CodecID)
	assert.Equal(t, "https://video.example.com/80-av1.m4s", streams[0].VideoURL)
	assert.Equal(t, int64(9_000_000), streams[0].Size)
	assert.Equal(t, 2, streams[0].DRMTechType)
}

func TestBuildQualityNameFallbacks(t *testing.T) {
	tests := []struct {
		name           string
		quality        int
		supportFormats []supportFormatEntry
		acceptDesc     string
		want           string
	}{
		{
			name:           "support format description",
			quality:        120,
			supportFormats: []supportFormatEntry{{Quality: 120}},
			acceptDesc:     "4K 超清",
			want:           "4K 超清",
		},
		{
			name:    "fallback map",
			quality: 80,
			want:    "1080P",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildQualityName(tt.quality, tt.supportFormats, tt.acceptDesc)
			assert.Equal(t, tt.want, got)
		})
	}
}
