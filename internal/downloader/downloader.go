package downloader

import (
	"EasyDownload/internal/logger"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

func isLiveOrStreamURL(u string) bool {
	lu := strings.ToLower(strings.TrimSpace(u))
	if lu == "" {
		return false
	}
	return strings.Contains(lu, ".m3u8") || strings.Contains(lu, ".flv") || strings.Contains(lu, ".mpd")
}

func isLikelyWeChatVODURL(raw string) (bool, string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return false, "empty url"
	}
	if isLiveOrStreamURL(raw) {
		return false, "live/stream url"
	}
	lu := strings.ToLower(raw)
	if strings.Contains(lu, "startidx=") || strings.Contains(lu, "size=") {
		return false, "chunked url (startIdx/size)"
	}
	pu, err := url.Parse(raw)
	if err == nil {
		host := strings.ToLower(pu.Host)
		if strings.Contains(host, "finder.video.qq.com") || strings.Contains(host, "findermp.video.qq.com") {
			if !strings.Contains(lu, "stodownload") {
				return false, "not stodownload"
			}
			if !strings.Contains(lu, "encfilekey=") {
				return false, "missing encfilekey"
			}
		}
	}
	return true, ""
}

// DownloadStatus represents the status of a download
type DownloadStatus string

const (
	StatusPending     DownloadStatus = "pending"
	StatusDownloading DownloadStatus = "downloading"
	StatusPaused      DownloadStatus = "paused"
	StatusCompleted   DownloadStatus = "completed"
	StatusFailed      DownloadStatus = "failed"
	StatusCancelled   DownloadStatus = "cancelled"
	StatusRetrying    DownloadStatus = "retrying"
)

// DownloadFunc is a custom download function type for sources that need special handling (e.g., Bilibili DASH)
// Parameters:
//   - ctx: context for cancellation
//   - task: the download task (for reading metadata, NOT for writing - use callbacks instead)
//   - onProgress: callback to report progress (downloaded bytes, total bytes)
//   - onComplete: callback when download completes (output file path)
//
// Returns error if download fails, nil on success
type DownloadFunc func(ctx context.Context, task *DownloadTask, onProgress func(downloaded, total int64), onComplete func(outputPath string)) error

// Default retry settings
const (
	DefaultMaxRetry     = 3
	DefaultBaseDelay    = time.Second
	DefaultMaxDelay     = 30 * time.Second
	DefaultRetryEnabled = true
)

// DownloadTask represents a download task
type DownloadTask struct {
	ID          string         `json:"id"`
	URL         string         `json:"url"`
	Title       string         `json:"title"`
	Cover       string         `json:"cover"`
	Source      string         `json:"source"` // "wechat" or "bilibili"
	Quality     string         `json:"quality"`
	FilePath    string         `json:"filePath"`
	FileName    string         `json:"fileName"`
	FileSize    int64          `json:"fileSize"`
	Downloaded  int64          `json:"downloaded"`
	Progress    float64        `json:"progress"`
	Speed       int64          `json:"speed"` // bytes per second
	Status      DownloadStatus `json:"status"`
	Error       string         `json:"error"`
	CreatedAt   int64          `json:"createdAt"`
	CompletedAt int64          `json:"completedAt"`

	// Retry fields
	RetryCount int    `json:"retryCount"`
	MaxRetry   int    `json:"maxRetry"`
	LastError  string `json:"lastError"`

	// Decryption fields (for WeChat videos)
	DecodeKey string `json:"decodeKey"` // Base64-encoded decryption key

	// Custom downloader for sources that need special handling (e.g., Bilibili DASH format)
	customDownloader DownloadFunc

	cancel context.CancelFunc
	mu     sync.RWMutex
}

// DownloadManager manages all download tasks
type DownloadManager struct {
	tasks         map[string]*DownloadTask
	tasksMu       sync.RWMutex
	downloadDir   string
	maxConcurrent int
	activeTasks   int
	activeTasksMu sync.Mutex

	// Retry settings
	autoRetry bool
	maxRetry  int
	baseDelay time.Duration
	maxDelay  time.Duration

	// State persistence
	statePath string

	// Callbacks
	onProgress func(task *DownloadTask)
	onComplete func(task *DownloadTask)
	onError    func(task *DownloadTask, err error)
	onRetry    func(task *DownloadTask, attempt int, delay time.Duration)
}

// NewDownloadManager creates a new DownloadManager
func NewDownloadManager(downloadDir string, maxConcurrent int) *DownloadManager {
	if maxConcurrent <= 0 {
		maxConcurrent = 3
	}

	return &DownloadManager{
		tasks:         make(map[string]*DownloadTask),
		downloadDir:   downloadDir,
		maxConcurrent: maxConcurrent,
		autoRetry:     DefaultRetryEnabled,
		maxRetry:      DefaultMaxRetry,
		baseDelay:     DefaultBaseDelay,
		maxDelay:      DefaultMaxDelay,
	}
}

