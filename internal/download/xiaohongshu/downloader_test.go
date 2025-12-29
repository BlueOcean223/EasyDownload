package xiaohongshu

import (
	"archive/zip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"EasyDownload/internal/download"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDownloadAlbumZip(t *testing.T) {
	var hits1, hits2 atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/img1.jpg":
			hits1.Add(1)
			_, _ = w.Write([]byte("one"))
		case "/img2.png":
			hits2.Add(1)
			_, _ = w.Write([]byte("two"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	item := &XHSItem{
		Type:   "image",
		ID:     "note123",
		Title:  "hello",
		Author: "author",
		Images: []XHSImage{
			{URL: ts.URL + "/img1.jpg"},
			{URL: ts.URL + "/img2.png"},
		},
	}

	dl := NewDownloaderWithClient(ts.Client())
	outDir := t.TempDir()
	fn := dl.BuildDownloadFunc(item, nil, "", outDir)

	task := &downloader.DownloadTask{}
	var progressCalls int64
	var completePath string
	err := fn(context.Background(), task, func(downloaded, total int64) {
		progressCalls++
	}, func(p string) { completePath = p })
	require.NoError(t, err)
	require.NotEmpty(t, completePath)

	assert.Equal(t, int32(1), hits1.Load())
	assert.Equal(t, int32(1), hits2.Load())

	zipFiles := readZipFiles(t, completePath)
	assert.Len(t, zipFiles, 2)

	assert.NotZero(t, progressCalls)
	assert.Equal(t, 2, task.AlbumCompleted)
	assert.GreaterOrEqual(t, task.Progress, 99.9)
}

func TestDownloadVideoQualitySelect(t *testing.T) {
	var sdHits, hdHits atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v/sd.mp4":
			sdHits.Add(1)
			w.Header().Set("Content-Length", "2")
			_, _ = w.Write([]byte("sd"))
		case "/v/hd.mp4":
			hdHits.Add(1)
			w.Header().Set("Content-Length", "2")
			_, _ = w.Write([]byte("hd"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	item := &XHSItem{
		Type:   "video",
		ID:     "note123",
		Title:  "hello",
		Author: "author",
		Streams: []XHSStream{
			{QualityKey: "sd", QualityName: "SD", URL: ts.URL + "/v/sd.mp4"},
			{QualityKey: "hd", QualityName: "HD", URL: ts.URL + "/v/hd.mp4"},
		},
	}

	dl := NewDownloaderWithClient(ts.Client())
	outDir := t.TempDir()
	fn := dl.BuildDownloadFunc(item, nil, "hd", outDir)

	task := &downloader.DownloadTask{}
	err := fn(context.Background(), task, nil, nil)
	require.NoError(t, err)

	assert.Equal(t, int32(0), sdHits.Load())
	assert.NotZero(t, hdHits.Load())
}

func TestDownloadProgressCallback(t *testing.T) {
	payload := strings.Repeat("a", 1024)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1024")
		_, _ = w.Write([]byte(payload))
	}))
	defer ts.Close()

	item := &XHSItem{
		Type:   "video",
		ID:     "id",
		Title:  "t",
		Author: "a",
		Streams: []XHSStream{
			{QualityKey: "q", QualityName: "Q", URL: ts.URL + "/v.mp4"},
		},
	}

	dl := NewDownloaderWithClient(ts.Client())
	outDir := t.TempDir()
	fn := dl.BuildDownloadFunc(item, nil, "q", outDir)

	var calls int
	var lastDownloaded, lastTotal int64
	err := fn(context.Background(), &downloader.DownloadTask{}, func(downloaded, total int64) {
		calls++
		lastDownloaded, lastTotal = downloaded, total
	}, nil)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, calls, 1)
	assert.Equal(t, int64(1024), lastTotal)
	assert.Equal(t, int64(1024), lastDownloaded)
}

func TestDownloadFileNaming(t *testing.T) {
	item := &XHSItem{
		Author: `Au/tho:r*?"<>|`,
		Title:  "  Ti\"tle with  spaces\nand\tstuff  ",
		ID:     "noteid01234567890123",
	}

	base := xhsFileBase(item)
	require.NotEmpty(t, base)
	assert.LessOrEqual(t, len(base), 200)
	for _, bad := range []string{"/", "\\", ":", "*", "?", "\"", "<", ">", "|"} {
		assert.NotContains(t, base, bad)
	}
}

