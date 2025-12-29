package downloader

import (
	"context"
	"fmt"
	"hash/fnv"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"EasyDownload/internal/download/wechat"
	"EasyDownload/internal/infra/logger"
	"EasyDownload/internal/utils"
)

// DownloadStatus represents the current state of a download task.
// The status transitions follow a state machine pattern:
//
//	pending -> downloading -> completed
//	                      \-> failed -> (manual retry) -> pending
//	                      \-> paused -> (resume) -> downloading
//	downloading -> retrying -> downloading (automatic retry on transient errors)
//	any state -> cancelled (user-initiated cancellation)
type DownloadStatus string

// Download status constants define all possible states for a download task.
const (
	// StatusPending indicates the task is queued but not yet started.
	StatusPending DownloadStatus = "pending"
	// StatusDownloading indicates the task is actively downloading.
	StatusDownloading DownloadStatus = "downloading"
	// StatusPaused indicates the task was paused by the user and can be resumed.
	StatusPaused DownloadStatus = "paused"
	// StatusCompleted indicates the download finished successfully.
	StatusCompleted DownloadStatus = "completed"
	// StatusFailed indicates the download failed after exhausting all retry attempts.
	StatusFailed DownloadStatus = "failed"
	// StatusCancelled indicates the user cancelled the download.
	StatusCancelled DownloadStatus = "cancelled"
	// StatusRetrying indicates the task is waiting to retry after a transient error.
	StatusRetrying DownloadStatus = "retrying"
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

// Default retry configuration constants.
// These values provide sensible defaults for automatic retry behavior.
const (
	// DefaultMaxRetry is the default number of retry attempts before marking a task as failed.
	DefaultMaxRetry = 3
	// DefaultBaseDelay is the initial delay before the first retry attempt.
	DefaultBaseDelay = time.Second
	// DefaultMaxDelay is the maximum delay between retry attempts (caps exponential backoff).
	DefaultMaxDelay = 30 * time.Second
	// DefaultRetryEnabled indicates whether automatic retry is enabled by default.
	DefaultRetryEnabled = true
)

// DownloadManager manages all download tasks and coordinates concurrent downloads.
// It provides a centralized interface for adding, starting, pausing, resuming,
// and cancelling download tasks.
//
// The manager enforces concurrency limits, handles automatic retries with
// exponential backoff, and persists state for recovery after application restart.
//
// All public methods are safe for concurrent access.
type DownloadManager struct {
	// tasks maps task IDs to their corresponding DownloadTask instances.
	tasks map[string]*DownloadTask
	// tasksMu protects the tasks map for concurrent access.
	tasksMu sync.RWMutex
	// downloadDir is the directory where downloaded files are saved.
	downloadDir string
	// maxConcurrent is the maximum number of simultaneous downloads allowed.
	maxConcurrent int
	// activeTasks tracks the current number of running downloads.
	activeTasks int
	// activeTasksMu protects activeTasks counter for concurrent access.
	activeTasksMu sync.Mutex

	// Retry configuration
	// autoRetry enables automatic retry on transient download failures.
	autoRetry bool
	// maxRetry is the maximum number of retry attempts per task.
	maxRetry int
	// baseDelay is the initial delay for exponential backoff.
	baseDelay time.Duration
	// maxDelay caps the maximum delay between retry attempts.
	maxDelay time.Duration

	// State persistence
	// statePath is the file path for saving/loading download state.
	statePath string

	// Event callbacks for external notification
	// onProgress is called periodically during download with updated progress.
	onProgress func(task *DownloadTask)
	// onComplete is called when a download finishes successfully.
	onComplete func(task *DownloadTask)
	// onError is called when a download fails after all retries.
	onError func(task *DownloadTask, err error)
	// onRetry is called before each retry attempt with the delay duration.
	onRetry func(task *DownloadTask, attempt int, delay time.Duration)
}

// NewDownloadManager creates a new DownloadManager with the specified download directory
// and maximum concurrent download limit.
//
// Parameters:
//   - downloadDir: the directory path where downloaded files will be saved
//   - maxConcurrent: maximum number of simultaneous downloads (defaults to 3 if <= 0)
//
// Returns a configured DownloadManager with default retry settings enabled.
func NewDownloadManager(downloadDir string, maxConcurrent int) *DownloadManager {
	// Apply default concurrency limit if invalid value provided
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

// SetRetryConfig configures the automatic retry behavior for failed downloads.
//
// Parameters:
//   - autoRetry: whether to automatically retry failed downloads
//   - maxRetry: maximum number of retry attempts (ignored if <= 0)
//   - baseDelay: initial delay before first retry, used for exponential backoff (ignored if <= 0)
//   - maxDelay: maximum delay between retries, caps exponential growth (ignored if <= 0)
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

// SetStatePath sets the file path for persisting download state.
// When set, the manager will automatically save state after task completion,
// pause, or failure, enabling recovery after application restart.
func (dm *DownloadManager) SetStatePath(path string) {
	dm.statePath = path
}

// SetRetryCallback sets a callback function that is invoked before each retry attempt.
// The callback receives the task, the current attempt number, and the delay before retry.
func (dm *DownloadManager) SetRetryCallback(callback func(task *DownloadTask, attempt int, delay time.Duration)) {
	dm.onRetry = callback
}

// GetMaxRetry returns the maximum number of retry attempts configured for the manager.
func (dm *DownloadManager) GetMaxRetry() int {
	return dm.maxRetry
}

// GetMaxConcurrent returns the maximum number of concurrent downloads allowed.
func (dm *DownloadManager) GetMaxConcurrent() int {
	return dm.maxConcurrent
}

// SetMaxConcurrent updates the maximum number of concurrent downloads.
// This only affects future download starts; currently running downloads are not affected.
// Values <= 0 are ignored.
func (dm *DownloadManager) SetMaxConcurrent(max int) {
	if max > 0 {
		dm.maxConcurrent = max
	}
}

// GetActiveTaskCount returns the number of downloads currently in progress.
// This count is protected by a mutex for thread-safe access.
func (dm *DownloadManager) GetActiveTaskCount() int {
	dm.activeTasksMu.Lock()
	defer dm.activeTasksMu.Unlock()
	return dm.activeTasks
}

// SetDownloadDir sets the directory where downloaded files will be saved.
// The directory is created if it does not exist.
//
// Returns an error if the directory cannot be created.
func (dm *DownloadManager) SetDownloadDir(dir string) error {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create download directory: %w", err)
	}
	dm.downloadDir = dir
	return nil
}

// GetDownloadDir returns the current download directory path.
func (dm *DownloadManager) GetDownloadDir() string {
	return dm.downloadDir
}

// SetProgressCallback sets a callback function that is invoked periodically
// during downloads to report progress updates.
// The callback receives the task with updated Downloaded, FileSize, Progress, and Speed fields.
func (dm *DownloadManager) SetProgressCallback(callback func(task *DownloadTask)) {
	dm.onProgress = callback
}

// SetCompleteCallback sets a callback function that is invoked when a download
// completes successfully.
func (dm *DownloadManager) SetCompleteCallback(callback func(task *DownloadTask)) {
	dm.onComplete = callback
}

// SetErrorCallback sets a callback function that is invoked when a download
// fails after exhausting all retry attempts.
func (dm *DownloadManager) SetErrorCallback(callback func(task *DownloadTask, err error)) {
	dm.onError = callback
}

// AddTask adds a new download task to the manager.
// This is a convenience wrapper around AddTaskWithDecodeKey with no decryption key.
//
// Parameters:
//   - id: unique identifier for the task
//   - rawURL: the download URL
//   - title: display name for the download
//   - cover: URL of the cover/thumbnail image
//   - source: content source identifier (e.g., "wechat", "bilibili", "douyin")
//   - quality: quality-specific metadata
//
// Returns the created task and nil on success, or nil and an error if the task
// already exists or the URL is invalid.
func (dm *DownloadManager) AddTask(id, rawURL, title, cover, source, quality string) (*DownloadTask, error) {
	return dm.AddTaskWithDecodeKey(id, rawURL, title, cover, source, quality, "")
}

// AddTaskWithDecodeKey adds a new download task with an optional decryption key.
// The decryption key is used for WeChat videos that are encrypted.
//
// Parameters:
//   - id: unique identifier for the task
//   - rawURL: the download URL
//   - title: display name for the download
//   - cover: URL of the cover/thumbnail image
//   - source: content source identifier (e.g., "wechat", "bilibili", "douyin")
//   - quality: quality-specific metadata (for WeChat, this is the fileFormat value)
//   - decodeKey: Base64-encoded decryption key (empty string if not encrypted)
//
// Returns the created task and nil on success, or nil and an error if:
//   - A task with the same ID already exists
//   - The URL fails validation for the specified source
func (dm *DownloadManager) AddTaskWithDecodeKey(id, rawURL, title, cover, source, quality, decodeKey string) (*DownloadTask, error) {
	dm.tasksMu.Lock()
	defer dm.tasksMu.Unlock()

	// Prevent duplicate task IDs
	if _, exists := dm.tasks[id]; exists {
		return nil, fmt.Errorf("task with ID %s already exists", id)
	}

	// Validate WeChat URLs to prevent downloading corrupt/partial files
	if strings.ToLower(strings.TrimSpace(source)) == "wechat" {
		if ok, reason := wechat.IsValidVODURL(rawURL); !ok {
			return nil, fmt.Errorf("invalid wechat video url (%s)", reason)
		}
	}

	// Process URL for WeChat videos with quality selection
	// The X-snsvideoflag parameter tells WeChat server which quality to return
	downloadURL := rawURL
	if strings.ToLower(strings.TrimSpace(source)) == "wechat" {
		// Remove any existing quality parameter to avoid duplicates
		if strings.Contains(downloadURL, "X-snsvideoflag=") {
			parsedURL, err := url.Parse(downloadURL)
			if err == nil {
				query := parsedURL.Query()
				query.Del("X-snsvideoflag")
				parsedURL.RawQuery = query.Encode()
				downloadURL = parsedURL.String()
			}
		}

		// Append the selected quality parameter if specified
		if quality != "" {
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

	// Generate a safe, unique filename from the title
	baseName := utils.SanitizeFileName(title, 100)
	usedFallbackName := false
	if baseName == "" {
		// Fallback: generate filename from source and ID hash when title is empty/invalid
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
	// Final sanitization pass to ensure filename is valid
	baseName = utils.SanitizeFileName(baseName, 100)
	if baseName == "" {
		baseName = fmt.Sprintf("video_%d", time.Now().UnixNano())
	}
	// Ensure unique filename in the download directory
	fileName := ensureUniqueFileName(dm.downloadDir, baseName, ".mp4")
	if usedFallbackName {
		logger.Info("Filename fallback used: id=%q -> %q", shortenForLog(id, 120), fileName)
	}

	// Create the task with initial pending status
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

// AddTaskWithDownloader adds a new download task with a custom downloader function.
// This is used for sources that need special handling (e.g., Bilibili DASH format
// which requires separate audio/video stream downloads and merging).
//
// Parameters:
//   - id: unique identifier for the task
//   - url: the download URL
//   - title: display name for the download
//   - cover: URL of the cover/thumbnail image
//   - source: content source identifier
//   - quality: quality-specific metadata
//   - downloader: custom download function to use instead of default HTTP download
//
// Returns the created task and nil on success, or nil and an error on failure.
func (dm *DownloadManager) AddTaskWithDownloader(id, url, title, cover, source, quality string, downloader DownloadFunc) (*DownloadTask, error) {
	task, err := dm.AddTask(id, url, title, cover, source, quality)
	if err != nil {
		return nil, err
	}
	task.customDownloader = downloader
	return task, nil
}

// StartTask starts downloading a task by its ID.
// The download runs in a separate goroutine.
//
// Returns an error if:
//   - The task ID is not found
//   - The task is already downloading
//   - The maximum concurrent download limit has been reached
//
// The task status transitions from pending to downloading upon success.
func (dm *DownloadManager) StartTask(id string) error {
	dm.tasksMu.RLock()
	task, exists := dm.tasks[id]
	dm.tasksMu.RUnlock()

	if !exists {
		return fmt.Errorf("task %s not found", id)
	}

	// Check current status to prevent duplicate starts
	task.mu.Lock()
	if task.Status == StatusDownloading {
		task.mu.Unlock()
		return fmt.Errorf("task %s is already downloading", id)
	}
	task.mu.Unlock()

	// Concurrency control: check and increment active task count atomically
	dm.activeTasksMu.Lock()
	if dm.activeTasks >= dm.maxConcurrent {
		dm.activeTasksMu.Unlock()
		return fmt.Errorf("maximum concurrent downloads reached")
	}
	dm.activeTasks++
	dm.activeTasksMu.Unlock()

	// Launch the download in a background goroutine
	go dm.downloadTask(task)
	return nil
}

// PauseTask pauses a downloading task and preserves its progress.
// The task can be resumed later using ResumeTask.
//
// Returns an error if:
//   - The task ID is not found
//   - The task is not currently downloading or retrying
//
// The task status transitions to paused and state is automatically saved.
func (dm *DownloadManager) PauseTask(id string) error {
	dm.tasksMu.RLock()
	task, exists := dm.tasks[id]
	dm.tasksMu.RUnlock()

	if !exists {
		return fmt.Errorf("task %s not found", id)
	}

	task.mu.Lock()
	// Only downloading or retrying tasks can be paused
	if task.Status != StatusDownloading && task.Status != StatusRetrying {
		task.mu.Unlock()
		return fmt.Errorf("task %s is not downloading", id)
	}

	// Cancel the download context to stop the active download
	if task.cancel != nil {
		task.cancel()
	}
	task.Status = StatusPaused
	task.Speed = 0 // Reset speed display when paused
	downloaded := task.Downloaded
	task.mu.Unlock()

	logger.Info("Task %s paused at %d bytes", id, downloaded)

	// Persist state immediately after pause for recovery
	if dm.statePath != "" {
		dm.SaveState()
	}

	return nil
}

// cleanupTempFilesByPrefix removes temporary files in the given directory that match
// the specified prefix and any of the given suffixes.
// Uses os.ReadDir with exact string matching to avoid glob metacharacter injection attacks.
//
// Parameters:
//   - dir: directory to search for temp files
//   - prefix: filename prefix to match
//   - suffixes: list of filename suffixes to match
//
// This function is used during task cancellation to clean up intermediate files
// created by multipart downloads (e.g., Bilibili DASH video/audio streams).
func cleanupTempFilesByPrefix(dir, prefix string, suffixes []string) {
	if strings.TrimSpace(dir) == "" || strings.TrimSpace(prefix) == "" || len(suffixes) == 0 {
		return
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		logger.Warn("[CancelTask] Failed to read dir for cleanup: dir=%s, err=%v", dir, err)
		return
	}

	// Iterate through directory entries looking for matching temp files
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		// Check if filename starts with our prefix
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		// Check if filename ends with any of our target suffixes
		for _, suffix := range suffixes {
			if strings.HasSuffix(name, suffix) {
				fullPath := filepath.Join(dir, name)
				logger.Debug("[CancelTask] Removing temp file: %s", fullPath)
				if err := os.Remove(fullPath); err != nil && !os.IsNotExist(err) {
					logger.Warn("[CancelTask] Failed to remove %s: %v", fullPath, err)
				} else if err == nil {
					logger.Debug("[CancelTask] Successfully removed: %s", fullPath)
				}
				break // Found matching suffix, move to next file
			}
		}
	}
}

// CancelTask cancels a download task and cleans up all associated files.
// This includes the partial download file, temporary stream files (for Bilibili DASH),
// multipart state files, and album temp directories (for Douyin).
//
// The task status is set to cancelled and the download goroutine is stopped.
// Unlike PauseTask, a cancelled task cannot be resumed.
//
// Returns an error if the task ID is not found.
func (dm *DownloadManager) CancelTask(id string) error {
	dm.tasksMu.RLock()
	task, exists := dm.tasks[id]
	dm.tasksMu.RUnlock()

	if !exists {
		return fmt.Errorf("task %s not found", id)
	}

	// Capture fields under lock, then release before file operations
	// to avoid holding the lock during potentially slow I/O
	task.mu.Lock()
	logger.Info("[CancelTask] Cancelling task: id=%s, status=%s, filePath=%s", id, task.Status, task.FilePath)
	// Stop the active download by cancelling its context
	if task.cancel != nil {
		task.cancel()
	}
	task.Status = StatusCancelled
	filePath := task.FilePath
	title := task.Title
	source := strings.ToLower(strings.TrimSpace(task.Source))
	task.mu.Unlock()

	// All file cleanup operations are done outside the lock to avoid blocking

	// Remove the main download file (partial or complete)
	logger.Debug("[CancelTask] Removing main file: %s", filePath)
	if err := os.Remove(filePath); err != nil && !os.IsNotExist(err) {
		logger.Warn("[CancelTask] Failed to remove main file: %v", err)
	}

	// Clean up Bilibili DASH temporary files
	// DASH format downloads create separate _video.m4s and _audio.m4s files
	// that need to be merged into the final .mp4
	if strings.HasSuffix(filePath, ".mp4") {
		basePath := strings.TrimSuffix(filePath, ".mp4")
		dir := filepath.Dir(filePath)
		baseName := filepath.Base(basePath)

		logger.Debug("[CancelTask] basePath=%s, dir=%s, baseName=%s", basePath, dir, baseName)

		// Remove known temp files for single-file downloads
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

		// Handle Bilibili multi-part video cleanup:
		// Multi-part titles have format: "视频标题 - P2 分P名"
		// Temp files have format: "视频标题_P2_分P名_video.m4s"
		// We must match only the specific part being cancelled, not all parts
		cleanupPrefix := baseName

		// Detect multi-part download by checking for " - P" separator in title
		if idx := strings.Index(title, " - P"); idx > 0 {
			// Extract the base video title (before " - P")
			videoBaseTitle := title[:idx]
			// Extract part info (after " - P")
			partInfo := title[idx+3:]

			// Parse the part number from the part info (e.g., "9 代码" -> "9")
			partNum := ""
			for i, c := range partInfo {
				if c >= '0' && c <= '9' {
					partNum += string(c)
				} else {
					// Non-digit found, stop parsing
					if i > 0 && len(partInfo) > i {
						break
					}
				}
			}

			// Build cleanup prefix for this specific part only
			if partNum != "" {
				sanitizedBaseTitle := utils.SanitizeFileName(videoBaseTitle, 100)
				cleanupPrefix = sanitizedBaseTitle + "_P" + partNum + "_"
			}
		}

		// Define suffixes for temp files that should be cleaned up
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

	// Remove multipart download state file
	// This JSON file tracks chunk download progress for resumable downloads
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

	// XiaoHongShu cleanup:
	// - .albumtmp directory for resumable album downloads
	// - partial video files (written directly, not .tmp)
	if source == "xiaohongshu" {
		// Clean up album temp directory
		albumTmp := utils.AlbumTempDir(filePath)
		logger.Debug("[CancelTask] Removing xiaohongshu album temp dir: %s", albumTmp)
		if err := os.RemoveAll(albumTmp); err != nil {
			logger.Warn("[CancelTask] Failed to remove xiaohongshu album temp dir: %v", err)
		}
	}

	logger.Info("[CancelTask] Task cancelled successfully: id=%s", id)
	return nil
}

// RemoveTask removes a task from the manager completely.
// If the task is currently downloading, it will be stopped first.
// This does not delete the downloaded file; use CancelTask for cleanup.
//
// Returns an error if the task ID is not found.
func (dm *DownloadManager) RemoveTask(id string) error {
	dm.tasksMu.Lock()
	defer dm.tasksMu.Unlock()

	task, exists := dm.tasks[id]
	if !exists {
		return fmt.Errorf("task %s not found", id)
	}

	// Stop any active download before removing
	if task.cancel != nil {
		task.cancel()
	}

	delete(dm.tasks, id)
	return nil
}

// GetTask retrieves a task by its ID.
// Returns the task and nil on success, or nil and an error if not found.
// Thread-safe.
func (dm *DownloadManager) GetTask(id string) (*DownloadTask, error) {
	dm.tasksMu.RLock()
	defer dm.tasksMu.RUnlock()

	task, exists := dm.tasks[id]
	if !exists {
		return nil, fmt.Errorf("task %s not found", id)
	}

	return task, nil
}

// GetAllTasks returns a slice of all tasks currently managed.
// The returned slice is a snapshot; modifications to the slice do not affect the manager.
// Thread-safe.
func (dm *DownloadManager) GetAllTasks() []*DownloadTask {
	dm.tasksMu.RLock()
	defer dm.tasksMu.RUnlock()

	tasks := make([]*DownloadTask, 0, len(dm.tasks))
	for _, task := range dm.tasks {
		tasks = append(tasks, task)
	}
	return tasks
}

// calculateBackoffDelay calculates the delay for exponential backoff.
// The formula is: baseDelay * 2^attempt, capped at maxDelay.
//
// Example with baseDelay=1s and maxDelay=30s:
//   - attempt 0: 1s
//   - attempt 1: 2s
//   - attempt 2: 4s
//   - attempt 3: 8s (and so on, up to 30s max)
func (dm *DownloadManager) calculateBackoffDelay(attempt int) time.Duration {
	delay := dm.baseDelay * time.Duration(math.Pow(2, float64(attempt)))
	if delay > dm.maxDelay {
		delay = dm.maxDelay
	}
	return delay
}

func waitForRetryDelay(task *DownloadTask, delay time.Duration) bool {
	if delay <= 0 {
		return true
	}
	deadline := time.Now().Add(delay)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return true
		}
		step := remaining
		if step > 200*time.Millisecond {
			step = 200 * time.Millisecond
		}
		time.Sleep(step)

		task.mu.RLock()
		status := task.Status
		task.mu.RUnlock()
		if status == StatusCancelled || status == StatusPaused {
			return false
		}
	}
}

// downloadTask is the main download loop that runs in a goroutine.
// It handles the retry logic with exponential backoff when downloads fail.
//
// The loop continues retrying until:
//   - Download succeeds
//   - Task is cancelled or paused
//   - Maximum retry attempts are exhausted
//
// This method decrements activeTasks when it exits (via defer).
func (dm *DownloadManager) downloadTask(task *DownloadTask) {
	// Ensure we decrement the active task counter when this goroutine exits
	defer func() {
		dm.activeTasksMu.Lock()
		dm.activeTasks--
		dm.activeTasksMu.Unlock()
	}()

	logger.Info("Starting download task: %s (%s)", task.ID, task.Title)

	// Retry loop: keeps attempting download until success or terminal condition
	for {
		err := dm.performDownload(task)
		if err == nil {
			// Download completed successfully - exit the retry loop
			return
		}

		// Check if task was cancelled or paused during download
		task.mu.RLock()
		status := task.Status
		task.mu.RUnlock()

		if status == StatusCancelled || status == StatusPaused {
			// User-initiated stop - exit without error handling
			return
		}

		// Determine if we should retry based on configuration and attempt count
		task.mu.Lock()
		task.LastError = err.Error()
		canRetry := dm.autoRetry && task.RetryCount < task.MaxRetry
		task.mu.Unlock()

		if !canRetry {
			// No more retries - mark as failed and exit
			dm.handleError(task, err)
			return
		}

		// Prepare for retry with exponential backoff
		task.mu.Lock()
		task.RetryCount++
		currentRetry := task.RetryCount
		task.Status = StatusRetrying
		task.mu.Unlock()

		// Calculate backoff delay (increases exponentially with each attempt)
		delay := dm.calculateBackoffDelay(currentRetry - 1)
		logger.Info("Retrying download task %s (attempt %d/%d) after %v: %v",
			task.ID, currentRetry, task.MaxRetry, delay, err)

		// Notify callback if registered
		if dm.onRetry != nil {
			dm.onRetry(task, currentRetry, delay)
		}

		// Wait before retry, but allow pause/cancel to interrupt the delay.
		if !waitForRetryDelay(task, delay) {
			return
		}
		// Continue to next iteration for retry attempt
	}
}

// performDownload executes a single download attempt.
// It creates a new context for cancellation and dispatches to either
// the custom downloader or the default HTTP downloader.
//
// On success, marks the task as completed and invokes the completion callback.
// On failure, returns the error for retry handling by the caller.
func (dm *DownloadManager) performDownload(task *DownloadTask) error {
	// Create a cancellable context for this download attempt
	ctx, cancel := context.WithCancel(context.Background())
	defer func() {
		cancel()
		task.mu.Lock()
		task.cancel = nil
		task.mu.Unlock()
	}()

	// Store cancel function and update status under lock
	task.mu.Lock()
	task.cancel = cancel
	task.Status = StatusDownloading
	customDownloader := task.customDownloader
	task.mu.Unlock()

	var err error

	// Dispatch to appropriate download method
	if customDownloader != nil {
		// Use custom downloader (e.g., Bilibili DASH format)
		err = dm.performCustomDownload(ctx, task, customDownloader)
	} else {
		// Use default HTTP download (e.g., WeChat videos)
		err = dm.performHTTPDownload(ctx, task)
	}

	if err != nil {
		return err
	}

	// Download successful - mark task as completed
	task.mu.Lock()
	task.Status = StatusCompleted
	task.Progress = 100
	task.CompletedAt = time.Now().Unix()
	task.mu.Unlock()

	logger.Info("Download completed: %s (%s)", task.Title, task.FilePath)

	// Persist state after successful completion
	if dm.statePath != "" {
		dm.SaveState()
	}

	// Invoke completion callback
	if dm.onComplete != nil {
		dm.onComplete(task)
	}

	return nil
}

// performCustomDownload executes a custom download function provided by the caller.
// This is used for sources requiring special handling (e.g., Bilibili DASH format
// which downloads separate video/audio streams and merges them).
//
// The function receives progress and completion callbacks to report status back
// to the download manager.
func (dm *DownloadManager) performCustomDownload(ctx context.Context, task *DownloadTask, downloader DownloadFunc) error {
	// Track timing for speed calculation
	lastUpdate := time.Now()
	var lastDownloaded int64 = 0

	err := downloader(ctx, task,
		// onProgress callback: updates task progress and calculates speed
		func(downloaded, total int64) {
			task.mu.Lock()
			task.Downloaded = downloaded
			task.FileSize = total
			if total > 0 {
				task.Progress = float64(downloaded) / float64(total) * 100
			}
			task.mu.Unlock()

			// Calculate and update speed every second
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
		// onComplete callback: updates the final output file path
		func(outputPath string) {
			task.mu.Lock()
			task.FilePath = outputPath
			task.mu.Unlock()
		},
	)

	return err
}

// performHTTPDownload performs a standard HTTP download with Range support.
// It automatically decides whether to use multipart (chunked) download for large files
// or sequential download for smaller files or servers without range support.
//
// Decision logic:
//   - Resuming with existing multipart state: use multipart download
//   - Resuming without multipart state: use sequential download (continue from offset)
//   - New download with range support and large file: use multipart download
//   - New download without range support or small file: use sequential download
func (dm *DownloadManager) performHTTPDownload(ctx context.Context, task *DownloadTask) error {
	// Read current task state
	task.mu.RLock()
	downloaded := task.Downloaded
	taskURL := task.URL
	filePath := task.FilePath
	fileSize := task.FileSize
	task.mu.RUnlock()

	// Handle resume scenarios
	if downloaded > 0 {
		// Check if we previously started a multipart download
		// Multipart downloads cannot be resumed with sequential mode because
		// they fill multiple non-contiguous ranges simultaneously
		if multipartStateExists(filePath) {
			totalSize := fileSize
			// Try to recover total size from state file if not in task
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

		// No multipart state - resume with sequential download from current offset
		logger.Debug("Resuming download from %d bytes, using sequential download", downloaded)
		return dm.performSequentialHTTPDownload(ctx, task)
	}

	// New download - probe server to determine best download strategy
	md := NewMultipartDownloader()
	md.SetHeaders(map[string]string{
		"Referer": "https://channels.weixin.qq.com/",
	})

	// Send HEAD request to check range support and get content length
	checkResult := md.CheckRangeSupport(ctx, taskURL)
	if checkResult.Error != nil {
		logger.Debug("Failed to check range support: %v, falling back to sequential", checkResult.Error)
		return dm.performSequentialHTTPDownload(ctx, task)
	}

	// Update file size in task from server response
	if checkResult.ContentLength > 0 {
		task.mu.Lock()
		task.FileSize = checkResult.ContentLength
		task.mu.Unlock()
	}

	// Warn about suspiciously small WeChat files (may indicate chunk/preload URLs)
	if strings.ToLower(strings.TrimSpace(task.Source)) == "wechat" && checkResult.ContentLength > 0 && checkResult.ContentLength < 3*1024*1024 {
		logger.Warn("[WeChat Download] Suspiciously small content length (%d bytes) for %s", checkResult.ContentLength, task.Title)
	}

	// Choose download strategy based on server capabilities and file size
	if ShouldUseMultipart(checkResult.SupportsRange, checkResult.ContentLength) {
		logger.Info("Using multipart download for %s (size: %d bytes, supports range: %v)",
			task.Title, checkResult.ContentLength, checkResult.SupportsRange)
		return dm.performMultipartHTTPDownload(ctx, task, md, checkResult.ContentLength)
	}

	// Use sequential download for small files or servers without range support
	logger.Debug("Using sequential download (size: %d, supports range: %v)",
		checkResult.ContentLength, checkResult.SupportsRange)
	return dm.performSequentialHTTPDownload(ctx, task)
}

// performMultipartHTTPDownload performs a multipart (chunked) download for large files.
// This method downloads multiple chunks of the file in parallel, improving throughput
// for large files on servers that support HTTP Range requests.
//
// Progress is tracked across all chunks and aggregated for reporting.
// The multipart state is persisted to enable resume after interruption.
func (dm *DownloadManager) performMultipartHTTPDownload(ctx context.Context, task *DownloadTask, md *MultipartDownloader, totalSize int64) error {
	task.mu.RLock()
	filePath := task.FilePath
	task.mu.RUnlock()

	// Track timing for speed calculation
	lastUpdate := time.Now()
	var lastDownloaded int64 = 0

	// Execute multipart download with progress callback
	result := md.Download(ctx, task.URL, filePath, totalSize, func(downloaded, total int64) {
		// Update task progress
		task.mu.Lock()
		task.Downloaded = downloaded
		task.FileSize = total
		if total > 0 {
			task.Progress = float64(downloaded) / float64(total) * 100
		}
		task.mu.Unlock()

		// Calculate and report speed every second
		if time.Since(lastUpdate) >= time.Second {
			task.mu.Lock()
			task.Speed = downloaded - lastDownloaded
			task.mu.Unlock()

			lastDownloaded = downloaded
			lastUpdate = time.Now()

			// Invoke progress callback
			if dm.onProgress != nil {
				dm.onProgress(task)
			}
		}
	})

	if result.Error != nil {
		return result.Error
	}

	// Post-download processing (e.g., decryption for WeChat videos)
	return dm.handlePostDownloadDecryption(task)
}

// performSequentialHTTPDownload performs a standard sequential HTTP download.
// This is the simplest download method, reading the file in a single stream.
// It supports resume via HTTP Range requests if the server supports it.
//
// Use this method for:
//   - Small files where multipart overhead isn't worth it
//   - Servers that don't support Range requests
//   - Resuming downloads that weren't started with multipart mode
func (dm *DownloadManager) performSequentialHTTPDownload(ctx context.Context, task *DownloadTask) error {
	task.mu.RLock()
	url := task.URL
	filePath := task.FilePath
	totalHint := task.FileSize
	lastDownloaded := task.Downloaded
	task.mu.RUnlock()

	headers := map[string]string{
		"User-Agent": "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36",
		"Referer":    "https://channels.weixin.qq.com/",
	}

	lastUpdate := time.Now()
	_, _, err := DownloadFileResumable(ctx, nil, url, filePath, ResumableDownloadOptions{
		Headers:         headers,
		TotalHint:       totalHint,
		MaxRangeRetries: 3,
	}, func(downloaded, total int64) {
		task.mu.Lock()
		if total > 0 {
			task.FileSize = total
		}
		task.Downloaded = downloaded
		if task.FileSize > 0 {
			task.Progress = float64(task.Downloaded) / float64(task.FileSize) * 100
		}
		task.mu.Unlock()

		// Calculate and report speed every second, and on completion.
		if time.Since(lastUpdate) >= time.Second || (total > 0 && downloaded >= total) {
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
	if err != nil {
		return err
	}

	// Post-download processing (decryption for WeChat if needed)
	return dm.handlePostDownloadDecryption(task)
}

// handlePostDownloadDecryption handles optional decryption after download completes.
// Some WeChat videos are encrypted and require decryption using a decode key.
//
// The function:
//  1. Checks if a decode key is present
//  2. Validates if the file is already a valid video (skip decryption if so)
//  3. Decrypts the file in-place if needed
//  4. Validates the decrypted result
//
// Decryption errors are logged but do not fail the download, allowing the user
// to retry decryption with a different key if needed.
func (dm *DownloadManager) handlePostDownloadDecryption(task *DownloadTask) error {
	task.mu.RLock()
	decodeKey := task.DecodeKey
	taskFilePath := task.FilePath
	task.mu.RUnlock()

	if decodeKey != "" {
		// Check if file is already a valid video container
		// Some WeChat URLs return unencrypted content even when a key is provided
		if wechat.ValidateVideoFormat(taskFilePath) {
			logger.Info("Skip decryption (already valid video): %s", taskFilePath)
		} else {
			// File appears encrypted, attempt decryption
			logger.Info("Decrypting video file: %s", taskFilePath)
			decryptor := wechat.NewVideoDecryptor()
			if err := decryptor.DecryptFile(taskFilePath, decodeKey); err != nil {
				logger.Error("Failed to decrypt video file: %v", err)
				// Keep the original file for potential retry with a different key
				return nil
			} else {
				logger.Info("Video decryption completed: %s", taskFilePath)
				// Verify the decrypted file is valid
				if !wechat.ValidateVideoFormat(taskFilePath) {
					logger.Warn("Decrypted file may not be a valid video format: %s", taskFilePath)
					// Keep file anyway - may be partial/chunk content from upstream
					return nil
				}
			}
		}
	}

	return nil
}

// ensureUniqueFileName generates a unique filename in the given directory.
// If the base filename already exists, it appends a numeric suffix (_2, _3, etc.).
//
// Parameters:
//   - dir: target directory
//   - base: base filename (without extension)
//   - ext: file extension (including dot, e.g., ".mp4")
//
// Returns a unique filename (just the name, not full path).
func ensureUniqueFileName(dir, base, ext string) string {
	// Sanitize base filename
	base = utils.SanitizeFileName(base, 100)
	if base == "" {
		base = fmt.Sprintf("video_%d", time.Now().UnixNano())
	}
	ext = strings.TrimSpace(ext)
	if ext == "" {
		ext = ".mp4"
	}

	// Helper to check if filename exists
	exists := func(name string) bool {
		_, err := os.Stat(filepath.Join(dir, name))
		return err == nil
	}

	// Try original name first
	name := utils.SanitizeFileName(base, 100) + ext
	if !exists(name) {
		return name
	}

	// Try with numeric suffixes (_2 through _99)
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

	// Fallback: use timestamp for guaranteed uniqueness
	return fmt.Sprintf("%s_%d%s", utils.SanitizeFileName(base, 100), time.Now().UnixNano(), ext)
}

// handleError handles download errors by updating task status and invoking the error callback.
// This is called when a download fails after exhausting all retry attempts.
func (dm *DownloadManager) handleError(task *DownloadTask, err error) {
	logger.Error("Download failed for task %s (%s): %v", task.ID, task.Title, err)

	// Update task status to failed
	task.mu.Lock()
	task.Status = StatusFailed
	task.Error = err.Error()
	task.mu.Unlock()

	// Persist state for potential recovery
	if dm.statePath != "" {
		dm.SaveState()
	}

	// Notify via callback
	if dm.onError != nil {
		dm.onError(task, err)
	}
}

// RetryTask manually retries a failed task.
// This resets the retry counter and starts the download fresh.
//
// Unlike automatic retries which preserve the retry count, manual retry
// resets all error state, giving the task a clean slate.
//
// Returns an error if:
//   - The task ID is not found
//   - The task is not in failed state
func (dm *DownloadManager) RetryTask(id string) error {
	dm.tasksMu.RLock()
	task, exists := dm.tasks[id]
	dm.tasksMu.RUnlock()

	if !exists {
		return fmt.Errorf("task %s not found", id)
	}

	task.mu.Lock()
	// Only failed tasks can be manually retried
	if task.Status != StatusFailed {
		task.mu.Unlock()
		return fmt.Errorf("task %s is not in failed state", id)
	}
	// Reset all error-related fields for fresh start
	task.RetryCount = 0
	task.Status = StatusPending
	task.Error = ""
	task.LastError = ""
	task.mu.Unlock()

	return dm.StartTask(id)
}

// GetPendingTasks returns all tasks that are eligible to be started or resumed.
// This includes tasks with status: pending, paused, or failed.
// Thread-safe.
func (dm *DownloadManager) GetPendingTasks() []*DownloadTask {
	dm.tasksMu.RLock()
	defer dm.tasksMu.RUnlock()

	var pending []*DownloadTask
	for _, task := range dm.tasks {
		task.mu.RLock()
		status := task.Status
		task.mu.RUnlock()

		// Include tasks that can be started/resumed
		if status == StatusPending || status == StatusPaused || status == StatusFailed {
			pending = append(pending, task)
		}
	}
	return pending
}

// ResumeTask resumes a paused task from where it left off.
// This validates the partial file exists and adjusts progress if needed.
//
// For multipart downloads, the resume state is tracked separately in a state file.
// For custom downloaders (e.g., Bilibili), file verification is skipped as the
// final file may not exist until merging completes.
//
// Returns an error if:
//   - The task ID is not found
//   - The task is not in paused state
//   - The partial file cannot be accessed
func (dm *DownloadManager) ResumeTask(id string) error {
	dm.tasksMu.RLock()
	task, exists := dm.tasks[id]
	dm.tasksMu.RUnlock()

	if !exists {
		return fmt.Errorf("task %s not found", id)
	}

	task.mu.Lock()
	// Only paused tasks can be resumed
	if task.Status != StatusPaused {
		task.mu.Unlock()
		return fmt.Errorf("task %s is not paused", id)
	}

	// Capture state for file verification
	downloaded := task.Downloaded
	filePath := task.FilePath
	customDownloader := task.customDownloader
	task.mu.Unlock()

	// Skip file verification for custom downloaders (e.g., Bilibili DASH)
	// Their final output file doesn't exist until video/audio streams are merged
	if downloaded > 0 && customDownloader == nil {
		fileInfo, err := os.Stat(filePath)
		if err != nil {
			if os.IsNotExist(err) {
				// Check for multipart state file - if present, keep progress
				if multipartStateExists(filePath) {
					// Multipart state tracks chunks, so progress is valid
				} else {
					// File was deleted, reset progress to start fresh
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
			// File size mismatch - adjust based on download type
			if multipartStateExists(filePath) {
				// Multipart downloads pre-allocate, so file size != downloaded is normal
			} else {
				// Sequential download: use actual file size for accuracy
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

	// Transition task to pending and start the download
	task.mu.Lock()
	task.Status = StatusPending
	resumeFrom := task.Downloaded
	task.mu.Unlock()

	logger.Info("Resuming task %s from %d bytes", id, resumeFrom)

	return dm.StartTask(id)
}

// GetDownloadingTaskCount returns the number of tasks currently in active download state.
// This includes both downloading and retrying tasks.
// Thread-safe.
func (dm *DownloadManager) GetDownloadingTaskCount() int {
	dm.tasksMu.RLock()
	defer dm.tasksMu.RUnlock()

	count := 0
	for _, task := range dm.tasks {
		task.mu.RLock()
		// Count both downloading and retrying as active
		if task.Status == StatusDownloading || task.Status == StatusRetrying {
			count++
		}
		task.mu.RUnlock()
	}
	return count
}
