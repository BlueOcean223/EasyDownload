// Package downloader 提供 HTTP 文件下载功能，支持断点续传、进度回调、超时控制和大小限制。
package downloader

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"
)

// HTTPStatusError 表示 HTTP 响应状态码错误（非 200/206）。
type HTTPStatusError struct {
	StatusCode int
	Status     string
}

func (e *HTTPStatusError) Error() string {
	if e == nil {
		return "http status error"
	}
	if e.Status != "" {
		return fmt.Sprintf("unexpected http status: %s", e.Status)
	}
	return fmt.Sprintf("unexpected http status: %d", e.StatusCode)
}

// NoProgressTimeoutError 表示下载过程中长时间无数据传输导致的超时错误。
type NoProgressTimeoutError struct {
	Timeout time.Duration
}

func (e *NoProgressTimeoutError) Error() string {
	if e == nil {
		return "no progress timeout"
	}
	return fmt.Sprintf("no progress timeout: %s", e.Timeout)
}

// SizeLimitExceededError 表示文件大小超出限制的错误。
// Reason 为 "total" 表示文件总大小超限，"downloaded" 表示已下载字节数超限。
type SizeLimitExceededError struct {
	Limit      int64
	Total      int64
	Downloaded int64
	Reason     string
}

func (e *SizeLimitExceededError) Error() string {
	if e == nil {
		return "size limit exceeded"
	}
	return fmt.Sprintf("size limit exceeded: reason=%s downloaded=%d total=%d limit=%d", e.Reason, e.Downloaded, e.Total, e.Limit)
}

// ResumableDownloadOptions 定义断点续传下载的配置选项。
type ResumableDownloadOptions struct {
	Headers           map[string]string // 自定义 HTTP 请求头
	TotalHint         int64             // 文件总大小提示（当服务器不返回 Content-Length 时使用）
	MaxRangeRetries   int               // Range 请求失败后的最大重试次数，默认 3
	MaxBytes          int64             // 最大下载字节数限制，0 表示不限制
	NoProgressTimeout time.Duration     // 无进度超时时间，超时后取消下载
	BufferSize        int               // 读写缓冲区大小，默认 32KB
}

