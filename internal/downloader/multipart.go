package downloader

import (
	"EasyDownload/internal/logger"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

// Constants for multipart download
const (
	// MinSizeForMultipart is the minimum file size to use multipart download (1MB)
	MinSizeForMultipart = 1 * 1024 * 1024
	// DefaultChunkThreads is the default number of concurrent download threads
	DefaultChunkThreads = 4
	// MinChunkSize is the minimum size for each chunk (256KB)
	MinChunkSize = 256 * 1024
	// DownloadBufferSize is the buffer size for reading data
	DownloadBufferSize = 32 * 1024
)

// ChunkInfo represents a download chunk
type ChunkInfo struct {
	Index      int
	Start      int64
	End        int64
	Downloaded int64
	TempPath   string
	Done       bool
	Error      error
}

// MultipartDownloader handles multi-threaded chunk downloads
type MultipartDownloader struct {
	threads      int
	minChunkSize int64
	client       *http.Client
	headers      map[string]string
}

// NewMultipartDownloader creates a new MultipartDownloader
func NewMultipartDownloader() *MultipartDownloader {
	return &MultipartDownloader{
		threads:      DefaultChunkThreads,
		minChunkSize: MinChunkSize,
		client: &http.Client{
			Timeout: 0, // No timeout for downloads
		},
		headers: make(map[string]string),
	}
}

// SetThreads sets the number of download threads
func (md *MultipartDownloader) SetThreads(threads int) {
	if threads > 0 && threads <= 16 {
		md.threads = threads
	}
}

// SetHeaders sets custom headers for requests
func (md *MultipartDownloader) SetHeaders(headers map[string]string) {
	md.headers = headers
}

// RangeCheckResult holds the result of checking range support
type RangeCheckResult struct {
	SupportsRange bool
	ContentLength int64
	Error         error
}

func (md *MultipartDownloader) probeByRange(ctx context.Context, url string) (supportsRange bool, totalSize int64, err error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return false, 0, err
	}

	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	req.Header.Set("Range", "bytes=0-0")
	req.Header.Set("Accept-Encoding", "identity")
	for key, value := range md.headers {
		req.Header.Set(key, value)
	}

	resp, err := md.client.Do(req)
	if err != nil {
		return false, 0, err
	}
	defer resp.Body.Close()
	_, _ = io.CopyN(io.Discard, resp.Body, 1) // drain at most 1 byte

	switch resp.StatusCode {
	case http.StatusPartialContent:
		supportsRange = true
	case http.StatusOK:
		supportsRange = false
	default:
		return false, 0, fmt.Errorf("range probe failed with status: %d", resp.StatusCode)
	}

	if cr := resp.Header.Get("Content-Range"); cr != "" {
		if n := totalSizeFromContentRange(cr); n > 0 {
			return supportsRange, n, nil
		}
	}

	if resp.ContentLength > 0 {
		// When range is honored, Content-Length is often 1; ignore that.
		if supportsRange && resp.ContentLength <= 1 {
			return supportsRange, 0, fmt.Errorf("range honored but total size missing")
		}
		return supportsRange, resp.ContentLength, nil
	}

	return supportsRange, 0, fmt.Errorf("unable to determine content length")
}

