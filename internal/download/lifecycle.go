package downloader

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"

	downloadtask "EasyDownload/internal/download/task"
)

type executionPhase string

const (
	executionRunning    executionPhase = "running"
	executionPublishing executionPhase = "publishing"
	executionStopping   executionPhase = "stopping"
	executionFinished   executionPhase = "finished"
)

type taskExecution struct {
	generation       uint64
	ctx              context.Context
	cancel           context.CancelFunc
	done             chan struct{}
	doneOnce         sync.Once
	phase            executionPhase
	mutationOpen     bool
	stopRequested    bool
	stopReason       downloadtask.StopReason
	operationID      string
	stopRevision     uint64
	terminalizing    bool
	finalizerStarted bool
	cleanupPerformed bool
	cleanupErr       error
	completion       chan StopReceipt
}

func (execution *taskExecution) finish() {
	if execution == nil {
		return
	}
	execution.doneOnce.Do(func() { close(execution.done) })
}

type StopOperationState string

const (
	StopOperationAccepted  StopOperationState = "accepted"
	StopOperationStopping  StopOperationState = "stopping"
	StopOperationCompleted StopOperationState = "completed"
	StopOperationFailed    StopOperationState = "failed"
)

type StopReceipt struct {
	Accepted        bool                    `json:"accepted"`
	OperationID     string                  `json:"operationId"`
	TaskID          string                  `json:"taskId"`
	RequestedReason downloadtask.StopReason `json:"requestedReason"`
	EffectiveReason downloadtask.StopReason `json:"effectiveReason"`
	ExecutionState  string                  `json:"executionState"`
	Revision        uint64                  `json:"revision"`
	TaskInstance    uint64                  `json:"taskInstance"`
	TaskGeneration  uint64                  `json:"taskGeneration"`
	TaskRevision    uint64                  `json:"taskRevision"`
	Error           *downloadtask.TaskError `json:"error,omitempty"`

	completion <-chan StopReceipt
}

type TaskLifecyclePhase string

const (
	TaskLifecycleStopping  TaskLifecyclePhase = "stopping"
	TaskLifecycleCompleted TaskLifecyclePhase = "completed"
	TaskLifecycleFailed    TaskLifecyclePhase = "failed"
)

type StopEvent struct {
	OperationID     string                  `json:"operationId"`
	TaskID          string                  `json:"taskId"`
	Phase           TaskLifecyclePhase      `json:"phase"`
	EffectiveReason downloadtask.StopReason `json:"effectiveReason"`
	ResultStatus    downloadtask.TaskStatus `json:"resultStatus,omitempty"`
	Removed         bool                    `json:"removed,omitempty"`
	Error           *downloadtask.TaskError `json:"error,omitempty"`
	Revision        uint64                  `json:"revision"`
	TaskInstance    uint64                  `json:"taskInstance"`
	TaskGeneration  uint64                  `json:"taskGeneration"`
	TaskRevision    uint64                  `json:"taskRevision"`
	Task            *PublicDownloadTask     `json:"task,omitempty"`
	OccurredAt      int64                   `json:"occurredAt"`
}

type taskEventVersion struct {
	instance   uint64
	generation uint64
	revision   uint64
}

func (dm *DownloadManager) nextTaskEventVersionLocked(task *DownloadTask, generation uint64) taskEventVersion {
	if task.eventInstance == 0 {
		task.eventInstance = dm.taskInstance.Add(1)
	}
	return taskEventVersion{
		instance:   task.eventInstance,
		generation: generation,
		revision:   dm.eventRevision.Add(1),
	}
}

func (version taskEventVersion) stamp(event StopEvent) StopEvent {
	event.TaskInstance = version.instance
	event.TaskGeneration = version.generation
	event.TaskRevision = version.revision
	return event
}

func (version taskEventVersion) stampReceipt(receipt StopReceipt) StopReceipt {
	receipt.TaskInstance = version.instance
	receipt.TaskGeneration = version.generation
	receipt.TaskRevision = version.revision
	return receipt
}