// SetRetryConfig configures retry behavior
func (dm *DownloadManager) SetRetryConfig(autoRetry bool, maxRetry int, baseDelay, maxDelay time.Duration) {
	dm.autoRetry = autoRetry
	if maxRetry > 0 {
		dm.maxRetry = maxRetry
	}
	if baseDelay > 0 {
		dm.baseDelay = baseDelay
	}
	if maxDelay > 0 {
		dm.maxDelay = maxDelay
	}
}

// SetStatePath sets the path for state persistence
func (dm *DownloadManager) SetStatePath(path string) {
	dm.statePath = path
}

// SetRetryCallback sets the retry callback
func (dm *DownloadManager) SetRetryCallback(callback func(task *DownloadTask, attempt int, delay time.Duration)) {
	dm.onRetry = callback
}

// GetMaxRetry returns the maximum retry count
func (dm *DownloadManager) GetMaxRetry() int {
	return dm.maxRetry
}

// GetMaxConcurrent returns the maximum concurrent downloads
func (dm *DownloadManager) GetMaxConcurrent() int {
	return dm.maxConcurrent
}

// SetMaxConcurrent sets the maximum concurrent downloads
func (dm *DownloadManager) SetMaxConcurrent(max int) {
	if max > 0 {
		dm.maxConcurrent = max
	}
}

// GetActiveTaskCount returns the number of currently active downloads
func (dm *DownloadManager) GetActiveTaskCount() int {
	dm.activeTasksMu.Lock()
	defer dm.activeTasksMu.Unlock()
	return dm.activeTasks
}

// SetDownloadDir sets the download directory
func (dm *DownloadManager) SetDownloadDir(dir string) error {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create download directory: %w", err)
	}
	dm.downloadDir = dir
	return nil
}

// GetDownloadDir returns the download directory
func (dm *DownloadManager) GetDownloadDir() string {
	return dm.downloadDir
}

// SetProgressCallback sets the progress callback
func (dm *DownloadManager) SetProgressCallback(callback func(task *DownloadTask)) {
	dm.onProgress = callback
}

// SetCompleteCallback sets the completion callback
func (dm *DownloadManager) SetCompleteCallback(callback func(task *DownloadTask)) {
	dm.onComplete = callback
}

// SetErrorCallback sets the error callback
func (dm *DownloadManager) SetErrorCallback(callback func(task *DownloadTask, err error)) {
	dm.onError = callback
}

// AddTask adds a new download task
func (dm *DownloadManager) AddTask(id, rawURL, title, cover, source, quality string) (*DownloadTask, error) {
	return dm.AddTaskWithDecodeKey(id, rawURL, title, cover, source, quality, "")
}

// AddTaskWithDecodeKey adds a new download task with an optional decryption key
func (dm *DownloadManager) AddTaskWithDecodeKey(id, rawURL, title, cover, source, quality, decodeKey string) (*DownloadTask, error) {
	dm.tasksMu.Lock()
	defer dm.tasksMu.Unlock()

	// Check if task already exists
	if _, exists := dm.tasks[id]; exists {
		return nil, fmt.Errorf("task with ID %s already exists", id)
	}

	// Final safety: reject obvious WeChat live/invalid/chunk URLs so users don't download corrupt files
	if strings.ToLower(strings.TrimSpace(source)) == "wechat" {
		if ok, reason := isLikelyWeChatVODURL(rawURL); !ok {
			return nil, fmt.Errorf("invalid wechat video url (%s)", reason)
		}
	}

	// For WeChat videos, handle quality parameter in URL
	// The X-snsvideoflag parameter tells the server which quality to return
	downloadURL := rawURL
	if strings.ToLower(strings.TrimSpace(source)) == "wechat" {
		// Remove any existing X-snsvideoflag parameter first
		// This ensures we don't have duplicate parameters
		if strings.Contains(downloadURL, "X-snsvideoflag=") {
			// Parse URL and remove the existing parameter
			parsedURL, err := url.Parse(downloadURL)
			if err == nil {
				query := parsedURL.Query()
				query.Del("X-snsvideoflag")
				parsedURL.RawQuery = query.Encode()
				downloadURL = parsedURL.String()
			}
		}
		
		// Add the selected quality parameter if specified
		if quality != "" {
			// quality contains the fileFormat value (e.g., "xWT...")
			if strings.Contains(downloadURL, "?") {
				downloadURL = downloadURL + "&X-snsvideoflag=" + quality
			} else {
				downloadURL = downloadURL + "?X-snsvideoflag=" + quality
			}
			logger.Info("[WeChat Download] Using quality format: %s", quality)
		} else {
			logger.Info("[WeChat Download] No quality specified, using original URL")
		}
		logger.Debug("[WeChat Download] Final URL: %s", downloadURL)
	}

	// Generate filename
	fileName := sanitizeFileName(title)
	if fileName == "" {
		fileName = fmt.Sprintf("video_%s", id)
	}
	fileName = fileName + ".mp4"

	task := &DownloadTask{
		ID:         id,
		URL:        downloadURL,
		Title:      title,
		Cover:      cover,
		Source:     source,
		Quality:    quality,
		FileName:   fileName,
		FilePath:   filepath.Join(dm.downloadDir, fileName),
		Status:     StatusPending,
		CreatedAt:  time.Now().Unix(),
		RetryCount: 0,
		MaxRetry:   dm.maxRetry,
		DecodeKey:  decodeKey,
	}

	dm.tasks[id] = task
	return task, nil
}