// CheckRangeSupport checks if the server supports range requests
func (md *MultipartDownloader) CheckRangeSupport(ctx context.Context, url string) RangeCheckResult {
	headSupportsRange := false
	headContentLength := int64(0)
	headStatus := 0
	headAcceptRanges := ""

	// Best-effort HEAD (some servers misreport Content-Length on HEAD for VOD URLs).
	if req, err := http.NewRequestWithContext(ctx, "HEAD", url, nil); err == nil {
		req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
		for key, value := range md.headers {
			req.Header.Set(key, value)
		}

		if resp, err := md.client.Do(req); err == nil && resp != nil {
			headStatus = resp.StatusCode
			headAcceptRanges = resp.Header.Get("Accept-Ranges")
			headSupportsRange = headAcceptRanges == "bytes"
			headContentLength = resp.ContentLength
			_ = resp.Body.Close()
		}
	}

	// Definitive probe using GET Range, which can provide total length via Content-Range.
	supportsRange, total, probeErr := md.probeByRange(ctx, url)
	if probeErr != nil {
		// If range probe fails, fall back to what we learned from HEAD.
		if headStatus == http.StatusOK && headContentLength > 0 {
			logger.Info("Range check fallback to HEAD: status=%d acceptRanges=%q len=%d err=%v", headStatus, headAcceptRanges, headContentLength, probeErr)
			return RangeCheckResult{SupportsRange: headSupportsRange, ContentLength: headContentLength, Error: nil}
		}
		return RangeCheckResult{Error: probeErr}
	}

	// Merge: if server honored range, always treat as supports range even if HEAD lied.
	finalSupports := supportsRange || headSupportsRange
	finalLen := total
	if finalLen <= 0 && headContentLength > 0 {
		finalLen = headContentLength
	}

	// Keep this as INFO: it explains 0MB/partial download issues in the field.
	logger.Info("Range check: head(status=%d acceptRanges=%q len=%d) probe(supports=%v total=%d) -> supports=%v total=%d",
		headStatus, headAcceptRanges, headContentLength,
		supportsRange, total,
		finalSupports, finalLen,
	)

	return RangeCheckResult{
		SupportsRange: finalSupports,
		ContentLength: finalLen,
		Error:         nil,
	}
}

// verifyRangeSupport verifies range support with an actual range request
func (md *MultipartDownloader) verifyRangeSupport(ctx context.Context, url string) bool {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return false
	}

	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	req.Header.Set("Range", "bytes=0-0")

	for key, value := range md.headers {
		req.Header.Set(key, value)
	}

	resp, err := md.client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()

	return resp.StatusCode == http.StatusPartialContent
}

// ProgressCallback is called to report download progress
type ProgressCallback func(downloaded, total int64)

// DownloadResult represents the result of a multipart download
type DownloadResult struct {
	OutputPath string
	TotalSize  int64
	Error      error
}

