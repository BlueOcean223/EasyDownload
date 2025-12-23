package douyin

import (
	"archive/zip"
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
	"strconv"
	"strings"
	"sync"
	"time"

	"EasyDownload/internal/downloader"
	"EasyDownload/internal/logger"
	"EasyDownload/internal/utils"
)

// Default values for download operations.
const (
	// defaultReferer is used in HTTP headers to mimic browser requests.
	defaultReferer = "https://www.douyin.com/"
	// albumConcurrency is the number of concurrent image downloads for albums.
	albumConcurrency = 4
	// albumMaxRetry is the maximum retry attempts for failed image downloads.
	albumMaxRetry = 3
	// albumStateSaveBatch controls how often album download state is persisted.
	// State is saved every N completed images to reduce IO overhead.
	albumStateSaveBatch = 5
)

// Error definitions for download operations.
var (
	// ErrStreamNotFound indicates the requested video quality stream was not found.
	ErrStreamNotFound = errors.New("douyin stream not found")
	// ErrNoImages indicates an album has no images to download.
	ErrNoImages = errors.New("douyin album has no images")
)

// Downloader handles downloading Douyin videos and albums.
// It supports resumable downloads, multi-part downloads for videos,
// and concurrent image downloads for albums with ZIP packaging.
type Downloader struct {
	httpClient *http.Client // HTTP client for download requests
	userAgent  string       // User-Agent header for requests
	referer    string       // Referer header for requests
}

// NewDownloader creates a new Downloader with default settings.
func NewDownloader() *Downloader {
	return &Downloader{
		httpClient: &http.Client{Timeout: 0},
		userAgent:  defaultUserAgent,
		referer:    defaultReferer,
	}
}

// DownloadVideo downloads a video at the specified quality to the destination path.
// The qualityKey should match one of the available stream quality keys (e.g., "720p", "1080p").
// If the quality is not found, it falls back to the first available stream.
// Progress is reported via the progressFn callback (0-100 percentage).
func (d *Downloader) DownloadVideo(item *DouyinItem, qualityKey string, destPath string, progressFn func(float64)) error {
	return d.downloadVideoWithContext(context.Background(), item, qualityKey, destPath, progressFn, nil)
}

// downloadVideoWithContext is the internal implementation for video downloads.
// It supports context cancellation and provides both percentage and byte-level progress.
// Uses multi-part download for large files if the server supports range requests.
func (d *Downloader) downloadVideoWithContext(ctx context.Context, item *DouyinItem, qualityKey string, destPath string, progressFn func(float64), bytesFn func(downloaded, total int64)) error {
	if item == nil {
		return fmt.Errorf("nil douyin item")
	}
	if destPath == "" {
		return fmt.Errorf("empty dest path")
	}

	stream := selectStream(item, qualityKey)
	if stream == nil {
		return ErrStreamNotFound
	}

	headers := d.effectiveHeaders()
	client := d.getHTTPClient()

	// Ensure the destination directory exists.
	if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
		return err
	}

	// Try multi-part download for large files (faster for big videos).
	md := downloader.NewMultipartDownloader()
	md.SetHeaders(headers)

	checkResult := md.CheckRangeSupport(ctx, stream.URL)
	totalSize := checkResult.ContentLength
	supportsRange := checkResult.Error == nil && checkResult.SupportsRange

	// Use multi-part download if server supports range requests and file is large enough.
	if supportsRange && totalSize > downloader.MinSizeForMultipart {
		result := md.Download(ctx, stream.URL, destPath, totalSize, func(downloaded, total int64) {
			if bytesFn != nil {
				bytesFn(downloaded, total)
			}
			if progressFn != nil && total > 0 {
				progressFn(float64(downloaded) / float64(total) * 100)
			}
		})
		if result.Error == nil {
			if progressFn != nil {
				progressFn(100)
			}
			if bytesFn != nil && totalSize > 0 {
				bytesFn(totalSize, totalSize)
			}
			return nil
		}
		// Fallback to sequential download if multi-part fails.
	}

	// Use sequential download for small files or when range requests aren't supported.
	return d.downloadVideoSequential(ctx, client, stream.URL, destPath, headers, totalSize, progressFn, bytesFn)
}

