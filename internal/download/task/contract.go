// Package task contains the platform-facing download task contract. Platform
// packages import this package instead of the DownloadManager implementation.
package task

import (
	"context"
	"encoding/json"
	"fmt"

	"EasyDownload/internal/download/fetch"
)

const CurrentPlatformDataVersion = 1

type PlatformID string

const (
	PlatformGeneric     PlatformID = "generic"
	PlatformWeChat      PlatformID = "wechat"
	PlatformBilibili    PlatformID = "bilibili"
	PlatformDouyin      PlatformID = "douyin"
	PlatformXiaohongshu PlatformID = "xiaohongshu"
)

type StopReason string

const (
	StopReasonPause       StopReason = "pause"
	StopReasonCancel      StopReason = "cancel"
	StopReasonShutdown    StopReason = "shutdown"
	StopReasonFailure     StopReason = "failure"
	StopReasonTaskRemoval StopReason = "task_removal"
)

type TaskStatus string

const (
	StatusPending   TaskStatus = "pending"
	StatusRunning   TaskStatus = "running"
	StatusPaused    TaskStatus = "paused"
	StatusCompleted TaskStatus = "completed"
	StatusFailed    TaskStatus = "failed"
	StatusCanceled  TaskStatus = "canceled"
)

type ConflictStrategy string

const (
	ConflictStrategyAutoRename ConflictStrategy = "auto_rename"
)

type OutputPolicy struct {
	Directory        string           `json:"directory"`
	PlannedFilename  string           `json:"plannedFilename"`
	PlannedFinalPath string           `json:"plannedFinalPath"`
	ReservationKey   string           `json:"reservationKey"`
	ConflictStrategy ConflictStrategy `json:"conflictStrategy"`
}

type TaskProgressSummary struct {
	Percent      float64 `json:"percent"`
	BytesLoaded  int64   `json:"bytesLoaded,omitempty"`
	BytesTotal   int64   `json:"bytesTotal,omitempty"`
	CurrentStage string  `json:"currentStage,omitempty"`
	StageLabel   string  `json:"stageLabel,omitempty"`
	ItemsDone    int     `json:"itemsDone,omitempty"`
	ItemsTotal   int     `json:"itemsTotal,omitempty"`
}

type TaskProgressUpdate struct {
	StageID        string   `json:"stageId"`
	StageLabel     string   `json:"stageLabel"`
	StagePercent   *float64 `json:"stagePercent,omitempty"`
	OverallPercent *float64 `json:"overallPercent,omitempty"`
	ItemsDone      int      `json:"itemsDone,omitempty"`
	ItemsTotal     int      `json:"itemsTotal,omitempty"`
	BytesLoaded    int64    `json:"bytesLoaded,omitempty"`
	BytesTotal     int64    `json:"bytesTotal,omitempty"`
}

// ProgressPercent returns an explicit progress percentage for a
// TaskProgressUpdate. A nil StagePercent means the update only carries other
// progress dimensions (for example byte counters) and must preserve the
// task's current percentage.
func ProgressPercent(value float64) *float64 {
	return &value
}

type TaskArtifactKind string

const (
	TaskArtifactFinal     TaskArtifactKind = "final"
	TaskArtifactTemporary TaskArtifactKind = "temporary"
)

type TaskArtifact struct {
	ID        string            `json:"id,omitempty"`
	Kind      TaskArtifactKind  `json:"kind"`
	Path      string            `json:"path"`
	FileName  string            `json:"fileName,omitempty"`
	MediaType string            `json:"mediaType,omitempty"`
	Size      int64             `json:"size,omitempty"`
	Primary   bool              `json:"primary,omitempty"`
	CreatedAt int64             `json:"createdAt,omitempty"`
	Metadata  map[string]string `json:"metadata,omitempty"`
}

// TaskArtifactDraft describes a final artifact before it is published. The
// manager owns the final path and turns this draft into a TaskArtifact only
// after a crash-safe, no-replace publish transaction succeeds.
type TaskArtifactDraft struct {
	ID        string            `json:"id"`
	MediaType string            `json:"mediaType,omitempty"`
	Size      int64             `json:"size"`
	SHA256    string            `json:"sha256,omitempty"`
	Primary   bool              `json:"primary"`
	Metadata  map[string]string `json:"metadata,omitempty"`
}

type PublishIntent struct {
	Generation       uint64            `json:"generation"`
	TemporaryPath    string            `json:"temporaryPath"`
	PlannedFinalPath string            `json:"plannedFinalPath"`
	Draft            TaskArtifactDraft `json:"draft"`
}

type PlatformCheckpointEnvelope struct {
	Version int             `json:"version"`
	Data    json.RawMessage `json:"data"`
}

type TaskErrorCategory string

const (
	TaskErrorCategoryTransport  TaskErrorCategory = "transport"
	TaskErrorCategoryPlatform   TaskErrorCategory = "platform"
	TaskErrorCategoryOutput     TaskErrorCategory = "output"
	TaskErrorCategoryCanceled   TaskErrorCategory = "canceled"
	TaskErrorCategoryUnexpected TaskErrorCategory = "unexpected"
)