// AddTaskWithDownloader adds a new download task with a custom downloader function
// This is used for sources that need special handling (e.g., Bilibili DASH format)
func (dm *DownloadManager) AddTaskWithDownloader(id, url, title, cover, source, quality string, downloader DownloadFunc) (*DownloadTask, error) {
	task, err := dm.AddTask(id, url, title, cover, source, quality)
	if err != nil {
		return nil, err
	}
	task.customDownloader = downloader
	return task, nil
}

// SetCustomDownloader sets a custom downloader for the task
func (t *DownloadTask) SetCustomDownloader(downloader DownloadFunc) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.customDownloader = downloader
}

// GetCustomDownloader returns the custom downloader for the task
func (t *DownloadTask) GetCustomDownloader() DownloadFunc {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.customDownloader
}

// StartTask starts downloading a task
func (dm *DownloadManager) StartTask(id string) error {
	dm.tasksMu.RLock()
	task, exists := dm.tasks[id]
	dm.tasksMu.RUnlock()

	if !exists {
		return fmt.Errorf("task %s not found", id)
	}

	task.mu.Lock()
	if task.Status == StatusDownloading {
		task.mu.Unlock()
		return fmt.Errorf("task %s is already downloading", id)
	}
	task.mu.Unlock()

	// Check concurrent limit
	dm.activeTasksMu.Lock()
	if dm.activeTasks >= dm.maxConcurrent {
		dm.activeTasksMu.Unlock()
		return fmt.Errorf("maximum concurrent downloads reached")
	}
	dm.activeTasks++
	dm.activeTasksMu.Unlock()

	go dm.downloadTask(task)
	return nil
}

// PauseTask pauses a downloading task and saves progress
func (dm *DownloadManager) PauseTask(id string) error {
	dm.tasksMu.RLock()
	task, exists := dm.tasks[id]
	dm.tasksMu.RUnlock()

	if !exists {
		return fmt.Errorf("task %s not found", id)
	}

	task.mu.Lock()
	if task.Status != StatusDownloading && task.Status != StatusRetrying {
		task.mu.Unlock()
		return fmt.Errorf("task %s is not downloading", id)
	}

	if task.cancel != nil {
		task.cancel()
	}
	task.Status = StatusPaused
	task.Speed = 0 // Reset speed when paused
	downloaded := task.Downloaded
	task.mu.Unlock()

	logger.Info("Task %s paused at %d bytes", id, downloaded)

	// Auto-save state after pause
	if dm.statePath != "" {
		dm.SaveState()
	}

	return nil
}

// CancelTask cancels a task
func (dm *DownloadManager) CancelTask(id string) error {
	dm.tasksMu.RLock()
	task, exists := dm.tasks[id]
	dm.tasksMu.RUnlock()

	if !exists {
		return fmt.Errorf("task %s not found", id)
	}

	task.mu.Lock()
	defer task.mu.Unlock()

	if task.cancel != nil {
		task.cancel()
	}
	task.Status = StatusCancelled

	// Remove partial file
	os.Remove(task.FilePath)

	return nil
}

// RemoveTask removes a task from the manager
func (dm *DownloadManager) RemoveTask(id string) error {
	dm.tasksMu.Lock()
	defer dm.tasksMu.Unlock()

	task, exists := dm.tasks[id]
	if !exists {
		return fmt.Errorf("task %s not found", id)
	}

	if task.cancel != nil {
		task.cancel()
	}

	delete(dm.tasks, id)
	return nil
}

// GetTask returns a task by ID
func (dm *DownloadManager) GetTask(id string) (*DownloadTask, error) {
	dm.tasksMu.RLock()
	defer dm.tasksMu.RUnlock()

	task, exists := dm.tasks[id]
	if !exists {
		return nil, fmt.Errorf("task %s not found", id)
	}

	return task, nil
}

// GetAllTasks returns all tasks
func (dm *DownloadManager) GetAllTasks() []*DownloadTask {
	dm.tasksMu.RLock()
	defer dm.tasksMu.RUnlock()

	tasks := make([]*DownloadTask, 0, len(dm.tasks))
	for _, task := range dm.tasks {
		tasks = append(tasks, task)
	}
	return tasks
}

// calculateBackoffDelay calculates exponential backoff delay
func (dm *DownloadManager) calculateBackoffDelay(attempt int) time.Duration {
	// Exponential backoff: baseDelay * 2^attempt
	delay := dm.baseDelay * time.Duration(math.Pow(2, float64(attempt)))
	if delay > dm.maxDelay {
		delay = dm.maxDelay
	}
	return delay
}