// DownloadAlbum downloads all images from an album and packages them into a ZIP file.
// Progress is reported via the progressFn callback (0-100 percentage).
// Supports resumable downloads - if interrupted, progress is preserved and can be resumed.
func (d *Downloader) DownloadAlbum(item *DouyinItem, destPath string, progressFn func(float64)) error {
	return d.downloadAlbumWithContext(context.Background(), item, destPath, progressFn, nil)
}

// downloadAlbumWithContext downloads all album images with context support.
func (d *Downloader) downloadAlbumWithContext(ctx context.Context, item *DouyinItem, destPath string, progressFn func(float64), bytesFn func(downloaded, total int64)) error {
	return d.downloadImagesCore(ctx, item, nil, destPath, progressFn, bytesFn)
}

// downloadImagesCore is the unified core method for downloading album images.
// It supports both full album downloads (indices == nil) and selective downloads.
//
// Album Download Process:
//  1. Load or create download state from temp directory
//  2. Download images concurrently (up to albumConcurrency at once)
//  3. Save progress periodically to support resume
//  4. Package completed images into a ZIP file
//  5. Clean up temp directory
//
// The state file tracks which images have been downloaded, allowing resume
// after interruption without re-downloading completed images.
func (d *Downloader) downloadImagesCore(
	ctx context.Context,
	item *DouyinItem,
	indices []int,
	destPath string,
	progressFn func(float64),
	bytesFn func(downloaded, total int64),
) error {
	if item == nil {
		return fmt.Errorf("nil douyin item")
	}
	if len(item.Images) == 0 {
		return ErrNoImages
	}
	if destPath == "" {
		return fmt.Errorf("empty dest path")
	}
	if indices != nil && len(indices) == 0 {
		return fmt.Errorf("empty indices")
	}

	// Determine which indices to download.
	// nil means all images; otherwise use the provided indices.
	selected := indices
	if selected == nil {
		selected = make([]int, len(item.Images))
		for i := range item.Images {
			selected[i] = i
		}
	} else {
		selected = append([]int(nil), indices...)
		for _, idx := range selected {
			if idx < 0 || idx >= len(item.Images) {
				return fmt.Errorf("index out of range: %d", idx)
			}
		}
	}

	client := d.getHTTPClient()
	if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
		return err
	}

	total := len(selected)
	tempDir := utils.AlbumTempDir(destPath)

	// Load existing download state or create new one.
	// State tracks which images have been completed for resume support.
	state, err := utils.LoadAlbumState(tempDir)
	if err != nil {
		return err
	}

	// Normalize indices for consistent comparison (order-independent).
	normalizedSelected := utils.NormalizeIndices(selected)

	// Check if state needs reset (different dest, different selection, etc.).
	reset := state == nil || state.Total != total || state.DestPath != destPath
	if !reset {
		if indices == nil {
			reset = len(state.Indices) != 0
		} else {
			reset = !utils.SameIntSlice(state.Indices, normalizedSelected)
		}
	}
	if reset {
		if err := os.RemoveAll(tempDir); err != nil {
			logger.Warn("[Douyin] failed to remove temp dir %s: %v", tempDir, err)
		}
		state = &utils.AlbumState{
			Total:    total,
			DestPath: destPath,
			TempDir:  tempDir,
		}
		if indices != nil {
			state.Indices = normalizedSelected
		}
		if err := utils.SaveAlbumState(state); err != nil {
			return err
		}
	}

	// Build target set for validation.
	targetSet := make(map[int]struct{}, len(selected))
	for _, idx := range selected {
		targetSet[idx] = struct{}{}
	}

	// Validate completed items from state - verify temp files still exist.
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
	sort.Ints(state.Completed) // Ensure stable ordering for reproducible state files.
	if err := utils.SaveAlbumState(state); err != nil {
		return err
	}

	// Track actual downloaded bytes (not image count) for accurate progress.
	var downloadedBytes int64
	completedCount := len(completed)

	// Sum up bytes from already completed temp files.
	for idx := range completed {
		if fi, err := os.Stat(utils.AlbumImageTempPath(tempDir, idx)); err == nil {
			downloadedBytes += fi.Size()
		}
	}

	// Report initial progress (from resumed state).
	if bytesFn != nil {
		bytesFn(downloadedBytes, 0) // total=0 means unknown
	}
	if progressFn != nil && total > 0 {
		progressFn(float64(completedCount) / float64(total) * 100)
	}

	// Already complete? Skip to final packaging.
	if completedCount == total && utils.FileExists(destPath) {
		if progressFn != nil {
			progressFn(100)
		}
		if bytesFn != nil {
			bytesFn(downloadedBytes, downloadedBytes) // final: downloaded == total
		}
		return nil
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Concurrent download coordination.
	var wg sync.WaitGroup
	sem := make(chan struct{}, albumConcurrency) // Limit concurrent downloads.
	var firstErr error
	var mu sync.Mutex
	completedSinceSave := 0

	// setErr captures the first error and cancels remaining downloads.
	setErr := func(err error) {
		if err == nil {
			return
		}
		mu.Lock()
		if firstErr == nil {
			firstErr = err
			cancel()
		}
		mu.Unlock()
	}

	// Launch concurrent media downloads (images or videos).
	for _, idx := range selected {
		if _, ok := completed[idx]; ok {
			continue // Already downloaded in previous session.
		}
		idx := idx // Capture for goroutine.
		img := item.Images[idx]

		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-sem }()

			// Determine download URL: prefer VideoURL for video items, otherwise use image URL.
			downloadURL := strings.TrimSpace(img.URL)
			isVideo := false
			if v := strings.TrimSpace(img.VideoURL); v != "" {
				downloadURL = v
				isVideo = true
			}
			if downloadURL == "" {
				setErr(fmt.Errorf("empty album media url: index %d", idx))
				return
			}

			tempPath := utils.AlbumImageTempPath(tempDir, idx)
			var dataSize int64

			if isVideo {
				// Download video using sequential downloader (handles large files better).
				headers := d.effectiveHeaders()
				if err := d.downloadVideoSequential(ctx, client, downloadURL, tempPath, headers, 0, nil, nil); err != nil {
					setErr(err)
					return
				}
				if fi, err := os.Stat(tempPath); err == nil {
					dataSize = fi.Size()
				}
			} else {
				// Download image with retry.
				data, err := d.downloadImageWithRetry(ctx, client, downloadURL)
				if err != nil {
					setErr(err)
					return
				}
				dataSize = int64(len(data))
				if err := os.WriteFile(tempPath, data, 0644); err != nil {
					setErr(err)
					return
				}
			}

			// Update state under lock, but defer IO outside lock.
			var stateCopy *utils.AlbumState
			mu.Lock()
			if firstErr != nil {
				mu.Unlock()
				return
			}
			if _, ok := completed[idx]; !ok {
				completed[idx] = struct{}{}
				state.Completed = append(state.Completed, idx)

				completedCount++
				downloadedBytes += dataSize
				completedSinceSave++

				// Snapshot state for async save when batch threshold reached.
				// This reduces IO by batching state saves.
				if completedSinceSave >= albumStateSaveBatch {
					stateCopy = &utils.AlbumState{
						Total:     state.Total,
						Completed: append([]int(nil), state.Completed...),
						DestPath:  state.DestPath,
						TempDir:   state.TempDir,
						Indices:   append([]int(nil), state.Indices...),
					}
					completedSinceSave = 0
				}

				if progressFn != nil {
					progressFn(float64(completedCount) / float64(total) * 100)
				}
				if bytesFn != nil {
					bytesFn(downloadedBytes, 0) // total=0 means unknown
				}
			}
			mu.Unlock()

			// Persist state outside lock to avoid blocking concurrent downloads.
			if stateCopy != nil {
				if err := utils.SaveAlbumState(stateCopy); err != nil {
					setErr(err)
					return
				}
			}
		}()
	}

	wg.Wait()

	// Handle errors - save state for resume on next attempt.
	if firstErr != nil {
		_ = utils.SaveAlbumState(state)
		return firstErr
	}
	if ctx.Err() != nil {
		_ = utils.SaveAlbumState(state)
		return ctx.Err()
	}

	// Force final state save.
	if err := utils.SaveAlbumState(state); err != nil {
		return err
	}

	// Package all downloaded images into a ZIP archive.
	if err := writeAlbumZip(destPath, tempDir, item.Images, selected); err != nil {
		return err
	}

	// Clean up temp directory after successful ZIP creation.
	if err := os.RemoveAll(tempDir); err != nil {
		logger.Warn("[Douyin] failed to remove temp dir %s: %v", tempDir, err)
	}

	if progressFn != nil {
		progressFn(100)
	}
	if bytesFn != nil {
		bytesFn(downloadedBytes, downloadedBytes) // final: downloaded == total
	}

	return nil
}