func (dm *DownloadManager) emitTaskStopEvent(task *DownloadTask, event StopEvent) {
	task.mu.Lock()
	generation := task.generationCounter
	if task.execution != nil {
		generation = task.execution.generation
	}
	version := dm.nextTaskEventVersionLocked(task, generation)
	task.mu.Unlock()
	dm.emitStopEvent(version.stamp(event))
}

type ShutdownResult struct {
	Completed       bool     `json:"completed"`
	TimedOutTaskIDs []string `json:"timedOutTaskIds,omitempty"`
	Revision        uint64   `json:"revision"`
}

var stopOperationSequence atomic.Uint64

func newTaskExecutionLocked(task *DownloadTask) *taskExecution {
	task.generationCounter++
	task.queuedGeneration = 0
	return newTaskExecutionWithGenerationLocked(task, task.generationCounter)
}

func newQueuedTaskExecutionLocked(task *DownloadTask) *taskExecution {
	generation := task.queuedGeneration
	if generation == 0 {
		task.generationCounter++
		generation = task.generationCounter
	}
	task.queuedGeneration = 0
	return newTaskExecutionWithGenerationLocked(task, generation)
}

func newTaskExecutionWithGenerationLocked(task *DownloadTask, generation uint64) *taskExecution {
	ctx, cancel := context.WithCancel(context.Background())
	execution := &taskExecution{
		generation:   generation,
		ctx:          ctx,
		cancel:       cancel,
		done:         make(chan struct{}),
		phase:        executionRunning,
		mutationOpen: true,
		completion:   make(chan StopReceipt, 1),
	}
	task.execution = execution
	task.cancel = cancel // legacy compatibility; lifecycle code uses execution.
	return execution
}

func (dm *DownloadManager) SetStopEventCallback(callback func(StopEvent)) {
	dm.lifecycleMu.Lock()
	dm.onStopEvent = callback
	dm.lifecycleMu.Unlock()
}

func (dm *DownloadManager) SetStopTimeouts(waitTimeout, cleanupTimeout time.Duration) {
	dm.lifecycleMu.Lock()
	defer dm.lifecycleMu.Unlock()
	if waitTimeout > 0 {
		dm.stopWaitTimeout = waitTimeout
	}
	if cleanupTimeout > 0 {
		dm.cleanupTimeout = cleanupTimeout
	}
}

func (dm *DownloadManager) RequestPauseTask(id string) (StopReceipt, error) {
	return dm.requestStopExpected(id, downloadtask.StopReasonPause, 0, 0)
}

func (dm *DownloadManager) RequestPauseTaskExpected(id string, instance, generation uint64) (StopReceipt, error) {
	return dm.requestStopExpected(id, downloadtask.StopReasonPause, instance, generation)
}

func (dm *DownloadManager) RequestCancelTask(id string) (StopReceipt, error) {
	return dm.requestStopExpected(id, downloadtask.StopReasonCancel, 0, 0)
}

func (dm *DownloadManager) RequestCancelTaskExpected(id string, instance, generation uint64) (StopReceipt, error) {
	return dm.requestStopExpected(id, downloadtask.StopReasonCancel, instance, generation)
}

func (dm *DownloadManager) RequestRemoveTask(id string) (StopReceipt, error) {
	return dm.requestStopExpected(id, downloadtask.StopReasonTaskRemoval, 0, 0)
}

func (dm *DownloadManager) RequestRemoveTaskExpected(id string, instance, generation uint64) (StopReceipt, error) {
	return dm.requestStopExpected(id, downloadtask.StopReasonTaskRemoval, instance, generation)
}

func currentTaskGenerationLocked(task *DownloadTask) uint64 {
	if task.execution != nil {
		return task.execution.generation
	}
	return task.generationCounter
}

func validateExpectedTaskLocked(task *DownloadTask, expectedInstance, expectedGeneration uint64) error {
	if expectedInstance == 0 {
		return nil
	}
	currentGeneration := currentTaskGenerationLocked(task)
	if task.eventInstance == expectedInstance && currentGeneration == expectedGeneration {
		return nil
	}
	return &downloadtask.TaskError{
		Code:       "task.stale_reference",
		Category:   downloadtask.TaskErrorCategoryCanceled,
		Message:    "下载任务已发生变化",
		Retryable:  false,
		UserAction: "请刷新下载列表后重试",
		Cause: fmt.Sprintf(
			"expected instance/generation %d/%d, current %d/%d",
			expectedInstance, expectedGeneration, task.eventInstance, currentGeneration,
		),
	}
}