// downloadTask performs the actual download with retry support
func (dm *DownloadManager) downloadTask(task *DownloadTask) {
	defer func() {
		dm.activeTasksMu.Lock()
		dm.activeTasks--
		dm.activeTasksMu.Unlock()
	}()

	logger.Info("Starting download task: %s (%s)", task.ID, task.Title)

	for {
		err := dm.performDownload(task)
		if err == nil {
			// Download completed successfully
			return
		}

		// Check if task was cancelled or paused
		task.mu.RLock()
		status := task.Status
		task.mu.RUnlock()

		if status == StatusCancelled || status == StatusPaused {
			return
		}

		// Check if we should retry
		task.mu.Lock()
		task.LastError = err.Error()
		canRetry := dm.autoRetry && task.RetryCount < task.MaxRetry
		task.mu.Unlock()

		if !canRetry {
			dm.handleError(task, err)
			return
		}

		// Perform retry with exponential backoff
		task.mu.Lock()
		task.RetryCount++
		currentRetry := task.RetryCount
		task.Status = StatusRetrying
		task.mu.Unlock()

		delay := dm.calculateBackoffDelay(currentRetry - 1)
		logger.Info("Retrying download task %s (attempt %d/%d) after %v: %v",
			task.ID, currentRetry, task.MaxRetry, delay, err)

		if dm.onRetry != nil {
			dm.onRetry(task, currentRetry, delay)
		}

		// Wait before retry
		time.Sleep(delay)

		// Check again if cancelled during wait
		task.mu.RLock()
		status = task.Status
		task.mu.RUnlock()

		if status == StatusCancelled || status == StatusPaused {
			return
		}
	}
}

// performDownload performs a single download attempt
func (dm *DownloadManager) performDownload(task *DownloadTask) error {
	ctx, cancel := context.WithCancel(context.Background())
	task.mu.Lock()
	task.cancel = cancel
	task.Status = StatusDownloading
	customDownloader := task.customDownloader
	task.mu.Unlock()

	var err error

	// Check if task has a custom downloader (e.g., Bilibili DASH)
	if customDownloader != nil {
		err = dm.performCustomDownload(ctx, task, customDownloader)
	} else {
		// Default HTTP download for simple URLs (e.g., WeChat)
		err = dm.performHTTPDownload(ctx, task)
	}

	if err != nil {
		return err
	}

	// Mark as completed (shared completion logic for all download types)
	task.mu.Lock()
	task.Status = StatusCompleted
	task.Progress = 100
	task.CompletedAt = time.Now().Unix()
	task.mu.Unlock()

	logger.Info("Download completed: %s (%s)", task.Title, task.FilePath)

	// Auto-save state after completion
	if dm.statePath != "" {
		dm.SaveState()
	}

	if dm.onComplete != nil {
		dm.onComplete(task)
	}

	return nil
}

// performCustomDownload executes a custom download function
func (dm *DownloadManager) performCustomDownload(ctx context.Context, task *DownloadTask, downloader DownloadFunc) error {
	lastUpdate := time.Now()
	var lastDownloaded int64 = 0

	err := downloader(ctx, task,
		// onProgress callback
		func(downloaded, total int64) {
			task.mu.Lock()
			task.Downloaded = downloaded
			task.FileSize = total
			if total > 0 {
				task.Progress = float64(downloaded) / float64(total) * 100
			}
			task.mu.Unlock()

			// Update speed every second
			if time.Since(lastUpdate) >= time.Second {
				task.mu.Lock()
				task.Speed = downloaded - lastDownloaded
				task.mu.Unlock()

				lastDownloaded = downloaded
				lastUpdate = time.Now()

				if dm.onProgress != nil {
					dm.onProgress(task)
				}
			}
		},
		// onComplete callback
		func(outputPath string) {
			task.mu.Lock()
			task.FilePath = outputPath
			task.mu.Unlock()
		},
	)

	return err
}

// performHTTPDownload performs a standard HTTP download with Range support
// It automatically decides whether to use multipart (chunked) download for large files
func (dm *DownloadManager) performHTTPDownload(ctx context.Context, task *DownloadTask) error {
	// Check if we're resuming a download - if so, use sequential download
	task.mu.RLock()
	downloaded := task.Downloaded
	taskURL := task.URL
	filePath := task.FilePath
	fileSize := task.FileSize
	task.mu.RUnlock()

	if downloaded > 0 {
		// If multipart resume state exists, resume with multipart (sequential can't safely continue from a
		// single offset because multipart downloads fill multiple ranges).
		if multipartStateExists(filePath) {
			totalSize := fileSize
			if totalSize <= 0 {
				if st, err := loadMultipartState(filePath); err == nil && st != nil {
					totalSize = st.TotalSize
				}
			}
			if totalSize > 0 {
				md := NewMultipartDownloader()
				md.SetHeaders(map[string]string{
					"Referer": "https://channels.weixin.qq.com/",
				})
				return dm.performMultipartHTTPDownload(ctx, task, md, totalSize)
			}
		}

		// Resuming a download - use sequential to continue from where we left off
		logger.Debug("Resuming download from %d bytes, using sequential download", downloaded)
		return dm.performSequentialHTTPDownload(ctx, task)
	}

	// For new downloads, check if multipart download is beneficial
	md := NewMultipartDownloader()
	md.SetHeaders(map[string]string{
		"Referer": "https://channels.weixin.qq.com/",
	})

	// Check range support and get file size
	checkResult := md.CheckRangeSupport(ctx, taskURL)
	if checkResult.Error != nil {
		logger.Debug("Failed to check range support: %v, falling back to sequential", checkResult.Error)
		return dm.performSequentialHTTPDownload(ctx, task)
	}

	// Update file size in task
	if checkResult.ContentLength > 0 {
		task.mu.Lock()
		task.FileSize = checkResult.ContentLength
		task.mu.Unlock()
	}

	// Decide whether to use multipart download
	if ShouldUseMultipart(checkResult.SupportsRange, checkResult.ContentLength) {
		logger.Info("Using multipart download for %s (size: %d bytes, supports range: %v)",
			task.Title, checkResult.ContentLength, checkResult.SupportsRange)
		return dm.performMultipartHTTPDownload(ctx, task, md, checkResult.ContentLength)
	}

	// Fall back to sequential download for small files or servers without range support
	logger.Debug("Using sequential download (size: %d, supports range: %v)",
		checkResult.ContentLength, checkResult.SupportsRange)
	return dm.performSequentialHTTPDownload(ctx, task)
}

