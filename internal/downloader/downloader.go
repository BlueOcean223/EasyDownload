package downloader

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"EasyDownload/internal/logger"
	"EasyDownload/internal/utils"
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

	// Album fields (for Douyin albums)
	IsAlbum        bool  `json:"isAlbum"`        // Whether this is an album download
	AlbumTotal     int   `json:"albumTotal"`     // Total number of images in album
	AlbumCompleted int   `json:"albumCompleted"` // Number of completed images

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
	baseName := utils.SanitizeFileName(title, 100)
	usedFallbackName := false
	if baseName == "" {
		usedFallbackName = true
		safeSource := utils.SanitizeFileName(source, 100)
		if safeSource == "" {
			safeSource = "video"
		}
		h := fnv.New32a()
		_, _ = h.Write([]byte(id))
		hash := fmt.Sprintf("%08x", h.Sum32())
		baseName = fmt.Sprintf("video_%s_%d_%s", safeSource, time.Now().Unix(), hash)
	}
	baseName = utils.SanitizeFileName(baseName, 100)
	if baseName == "" {
		baseName = fmt.Sprintf("video_%d", time.Now().UnixNano())
	}
	fileName := ensureUniqueFileName(dm.downloadDir, baseName, ".mp4")
	if usedFallbackName {
		logger.Info("Filename fallback used: id=%q -> %q", shortenForLog(id, 120), fileName)
	}

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

// cleanupTempFilesByPrefix removes temp files in dir that match prefix + any suffix from suffixes.
// Uses os.ReadDir + exact string matching to avoid glob metacharacter injection.
func cleanupTempFilesByPrefix(dir, prefix string, suffixes []string) {
	if strings.TrimSpace(dir) == "" || strings.TrimSpace(prefix) == "" || len(suffixes) == 0 {
		return
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		logger.Warn("[CancelTask] Failed to read dir for cleanup: dir=%s, err=%v", dir, err)
		return
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		for _, suffix := range suffixes {
			if strings.HasSuffix(name, suffix) {
				fullPath := filepath.Join(dir, name)
				logger.Debug("[CancelTask] Removing temp file: %s", fullPath)
				if err := os.Remove(fullPath); err != nil && !os.IsNotExist(err) {
					logger.Warn("[CancelTask] Failed to remove %s: %v", fullPath, err)
				} else if err == nil {
					logger.Debug("[CancelTask] Successfully removed: %s", fullPath)
				}
				break
			}
		}
	}
}

