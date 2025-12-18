package douyin

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"EasyDownload/internal/downloader"
)

func TestDownloadVideoSuccess(t *testing.T) {
	data := []byte("video-data-123")
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Referer") != defaultReferer {
			t.Fatalf("unexpected referer: %s", r.Header.Get("Referer"))
		}
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(data)))
		_, _ = w.Write(data)
	}))
	defer ts.Close()

	item := &DouyinItem{
		Type: "video",
		Streams: []Stream{
			{QualityKey: "1080p", URL: ts.URL + "/video.mp4"},
		},
	}

	dl := NewDownloader()
	dl.httpClient = ts.Client()

	dest := filepath.Join(t.TempDir(), "video.mp4")
	var progress []float64
	if err := dl.DownloadVideo(item, "1080p", dest, func(p float64) { progress = append(progress, p) }); err != nil {
		t.Fatalf("DownloadVideo error: %v", err)
	}

	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("unexpected content: %q", string(got))
	}
	if len(progress) == 0 || progress[len(progress)-1] < 99 {
		t.Fatalf("progress not reported to completion: %v", progress)
	}
}

func TestDownloadVideoSelectFallback(t *testing.T) {
	item := &DouyinItem{
		Type: "video",
		Streams: []Stream{
			{QualityKey: "720p", URL: "http://example.com/720"},
			{QualityKey: "1080p", URL: "http://example.com/1080"},
		},
	}

	if s := selectStream(item, "1080p"); s == nil || s.QualityKey != "1080p" {
		t.Fatalf("expected 1080p stream, got %+v", s)
	}
	if s := selectStream(item, ""); s == nil || s.QualityKey != "720p" {
		t.Fatalf("expected fallback first stream, got %+v", s)
	}
	if s := selectStream(&DouyinItem{}, "1080p"); s != nil {
		t.Fatal("expected nil stream for empty list")
	}
}

func TestDownloadVideoForbidden(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer ts.Close()

	item := &DouyinItem{
		Type:    "video",
		Streams: []Stream{{QualityKey: "any", URL: ts.URL}},
	}

	dl := NewDownloader()
	dl.httpClient = ts.Client()

	dest := filepath.Join(t.TempDir(), "video.mp4")
	if err := dl.DownloadVideo(item, "any", dest, nil); err == nil {
		t.Fatal("expected forbidden error")
	}
}

func TestDownloadAlbumZipAndRetry(t *testing.T) {
	type rec struct {
		count int
		body  string
	}
	records := map[string]*rec{
		"/img1.jpg": {body: "one"},
		"/img2.jpg": {body: "two"},
	}
	var mu sync.Mutex

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		rec, ok := records[r.URL.Path]
		if !ok {
			mu.Unlock()
			w.WriteHeader(http.StatusNotFound)
			return
		}
		rec.count++
		count := rec.count
		body := rec.body
		mu.Unlock()

		if strings.Contains(r.URL.Path, "img1") && count == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	defer ts.Close()

	item := &DouyinItem{
		Type: "album",
		Images: []Image{
			{URL: ts.URL + "/img1.jpg"},
			{URL: ts.URL + "/img2.jpg"},
		},
	}

	dl := NewDownloader()
	dl.httpClient = ts.Client()

	dest := filepath.Join(t.TempDir(), "album.zip")
	var progress []float64
	if err := dl.DownloadAlbum(item, dest, func(p float64) { progress = append(progress, p) }); err != nil {
		t.Fatalf("DownloadAlbum error: %v", err)
	}

	zr, err := zip.OpenReader(dest)
	if err != nil {
		t.Fatalf("open zip: %v", err)
	}
	defer zr.Close()

	if len(zr.File) != 2 {
		t.Fatalf("expected 2 zip entries, got %d", len(zr.File))
	}
	var names []string
	for _, f := range zr.File {
		names = append(names, f.Name)
	}
	sort.Strings(names)
	expected := []string{"1_img1.jpg", "2_img2.jpg"}
	sort.Strings(expected)
	if !equalStrings(names, expected) {
		t.Fatalf("unexpected zip names: %v", names)
	}

	if len(progress) == 0 || progress[len(progress)-1] < 99 {
		t.Fatalf("progress not completed: %v", progress)
	}

	mu.Lock()
	retries := records["/img1.jpg"].count
	mu.Unlock()
	if retries < 2 {
		t.Fatalf("expected retry for img1, got %d attempts", retries)
	}
}