// DownloadAlbumPartial downloads only the specified image indices into a ZIP.
// Indices are 0-based. This allows users to select specific images from an album.
func (d *Downloader) DownloadAlbumPartial(item *DouyinItem, indices []int, destPath string, progressFn func(float64)) error {
	return d.DownloadAlbumPartialWithContext(context.Background(), item, indices, destPath, progressFn)
}

// DownloadAlbumPartialWithContext is the context-aware variant for selective album downloads.
// Used by DownloadManager for cancellation support.
func (d *Downloader) DownloadAlbumPartialWithContext(ctx context.Context, item *DouyinItem, indices []int, destPath string, progressFn func(float64)) error {
	return d.DownloadAlbumPartialWithBytes(ctx, item, indices, destPath, progressFn, nil)
}

// DownloadAlbumPartialWithBytes is the full variant with both percentage and byte-level progress.
func (d *Downloader) DownloadAlbumPartialWithBytes(ctx context.Context, item *DouyinItem, indices []int, destPath string, progressFn func(float64), bytesFn func(downloaded, total int64)) error {
	if item == nil {
		return fmt.Errorf("nil douyin item")
	}
	if len(item.Images) == 0 {
		return ErrNoImages
	}
	if destPath == "" {
		return fmt.Errorf("empty dest path")
	}
	if len(indices) == 0 {
		return fmt.Errorf("empty indices")
	}

	// Normalize and deduplicate indices to prevent duplicate downloads.
	normalized := make([]int, 0, len(indices))
	seen := make(map[int]struct{}, len(indices))
	for _, idx := range indices {
		if idx < 0 || idx >= len(item.Images) {
			return fmt.Errorf("index out of range: %d", idx)
		}
		if _, ok := seen[idx]; ok {
			continue
		}
		seen[idx] = struct{}{}
		normalized = append(normalized, idx)
	}
	if len(normalized) == 0 {
		return fmt.Errorf("empty indices")
	}

	return d.downloadImagesCore(ctx, item, normalized, destPath, progressFn, bytesFn)
}

