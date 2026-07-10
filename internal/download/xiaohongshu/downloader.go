package xiaohongshu

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"EasyDownload/internal/download/fetch"
	"EasyDownload/internal/utils"
)

var (
	// ErrStreamNotFound indicates no video stream was found.
	ErrStreamNotFound = errors.New("xhs stream not found")
	// ErrVideoTooLarge indicates the video exceeds the configured size limit.
	ErrVideoTooLarge = errors.New("xhs video exceeds size limit")
	// ErrNoProgressTimeout indicates the download stalled (no bytes received for too long).
	ErrNoProgressTimeout = errors.New("xhs download stalled")
)

const (
	// albumStateSaveBatch controls how often album download state is persisted.
	albumStateSaveBatch = 5
	// defaultMaxVideoSize is a safety cap for video downloads (2GiB).
	defaultMaxVideoSize = 2 * 1024 * 1024 * 1024
	// defaultNoProgressTimeout cancels downloads that receive no bytes for a while.
	defaultNoProgressTimeout = 2 * time.Minute
	// zipBufferSize controls buffered output size when writing ZIP archives.
	zipBufferSize = 256 * 1024
)

// Downloader handles downloading XiaoHongShu content.
type Downloader struct {
	httpClient        *http.Client
	userAgent         string
	referer           string
	maxVideoSize      int64
	noProgressTimeout time.Duration
}

// NewDownloader creates a new downloader with defaults.
func NewDownloader() *Downloader {
	return NewDownloaderWithClient(nil)
}

// NewDownloaderWithClient creates a new downloader with a custom HTTP client.
func NewDownloaderWithClient(client *http.Client) *Downloader {
	if client == nil {
		client = &http.Client{Timeout: 0}
	}
	return &Downloader{
		httpClient:        client,
		userAgent:         defaultUserAgent,
		referer:           defaultReferer,
		maxVideoSize:      defaultMaxVideoSize,
		noProgressTimeout: defaultNoProgressTimeout,
	}
}

// SetMaxVideoSize sets the maximum allowed video size in bytes.
// Set to 0 to disable the limit.
func (d *Downloader) SetMaxVideoSize(limitBytes int64) {
	if d == nil {
		return
	}
	if limitBytes < 0 {
		return
	}
	d.maxVideoSize = limitBytes
}

// SetNoProgressTimeout sets the maximum duration without receiving any bytes before aborting.
// Set to 0 to disable the timeout.
func (d *Downloader) SetNoProgressTimeout(timeout time.Duration) {
	if d == nil {
		return
	}
	if timeout < 0 {
		return
	}
	d.noProgressTimeout = timeout
}

func xhsFileBase(item *XHSItem) string {
	if item == nil {
		return "xhs"
	}
	author := utils.SanitizeFileName(item.Author, 40)
	title := utils.SanitizeFileName(item.Title, 60)
	id := utils.SanitizeFileName(item.ID, 40)

	base := strings.Trim(strings.Join([]string{author, title, id}, "_"), "_")
	base = utils.SanitizeFileName(base, 120)
	if base == "" {
		if id != "" {
			return id
		}
		return "xhs"
	}
	return base
}

func normalizeSelection(indices []int, n int) []int {
	if n <= 0 {
		return nil
	}
	if len(indices) == 0 {
		out := make([]int, n)
		for i := 0; i < n; i++ {
			out[i] = i
		}
		return out
	}
	seen := make(map[int]struct{}, len(indices))
	out := make([]int, 0, len(indices))
	for _, idx := range indices {
		if idx < 0 || idx >= n {
			continue
		}
		if _, ok := seen[idx]; ok {
			continue
		}
		seen[idx] = struct{}{}
		out = append(out, idx)
	}
	return out
}

func selectStream(item *XHSItem, quality string) *XHSStream {
	if item == nil {
		return nil
	}
	quality = strings.ToLower(strings.TrimSpace(quality))
	if quality != "" {
		for i := range item.Streams {
			s := &item.Streams[i]
			if strings.ToLower(strings.TrimSpace(s.QualityKey)) == quality && strings.TrimSpace(s.URL) != "" {
				return s
			}
		}
		for i := range item.Streams {
			s := &item.Streams[i]
			if strings.ToLower(strings.TrimSpace(s.QualityName)) == quality && strings.TrimSpace(s.URL) != "" {
				return s
			}
			if s.StreamType > 0 && fmt.Sprintf("%d", s.StreamType) == quality && strings.TrimSpace(s.URL) != "" {
				return s
			}
		}
	}
	for i := range item.Streams {
		s := &item.Streams[i]
		if strings.TrimSpace(s.URL) != "" {
			return s
		}
	}
	return nil
}

func selectStreamURL(item *XHSItem, quality string) string {
	stream := selectStream(item, quality)
	if stream == nil {
		return ""
	}
	return strings.TrimSpace(stream.URL)
}

