package xiaohongshu

import (
	"archive/zip"
	"bufio"
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
	"sync/atomic"
	"time"

	"EasyDownload/internal/downloader"
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
	if destPath == "" {
		return fmt.Errorf("empty dest path")
	}
	if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
		return err
	}

	const maxRangeRetries = 3
	var rangeRetry int

	// Check for existing partial download
	var downloaded int64
	if fi, err := os.Stat(destPath); err == nil {
		downloaded = fi.Size()
	} else if !os.IsNotExist(err) {
		return err
	}

	for {
		// Derive a context that can be cancelled if the download stalls.
		reqCtx, cancel := context.WithCancel(ctx)

		var lastProgress atomic.Int64
		lastProgress.Store(time.Now().UnixNano())
		var stalled atomic.Bool
		if d.noProgressTimeout > 0 {
			go func() {
				t := time.NewTicker(time.Second)
				defer t.Stop()
				for {
					select {
					case <-reqCtx.Done():
						return
					case <-t.C:
						last := time.Unix(0, lastProgress.Load())
						if time.Since(last) > d.noProgressTimeout {
							stalled.Store(true)
							cancel()
							return
						}
					}
				}
			}()
		}

		req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, rawURL, nil)
		if err != nil {
			cancel()
			return err
		}
		req.Header.Set("User-Agent", d.userAgent)
		req.Header.Set("Referer", d.referer)

		// Add Range header for resume
		if downloaded > 0 {
			req.Header.Set("Range", fmt.Sprintf("bytes=%d-", downloaded))
		}

		resp, err := d.httpClient.Do(req)
		if err != nil {
			cancel()
			return err
		}

		// Handle 416 Range Not Satisfiable (file already complete)
		if resp.StatusCode == http.StatusRequestedRangeNotSatisfiable && downloaded > 0 {
			resp.Body.Close()
			cancel()
			if onProgress != nil {
				onProgress(downloaded, downloaded)
			}
			return nil
		}

		if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
			resp.Body.Close()
			cancel()
			return fmt.Errorf("unexpected status %d", resp.StatusCode)
		}

		// Server ignored Range header (returned 200 instead of 206)
		if downloaded > 0 && resp.StatusCode == http.StatusOK {
			resp.Body.Close()
			cancel()
			downloaded = 0
			_ = os.Remove(destPath)
			rangeRetry++
			if rangeRetry >= maxRangeRetries {
				return fmt.Errorf("server ignores range requests after %d retries", maxRangeRetries)
			}
			continue
		}

		// Determine total size
		total := int64(0)
		if resp.StatusCode == http.StatusPartialContent {
			if crTotal := parseContentRangeTotal(resp.Header.Get("Content-Range")); crTotal > 0 {
				total = crTotal
			} else if resp.ContentLength > 0 {
				total = resp.ContentLength + downloaded
			}
		}
		if total <= 0 && resp.ContentLength > 0 {
			total = resp.ContentLength
		}
		if d.maxVideoSize > 0 && total > 0 && total > d.maxVideoSize {
			resp.Body.Close()
			cancel()
			return fmt.Errorf("%w: total=%d limit=%d", ErrVideoTooLarge, total, d.maxVideoSize)
		}
		if d.maxVideoSize > 0 && downloaded > d.maxVideoSize {
			resp.Body.Close()
			cancel()
			return fmt.Errorf("%w: downloaded=%d limit=%d", ErrVideoTooLarge, downloaded, d.maxVideoSize)
		}

		// Open file for writing
		var out *os.File
		if downloaded > 0 {
			out, err = os.OpenFile(destPath, os.O_APPEND|os.O_WRONLY, 0644)
		} else {
			out, err = os.Create(destPath)
		}
		if err != nil {
			resp.Body.Close()
			cancel()
			return err
		}

		// Report initial progress for resumed downloads
		if downloaded > 0 && onProgress != nil {
			onProgress(downloaded, total)
		}

		buf := make([]byte, 32*1024)
		var readErr error
		for {
			n, rerr := resp.Body.Read(buf)
			if n > 0 {
				if _, werr := out.Write(buf[:n]); werr != nil {
					resp.Body.Close()
					_ = out.Close()
					cancel()
					return werr
				}
				downloaded += int64(n)
				lastProgress.Store(time.Now().UnixNano())
				if d.maxVideoSize > 0 && downloaded > d.maxVideoSize {
					resp.Body.Close()
					_ = out.Close()
					cancel()
					return fmt.Errorf("%w: downloaded=%d limit=%d", ErrVideoTooLarge, downloaded, d.maxVideoSize)
				}
				if onProgress != nil {
					onProgress(downloaded, total)
				}
			}
			if rerr != nil {
				readErr = rerr
				break
			}
		}

		resp.Body.Close()
		if err := out.Close(); err != nil {
			cancel()
			return err
		}
		if ctx.Err() != nil {
			cancel()
			return ctx.Err()
		}
		if stalled.Load() {
			cancel()
			return fmt.Errorf("%w: timeout=%s", ErrNoProgressTimeout, d.noProgressTimeout)
		}
		cancel()
		if readErr != nil && readErr != io.EOF {
			return readErr
		}
		break
	}
	return nil
}

// parseContentRangeTotal extracts total size from Content-Range header.
// Format: "bytes start-end/total"
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

// writeAlbumZip creates a ZIP archive from downloaded temp images.
func writeAlbumZip(destPath, tempDir string, images []XHSImage, indices []int) (err error) {
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

	// Buffer writes to reduce syscalls when writing many small zip entries.
	bw := bufio.NewWriterSize(outFile, zipBufferSize)
	zw := zip.NewWriter(bw)
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

		entryName := fmt.Sprintf("%03d%s", idx+1, imageExt(images[idx].URL))
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
	if flushErr := bw.Flush(); flushErr != nil {
		err = flushErr
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

	_ = os.Remove(destPath)
	if renameErr := os.Rename(tmpPath, destPath); renameErr != nil {
		err = renameErr
		return err
	}
	return nil
}
