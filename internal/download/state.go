package downloader

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"EasyDownload/internal/infra/logger"
)

// DownloadState represents the persisted state of all downloads.
// This structure is serialized to JSON and saved to disk for recovery.
type DownloadState struct {
	// Tasks contains the state of all managed download tasks.
	Tasks []TaskState `json:"tasks"`
}

// TaskState represents the persisted state of a single download task.
// This is a serializable snapshot of DownloadTask used for state persistence.
// Note: Custom downloader functions cannot be persisted and must be re-registered
// after loading state.
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

// SaveState saves the current download state to disk for recovery.
// This is called automatically after downloads complete, pause, or fail.
//
// The state includes all task metadata and progress. Completed tasks older than
// 24 hours are excluded to avoid unbounded growth.
//
// Returns an error if the state path is not configured or file operations fail.
func (dm *DownloadManager) SaveState() error {
	if dm.statePath == "" {
		return fmt.Errorf("state path not configured")
	}

	dm.tasksMu.RLock()
	defer dm.tasksMu.RUnlock()

	// Build state structure with all current tasks
	state := DownloadState{
		Tasks: make([]TaskState, 0, len(dm.tasks)),
	}

	for _, task := range dm.tasks {
		// Capture task state under its lock
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

		// Only persist non-completed tasks or recently completed ones (within 24 hours)
		// This prevents unbounded state file growth
		if taskState.Status != StatusCompleted || time.Now().Unix()-taskState.CompletedAt < 86400 {
			state.Tasks = append(state.Tasks, taskState)
		}
	}

	// Ensure parent directory exists
	dir := filepath.Dir(dm.statePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create state directory: %w", err)
	}

	// Write state as formatted JSON
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

// LoadState loads the download state from disk and restores tasks.
// This should be called during application startup to restore pending downloads.
//
// The method handles several edge cases:
//   - Missing state file: returns nil (normal for first run)
//   - Corrupted state file: backs up the file and starts fresh
//   - Duplicate task IDs: skips tasks that already exist
//   - Tasks that were downloading: transitions them to paused state
//
// Returns an error if the state path is not configured or file read fails.
func (dm *DownloadManager) LoadState() error {
	if dm.statePath == "" {
		return fmt.Errorf("state path not configured")
	}

	data, err := os.ReadFile(dm.statePath)
	if err != nil {
		if os.IsNotExist(err) {
			// No state file is normal for first run
			logger.Debug("No download state file found at: %s", dm.statePath)
			return nil
		}
		return fmt.Errorf("failed to read state file: %w", err)
	}

	var state DownloadState
	if err := json.Unmarshal(data, &state); err != nil {
		// Corrupted state file - backup and start fresh
		logger.Error("Download state file corrupted, backing up")
		backupPath := dm.statePath + ".backup"
		os.Rename(dm.statePath, backupPath)
		return nil
	}

	dm.tasksMu.Lock()
	defer dm.tasksMu.Unlock()

	// Restore each task from persisted state
	for _, taskState := range state.Tasks {
		// Skip if task already exists (shouldn't happen normally)
		if _, exists := dm.tasks[taskState.ID]; exists {
			continue
		}

		// Reconstruct the task from persisted state
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

		// Transition active states to paused (download goroutines don't survive restart)
		if task.Status == StatusDownloading || task.Status == StatusRetrying {
			task.Status = StatusPaused
		}

		dm.tasks[task.ID] = task
	}

	logger.Info("Loaded %d download tasks from state", len(state.Tasks))
	return nil
}