// DownloadFileResumable 执行支持断点续传的 HTTP 文件下载。
//
// 参数:
//   - ctx: 用于取消下载的上下文
//   - client: HTTP 客户端，为 nil 时使用默认客户端
//   - rawURL: 下载地址
//   - destPath: 目标文件路径
//   - opts: 下载配置选项
//   - onProgress: 进度回调函数，可为 nil
//
// 返回:
//   - downloaded: 已下载字节数
//   - total: 文件总大小（未知时为 0）
//   - err: 错误信息，可能为 HTTPStatusError、NoProgressTimeoutError 或 SizeLimitExceededError
func DownloadFileResumable(ctx context.Context, client *http.Client, rawURL, destPath string, opts ResumableDownloadOptions, onProgress func(downloaded, total int64)) (downloaded int64, total int64, err error) {
	if rawURL == "" {
		return 0, 0, fmt.Errorf("empty url")
	}
	if destPath == "" {
		return 0, 0, fmt.Errorf("empty dest path")
	}
	if client == nil {
		client = &http.Client{Timeout: 0}
	}

	// Resume offset is determined from the existing file size.
	if fi, statErr := os.Stat(destPath); statErr == nil {
		downloaded = fi.Size()
	} else if !os.IsNotExist(statErr) {
		return 0, 0, statErr
	}

	maxRangeRetries := opts.MaxRangeRetries
	if maxRangeRetries <= 0 {
		maxRangeRetries = 3
	}
	bufSize := opts.BufferSize
	if bufSize <= 0 {
		bufSize = 32 * 1024
	}

	rangeRetry := 0
	for {
		// Derive a context that can be cancelled if the download stalls.
		reqCtx, cancel := context.WithCancel(ctx)

		var lastProgress atomic.Int64
		lastProgress.Store(time.Now().UnixNano())
		var stalled atomic.Bool
		if opts.NoProgressTimeout > 0 {
			go func() {
				t := time.NewTicker(time.Second)
				defer t.Stop()
				for {
					select {
					case <-reqCtx.Done():
						return
					case <-t.C:
						last := time.Unix(0, lastProgress.Load())
						if time.Since(last) > opts.NoProgressTimeout {
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
			return 0, 0, err
		}
		for k, v := range opts.Headers {
			req.Header.Set(k, v)
		}
		if downloaded > 0 {
			req.Header.Set("Range", fmt.Sprintf("bytes=%d-", downloaded))
		}

		resp, err := client.Do(req)
		if err != nil {
			cancel()
			return 0, 0, err
		}

		// Handle 416 Range Not Satisfiable. This only means success when the
		// local file size exactly matches the server's total size; if the local
		// file is larger or the total is unknown, treating it as complete can
		// preserve a corrupt file.
		if resp.StatusCode == http.StatusRequestedRangeNotSatisfiable && downloaded > 0 {
			serverTotal := TotalSizeFromContentRange(resp.Header.Get("Content-Range"))
			if serverTotal <= 0 {
				serverTotal = opts.TotalHint
			}
			resp.Body.Close()
			cancel()
			if serverTotal > 0 && downloaded == serverTotal {
				if onProgress != nil {
					onProgress(downloaded, serverTotal)
				}
				return downloaded, serverTotal, nil
			}
			return downloaded, serverTotal, fmt.Errorf("range not satisfiable: local size=%d remote size=%d", downloaded, serverTotal)
		}

		if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
			resp.Body.Close()
			cancel()
			return downloaded, total, &HTTPStatusError{StatusCode: resp.StatusCode, Status: resp.Status}
		}

		// Server ignored Range header (returned 200 instead of 206)
		if downloaded > 0 && resp.StatusCode == http.StatusOK {
			resp.Body.Close()
			cancel()
			downloaded = 0
			_ = os.Remove(destPath)
			rangeRetry++
			if rangeRetry >= maxRangeRetries {
				return 0, 0, fmt.Errorf("server ignores range requests after %d retries", maxRangeRetries)
			}
			continue
		}

		// Determine total size
		total = opts.TotalHint
		if resp.StatusCode == http.StatusPartialContent {
			if crTotal := TotalSizeFromContentRange(resp.Header.Get("Content-Range")); crTotal > 0 {
				total = crTotal
			} else if resp.ContentLength > 0 {
				total = resp.ContentLength + downloaded
			}
		}
		if total <= 0 && resp.ContentLength > 0 {
			// For 206 responses Content-Length is just the remaining bytes; include already downloaded.
			if resp.StatusCode == http.StatusPartialContent && downloaded > 0 {
				total = resp.ContentLength + downloaded
			} else {
				total = resp.ContentLength
			}
		}

		if opts.MaxBytes > 0 && total > 0 && total > opts.MaxBytes {
			resp.Body.Close()
			cancel()
			return downloaded, total, &SizeLimitExceededError{Limit: opts.MaxBytes, Total: total, Downloaded: downloaded, Reason: "total"}
		}
		if opts.MaxBytes > 0 && downloaded > opts.MaxBytes {
			resp.Body.Close()
			cancel()
			return downloaded, total, &SizeLimitExceededError{Limit: opts.MaxBytes, Total: total, Downloaded: downloaded, Reason: "downloaded"}
		}

		if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
			resp.Body.Close()
			cancel()
			return 0, 0, err
		}

		// Open file for writing (append for resume, create for new).
		var f *os.File
		if downloaded > 0 {
			f, err = os.OpenFile(destPath, os.O_WRONLY, 0644)
			if err != nil {
				resp.Body.Close()
				cancel()
				return 0, 0, err
			}
			if _, err := f.Seek(downloaded, io.SeekStart); err != nil {
				resp.Body.Close()
				_ = f.Close()
				cancel()
				return 0, 0, err
			}
		} else {
			f, err = os.Create(destPath)
			if err != nil {
				resp.Body.Close()
				cancel()
				return 0, 0, err
			}
		}

		if downloaded > 0 && onProgress != nil {
			onProgress(downloaded, total)
		}

		buf := make([]byte, bufSize)
		var readErr error
		for {
			n, rerr := resp.Body.Read(buf)
			if n > 0 {
				if _, werr := f.Write(buf[:n]); werr != nil {
					resp.Body.Close()
					_ = f.Close()
					cancel()
					return downloaded, total, werr
				}
				downloaded += int64(n)
				lastProgress.Store(time.Now().UnixNano())

				if opts.MaxBytes > 0 && downloaded > opts.MaxBytes {
					resp.Body.Close()
					_ = f.Close()
					cancel()
					return downloaded, total, &SizeLimitExceededError{Limit: opts.MaxBytes, Total: total, Downloaded: downloaded, Reason: "downloaded"}
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
		if cerr := f.Close(); cerr != nil {
			cancel()
			return downloaded, total, cerr
		}

		if stalled.Load() {
			cancel()
			return downloaded, total, &NoProgressTimeoutError{Timeout: opts.NoProgressTimeout}
		}
		if ctx.Err() != nil {
			cancel()
			return downloaded, total, ctx.Err()
		}
		cancel()
		if !errors.Is(readErr, io.EOF) {
			return downloaded, total, readErr
		}
		break
	}

	return downloaded, total, nil
}
