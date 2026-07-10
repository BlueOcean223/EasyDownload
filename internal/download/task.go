package downloader

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"

	downloadtask "EasyDownload/internal/download/task"
)

// DownloadTask represents a single download task with all its metadata and state.
// It is safe for concurrent access via its internal mutex (mu).
//
// A task progresses through various states (see DownloadStatus) and tracks
// download progress, speed, and error information.
type DownloadTask struct {
	PlatformID          string                                   `json:"platformId"`
	PlatformDataVersion int                                      `json:"platformDataVersion"`
	PlatformData        json.RawMessage                          `json:"platformData,omitempty"`
	PlatformCheckpoint  *downloadtask.PlatformCheckpointEnvelope `json:"platformCheckpoint,omitempty"`
	PublishIntent       *downloadtask.PublishIntent              `json:"publishIntent,omitempty"`
	OutputPolicy        downloadtask.OutputPolicy                `json:"outputPolicy"`
	// ID is a unique identifier for this download task.
	ID string `json:"id"`
	// Title is the display name for this download.
	Title string `json:"title"`
	// Cover is the URL of the cover/thumbnail image.
	Cover string `json:"cover"`
	// DisplaySource is presentation-only; execution identity is PlatformID.
	DisplaySource string `json:"displaySource"`
	// ProgressSummary is the structured progress view owned by DownloadManager.
	ProgressSummary downloadtask.TaskProgressSummary `json:"progressSummary"`
	// Artifacts records final and temporary outputs produced by the task.
	Artifacts []downloadtask.TaskArtifact `json:"artifacts,omitempty"`
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

	// LastError stores the error message from the most recent failed attempt.
	LastError string `json:"lastError"`
	// LastErrorDetail stores the structured task error used across module boundaries.
	LastErrorDetail *downloadtask.TaskError `json:"lastErrorDetail,omitempty"`

	// cancel is the context cancellation function for stopping this download.
	cancel context.CancelFunc
	// generationCounter is monotonically increasing for this process. execution
	// and all mutation gates are protected by mu.
	generationCounter uint64
	// queuedGeneration is reserved when a start is durably queued. Public
	// snapshots expose it before automatic dispatch so expected-generation
	// commands remain valid across the queue -> running transition.
	queuedGeneration uint64
	// eventInstance identifies this in-memory task object within one manager
	// session. Unlike generationCounter it changes when a removed ID is reused,
	// allowing the UI to distinguish a genuinely new task from late events for
	// the deleted object. It is intentionally not persisted: the frontend and
	// manager session restart together.
	eventInstance uint64
	execution     *taskExecution
	// mu protects all fields of this struct for concurrent access.
	mu sync.RWMutex
}

// PublicOutputPolicy is the output policy exposed to the UI. ReservationKey is
// deliberately absent: it is an internal allocator capability, not public task
// state.
type PublicOutputPolicy struct {
	Directory        string                        `json:"directory"`
	PlannedFilename  string                        `json:"plannedFilename"`
	PlannedFinalPath string                        `json:"plannedFinalPath"`
	ConflictStrategy downloadtask.ConflictStrategy `json:"conflictStrategy"`
}

// PublicTaskArtifact intentionally omits adapter-owned Metadata. The sanitized
// CleanupFailed bit keeps post-publish leftovers observable without exposing
// raw filesystem errors. Artifact paths are local output paths only; remote
// resource URLs remain in PlatformData.
type PublicTaskArtifact struct {
	ID            string                        `json:"id,omitempty"`
	Kind          downloadtask.TaskArtifactKind `json:"kind"`
	Path          string                        `json:"path"`
	FileName      string                        `json:"fileName,omitempty"`
	MediaType     string                        `json:"mediaType,omitempty"`
	Size          int64                         `json:"size,omitempty"`
	Primary       bool                          `json:"primary,omitempty"`
	CreatedAt     int64                         `json:"createdAt,omitempty"`
	CleanupFailed bool                          `json:"cleanupFailed,omitempty"`
}

// PublicDownloadTask is the secret-free task projection used by API responses
// and Wails events. Execution inputs and recovery internals (PlatformData,
// PlatformCheckpoint, PublishIntent, DecodeKey, and the output reservation key)
// only belong in the persisted TaskSnapshot and adapter-facing contract.
type PublicDownloadTask struct {
	ID              string                           `json:"id"`
	Instance        uint64                           `json:"instance"`
	Generation      uint64                           `json:"generation"`
	Revision        uint64                           `json:"revision"`
	PlatformID      string                           `json:"platformId,omitempty"`
	Title           string                           `json:"title"`
	Cover           string                           `json:"cover"`
	DisplaySource   string                           `json:"displaySource,omitempty"`
	OutputPolicy    PublicOutputPolicy               `json:"outputPolicy"`
	ProgressSummary downloadtask.TaskProgressSummary `json:"progressSummary"`
	Artifacts       []PublicTaskArtifact             `json:"artifacts,omitempty"`
	Speed           int64                            `json:"speed"`
	Status          DownloadStatus                   `json:"status"`
	Error           string                           `json:"error"`
	CreatedAt       int64                            `json:"createdAt"`
	CompletedAt     int64                            `json:"completedAt"`
	LastError       string                           `json:"lastError"`
	LastErrorDetail *downloadtask.TaskError          `json:"lastErrorDetail,omitempty"`
	ExecutionState  string                           `json:"executionState,omitempty"`
}

