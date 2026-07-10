package downloader

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	downloadtask "EasyDownload/internal/download/task"
	"EasyDownload/internal/infra/logger"
)

// CurrentTaskSchemaVersion is kept for source compatibility. File format
// versioning is authoritative only at TaskStoreEnvelope.SchemaVersion.
const CurrentTaskSchemaVersion = TaskStoreSchemaVersion

// SaveState synchronously commits a complete, immutable v2 snapshot. The
// manager serializes capture and save so a lower revision can never overwrite a
// newer deletion or mutation.
func (dm *DownloadManager) SaveState() error {
	if dm == nil || dm.statePath == "" {
		return fmt.Errorf("state path not configured")
	}
	dm.persistenceMu.Lock()
	defer dm.persistenceMu.Unlock()
	return dm.saveStateSnapshotLocked("")
}

// saveStateSnapshotLocked commits a snapshot while the caller owns
// persistenceMu. Map mutations that must be atomic with their durable view use
// this helper to prevent an unrelated SaveState from observing an intermediate
// create/remove state.
func (dm *DownloadManager) saveStateSnapshotLocked(excludedTaskID string) error {
	if dm.taskStore == nil || dm.taskStore.Path() != dm.statePath {
		dm.taskStore = NewTaskStore(dm.statePath)
	}

	dm.tasksMu.RLock()
	tasks := make([]downloadtask.TaskSnapshot, 0, len(dm.tasks))
	now := time.Now().Unix()
	for _, task := range dm.tasks {
		if excludedTaskID != "" && task.ID == excludedTaskID {
			continue
		}
		snapshot := taskSnapshot(task)
		completedAt := taskCompletedAt(task)
		if snapshot.Status != downloadtask.StatusCompleted || now-completedAt < 86400 {
			tasks = append(tasks, snapshot)
		}
	}
	dm.tasksMu.RUnlock()

	revision := dm.revision.Add(1)
	envelope := TaskStoreEnvelope{
		SchemaVersion: TaskStoreSchemaVersion,
		Revision:      revision,
		Tasks:         tasks,
	}
	if err := dm.taskStore.Save(context.Background(), envelope); err != nil {
		return fmt.Errorf("save task store revision %d: %w", envelope.Revision, err)
	}
	logger.Debug("Download v2 state saved: path=%s revision=%d", dm.statePath, envelope.Revision)
	return nil
}

func taskCompletedAt(task *DownloadTask) int64 {
	task.mu.RLock()
	defer task.mu.RUnlock()
	return task.CompletedAt
}