// BuildDownloadFunc creates a download function for use with DownloadManager.
// This integrates the Douyin downloader with the generic download manager system.
// It automatically detects whether to download a video or album based on item type.
func (d *Downloader) BuildDownloadFunc(item *DouyinItem, qualityKey string, outputDir string) downloader.DownloadFunc {
	return func(ctx context.Context, task *downloader.DownloadTask, onProgress func(downloaded, total int64), onComplete func(outputPath string)) error {
		isAlbum := strings.ToLower(strings.TrimSpace(item.Type)) == "album" || len(item.Images) > 0
		ext := ".mp4"
		if isAlbum {
			ext = ".zip"
		}

		baseName := douyinFileBase(item)
		destPath := filepath.Join(outputDir, baseName+ext)

		if task != nil && task.FilePath != "" {
			destPath = task.FilePath
			if filepath.Ext(destPath) == "" {
				destPath = destPath + ext
			}
		}

		if isAlbum {
			total := len(item.Images)
			progressFn := func(p float64) {
				if task != nil && total > 0 {
					completed := int(p/100*float64(total) + 0.5)
					if completed > total {
						completed = total
					}
					task.AlbumCompleted = completed
					task.Progress = p // Update progress percentage
				}
			}
			if err := d.downloadAlbumWithContext(ctx, item, destPath, progressFn, onProgress); err != nil {
				return err
			}
			if task != nil {
				task.AlbumCompleted = total
				task.Progress = 100
			}
		} else {
			if err := d.downloadVideoWithContext(ctx, item, qualityKey, destPath, nil, onProgress); err != nil {
				return err
			}
		}

		if onComplete != nil {
			onComplete(destPath)
		}
		return nil
	}
}