func (dm *DownloadManager) requestStop(id string, reason downloadtask.StopReason) (StopReceipt, error) {
	return dm.requestStopExpected(id, reason, 0, 0)
}

func (dm *DownloadManager) requestStopExpected(id string, reason downloadtask.StopReason, expectedInstance, expectedGeneration uint64) (StopReceipt, error) {
	dm.tasksMu.RLock()
	task, exists := dm.tasks[id]
	if !exists {
		dm.tasksMu.RUnlock()
		return StopReceipt{}, fmt.Errorf("task %s not found", id)
	}

	// Reject no-op/invalid requests before touching the pending queue. In
	// particular, Pause(pending) must not silently dequeue a task and leave it
	// stranded forever after returning an error.
	task.mu.RLock()
	if err := validateExpectedTaskLocked(task, expectedInstance, expectedGeneration); err != nil {
		task.mu.RUnlock()
		dm.tasksMu.RUnlock()
		return StopReceipt{}, err
	}
	precheckStatus := task.Status
	precheckExecution := task.execution
	precheckRetryableError := task.LastErrorDetail != nil && task.LastErrorDetail.Retryable
	task.mu.RUnlock()
	if reason == downloadtask.StopReasonPause && precheckStatus != StatusDownloading {
		dm.tasksMu.RUnlock()
		return StopReceipt{}, fmt.Errorf("task %s is not running (status=%s)", id, precheckStatus)
	}
	if reason == downloadtask.StopReasonCancel && precheckStatus == StatusCompleted {
		dm.tasksMu.RUnlock()
		return StopReceipt{}, fmt.Errorf("task %s is completed", id)
	}
	if reason == downloadtask.StopReasonCancel && precheckStatus == StatusCancelled && precheckExecution == nil &&
		!precheckRetryableError {
		task.mu.Lock()
		version := dm.nextTaskEventVersionLocked(task, task.generationCounter)
		task.mu.Unlock()
		dm.tasksMu.RUnlock()
		return version.stampReceipt(StopReceipt{
			Accepted: false, TaskID: id, RequestedReason: reason, EffectiveReason: reason,
			ExecutionState: string(StopOperationCompleted), Revision: dm.revision.Load(),
		}), nil
	}

	dm.activeTasksMu.Lock()
	task.mu.Lock()
	if err := validateExpectedTaskLocked(task, expectedInstance, expectedGeneration); err != nil {
		task.mu.Unlock()
		dm.activeTasksMu.Unlock()
		dm.tasksMu.RUnlock()
		return StopReceipt{}, err
	}
	if reason == downloadtask.StopReasonPause && task.Status != StatusDownloading {
		status := task.Status
		task.mu.Unlock()
		dm.activeTasksMu.Unlock()
		dm.tasksMu.RUnlock()
		return StopReceipt{}, fmt.Errorf("task %s is not running (status=%s)", id, status)
	}
	if reason == downloadtask.StopReasonCancel && task.Status == StatusCompleted {
		task.mu.Unlock()
		dm.activeTasksMu.Unlock()
		dm.tasksMu.RUnlock()
		return StopReceipt{}, fmt.Errorf("task %s is completed", id)
	}
	if reason == downloadtask.StopReasonCancel && task.Status == StatusCancelled && task.execution == nil &&
		(task.LastErrorDetail == nil || !task.LastErrorDetail.Retryable) {
		version := dm.nextTaskEventVersionLocked(task, task.generationCounter)
		receipt := version.stampReceipt(StopReceipt{
			Accepted:        false,
			TaskID:          id,
			RequestedReason: reason,
			EffectiveReason: reason,
			ExecutionState:  string(StopOperationCompleted),
			Revision:        dm.revision.Load(),
		})
		task.mu.Unlock()
		dm.activeTasksMu.Unlock()
		dm.tasksMu.RUnlock()
		return receipt, nil
	}
	dm.removeQueuedTaskInstanceLocked(task)

	execution := task.execution
	mutated := false
	if execution == nil {
		generation := task.queuedGeneration
		if generation == 0 {
			task.generationCounter++
			generation = task.generationCounter
		}
		task.queuedGeneration = 0
		execution = &taskExecution{
			generation:    generation,
			ctx:           context.Background(),
			cancel:        func() {},
			done:          make(chan struct{}),
			phase:         executionStopping,
			mutationOpen:  false,
			stopRequested: true,
			stopReason:    reason,
			completion:    make(chan StopReceipt, 1),
		}
		execution.finish()
		task.execution = execution
		mutated = true
	} else {
		if !execution.stopRequested {
			mutated = true
		}
		if stopReasonPriority(reason) > stopReasonPriority(execution.stopReason) {
			execution.stopReason = reason
			mutated = true
		}
		execution.stopRequested = true
		execution.mutationOpen = false
		if execution.phase != executionPublishing {
			execution.phase = executionStopping
			execution.cancel()
		}
	}
	if execution.operationID == "" {
		execution.operationID = fmt.Sprintf("%s:%d:%d", id, execution.generation, stopOperationSequence.Add(1))
		mutated = true
	}
	if execution.stopReason == "" {
		execution.stopReason = reason
		mutated = true
	}
	if mutated {
		execution.stopRevision = dm.revision.Add(1)
	}
	taskVersion := dm.nextTaskEventVersionLocked(task, execution.generation)
	receipt := taskVersion.stampReceipt(StopReceipt{
		Accepted:        true,
		OperationID:     execution.operationID,
		TaskID:          id,
		RequestedReason: reason,
		EffectiveReason: execution.stopReason,
		ExecutionState:  string(executionStopping),
		Revision:        execution.stopRevision,
		completion:      execution.completion,
	})
	startFinalizer := !execution.finalizerStarted
	execution.finalizerStarted = true
	task.ProgressSummary.CurrentStage = "stopping"
	task.ProgressSummary.StageLabel = "正在停止"
	task.mu.Unlock()
	dm.activeTasksMu.Unlock()
	dm.tasksMu.RUnlock()

	if mutated {
		dm.emitStopEvent(taskVersion.stamp(StopEvent{
			OperationID:     receipt.OperationID,
			TaskID:          receipt.TaskID,
			Phase:           TaskLifecycleStopping,
			EffectiveReason: receipt.EffectiveReason,
			Revision:        receipt.Revision,
		}))
	}
	if startFinalizer {
		go dm.finalizeStop(task, execution)
	}
	return receipt, nil
}