// LoadState restores v2 tasks only. The legacy v1 path is deliberately never
// read, moved, or overwritten by this manager.
func (dm *DownloadManager) LoadState() error {
	if dm == nil || dm.statePath == "" {
		return fmt.Errorf("state path not configured")
	}
	dm.persistenceMu.Lock()
	defer dm.persistenceMu.Unlock()
	if dm.taskStore == nil || dm.taskStore.Path() != dm.statePath {
		dm.taskStore = NewTaskStore(dm.statePath)
	}
	envelope, err := dm.taskStore.Load(context.Background())
	if err != nil {
		return err
	}
	if envelope.SchemaVersion != TaskStoreSchemaVersion {
		return fmt.Errorf("%w: %d", ErrUnknownTaskSchema, envelope.SchemaVersion)
	}

	changed := false
	dm.tasksMu.RLock()
	existingTaskIDs := make(map[string]struct{}, len(dm.tasks))
	for id := range dm.tasks {
		existingTaskIDs[id] = struct{}{}
	}
	dm.tasksMu.RUnlock()
	seenTaskIDs := make(map[string]struct{}, len(envelope.Tasks))
	restored := make([]*DownloadTask, 0, len(envelope.Tasks))
	for _, persisted := range envelope.Tasks {
		snapshot := downloadtask.CloneSnapshot(persisted)
		if snapshot.ID == "" || snapshot.PlatformID == "" {
			return fmt.Errorf("invalid persisted task contract: id=%q platform=%q", snapshot.ID, snapshot.PlatformID)
		}
		if err := downloadtask.RequireCurrentPlatformDataVersion(snapshot); err != nil {
			return fmt.Errorf("invalid persisted task contract for id=%q: %w", snapshot.ID, err)
		}
		if _, duplicate := seenTaskIDs[snapshot.ID]; duplicate {
			return fmt.Errorf("invalid persisted task contract: duplicate id=%q", snapshot.ID)
		}
		seenTaskIDs[snapshot.ID] = struct{}{}
		if _, exists := existingTaskIDs[snapshot.ID]; exists {
			continue
		}
		adapter, registered := dm.platformRegistry.Get(snapshot.PlatformID)
		if !registered {
			return fmt.Errorf("invalid persisted task contract: no adapter registered for platform=%q", snapshot.PlatformID)
		}
		if err := adapter.ValidateTask(downloadtask.CloneSnapshot(snapshot)); err != nil {
			return fmt.Errorf("invalid persisted task contract for id=%q: %w", snapshot.ID, err)
		}
		task := downloadTaskFromSnapshot(snapshot)
		task.eventInstance = dm.taskInstance.Add(1)
		if task.CreatedAt == 0 {
			task.CreatedAt = time.Now().Unix()
			changed = true
		}
		if task.Status == StatusPending {
			// A persisted pending task has no durable queue position and LoadState
			// does not auto-start work. Expose it as paused so the existing Resume
			// command can explicitly create a fresh generation after restart.
			task.Status = StatusPaused
			task.ProgressSummary.CurrentStage = "paused"
			task.ProgressSummary.StageLabel = "已暂停"
			changed = true
		}
		if task.Status == StatusDownloading {
			// Compatibility recovery for snapshots written by early v2 builds:
			// publishing the final artifact used to commit before the completed
			// status. A primary final artifact is therefore an already-completed
			// task, not a resumable running task.
			if task.PublishIntent == nil && hasPrimaryFinalArtifact(task.Artifacts) {
				task.Status = StatusCompleted
				task.ProgressSummary.Percent = 100
				task.ProgressSummary.CurrentStage = "completed"
				task.ProgressSummary.StageLabel = "已完成"
				if task.CompletedAt == 0 {
					task.CompletedAt = primaryFinalArtifactCreatedAt(task.Artifacts)
				}
				if task.CompletedAt == 0 {
					task.CompletedAt = time.Now().Unix()
				}
			} else {
				task.Status = StatusPaused
				task.ProgressSummary.CurrentStage = "paused"
				task.ProgressSummary.StageLabel = "已暂停"
			}
			changed = true
		}
		if task.Status == StatusCompleted && task.CompletedAt == 0 {
			task.CompletedAt = primaryFinalArtifactCreatedAt(task.Artifacts)
			if task.CompletedAt == 0 {
				task.CompletedAt = time.Now().Unix()
			}
			changed = true
		}
		restored = append(restored, task)
	}

	// Parsing and normalization above are side-effect free. Only after every
	// snapshot has passed validation do we publish the batch to the manager, so
	// one malformed later entry can never leave a partially loaded task set.
	dm.tasksMu.Lock()
	inserted := restored[:0]
	for _, task := range restored {
		if _, exists := dm.tasks[task.ID]; exists {
			continue
		}
		dm.tasks[task.ID] = task
		inserted = append(inserted, task)
	}
	restored = inserted
	dm.setRevisionAtLeast(envelope.Revision)
	dm.tasksMu.Unlock()

	reservationFailed := make(map[*DownloadTask]bool)
	// Restore every active reservation before any publish-intent recovery may
	// reallocate a path. Otherwise an earlier recovered task can steal a later
	// task's persisted name merely because of envelope iteration order.
	for _, task := range restored {
		if !shouldReserveOutput(task) {
			continue
		}
		task.mu.RLock()
		policy := task.OutputPolicy
		task.mu.RUnlock()
		if err := dm.outputAllocator.Restore(task.ID, policy); err != nil {
			markPublishRecoveryFailure(task, err)
			dm.outputAllocator.Release(task.ID)
			reservationFailed[task] = true
			changed = true
		}
	}

	for _, task := range restored {
		task.mu.RLock()
		hadPublishIntent := task.PublishIntent != nil
		task.mu.RUnlock()
		if hadPublishIntent {
			changed = true
		}
		if !reservationFailed[task] {
			if err := dm.restoreOutputState(task); err != nil {
				markPublishRecoveryFailure(task, err)
				dm.outputAllocator.Release(task.ID)
				changed = true
			}
		}
	}
	if changed {
		// We already hold persistenceMu; commit directly to avoid re-entering
		// SaveState. This normalized snapshot is causally after the loaded one.
		revision := dm.revision.Add(1)
		normalized := dm.captureEnvelopeLocked(revision)
		if err := dm.taskStore.Save(context.Background(), normalized); err != nil {
			return err
		}
	}
	logger.Info("Loaded %d download tasks from v2 state revision %d", len(restored), dm.revision.Load())
	return nil
}

