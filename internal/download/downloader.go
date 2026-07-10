package downloader

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"EasyDownload/internal/download/fetch"
	downloadtask "EasyDownload/internal/download/task"
	"EasyDownload/internal/infra/logger"
	"EasyDownload/internal/utils"
)

// DownloadStatus represents the current state of a download task.
// The status transitions follow a state machine pattern:
//
//	pending -> running -> completed
//	                    \-> failed -> (manual retry) -> pending
//	                    \-> paused -> (resume) -> running
//	any state -> canceled (user-initiated cancellation)
type DownloadStatus string

// Download status constants define all possible states for a download task.
const (
	// StatusPending indicates the task is queued but not yet started.
	StatusPending DownloadStatus = "pending"
	// StatusDownloading indicates the task is actively running. The constant
	// name is retained for internal call-site compatibility; the serialized
	// lifecycle value is "running".
	StatusDownloading DownloadStatus = "running"
	// StatusPaused indicates the task was paused by the user and can be resumed.
	StatusPaused DownloadStatus = "paused"
	// StatusCompleted indicates the download finished successfully.
	StatusCompleted DownloadStatus = "completed"
	// StatusFailed indicates the download failed after exhausting all retry attempts.
	StatusFailed DownloadStatus = "failed"
	// StatusCancelled indicates the user cancelled the download.
	StatusCancelled DownloadStatus = "canceled"
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
	configMu    sync.RWMutex
	downloadDir string
	// maxConcurrent is the maximum number of simultaneous downloads allowed.
	maxConcurrent int
	// activeTasks tracks the current number of running downloads.
	activeTasks int
	// activeTasksMu protects activeTasks and the pending start queue.
	activeTasksMu sync.Mutex
	// queuedTasks holds tasks waiting for an available download slot.
	queuedTasks []*DownloadTask
	// queuedTaskIDs prevents the same task from being queued more than once.
	queuedTaskIDs map[string]struct{}
	// startTransitionMu serializes slot/queue transitions with their durable
	// snapshot. It also prevents CreateAndStart from racing a dispatcher while
	// holding the persistence transaction.
	startTransitionMu sync.Mutex

	// State persistence
	// statePath is the file path for saving/loading download state.
	statePath       string
	legacyStatePath string
	taskStore       *TaskStore
	persistenceMu   sync.Mutex
	revision        atomic.Uint64
	eventRevision   atomic.Uint64
	taskInstance    atomic.Uint64
	legacyNoticeMu  sync.Mutex
	legacyNotified  bool
	// platformRegistry holds platform-specific task hooks registered by the
	// composition root. DownloadManager does not import platform packages.
	platformRegistry *PlatformRegistry
	outputAllocator  *OutputPathAllocator
	fetcher          fetch.Fetcher
	ffmpeg           downloadtask.FFmpegLocator
	credentials      downloadtask.CredentialStore

	lifecycleMu       sync.Mutex
	stopWaitTimeout   time.Duration
	cleanupTimeout    time.Duration
	onStopEvent       func(StopEvent)
	beforeStopCleanup func()

	// Event callbacks for external notification
	// onProgress is called periodically during download with updated progress.
	onProgress func(task *DownloadTask)
	// onComplete is called when a download finishes successfully.
	onComplete func(task *DownloadTask)
	// onError is called when a download fails after all retries.
	onError func(task *DownloadTask, err error)
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
		tasks:            make(map[string]*DownloadTask),
		downloadDir:      downloadDir,
		maxConcurrent:    maxConcurrent,
		queuedTasks:      make([]*DownloadTask, 0),
		queuedTaskIDs:    make(map[string]struct{}),
		platformRegistry: NewPlatformRegistry(),
		outputAllocator:  NewOutputPathAllocator(),
		fetcher:          fetch.New(nil),
		stopWaitTimeout:  2 * time.Second,
		cleanupTimeout:   30 * time.Second,
	}
}

func (dm *DownloadManager) RegisterPlatformAdapter(adapter downloadtask.PlatformAdapter) error {
	if dm.platformRegistry == nil {
		dm.platformRegistry = NewPlatformRegistry()
	}
	return dm.platformRegistry.Register(adapter)
}

func taskSnapshot(task *DownloadTask) downloadtask.TaskSnapshot {
	task.mu.RLock()
	defer task.mu.RUnlock()
	return taskSnapshotLocked(task)
}

func taskSnapshotLocked(task *DownloadTask) downloadtask.TaskSnapshot {
	snapshot := downloadtask.TaskSnapshot{
		ID:                  task.ID,
		PlatformID:          downloadtask.PlatformID(task.PlatformID),
		Title:               task.Title,
		Cover:               task.Cover,
		DisplaySource:       task.DisplaySource,
		CreatedAt:           task.CreatedAt,
		CompletedAt:         task.CompletedAt,
		Status:              downloadtask.TaskStatus(task.Status),
		OutputPolicy:        task.OutputPolicy,
		Progress:            task.ProgressSummary,
		Artifacts:           task.Artifacts,
		LastError:           cloneTaskError(task.LastErrorDetail),
		PlatformDataVersion: task.PlatformDataVersion,
		PlatformData:        task.PlatformData,
		PlatformCheckpoint:  task.PlatformCheckpoint,
		PublishIntent:       task.PublishIntent,
	}
	return downloadtask.CloneSnapshot(snapshot)
}

func (dm *DownloadManager) adapterForTask(task *DownloadTask) (downloadtask.PlatformAdapter, bool) {
	return dm.adapterForSnapshot(taskSnapshot(task))
}

func (dm *DownloadManager) adapterForSnapshot(snapshot downloadtask.TaskSnapshot) (downloadtask.PlatformAdapter, bool) {
	return dm.platformRegistry.Get(snapshot.PlatformID)
}

func (dm *DownloadManager) SetExecutionCapabilities(fetcherInstance fetch.Fetcher, ffmpeg downloadtask.FFmpegLocator, credentials downloadtask.CredentialStore) {
	if fetcherInstance != nil {
		dm.fetcher = fetcherInstance
	}
	dm.ffmpeg = ffmpeg
	dm.credentials = credentials
}