// CancelTask cancels a task
func (dm *DownloadManager) CancelTask(id string) error {
	dm.tasksMu.RLock()
	task, exists := dm.tasks[id]
	dm.tasksMu.RUnlock()

	if !exists {
		return fmt.Errorf("task %s not found", id)
	}

	// Capture fields under lock, then release before file operations
	task.mu.Lock()
	logger.Info("[CancelTask] Cancelling task: id=%s, status=%s, filePath=%s", id, task.Status, task.FilePath)
	if task.cancel != nil {
		task.cancel()
	}
	task.Status = StatusCancelled
	filePath := task.FilePath
	title := task.Title
	task.mu.Unlock()

	// All file operations below are done outside the lock to avoid blocking

	// Remove partial file and related temp files
	logger.Debug("[CancelTask] Removing main file: %s", filePath)
	if err := os.Remove(filePath); err != nil && !os.IsNotExist(err) {
		logger.Warn("[CancelTask] Failed to remove main file: %v", err)
	}

	// Remove Bilibili DASH temp files (_video.m4s and _audio.m4s)
	// For multi-part Bilibili downloads, files are named: {title}_P{n}_{partName}_video.m4s
	// We use prefix matching to find all related temp files
	if strings.HasSuffix(filePath, ".mp4") {
		basePath := strings.TrimSuffix(filePath, ".mp4")
		dir := filepath.Dir(filePath)
		baseName := filepath.Base(basePath)

		logger.Debug("[CancelTask] basePath=%s, dir=%s, baseName=%s", basePath, dir, baseName)

		// Remove single file temp files (always safe to try)
		tempFiles := []string{
			basePath + "_video.m4s",
			basePath + "_audio.m4s",
			basePath + "_video.m4s.edstate.json",
			basePath + "_audio.m4s.edstate.json",
		}
		for _, tf := range tempFiles {
			logger.Debug("[CancelTask] Attempting to remove: %s", tf)
			if err := os.Remove(tf); err != nil && !os.IsNotExist(err) {
				logger.Warn("[CancelTask] Failed to remove %s: %v", tf, err)
			} else if err == nil {
				logger.Debug("[CancelTask] Successfully removed: %s", tf)
			}
		}

		// For Bilibili multi-part downloads:
		// - task.Title format: "视频标题 - P2 分P名" (with " - Px " separator)
		// - temp file format: "视频标题_P2_分P名_video.m4s" (with "_Px_" separator)
		// We need to match ONLY the specific part being cancelled, not all parts

		cleanupPrefix := baseName

		// Check if this is a multi-part download by looking for " - P" in title
		if idx := strings.Index(title, " - P"); idx > 0 {
			// This is a multi-part download - only clean THIS specific part
			// Extract base video title and part info
			videoBaseTitle := title[:idx]
			partInfo := title[idx+3:] // Skip " - P", get "x 分P名"

			// Extract part number (e.g., "9" from "9 代码")
			partNum := ""
			for i, c := range partInfo {
				if c >= '0' && c <= '9' {
					partNum += string(c)
				} else {
					// After digits, the rest is part name with leading space
					if i > 0 && len(partInfo) > i {
						break
					}
				}
			}

			if partNum != "" {
				sanitizedBaseTitle := utils.SanitizeFileName(videoBaseTitle, 100)
				// Build prefix for this specific part: "视频标题_P9_"
				cleanupPrefix = sanitizedBaseTitle + "_P" + partNum + "_"
			}
		}

		cleanupSuffixes := []string{
			"_video.m4s",
			"_audio.m4s",
			"_video.m4s.edstate.json",
			"_audio.m4s.edstate.json",
			".edstate.json",
		}
		logger.Debug("[CancelTask] Cleaning temp files by prefix: dir=%s, prefix=%s", dir, cleanupPrefix)
		cleanupTempFilesByPrefix(dir, cleanupPrefix, cleanupSuffixes)
	}

	// Remove multipart download state file (.edstate.json)
	// This is created by multipart downloader to track chunk progress
	stateFile := filePath + ".edstate.json"
	logger.Debug("[CancelTask] Removing state file: %s", stateFile)
	if err := os.Remove(stateFile); err != nil && !os.IsNotExist(err) {
		logger.Warn("[CancelTask] Failed to remove state file: %v", err)
	}

	// Remove Douyin album temp directory (.albumtmp)
	// This contains downloaded images and state.json for album downloads
	if strings.HasSuffix(filePath, ".zip") {
		albumTmp := filePath + ".albumtmp"
		logger.Debug("[CancelTask] Removing album temp dir: %s", albumTmp)
		if err := os.RemoveAll(albumTmp); err != nil {
			logger.Warn("[CancelTask] Failed to remove album temp dir: %v", err)
		}
	}

	logger.Info("[CancelTask] Task cancelled successfully: id=%s", id)
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
	// Heuristic: flag obviously tiny WeChat files (likely chunk/preload) to help diagnostics.
	if strings.ToLower(strings.TrimSpace(task.Source)) == "wechat" && checkResult.ContentLength > 0 && checkResult.ContentLength < 3*1024*1024 {
		logger.Warn("[WeChat Download] Suspiciously small content length (%d bytes) for %s", checkResult.ContentLength, task.Title)
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
		if resp.StatusCode == http.StatusPartialContent {
			if total := totalSizeFromContentRange(resp.Header.Get("Content-Range")); total > 0 {
				task.FileSize = total
			} else {
				task.FileSize = resp.ContentLength
			}
		} else {
			task.FileSize = resp.ContentLength
		}
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
				// Do not fail the download on decrypt error; keep the original file so user can retry with a fresh key.
				return nil
			} else {
				logger.Info("Video decryption completed: %s", taskFilePath)
				// Validate the decrypted file format
				if !ValidateVideoFormat(taskFilePath) {
					logger.Warn("Decrypted file may not be a valid video format: %s", taskFilePath)
					// Keep file anyway; upstream may have returned partial/chunk content.
					return nil
				}
			}
		}
	}

	return nil
}

