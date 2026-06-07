package bilibili

import (
	"EasyDownload/internal/download"
	"EasyDownload/internal/infra/logger"
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
)

const bilibiliUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"

// getContentLength fetches the content length of a URL using a HEAD request.
// This is used to determine file sizes before downloading for progress estimation.
func (bd *BilibiliDownloader) getContentLength(url string) (int64, error) {
	req, err := http.NewRequest("HEAD", url, nil)
	if err != nil {
		return 0, err
	}

	bd.setHeaders(req)
	req.Header.Set("Referer", "https://www.bilibili.com/")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return 0, fmt.Errorf("unexpected status while fetching content length: %s", resp.Status)
	}
	if resp.ContentLength <= 0 {
		return 0, fmt.Errorf("content length unavailable")
	}

	return resp.ContentLength, nil
}

// getContentLengthWithFallback fetches content length from the primary URL first,
// then tries backup URLs until a valid content length is available.
func (bd *BilibiliDownloader) getContentLengthWithFallback(primaryURL string, backupURLs []string) int64 {
	urls := collectDownloadURLs(primaryURL, backupURLs)
	for i, url := range urls {
		size, err := bd.getContentLength(url)
		if err == nil && size > 0 {
			return size
		}
		if err != nil {
			logger.Debug("Failed to get content length for URL %d/%d: %v", i+1, len(urls), err)
		}
	}
	return 0
}

// downloadFileWithContext downloads a file with cancellation and resume support.
// It automatically chooses between multipart download (for large files with range support)
// and sequential download based on file size and server capabilities.
// Resume is supported through partial file detection and range requests.
func (bd *BilibiliDownloader) downloadFileWithContext(ctx context.Context, url, path string, knownSize int64, onProgress func(float64)) (string, error) {
	// Check if file already exists for resume
	var existingSize int64 = 0
	if fileInfo, err := os.Stat(path); err == nil {
		existingSize = fileInfo.Size()
	}
	stateExists := downloader.MultipartStateExists(path)

	// If we already have the full file, skip download
	if knownSize > 0 && existingSize >= knownSize && !stateExists {
		logger.Info("File already complete: %s (%d bytes)", path, existingSize)
		if onProgress != nil {
			onProgress(100)
		}
		return path, nil
	}

	// If resuming a multipart download (resume state exists), continue with multipart.
	if stateExists {
		md := downloader.NewMultipartDownloader()
		md.SetHeaders(bd.bilibiliHeaders())
		totalSize := knownSize
		if totalSize <= 0 {
			if st, err := downloader.LoadMultipartTotalSize(path); err == nil && st > 0 {
				totalSize = st
			}
		}
		if totalSize <= 0 {
			checkResult := md.CheckRangeSupport(ctx, url)
			if checkResult.Error != nil {
				logger.Debug("Failed to check range support: %v, falling back to sequential", checkResult.Error)
				return bd.downloadFileSequential(ctx, url, path, 0, onProgress)
			}
			totalSize = checkResult.ContentLength
		}
		return bd.downloadFileMultipart(ctx, url, path, totalSize, md, onProgress)
	}

	// If resuming, use sequential download
	if existingSize > 0 {
		logger.Debug("Resuming download from %d bytes, using sequential download", existingSize)
		return bd.downloadFileSequential(ctx, url, path, knownSize, onProgress)
	}

	// For new downloads, check if multipart download is beneficial
	md := downloader.NewMultipartDownloader()
	md.SetHeaders(bd.bilibiliHeaders())

	// Determine file size - use knownSize if provided, otherwise check
	totalSize := knownSize
	supportsRange := true

	if totalSize <= 0 {
		checkResult := md.CheckRangeSupport(ctx, url)
		if checkResult.Error != nil {
			logger.Debug("Failed to check range support: %v, falling back to sequential", checkResult.Error)
			return bd.downloadFileSequential(ctx, url, path, 0, onProgress)
		}
		totalSize = checkResult.ContentLength
		supportsRange = checkResult.SupportsRange
	}

	// Decide whether to use multipart download
	if downloader.ShouldUseMultipart(supportsRange, totalSize) {
		logger.Info("Using multipart download for Bilibili (size: %d bytes)", totalSize)
		return bd.downloadFileMultipart(ctx, url, path, totalSize, md, onProgress)
	}

	// Fall back to sequential download
	logger.Debug("Using sequential download (size: %d, supports range: %v)", totalSize, supportsRange)
	return bd.downloadFileSequential(ctx, url, path, totalSize, onProgress)
}

