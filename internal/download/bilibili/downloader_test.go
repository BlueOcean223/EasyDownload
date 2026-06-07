package bilibili

import (
	"testing"

	"github.com/leanovate/gopter/gen"
	"github.com/leanovate/gopter/prop"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBilibiliDownloaderCreation(t *testing.T) {
	bd := NewBilibiliDownloader()

	require.NotNil(t, bd)
	require.NotNil(t, bd.rateLimiter)
}

func TestSetSessData(t *testing.T) {
	bd := NewBilibiliDownloader()

	bd.SetSessData("test_sessdata_value")

	assert.Equal(t, "test_sessdata_value", bd.GetSessData())
}

func TestSetFFmpegPath(t *testing.T) {
	bd := NewBilibiliDownloader()

	bd.SetFFmpegPath("/usr/bin/ffmpeg")

	assert.Equal(t, "/usr/bin/ffmpeg", bd.ffmpegPath)
}

func TestSessDataRoundTripProperty(t *testing.T) {
	properties := newProperties()
	sessDataGen := gen.RegexMatch(`[a-zA-Z0-9%*_-]{1,100}`)

	properties.Property("SESSDATA in-memory round trip preserves value", prop.ForAll(
		func(sessData string) bool {
			bd := NewBilibiliDownloader()
			bd.SetSessData(sessData)
			return bd.GetSessData() == sessData
		},
		sessDataGen,
	))

	properties.Property("empty SESSDATA in-memory round trip works", prop.ForAll(
		func(_ int) bool {
			bd := NewBilibiliDownloader()
			bd.SetSessData("")
			return bd.GetSessData() == ""
		},
		gen.Int(),
	))

	properties.TestingRun(t)
}