// Download performs a multipart download
func (md *MultipartDownloader) Download(
	ctx context.Context,
	url string,
	outputPath string,
	totalSize int64,
	onProgress ProgressCallback,
) DownloadResult {
	// Calculate chunk sizes
	chunks := md.calculateChunks(totalSize)
	numChunks := len(chunks)

	logger.Info("Starting multipart download: %d chunks, %d bytes total", numChunks, totalSize)

	// Create output directory
	if err := os.MkdirAll(filepath.Dir(outputPath), 0755); err != nil {
		return DownloadResult{Error: fmt.Errorf("failed to create output directory: %w", err)}
	}

	// Load or initialize resume state
	var st *multipartState
	if multipartStateExists(outputPath) {
		loaded, err := loadMultipartState(outputPath)
		if err == nil && loaded != nil && loaded.TotalSize == totalSize && loaded.Threads == md.threads && len(loaded.Chunks) == numChunks {
			st = loaded
		}
	}

	if st == nil {
		st = &multipartState{
			Version:   1,
			TotalSize: totalSize,
			Threads:   md.threads,
			Chunks:    make([]multipartStateChunk, numChunks),
		}
		for i := range chunks {
			st.Chunks[i] = multipartStateChunk{
				Index:      chunks[i].Index,
				Start:      chunks[i].Start,
				End:        chunks[i].End,
				Downloaded: 0,
				Done:       false,
			}
		}
		_ = saveMultipartState(outputPath, st)

		// Create the output file with proper size (new download only)
		file, err := os.Create(outputPath)
		if err != nil {
			return DownloadResult{Error: fmt.Errorf("failed to create output file: %w", err)}
		}

		// Pre-allocate file size for efficiency
		if err := file.Truncate(totalSize); err != nil {
			file.Close()
			return DownloadResult{Error: fmt.Errorf("failed to allocate file size: %w", err)}
		}
		file.Close()
	} else {
		// If output missing, we can't resume reliably. Reset and start fresh.
		if _, err := os.Stat(outputPath); err != nil {
			_ = os.Remove(multipartStatePath(outputPath))
			return md.Download(ctx, url, outputPath, totalSize, onProgress)
		}
	}

	// Progress tracking
	var totalDownloaded int64 = 0
	for i := range st.Chunks {
		atomic.AddInt64(&totalDownloaded, st.Chunks[i].Downloaded)
	}
	var progressMu sync.Mutex

	// Error channel
	errChan := make(chan error, numChunks)

	// WaitGroup for all chunks
	var wg sync.WaitGroup

	// Context with cancellation for all chunks
	downloadCtx, cancelDownload := context.WithCancel(ctx)
	defer cancelDownload()

	var stateMu sync.Mutex
	lastStateSave := time.Now()

	// Start downloading each chunk
	for i := range chunks {
		chunkIdx := i
		chunkStart := st.Chunks[chunkIdx].Start
		chunkEnd := st.Chunks[chunkIdx].End
		expectedSize := chunkEnd - chunkStart + 1
		already := st.Chunks[chunkIdx].Downloaded
		if already < 0 {
			already = 0
		}
		if already >= expectedSize {
			stateMu.Lock()
			st.Chunks[chunkIdx].Downloaded = expectedSize
			st.Chunks[chunkIdx].Done = true
			_ = saveMultipartState(outputPath, st)
			stateMu.Unlock()
			continue
		}

		wg.Add(1)
		go func(chunk *ChunkInfo) {
			defer wg.Done()

			err := md.downloadChunk(downloadCtx, url, outputPath, chunk, already, func(chunkDownloaded int64) {
				progressMu.Lock()
				// Calculate total downloaded across all chunks
				newTotal := atomic.AddInt64(&totalDownloaded, chunkDownloaded)
				progressMu.Unlock()

				// Persist resume state occasionally
				stateMu.Lock()
				st.Chunks[chunkIdx].Downloaded += chunkDownloaded
				if st.Chunks[chunkIdx].Downloaded >= expectedSize {
					st.Chunks[chunkIdx].Downloaded = expectedSize
					st.Chunks[chunkIdx].Done = true
				}
				if time.Since(lastStateSave) >= time.Second {
					_ = saveMultipartState(outputPath, st)
					lastStateSave = time.Now()
				}
				stateMu.Unlock()

				if onProgress != nil {
					onProgress(newTotal, totalSize)
				}
			})

			if err != nil {
				errChan <- fmt.Errorf("chunk %d failed: %w", chunk.Index, err)
				cancelDownload() // Cancel other chunks on error
			}
		}(&chunks[i])
	}

	// Wait for all chunks to complete
	wg.Wait()
	close(errChan)

	// Check for errors
	var firstError error
	for err := range errChan {
		if firstError == nil {
			firstError = err
		}
	}

	if firstError != nil {
		_ = saveMultipartState(outputPath, st)
		// Keep output+state for pause/resume (and for transient errors) - do NOT remove.
		if errors.Is(firstError, context.Canceled) || errors.Is(firstError, context.DeadlineExceeded) {
			// intentional no-op
		}
		return DownloadResult{Error: firstError}
	}

	_ = os.Remove(multipartStatePath(outputPath))
	logger.Info("Multipart download completed: %s", outputPath)

	return DownloadResult{
		OutputPath: outputPath,
		TotalSize:  totalSize,
		Error:      nil,
	}
}

// calculateChunks calculates the chunk boundaries
func (md *MultipartDownloader) calculateChunks(totalSize int64) []ChunkInfo {
	// Calculate optimal chunk size
	chunkSize := totalSize / int64(md.threads)
	if chunkSize < md.minChunkSize {
		chunkSize = md.minChunkSize
	}

	// Calculate number of chunks
	numChunks := int(totalSize / chunkSize)
	if totalSize%chunkSize != 0 {
		numChunks++
	}

	// Limit to configured threads
	if numChunks > md.threads {
		numChunks = md.threads
		chunkSize = totalSize / int64(numChunks)
	}

	chunks := make([]ChunkInfo, numChunks)
	var start int64 = 0

	for i := 0; i < numChunks; i++ {
		end := start + chunkSize - 1
		if i == numChunks-1 {
			// Last chunk gets remaining bytes
			end = totalSize - 1
		}

		chunks[i] = ChunkInfo{
			Index: i,
			Start: start,
			End:   end,
		}

		start = end + 1
	}

	return chunks
}