// bilibiliHeaders returns HTTP headers required for Bilibili API requests.
// Includes Referer, Origin, and SESSDATA cookie (if authenticated).
func (bd *BilibiliDownloader) bilibiliHeaders() map[string]string {
	headers := map[string]string{
		"User-Agent":      bilibiliUserAgent,
		"Referer":         "https://www.bilibili.com/",
		"Origin":          "https://www.bilibili.com",
		"Accept":          "application/json, text/plain, */*",
		"Accept-Language": "zh-CN,zh;q=0.9,en;q=0.8",
	}
	bd.sessDataMu.RLock()
	sessData := bd.sessData
	bd.sessDataMu.RUnlock()
	if sessData != "" {
		headers["Cookie"] = fmt.Sprintf("SESSDATA=%s", sessData)
	}
	return headers
}

// downloadFileMultipart performs concurrent multipart download for faster speeds.
// Splits the file into chunks and downloads them in parallel using multiple connections.
func (bd *BilibiliDownloader) downloadFileMultipart(ctx context.Context, url, path string, totalSize int64, md *downloader.MultipartDownloader, onProgress func(float64)) (string, error) {
	result := md.Download(ctx, url, path, totalSize, func(downloaded, total int64) {
		if onProgress != nil && total > 0 {
			progress := float64(downloaded) / float64(total) * 100
			onProgress(progress)
		}
	})

	if result.Error != nil {
		return "", result.Error
	}

	return path, nil
}

// downloadFileSequential performs traditional sequential download.
// Supports resume through HTTP Range requests if a partial file exists.
// Used as fallback when multipart download is not suitable or supported.
func (bd *BilibiliDownloader) downloadFileSequential(ctx context.Context, url, path string, knownSize int64, onProgress func(float64)) (string, error) {
	headers := bd.bilibiliHeaders()

	downloaded, total, err := downloader.DownloadFileResumable(ctx, nil, url, path, downloader.ResumableDownloadOptions{
		Headers:         headers,
		TotalHint:       knownSize,
		MaxRangeRetries: 3,
	}, func(downloaded, total int64) {
		if onProgress != nil && total > 0 {
			onProgress(float64(downloaded) / float64(total) * 100)
		}
	})
	if err != nil {
		return "", err
	}
	if onProgress != nil && total > 0 {
		onProgress(float64(downloaded) / float64(total) * 100)
	}
	return path, nil
}

// collectDownloadURLs builds an ordered, de-duplicated list of candidate download URLs.
func collectDownloadURLs(primaryURL string, backupURLs []string) []string {
	urls := make([]string, 0, 1+len(backupURLs))
	seen := make(map[string]struct{}, 1+len(backupURLs))
	add := func(url string) {
		url = strings.TrimSpace(url)
		if url == "" {
			return
		}
		if _, ok := seen[url]; ok {
			return
		}
		seen[url] = struct{}{}
		urls = append(urls, url)
	}

	add(primaryURL)
	for _, url := range backupURLs {
		add(url)
	}
	return urls
}

// downloadFileWithFallback downloads a file, trying primary URL first then fallback URLs.
// It uses the same download strategy as downloadFileWithContext but iterates over backup URLs
// when the primary fails. Returns the final path and any error.
func (bd *BilibiliDownloader) downloadFileWithFallback(ctx context.Context, primaryURL string, backupURLs []string, path string, knownSize int64, onProgress func(float64)) (string, error) {
	urls := collectDownloadURLs(primaryURL, backupURLs)
	if len(urls) == 0 {
		return "", fmt.Errorf("no download URL available")
	}

	var lastErr error
	for i, url := range urls {
		if i > 0 {
			logger.Info("Download URL failed, trying fallback URL %d/%d: %s", i, len(urls)-1, url[:min(60, len(url))])
		}
		result, err := bd.downloadFileWithContext(ctx, url, path, knownSize, onProgress)
		if err == nil {
			return result, nil
		}
		// If cancelled, don't retry
		if ctx.Err() != nil {
			return "", err
		}
		lastErr = err
		logger.Debug("Download failed for URL %d: %v", i+1, err)
	}
	return "", fmt.Errorf("all download URLs failed: %w", lastErr)
}

// setHeaders sets common HTTP headers required for Bilibili API requests.
// This includes User-Agent, Accept headers, and SESSDATA cookie if authenticated.
// The headers mimic a modern browser to avoid being blocked by Bilibili's anti-bot measures.
func (bd *BilibiliDownloader) setHeaders(req *http.Request) {
	req.Header.Set("User-Agent", bilibiliUserAgent)
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
	req.Header.Set("Origin", "https://www.bilibili.com")
	req.Header.Set("Referer", "https://www.bilibili.com/")

	// Thread-safe access to sessData
	bd.sessDataMu.RLock()
	sessData := bd.sessData
	bd.sessDataMu.RUnlock()

	if sessData != "" {
		req.Header.Set("Cookie", fmt.Sprintf("SESSDATA=%s", sessData))
	}
}
