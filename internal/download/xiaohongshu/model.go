// Package xiaohongshu provides functionality for downloading content from XiaoHongShu (RED).
package xiaohongshu

import (
	"errors"
	"fmt"
	"strings"
)

var (
	ErrInvalidNoteID = errors.New("invalid xiaohongshu note id")
	ErrNoImages      = errors.New("xiaohongshu note has no images")
)

// XHSItem represents a parsed XiaoHongShu note with all relevant metadata.
type XHSItem struct {
	Type   string // "video" or "image"
	ID     string
	Title  string
	Desc   string
	Cover  string
	Author string

	AuthorID     string
	AuthorAvatar string
	Timestamp    int64

	IPLocation   string
	Tags         []XHSTag
	InteractInfo XHSInteractInfo

	Streams []XHSStream
	Images  []XHSImage
}

// XHSTag represents a topic/tag attached to a note.
type XHSTag struct {
	ID   string
	Name string
	Type string
}

// XHSInteractInfo contains interaction counters exposed by the note page.
// Counts are kept as strings because XiaoHongShu may return compact values such as "1万+".
type XHSInteractInfo struct {
	LikedCount     string
	CollectedCount string
	CommentCount   string
	ShareCount     string
}

// XHSImage represents an image in an album.
type XHSImage struct {
	URL          string
	BackupURLs   []string
	Width        int
	Height       int
	TraceId      string
	FileID       string
	LivePhoto    bool
	LivePhotoURL string
}

// XHSStream represents a downloadable video stream.
type XHSStream struct {
	QualityKey  string
	QualityName string
	Width       int
	Height      int
	URL         string
	BackupURLs  []string
	Size        int64
	Format      string

	FPS           int
	VideoCodec    string
	VideoBitrate  int64
	AudioCodec    string
	AudioBitrate  int64
	StreamDesc    string
	StreamType    int
	Weight        int
	Duration      int64
	DefaultStream int
	HDRType       int
	Rotate        int
}

// IsAlbum returns true if this item is an image album.
func (i *XHSItem) IsAlbum() bool {
	if i == nil {
		return false
	}
	t := strings.ToLower(strings.TrimSpace(i.Type))
	if t == "video" {
		return false
	}
	if t == "image" || t == "album" || t == "image_album" {
		return true
	}
	return len(i.Images) > 0 && len(i.Streams) == 0
}

// ValidateSelectedImages validates the selected image indices.
func (i *XHSItem) ValidateSelectedImages(indices []int) error {
	if i == nil {
		return fmt.Errorf("nil item")
	}
	if !i.IsAlbum() {
		return nil
	}
	if len(i.Images) == 0 {
		return ErrNoImages
	}
	for _, idx := range indices {
		if idx < 0 || idx >= len(i.Images) {
			return fmt.Errorf("index out of range: %d", idx)
		}
	}
	return nil
}

// SelectedCount returns the count of selected images.
func (i *XHSItem) SelectedCount(indices []int) int {
	if i == nil || !i.IsAlbum() {
		return 0
	}
	if len(indices) == 0 {
		return len(i.Images)
	}
	seen := make(map[int]struct{}, len(indices))
	for _, idx := range indices {
		if idx < 0 || idx >= len(i.Images) {
			continue
		}
		seen[idx] = struct{}{}
	}
	return len(seen)
}
