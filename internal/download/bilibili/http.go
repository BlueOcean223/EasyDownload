package bilibili

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"EasyDownload/internal/download/fetch"
	"EasyDownload/internal/infra/logger"
)

const bilibiliUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"

func (bd *BilibiliDownloader) getContentLength(ctx context.Context, fetcher fetch.Fetcher, rawURL string) (int64, error) {
	if fetcher == nil {
		return 0, fmt.Errorf("fetcher is required")
	}
	probe, err := fetcher.Probe(ctx, fetch.ProbeRequest{
		URL:     rawURL,
		Headers: bd.bilibiliMediaHeaders(),
	})
	if err != nil {
		return 0, err
	}
	if probe.ContentSize <= 0 {
		return 0, fmt.Errorf("content length unavailable")
	}
	return probe.ContentSize, nil
}

func (bd *BilibiliDownloader) getContentLengthWithFallback(ctx context.Context, fetcher fetch.Fetcher, primaryURL string, backupURLs []string) int64 {
	urls := collectDownloadURLs(primaryURL, backupURLs)
	for index, rawURL := range urls {
		size, err := bd.getContentLength(ctx, fetcher, rawURL)
		if err == nil && size > 0 {
			return size
		}
		if err != nil {
			logger.Debug("Failed to get content length for URL %d/%d: %v", index+1, len(urls), err)
		}
	}
	return 0
}

// downloadFileWithFallback uses only CDN URLs which Bilibili declares as
// backups for the same DASH resource. Different qualities/media candidates
// are selected by the adapter and never placed in EquivalentMirrorURLs.
func (bd *BilibiliDownloader) downloadFileWithFallback(ctx context.Context, fetcher fetch.Fetcher, primaryURL string, backupURLs []string, temporaryPath string, knownSize int64, onProgress func(float64)) (string, error) {
	urls := collectDownloadURLs(primaryURL, backupURLs)
	if len(urls) == 0 {
		return "", fmt.Errorf("no download URL available")
	}
	if fetcher == nil {
		return "", fmt.Errorf("fetcher is required")
	}
	result, err := fetcher.Download(ctx, fetch.Request{
		URL:                  urls[0],
		EquivalentMirrorURLs: urls[1:],
		Headers:              bd.bilibiliMediaHeaders(),
		Identity: fetch.ResourceIdentity{
			ExpectedSize: knownSize,
		},
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
		if onProgress != nil && progress.Total > 0 {
			onProgress(float64(progress.Downloaded) / float64(progress.Total) * 100)
		}
	})
	if err != nil {
		return "", fmt.Errorf("all byte-equivalent Bilibili CDN URLs failed: %w", err)
	}
	if onProgress != nil && result.Total > 0 {
		onProgress(float64(result.Downloaded) / float64(result.Total) * 100)
	}
	return result.TemporaryPath, nil
}

func collectDownloadURLs(primaryURL string, backupURLs []string) []string {
	urls := make([]string, 0, 1+len(backupURLs))
	seen := make(map[string]struct{}, 1+len(backupURLs))
	add := func(rawURL string) {
		rawURL = strings.TrimSpace(rawURL)
		if rawURL == "" {
			return
		}
		if _, ok := seen[rawURL]; ok {
			return
		}
		seen[rawURL] = struct{}{}
		urls = append(urls, rawURL)
	}
	add(primaryURL)
	for _, rawURL := range backupURLs {
		add(rawURL)
	}
	return urls
}

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

// bilibiliMediaHeaders deliberately excludes the Bilibili session cookie.
// DASH URLs and their byte-equivalent mirrors are hosted by media CDNs; a
// browser would not send a .bilibili.com cookie to those hosts either. The
// signed media URL is the authorization capability for the byte transfer.
func (bd *BilibiliDownloader) bilibiliMediaHeaders() map[string]string {
	return map[string]string{
		"User-Agent":      bilibiliUserAgent,
		"Referer":         "https://www.bilibili.com/",
		"Origin":          "https://www.bilibili.com",
		"Accept":          "*/*",
		"Accept-Language": "zh-CN,zh;q=0.9,en;q=0.8",
	}
}

func (bd *BilibiliDownloader) setHeaders(req *http.Request) {
	for key, value := range bd.bilibiliHeaders() {
		req.Header.Set(key, value)
	}
}