func primaryFinalArtifactCreatedAt(artifacts []downloadtask.TaskArtifact) int64 {
	for _, artifact := range artifacts {
		if artifact.Primary && artifact.Kind == downloadtask.TaskArtifactFinal {
			return artifact.CreatedAt
		}
	}
	return 0
}

func (dm *DownloadManager) setRevisionAtLeast(revision uint64) {
	for {
		current := dm.revision.Load()
		if current >= revision || dm.revision.CompareAndSwap(current, revision) {
			return
		}
	}
}

func (dm *DownloadManager) captureEnvelopeLocked(revision uint64) TaskStoreEnvelope {
	dm.tasksMu.RLock()
	defer dm.tasksMu.RUnlock()
	tasks := make([]downloadtask.TaskSnapshot, 0, len(dm.tasks))
	for _, task := range dm.tasks {
		tasks = append(tasks, taskSnapshot(task))
	}
	return TaskStoreEnvelope{SchemaVersion: TaskStoreSchemaVersion, Revision: revision, Tasks: tasks}
}

func downloadTaskFromSnapshot(snapshot downloadtask.TaskSnapshot) *DownloadTask {
	task := &DownloadTask{
		PlatformID:          string(snapshot.PlatformID),
		PlatformDataVersion: snapshot.PlatformDataVersion,
		PlatformData:        append(json.RawMessage(nil), snapshot.PlatformData...),
		PlatformCheckpoint:  cloneCheckpoint(snapshot.PlatformCheckpoint),
		PublishIntent:       clonePublishIntent(snapshot.PublishIntent),
		OutputPolicy:        snapshot.OutputPolicy,
		ID:                  snapshot.ID,
		Title:               snapshot.Title,
		Cover:               snapshot.Cover,
		DisplaySource:       snapshot.DisplaySource,
		CreatedAt:           snapshot.CreatedAt,
		CompletedAt:         snapshot.CompletedAt,
		ProgressSummary:     snapshot.Progress,
		Artifacts:           downloadtask.CloneSnapshot(snapshot).Artifacts,
		Status:              DownloadStatus(snapshot.Status),
		LastErrorDetail:     cloneTaskError(snapshot.LastError),
	}
	if task.LastErrorDetail != nil {
		publicError := publicTaskError(task.LastErrorDetail)
		task.Error = publicError.Message
		task.LastError = publicError.Message
	}
	if task.PublishIntent != nil && task.PublishIntent.Generation > task.generationCounter {
		task.generationCounter = task.PublishIntent.Generation
	}
	return task
}