// downloadAlbumZip downloads album images with resumable support.
// Uses AlbumState to track progress and temp files for each image.
func (d *Downloader) downloadAlbumZip(ctx context.Context, fetcher fetch.Fetcher, item *XHSItem, indices []int, destPath string, progressFn func(done int)) (err error) {
	if item == nil {
		return fmt.Errorf("nil xhs item")
	}
	if destPath == "" {
		return fmt.Errorf("empty dest path")
	}
	if len(item.Images) == 0 {
		return ErrNoImages
	}
	if len(indices) == 0 {
		return fmt.Errorf("empty indices")
	}
	if fetcher == nil {
		return fmt.Errorf("fetcher is required")
	}
	for _, idx := range indices {
		if idx < 0 || idx >= len(item.Images) {
			return fmt.Errorf("index out of range: %d", idx)
		}
	}

	if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
		return err
	}

	total := len(indices)
	tempDir := utils.AlbumTempDir(destPath)

	// Load existing state or create new one
	state, err := utils.LoadAlbumState(tempDir)
	if err != nil {
		return err
	}

	normalizedSelected := utils.NormalizeIndices(indices)
	reset := state == nil || state.Total != total || state.DestPath != destPath
	if !reset {
		reset = !utils.SameIntSlice(state.Indices, normalizedSelected)
	}
	if reset {
		_ = os.RemoveAll(tempDir)
		state = &utils.AlbumState{
			Total:    total,
			DestPath: destPath,
			TempDir:  tempDir,
			Indices:  normalizedSelected,
		}
		if err := utils.SaveAlbumState(state); err != nil {
			return err
		}
	}

	// Build target set for validation
	targetSet := make(map[int]struct{}, len(indices))
	for _, idx := range indices {
		targetSet[idx] = struct{}{}
	}

	// Validate completed items from state
	completed := make(map[int]struct{}, len(state.Completed))
	for _, idx := range state.Completed {
		if idx < 0 || idx >= len(item.Images) {
			continue
		}
		if _, ok := targetSet[idx]; !ok {
			continue
		}
		if albumAssetsComplete(tempDir, idx, item.Images[idx]) {
			completed[idx] = struct{}{}
		}
	}
	state.Completed = state.Completed[:0]
	for idx := range completed {
		state.Completed = append(state.Completed, idx)
	}
	sort.Ints(state.Completed)
	if err := utils.SaveAlbumState(state); err != nil {
		return err
	}

	completedCount := len(completed)
	if progressFn != nil {
		progressFn(completedCount)
	}

	// Already complete?
	if completedCount == total && utils.FileExists(destPath) {
		return nil
	}

	completedSinceSave := 0
	for _, idx := range indices {
		if _, ok := completed[idx]; ok {
			continue
		}
		if ctx.Err() != nil {
			_ = utils.SaveAlbumState(state)
			return ctx.Err()
		}

		img := item.Images[idx]
		raw := strings.TrimSpace(img.URL)
		if raw == "" {
			_ = utils.SaveAlbumState(state)
			return fmt.Errorf("empty image url: index %d", idx)
		}

		tempPath := utils.AlbumImageTempPath(tempDir, idx)
		result, err := d.downloadAlbumMedia(ctx, fetcher, raw, img.BackupURLs, tempPath, maxImageSize, nil)
		if err != nil {
			_ = utils.SaveAlbumState(state)
			return err
		}
		_ = removeResumeState(result.ResumeStatePath)

		if liveURL := strings.TrimSpace(img.LivePhotoURL); liveURL != "" {
			result, err := d.downloadAlbumMedia(ctx, fetcher, liveURL, nil, albumLivePhotoTempPath(tempDir, idx), maxLivePhotoSize, nil)
			if err != nil {
				_ = utils.SaveAlbumState(state)
				return err
			}
			_ = removeResumeState(result.ResumeStatePath)
		}

		completed[idx] = struct{}{}
		state.Completed = append(state.Completed, idx)
		completedCount++
		completedSinceSave++

		if progressFn != nil {
			progressFn(completedCount)
		}

		if completedSinceSave >= albumStateSaveBatch {
			sort.Ints(state.Completed)
			if err := utils.SaveAlbumState(state); err != nil {
				return err
			}
			completedSinceSave = 0
		}
	}

	sort.Ints(state.Completed)
	if err := utils.SaveAlbumState(state); err != nil {
		return err
	}

	// Package into zip
	if err := writeAlbumZip(destPath, tempDir, item.Images, indices); err != nil {
		_ = utils.SaveAlbumState(state)
		return err
	}

	// Cleanup temp directory
	_ = os.RemoveAll(tempDir)
	if progressFn != nil {
		progressFn(total)
	}
	return nil
}

func albumLivePhotoTempPath(tempDir string, index int) string {
	return filepath.Join(tempDir, fmt.Sprintf("%d.live", index))
}