// PublicSnapshot returns an immutable, secret-free task view suitable for
// crossing the backend/frontend boundary. Thread-safe.
func (t *DownloadTask) PublicSnapshot() PublicDownloadTask {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.publicSnapshotLocked()
}

func (t *DownloadTask) publicSnapshotLocked() PublicDownloadTask {
	artifacts := make([]PublicTaskArtifact, 0, len(t.Artifacts))
	for _, artifact := range t.Artifacts {
		path := strings.TrimSpace(artifact.Path)
		if path == "" || !filepath.IsAbs(path) {
			continue
		}
		artifacts = append(artifacts, PublicTaskArtifact{
			ID:            artifact.ID,
			Kind:          artifact.Kind,
			Path:          path,
			FileName:      artifact.FileName,
			MediaType:     artifact.MediaType,
			Size:          artifact.Size,
			Primary:       artifact.Primary,
			CreatedAt:     artifact.CreatedAt,
			CleanupFailed: strings.TrimSpace(artifact.Metadata["cleanupError"]) != "",
		})
	}
	lastError := publicTaskError(t.LastErrorDetail)
	publicErrorMessage := ""
	if lastError != nil {
		publicErrorMessage = lastError.Message
	}
	executionState := ""
	generation := t.generationCounter
	if t.execution != nil {
		executionState = string(t.execution.phase)
		generation = t.execution.generation
	}
	return PublicDownloadTask{
		ID:            t.ID,
		Instance:      t.eventInstance,
		Generation:    generation,
		PlatformID:    t.PlatformID,
		Title:         t.Title,
		Cover:         t.Cover,
		DisplaySource: t.DisplaySource,
		OutputPolicy: PublicOutputPolicy{
			Directory:        t.OutputPolicy.Directory,
			PlannedFilename:  t.OutputPolicy.PlannedFilename,
			PlannedFinalPath: t.OutputPolicy.PlannedFinalPath,
			ConflictStrategy: t.OutputPolicy.ConflictStrategy,
		},
		ProgressSummary: t.ProgressSummary,
		Artifacts:       artifacts,
		Speed:           t.Speed,
		Status:          t.Status,
		Error:           publicErrorMessage,
		CreatedAt:       t.CreatedAt,
		CompletedAt:     t.CompletedAt,
		LastError:       publicErrorMessage,
		LastErrorDetail: lastError,
		ExecutionState:  executionState,
	}
}

// PublicTaskSnapshot stamps a task projection with manager-session ordering.
// Instance changes when an ID is removed and reused, Generation changes for a
// retry/resume execution, and Revision orders snapshots within that pair.
func (dm *DownloadManager) PublicTaskSnapshot(task *DownloadTask) PublicDownloadTask {
	if task == nil {
		return PublicDownloadTask{}
	}
	task.mu.Lock()
	defer task.mu.Unlock()
	if dm != nil {
		if task.eventInstance == 0 {
			task.eventInstance = dm.taskInstance.Add(1)
		}
	}
	snapshot := task.publicSnapshotLocked()
	if dm != nil {
		snapshot.Revision = dm.eventRevision.Add(1)
	}
	return snapshot
}

// GetPublicTaskSnapshots captures membership and each task's versioned public
// payload under one tasks-map read lock. A concurrent remove/create must occur
// after this capture and therefore receives a later task-event fence, so an
// initial RPC response cannot resurrect a removed object when buffered events
// are replayed.
func (dm *DownloadManager) GetPublicTaskSnapshots() []PublicDownloadTask {
	if dm == nil {
		return nil
	}
	dm.tasksMu.RLock()
	defer dm.tasksMu.RUnlock()
	result := make([]PublicDownloadTask, 0, len(dm.tasks))
	for _, task := range dm.tasks {
		result = append(result, dm.PublicTaskSnapshot(task))
	}
	return result
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
	return t.ProgressSummary.Percent
}

// SetProgress updates the download progress percentage.
// Thread-safe.
func (t *DownloadTask) SetProgress(progress float64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.ProgressSummary.Percent = progress
}

// GetFileSize returns the total file size in bytes.
// Thread-safe.
func (t *DownloadTask) GetFileSize() int64 {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.ProgressSummary.BytesTotal
}

// SetFileSize updates the total file size in bytes.
// Thread-safe.
func (t *DownloadTask) SetFileSize(size int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.ProgressSummary.BytesTotal = size
}

// GetDownloaded returns the number of bytes downloaded so far.
// Thread-safe.
func (t *DownloadTask) GetDownloaded() int64 {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.ProgressSummary.BytesLoaded
}

// SetDownloaded updates the number of bytes downloaded.
// Thread-safe.
func (t *DownloadTask) SetDownloaded(downloaded int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.ProgressSummary.BytesLoaded = downloaded
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

// SetCompletedAt sets the Unix timestamp when the download completed.
// Thread-safe.
func (t *DownloadTask) SetCompletedAt(ts int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.CompletedAt = ts
}