func ensureUniqueFileName(dir, base, ext string) string {
	base = utils.SanitizeFileName(base, 100)
	if base == "" {
		base = fmt.Sprintf("video_%d", time.Now().UnixNano())
	}
	ext = strings.TrimSpace(ext)
	if ext == "" {
		ext = ".mp4"
	}

	exists := func(name string) bool {
		_, err := os.Stat(filepath.Join(dir, name))
		return err == nil
	}

	name := utils.SanitizeFileName(base, 100) + ext
	if !exists(name) {
		return name
	}

	for i := 2; i <= 99; i++ {
		candidateBase := utils.SanitizeFileName(fmt.Sprintf("%s_%d", base, i), 100)
		if candidateBase == "" {
			candidateBase = fmt.Sprintf("video_%d", time.Now().UnixNano())
		}
		candidate := candidateBase + ext
		if !exists(candidate) {
			return candidate
		}
	}

	return fmt.Sprintf("%s_%d%s", utils.SanitizeFileName(base, 100), time.Now().UnixNano(), ext)
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
		"id":             t.ID,
		"url":            t.URL,
		"title":          t.Title,
		"cover":          t.Cover,
		"source":         t.Source,
		"quality":        t.Quality,
		"filePath":       t.FilePath,
		"fileName":       t.FileName,
		"fileSize":       t.FileSize,
		"downloaded":     t.Downloaded,
		"progress":       t.Progress,
		"speed":          t.Speed,
		"status":         t.Status,
		"error":          t.Error,
		"createdAt":      t.CreatedAt,
		"completedAt":    t.CompletedAt,
		"retryCount":     t.RetryCount,
		"maxRetry":       t.MaxRetry,
		"lastError":      t.LastError,
		"decodeKey":      t.DecodeKey,
		"isAlbum":        t.IsAlbum,
		"albumTotal":     t.AlbumTotal,
		"albumCompleted": t.AlbumCompleted,
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
	ID             string         `json:"id"`
	URL            string         `json:"url"`
	Title          string         `json:"title"`
	Cover          string         `json:"cover"`
	Source         string         `json:"source"`
	Quality        string         `json:"quality"`
	FilePath       string         `json:"filePath"`
	FileName       string         `json:"fileName"`
	FileSize       int64          `json:"fileSize"`
	Downloaded     int64          `json:"downloaded"`
	Progress       float64        `json:"progress"`
	Status         DownloadStatus `json:"status"`
	Error          string         `json:"error"`
	CreatedAt      int64          `json:"createdAt"`
	CompletedAt    int64          `json:"completedAt"`
	RetryCount     int            `json:"retryCount"`
	MaxRetry       int            `json:"maxRetry"`
	LastError      string         `json:"lastError"`
	DecodeKey      string         `json:"decodeKey"`
	IsAlbum        bool           `json:"isAlbum"`
	AlbumTotal     int            `json:"albumTotal"`
	AlbumCompleted int            `json:"albumCompleted"`
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
			ID:             task.ID,
			URL:            task.URL,
			Title:          task.Title,
			Cover:          task.Cover,
			Source:         task.Source,
			Quality:        task.Quality,
			FilePath:       task.FilePath,
			FileName:       task.FileName,
			FileSize:       task.FileSize,
			Downloaded:     task.Downloaded,
			Progress:       task.Progress,
			Status:         task.Status,
			Error:          task.Error,
			CreatedAt:      task.CreatedAt,
			CompletedAt:    task.CompletedAt,
			RetryCount:     task.RetryCount,
			MaxRetry:       task.MaxRetry,
			LastError:      task.LastError,
			DecodeKey:      task.DecodeKey,
			IsAlbum:        task.IsAlbum,
			AlbumTotal:     task.AlbumTotal,
			AlbumCompleted: task.AlbumCompleted,
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
			ID:             taskState.ID,
			URL:            taskState.URL,
			Title:          taskState.Title,
			Cover:          taskState.Cover,
			Source:         taskState.Source,
			Quality:        taskState.Quality,
			FilePath:       taskState.FilePath,
			FileName:       taskState.FileName,
			FileSize:       taskState.FileSize,
			Downloaded:     taskState.Downloaded,
			Progress:       taskState.Progress,
			Status:         taskState.Status,
			Error:          taskState.Error,
			CreatedAt:      taskState.CreatedAt,
			CompletedAt:    taskState.CompletedAt,
			RetryCount:     taskState.RetryCount,
			MaxRetry:       taskState.MaxRetry,
			LastError:      taskState.LastError,
			DecodeKey:      taskState.DecodeKey,
			IsAlbum:        taskState.IsAlbum,
			AlbumTotal:     taskState.AlbumTotal,
			AlbumCompleted: taskState.AlbumCompleted,
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