func cloneCheckpoint(checkpoint *downloadtask.PlatformCheckpointEnvelope) *downloadtask.PlatformCheckpointEnvelope {
	if checkpoint == nil {
		return nil
	}
	clone := *checkpoint
	clone.Data = append(json.RawMessage(nil), checkpoint.Data...)
	return &clone
}

func clonePublishIntent(intent *downloadtask.PublishIntent) *downloadtask.PublishIntent {
	if intent == nil {
		return nil
	}
	snapshot := downloadtask.CloneSnapshot(downloadtask.TaskSnapshot{PublishIntent: intent})
	return snapshot.PublishIntent
}

func shouldReserveOutput(task *DownloadTask) bool {
	task.mu.RLock()
	defer task.mu.RUnlock()
	if strings.TrimSpace(task.OutputPolicy.PlannedFinalPath) == "" {
		return false
	}
	if task.PublishIntent != nil {
		return true
	}
	if task.Status == StatusPending || task.Status == StatusDownloading || task.Status == StatusPaused {
		return true
	}
	return task.Status == StatusFailed && task.LastErrorDetail != nil && task.LastErrorDetail.Retryable
}

func (dm *DownloadManager) restoreOutputState(task *DownloadTask) error {
	task.mu.Lock()
	intent := clonePublishIntent(task.PublishIntent)
	task.mu.Unlock()
	if intent == nil {
		return nil
	}
	if _, err := normalizeSHA256(intent.Draft.SHA256); err != nil {
		return fmt.Errorf("invalid publish intent: %w", err)
	}

	finalExists, err := regularFileExists(intent.PlannedFinalPath)
	if err != nil {
		return fmt.Errorf("inspect publish final: %w", err)
	}
	temporaryExists, err := regularFileExists(intent.TemporaryPath)
	if err != nil {
		return fmt.Errorf("inspect publish temporary: %w", err)
	}

	if finalExists && !temporaryExists {
		if !matchesArtifactDraft(intent.PlannedFinalPath, intent.Draft) {
			return errors.New("published final file does not match the persisted artifact draft")
		}
		dm.claimRecoveredArtifact(task, intent)
		return nil
	}
	if !temporaryExists {
		return errors.New("publish intent has neither a matching final file nor a temporary file")
	}

	validatedDraft, err := inspectArtifactDraft(intent.TemporaryPath, intent.Draft)
	if err != nil {
		return fmt.Errorf("validate publish temporary: %w", err)
	}
	intent.Draft = validatedDraft
	task.mu.RLock()
	policy := task.OutputPolicy
	task.mu.RUnlock()

	// final+temporary is never treated as a successful prior publish, even if
	// the bytes happen to match. The final name is externally occupied; retain
	// the validated temporary artifact and allocate a new final path.
	needsReallocation := finalExists
	for attempts := 0; attempts < 100; attempts++ {
		if needsReallocation {
			policy, err = dm.outputAllocator.Reallocate(task.ID, policy)
			if err != nil {
				return fmt.Errorf("reallocate recovered publish path: %w", err)
			}
			intent.PlannedFinalPath = policy.PlannedFinalPath
		}
		dm.setRecoveryPublishIntent(task, policy, intent)
		if err := dm.persistRecoverySnapshot(); err != nil {
			return fmt.Errorf("persist recovered publish intent: %w", err)
		}
		outcome, err := publishNoReplace(intent.TemporaryPath, intent.PlannedFinalPath)
		if err != nil {
			if errors.Is(err, ErrOutputExists) {
				needsReallocation = true
				continue
			}
			return err
		}
		intent.Draft = applyPublishWarnings(intent.Draft, outcome)
		if !matchesArtifactDraft(intent.PlannedFinalPath, intent.Draft) {
			return errors.New("recovered publish produced a final file that does not match its draft")
		}
		dm.claimRecoveredArtifact(task, intent)
		return nil
	}
	return errors.New("recovered output path reallocation limit exceeded")
}