// downloadImageWithRetry downloads a single image with exponential backoff retry.
// Returns the image data as bytes on success.
func (d *Downloader) downloadImageWithRetry(ctx context.Context, client *http.Client, rawURL string) ([]byte, error) {
	var lastErr error
	for attempt := 1; attempt <= albumMaxRetry; attempt++ {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		data, err := d.downloadImage(ctx, client, rawURL)
		if err == nil {
			return data, nil
		}
		lastErr = err
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Duration(attempt*100) * time.Millisecond):
		}
	}
	return nil, fmt.Errorf("failed to download image after %d attempts: %w", albumMaxRetry, lastErr)
}

// downloadImage downloads a single image and returns its bytes.
func (d *Downloader) downloadImage(ctx context.Context, client *http.Client, rawURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	d.applyHeaders(req)

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests {
		return nil, fmt.Errorf("douyin image download forbidden: status %d", resp.StatusCode)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("unexpected douyin image status: %d", resp.StatusCode)
	}

	return io.ReadAll(resp.Body)
}

// getHTTPClient returns the configured HTTP client or a default one.
func (d *Downloader) getHTTPClient() *http.Client {
	if d.httpClient != nil {
		return d.httpClient
	}
	return &http.Client{Timeout: 0}
}

// applyHeaders sets required HTTP headers for Douyin download requests.
func (d *Downloader) applyHeaders(req *http.Request) {
	ua := strings.TrimSpace(d.userAgent)
	if ua == "" {
		ua = defaultUserAgent
	}
	referer := strings.TrimSpace(d.referer)
	if referer == "" {
		referer = defaultReferer
	}

	req.Header.Set("User-Agent", ua)
	req.Header.Set("Referer", referer)
	req.Header.Set("Origin", originFromReferer(referer))
	req.Header.Set("Accept", "*/*")
}

// originFromReferer extracts the origin (scheme + host) from a referer URL.
func originFromReferer(ref string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "https://www.douyin.com"
	}
	u, err := url.Parse(ref)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ref
	}
	return fmt.Sprintf("%s://%s", u.Scheme, u.Host)
}

// selectStream finds a stream matching the requested quality key.
// Falls back to the first (highest quality) stream if not found.
func selectStream(item *DouyinItem, qualityKey string) *Stream {
	if item == nil || len(item.Streams) == 0 {
		logger.Warn("[Douyin] selectStream: no streams available")
		return nil
	}

	q := strings.ToLower(strings.TrimSpace(qualityKey))
	logger.Debug("[Douyin] selectStream: requested qualityKey=%q, available streams=%d", qualityKey, len(item.Streams))

	if q != "" {
		for i := range item.Streams {
			if strings.ToLower(item.Streams[i].QualityKey) == q {
				s := &item.Streams[i]
				logger.Debug("[Douyin] selectStream: matched stream QualityKey=%s, Resolution=%dx%d, Bitrate=%d",
					s.QualityKey, s.Width, s.Height, s.Bitrate)
				return s
			}
		}
		logger.Warn("[Douyin] selectStream: requested quality %q not found, falling back to first stream", qualityKey)
	}

	s := &item.Streams[0]
	logger.Debug("[Douyin] selectStream: using first stream QualityKey=%s, Resolution=%dx%d, Bitrate=%d",
		s.QualityKey, s.Width, s.Height, s.Bitrate)
	return s
}

// imageFileName generates a filename for an album image.
// Format: {sequence}_{original_name}.{ext}
// Example: "1_abc123.jpg", "2_image_2.png"
func imageFileName(idx int, rawURL string) string {
	seq := idx + 1
	base := ""

	if u, err := url.Parse(rawURL); err == nil && u.Path != "" {
		base = path.Base(u.Path)
	}
	if base == "" || base == "/" || base == "." {
		base = fmt.Sprintf("image_%d", seq)
	}

	extRaw := path.Ext(base)
	ext := strings.ToLower(extRaw)
	name := strings.TrimSuffix(base, extRaw)
	name = utils.SanitizeFileName(name, 50)
	if name == "" || name == "." || name == ".." {
		name = fmt.Sprintf("image_%d", seq)
	}

	if ext == "" {
		ext = ".jpg"
	}
	switch ext {
	case ".jpg", ".jpeg", ".png", ".gif", ".webp":
	default:
		ext = ".jpg"
	}

	return fmt.Sprintf("%d_%s%s", seq, name, ext)
}