func TestDownloadErrors(t *testing.T) {
	dl := NewDownloader()

	{
		fn := dl.BuildDownloadFunc(nil, nil, "", "")
		err := fn(context.Background(), &downloader.DownloadTask{}, nil, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "nil xhs item")
	}

	{
		item := &XHSItem{Type: "album", Images: nil}
		fn := dl.BuildDownloadFunc(item, nil, "", t.TempDir())
		err := fn(context.Background(), &downloader.DownloadTask{}, nil, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no images")
	}

	{
		item := &XHSItem{Type: "video", Streams: nil}
		fn := dl.BuildDownloadFunc(item, nil, "", t.TempDir())
		err := fn(context.Background(), &downloader.DownloadTask{}, nil, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "stream not found")
	}
}

func TestNormalizeSelection(t *testing.T) {
	cases := []struct {
		indices []int
		n       int
		expect  int
	}{
		{nil, 5, 5},
		{[]int{}, 3, 3},
		{[]int{0, 2}, 5, 2},
		{[]int{0, 0, 1}, 3, 2},
		{[]int{-1, 5}, 3, 0},
	}

	for _, tc := range cases {
		got := normalizeSelection(tc.indices, tc.n)
		assert.Len(t, got, tc.expect, "indices=%v n=%d", tc.indices, tc.n)
	}
}

func TestSelectStreamURL(t *testing.T) {
	item := &XHSItem{
		Streams: []XHSStream{
			{QualityKey: "sd", URL: "http://example.com/sd.mp4"},
			{QualityKey: "hd", URL: "http://example.com/hd.mp4"},
		},
	}

	assert.Equal(t, "http://example.com/hd.mp4", selectStreamURL(item, "hd"))
	assert.Equal(t, "http://example.com/sd.mp4", selectStreamURL(item, "unknown"))
	assert.Empty(t, selectStreamURL(nil, ""))
}

func readZipFiles(t *testing.T, zipPath string) map[string][]byte {
	t.Helper()

	zr, err := zip.OpenReader(zipPath)
	require.NoError(t, err)
	defer zr.Close()

	out := make(map[string][]byte, len(zr.File))
	for _, f := range zr.File {
		rc, err := f.Open()
		require.NoError(t, err, "open zip entry %s", f.Name)
		data, readErr := io.ReadAll(rc)
		_ = rc.Close()
		require.NoError(t, readErr, "read zip entry %s", f.Name)
		out[f.Name] = data
	}
	return out
}

func TestImageExt(t *testing.T) {
	cases := []struct {
		url    string
		expect string
	}{
		{"https://example.com/img.jpg", ".jpg"},
		{"https://example.com/img.png", ".png"},
		{"https://example.com/img", ".jpg"},
		{"https://example.com/path/to/file.gif?query=1", ".gif"},
	}

	for _, tc := range cases {
		got := imageExt(tc.url)
		assert.Equal(t, tc.expect, got, "imageExt(%q)", tc.url)
	}
}

func TestXhsFileBaseEmpty(t *testing.T) {
	item := &XHSItem{}
	base := xhsFileBase(item)
	assert.Equal(t, "xhs", base)
}

func TestDownloadAlbumPartialSelection(t *testing.T) {
	var hits atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte("img"))
	}))
	defer ts.Close()

	item := &XHSItem{
		Type:   "album",
		ID:     "note123",
		Title:  "hello",
		Author: "author",
		Images: []XHSImage{
			{URL: ts.URL + "/img1.jpg"},
			{URL: ts.URL + "/img2.jpg"},
			{URL: ts.URL + "/img3.jpg"},
		},
	}

	dl := NewDownloaderWithClient(ts.Client())
	outDir := t.TempDir()
	fn := dl.BuildDownloadFunc(item, []int{0, 2}, "", outDir)

	task := &downloader.DownloadTask{}
	err := fn(context.Background(), task, nil, nil)
	require.NoError(t, err)

	assert.Equal(t, int32(2), hits.Load())
}

func TestDownloadWithTaskFilePath(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "4")
		_, _ = w.Write([]byte("data"))
	}))
	defer ts.Close()

	item := &XHSItem{
		Type:    "video",
		ID:      "id",
		Title:   "t",
		Author:  "a",
		Streams: []XHSStream{{QualityKey: "q", URL: ts.URL + "/v.mp4"}},
	}

	dl := NewDownloaderWithClient(ts.Client())
	outDir := t.TempDir()
	customPath := filepath.Join(outDir, "custom.mp4")

	task := &downloader.DownloadTask{FilePath: customPath}
	fn := dl.BuildDownloadFunc(item, nil, "q", outDir)
	var completePath string
	err := fn(context.Background(), task, nil, func(p string) { completePath = p })
	require.NoError(t, err)

	assert.Equal(t, customPath, completePath)

	_, err = os.Stat(customPath)
	require.NoError(t, err)
}

func TestDownloadVideoSizeLimit(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "4")
		_, _ = w.Write([]byte("data"))
	}))
	defer ts.Close()

	item := &XHSItem{
		Type:   "video",
		ID:     "id",
		Title:  "t",
		Author: "a",
		Streams: []XHSStream{
			{QualityKey: "q", QualityName: "Q", URL: ts.URL + "/v.mp4"},
		},
	}

	dl := NewDownloaderWithClient(ts.Client())
	dl.SetMaxVideoSize(3) // smaller than 4 bytes
	outDir := t.TempDir()
	fn := dl.BuildDownloadFunc(item, nil, "q", outDir)

	err := fn(context.Background(), &downloader.DownloadTask{}, nil, nil)
	require.ErrorIs(t, err, ErrVideoTooLarge)
}