func (dm *DownloadManager) finalizeStop(task *DownloadTask, execution *taskExecution) {
	if task == nil || execution == nil {
		return
	}
	waitTimer := time.NewTimer(dm.stopWaitTimeoutValue())
	select {
	case <-execution.done:
		if !waitTimer.Stop() {
			<-waitTimer.C
		}
	case <-waitTimer.C:
		task.mu.Lock()
		if task.execution != execution {
			task.mu.Unlock()
			return
		}
		reason := execution.stopReason
		revision := dm.revision.Add(1)
		execution.stopRevision = revision
		task.mu.Unlock()
		diagnostic := workerStoppingTaskError()
		dm.emitTaskStopEvent(task, StopEvent{
			OperationID:     execution.operationID,
			TaskID:          task.ID,
			Phase:           TaskLifecycleStopping,
			EffectiveReason: reason,
			Error:           diagnostic,
			Revision:        revision,
		})
		// The diagnostic is non-terminal. Continue waiting without cleaning up,
		// deleting the task, or allowing a new generation to start.
		<-execution.done
	}

	dm.lifecycleMu.Lock()
	beforeCleanup := dm.beforeStopCleanup
	dm.lifecycleMu.Unlock()
	if beforeCleanup != nil {
		beforeCleanup()
	}

	var (
		reason           downloadtask.StopReason
		cleanupErr       = execution.cleanupErr
		cleanupPerformed = execution.cleanupPerformed
		completionWon    bool
		terminalError    *downloadtask.TaskError
		resultStatus     downloadtask.TaskStatus
	)
	for {
		task.mu.Lock()
		if task.execution != execution {
			task.mu.Unlock()
			return
		}
		reason = execution.stopReason
		snapshot := taskSnapshotLocked(task)
		completionWon = snapshot.PublishIntent == nil && hasPrimaryFinalArtifact(snapshot.Artifacts)
		task.mu.Unlock()

		if destructiveStopReason(reason) && !cleanupPerformed &&
			!(completionWon && reason != downloadtask.StopReasonTaskRemoval) {
			cleanupTimeout := dm.cleanupTimeoutValue()
			cleanupCtx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
			cleanupErr = dm.cleanupTask(cleanupCtx, task, snapshot, reason)
			cancel()
			cleanupPerformed = true
			execution.cleanupErr = cleanupErr
			execution.cleanupPerformed = true
			// Re-read the effective reason after cleanup. cancel -> removal can
			// reuse the same destructive cleanup, while pause -> cancel must not
			// commit before cleanup has run.
			continue
		}

		task.mu.Lock()
		if task.execution != execution {
			task.mu.Unlock()
			return
		}
		latestReason := execution.stopReason
		completionWon = task.PublishIntent == nil && hasPrimaryFinalArtifact(task.Artifacts)
		if destructiveStopReason(latestReason) && !cleanupPerformed &&
			!(completionWon && latestReason != downloadtask.StopReasonTaskRemoval) {
			task.mu.Unlock()
			continue
		}
		reason = latestReason
		execution.terminalizing = true
		execution.phase = executionStopping
		execution.mutationOpen = false
		task.Speed = 0
		if cleanupErr != nil {
			terminalError = &downloadtask.TaskError{
				Code:       "task.cleanup_failed",
				Category:   downloadtask.TaskErrorCategoryOutput,
				Message:    "清理下载临时文件失败",
				Retryable:  true,
				UserAction: "请重试取消或删除操作",
				Cause:      cleanupErr.Error(),
			}
			task.Status = StatusCancelled
			task.Error = terminalError.Message
			task.LastError = terminalError.Message
			task.LastErrorDetail = terminalError
			task.ProgressSummary.CurrentStage = "cleanup_failed"
			task.ProgressSummary.StageLabel = "清理失败"
		} else {
			task.Error = ""
			task.LastError = ""
			task.LastErrorDetail = nil
			if completionWon && reason != downloadtask.StopReasonTaskRemoval {
				// A primary no-replace publish is the irreversible completion
				// boundary. If pause/cancel/shutdown races that commit, completion
				// wins; otherwise a resumable paused task would redownload an
				// already-published file under a new name.
				task.Status = StatusCompleted
				task.ProgressSummary.Percent = 100
				task.ProgressSummary.CurrentStage = "completed"
				task.ProgressSummary.StageLabel = "已完成"
				if task.CompletedAt == 0 {
					task.CompletedAt = time.Now().Unix()
				}
			} else {
				switch reason {
				case downloadtask.StopReasonPause, downloadtask.StopReasonShutdown:
					task.Status = StatusPaused
					task.ProgressSummary.CurrentStage = "paused"
					task.ProgressSummary.StageLabel = "已暂停"
				case downloadtask.StopReasonCancel, downloadtask.StopReasonTaskRemoval:
					task.Status = StatusCancelled
					task.ProgressSummary.CurrentStage = "canceled"
					task.ProgressSummary.StageLabel = "已取消"
				}
			}
		}
		execution.stopRevision = dm.revision.Add(1)
		resultStatus = downloadtask.TaskStatus(task.Status)
		task.mu.Unlock()
		break
	}

	removing := cleanupErr == nil && reason == downloadtask.StopReasonTaskRemoval
	var persistenceErr error
	removed := false
	reservationReleased := false
	if removing {
		// Durable exclusion and in-memory deletion share one persistence
		// transaction with reservation release. No concurrent SaveState or
		// same-ID CreateTask can reintroduce the task or suffer an allocator ABA
		// between these operations.
		dm.persistenceMu.Lock()
		dm.tasksMu.RLock()
		currentTask, currentExists := dm.tasks[task.ID]
		ownsRegisteredIdentity := currentExists && currentTask == task
		dm.tasksMu.RUnlock()
		if !ownsRegisteredIdentity {
			// This operation belongs to an object that another completed removal
			// already retired. A same-ID replacement is a different identity: do
			// not exclude it from persistence or release its reservation.
			removed = true
		} else {
			if dm.statePath != "" {
				persistenceErr = dm.saveStateSnapshotLocked(task.ID)
			}
		}
		if ownsRegisteredIdentity && persistenceErr == nil {
			dm.tasksMu.Lock()
			if dm.tasks[task.ID] == task {
				delete(dm.tasks, task.ID)
				removed = true
			}
			dm.tasksMu.Unlock()
			dm.outputAllocator.Release(task.ID)
			reservationReleased = true
		}
		dm.persistenceMu.Unlock()
	} else {
		if dm.statePath != "" {
			persistenceErr = dm.SaveState()
		}
	}
	if persistenceErr != nil {
		removing = false
		terminalError = persistenceTaskError("persist stop terminal", persistenceErr)
		task.mu.Lock()
		if task.execution == execution {
			task.Error = terminalError.Message
			task.LastError = terminalError.Message
			task.LastErrorDetail = terminalError
			task.ProgressSummary.CurrentStage = "persistence_failed"
			task.ProgressSummary.StageLabel = "状态保存失败"
			execution.stopRevision = dm.revision.Add(1)
			resultStatus = downloadtask.TaskStatus(task.Status)
		}
		task.mu.Unlock()
	}

	var (
		terminalTaskVersion taskEventVersion
		terminalPublicTask  PublicDownloadTask
	)
	task.mu.Lock()
	if task.execution != execution {
		task.mu.Unlock()
		return
	}
	if task.execution == execution && persistenceErr == nil &&
		stopReasonPriority(execution.stopReason) > stopReasonPriority(reason) {
		// A higher-priority request arrived while terminal state persistence was
		// in flight. Re-enter finalization on the same operation; destructive
		// cleanup state is retained on execution so it still runs at most once.
		execution.terminalizing = false
		task.mu.Unlock()
		dm.finalizeStop(task, execution)
		return
	}
	// Reserve the terminal event version while the finalized generation is
	// still current. Resume/Retry may start immediately after this lock is
	// released, but the delayed terminal event must remain bound to the old
	// execution rather than being restamped as the new one.
	execution.phase = executionFinished
	execution.terminalizing = false
	execution.mutationOpen = false
	terminalTaskVersion = dm.nextTaskEventVersionLocked(task, execution.generation)
	terminalPublicTask = task.publicSnapshotLocked()
	terminalPublicTask.Instance = terminalTaskVersion.instance
	terminalPublicTask.Generation = terminalTaskVersion.generation
	terminalPublicTask.Revision = terminalTaskVersion.revision
	task.execution = nil
	task.cancel = nil
	revision := dm.revision.Load()
	if revision < execution.stopRevision {
		revision = execution.stopRevision
	}
	task.mu.Unlock()

	dm.tasksMu.RLock()
	reservationOwnerStillCurrent := dm.tasks[task.ID] == task
	dm.tasksMu.RUnlock()
	if cleanupErr == nil && persistenceErr == nil && !reservationReleased && reservationOwnerStillCurrent &&
		(destructiveStopReason(reason) || completionWon) {
		dm.outputAllocator.Release(task.ID)
	}

	terminalState := StopOperationCompleted
	phase := TaskLifecycleCompleted
	if cleanupErr != nil || persistenceErr != nil {
		terminalState = StopOperationFailed
		phase = TaskLifecycleFailed
	}
	publicError := publicTaskError(terminalError)
	receipt := terminalTaskVersion.stampReceipt(StopReceipt{
		Accepted:        true,
		OperationID:     execution.operationID,
		TaskID:          task.ID,
		RequestedReason: reason,
		EffectiveReason: reason,
		ExecutionState:  string(terminalState),
		Revision:        revision,
		Error:           publicError,
	})
	dm.emitStopEvent(terminalTaskVersion.stamp(StopEvent{
		OperationID:     execution.operationID,
		TaskID:          task.ID,
		Phase:           phase,
		EffectiveReason: reason,
		ResultStatus:    resultStatus,
		Removed:         removed,
		Error:           publicError,
		Revision:        revision,
		Task:            &terminalPublicTask,
	}))
	execution.completion <- receipt
	close(execution.completion)
}

