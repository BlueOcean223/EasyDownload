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
	"strconv"
	"strings"
	"sync"
	"time"

	"EasyDownload/internal/downloader"
	"EasyDownload/internal/logger"
)

const (
	defaultReferer   = "https://www.douyin.com/"
	albumConcurrency = 4
	albumMaxRetry    = 3
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
	if item == nil {
		return fmt.Errorf("nil douyin item")
	}
	if len(item.Images) == 0 {
		return ErrNoImages
	}
	if destPath == "" {
		return fmt.Errorf("empty dest path")
	}

	client := d.getHTTPClient()
	if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
		return err
	}

	type imgResult struct {
		index int
		name  string
		data  []byte
		err   error
	}

	results := make([]imgResult, len(item.Images))
	var wg sync.WaitGroup
	sem := make(chan struct{}, albumConcurrency)
	var firstErr error
	var mu sync.Mutex
	var downloadedCount int64

	for idx, img := range item.Images {
		idx := idx
		img := img

		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			data, err := d.downloadImageWithRetry(ctx, client, img.URL)
			if err != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				mu.Unlock()
				return
			}

			results[idx] = imgResult{
				index: idx,
				name:  imageFileName(idx, img.URL),
				data:  data,
			}

			mu.Lock()
			downloadedCount++
			if progressFn != nil {
				progressFn(float64(downloadedCount) / float64(len(item.Images)) * 100)
			}
			if bytesFn != nil {
				bytesFn(downloadedCount, int64(len(item.Images)))
			}
			mu.Unlock()
		}()
	}

	wg.Wait()

	if firstErr != nil {
		return firstErr
	}

	outFile, err := os.Create(destPath)
	if err != nil {
		return err
	}
	defer outFile.Close()

	zw := zip.NewWriter(outFile)
	for _, res := range results {
		if res.data == nil {
			return fmt.Errorf("missing data for image %d", res.index)
		}
		writer, err := zw.Create(res.name)
		if err != nil {
			return err
		}
		if _, err := writer.Write(res.data); err != nil {
			return err
		}
	}
	if err := zw.Close(); err != nil {
		return err
	}

	if progressFn != nil {
		progressFn(100)
	}
	if bytesFn != nil {
		bytesFn(int64(len(item.Images)), int64(len(item.Images)))
	}

	return nil
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
			if err := d.downloadAlbumWithContext(ctx, item, destPath, nil, onProgress); err != nil {
				return err
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
		data, err := d.downloadImage(ctx, client, rawURL)
		if err == nil {
			return data, nil
		}
		lastErr = err
		time.Sleep(time.Duration(attempt*100) * time.Millisecond)
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

	ext := path.Ext(base)
	name := strings.TrimSuffix(base, ext)
	if ext == "" {
		ext = ".jpg"
	}
	if strings.TrimSpace(name) == "" {
		name = fmt.Sprintf("image_%d", seq)
	}

	return fmt.Sprintf("%d_%s%s", seq, name, ext)
}

func douyinFileBase(item *DouyinItem) string {
	parts := []string{
		strings.TrimSpace(item.Author),
		strings.TrimSpace(item.Title),
		strings.TrimSpace(item.ID),
	}
	joined := strings.Trim(strings.Join(filterNonEmpty(parts), "_"), "._ ")
	if joined == "" {
		joined = "douyin"
	}
	return sanitizeFileComponent(joined)
}

func filterNonEmpty(in []string) []string {
	var out []string
	for _, s := range in {
		if strings.TrimSpace(s) != "" {
			out = append(out, strings.TrimSpace(s))
		}
	}
	return out
}

func sanitizeFileComponent(s string) string {
	replacer := strings.NewReplacer(
		"\\", "_",
		"/", "_",
		":", "_",
		"*", "_",
		"?", "_",
		"\"", "_",
		"<", "_",
		">", "_",
		"|", "_",
		"\r", " ",
		"\n", " ",
		"\t", " ",
	)
	s = replacer.Replace(strings.TrimSpace(s))
	s = strings.Trim(s, " ._")
	if s == "" {
		return "douyin"
	}
	if len(s) > 80 {
		s = s[:80]
	}
	return s
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

		// If server ignored range, restart from scratch
		if downloaded > 0 && resp.StatusCode == http.StatusOK {
			resp.Body.Close()
			downloaded = 0
			_ = os.Remove(destPath)
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