// SetStatePath sets the file path for persisting download state.
// When set, the manager will automatically save state after task completion,
// pause, or failure, enabling recovery after application restart.
func (dm *DownloadManager) SetStatePath(path string) {
	path = filepath.Clean(strings.TrimSpace(path))
	dm.legacyStatePath = ""
	if strings.EqualFold(filepath.Base(path), "downloads.json") {
		dm.legacyStatePath = path
		dm.statePath = filepath.Join(filepath.Dir(path), "downloads.v2.json")
	} else {
		dm.statePath = path
	}
	dm.taskStore = NewTaskStore(dm.statePath)
	dm.legacyNoticeMu.Lock()
	dm.legacyNotified = false
	dm.legacyNoticeMu.Unlock()
}

func (dm *DownloadManager) StatePath() string { return dm.statePath }

func (dm *DownloadManager) LegacyStatePath() string { return dm.legacyStatePath }

func (dm *DownloadManager) HasLegacyState() bool {
	if strings.TrimSpace(dm.legacyStatePath) == "" {
		return false
	}
	info, err := os.Stat(dm.legacyStatePath)
	return err == nil && info.Size() > 0 && !info.IsDir()
}

type LegacyTaskStateNotice struct {
	Code              string `json:"code"`
	LegacyPath        string `json:"legacyPath"`
	V2Path            string `json:"v2Path"`
	Imported          bool   `json:"imported"`
	Preserved         bool   `json:"preserved"`
	RollbackAvailable bool   `json:"rollbackAvailable"`
	Message           string `json:"message"`
}

// TakeLegacyStateNotice returns a typed, one-shot activation notice when a
// non-empty downloads.json was deliberately left untouched beside downloads.v2.json.
func (dm *DownloadManager) TakeLegacyStateNotice() *LegacyTaskStateNotice {
	if dm == nil || !dm.HasLegacyState() {
		return nil
	}
	dm.legacyNoticeMu.Lock()
	defer dm.legacyNoticeMu.Unlock()
	if dm.legacyNotified {
		return nil
	}
	dm.legacyNotified = true
	return &LegacyTaskStateNotice{
		Code:              "download.legacy_state_preserved",
		LegacyPath:        dm.legacyStatePath,
		V2Path:            dm.statePath,
		Imported:          false,
		Preserved:         true,
		RollbackAvailable: true,
		Message:           "旧版下载任务未导入并已原样保留；需要回退时仍可使用。",
	}
}

func (dm *DownloadManager) saveStateBestEffort() {
	if dm.statePath == "" {
		return
	}
	if err := dm.SaveState(); err != nil {
		logger.Warn("Failed to save download state: %v", err)
	}
}

func (dm *DownloadManager) persistIfConfiguredWithLock(persistenceLocked bool) error {
	if dm == nil || dm.statePath == "" {
		return nil
	}
	if persistenceLocked {
		return dm.saveStateSnapshotLocked("")
	}
	return dm.SaveState()
}

// GetMaxConcurrent returns the maximum number of concurrent downloads allowed.
func (dm *DownloadManager) GetMaxConcurrent() int {
	dm.configMu.RLock()
	defer dm.configMu.RUnlock()
	return dm.maxConcurrent
}

// SetMaxConcurrent updates the maximum number of concurrent downloads.
// This only affects future download starts; currently running downloads are not affected.
// Values <= 0 are ignored.
func (dm *DownloadManager) SetMaxConcurrent(max int) {
	if max <= 0 {
		return
	}
	update, err := dm.BeginRuntimeConfigUpdate(RuntimeConfigPatch{MaxConcurrent: &max})
	if err != nil {
		return
	}
	_ = update.Commit()
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
	update, err := dm.BeginRuntimeConfigUpdate(RuntimeConfigPatch{DownloadDir: &dir})
	if err != nil {
		return fmt.Errorf("failed to update download directory: %w", err)
	}
	return update.Commit()
}

