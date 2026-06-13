package bilibili

import (
	"testing"

	"github.com/stretchr/testify/assert"
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

func TestBuildVideoViewAPIURLSupportsAV(t *testing.T) {
	assert.Equal(t, "https://api.bilibili.com/x/web-interface/view?aid=170001", buildVideoViewAPIURL("av170001"))
	assert.Equal(t, "https://api.bilibili.com/x/web-interface/view?bvid=BV1xx411c7mD", buildVideoViewAPIURL("BV1xx411c7mD"))
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