func albumAssetsComplete(tempDir string, index int, img XHSImage) bool {
	if !utils.FileExists(utils.AlbumImageTempPath(tempDir, index)) {
		return false
	}
	if strings.TrimSpace(img.LivePhotoURL) == "" {
		return true
	}
	return utils.FileExists(albumLivePhotoTempPath(tempDir, index))
}

func mediaExt(raw string, fallback string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err == nil {
		if ext := path.Ext(u.Path); ext != "" && len(ext) <= 6 {
			return ext
		}
	}
	return fallback
}

func imageExt(raw string) string {
	return mediaExt(raw, ".jpg")
}

func videoExt(raw string) string {
	return mediaExt(raw, ".mp4")
}

// maxImageSize is the maximum allowed size for image downloads (50MB).
// This prevents memory exhaustion from malicious or oversized responses.
const maxImageSize = 50 * 1024 * 1024

// maxLivePhotoSize is the maximum allowed size for Live Photo video sidecars (200MB).
const maxLivePhotoSize = 200 * 1024 * 1024

func (d *Downloader) downloadAlbumMedia(ctx context.Context, fetcher fetch.Fetcher, primaryURL string, equivalentURLs []string, temporaryPath string, maxBytes int64, onProgress func(downloaded, total int64)) (fetch.FetchResult, error) {
	urls := collectDownloadURLs(primaryURL, equivalentURLs)
	if len(urls) == 0 {
		return fetch.FetchResult{}, fmt.Errorf("empty url")
	}
	if fetcher == nil {
		return fetch.FetchResult{}, fmt.Errorf("fetcher is required")
	}
	result, err := fetcher.Download(ctx, fetch.FetchRequest{
		URL:                  urls[0],
		EquivalentMirrorURLs: urls[1:],
		Headers:              d.downloadHeaders(),
		MaxBytes:             maxBytes,
		NoProgressTimeout:    d.noProgressTimeout,
		ResumePolicy: fetch.ResumePolicy{
			Enabled:                 true,
			RestartWhenRangeIgnored: true,
		},
		RetryPolicy: fetch.RetryPolicy{
			MaxAttempts:    3,
			InitialBackoff: 200 * time.Millisecond,
			MaxBackoff:     2 * time.Second,
		},
	}, fetch.Destination{
		TemporaryPath:   temporaryPath,
		ResumeStatePath: temporaryPath + ".resume.json",
	}, func(progress fetch.Progress) {
		if onProgress != nil {
			onProgress(progress.Downloaded, progress.Total)
		}
	})
	if err != nil {
		var fetchErr *fetch.Error
		if errors.As(err, &fetchErr) && fetchErr.Kind == fetch.ErrorTimeout {
			return fetch.FetchResult{}, fmt.Errorf("%w: %v", ErrNoProgressTimeout, fetchErr)
		}
		if errors.As(err, &fetchErr) && fetchErr.Kind == fetch.ErrorSizeLimit {
			if maxBytes == d.maxVideoSize {
				return fetch.FetchResult{}, fmt.Errorf("%w: %v", ErrVideoTooLarge, fetchErr.Err)
			}
			return fetch.FetchResult{}, fetchErr
		}
		return fetch.FetchResult{}, fmt.Errorf("xhs media download failed: %w", err)
	}
	return result, nil
}

func (d *Downloader) downloadHeaders() map[string]string {
	userAgent := strings.TrimSpace(d.userAgent)
	if userAgent == "" {
		userAgent = defaultUserAgent
	}
	referer := strings.TrimSpace(d.referer)
	if referer == "" {
		referer = defaultReferer
	}
	return map[string]string{
		"User-Agent": userAgent,
		"Referer":    referer,
	}
}

func removeResumeState(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove xhs resume state: %w", err)
	}
	return nil
}

// writeAlbumZip creates a ZIP archive from downloaded temp images.
func writeAlbumZip(destPath, tempDir string, images []XHSImage, indices []int) (err error) {
	if len(indices) == 0 {
		indices = make([]int, len(images))
		for i := range images {
			indices[i] = i
		}
	}

	files := make([]utils.ZipFile, 0, len(indices))
	for _, idx := range indices {
		if idx < 0 || idx >= len(images) {
			return fmt.Errorf("index out of range: %d", idx)
		}
		files = append(files, utils.ZipFile{
			Path: utils.AlbumImageTempPath(tempDir, idx),
			Name: fmt.Sprintf("%03d%s", idx+1, imageExt(images[idx].URL)),
		})
		if strings.TrimSpace(images[idx].LivePhotoURL) != "" {
			files = append(files, utils.ZipFile{
				Path: albumLivePhotoTempPath(tempDir, idx),
				Name: fmt.Sprintf("%03d_live%s", idx+1, videoExt(images[idx].LivePhotoURL)),
			})
		}
	}

	return utils.WriteZipAtomic(destPath, files, utils.WriteZipOptions{
		BufferSize: zipBufferSize,
		Overwrite:  true,
	})
}