func TestDownloadAlbumNoImages(t *testing.T) {
	dl := NewDownloader()
	err := dl.DownloadAlbum(&DouyinItem{}, filepath.Join(t.TempDir(), "x.zip"), nil)
	if !errors.Is(err, ErrNoImages) {
		t.Fatalf("expected ErrNoImages, got %v", err)
	}
}

func TestBuildDownloadFuncVideo(t *testing.T) {
	data := []byte("hello")
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(data)))
		_, _ = w.Write(data)
	}))
	defer ts.Close()

	item := &DouyinItem{
		Type: "video",
		Streams: []Stream{
			{QualityKey: "720p", URL: ts.URL},
		},
	}

	dl := NewDownloader()
	dl.httpClient = ts.Client()

	destDir := t.TempDir()
	fn := dl.BuildDownloadFunc(item, "720p", destDir)

	var progressCalled bool
	var completedPath string
	err := fn(context.Background(), &downloader.DownloadTask{}, func(d, total int64) {
		progressCalled = true
		if total == 0 {
			t.Fatalf("expected non-zero total")
		}
	}, func(path string) {
		completedPath = path
	})
	if err != nil {
		t.Fatalf("download func error: %v", err)
	}
	if !progressCalled {
		t.Fatal("expected progress callback")
	}
	if completedPath == "" {
		t.Fatal("expected onComplete callback")
	}
	info, err := os.Stat(completedPath)
	if err != nil || info.Size() == 0 {
		t.Fatalf("expected output file, got err=%v size=%d", err, info.Size())
	}
}

func TestImageFileNameFallback(t *testing.T) {
	name := imageFileName(0, "")
	if !strings.HasPrefix(name, "1_image_") && !strings.HasPrefix(name, "1_image") {
		t.Fatalf("unexpected fallback name: %s", name)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestDownloadVideoNilItem(t *testing.T) {
	dl := NewDownloader()
	err := dl.DownloadVideo(nil, "1080p", "/tmp/test.mp4", nil)
	if err == nil {
		t.Fatal("expected error for nil item")
	}
}

func TestDownloadVideoEmptyDest(t *testing.T) {
	dl := NewDownloader()
	item := &DouyinItem{Streams: []Stream{{QualityKey: "720p", URL: "http://example.com"}}}
	err := dl.DownloadVideo(item, "720p", "", nil)
	if err == nil {
		t.Fatal("expected error for empty dest")
	}
}

func TestDownloadAlbumNilItem(t *testing.T) {
	dl := NewDownloader()
	err := dl.DownloadAlbum(nil, "/tmp/album.zip", nil)
	if err == nil {
		t.Fatal("expected error for nil item")
	}
}

func TestDownloadAlbumEmptyDest(t *testing.T) {
	dl := NewDownloader()
	item := &DouyinItem{Images: []Image{{URL: "http://example.com/img.jpg"}}}
	err := dl.DownloadAlbum(item, "", nil)
	if err == nil {
		t.Fatal("expected error for empty dest")
	}
}

func TestOriginFromRefererEmpty(t *testing.T) {
	origin := originFromReferer("")
	if origin != "https://www.douyin.com" {
		t.Fatalf("expected default origin, got %s", origin)
	}
}

func TestOriginFromRefererInvalid(t *testing.T) {
	origin := originFromReferer("not-a-url")
	if origin != "not-a-url" {
		t.Fatalf("expected passthrough for invalid url, got %s", origin)
	}
}

func TestSelectStreamNilItem(t *testing.T) {
	if s := selectStream(nil, "1080p"); s != nil {
		t.Fatal("expected nil for nil item")
	}
}

func TestDouyinFileBaseEmpty(t *testing.T) {
	item := &DouyinItem{}
	base := douyinFileBase(item)
	if base != "douyin" {
		t.Fatalf("expected 'douyin', got %s", base)
	}
}

func TestSanitizeFileComponent(t *testing.T) {
	result := sanitizeFileComponent("test:file?name")
	if strings.ContainsAny(result, ":?") {
		t.Fatalf("expected sanitized name, got %s", result)
	}
}

func TestSanitizeFileComponentEmpty(t *testing.T) {
	result := sanitizeFileComponent("")
	if result != "douyin" {
		t.Fatalf("expected 'douyin' for empty, got %s", result)
	}
}

func TestSanitizeFileComponentLong(t *testing.T) {
	long := strings.Repeat("a", 100)
	result := sanitizeFileComponent(long)
	if len(result) > 80 {
		t.Fatalf("expected truncated to 80, got %d", len(result))
	}
}

func TestFilterNonEmpty(t *testing.T) {
	result := filterNonEmpty([]string{"", "a", "  ", "b"})
	if len(result) != 2 || result[0] != "a" || result[1] != "b" {
		t.Fatalf("unexpected result: %v", result)
	}
}

func TestGetHTTPClientNil(t *testing.T) {
	dl := &Downloader{}
	client := dl.getHTTPClient()
	if client == nil {
		t.Fatal("expected non-nil client")
	}
}

func TestApplyHeadersDefaults(t *testing.T) {
	dl := &Downloader{}
	req, _ := http.NewRequest("GET", "http://example.com", nil)
	dl.applyHeaders(req)
	if req.Header.Get("User-Agent") != defaultUserAgent {
		t.Fatal("expected default UA")
	}
	if req.Header.Get("Referer") != defaultReferer {
		t.Fatal("expected default referer")
	}
}

func TestImageFileNameWithExt(t *testing.T) {
	name := imageFileName(2, "http://example.com/photo.png")
	if !strings.Contains(name, ".png") {
		t.Fatalf("expected .png extension, got %s", name)
	}
	if !strings.HasPrefix(name, "3_") {
		t.Fatalf("expected prefix '3_', got %s", name)
	}
}

func TestBuildDownloadFuncAlbum(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("image-data"))
	}))
	defer ts.Close()

	item := &DouyinItem{
		Type:   "album",
		Images: []Image{{URL: ts.URL + "/img.jpg"}},
	}

	dl := NewDownloader()
	dl.httpClient = ts.Client()

	destDir := t.TempDir()
	fn := dl.BuildDownloadFunc(item, "", destDir)

	var completedPath string
	err := fn(context.Background(), &downloader.DownloadTask{}, nil, func(path string) {
		completedPath = path
	})
	if err != nil {
		t.Fatalf("download func error: %v", err)
	}
	if !strings.HasSuffix(completedPath, ".zip") {
		t.Fatalf("expected .zip extension, got %s", completedPath)
	}
}

