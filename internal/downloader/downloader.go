package downloader

import (
	"EasyDownload/internal/logger"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

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
func (dm *DownloadManager) AddTask(id, url, title, cover, source, quality string) (*DownloadTask, error) {
	dm.tasksMu.Lock()
	defer dm.tasksMu.Unlock()

	// Check if task already exists
	if _, exists := dm.tasks[id]; exists {
		return nil, fmt.Errorf("task with ID %s already exists", id)
	}

	// Generate filename
	fileName := sanitizeFileName(title)
	if fileName == "" {
		fileName = fmt.Sprintf("video_%s", id)
	}
	fileName = fileName + ".mp4"

	task := &DownloadTask{
		ID:         id,
		URL:        url,
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
	}

	dm.tasks[id] = task
	return task, nil
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
	task.mu.Unlock()

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

	// Mark as completed
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
	// Replace invalid characters
	invalid := []string{"/", "\\", ":", "*", "?", "\"", "<", ">", "|"}
	result := name
	for _, char := range invalid {
		result = replaceAll(result, char, "_")
	}

	// Limit length
	if len(result) > 100 {
		result = result[:100]
	}

	return result
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
	}
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
	task.mu.Unlock()

	if downloaded > 0 {
		fileInfo, err := os.Stat(filePath)
		if err != nil {
			if os.IsNotExist(err) {
				// File was deleted, reset progress
				task.mu.Lock()
				task.Downloaded = 0
				task.Progress = 0
				task.mu.Unlock()
				logger.Info("Partial file not found for task %s, restarting from beginning", id)
			} else {
				return fmt.Errorf("failed to check partial file: %w", err)
			}
		} else if fileInfo.Size() != downloaded {
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
