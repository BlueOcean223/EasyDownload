package bilibili

import (
	"fmt"
	"strings"
	"testing"

	"github.com/leanovate/gopter/gen"
	"github.com/leanovate/gopter/prop"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseURL(t *testing.T) {
	bd := NewBilibiliDownloader()

	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{
			name:  "bv url",
			input: "https://www.bilibili.com/video/BV1xx411c7mD",
			want:  "BV1xx411c7mD",
		},
		{
			name:  "bv url with query",
			input: "https://www.bilibili.com/video/BV1xx411c7mD?p=1",
			want:  "BV1xx411c7mD",
		},
		{
			name:  "av url",
			input: "https://www.bilibili.com/video/av170001",
			want:  "av170001",
		},
		{
			name:  "av url with query",
			input: "https://www.bilibili.com/video/av170001?p=2",
			want:  "av170001",
		},
		{
			name:  "bare bv id",
			input: "BV1xx411c7mD",
			want:  "BV1xx411c7mD",
		},
		{
			name:    "other site",
			input:   "https://www.youtube.com/watch?v=xxx",
			wantErr: true,
		},
		{
			name:    "invalid input",
			input:   "invalid_url",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := bd.ParseURL(tt.input)
			if tt.wantErr {
				require.Error(t, err)
				assert.Empty(t, got)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestParseBangumiURL(t *testing.T) {
	bd := NewBilibiliDownloader()

	tests := []struct {
		name     string
		input    string
		wantKind string
		wantID   string
		wantErr  bool
	}{
		{
			name:     "episode url",
			input:    "https://www.bilibili.com/bangumi/play/ep3854801",
			wantKind: "ep",
			wantID:   "3854801",
		},
		{
			name:     "season url",
			input:    "https://www.bilibili.com/bangumi/play/ss28747?from=search",
			wantKind: "season",
			wantID:   "28747",
		},
		{
			name:     "media url",
			input:    "https://www.bilibili.com/bangumi/media/md28223043",
			wantKind: "media",
			wantID:   "28223043",
		},
		{
			name:     "bare ep id",
			input:    "ep733316",
			wantKind: "ep",
			wantID:   "733316",
		},
		{
			name:    "ordinary video",
			input:   "https://www.bilibili.com/video/BV1xx411c7mD",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotKind, gotID, err := bd.ParseBangumiURL(tt.input)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantKind, gotKind)
			assert.Equal(t, tt.wantID, gotID)
		})
	}
}

func TestParseURLBVProperty(t *testing.T) {
	properties := newProperties()
	bvGen := gen.RegexMatch(`BV[a-zA-Z0-9]{10}`)

	properties.Property("extracts BV ID from URL", prop.ForAll(
		func(bvid string) bool {
			bd := NewBilibiliDownloader()
			got, err := bd.ParseURL("https://www.bilibili.com/video/" + bvid)
			return err == nil && got == bvid
		},
		bvGen,
	))

	properties.Property("extracts BV ID from URL with query params", prop.ForAll(
		func(bvid string, page int) bool {
			bd := NewBilibiliDownloader()
			url := fmt.Sprintf("https://www.bilibili.com/video/%s?p=%d", bvid, page)
			got, err := bd.ParseURL(url)
			return err == nil && got == bvid
		},
		bvGen,
		gen.IntRange(1, 9),
	))

	properties.TestingRun(t)
}

func TestIsBilibiliURL(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{
			name:  "www bilibili video",
			input: "https://www.bilibili.com/video/BV1xx411c7mD",
			want:  true,
		},
		{
			name:  "apex bilibili video",
			input: "https://bilibili.com/video/BV1xx411c7mD",
			want:  true,
		},
		{
			name:  "short link",
			input: "https://b23.tv/abc123",
			want:  true,
		},
		{
			name:  "bangumi episode",
			input: "https://www.bilibili.com/bangumi/play/ep3854801",
			want:  true,
		},
		{
			name:  "youtube",
			input: "https://www.youtube.com/watch?v=xxx",
			want:  false,
		},
		{
			name:  "example",
			input: "https://example.com",
			want:  false,
		},
		{
			name:  "lookalike domain",
			input: "https://bilibili.com.evil.example/video/BV1xx411c7mD",
			want:  false,
		},
		{
			name:  "schemeless host",
			input: "www.bilibili.com/video/BV1xx411c7mD",
			want:  true,
		},
		{
			name: "empty",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, IsBilibiliURL(tt.input))
		})
	}
}

func TestIsBilibiliURLProperty(t *testing.T) {
	properties := newProperties()

	properties.Property("detects bilibili.com URLs", prop.ForAll(
		func(path string) bool {
			return IsBilibiliURL("https://www.bilibili.com/" + path)
		},
		gen.AlphaString(),
	))

	properties.Property("detects b23.tv URLs", prop.ForAll(
		func(path string) bool {
			return IsBilibiliURL("https://b23.tv/" + path)
		},
		gen.AlphaString(),
	))

	properties.Property("does not detect unrelated domains", prop.ForAll(
		func(domain string) bool {
			domain = strings.ToLower(domain)
			return !IsBilibiliURL("https://" + domain + ".example/video")
		},
		gen.AlphaString().SuchThat(func(s string) bool {
			s = strings.ToLower(s)
			return s != "" && !strings.Contains(s, "bilibili") && !strings.Contains(s, "b23")
		}),
	))

	properties.TestingRun(t)
}