func TestBuildDownloadFuncWithFilePath(t *testing.T) {
	data := []byte("hello")
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(data)))
		_, _ = w.Write(data)
	}))
	defer ts.Close()

	item := &DouyinItem{
		Type:    "video",
		Streams: []Stream{{QualityKey: "720p", URL: ts.URL}},
	}

	dl := NewDownloader()
	dl.httpClient = ts.Client()

	destDir := t.TempDir()
	customPath := filepath.Join(destDir, "custom_video")
	fn := dl.BuildDownloadFunc(item, "720p", destDir)

	var completedPath string
	err := fn(context.Background(), &downloader.DownloadTask{FilePath: customPath}, nil, func(path string) {
		completedPath = path
	})
	if err != nil {
		t.Fatalf("download func error: %v", err)
	}
	if completedPath != customPath+".mp4" {
		t.Fatalf("expected custom path with .mp4, got %s", completedPath)
	}
}

func TestDownloadVideoTooManyRequests(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer ts.Close()

	item := &DouyinItem{
		Type:    "video",
		Streams: []Stream{{QualityKey: "any", URL: ts.URL}},
	}

	dl := NewDownloader()
	dl.httpClient = ts.Client()

	err := dl.DownloadVideo(item, "any", filepath.Join(t.TempDir(), "video.mp4"), nil)
	if err == nil {
		t.Fatal("expected rate limit error")
	}
}

func TestDownloadVideoUnexpectedStatus(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer ts.Close()

	item := &DouyinItem{
		Type:    "video",
		Streams: []Stream{{QualityKey: "any", URL: ts.URL}},
	}

	dl := NewDownloader()
	dl.httpClient = ts.Client()

	err := dl.DownloadVideo(item, "any", filepath.Join(t.TempDir(), "video.mp4"), nil)
	if err == nil {
		t.Fatal("expected error for 500 status")
	}
}