func workerStoppingTaskError() *downloadtask.TaskError {
	return &downloadtask.TaskError{
		Code:       "task.worker_stopping",
		Category:   downloadtask.TaskErrorCategoryCanceled,
		Message:    "下载任务仍在停止，请稍后重试",
		Retryable:  true,
		UserAction: "请等待当前任务完全停止",
	}
}

func destructiveStopReason(reason downloadtask.StopReason) bool {
	return reason == downloadtask.StopReasonCancel || reason == downloadtask.StopReasonTaskRemoval
}

func (dm *DownloadManager) cleanupTask(ctx context.Context, task *DownloadTask, snapshot downloadtask.TaskSnapshot, reason downloadtask.StopReason) error {
	var errs []error
	if adapter, ok := dm.adapterForSnapshot(snapshot); ok {
		if err := adapter.CleanupTask(ctx, downloadtask.CloneSnapshot(snapshot), reason); err != nil {
			errs = append(errs, err)
		}
	}
	if snapshot.PublishIntent != nil && snapshot.PublishIntent.TemporaryPath != "" {
		if err := os.Remove(snapshot.PublishIntent.TemporaryPath); err != nil && !os.IsNotExist(err) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func stopReasonPriority(reason downloadtask.StopReason) int {
	switch reason {
	case downloadtask.StopReasonTaskRemoval:
		return 5
	case downloadtask.StopReasonCancel:
		return 4
	case downloadtask.StopReasonPause:
		return 3
	case downloadtask.StopReasonShutdown:
		return 2
	case downloadtask.StopReasonFailure:
		return 1
	default:
		return 0
	}
}

func (dm *DownloadManager) emitStopEvent(event StopEvent) {
	dm.lifecycleMu.Lock()
	callback := dm.onStopEvent
	dm.lifecycleMu.Unlock()
	if callback != nil {
		event.OccurredAt = time.Now().UnixMilli()
		callback(event)
	}
}

func (dm *DownloadManager) stopWaitTimeoutValue() time.Duration {
	dm.lifecycleMu.Lock()
	defer dm.lifecycleMu.Unlock()
	if dm.stopWaitTimeout <= 0 {
		return 2 * time.Second
	}
	return dm.stopWaitTimeout
}

func (dm *DownloadManager) cleanupTimeoutValue() time.Duration {
	dm.lifecycleMu.Lock()
	defer dm.lifecycleMu.Unlock()
	if dm.cleanupTimeout <= 0 {
		return 30 * time.Second
	}
	return dm.cleanupTimeout
}

// Shutdown registers a shutdown stop for every active or queued task and waits
// only until the caller's global context deadline. A timed-out worker remains
// in stopping state; its coordinator continues in the background and cleanup
// still cannot run before that worker exits.
func (dm *DownloadManager) Shutdown(ctx context.Context) ShutdownResult {
	if ctx == nil {
		ctx = context.Background()
	}
	dm.tasksMu.RLock()
	tasks := make([]*DownloadTask, 0, len(dm.tasks))
	for _, task := range dm.tasks {
		tasks = append(tasks, task)
	}
	dm.tasksMu.RUnlock()

	receipts := make([]StopReceipt, 0, len(tasks))
	for _, task := range tasks {
		task.mu.RLock()
		active := task.execution != nil || task.Status == StatusDownloading || task.Status == StatusPending
		id := task.ID
		task.mu.RUnlock()
		if !active {
			continue
		}
		receipt, err := dm.requestStop(id, downloadtask.StopReasonShutdown)
		if err == nil && receipt.completion != nil {
			receipts = append(receipts, receipt)
		}
	}

	result := ShutdownResult{Completed: true}
	for index, receipt := range receipts {
		select {
		case <-receipt.completion:
		case <-ctx.Done():
			result.Completed = false
			result.TimedOutTaskIDs = append(result.TimedOutTaskIDs, receipt.TaskID)
			for _, remaining := range receipts[index+1:] {
				result.TimedOutTaskIDs = append(result.TimedOutTaskIDs, remaining.TaskID)
			}
			result.Revision = dm.revision.Load()
			return result
		}
	}
	result.Revision = dm.revision.Load()
	return result
}