// downloadChunk downloads a single chunk
func (md *MultipartDownloader) downloadChunk(
	ctx context.Context,
	url string,
	outputPath string,
	chunk *ChunkInfo,
	alreadyDownloaded int64,
	onChunkProgress func(downloaded int64),
) error {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return err
	}

	start := chunk.Start + alreadyDownloaded
	if start < chunk.Start {
		start = chunk.Start
	}
	if start > chunk.End {
		// Nothing left to download
		chunk.Downloaded = chunk.End - chunk.Start + 1
		chunk.Done = true
		return nil
	}

	// Set headers
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, chunk.End))

	for key, value := range md.headers {
		req.Header.Set(key, value)
	}

	resp, err := md.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusPartialContent && resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	// Open file for writing at correct offset
	file, err := os.OpenFile(outputPath, os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer file.Close()

	// Seek to chunk start position
	if _, err := file.Seek(start, io.SeekStart); err != nil {
		return err
	}

	// Download with progress tracking
	buf := make([]byte, DownloadBufferSize)
	var chunkDownloaded int64 = alreadyDownloaded
	expectedSize := chunk.End - chunk.Start + 1
	remaining := expectedSize - chunkDownloaded
	if remaining < 0 {
		remaining = 0
	}

	for remaining > 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			// Don't write more than expected
			toWrite := int64(n)
			if toWrite > remaining {
				toWrite = remaining
			}

			_, writeErr := file.Write(buf[:toWrite])
			if writeErr != nil {
				return writeErr
			}

			if onChunkProgress != nil {
				onChunkProgress(toWrite)
			}
			chunkDownloaded += toWrite
			remaining -= toWrite
		}

		if readErr != nil {
			if readErr == io.EOF {
				break
			}
			return readErr
		}
	}

	chunk.Downloaded = chunkDownloaded
	chunk.Done = true

	logger.Debug("Chunk %d completed: %d bytes", chunk.Index, chunkDownloaded)
	return nil
}

// MultipartProgressTracker tracks progress for multipart downloads
type MultipartProgressTracker struct {
	totalSize      int64
	downloaded     int64
	lastReportTime time.Time
	lastDownloaded int64
	onProgress     func(downloaded, total int64, speed int64)
	mu             sync.Mutex
}

// NewMultipartProgressTracker creates a new progress tracker
func NewMultipartProgressTracker(totalSize int64, onProgress func(downloaded, total int64, speed int64)) *MultipartProgressTracker {
	return &MultipartProgressTracker{
		totalSize:      totalSize,
		lastReportTime: time.Now(),
		onProgress:     onProgress,
	}
}

// Update updates the progress
func (t *MultipartProgressTracker) Update(additionalBytes int64) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.downloaded += additionalBytes

	// Report progress at most once per 100ms
	now := time.Now()
	if now.Sub(t.lastReportTime) >= 100*time.Millisecond {
		speed := int64(float64(t.downloaded-t.lastDownloaded) / now.Sub(t.lastReportTime).Seconds())
		t.lastDownloaded = t.downloaded
		t.lastReportTime = now

		if t.onProgress != nil {
			t.onProgress(t.downloaded, t.totalSize, speed)
		}
	}
}

// GetDownloaded returns the total downloaded bytes
func (t *MultipartProgressTracker) GetDownloaded() int64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.downloaded
}

// ShouldUseMultipart determines if multipart download should be used
func ShouldUseMultipart(supportsRange bool, fileSize int64) bool {
	return supportsRange && fileSize > MinSizeForMultipart
}