// performMultipartHTTPDownload performs a multipart (chunked) download for large files
func (dm *DownloadManager) performMultipartHTTPDownload(ctx context.Context, task *DownloadTask, md *MultipartDownloader, totalSize int64) error {
	task.mu.RLock()
	filePath := task.FilePath
	task.mu.RUnlock()

	lastUpdate := time.Now()
	var lastDownloaded int64 = 0

	// Perform multipart download
	result := md.Download(ctx, task.URL, filePath, totalSize, func(downloaded, total int64) {
		task.mu.Lock()
		task.Downloaded = downloaded
		task.FileSize = total
		if total > 0 {
			task.Progress = float64(downloaded) / float64(total) * 100
		}
		task.mu.Unlock()

		// Update speed every second
		if time.Since(lastUpdate) >= time.Second {
			task.mu.Lock()
			task.Speed = downloaded - lastDownloaded
			task.mu.Unlock()

			lastDownloaded = downloaded
			lastUpdate = time.Now()

			if dm.onProgress != nil {
				dm.onProgress(task)
			}
		}
	})

	if result.Error != nil {
		return result.Error
	}

	// Handle decryption if needed
	return dm.handlePostDownloadDecryption(task)
}

// performSequentialHTTPDownload performs a standard sequential HTTP download
func (dm *DownloadManager) performSequentialHTTPDownload(ctx context.Context, task *DownloadTask) error {
	// Create HTTP request
	req, err := http.NewRequestWithContext(ctx, "GET", task.URL, nil)
	if err != nil {
		return err
	}

	// Set headers
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	req.Header.Set("Referer", "https://channels.weixin.qq.com/")

	// Support resume
	task.mu.RLock()
	downloaded := task.Downloaded
	task.mu.RUnlock()

	if downloaded > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", downloaded))
	}

	client := &http.Client{
		Timeout: 0, // No timeout for downloads
	}

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	// Check response status
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		return fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	// Get file size
	task.mu.Lock()
	if task.FileSize == 0 {
		task.FileSize = resp.ContentLength
	}
	task.mu.Unlock()

	// Create or open file
	var file *os.File
	task.mu.RLock()
	downloaded = task.Downloaded
	filePath := task.FilePath
	task.mu.RUnlock()

	if downloaded > 0 {
		file, err = os.OpenFile(filePath, os.O_APPEND|os.O_WRONLY, 0644)
	} else {
		// Ensure directory exists
		os.MkdirAll(filepath.Dir(filePath), 0755)
		file, err = os.Create(filePath)
	}
	if err != nil {
		return err
	}
	defer file.Close()

	// Download with progress tracking
	buf := make([]byte, 32*1024)
	lastUpdate := time.Now()

	task.mu.RLock()
	lastDownloaded := task.Downloaded
	task.mu.RUnlock()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			_, writeErr := file.Write(buf[:n])
			if writeErr != nil {
				return writeErr
			}

			task.mu.Lock()
			task.Downloaded += int64(n)
			if task.FileSize > 0 {
				task.Progress = float64(task.Downloaded) / float64(task.FileSize) * 100
			}
			task.mu.Unlock()

			// Update speed every second
			if time.Since(lastUpdate) >= time.Second {
				task.mu.Lock()
				task.Speed = task.Downloaded - lastDownloaded
				task.mu.Unlock()

				task.mu.RLock()
				lastDownloaded = task.Downloaded
				task.mu.RUnlock()

				lastUpdate = time.Now()

				if dm.onProgress != nil {
					dm.onProgress(task)
				}
			}
		}

		if readErr != nil {
			if readErr == io.EOF {
				break
			}
			return readErr
		}
	}

	// Handle decryption if needed
	return dm.handlePostDownloadDecryption(task)
}

