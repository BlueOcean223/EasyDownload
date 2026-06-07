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