func regularFileExists(path string) (bool, error) {
	info, err := os.Stat(path)
	if err == nil {
		if info.IsDir() {
			return false, fmt.Errorf("%s is a directory", path)
		}
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

func (dm *DownloadManager) setRecoveryPublishIntent(task *DownloadTask, policy downloadtask.OutputPolicy, intent *downloadtask.PublishIntent) {
	task.mu.Lock()
	task.OutputPolicy = policy
	task.PublishIntent = clonePublishIntent(intent)
	task.mu.Unlock()
}

// persistRecoverySnapshot is called only while LoadState owns persistenceMu.
// Reallocated intent must become durable before the no-replace publish.
func (dm *DownloadManager) persistRecoverySnapshot() error {
	revision := dm.revision.Add(1)
	return dm.taskStore.Save(context.Background(), dm.captureEnvelopeLocked(revision))
}

func (dm *DownloadManager) claimRecoveredArtifact(task *DownloadTask, intent *downloadtask.PublishIntent) {
	artifact := artifactFromDraft(intent.PlannedFinalPath, intent.Draft)
	task.mu.Lock()
	task.Artifacts = upsertArtifact(task.Artifacts, artifact)
	task.PublishIntent = nil
	if intent.Draft.Primary {
		task.Status = StatusCompleted
		task.ProgressSummary.Percent = 100
		task.ProgressSummary.CurrentStage = "completed"
		task.ProgressSummary.StageLabel = "已完成"
		task.CompletedAt = time.Now().Unix()
	}
	task.mu.Unlock()
	dm.outputAllocator.Release(task.ID)
}

func markPublishRecoveryFailure(task *DownloadTask, err error) {
	taskErr := &downloadtask.TaskError{
		Code:       "task.publish_recovery_failed",
		Category:   downloadtask.TaskErrorCategoryOutput,
		Message:    "无法安全恢复文件发布状态",
		Retryable:  false,
		UserAction: "请检查输出目录并重新创建下载任务",
		Cause:      err.Error(),
	}
	task.mu.Lock()
	task.Status = StatusFailed
	task.Error = taskErr.Message
	task.LastError = taskErr.Message
	task.LastErrorDetail = taskErr
	task.ProgressSummary.CurrentStage = "publish_recovery_failed"
	task.ProgressSummary.StageLabel = "发布恢复失败"
	task.mu.Unlock()
}

func artifactFromDraft(path string, draft downloadtask.TaskArtifactDraft) downloadtask.TaskArtifact {
	return downloadtask.TaskArtifact{
		ID:        draft.ID,
		Kind:      downloadtask.TaskArtifactFinal,
		Path:      path,
		FileName:  filepath.Base(path),
		MediaType: draft.MediaType,
		Size:      draft.Size,
		Primary:   draft.Primary,
		CreatedAt: time.Now().Unix(),
		Metadata:  cloneStringMapLocal(draft.Metadata),
	}
}

func matchesArtifactDraft(path string, draft downloadtask.TaskArtifactDraft) bool {
	expectedHash, err := normalizeSHA256(draft.SHA256)
	if err != nil || draft.Size <= 0 {
		return false
	}
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return false
	}
	if info.Size() != draft.Size {
		return false
	}
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return false
	}
	return strings.EqualFold(hex.EncodeToString(hash.Sum(nil)), expectedHash)
}

func cloneTaskError(err *downloadtask.TaskError) *downloadtask.TaskError {
	if err == nil {
		return nil
	}
	clone := *err
	clone.Metadata = cloneStringMapLocal(err.Metadata)
	return &clone
}

func cloneStringMapLocal(source map[string]string) map[string]string {
	if source == nil {
		return nil
	}
	clone := make(map[string]string, len(source))
	for key, value := range source {
		clone[key] = value
	}
	return clone
}