// handlePostDownloadDecryption handles decryption after download completes
func (dm *DownloadManager) handlePostDownloadDecryption(task *DownloadTask) error {
	task.mu.RLock()
	decodeKey := task.DecodeKey
	taskFilePath := task.FilePath
	task.mu.RUnlock()

	if decodeKey != "" {
		// Some WeChat "stodownload" URLs may already return a normal MP4 even if decodeKey is present.
		// Never decrypt a file that already looks like a valid container, otherwise we corrupt it.
		if ValidateVideoFormat(taskFilePath) {
			logger.Info("Skip decryption (already valid video): %s", taskFilePath)
		} else {
			logger.Info("Decrypting video file: %s", taskFilePath)
			decryptor := NewVideoDecryptor()
			if err := decryptor.DecryptFile(taskFilePath, decodeKey); err != nil {
				logger.Error("Failed to decrypt video file: %v", err)
				// Don't fail the download, just log the error
				// The file is still saved, user can try manual decryption
			} else {
				logger.Info("Video decryption completed: %s", taskFilePath)
				// Validate the decrypted file format
				if !ValidateVideoFormat(taskFilePath) {
					logger.Warn("Decrypted file may not be a valid video format: %s", taskFilePath)
				}
			}
		}
	}

	return nil
}

// handleError handles download errors
func (dm *DownloadManager) handleError(task *DownloadTask, err error) {
	logger.Error("Download failed for task %s (%s): %v", task.ID, task.Title, err)

	task.mu.Lock()
	task.Status = StatusFailed
	task.Error = err.Error()
	task.mu.Unlock()

	// Auto-save state after failure
	if dm.statePath != "" {
		dm.SaveState()
	}

	if dm.onError != nil {
		dm.onError(task, err)
	}
}

// sanitizeFileName removes invalid characters from filename
func sanitizeFileName(name string) string {
	// Normalize whitespace and remove control characters. Windows file names cannot contain
	// many characters, and also cannot contain CR/LF which can break paths/logs.
	result := strings.TrimSpace(name)
	if result == "" {
		return ""
	}

	// Replace common line separators with spaces first.
	result = strings.NewReplacer(
		"\r", " ",
		"\n", " ",
		"\t", " ",
		"\u2028", " ",
		"\u2029", " ",
	).Replace(result)

	var b strings.Builder
	b.Grow(len(result))
	prevUnderscore := false
	for _, r := range result {
		// Drop replacement rune and ASCII control chars
		if r == utf8.RuneError || r == 0xFFFD {
			continue
		}
		if r < 32 || r == 127 {
			continue
		}

		// Convert any whitespace run to a single underscore
		if unicode.IsSpace(r) {
			if !prevUnderscore {
				b.WriteByte('_')
				prevUnderscore = true
			}
			continue
		}

		// Replace invalid Windows filename chars
		switch r {
		case '/', '\\', ':', '*', '?', '"', '<', '>', '|':
			if !prevUnderscore {
				b.WriteByte('_')
				prevUnderscore = true
			}
			continue
		}

		b.WriteRune(r)
		prevUnderscore = false
	}

	out := b.String()
	out = strings.Trim(out, " ._")
	if out == "" {
		return ""
	}

	// Avoid Windows reserved device names.
	upper := strings.ToUpper(out)
	switch upper {
	case "CON", "PRN", "AUX", "NUL",
		"COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9",
		"LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9":
		out = "_" + out
	}

	// Limit length to 100 bytes, but keep UTF-8 rune boundaries.
	if len(out) > 100 {
		var cut strings.Builder
		cut.Grow(100)
		n := 0
		for _, r := range out {
			rl := utf8.RuneLen(r)
			if rl <= 0 || n+rl > 100 {
				break
			}
			cut.WriteRune(r)
			n += rl
		}
		out = strings.Trim(cut.String(), " ._")
	}

	return out
}

func replaceAll(s, old, new string) string {
	result := ""
	for _, c := range s {
		if string(c) == old {
			result += new
		} else {
			result += string(c)
		}
	}
	return result
}

// RetryTask manually retries a failed task
func (dm *DownloadManager) RetryTask(id string) error {
	dm.tasksMu.RLock()
	task, exists := dm.tasks[id]
	dm.tasksMu.RUnlock()

	if !exists {
		return fmt.Errorf("task %s not found", id)
	}

	task.mu.Lock()
	if task.Status != StatusFailed {
		task.mu.Unlock()
		return fmt.Errorf("task %s is not in failed state", id)
	}
	// Reset retry count for manual retry
	task.RetryCount = 0
	task.Status = StatusPending
	task.Error = ""
	task.LastError = ""
	task.mu.Unlock()

	return dm.StartTask(id)
}

// GetPendingTasks returns all tasks that are pending or can be resumed
func (dm *DownloadManager) GetPendingTasks() []*DownloadTask {
	dm.tasksMu.RLock()
	defer dm.tasksMu.RUnlock()

	var pending []*DownloadTask
	for _, task := range dm.tasks {
		task.mu.RLock()
		status := task.Status
		task.mu.RUnlock()

		if status == StatusPending || status == StatusPaused || status == StatusFailed {
			pending = append(pending, task)
		}
	}
	return pending
}