// videoFileName generates a filename for an album video.
// Format: {sequence}_{original_name}.{ext}
// Example: "1_abc123.mp4", "2_video_2.mp4"
func videoFileName(idx int, rawURL string) string {
	seq := idx + 1
	base := ""

	if u, err := url.Parse(rawURL); err == nil && u.Path != "" {
		base = path.Base(u.Path)
	}
	if base == "" || base == "/" || base == "." {
		base = fmt.Sprintf("video_%d", seq)
	}

	extRaw := path.Ext(base)
	ext := strings.ToLower(extRaw)
	name := strings.TrimSuffix(base, extRaw)
	name = utils.SanitizeFileName(name, 50)
	if name == "" || name == "." || name == ".." {
		name = fmt.Sprintf("video_%d", seq)
	}

	if ext == "" {
		ext = ".mp4"
	}
	switch ext {
	case ".mp4", ".mov", ".m4v", ".webm":
	default:
		ext = ".mp4"
	}

	return fmt.Sprintf("%d_%s%s", seq, name, ext)
}

// albumEntryName generates a filename for an album entry (image or video).
// Uses videoFileName if the item has VideoURL, otherwise uses imageFileName.
func albumEntryName(idx int, img Image) string {
	if strings.TrimSpace(img.VideoURL) != "" {
		return videoFileName(idx, img.VideoURL)
	}
	return imageFileName(idx, img.URL)
}

// douyinFileBase generates a base filename from item metadata.
// Combines author, title, and ID for a descriptive filename.
func douyinFileBase(item *DouyinItem) string {
	parts := []string{
		strings.TrimSpace(item.Author),
		strings.TrimSpace(item.Title),
		strings.TrimSpace(item.ID),
	}
	joined := strings.Trim(strings.Join(utils.FilterNonEmpty(parts), "_"), "._ ")
	if joined == "" {
		joined = "douyin"
	}
	return utils.SanitizeFileName(joined, 80)
}

// effectiveHeaders returns the current header configuration as a map.
func (d *Downloader) effectiveHeaders() map[string]string {
	ua := strings.TrimSpace(d.userAgent)
	if ua == "" {
		ua = defaultUserAgent
	}
	referer := strings.TrimSpace(d.referer)
	if referer == "" {
		referer = defaultReferer
	}
	return map[string]string{
		"User-Agent": ua,
		"Referer":    referer,
		"Origin":     originFromReferer(referer),
		"Accept":     "*/*",
	}
}

// downloadVideoSequential downloads a video using sequential requests.
// Supports resumable downloads via Range headers.
// Used when multi-part download fails or isn't supported.
func (d *Downloader) downloadVideoSequential(ctx context.Context, client *http.Client, url string, destPath string, headers map[string]string, totalHint int64, progressFn func(float64), bytesFn func(downloaded, total int64)) error {
	const maxRangeRetries = 3 // Max retries when server ignores range requests.
	var rangeRetry int

	// Check for existing partial download to resume.
	var downloaded int64
	if fi, err := os.Stat(destPath); err == nil {
		downloaded = fi.Size()
	} else if !os.IsNotExist(err) {
		return err
	}

	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return err
		}
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		// Add Range header for resume support.
		if downloaded > 0 {
			req.Header.Set("Range", fmt.Sprintf("bytes=%d-", downloaded))
		}

		resp, err := client.Do(req)
		if err != nil {
			return err
		}

		if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests {
			resp.Body.Close()
			return fmt.Errorf("douyin download forbidden: status %d", resp.StatusCode)
		}
		if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
			resp.Body.Close()
			return fmt.Errorf("unexpected douyin response: %d", resp.StatusCode)
		}

		// If server ignored our Range header (returned 200 instead of 206),
		// restart download from scratch with a retry limit.
		if downloaded > 0 && resp.StatusCode == http.StatusOK {
			resp.Body.Close()
			downloaded = 0
			_ = os.Remove(destPath)
			rangeRetry++
			if rangeRetry >= maxRangeRetries {
				return fmt.Errorf("server ignores range requests after %d retries", maxRangeRetries)
			}
			continue
		}

		// Determine total file size from Content-Range or Content-Length.
		total := totalHint
		if resp.StatusCode == http.StatusPartialContent {
			if crTotal := parseContentRangeTotal(resp.Header.Get("Content-Range")); crTotal > 0 {
				total = crTotal
			} else if resp.ContentLength > 0 {
				total = resp.ContentLength + downloaded
			}
		}
		if total <= 0 {
			total = resp.ContentLength
		}

		// Open file for writing (append for resume, create for new).
		var f *os.File
		if downloaded > 0 {
			f, err = os.OpenFile(destPath, os.O_WRONLY, 0644)
			if err != nil {
				resp.Body.Close()
				return err
			}
			if _, err := f.Seek(downloaded, io.SeekStart); err != nil {
				resp.Body.Close()
				f.Close()
				return err
			}
		} else {
			f, err = os.Create(destPath)
			if err != nil {
				resp.Body.Close()
				return err
			}
		}

		// Report initial progress for resumed downloads.
		if downloaded > 0 {
			if bytesFn != nil {
				bytesFn(downloaded, total)
			}
			if progressFn != nil && total > 0 {
				progressFn(float64(downloaded) / float64(total) * 100)
			}
		}

		// Stream data to file with progress reporting.
		buf := make([]byte, 32*1024)
		for {
			n, readErr := resp.Body.Read(buf)
			if n > 0 {
				if _, err := f.Write(buf[:n]); err != nil {
					resp.Body.Close()
					f.Close()
					return err
				}
				downloaded += int64(n)

				if bytesFn != nil {
					bytesFn(downloaded, total)
				}
				if progressFn != nil && total > 0 {
					progressFn(float64(downloaded) / float64(total) * 100)
				}
			}

			if readErr != nil {
				if readErr == io.EOF {
					break
				}
				resp.Body.Close()
				f.Close()
				return readErr
			}
		}

		resp.Body.Close()
		f.Close()
		break
	}

	if progressFn != nil {
		progressFn(100)
	}
	if bytesFn != nil && totalHint <= 0 {
		bytesFn(downloaded, downloaded)
	}
	return nil
}

