package xiaohongshu

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"EasyDownload/internal/download"
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

// BuildDownloadFunc creates a download function for the DownloadManager.
func (d *Downloader) BuildDownloadFunc(item *XHSItem, selectedImages []int, quality string, outputDir string) downloader.DownloadFunc {
	return func(ctx context.Context, task *downloader.DownloadTask, onProgress func(downloaded, total int64), onComplete func(outputPath string)) error {
		if item == nil {
			return fmt.Errorf("nil xhs item")
		}

		isAlbum := item.IsAlbum()
		ext := ".mp4"
		if isAlbum {
			ext = ".zip"
		}

		baseName := xhsFileBase(item)
		destPath := filepath.Join(outputDir, baseName+ext)
		if task != nil && task.FilePath != "" {
			destPath = task.FilePath
			if filepath.Ext(destPath) == "" {
				destPath += ext
			}
		}

		if isAlbum {
			if err := item.ValidateSelectedImages(selectedImages); err != nil {
				return err
			}
			indices := normalizeSelection(selectedImages, len(item.Images))
			total := int64(len(indices))
			progressFn := func(done int) {
				if task != nil && total > 0 {
					task.AlbumCompleted = done
					task.Progress = float64(done) / float64(total) * 100
				}
				if onProgress != nil {
					onProgress(int64(done), total)
				}
			}
			if err := d.downloadAlbumZip(ctx, item, indices, destPath, progressFn); err != nil {
				return err
			}
			if task != nil {
				task.AlbumCompleted = int(total)
				task.Progress = 100
			}
		} else {
			streamURL := selectStreamURL(item, quality)
			if streamURL == "" {
				return ErrStreamNotFound
			}
			if err := d.downloadFile(ctx, streamURL, destPath, onProgress); err != nil {
				return err
			}
		}

		if onComplete != nil {
			onComplete(destPath)
		}
		return nil
	}
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

func selectStreamURL(item *XHSItem, quality string) string {
	if item == nil {
		return ""
	}
	quality = strings.ToLower(strings.TrimSpace(quality))
	if quality != "" {
		for _, s := range item.Streams {
			if strings.ToLower(strings.TrimSpace(s.QualityKey)) == quality && strings.TrimSpace(s.URL) != "" {
				return strings.TrimSpace(s.URL)
			}
		}
	}
	for _, s := range item.Streams {
		if strings.TrimSpace(s.URL) != "" {
			return strings.TrimSpace(s.URL)
		}
	}
	return ""
}

// downloadAlbumZip downloads album images with resumable support.
// Uses AlbumState to track progress and temp files for each image.
func (d *Downloader) downloadAlbumZip(ctx context.Context, item *XHSItem, indices []int, destPath string, progressFn func(done int)) (err error) {
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
		if utils.FileExists(utils.AlbumImageTempPath(tempDir, idx)) {
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

		raw := strings.TrimSpace(item.Images[idx].URL)
		if raw == "" {
			_ = utils.SaveAlbumState(state)
			return fmt.Errorf("empty image url: index %d", idx)
		}

		data, dlErr := d.downloadBytes(ctx, raw)
		if dlErr != nil {
			_ = utils.SaveAlbumState(state)
			return dlErr
		}

		tempPath := utils.AlbumImageTempPath(tempDir, idx)
		if err := os.WriteFile(tempPath, data, 0644); err != nil {
			_ = utils.SaveAlbumState(state)
			return err
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

func imageExt(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err == nil {
		if ext := path.Ext(u.Path); ext != "" && len(ext) <= 6 {
			return ext
		}
	}
	return ".jpg"
}

// maxImageSize is the maximum allowed size for image downloads (50MB).
// This prevents memory exhaustion from malicious or oversized responses.
const maxImageSize = 50 * 1024 * 1024

func (d *Downloader) downloadBytes(ctx context.Context, rawURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", d.userAgent)
	req.Header.Set("Referer", d.referer)
	resp, err := d.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	// Limit response body size to prevent memory exhaustion
	limitedReader := io.LimitReader(resp.Body, maxImageSize+1)
	data, err := io.ReadAll(limitedReader)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxImageSize {
		return nil, fmt.Errorf("image size exceeds limit (%d bytes)", maxImageSize)
	}
	return data, nil
}

// downloadFile downloads a video file with resumable support.
// Uses HTTP Range requests to continue from where it left off.
func (d *Downloader) downloadFile(ctx context.Context, rawURL string, destPath string, onProgress func(downloaded, total int64)) error {
	headers := map[string]string{
		"User-Agent": d.userAgent,
		"Referer":    d.referer,
	}

	downloaded, total, err := downloader.DownloadFileResumable(ctx, d.httpClient, rawURL, destPath, downloader.ResumableDownloadOptions{
		Headers:           headers,
		MaxRangeRetries:   3,
		MaxBytes:          d.maxVideoSize,
		NoProgressTimeout: d.noProgressTimeout,
	}, onProgress)
	if err != nil {
		var sizeErr *downloader.SizeLimitExceededError
		if errors.As(err, &sizeErr) {
			if sizeErr.Reason == "total" && sizeErr.Total > 0 {
				return fmt.Errorf("%w: total=%d limit=%d", ErrVideoTooLarge, sizeErr.Total, sizeErr.Limit)
			}
			return fmt.Errorf("%w: downloaded=%d limit=%d", ErrVideoTooLarge, sizeErr.Downloaded, sizeErr.Limit)
		}

		var stallErr *downloader.NoProgressTimeoutError
		if errors.As(err, &stallErr) {
			return fmt.Errorf("%w: timeout=%s", ErrNoProgressTimeout, stallErr.Timeout)
		}
		return err
	}

	if onProgress != nil {
		onProgress(downloaded, total)
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
	}

	return utils.WriteZipAtomic(destPath, files, utils.WriteZipOptions{
		BufferSize: zipBufferSize,
		Overwrite:  true,
	})
}