// TaskToJSON returns task info as a map (for JSON serialization)
func (t *DownloadTask) TaskToJSON() map[string]interface{} {
	t.mu.RLock()
	defer t.mu.RUnlock()

	return map[string]interface{}{
		"id":          t.ID,
		"url":         t.URL,
		"title":       t.Title,
		"cover":       t.Cover,
		"source":      t.Source,
		"quality":     t.Quality,
		"filePath":    t.FilePath,
		"fileName":    t.FileName,
		"fileSize":    t.FileSize,
		"downloaded":  t.Downloaded,
		"progress":    t.Progress,
		"speed":       t.Speed,
		"status":      t.Status,
		"error":       t.Error,
		"createdAt":   t.CreatedAt,
		"completedAt": t.CompletedAt,
		"retryCount":  t.RetryCount,
		"maxRetry":    t.MaxRetry,
		"lastError":   t.LastError,
		"decodeKey":   t.DecodeKey,
	}
}

// Thread-safe getter/setter methods for DownloadTask

// SetCancel sets the cancel function for the task
func (t *DownloadTask) SetCancel(cancel context.CancelFunc) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.cancel = cancel
}

// GetStatus returns the task status
func (t *DownloadTask) GetStatus() DownloadStatus {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.Status
}

// SetStatus sets the task status
func (t *DownloadTask) SetStatus(status DownloadStatus) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.Status = status
}

// GetProgress returns the task progress
func (t *DownloadTask) GetProgress() float64 {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.Progress
}

// SetProgress sets the task progress
func (t *DownloadTask) SetProgress(progress float64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.Progress = progress
}

// GetFileSize returns the file size
func (t *DownloadTask) GetFileSize() int64 {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.FileSize
}

// SetFileSize sets the file size
func (t *DownloadTask) SetFileSize(size int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.FileSize = size
}

// GetDownloaded returns the downloaded bytes
func (t *DownloadTask) GetDownloaded() int64 {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.Downloaded
}

// SetDownloaded sets the downloaded bytes
func (t *DownloadTask) SetDownloaded(downloaded int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.Downloaded = downloaded
}

// GetSpeed returns the download speed
func (t *DownloadTask) GetSpeed() int64 {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.Speed
}

// SetSpeed sets the download speed
func (t *DownloadTask) SetSpeed(speed int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.Speed = speed
}

// SetError sets the error message
func (t *DownloadTask) SetError(err string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.Error = err
}

// SetFilePath sets the file path
func (t *DownloadTask) SetFilePath(path string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.FilePath = path
}

// SetCompletedAt sets the completion timestamp
func (t *DownloadTask) SetCompletedAt(ts int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.CompletedAt = ts
}

// DownloadState represents the persisted state of downloads
type DownloadState struct {
	Tasks []TaskState `json:"tasks"`
}

// TaskState represents a single task's persisted state
type TaskState struct {
	ID          string         `json:"id"`
	URL         string         `json:"url"`
	Title       string         `json:"title"`
	Cover       string         `json:"cover"`
	Source      string         `json:"source"`
	Quality     string         `json:"quality"`
	FilePath    string         `json:"filePath"`
	FileName    string         `json:"fileName"`
	FileSize    int64          `json:"fileSize"`
	Downloaded  int64          `json:"downloaded"`
	Progress    float64        `json:"progress"`
	Status      DownloadStatus `json:"status"`
	Error       string         `json:"error"`
	CreatedAt   int64          `json:"createdAt"`
	CompletedAt int64          `json:"completedAt"`
	RetryCount  int            `json:"retryCount"`
	MaxRetry    int            `json:"maxRetry"`
	LastError   string         `json:"lastError"`
	DecodeKey   string         `json:"decodeKey"`
}

// SaveState saves the current download state to disk
func (dm *DownloadManager) SaveState() error {
	if dm.statePath == "" {
		return fmt.Errorf("state path not configured")
	}

	dm.tasksMu.RLock()
	defer dm.tasksMu.RUnlock()

	state := DownloadState{
		Tasks: make([]TaskState, 0, len(dm.tasks)),
	}

	for _, task := range dm.tasks {
		task.mu.RLock()
		taskState := TaskState{
			ID:          task.ID,
			URL:         task.URL,
			Title:       task.Title,
			Cover:       task.Cover,
			Source:      task.Source,
			Quality:     task.Quality,
			FilePath:    task.FilePath,
			FileName:    task.FileName,
			FileSize:    task.FileSize,
			Downloaded:  task.Downloaded,
			Progress:    task.Progress,
			Status:      task.Status,
			Error:       task.Error,
			CreatedAt:   task.CreatedAt,
			CompletedAt: task.CompletedAt,
			RetryCount:  task.RetryCount,
			MaxRetry:    task.MaxRetry,
			LastError:   task.LastError,
			DecodeKey:   task.DecodeKey,
		}
		task.mu.RUnlock()

		// Only save non-completed tasks or recently completed ones
		if taskState.Status != StatusCompleted || time.Now().Unix()-taskState.CompletedAt < 86400 {
			state.Tasks = append(state.Tasks, taskState)
		}
	}

	// Ensure directory exists
	dir := filepath.Dir(dm.statePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create state directory: %w", err)
	}

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal state: %w", err)
	}

	if err := os.WriteFile(dm.statePath, data, 0644); err != nil {
		return fmt.Errorf("failed to write state file: %w", err)
	}

	logger.Debug("Download state saved to: %s", dm.statePath)
	return nil
}

