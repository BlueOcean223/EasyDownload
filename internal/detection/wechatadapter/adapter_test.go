package wechatadapter

import (
	"context"
	"testing"
	"time"

	"EasyDownload/internal/detection"
	"EasyDownload/internal/download/wechat"

	"github.com/stretchr/testify/require"
)

func TestFromVideoInfoBuildsStablePrivateCandidates(t *testing.T) {
	observedAt := time.Date(2026, 7, 10, 8, 0, 0, 0, time.UTC)
	input := wechat.VideoInfo{
		ID: "content-1", URL: "https://finder.example/stodownload?token=one",
		Title: "Video", DecodeKey: "private-key", Width: 1920, Height: 1080,
		FileFormats: []string{"hd", "sd"},
		Specs:       []wechat.VideoSpec{{FileFormat: "hd", Width: 1280, Height: 720}},
	}

	first := FromVideoInfo(input, observedAt)
	require.Equal(t, detection.SourceWeChatProxy, first.Source)
	require.Len(t, first.Candidates, 3)
	require.True(t, first.Candidates[0].Default)
	require.Equal(t, "private-key", first.Candidates[2].DecodeKey)
	require.Equal(t, "https://channels.weixin.qq.com/", first.Candidates[1].Headers["Referer"])
	require.NotContains(t, first.ID, "content-1", "public detection ID must remain opaque")
	store := detection.NewMemoryStore(10)
	stored, err := store.Upsert(context.Background(), first)
	require.NoError(t, err)
	require.Len(t, stored.Snapshot.Videos[0].Candidates, 3,
		"explicit original/hd/sd renditions sharing a URL must remain selectable")

	input.URL = "https://finder.example/stodownload?token=two"
	second := FromVideoInfo(input, observedAt.Add(time.Minute))
	require.Equal(t, first.ID, second.ID, "platform content ID must survive signed URL rotation")
	require.Equal(t, first.Candidates[1].ID, second.Candidates[1].ID)

	withoutID := wechat.VideoInfo{URL: "https://finder.video.qq.com/7/stodownload?encfilekey=stable&m=media&token=one"}
	rotated := withoutID
	rotated.URL = "https://finder.video.qq.com/7/stodownload?encfilekey=stable&m=media&token=two"
	require.Equal(t,
		FromVideoInfo(withoutID, observedAt).ID,
		FromVideoInfo(rotated, observedAt).ID,
		"known WeChat volatile query parameters must not split one detection",
	)
}
