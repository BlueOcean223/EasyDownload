package downloader

import (
	"context"
	"sync"
)

// DownloadTask represents a single download task with all its metadata and state.
// It is safe for concurrent access via its internal mutex (mu).
//
// A task progresses through various states (see DownloadStatus) and tracks
// download progress, speed, and error information.
type DownloadTask struct {
	// ID is a unique identifier for this download task.
	ID string `json:"id"`
	// URL is the download URL (may include quality parameters for WeChat).
	URL string `json:"url"`
	// Title is the display name for this download.
	Title string `json:"title"`
	// Cover is the URL of the cover/thumbnail image.
	Cover string `json:"cover"`
	// Source identifies the content source (e.g., "wechat", "bilibili", "douyin").
	Source string `json:"source"`
	// Quality contains quality-specific metadata (e.g., WeChat fileFormat value).
	Quality string `json:"quality"`
	// FilePath is the absolute path where the file will be saved.
	FilePath string `json:"filePath"`
	// FileName is just the filename portion of FilePath.
	FileName string `json:"fileName"`
	// FileSize is the total size of the file in bytes (0 if unknown).
	FileSize int64 `json:"fileSize"`
	// Downloaded is the number of bytes downloaded so far.
	Downloaded int64 `json:"downloaded"`
	// Progress is the download progress as a percentage (0-100).
	Progress float64 `json:"progress"`
	// Speed is the current download speed in bytes per second.
	Speed int64 `json:"speed"`
	// Status is the current state of this download task.
	Status DownloadStatus `json:"status"`
	// Error contains the error message if the task failed.
	Error string `json:"error"`
	// CreatedAt is the Unix timestamp when this task was created.
	CreatedAt int64 `json:"createdAt"`
	// CompletedAt is the Unix timestamp when this task completed (0 if not completed).
	CompletedAt int64 `json:"completedAt"`

	// Retry configuration fields
	// RetryCount tracks the number of retry attempts made for this task.
	RetryCount int `json:"retryCount"`
	// MaxRetry is the maximum number of retry attempts allowed.
	MaxRetry int `json:"maxRetry"`
	// LastError stores the error message from the most recent failed attempt.
	LastError string `json:"lastError"`

	// Decryption fields (for WeChat videos)
	// DecodeKey is the Base64-encoded decryption key for encrypted WeChat videos.
	DecodeKey string `json:"decodeKey"`

	// Album fields (for Douyin albums)
	// IsAlbum indicates whether this task is a Douyin album download.
	IsAlbum bool `json:"isAlbum"`
	// AlbumTotal is the total number of images in the album.
	AlbumTotal int `json:"albumTotal"`
	// AlbumCompleted is the number of images that have been downloaded.
	AlbumCompleted int `json:"albumCompleted"`

	// customDownloader is an optional function for sources requiring special download logic.
	// When set, this function is used instead of the default HTTP download.
	customDownloader DownloadFunc

	// cancel is the context cancellation function for stopping this download.
	cancel context.CancelFunc
	// mu protects all fields of this struct for concurrent access.
	mu sync.RWMutex
}

// SetCustomDownloader sets a custom downloader function for the task.
// This allows changing the download method after task creation.
// Thread-safe.
func (t *DownloadTask) SetCustomDownloader(downloadFunc DownloadFunc) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.customDownloader = downloadFunc
}

// GetCustomDownloader returns the custom downloader function for the task, if any.
// Returns nil if no custom downloader is set.
// Thread-safe.
func (t *DownloadTask) GetCustomDownloader() DownloadFunc {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.customDownloader
}

// TaskToJSON returns a map representation of the task suitable for JSON serialization.
// This provides a snapshot of all task fields for API responses or persistence.
// Thread-safe.
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
// These methods provide safe concurrent access to task fields.

// SetCancel sets the context cancellation function for the task.
// This is called internally when starting a download.
func (t *DownloadTask) SetCancel(cancel context.CancelFunc) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.cancel = cancel
}

// GetStatus returns the current status of the task.
// Thread-safe.
func (t *DownloadTask) GetStatus() DownloadStatus {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.Status
}

// SetStatus updates the task status.
// Thread-safe.
func (t *DownloadTask) SetStatus(status DownloadStatus) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.Status = status
}

// GetProgress returns the download progress as a percentage (0-100).
// Thread-safe.
func (t *DownloadTask) GetProgress() float64 {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.Progress
}

// SetProgress updates the download progress percentage.
// Thread-safe.
func (t *DownloadTask) SetProgress(progress float64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.Progress = progress
}

// GetFileSize returns the total file size in bytes.
// Thread-safe.
func (t *DownloadTask) GetFileSize() int64 {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.FileSize
}

// SetFileSize updates the total file size in bytes.
// Thread-safe.
func (t *DownloadTask) SetFileSize(size int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.FileSize = size
}

// GetDownloaded returns the number of bytes downloaded so far.
// Thread-safe.
func (t *DownloadTask) GetDownloaded() int64 {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.Downloaded
}

// SetDownloaded updates the number of bytes downloaded.
// Thread-safe.
func (t *DownloadTask) SetDownloaded(downloaded int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.Downloaded = downloaded
}

// GetSpeed returns the current download speed in bytes per second.
// Thread-safe.
func (t *DownloadTask) GetSpeed() int64 {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.Speed
}

// SetSpeed updates the download speed in bytes per second.
// Thread-safe.
func (t *DownloadTask) SetSpeed(speed int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.Speed = speed
}

// SetError sets the error message for a failed task.
// Thread-safe.
func (t *DownloadTask) SetError(err string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.Error = err
}

// SetFilePath updates the output file path.
// This may be called by custom downloaders that determine the final path.
// Thread-safe.
func (t *DownloadTask) SetFilePath(path string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.FilePath = path
}

// SetCompletedAt sets the Unix timestamp when the download completed.
// Thread-safe.
func (t *DownloadTask) SetCompletedAt(ts int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.CompletedAt = ts
}