type TaskError struct {
	Code       string            `json:"code"`
	Category   TaskErrorCategory `json:"category"`
	Message    string            `json:"message"`
	Retryable  bool              `json:"retryable"`
	UserAction string            `json:"userAction,omitempty"`
	Cause      string            `json:"cause,omitempty"`
	Metadata   map[string]string `json:"metadata,omitempty"`
}

func (err *TaskError) Error() string {
	if err == nil {
		return ""
	}
	return err.Message
}

type GenericPlatformData struct {
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers,omitempty"`
}

type TaskSnapshot struct {
	PlatformID          PlatformID                  `json:"platformId"`
	ID                  string                      `json:"id"`
	Title               string                      `json:"title"`
	Cover               string                      `json:"cover,omitempty"`
	DisplaySource       string                      `json:"displaySource,omitempty"`
	CreatedAt           int64                       `json:"createdAt"`
	CompletedAt         int64                       `json:"completedAt,omitempty"`
	Status              TaskStatus                  `json:"status"`
	OutputPolicy        OutputPolicy                `json:"outputPolicy"`
	Progress            TaskProgressSummary         `json:"progress"`
	Artifacts           []TaskArtifact              `json:"artifacts,omitempty"`
	LastError           *TaskError                  `json:"lastError,omitempty"`
	PlatformDataVersion int                         `json:"platformDataVersion"`
	PlatformData        json.RawMessage             `json:"platformData"`
	PlatformCheckpoint  *PlatformCheckpointEnvelope `json:"platformCheckpoint,omitempty"`
	PublishIntent       *PublishIntent              `json:"publishIntent,omitempty"`
}

// RequireCurrentPlatformDataVersion prevents a future or corrupted private
// payload from being decoded with v1 semantics. PlatformData is an execution
// contract, so unknown versions must fail closed rather than best-effort parse.
func RequireCurrentPlatformDataVersion(snapshot TaskSnapshot) error {
	if snapshot.PlatformDataVersion != CurrentPlatformDataVersion {
		return fmt.Errorf("unsupported platform data version %d (supported: %d)", snapshot.PlatformDataVersion, CurrentPlatformDataVersion)
	}
	return nil
}

type PlatformAdapter interface {
	ID() PlatformID
	ValidateTask(TaskSnapshot) error
	RunTask(context.Context, TaskSnapshot, TaskExecutionContext) error
	CleanupTask(context.Context, TaskSnapshot, StopReason) error
}

type TaskExecutionContext interface {
	Fetcher() fetch.Fetcher
	FFmpeg() FFmpegLocator
	Credentials() CredentialStore
	UpdateTaskProgress(TaskProgressUpdate) error
	RecordArtifact(TaskArtifact) error
	// RecordPostPublishCleanupFailure is the only mutation allowed after a
	// primary final artifact closes the normal generation-bound mutation sinks.
	// It records a leftover temporary/sidecar path whose best-effort deletion
	// failed without downgrading the already-committed task completion.
	RecordPostPublishCleanupFailure(TaskArtifact) error
	RecordCheckpoint(PlatformCheckpointEnvelope) error
	PublishFinal(context.Context, string, TaskArtifactDraft) (TaskArtifact, error)
}

type FFmpegLocator interface {
	Locate(context.Context) (string, error)
}

type CredentialStore interface {
	Get(context.Context, string, string) ([]byte, error)
}

// CloneSnapshot returns an adapter-safe deep copy. A struct assignment is not
// sufficient because RawMessage, slices, and metadata maps remain mutable.
func CloneSnapshot(snapshot TaskSnapshot) TaskSnapshot {
	clone := snapshot
	clone.PlatformData = append(json.RawMessage(nil), snapshot.PlatformData...)
	clone.Artifacts = make([]TaskArtifact, len(snapshot.Artifacts))
	for index, artifact := range snapshot.Artifacts {
		clone.Artifacts[index] = artifact
		clone.Artifacts[index].Metadata = cloneStringMap(artifact.Metadata)
	}
	if snapshot.LastError != nil {
		lastError := *snapshot.LastError
		lastError.Metadata = cloneStringMap(snapshot.LastError.Metadata)
		clone.LastError = &lastError
	}
	if snapshot.PlatformCheckpoint != nil {
		checkpoint := *snapshot.PlatformCheckpoint
		checkpoint.Data = append(json.RawMessage(nil), snapshot.PlatformCheckpoint.Data...)
		clone.PlatformCheckpoint = &checkpoint
	}
	if snapshot.PublishIntent != nil {
		intent := *snapshot.PublishIntent
		intent.Draft.Metadata = cloneStringMap(snapshot.PublishIntent.Draft.Metadata)
		clone.PublishIntent = &intent
	}
	return clone
}

func cloneStringMap(source map[string]string) map[string]string {
	if source == nil {
		return nil
	}
	clone := make(map[string]string, len(source))
	for key, value := range source {
		clone[key] = value
	}
	return clone
}