// parseContentRangeTotal extracts the total size from a Content-Range header.
// Format: "bytes start-end/total" or "bytes start-end/*" (unknown total).
func parseContentRangeTotal(cr string) int64 {
	if cr == "" {
		return 0
	}
	parts := strings.SplitN(cr, "/", 2)
	if len(parts) != 2 {
		return 0
	}
	totalStr := strings.TrimSpace(parts[1])
	if totalStr == "*" {
		return 0
	}
	total, err := strconv.ParseInt(totalStr, 10, 64)
	if err != nil || total < 0 {
		return 0
	}
	return total
}

// writeAlbumZip creates a ZIP archive containing all downloaded album images.
// Uses a temp file and atomic rename for crash safety.
func writeAlbumZip(destPath, tempDir string, images []Image, indices []int) (err error) {
	if len(indices) == 0 {
		indices = make([]int, len(images))
		for i := range images {
			indices[i] = i
		}
	}

	tmpPath := destPath + ".tmp"
	outFile, err := os.Create(tmpPath)
	if err != nil {
		return err
	}
	closed := false
	defer func() {
		if !closed {
			_ = outFile.Close()
		}
		if err != nil {
			_ = os.Remove(tmpPath)
		}
	}()

	zw := zip.NewWriter(outFile)
	for _, idx := range indices {
		if idx < 0 || idx >= len(images) {
			_ = zw.Close()
			err = fmt.Errorf("index out of range: %d", idx)
			return err
		}

		tempPath := utils.AlbumImageTempPath(tempDir, idx)
		f, openErr := os.Open(tempPath)
		if openErr != nil {
			_ = zw.Close()
			err = openErr
			return err
		}

		entryName := albumEntryName(idx, images[idx])
		writer, createErr := zw.Create(entryName)
		if createErr != nil {
			_ = f.Close()
			_ = zw.Close()
			err = createErr
			return err
		}
		if _, copyErr := io.Copy(writer, f); copyErr != nil {
			_ = f.Close()
			_ = zw.Close()
			err = copyErr
			return err
		}
		_ = f.Close()
	}
	if closeErr := zw.Close(); closeErr != nil {
		err = closeErr
		return err
	}
	if syncErr := outFile.Sync(); syncErr != nil {
		err = syncErr
		return err
	}
	if closeErr := outFile.Close(); closeErr != nil {
		err = closeErr
		return err
	}
	closed = true

	if renameErr := os.Rename(tmpPath, destPath); renameErr != nil {
		err = renameErr
		return err
	}
	return nil
}
