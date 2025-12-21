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

const (
	defaultReferer      = "https://www.douyin.com/"
	albumConcurrency    = 4
	albumMaxRetry       = 3
	albumStateSaveBatch = 5 // batch state saves to reduce IO
)

var (
	ErrStreamNotFound = errors.New("douyin stream not found")
	ErrNoImages       = errors.New("douyin album has no images")
)

type Downloader struct {
	httpClient *http.Client
	userAgent  string
	referer    string
}

func NewDownloader() *Downloader {
	return &Downloader{
		httpClient: &http.Client{Timeout: 0},
		userAgent:  defaultUserAgent,
		referer:    defaultReferer,
	}
}

func (d *Downloader) DownloadVideo(item *DouyinItem, qualityKey string, destPath string, progressFn func(float64)) error {
	return d.downloadVideoWithContext(context.Background(), item, qualityKey, destPath, progressFn, nil)
}

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

	if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
		return err
	}

	md := downloader.NewMultipartDownloader()
	md.SetHeaders(headers)

	checkResult := md.CheckRangeSupport(ctx, stream.URL)
	totalSize := checkResult.ContentLength
	supportsRange := checkResult.Error == nil && checkResult.SupportsRange

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
		// Fallback to sequential if multipart fails
	}

	return d.downloadVideoSequential(ctx, client, stream.URL, destPath, headers, totalSize, progressFn, bytesFn)
}

func (d *Downloader) DownloadAlbum(item *DouyinItem, destPath string, progressFn func(float64)) error {
	return d.downloadAlbumWithContext(context.Background(), item, destPath, progressFn, nil)
}

func (d *Downloader) downloadAlbumWithContext(ctx context.Context, item *DouyinItem, destPath string, progressFn func(float64), bytesFn func(downloaded, total int64)) error {
	return d.downloadImagesCore(ctx, item, nil, destPath, progressFn, bytesFn)
}

// downloadImagesCore is the unified core method for downloading album images.
// indices == nil means download all images; otherwise download only specified indices.
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

	// Determine which indices to download
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
	state, err := utils.LoadAlbumState(tempDir)
	if err != nil {
		return err
	}

	// Normalize indices for consistent comparison (order-independent)
	normalizedSelected := utils.NormalizeIndices(selected)

	// Check if state needs reset
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

	// Build target set for validation
	targetSet := make(map[int]struct{}, len(selected))
	for _, idx := range selected {
		targetSet[idx] = struct{}{}
	}

	// Validate completed items
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
	sort.Ints(state.Completed) // ensure stable ordering for reproducible state files
	if err := utils.SaveAlbumState(state); err != nil {
		return err
	}

	// Track actual downloaded bytes (not image count)
	var downloadedBytes int64
	completedCount := len(completed)

	// Sum up bytes from already completed temp files
	for idx := range completed {
		if fi, err := os.Stat(utils.AlbumImageTempPath(tempDir, idx)); err == nil {
			downloadedBytes += fi.Size()
		}
	}

	if bytesFn != nil {
		bytesFn(downloadedBytes, 0) // total=0 means unknown
	}
	if progressFn != nil && total > 0 {
		progressFn(float64(completedCount) / float64(total) * 100)
	}

	// Already complete?
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

	var wg sync.WaitGroup
	sem := make(chan struct{}, albumConcurrency)
	var firstErr error
	var mu sync.Mutex
	completedSinceSave := 0

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

	for _, idx := range selected {
		if _, ok := completed[idx]; ok {
			continue
		}
		idx := idx
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

			data, err := d.downloadImageWithRetry(ctx, client, img.URL)
			if err != nil {
				setErr(err)
				return
			}

			dataSize := int64(len(data))
			tempPath := utils.AlbumImageTempPath(tempDir, idx)
			if err := os.WriteFile(tempPath, data, 0644); err != nil {
				setErr(err)
				return
			}

			// Update state under lock, but defer IO outside lock
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

				// Snapshot state for async save when batch threshold reached
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

			// Persist state outside lock to avoid blocking concurrent downloads
			if stateCopy != nil {
				if err := utils.SaveAlbumState(stateCopy); err != nil {
					setErr(err)
					return
				}
			}
		}()
	}

	wg.Wait()

	if firstErr != nil {
		_ = utils.SaveAlbumState(state)
		return firstErr
	}
	if ctx.Err() != nil {
		_ = utils.SaveAlbumState(state)
		return ctx.Err()
	}

	// Force final save
	if err := utils.SaveAlbumState(state); err != nil {
		return err
	}

	if err := writeAlbumZip(destPath, tempDir, item.Images, selected); err != nil {
		return err
	}
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

// DownloadAlbumPartial downloads only the specified image indices (0-based) into a ZIP.
func (d *Downloader) DownloadAlbumPartial(item *DouyinItem, indices []int, destPath string, progressFn func(float64)) error {
	return d.DownloadAlbumPartialWithContext(context.Background(), item, indices, destPath, progressFn)
}

// DownloadAlbumPartialWithContext is the context-aware variant used by DownloadManager for cancellation.
func (d *Downloader) DownloadAlbumPartialWithContext(ctx context.Context, item *DouyinItem, indices []int, destPath string, progressFn func(float64)) error {
	return d.DownloadAlbumPartialWithBytes(ctx, item, indices, destPath, progressFn, nil)
}

// DownloadAlbumPartialWithBytes is the full variant with byte progress callback.
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

	// Normalize and deduplicate indices
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

func (d *Downloader) getHTTPClient() *http.Client {
	if d.httpClient != nil {
		return d.httpClient
	}
	return &http.Client{Timeout: 0}
}

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

func selectStream(item *DouyinItem, qualityKey string) *Stream {
	if item == nil || len(item.Streams) == 0 {
		logger.Warn("[Douyin] selectStream: no streams available")
		return nil
	}

	q := strings.ToLower(strings.TrimSpace(qualityKey))
	logger.Info("[Douyin] selectStream: requested qualityKey=%q, available streams=%d", qualityKey, len(item.Streams))

	if q != "" {
		for i := range item.Streams {
			if strings.ToLower(item.Streams[i].QualityKey) == q {
				s := &item.Streams[i]
				logger.Info("[Douyin] selectStream: matched stream QualityKey=%s, Resolution=%dx%d, Bitrate=%d",
					s.QualityKey, s.Width, s.Height, s.Bitrate)
				return s
			}
		}
		logger.Warn("[Douyin] selectStream: requested quality %q not found, falling back to first stream", qualityKey)
	}

	s := &item.Streams[0]
	logger.Info("[Douyin] selectStream: using first stream QualityKey=%s, Resolution=%dx%d, Bitrate=%d",
		s.QualityKey, s.Width, s.Height, s.Bitrate)
	return s
}

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

func (d *Downloader) downloadVideoSequential(ctx context.Context, client *http.Client, url string, destPath string, headers map[string]string, totalHint int64, progressFn func(float64), bytesFn func(downloaded, total int64)) error {
	const maxRangeRetries = 3
	var rangeRetry int

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

		// If server ignored range, restart from scratch with retry limit
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

		if downloaded > 0 {
			if bytesFn != nil {
				bytesFn(downloaded, total)
			}
			if progressFn != nil && total > 0 {
				progressFn(float64(downloaded) / float64(total) * 100)
			}
		}

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

		entryName := imageFileName(idx, images[idx].URL)
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