// GetDownloadDir returns the current download directory path.
func (dm *DownloadManager) GetDownloadDir() string {
	return dm.RuntimeConfig().DownloadDir
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

// TaskCreationInput is the backend-only v2 creation contract. Platform URLs,
// headers, credentials, and decryption material belong inside PlatformData and
// must not be copied into a Wails DTO.
type TaskCreationInput struct {
	ID                  string
	PlatformID          downloadtask.PlatformID
	Title               string
	Cover               string
	DisplaySource       string
	OutputDirectory     string
	SuggestedFilename   string
	SuggestedExtension  string
	PlatformDataVersion int
	PlatformData        json.RawMessage
}

func (dm *DownloadManager) CreateTask(input TaskCreationInput) (*DownloadTask, error) {
	dm.configMu.RLock()
	defer dm.configMu.RUnlock()
	return dm.createTaskLocked(input, true)
}

// CreateAndStartTask commits creation together with the initial running/queued
// transition. No intermediate pending task is exposed to persistence, so a
// synchronous start failure cannot leave an orphan whose ID the caller never
// received.
func (dm *DownloadManager) CreateAndStartTask(input TaskCreationInput) (*DownloadTask, error) {
	dm.configMu.RLock()
	defer dm.configMu.RUnlock()
	dm.startTransitionMu.Lock()
	defer dm.startTransitionMu.Unlock()

	// persistenceMu also serializes task identity/reservation ownership when no
	// state file is configured; this prevents remove/recreate ABA on the same ID.
	persistenceLocked := true
	dm.persistenceMu.Lock()
	defer dm.persistenceMu.Unlock()

	task, err := dm.createTaskLocked(input, false)
	if err != nil {
		return nil, err
	}
	if err := dm.startTaskLocked(task.ID, persistenceLocked); err != nil {
		dm.tasksMu.Lock()
		if dm.tasks[task.ID] == task {
			delete(dm.tasks, task.ID)
		}
		dm.tasksMu.Unlock()
		dm.outputAllocator.Release(task.ID)
		return nil, fmt.Errorf("start newly created task %s: %w", task.ID, err)
	}
	return task, nil
}

func (dm *DownloadManager) createTaskLocked(input TaskCreationInput, persist bool) (*DownloadTask, error) {
	input.ID = strings.TrimSpace(input.ID)
	if input.ID == "" {
		return nil, errors.New("task id is required")
	}
	if input.PlatformID == "" {
		return nil, errors.New("platform id is required")
	}
	input.PlatformData = append(json.RawMessage(nil), input.PlatformData...)
	if len(input.PlatformData) == 0 || !json.Valid(input.PlatformData) {
		return nil, fmt.Errorf("platform data for task %s must be valid non-empty JSON", input.ID)
	}
	if input.PlatformDataVersion <= 0 {
		input.PlatformDataVersion = 1
	}
	extension := strings.TrimSpace(input.SuggestedExtension)
	if extension == "" {
		extension = ".mp4"
	} else if !strings.HasPrefix(extension, ".") {
		extension = "." + extension
	}

	baseName := utils.SanitizeFileName(input.SuggestedFilename, 100)
	usedFallbackName := false
	if baseName == "" {
		usedFallbackName = true
		safeSource := utils.SanitizeFileName(input.DisplaySource, 100)
		if safeSource == "" {
			safeSource = "video"
		}
		h := fnv.New32a()
		_, _ = h.Write([]byte(input.ID))
		hash := fmt.Sprintf("%08x", h.Sum32())
		baseName = fmt.Sprintf("video_%s_%d_%s", safeSource, time.Now().Unix(), hash)
	}
	// Final sanitization pass to ensure filename is valid
	baseName = utils.SanitizeFileName(baseName, 100)
	if baseName == "" {
		baseName = fmt.Sprintf("video_%d", time.Now().UnixNano())
	}
	fileName := baseName + extension

	lockCreation := persist
	if lockCreation {
		dm.persistenceMu.Lock()
		defer dm.persistenceMu.Unlock()
	}
	dm.tasksMu.Lock()
	if _, exists := dm.tasks[input.ID]; exists {
		dm.tasksMu.Unlock()
		return nil, fmt.Errorf("task with ID %s already exists", input.ID)
	}
	outputDirectory := strings.TrimSpace(input.OutputDirectory)
	if outputDirectory == "" {
		outputDirectory = dm.downloadDir
	}
	outputPolicy, err := dm.outputAllocator.Reserve(input.ID, outputDirectory, fileName, downloadtask.ConflictStrategyAutoRename)
	if err != nil {
		dm.tasksMu.Unlock()
		return nil, err
	}
	releaseReservation := true
	defer func() {
		if releaseReservation {
			dm.outputAllocator.Release(input.ID)
		}
	}()
	if usedFallbackName {
		idForLog := input.ID
		if runes := []rune(idForLog); len(runes) > 120 {
			idForLog = string(runes[:120]) + "..."
		}
		logger.Info("Filename fallback used: id=%q -> %q", idForLog, outputPolicy.PlannedFilename)
	}

	task := &DownloadTask{
		eventInstance:       dm.taskInstance.Add(1),
		PlatformID:          string(input.PlatformID),
		PlatformDataVersion: input.PlatformDataVersion,
		PlatformData:        input.PlatformData,
		OutputPolicy:        outputPolicy,
		ID:                  input.ID,
		Title:               input.Title,
		Cover:               input.Cover,
		DisplaySource:       input.DisplaySource,
		Status:              StatusPending,
		ProgressSummary: downloadtask.TaskProgressSummary{
			CurrentStage: "queued",
			StageLabel:   "等待中",
		},
		CreatedAt: time.Now().Unix(),
	}
	adapter, ok := dm.platformRegistry.Get(input.PlatformID)
	if !ok {
		dm.tasksMu.Unlock()
		return nil, fmt.Errorf("no platform adapter registered for %s", input.PlatformID)
	}
	if err := adapter.ValidateTask(downloadtask.CloneSnapshot(taskSnapshot(task))); err != nil {
		dm.tasksMu.Unlock()
		return nil, err
	}
	dm.tasks[input.ID] = task
	dm.tasksMu.Unlock()
	releaseReservation = false
	if persist && dm.statePath != "" {
		if err := dm.saveStateSnapshotLocked(""); err != nil {
			dm.tasksMu.Lock()
			if dm.tasks[input.ID] == task {
				delete(dm.tasks, input.ID)
			}
			dm.tasksMu.Unlock()
			dm.outputAllocator.Release(input.ID)
			return nil, fmt.Errorf("persist new task: %w", err)
		}
	}
	return task, nil
}

// StartTask starts downloading a task by its ID.
// If all download slots are busy, the task remains pending and is queued until
// another task finishes. The download runs in a separate goroutine once active.
//
// Returns an error if:
//   - The task ID is not found
//   - The task is already running or in a terminal state
//
// The task status transitions from pending/paused/failed to running when a
// slot is acquired, or to pending while queued.
func (dm *DownloadManager) StartTask(id string) error {
	dm.configMu.RLock()
	defer dm.configMu.RUnlock()
	dm.startTransitionMu.Lock()
	defer dm.startTransitionMu.Unlock()
	persistenceLocked := dm.statePath != ""
	if persistenceLocked {
		dm.persistenceMu.Lock()
		defer dm.persistenceMu.Unlock()
	}
	return dm.startTaskLocked(id, persistenceLocked)
}

func (dm *DownloadManager) startTaskLocked(id string, persistenceLocked bool) error {
	dm.tasksMu.RLock()
	task, exists := dm.tasks[id]
	dm.tasksMu.RUnlock()

	if !exists {
		return fmt.Errorf("task %s not found", id)
	}

	dm.activeTasksMu.Lock()
	if dm.queuedTaskIDs == nil {
		dm.queuedTaskIDs = make(map[string]struct{})
	}
	if _, queued := dm.queuedTaskIDs[id]; queued {
		dm.activeTasksMu.Unlock()
		return nil
	}

	task.mu.Lock()
	previousStatus := task.Status
	previousError := task.Error
	previousLastError := task.LastError
	previousLastErrorDetail := cloneTaskError(task.LastErrorDetail)
	previousProgress := task.ProgressSummary
	previousSpeed := task.Speed
	previousGenerationCounter := task.generationCounter
	previousQueuedGeneration := task.queuedGeneration
	switch task.Status {
	case StatusDownloading:
		task.mu.Unlock()
		dm.activeTasksMu.Unlock()
		return fmt.Errorf("task %s is already running", id)
	case StatusCompleted, StatusCancelled:
		status := task.Status
		task.mu.Unlock()
		dm.activeTasksMu.Unlock()
		return fmt.Errorf("task %s is %s", id, status)
	case StatusPending, StatusPaused, StatusFailed:
		// A terminal callback may already have published failed/completed state
		// while the worker defer has not closed its done barrier yet. Do not let a
		// retry replace that generation (or a stop coordinator that is still
		// finalizing it).
		if task.execution != nil {
			task.mu.Unlock()
			dm.activeTasksMu.Unlock()
			return workerStoppingTaskError()
		}
	default:
		status := task.Status
		task.mu.Unlock()
		dm.activeTasksMu.Unlock()
		return fmt.Errorf("task %s cannot start from status %s", id, status)
	}

	if dm.activeTasks < dm.maxConcurrent {
		dm.activeTasks++
		execution := newTaskExecutionLocked(task)
		task.Status = StatusDownloading
		task.Error = ""
		task.LastError = ""
		task.LastErrorDetail = nil
		task.ProgressSummary.CurrentStage = "running"
		task.ProgressSummary.StageLabel = "下载中"
		task.mu.Unlock()
		if err := dm.persistIfConfiguredWithLock(persistenceLocked); err != nil {
			task.mu.Lock()
			if task.execution == execution {
				task.execution = nil
				task.cancel = nil
				task.Status = previousStatus
				task.Error = previousError
				task.LastError = previousLastError
				task.LastErrorDetail = previousLastErrorDetail
				task.ProgressSummary = previousProgress
				task.Speed = previousSpeed
				task.generationCounter = previousGenerationCounter
				task.queuedGeneration = previousQueuedGeneration
			}
			task.mu.Unlock()
			execution.cancel()
			execution.finish()
			dm.activeTasks--
			dm.activeTasksMu.Unlock()
			return fmt.Errorf("persist running task transition: %w", err)
		}
		dm.activeTasksMu.Unlock()
		go dm.downloadTask(task, execution)
		return nil
	}

	// Queue for later dispatch instead of failing the add/start flow.
	task.generationCounter++
	task.queuedGeneration = task.generationCounter
	task.Status = StatusPending
	task.Error = ""
	task.LastError = ""
	task.LastErrorDetail = nil
	task.ProgressSummary.CurrentStage = "queued"
	task.ProgressSummary.StageLabel = "等待中"
	task.Speed = 0
	dm.queuedTasks = append(dm.queuedTasks, task)
	dm.queuedTaskIDs[id] = struct{}{}
	task.mu.Unlock()
	if err := dm.persistIfConfiguredWithLock(persistenceLocked); err != nil {
		dm.removeQueuedTaskLocked(id)
		task.mu.Lock()
		task.Status = previousStatus
		task.Error = previousError
		task.LastError = previousLastError
		task.LastErrorDetail = previousLastErrorDetail
		task.ProgressSummary = previousProgress
		task.Speed = previousSpeed
		task.generationCounter = previousGenerationCounter
		task.queuedGeneration = previousQueuedGeneration
		task.mu.Unlock()
		dm.activeTasksMu.Unlock()
		return fmt.Errorf("persist queued task transition: %w", err)
	}
	dm.activeTasksMu.Unlock()
	logger.Info("Queued download task: %s (%s)", task.ID, task.Title)
	return nil
}

func (dm *DownloadManager) removeQueuedTaskLocked(id string) {
	if dm.queuedTaskIDs == nil {
		return
	}
	if _, ok := dm.queuedTaskIDs[id]; !ok {
		return
	}
	delete(dm.queuedTaskIDs, id)
	for i, task := range dm.queuedTasks {
		if task != nil && task.ID == id {
			dm.queuedTasks = append(dm.queuedTasks[:i], dm.queuedTasks[i+1:]...)
			return
		}
	}
}

// removeQueuedTaskInstanceLocked removes only the exact task object that owns
// a stop request. A removed ID may be reused, so an old request must never
// dequeue a newer task that happens to have the same string ID.
func (dm *DownloadManager) removeQueuedTaskInstanceLocked(expected *DownloadTask) {
	if expected == nil || dm.queuedTaskIDs == nil {
		return
	}
	for i, task := range dm.queuedTasks {
		if task == expected {
			dm.queuedTasks = append(dm.queuedTasks[:i], dm.queuedTasks[i+1:]...)
			delete(dm.queuedTaskIDs, expected.ID)
			return
		}
	}
}

func (dm *DownloadManager) dispatchQueued() {
	for {
		var taskToStart *DownloadTask

		dm.configMu.RLock()
		dm.startTransitionMu.Lock()
		persistenceLocked := dm.statePath != ""
		if persistenceLocked {
			dm.persistenceMu.Lock()
		}
		dm.activeTasksMu.Lock()
		if dm.queuedTaskIDs == nil {
			dm.queuedTaskIDs = make(map[string]struct{})
		}
		for dm.activeTasks < dm.maxConcurrent && len(dm.queuedTasks) > 0 {
			task := dm.queuedTasks[0]
			dm.queuedTasks = dm.queuedTasks[1:]
			if task == nil {
				continue
			}
			delete(dm.queuedTaskIDs, task.ID)

			task.mu.Lock()
			if task.Status != StatusPending && task.Status != StatusFailed && task.Status != StatusPaused {
				task.mu.Unlock()
				continue
			}
			dm.activeTasks++
			execution := newQueuedTaskExecutionLocked(task)
			task.Status = StatusDownloading
			task.Error = ""
			task.LastError = ""
			task.LastErrorDetail = nil
			task.ProgressSummary.CurrentStage = "running"
			task.ProgressSummary.StageLabel = "下载中"
			task.mu.Unlock()
			taskToStart = task
			_ = execution
			break
		}
		if taskToStart == nil {
			dm.activeTasksMu.Unlock()
			if persistenceLocked {
				dm.persistenceMu.Unlock()
			}
			dm.startTransitionMu.Unlock()
			dm.configMu.RUnlock()
			return
		}
		taskToStart.mu.RLock()
		execution := taskToStart.execution
		taskToStart.mu.RUnlock()
		if err := dm.persistIfConfiguredWithLock(persistenceLocked); err != nil {
			taskToStart.mu.Lock()
			if taskToStart.execution == execution {
				taskToStart.execution = nil
				taskToStart.cancel = nil
				taskToStart.Status = StatusFailed
				failure := persistenceTaskError("persist dispatched task transition", err)
				taskToStart.Error = failure.Message
				taskToStart.LastError = failure.Message
				taskToStart.LastErrorDetail = failure
				taskToStart.ProgressSummary.CurrentStage = "persistence_failed"
				taskToStart.ProgressSummary.StageLabel = "状态保存失败"
			}
			taskToStart.mu.Unlock()
			execution.cancel()
			execution.finish()
			dm.activeTasks--
			dm.activeTasksMu.Unlock()
			if persistenceLocked {
				dm.persistenceMu.Unlock()
			}
			dm.startTransitionMu.Unlock()
			dm.configMu.RUnlock()
			if dm.onError != nil {
				dm.onError(taskToStart, err)
			}
			return
		}
		dm.activeTasksMu.Unlock()
		if persistenceLocked {
			dm.persistenceMu.Unlock()
		}
		dm.startTransitionMu.Unlock()
		dm.configMu.RUnlock()
		go dm.downloadTask(taskToStart, execution)
	}
}

// PauseTask pauses a downloading task and preserves its progress.
// The task can be resumed later using ResumeTask.
//
// Returns an error if:
//   - The task ID is not found
//   - The task is not currently running
//
// The task status transitions to paused and state is automatically saved.
func (dm *DownloadManager) PauseTask(id string) error {
	_, err := dm.RequestPauseTask(id)
	return err
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
	_, err := dm.RequestCancelTask(id)
	return err
}

// RemoveTask removes a task from the manager completely.
// If the task is currently downloading, it will be stopped first.
// This does not delete the downloaded file; use CancelTask for cleanup.
//
// Returns an error if the task ID is not found.
func (dm *DownloadManager) RemoveTask(id string) error {
	_, err := dm.RequestRemoveTask(id)
	return err
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

// downloadTask is the main download loop that runs in a goroutine.
// DownloadManager does not perform automatic network retries; fetchers and
// platform adapters own bounded lower-level retry/fallback behavior. This method
// decrements activeTasks when it exits (via defer).
func (dm *DownloadManager) downloadTask(task *DownloadTask, execution *taskExecution) {
	// Ensure we decrement the active task counter when this goroutine exits,
	// then dispatch the next queued task if capacity is available.
	defer func() {
		if execution != nil {
			execution.cancel()
			task.mu.Lock()
			// done is the authoritative worker barrier. Close it before detaching
			// the execution so a concurrent stop can never mistake a callback-phase
			// worker for an already joined generation.
			execution.finish()
			if task.execution == execution && !execution.stopRequested {
				task.execution = nil
				task.cancel = nil
			}
			task.mu.Unlock()
		}
		dm.activeTasksMu.Lock()
		if dm.activeTasks > 0 {
			dm.activeTasks--
		}
		dm.activeTasksMu.Unlock()
		dm.dispatchQueued()
	}()

	logger.Info("Starting download task: %s (%s)", task.ID, task.Title)

	err := dm.performDownload(task, execution)
	if err == nil {
		return
	}

	task.mu.RLock()
	stopping := execution == nil || task.execution != execution || execution.stopRequested || execution.phase == executionStopping
	task.mu.RUnlock()
	if stopping {
		return
	}
	task.mu.Lock()
	if task.execution == execution {
		execution.phase = executionFinished
		execution.mutationOpen = false
	}
	task.mu.Unlock()
	dm.handleError(task, err)
}

// performDownload executes a single download attempt.
// It creates a new context for cancellation and dispatches to either
// the registered platform adapter or the default HTTP downloader.
//
// On success, marks the task as completed and invokes the completion callback.
// On failure, returns the error for retry handling by the caller.
func (dm *DownloadManager) performDownload(task *DownloadTask, execution *taskExecution) error {
	if execution == nil {
		return errors.New("download execution is nil")
	}
	task.mu.Lock()
	if task.execution != execution || execution.phase != executionRunning || !execution.mutationOpen {
		task.mu.Unlock()
		return context.Canceled
	}
	task.Status = StatusDownloading
	task.ProgressSummary.CurrentStage = "running"
	task.ProgressSummary.StageLabel = "下载中"
	task.mu.Unlock()

	var err error

	// Dispatch to appropriate download method
	if adapter, ok := dm.adapterForTask(task); ok {
		err = dm.performPlatformDownload(execution.ctx, task, execution, adapter)
	} else {
		err = fmt.Errorf("no platform adapter registered for task %s", task.ID)
	}

	if err != nil {
		task.mu.RLock()
		completionCommitted := task.execution == execution &&
			task.Status == StatusCompleted && task.PublishIntent == nil &&
			hasPrimaryFinalArtifact(task.Artifacts)
		task.mu.RUnlock()
		if !completionCommitted {
			return err
		}
		// The adapter crossed the irreversible primary-publish boundary before
		// returning an error. Completion wins; the later error is diagnostic only
		// and must not downgrade a durable completed task to failed.
		logger.Warn("Adapter returned after primary publish: task=%s err=%v", task.ID, err)
	}

	// Download successful - mark task as completed unless the user cancelled or
	// paused while the lower-level downloader was returning.
	task.mu.Lock()
	if task.execution != execution || execution.stopRequested || execution.phase == executionStopping {
		task.mu.Unlock()
		return context.Canceled
	}
	if !hasPrimaryFinalArtifact(task.Artifacts) {
		task.mu.Unlock()
		return errors.New("platform adapter returned without publishing a primary final artifact")
	}
	execution.phase = executionFinished
	execution.mutationOpen = false
	task.Status = StatusCompleted
	task.ProgressSummary.Percent = 100
	task.ProgressSummary.CurrentStage = "completed"
	task.ProgressSummary.StageLabel = "已完成"
	if task.CompletedAt == 0 {
		task.CompletedAt = time.Now().Unix()
	}
	outputPath := primaryFinalArtifactPath(task.Artifacts)
	task.mu.Unlock()
	dm.outputAllocator.Release(task.ID)

	logger.Info("Download completed: %s (%s)", task.Title, outputPath)

	// PublishFinal persisted the primary artifact and completed status in one
	// synchronous commit. Only emit completion after that commit has succeeded.
	if dm.onComplete != nil {
		dm.onComplete(task)
	}

	return nil
}

type managerExecutionContext struct {
	dm          *DownloadManager
	task        *DownloadTask
	fetcher     fetch.Fetcher
	execution   *taskExecution
	mu          sync.Mutex
	lastPersist time.Time
	lastPercent float64
}

func newManagerExecutionContext(dm *DownloadManager, task *DownloadTask, execution *taskExecution) *managerExecutionContext {
	fetcherInstance := dm.fetcher
	if fetcherInstance == nil {
		fetcherInstance = fetch.New(nil)
	}
	return &managerExecutionContext{
		dm:          dm,
		task:        task,
		fetcher:     fetcherInstance,
		execution:   execution,
		lastPersist: time.Now(),
	}
}

func (ctx *managerExecutionContext) Fetcher() fetch.Fetcher {
	return ctx.fetcher
}

func (ctx *managerExecutionContext) FFmpeg() downloadtask.FFmpegLocator {
	if ctx == nil || ctx.dm == nil {
		return nil
	}
	return ctx.dm.ffmpeg
}

func (ctx *managerExecutionContext) Credentials() downloadtask.CredentialStore {
	if ctx == nil || ctx.dm == nil {
		return nil
	}
	return ctx.dm.credentials
}

func (ctx *managerExecutionContext) UpdateTaskProgress(update downloadtask.TaskProgressUpdate) error {
	if ctx == nil || ctx.task == nil {
		return nil
	}
	if err := ctx.lockMutation(); err != nil {
		return err
	}
	percent := ctx.task.ProgressSummary.Percent
	if update.StagePercent != nil {
		percent = *update.StagePercent
	}
	if update.OverallPercent != nil {
		percent = *update.OverallPercent
	}
	percent = clampPercent(percent)
	ctx.task.ProgressSummary.Percent = percent
	ctx.task.ProgressSummary.CurrentStage = strings.TrimSpace(update.StageID)
	ctx.task.ProgressSummary.StageLabel = strings.TrimSpace(update.StageLabel)
	if ctx.task.ProgressSummary.CurrentStage == "" {
		ctx.task.ProgressSummary.CurrentStage = "running"
	}
	if ctx.task.ProgressSummary.StageLabel == "" {
		ctx.task.ProgressSummary.StageLabel = "下载中"
	}
	if update.BytesLoaded > 0 || update.BytesTotal > 0 {
		ctx.task.ProgressSummary.BytesLoaded = update.BytesLoaded
	}
	if update.BytesTotal > 0 {
		ctx.task.ProgressSummary.BytesTotal = update.BytesTotal
	}
	if update.ItemsDone > 0 || update.ItemsTotal > 0 {
		ctx.task.ProgressSummary.ItemsDone = update.ItemsDone
	}
	if update.ItemsTotal > 0 {
		ctx.task.ProgressSummary.ItemsTotal = update.ItemsTotal
	}
	ctx.task.mu.Unlock()

	now := time.Now()
	ctx.mu.Lock()
	persist := now.Sub(ctx.lastPersist) >= 2*time.Second || absFloat(percent-ctx.lastPercent) >= 1 || percent >= 100
	if persist {
		ctx.lastPersist = now
		ctx.lastPercent = percent
	}
	ctx.mu.Unlock()

	if ctx.dm != nil && ctx.dm.onProgress != nil {
		ctx.dm.onProgress(ctx.task)
	}
	if persist && ctx.dm != nil {
		ctx.dm.saveStateBestEffort()
	}
	return nil
}

func absFloat(value float64) float64 {
	if value < 0 {
		return -value
	}
	return value
}

func (ctx *managerExecutionContext) RecordArtifact(artifact downloadtask.TaskArtifact) error {
	if ctx == nil || ctx.task == nil {
		return nil
	}
	artifact, err := prepareTemporaryArtifact(artifact)
	if err != nil {
		return err
	}
	if err := ctx.lockMutation(); err != nil {
		return err
	}
	ctx.task.Artifacts = upsertArtifact(ctx.task.Artifacts, artifact)
	ctx.task.mu.Unlock()
	if ctx.dm != nil {
		ctx.dm.saveStateBestEffort()
	}
	return nil
}

// RecordPostPublishCleanupFailure persists a narrowly-scoped diagnostic after
// primary publication has closed every normal mutation sink. It never reopens
// progress/checkpoint/artifact mutation and never downgrades completed status.
func (ctx *managerExecutionContext) RecordPostPublishCleanupFailure(artifact downloadtask.TaskArtifact) error {
	if ctx == nil || ctx.dm == nil || ctx.task == nil || ctx.execution == nil {
		return ErrStaleExecution
	}
	artifact, err := prepareTemporaryArtifact(artifact)
	if err != nil {
		return err
	}
	if artifact.Primary {
		return errors.New("post-publish cleanup diagnostic cannot be a primary artifact")
	}
	if strings.TrimSpace(artifact.Metadata["cleanupError"]) == "" {
		return errors.New("post-publish cleanup diagnostic requires cleanupError metadata")
	}

	persistenceLocked := ctx.dm.statePath != ""
	if persistenceLocked {
		ctx.dm.persistenceMu.Lock()
	}
	ctx.task.mu.Lock()
	validPhase := ctx.execution.phase == executionFinished || ctx.execution.phase == executionStopping
	if ctx.task.execution != ctx.execution || !validPhase || ctx.execution.mutationOpen ||
		ctx.task.Status != StatusCompleted || !hasPrimaryFinalArtifact(ctx.task.Artifacts) {
		ctx.task.mu.Unlock()
		if persistenceLocked {
			ctx.dm.persistenceMu.Unlock()
		}
		return fmt.Errorf("%w: post-publish cleanup task=%s generation=%d", ErrStaleExecution, ctx.task.ID, ctx.execution.generation)
	}
	previousArtifacts := downloadtask.CloneSnapshot(downloadtask.TaskSnapshot{Artifacts: ctx.task.Artifacts}).Artifacts
	ctx.task.Artifacts = upsertArtifact(ctx.task.Artifacts, artifact)
	ctx.task.mu.Unlock()

	if err := ctx.dm.persistIfConfiguredWithLock(persistenceLocked); err != nil {
		ctx.task.mu.Lock()
		if ctx.task.execution == ctx.execution {
			ctx.task.Artifacts = previousArtifacts
		}
		ctx.task.mu.Unlock()
		if persistenceLocked {
			ctx.dm.persistenceMu.Unlock()
		}
		logger.Warn("Failed to persist post-publish cleanup diagnostic: task=%s path=%s err=%v", ctx.task.ID, artifact.Path, err)
		return fmt.Errorf("persist post-publish cleanup diagnostic: %w", err)
	}
	if persistenceLocked {
		ctx.dm.persistenceMu.Unlock()
	}
	if ctx.dm.onProgress != nil {
		ctx.dm.onProgress(ctx.task)
	}
	logger.Warn("Post-publish cleanup left a temporary artifact: task=%s path=%s", ctx.task.ID, artifact.Path)
	return nil
}

func prepareTemporaryArtifact(artifact downloadtask.TaskArtifact) (downloadtask.TaskArtifact, error) {
	if strings.TrimSpace(artifact.Path) == "" {
		return artifact, fmt.Errorf("artifact path is required")
	}
	if !filepath.IsAbs(artifact.Path) {
		return artifact, errors.New("artifact path must be an absolute local path")
	}
	artifact.Path = filepath.Clean(artifact.Path)
	if artifact.Kind == downloadtask.TaskArtifactFinal {
		return artifact, errors.New("final artifacts must be committed through PublishFinal")
	}
	if artifact.Kind != downloadtask.TaskArtifactTemporary {
		return artifact, fmt.Errorf("unsupported recordable artifact kind %q", artifact.Kind)
	}
	if artifact.FileName == "" {
		artifact.FileName = filepath.Base(artifact.Path)
	}
	if artifact.CreatedAt == 0 {
		artifact.CreatedAt = time.Now().Unix()
	}
	if artifact.Metadata != nil {
		metadata := make(map[string]string, len(artifact.Metadata))
		for key, value := range artifact.Metadata {
			metadata[key] = value
		}
		artifact.Metadata = metadata
	}
	return artifact, nil
}

func (ctx *managerExecutionContext) RecordCheckpoint(checkpoint downloadtask.PlatformCheckpointEnvelope) error {
	if ctx == nil || ctx.task == nil {
		return nil
	}
	if checkpoint.Version <= 0 {
		return fmt.Errorf("checkpoint version must be positive")
	}
	if len(checkpoint.Data) > 0 && !json.Valid(checkpoint.Data) {
		return fmt.Errorf("checkpoint is not valid JSON")
	}
	if err := ctx.lockMutation(); err != nil {
		return err
	}
	if checkpointsEqual(ctx.task.PlatformCheckpoint, &checkpoint) {
		ctx.task.mu.Unlock()
		return nil
	}
	ctx.task.PlatformCheckpoint = cloneCheckpoint(&checkpoint)
	ctx.task.mu.Unlock()
	if ctx.dm != nil {
		ctx.dm.saveStateBestEffort()
	}
	return nil
}

var ErrStaleExecution = errors.New("stale execution")

func (ctx *managerExecutionContext) lockMutation() error {
	if ctx == nil || ctx.task == nil || ctx.execution == nil {
		return ErrStaleExecution
	}
	ctx.task.mu.Lock()
	if ctx.task.execution != ctx.execution || ctx.execution.phase != executionRunning || !ctx.execution.mutationOpen {
		ctx.task.mu.Unlock()
		return fmt.Errorf("%w: task=%s generation=%d", ErrStaleExecution, ctx.task.ID, ctx.execution.generation)
	}
	return nil
}

func checkpointsEqual(left, right *downloadtask.PlatformCheckpointEnvelope) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.Version == right.Version && string(left.Data) == string(right.Data)
}

func clampPercent(percent float64) float64 {
	if percent < 0 {
		return 0
	}
	if percent > 100 {
		return 100
	}
	return percent
}

func hasPrimaryFinalArtifact(artifacts []downloadtask.TaskArtifact) bool {
	for _, artifact := range artifacts {
		if artifact.Primary && artifact.Kind == downloadtask.TaskArtifactFinal && strings.TrimSpace(artifact.Path) != "" {
			return true
		}
	}
	return false
}

func primaryFinalArtifactPath(artifacts []downloadtask.TaskArtifact) string {
	for _, artifact := range artifacts {
		if artifact.Primary && artifact.Kind == downloadtask.TaskArtifactFinal {
			return artifact.Path
		}
	}
	return ""
}

func upsertArtifact(artifacts []downloadtask.TaskArtifact, artifact downloadtask.TaskArtifact) []downloadtask.TaskArtifact {
	if strings.TrimSpace(artifact.Path) == "" {
		return artifacts
	}
	if artifact.FileName == "" {
		artifact.FileName = filepath.Base(artifact.Path)
	}
	if artifact.CreatedAt == 0 {
		artifact.CreatedAt = time.Now().Unix()
	}
	for i, existing := range artifacts {
		if existing.Path == artifact.Path || (artifact.Primary && existing.Primary && existing.Kind == artifact.Kind) {
			next := append([]downloadtask.TaskArtifact(nil), artifacts...)
			next[i] = artifact
			return next
		}
	}
	return append(append([]downloadtask.TaskArtifact(nil), artifacts...), artifact)
}

func (dm *DownloadManager) performPlatformDownload(ctx context.Context, task *DownloadTask, generation *taskExecution, adapter downloadtask.PlatformAdapter) error {
	execution := newManagerExecutionContext(dm, task, generation)
	if err := adapter.RunTask(ctx, downloadtask.CloneSnapshot(taskSnapshot(task)), execution); err != nil {
		return err
	}
	return nil
}

// handleError handles download errors by updating task status and invoking the error callback.
// This is called when a download fails after exhausting all retry attempts.
func (dm *DownloadManager) handleError(task *DownloadTask, err error) {
	taskErr := taskErrorFromError(err)
	publicErr := publicTaskError(taskErr)
	logger.Error("Download failed: task=%s title=%q code=%s host=%s", task.ID, task.Title, publicErr.Code, publicErr.Metadata["urlHost"])

	// Update task status to failed unless the user paused/cancelled while the
	// error was being handled.
	task.mu.Lock()
	if task.Status == StatusCancelled || task.Status == StatusPaused {
		task.mu.Unlock()
		return
	}
	task.Status = StatusFailed
	task.ProgressSummary.CurrentStage = "failed"
	task.ProgressSummary.StageLabel = "失败"
	task.Error = publicErr.Message
	task.LastError = publicErr.Message
	task.LastErrorDetail = taskErr
	task.mu.Unlock()
	if !taskErr.Retryable {
		dm.outputAllocator.Release(task.ID)
	}

	// Persist state for potential recovery
	dm.saveStateBestEffort()

	// Notify via callback
	if dm.onError != nil {
		dm.onError(task, err)
	}
}

func taskErrorFromError(err error) *downloadtask.TaskError {
	if err == nil {
		return nil
	}
	var structured *downloadtask.TaskError
	if errors.As(err, &structured) {
		return cloneTaskError(structured)
	}
	var fetchErr *fetch.Error
	if errors.As(err, &fetchErr) {
		category := downloadtask.TaskErrorCategoryTransport
		code := "fetch." + string(fetchErr.Kind)
		if fetchErr.Kind == fetch.ErrorCanceled {
			category = downloadtask.TaskErrorCategoryCanceled
			code = "task.canceled"
		}
		retryable := fetchErr.Kind == fetch.ErrorNetwork || fetchErr.Kind == fetch.ErrorTimeout ||
			(fetchErr.Kind == fetch.ErrorStatusCode && fetchErr.StatusCode >= 500 && fetchErr.StatusCode < 600)
		metadata := map[string]string{}
		if fetchErr.URL != "" {
			metadata["url"] = fetchErr.URL
		}
		if fetchErr.StatusCode > 0 {
			metadata["statusCode"] = fmt.Sprintf("%d", fetchErr.StatusCode)
		}
		if fetchErr.Attempts > 0 {
			metadata["attempts"] = fmt.Sprintf("%d", fetchErr.Attempts)
		}
		message := "下载请求失败"
		userAction := "请稍后重试"
		if fetchErr.Kind == fetch.ErrorCanceled {
			message = "下载已取消"
			userAction = ""
		} else if fetchErr.Kind == fetch.ErrorIntegrity || fetchErr.Kind == fetch.ErrorIdentityMismatch {
			message = "下载文件校验失败"
			userAction = "请重试下载"
		} else if fetchErr.Kind == fetch.ErrorSizeLimit {
			message = "下载内容超过允许的大小限制"
			userAction = "请检查下载设置"
		} else if fetchErr.StatusCode == 401 || fetchErr.StatusCode == 403 {
			message = "下载凭据已失效"
			userAction = "请重新登录或刷新凭据"
		}
		return &downloadtask.TaskError{
			Code:       code,
			Category:   category,
			Message:    message,
			Retryable:  retryable,
			UserAction: userAction,
			Cause:      err.Error(),
			Metadata:   metadata,
		}
	}
	return &downloadtask.TaskError{
		Code:       "task.unexpected_error",
		Category:   downloadtask.TaskErrorCategoryUnexpected,
		Message:    "下载任务发生意外错误",
		Retryable:  false,
		UserAction: "请重试；如果问题持续出现，请查看日志",
		Cause:      err.Error(),
	}
}

func persistenceTaskError(operation string, err error) *downloadtask.TaskError {
	cause := strings.TrimSpace(operation)
	if err != nil {
		cause += ": " + err.Error()
	}
	return &downloadtask.TaskError{
		Code:       "task.persistence_failed",
		Category:   downloadtask.TaskErrorCategoryUnexpected,
		Message:    "无法保存下载任务状态",
		Retryable:  true,
		UserAction: "请检查应用数据目录后重试",
		Cause:      cause,
	}
}

func publicTaskError(private *downloadtask.TaskError) *downloadtask.TaskError {
	if private == nil {
		return nil
	}
	public := &downloadtask.TaskError{
		Code:       private.Code,
		Category:   private.Category,
		Message:    private.Message,
		Retryable:  private.Retryable,
		UserAction: private.UserAction,
	}
	metadata := make(map[string]string)
	for _, key := range []string{"statusCode", "attempts", "urlHost"} {
		if value := strings.TrimSpace(private.Metadata[key]); value != "" {
			metadata[key] = value
		}
	}
	if _, exists := metadata["urlHost"]; !exists {
		if rawURL := strings.TrimSpace(private.Metadata["url"]); rawURL != "" {
			if parsed, err := url.Parse(rawURL); err == nil && parsed.Hostname() != "" {
				metadata["urlHost"] = parsed.Hostname()
			}
		}
	}
	if len(metadata) > 0 {
		public.Metadata = metadata
	}
	return public
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
	return dm.retryTaskExpected(id, 0, 0)
}

func (dm *DownloadManager) RetryTaskExpected(id string, instance, generation uint64) error {
	return dm.retryTaskExpected(id, instance, generation)
}

func (dm *DownloadManager) retryTaskExpected(id string, expectedInstance, expectedGeneration uint64) error {
	return dm.startEligibleTaskExpected(id, expectedInstance, expectedGeneration, StatusFailed, true)
}

func (dm *DownloadManager) startEligibleTaskExpected(
	id string,
	expectedInstance, expectedGeneration uint64,
	requiredStatus DownloadStatus,
	requireRetryable bool,
) error {
	dm.configMu.RLock()
	defer dm.configMu.RUnlock()
	dm.startTransitionMu.Lock()
	defer dm.startTransitionMu.Unlock()
	// Identity mutation is serialized by persistenceMu even when no state path
	// is configured. This makes expected-instance validation and StartTask one
	// compare-and-act transaction.
	dm.persistenceMu.Lock()
	defer dm.persistenceMu.Unlock()

	dm.tasksMu.RLock()
	task, exists := dm.tasks[id]
	if !exists {
		dm.tasksMu.RUnlock()
		return fmt.Errorf("task %s not found", id)
	}

	task.mu.RLock()
	if err := validateExpectedTaskLocked(task, expectedInstance, expectedGeneration); err != nil {
		task.mu.RUnlock()
		dm.tasksMu.RUnlock()
		return err
	}
	if task.Status != requiredStatus {
		status := task.Status
		task.mu.RUnlock()
		dm.tasksMu.RUnlock()
		return fmt.Errorf("task %s has status %s, want %s", id, status, requiredStatus)
	}
	if requireRetryable && (task.LastErrorDetail == nil || !task.LastErrorDetail.Retryable) {
		task.mu.RUnlock()
		dm.tasksMu.RUnlock()
		return fmt.Errorf("task %s failure is not retryable; follow the required user action or create a new task", id)
	}
	resumeFrom := task.ProgressSummary.BytesLoaded
	task.mu.RUnlock()
	dm.tasksMu.RUnlock()

	if requiredStatus == StatusPaused {
		logger.Info("Resuming task %s from %d bytes", id, resumeFrom)
	}
	return dm.startTaskLocked(id, true)
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
// For adapter-owned tasks, platform temporary artifacts are managed by the
// adapter, so generic final-file verification is skipped.
// For generic multipart downloads, the resume state is tracked separately in a
// state file.
//
// Returns an error if:
//   - The task ID is not found
//   - The task is not in paused state
//   - The partial file cannot be accessed
func (dm *DownloadManager) ResumeTask(id string) error {
	return dm.resumeTaskExpected(id, 0, 0)
}

func (dm *DownloadManager) ResumeTaskExpected(id string, instance, generation uint64) error {
	return dm.resumeTaskExpected(id, instance, generation)
}

func (dm *DownloadManager) resumeTaskExpected(id string, expectedInstance, expectedGeneration uint64) error {
	return dm.startEligibleTaskExpected(id, expectedInstance, expectedGeneration, StatusPaused, false)
}

// GetDownloadingTaskCount returns the number of tasks currently in active download state.
// Thread-safe.
func (dm *DownloadManager) GetDownloadingTaskCount() int {
	dm.tasksMu.RLock()
	defer dm.tasksMu.RUnlock()

	count := 0
	for _, task := range dm.tasks {
		task.mu.RLock()
		if task.Status == StatusDownloading {
			count++
		}
		task.mu.RUnlock()
	}
	return count
}