// LoadState loads the download state from disk
func (dm *DownloadManager) LoadState() error {
	if dm.statePath == "" {
		return fmt.Errorf("state path not configured")
	}

	data, err := os.ReadFile(dm.statePath)
	if err != nil {
		if os.IsNotExist(err) {
			// No state file exists, that's okay
			logger.Debug("No download state file found at: %s", dm.statePath)
			return nil
		}
		return fmt.Errorf("failed to read state file: %w", err)
	}

	var state DownloadState
	if err := json.Unmarshal(data, &state); err != nil {
		// State file is corrupted, backup and start fresh
		logger.Error("Download state file corrupted, backing up")
		backupPath := dm.statePath + ".backup"
		os.Rename(dm.statePath, backupPath)
		return nil
	}

	dm.tasksMu.Lock()
	defer dm.tasksMu.Unlock()

	for _, taskState := range state.Tasks {
		// Skip if task already exists
		if _, exists := dm.tasks[taskState.ID]; exists {
			continue
		}

		task := &DownloadTask{
			ID:          taskState.ID,
			URL:         taskState.URL,
			Title:       taskState.Title,
			Cover:       taskState.Cover,
			Source:      taskState.Source,
			Quality:     taskState.Quality,
			FilePath:    taskState.FilePath,
			FileName:    taskState.FileName,
			FileSize:    taskState.FileSize,
			Downloaded:  taskState.Downloaded,
			Progress:    taskState.Progress,
			Status:      taskState.Status,
			Error:       taskState.Error,
			CreatedAt:   taskState.CreatedAt,
			CompletedAt: taskState.CompletedAt,
			RetryCount:  taskState.RetryCount,
			MaxRetry:    taskState.MaxRetry,
			LastError:   taskState.LastError,
			DecodeKey:   taskState.DecodeKey,
		}

		// Reset downloading/retrying tasks to paused state
		if task.Status == StatusDownloading || task.Status == StatusRetrying {
			task.Status = StatusPaused
		}

		dm.tasks[task.ID] = task
	}

	logger.Info("Loaded %d download tasks from state", len(state.Tasks))
	return nil
}

// ResumeTask resumes a paused task from where it left off
func (dm *DownloadManager) ResumeTask(id string) error {
	dm.tasksMu.RLock()
	task, exists := dm.tasks[id]
	dm.tasksMu.RUnlock()

	if !exists {
		return fmt.Errorf("task %s not found", id)
	}

	task.mu.Lock()
	if task.Status != StatusPaused {
		task.mu.Unlock()
		return fmt.Errorf("task %s is not paused", id)
	}

	// Verify the partial file exists and matches our progress
	downloaded := task.Downloaded
	filePath := task.FilePath
	customDownloader := task.customDownloader
	task.mu.Unlock()

	// For custom downloaders (e.g. Bilibili DASH), the task's FilePath may be the FINAL path
	// that doesn't exist until merge completes. Skip generic partial-file verification.
	if downloaded > 0 && customDownloader == nil {
		fileInfo, err := os.Stat(filePath)
		if err != nil {
			if os.IsNotExist(err) {
				// If multipart resume state exists, don't reset (partial data tracked via state file)
				if multipartStateExists(filePath) {
					// keep progress as-is
				} else {
					// File was deleted, reset progress
					task.mu.Lock()
					task.Downloaded = 0
					task.Progress = 0
					task.mu.Unlock()
					logger.Info("Partial file not found for task %s, restarting from beginning", id)
				}
			} else {
				return fmt.Errorf("failed to check partial file: %w", err)
			}
		} else if fileInfo.Size() != downloaded {
			// If multipart state exists, file size is not reliable because we pre-allocate.
			if multipartStateExists(filePath) {
				// keep Downloaded as-is
			} else {
				// File size doesn't match, use actual file size
				task.mu.Lock()
				task.Downloaded = fileInfo.Size()
				if task.FileSize > 0 {
					task.Progress = float64(task.Downloaded) / float64(task.FileSize) * 100
				}
				task.mu.Unlock()
				logger.Info("Adjusted progress for task %s to match file size: %d bytes", id, fileInfo.Size())
			}
		}
	}

	task.mu.Lock()
	task.Status = StatusPending
	resumeFrom := task.Downloaded
	task.mu.Unlock()

	logger.Info("Resuming task %s from %d bytes", id, resumeFrom)

	return dm.StartTask(id)
}

// GetDownloadingTaskCount returns the count of tasks currently downloading
func (dm *DownloadManager) GetDownloadingTaskCount() int {
	dm.tasksMu.RLock()
	defer dm.tasksMu.RUnlock()

	count := 0
	for _, task := range dm.tasks {
		task.mu.RLock()
		if task.Status == StatusDownloading || task.Status == StatusRetrying {
			count++
		}
		task.mu.RUnlock()
	}
	return count
}
