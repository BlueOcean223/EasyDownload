package xiaohongshu

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestXHSItemIsAlbum(t *testing.T) {
	cases := []struct {
		name   string
		item   *XHSItem
		expect bool
	}{
		{"nil item", nil, false},
		{"album type", &XHSItem{Type: "album"}, true},
		{"image type", &XHSItem{Type: "image"}, true},
		{"image_album type", &XHSItem{Type: "image_album"}, true},
		{"video type", &XHSItem{Type: "video"}, false},
		{"with images", &XHSItem{Type: "video", Images: []XHSImage{{URL: "a"}}}, false},
		{"empty images", &XHSItem{Type: "unknown", Images: []XHSImage{}}, false},
	}

	for _, tc := range cases {
		got := tc.item.IsAlbum()
		assert.Equal(t, tc.expect, got, tc.name)
	}
}

func TestXHSItemValidateSelectedImages(t *testing.T) {
	cases := []struct {
		name    string
		item    *XHSItem
		indices []int
		wantErr bool
	}{
		{"nil item", nil, nil, true},
		{"non-album", &XHSItem{Type: "video"}, []int{0}, false},
		{"album no images", &XHSItem{Type: "album", Images: []XHSImage{}}, nil, true},
		{"valid selection", &XHSItem{Type: "album", Images: []XHSImage{{URL: "a"}, {URL: "b"}}}, []int{0, 1}, false},
		{"out of range", &XHSItem{Type: "album", Images: []XHSImage{{URL: "a"}}}, []int{1}, true},
		{"negative index", &XHSItem{Type: "album", Images: []XHSImage{{URL: "a"}}}, []int{-1}, true},
		{"empty selection", &XHSItem{Type: "album", Images: []XHSImage{{URL: "a"}}}, []int{}, false},
	}

	for _, tc := range cases {
		err := tc.item.ValidateSelectedImages(tc.indices)
		if tc.wantErr {
			assert.Error(t, err, tc.name)
		} else {
			assert.NoError(t, err, tc.name)
		}
	}
}

func TestXHSItemSelectedCount(t *testing.T) {
	cases := []struct {
		name    string
		item    *XHSItem
		indices []int
		expect  int
	}{
		{"nil item", nil, nil, 0},
		{"non-album", &XHSItem{Type: "video"}, []int{0, 1}, 0},
		{"album empty selection", &XHSItem{Type: "album", Images: []XHSImage{{URL: "a"}, {URL: "b"}}}, []int{}, 2},
		{"album partial selection", &XHSItem{Type: "album", Images: []XHSImage{{URL: "a"}, {URL: "b"}, {URL: "c"}}}, []int{0, 2}, 2},
		{"album with dups", &XHSItem{Type: "album", Images: []XHSImage{{URL: "a"}, {URL: "b"}}}, []int{0, 0, 1}, 2},
		{"album out of range", &XHSItem{Type: "album", Images: []XHSImage{{URL: "a"}}}, []int{0, 1, 2}, 1},
	}

	for _, tc := range cases {
		got := tc.item.SelectedCount(tc.indices)
		assert.Equal(t, tc.expect, got, tc.name)
	}
}
