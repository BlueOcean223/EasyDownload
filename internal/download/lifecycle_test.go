package downloader

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	downloadtask "EasyDownload/internal/download/task"
)

type lifecycleTestAdapter struct {
	id             downloadtask.PlatformID
	started        chan struct{}
	releases       map[string]<-chan struct{}
	ignoreContext  bool
	cleanupCalls   atomic.Int32
	startedCount   atomic.Int32
	cleanupStarted chan struct{}
	cleanupRelease <-chan struct{}
	cleanupOnce    sync.Once
	startedOnce    sync.Once
}

type finishedWorkerBarrierAdapter struct {
	id           downloadtask.PlatformID
	runCalls     atomic.Int32
	cleanupCalls atomic.Int32
}

func (adapter *finishedWorkerBarrierAdapter) ID() downloadtask.PlatformID          { return adapter.id }
func (*finishedWorkerBarrierAdapter) ValidateTask(downloadtask.TaskSnapshot) error { return nil }
func (adapter *finishedWorkerBarrierAdapter) RunTask(context.Context, downloadtask.TaskSnapshot, downloadtask.TaskExecutionContext) error {
	adapter.runCalls.Add(1)
	return &downloadtask.TaskError{
		Code: "test.retryable", Category: downloadtask.TaskErrorCategoryTransport,
		Message: "injected adapter failure", Retryable: true,
	}
}
func (adapter *finishedWorkerBarrierAdapter) CleanupTask(context.Context, downloadtask.TaskSnapshot, downloadtask.StopReason) error {
	adapter.cleanupCalls.Add(1)
	return nil
}

func (adapter *lifecycleTestAdapter) ID() downloadtask.PlatformID { return adapter.id }
func (adapter *lifecycleTestAdapter) ValidateTask(downloadtask.TaskSnapshot) error {
	return nil
}
func (adapter *lifecycleTestAdapter) RunTask(ctx context.Context, snapshot downloadtask.TaskSnapshot, _ downloadtask.TaskExecutionContext) error {
	adapter.startedCount.Add(1)
	if adapter.started != nil {
		adapter.startedOnce.Do(func() { close(adapter.started) })
	}
	release := adapter.releases[snapshot.ID]
	if release == nil {
		return nil
	}
	if adapter.ignoreContext {
		<-release
		return nil
	}
	select {
	case <-release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (adapter *lifecycleTestAdapter) CleanupTask(context.Context, downloadtask.TaskSnapshot, downloadtask.StopReason) error {
	adapter.cleanupCalls.Add(1)
	if adapter.cleanupStarted != nil {
		adapter.cleanupOnce.Do(func() { close(adapter.cleanupStarted) })
	}
	if adapter.cleanupRelease != nil {
		<-adapter.cleanupRelease
	}
	return nil
}

func TestPrimaryPublishWinsPauseCancelAndShutdownRace(t *testing.T) {
	for _, reason := range []downloadtask.StopReason{
		downloadtask.StopReasonPause,
		downloadtask.StopReasonCancel,
		downloadtask.StopReasonShutdown,
	} {
		t.Run(string(reason), func(t *testing.T) {
			dm := NewDownloadManager(t.TempDir(), 1)
			adapter := &lifecycleTestAdapter{id: downloadtask.PlatformID("publish-race")}
			if err := dm.RegisterPlatformAdapter(adapter); err != nil {
				t.Fatal(err)
			}
			task, err := createStrictTestTask(dm, "task-1", "https://example.com/video.mp4", "Video", adapter.id)
			if err != nil {
				t.Fatal(err)
			}
			finalPath := task.OutputPolicy.PlannedFinalPath
			if err := os.WriteFile(finalPath, []byte("published"), 0o600); err != nil {
				t.Fatal(err)
			}

			task.mu.Lock()
			execution := newTaskExecutionLocked(task)
			task.Status = StatusDownloading
			task.Artifacts = []downloadtask.TaskArtifact{{
				Kind: downloadtask.TaskArtifactFinal, Path: finalPath,
				Primary: true, Size: int64(len("published")), CreatedAt: time.Now().Unix(),
			}}
			execution.finish()
			task.mu.Unlock()
			events := make(chan StopEvent, 4)
			dm.SetStopEventCallback(func(event StopEvent) { events <- event })

			receipt, err := dm.requestStop(task.ID, reason)
			if err != nil {
				t.Fatal(err)
			}
			terminal := <-receipt.completion
			if terminal.ExecutionState != string(StopOperationCompleted) {
				t.Fatalf("terminal receipt=%#v", terminal)
			}
			task.mu.RLock()
			status := task.Status
			stage := task.ProgressSummary.CurrentStage
			task.mu.RUnlock()
			if status != StatusCompleted || stage != "completed" {
				t.Fatalf("publish race ended status=%s stage=%s", status, stage)
			}
			if adapter.cleanupCalls.Load() != 0 {
				t.Fatalf("committed final artifact was cleaned %d times", adapter.cleanupCalls.Load())
			}
			if owner, reserved := dm.outputAllocator.Owner(finalPath); reserved {
				t.Fatalf("completed output reservation retained by %s", owner)
			}
			terminalEvent := waitForLifecycleEvent(t, events, func(event StopEvent) bool {
				return event.Phase == TaskLifecycleCompleted
			})
			if terminalEvent.Task == nil || terminalEvent.Task.Status != StatusCompleted ||
				terminalEvent.Task.ProgressSummary.Percent != 100 ||
				len(terminalEvent.Task.Artifacts) != 1 || terminalEvent.Task.Artifacts[0].Path != finalPath {
				t.Fatalf("terminal event omitted authoritative completion snapshot: %#v", terminalEvent)
			}
		})
	}
}

func TestStopReceiptsAndEventsUseMonotonicRevision(t *testing.T) {
	dm := NewDownloadManager(t.TempDir(), 1)
	started := make(chan struct{})
	release := make(chan struct{})
	adapter := &lifecycleTestAdapter{
		id:            "lifecycle-revision",
		started:       started,
		releases:      map[string]<-chan struct{}{"task-1": release},
		ignoreContext: true,
	}
	if err := dm.RegisterPlatformAdapter(adapter); err != nil {
		t.Fatal(err)
	}
	events := make(chan StopEvent, 16)
	dm.SetStopEventCallback(func(event StopEvent) { events <- event })
	if _, err := createStrictTestTask(dm, "task-1", "https://example.com/video.mp4", "Video", adapter.id); err != nil {
		t.Fatal(err)
	}
	if err := dm.StartTask("task-1"); err != nil {
		t.Fatal(err)
	}
	<-started

	pause, err := dm.RequestPauseTask("task-1")
	if err != nil {
		t.Fatal(err)
	}
	if !pause.Accepted || pause.Revision == 0 || pause.EffectiveReason != downloadtask.StopReasonPause {
		t.Fatalf("unexpected pause receipt: %#v", pause)
	}
	repeated, err := dm.RequestPauseTask("task-1")
	if err != nil {
		t.Fatal(err)
	}
	if repeated.OperationID != pause.OperationID || repeated.Revision != pause.Revision {
		t.Fatalf("idempotent stop changed operation/revision: first=%#v repeated=%#v", pause, repeated)
	}
	cancel, err := dm.RequestCancelTask("task-1")
	if err != nil {
		t.Fatal(err)
	}
	if cancel.OperationID != pause.OperationID || cancel.EffectiveReason != downloadtask.StopReasonCancel || cancel.Revision <= pause.Revision {
		t.Fatalf("reason upgrade did not advance same operation: pause=%#v cancel=%#v", pause, cancel)
	}

	serialized, err := json.Marshal(cancel)
	if err != nil {
		t.Fatal(err)
	}
	var public map[string]interface{}
	if err := json.Unmarshal(serialized, &public); err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"accepted", "requestedReason", "effectiveReason", "executionState", "revision"} {
		if _, exists := public[required]; !exists {
			t.Errorf("stop receipt lacks %q: %s", required, serialized)
		}
	}
	for _, compatibilityOnly := range []string{"generation", "reason", "state"} {
		if _, exists := public[compatibilityOnly]; exists {
			t.Errorf("stop receipt exposes compatibility field %q: %s", compatibilityOnly, serialized)
		}
	}

	initialEvent := waitForLifecycleEvent(t, events, func(event StopEvent) bool {
		return event.Revision == pause.Revision
	})
	upgradeEvent := waitForLifecycleEvent(t, events, func(event StopEvent) bool {
		return event.Revision == cancel.Revision
	})
	if upgradeEvent.Revision <= initialEvent.Revision {
		t.Fatalf("upgrade event revision did not advance: initial=%d upgrade=%d", initialEvent.Revision, upgradeEvent.Revision)
	}

	close(release)
	terminal := waitForLifecycleEvent(t, events, func(event StopEvent) bool {
		return event.Phase == TaskLifecycleCompleted
	})
	if terminal.Revision <= cancel.Revision || terminal.ResultStatus != downloadtask.StatusCanceled {
		t.Fatalf("unexpected terminal lifecycle event: %#v", terminal)
	}

	// A UI reducer that accepts only a strictly newer manager revision converges
	// even when the terminal event arrives before an older event/receipt.
	currentRevision := uint64(0)
	currentPhase := TaskLifecyclePhase("")
	apply := func(revision uint64, phase TaskLifecyclePhase) {
		if revision <= currentRevision {
			return
		}
		currentRevision = revision
		currentPhase = phase
	}
	apply(terminal.Revision, terminal.Phase)
	apply(initialEvent.Revision, initialEvent.Phase)
	apply(pause.Revision, TaskLifecycleStopping)
	if currentRevision != terminal.Revision || currentPhase != TaskLifecycleCompleted {
		t.Fatalf("stale lifecycle input regressed state: revision=%d phase=%s", currentRevision, currentPhase)
	}
}

func TestTerminalEventKeepsFinalizedGenerationWhenResumeStartsBeforeDelivery(t *testing.T) {
	dm := NewDownloadManager(t.TempDir(), 1)
	started := make(chan struct{})
	secondRunRelease := make(chan struct{})
	adapter := &lifecycleTestAdapter{
		id:       "terminal-event-generation",
		started:  started,
		releases: map[string]<-chan struct{}{"task-1": secondRunRelease},
	}
	if err := dm.RegisterPlatformAdapter(adapter); err != nil {
		t.Fatal(err)
	}

	terminalEntered := make(chan StopEvent, 1)
	releaseTerminalEvent := make(chan struct{})
	dm.SetStopEventCallback(func(event StopEvent) {
		if event.Phase == TaskLifecycleCompleted {
			terminalEntered <- event
			<-releaseTerminalEvent
		}
	})
	task, err := createStrictTestTask(dm, "task-1", "https://example.com/video.mp4", "Video", adapter.id)
	if err != nil {
		t.Fatal(err)
	}
	if err := dm.StartTask(task.ID); err != nil {
		t.Fatal(err)
	}
	waitForSignal(t, started, "first execution")

	pause, err := dm.RequestPauseTask(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	var terminal StopEvent
	select {
	case terminal = <-terminalEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("terminal event callback was not reached")
	}
	if terminal.TaskGeneration != 1 {
		t.Fatalf("finalized event generation=%d, want 1", terminal.TaskGeneration)
	}

	// The callback is still blocked, but finalization already detached g1. A
	// resume may start g2; the captured event must not be restamped from it.
	if err := dm.ResumeTask(task.ID); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for adapter.startedCount.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if adapter.startedCount.Load() != 2 {
		t.Fatal("second execution did not start while terminal delivery was blocked")
	}
	current := dm.PublicTaskSnapshot(task)
	if current.Generation != 2 || current.Status != StatusDownloading {
		t.Fatalf("current snapshot=%#v, want running generation 2", current)
	}
	if terminal.TaskInstance != current.Instance || terminal.TaskGeneration >= current.Generation {
		t.Fatalf("terminal fence=%d/%d current=%d/%d", terminal.TaskInstance, terminal.TaskGeneration, current.Instance, current.Generation)
	}

	close(releaseTerminalEvent)
	select {
	case completed := <-pause.completion:
		if completed.TaskGeneration != 1 {
			t.Fatalf("terminal receipt generation=%d, want 1", completed.TaskGeneration)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("terminal receipt did not complete")
	}
	close(secondRunRelease)
}

func TestPublicTaskListPinsMembershipUntilProjectionIsVersioned(t *testing.T) {
	dm := NewDownloadManager(t.TempDir(), 1)
	adapter := &lifecycleTestAdapter{id: "public-list-linearization"}
	if err := dm.RegisterPlatformAdapter(adapter); err != nil {
		t.Fatal(err)
	}
	task, err := createStrictTestTask(dm, "task-1", "https://example.com/video.mp4", "Video", adapter.id)
	if err != nil {
		t.Fatal(err)
	}
	events := make(chan StopEvent, 8)
	dm.SetStopEventCallback(func(event StopEvent) { events <- event })

	// Block projection after GetPublicTaskSnapshots has taken the membership
	// read lock. A removal may register concurrently, but cannot delete the map
	// entry or reserve its terminal fence until this captured member is stamped.
	task.mu.Lock()
	publicResult := make(chan []PublicDownloadTask, 1)
	go func() { publicResult <- dm.GetPublicTaskSnapshots() }()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if !dm.tasksMu.TryLock() {
			break
		}
		dm.tasksMu.Unlock()
		if time.Now().After(deadline) {
			task.mu.Unlock()
			t.Fatal("public list did not retain the membership lock during projection")
		}
		time.Sleep(time.Millisecond)
	}
	removeResult := make(chan StopReceipt, 1)
	removeError := make(chan error, 1)
	go func() {
		receipt, requestErr := dm.RequestRemoveTask(task.ID)
		if requestErr != nil {
			removeError <- requestErr
			return
		}
		removeResult <- receipt
	}()
	task.mu.Unlock()

	var snapshots []PublicDownloadTask
	select {
	case snapshots = <-publicResult:
	case <-time.After(2 * time.Second):
		t.Fatal("public list projection did not finish")
	}
	if len(snapshots) != 1 || snapshots[0].ID != task.ID {
		t.Fatalf("captured public list=%#v, want old task", snapshots)
	}
	var receipt StopReceipt
	select {
	case err := <-removeError:
		t.Fatal(err)
	case receipt = <-removeResult:
	case <-time.After(2 * time.Second):
		t.Fatal("remove request did not return")
	}
	terminal := waitForLifecycleEvent(t, events, func(event StopEvent) bool {
		return event.OperationID == receipt.OperationID && event.Phase == TaskLifecycleCompleted
	})
	if terminal.TaskInstance != snapshots[0].Instance || terminal.TaskRevision <= snapshots[0].Revision {
		t.Fatalf("terminal fence=%d/%d did not follow captured member=%d/%d", terminal.TaskInstance, terminal.TaskRevision, snapshots[0].Instance, snapshots[0].Revision)
	}
}

func TestStopTimeoutEmitsDiagnosticWithoutEarlyCleanup(t *testing.T) {
	dm := NewDownloadManager(t.TempDir(), 1)
	dm.SetStopTimeouts(20*time.Millisecond, time.Second)
	started := make(chan struct{})
	release := make(chan struct{})
	adapter := &lifecycleTestAdapter{
		id:            "ignore-cancel",
		started:       started,
		releases:      map[string]<-chan struct{}{"task-1": release},
		ignoreContext: true,
	}
	if err := dm.RegisterPlatformAdapter(adapter); err != nil {
		t.Fatal(err)
	}
	events := make(chan StopEvent, 16)
	dm.SetStopEventCallback(func(event StopEvent) { events <- event })
	if _, err := createStrictTestTask(dm, "task-1", "https://example.com/video.mp4", "Video", adapter.id); err != nil {
		t.Fatal(err)
	}
	if err := dm.StartTask("task-1"); err != nil {
		t.Fatal(err)
	}
	<-started
	receipt, err := dm.RequestCancelTask("task-1")
	if err != nil {
		t.Fatal(err)
	}
	diagnostic := waitForLifecycleEvent(t, events, func(event StopEvent) bool {
		return event.Error != nil && event.Error.Code == "task.worker_stopping"
	})
	if diagnostic.Phase != TaskLifecycleStopping || diagnostic.Revision <= receipt.Revision {
		t.Fatalf("unexpected stopping diagnostic: %#v", diagnostic)
	}
	if adapter.cleanupCalls.Load() != 0 {
		t.Fatal("cleanup ran before the worker exited")
	}
	if err := dm.StartTask("task-1"); err == nil {
		t.Fatal("a new generation started while the previous worker was stopping")
	}

	close(release)
	terminal := waitForLifecycleEvent(t, events, func(event StopEvent) bool {
		return event.Phase == TaskLifecycleCompleted
	})
	if terminal.Revision <= diagnostic.Revision {
		t.Fatalf("terminal revision=%d, want > diagnostic=%d", terminal.Revision, diagnostic.Revision)
	}
	if adapter.cleanupCalls.Load() != 1 {
		t.Fatalf("cleanup calls=%d, want exactly 1", adapter.cleanupCalls.Load())
	}
}

func TestStopReasonUpgradeAfterWorkerExitStillRunsDestructiveCleanup(t *testing.T) {
	dm := NewDownloadManager(t.TempDir(), 1)
	started := make(chan struct{})
	workerRelease := make(chan struct{})
	adapter := &lifecycleTestAdapter{
		id:            "upgrade-after-done",
		started:       started,
		releases:      map[string]<-chan struct{}{"task-1": workerRelease},
		ignoreContext: true,
	}
	if err := dm.RegisterPlatformAdapter(adapter); err != nil {
		t.Fatal(err)
	}
	events := make(chan StopEvent, 16)
	dm.SetStopEventCallback(func(event StopEvent) { events <- event })
	beforeCleanup := make(chan struct{})
	continueCleanup := make(chan struct{})
	dm.beforeStopCleanup = func() {
		close(beforeCleanup)
		<-continueCleanup
	}
	if _, err := createStrictTestTask(dm, "task-1", "https://example.com/video.mp4", "Video", adapter.id); err != nil {
		t.Fatal(err)
	}
	if err := dm.StartTask("task-1"); err != nil {
		t.Fatal(err)
	}
	<-started
	pause, err := dm.RequestPauseTask("task-1")
	if err != nil {
		t.Fatal(err)
	}
	close(workerRelease)
	<-beforeCleanup
	cancel, err := dm.RequestCancelTask("task-1")
	if err != nil {
		t.Fatal(err)
	}
	if cancel.OperationID != pause.OperationID || cancel.Revision <= pause.Revision {
		t.Fatalf("upgrade did not retain operation and advance revision: pause=%#v cancel=%#v", pause, cancel)
	}
	close(continueCleanup)
	terminal := waitForLifecycleEvent(t, events, func(event StopEvent) bool {
		return event.Phase == TaskLifecycleCompleted
	})
	if terminal.EffectiveReason != downloadtask.StopReasonCancel || terminal.ResultStatus != downloadtask.StatusCanceled {
		t.Fatalf("terminal used stale pause reason: %#v", terminal)
	}
	if adapter.cleanupCalls.Load() != 1 {
		t.Fatalf("destructive cleanup calls=%d, want 1", adapter.cleanupCalls.Load())
	}
}

func TestStopReasonUpgradeDuringTerminalPersistenceRemovesWithSingleCleanup(t *testing.T) {
	directory := t.TempDir()
	dm := NewDownloadManager(directory, 1)
	adapter := &lifecycleTestAdapter{id: "upgrade-during-terminal-persistence"}
	if err := dm.RegisterPlatformAdapter(adapter); err != nil {
		t.Fatal(err)
	}
	dm.SetStatePath(filepath.Join(directory, "downloads.v2.json"))
	events := make(chan StopEvent, 16)
	dm.SetStopEventCallback(func(event StopEvent) { events <- event })
	task, err := createStrictTestTask(dm, "task-1", "https://example.com/video.mp4", "Video", adapter.id)
	if err != nil {
		t.Fatal(err)
	}

	realWrite := dm.taskStore.writeFile
	terminalWriteEntered := make(chan struct{})
	releaseTerminalWrite := make(chan struct{})
	var releaseOnce sync.Once
	var blocked atomic.Bool
	t.Cleanup(func() {
		dm.taskStore.writeFile = realWrite
		releaseOnce.Do(func() { close(releaseTerminalWrite) })
	})
	dm.taskStore.writeFile = func(ctx context.Context, destination string, data []byte) error {
		if filepath.Clean(destination) == filepath.Clean(dm.StatePath()) {
			var envelope TaskStoreEnvelope
			if err := json.Unmarshal(data, &envelope); err != nil {
				return err
			}
			for _, snapshot := range envelope.Tasks {
				if snapshot.ID == task.ID && snapshot.Status == downloadtask.StatusCanceled && blocked.CompareAndSwap(false, true) {
					close(terminalWriteEntered)
					<-releaseTerminalWrite
					break
				}
			}
		}
		return realWrite(ctx, destination, data)
	}

	cancelReceipt, err := dm.RequestCancelTask(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	waitForSignal(t, terminalWriteEntered, "cancel terminal persistence")
	removeReceipt, err := dm.RequestRemoveTask(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if removeReceipt.OperationID != cancelReceipt.OperationID || removeReceipt.completion != cancelReceipt.completion {
		t.Fatalf("reason upgrade did not reuse operation: cancel=%#v remove=%#v", cancelReceipt, removeReceipt)
	}
	if removeReceipt.EffectiveReason != downloadtask.StopReasonTaskRemoval || removeReceipt.Revision <= cancelReceipt.Revision {
		t.Fatalf("reason upgrade did not advance to task removal: cancel=%#v remove=%#v", cancelReceipt, removeReceipt)
	}

	releaseOnce.Do(func() { close(releaseTerminalWrite) })
	select {
	case terminalReceipt := <-cancelReceipt.completion:
		if terminalReceipt.ExecutionState != string(StopOperationCompleted) || terminalReceipt.EffectiveReason != downloadtask.StopReasonTaskRemoval {
			t.Fatalf("unexpected terminal receipt: %#v", terminalReceipt)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for upgraded stop operation")
	}
	terminalEvent := waitForLifecycleEvent(t, events, func(event StopEvent) bool {
		return event.Phase == TaskLifecycleCompleted
	})
	if terminalEvent.OperationID != cancelReceipt.OperationID || terminalEvent.EffectiveReason != downloadtask.StopReasonTaskRemoval || !terminalEvent.Removed {
		t.Fatalf("unexpected upgraded terminal event: %#v", terminalEvent)
	}
	if adapter.cleanupCalls.Load() != 1 {
		t.Fatalf("cleanup calls=%d, want exactly 1", adapter.cleanupCalls.Load())
	}
	assertTaskFullyAbsent(t, dm, task.ID, task.OutputPolicy.PlannedFinalPath)
	assertDurableTaskIDs(t, dm.StatePath())
}

func TestConcurrentRemovalRequestCannotCrossSameIDReplacement(t *testing.T) {
	directory := t.TempDir()
	dm := NewDownloadManager(directory, 1)
	adapter := &lifecycleTestAdapter{id: "same-id-stop-identity"}
	if err := dm.RegisterPlatformAdapter(adapter); err != nil {
		t.Fatal(err)
	}
	dm.SetStatePath(filepath.Join(directory, "downloads.v2.json"))
	oldTask, err := createStrictTestTask(dm, "reused-id", "https://example.com/old.mp4", "Video", adapter.id)
	if err != nil {
		t.Fatal(err)
	}

	cleanupBarrierEntered := make(chan struct{})
	releaseCleanupBarrier := make(chan struct{})
	var barrierOnce sync.Once
	dm.beforeStopCleanup = func() {
		barrierOnce.Do(func() { close(cleanupBarrierEntered) })
		<-releaseCleanupBarrier
	}
	first, err := dm.RequestRemoveTask(oldTask.ID)
	if err != nil {
		t.Fatal(err)
	}
	waitForSignal(t, cleanupBarrierEntered, "first removal cleanup barrier")

	// Hold the queue lock so the second request pauses after it has acquired the
	// tasks-map read lock that pins oldTask's identity.
	dm.activeTasksMu.Lock()
	type stopResult struct {
		receipt StopReceipt
		err     error
	}
	secondResult := make(chan stopResult, 1)
	go func() {
		receipt, requestErr := dm.RequestRemoveTask(oldTask.ID)
		secondResult <- stopResult{receipt: receipt, err: requestErr}
	}()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if !dm.tasksMu.TryLock() {
			break
		}
		dm.tasksMu.Unlock()
		if time.Now().After(deadline) {
			dm.activeTasksMu.Unlock()
			t.Fatal("second request never pinned the old task identity")
		}
		time.Sleep(time.Millisecond)
	}

	close(releaseCleanupBarrier)
	select {
	case completed := <-first.completion:
		dm.activeTasksMu.Unlock()
		t.Fatalf("old removal crossed its pinned reader and completed early: %#v", completed)
	case <-time.After(30 * time.Millisecond):
	}
	dm.activeTasksMu.Unlock()

	var second stopResult
	select {
	case second = <-secondResult:
	case <-time.After(2 * time.Second):
		t.Fatal("second removal request did not return")
	}
	if second.err != nil {
		t.Fatal(second.err)
	}
	if second.receipt.OperationID != first.OperationID || second.receipt.completion != first.completion {
		t.Fatalf("concurrent removal did not converge: first=%#v second=%#v", first, second.receipt)
	}
	select {
	case terminal := <-first.completion:
		if terminal.ExecutionState != string(StopOperationCompleted) {
			t.Fatalf("unexpected terminal receipt: %#v", terminal)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("first removal did not complete")
	}

	newTask, err := createStrictTestTask(dm, oldTask.ID, "https://example.com/new.mp4", "Video", adapter.id)
	if err != nil {
		t.Fatal(err)
	}
	if newTask == oldTask || newTask.eventInstance <= oldTask.eventInstance {
		t.Fatalf("replacement identity was not advanced: old=%p/%d new=%p/%d", oldTask, oldTask.eventInstance, newTask, newTask.eventInstance)
	}
	if owner, ok := dm.outputAllocator.Owner(newTask.OutputPolicy.PlannedFinalPath); !ok || owner != newTask.ID {
		t.Fatalf("replacement reservation missing after old requests completed: owner=%q ok=%v", owner, ok)
	}
	persisted, err := NewTaskStore(dm.StatePath()).Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(persisted.Tasks) != 1 || persisted.Tasks[0].ID != newTask.ID {
		t.Fatalf("replacement missing from durable state: %#v", persisted.Tasks)
	}
}

func TestExpectedTaskReferenceRejectsSameIDReplacementCommands(t *testing.T) {
	directory := t.TempDir()
	dm := NewDownloadManager(directory, 1)
	adapter := &lifecycleTestAdapter{id: "expected-task-reference"}
	if err := dm.RegisterPlatformAdapter(adapter); err != nil {
		t.Fatal(err)
	}
	dm.SetStatePath(filepath.Join(directory, "downloads.v2.json"))
	oldTask, err := createStrictTestTask(dm, "reused-id", "https://example.com/old.mp4", "Video", adapter.id)
	if err != nil {
		t.Fatal(err)
	}
	oldReference := dm.PublicTaskSnapshot(oldTask)
	remove, err := dm.RequestRemoveTaskExpected(oldTask.ID, oldReference.Instance, oldReference.Generation)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-remove.completion:
	case <-time.After(2 * time.Second):
		t.Fatal("old task removal did not complete")
	}
	newTask, err := createStrictTestTask(dm, oldTask.ID, "https://example.com/new.mp4", "Video", adapter.id)
	if err != nil {
		t.Fatal(err)
	}
	newReference := dm.PublicTaskSnapshot(newTask)
	if newReference.Instance <= oldReference.Instance {
		t.Fatalf("replacement instance=%d, want > %d", newReference.Instance, oldReference.Instance)
	}

	assertStale := func(name string, err error) {
		t.Helper()
		var taskErr *downloadtask.TaskError
		if !errors.As(err, &taskErr) || taskErr.Code != "task.stale_reference" {
			t.Fatalf("%s error=%v (%#v), want task.stale_reference", name, err, taskErr)
		}
	}
	_, err = dm.RequestRemoveTaskExpected(newTask.ID, oldReference.Instance, oldReference.Generation)
	assertStale("remove", err)
	assertStale("resume", dm.ResumeTaskExpected(newTask.ID, oldReference.Instance, oldReference.Generation))
	assertStale("retry", dm.RetryTaskExpected(newTask.ID, oldReference.Instance, oldReference.Generation))

	retained, err := dm.GetTask(newTask.ID)
	if err != nil || retained != newTask {
		t.Fatalf("stale command changed replacement: retained=%p err=%v", retained, err)
	}
	if owner, ok := dm.outputAllocator.Owner(newTask.OutputPolicy.PlannedFinalPath); !ok || owner != newTask.ID {
		t.Fatalf("stale command released replacement reservation: owner=%q ok=%v", owner, ok)
	}
}

func TestFinishedExecutionWaitsForRealWorkerDoneBeforeRetryOrRemoval(t *testing.T) {
	dm := NewDownloadManager(t.TempDir(), 1)
	adapter := &finishedWorkerBarrierAdapter{id: "finished-worker-barrier"}
	if err := dm.RegisterPlatformAdapter(adapter); err != nil {
		t.Fatal(err)
	}
	events := make(chan StopEvent, 16)
	dm.SetStopEventCallback(func(event StopEvent) { events <- event })

	callbackEntered := make(chan struct{})
	releaseCallback := make(chan struct{})
	var callbackOnce sync.Once
	dm.SetErrorCallback(func(*DownloadTask, error) {
		callbackOnce.Do(func() { close(callbackEntered) })
		<-releaseCallback
	})
	if _, err := createStrictTestTask(dm, "task-1", "https://example.com/video.mp4", "Video", adapter.id); err != nil {
		t.Fatal(err)
	}
	if err := dm.StartTask("task-1"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-callbackEntered:
	case <-time.After(time.Second):
		t.Fatal("error callback was not reached")
	}

	// The adapter has returned and status is failed, but the worker's real done
	// barrier remains open until the callback and defer finish.
	err := dm.RetryTask("task-1")
	var taskErr *downloadtask.TaskError
	if !errors.As(err, &taskErr) || taskErr.Code != "task.worker_stopping" {
		t.Fatalf("retry did not reject the live finished generation: %v (%#v)", err, taskErr)
	}
	if adapter.runCalls.Load() != 1 {
		t.Fatalf("a new generation started before worker done: calls=%d", adapter.runCalls.Load())
	}

	receipt, err := dm.RequestRemoveTask("task-1")
	if err != nil {
		t.Fatal(err)
	}
	if !receipt.Accepted || receipt.OperationID == "" {
		t.Fatalf("unexpected removal receipt: %#v", receipt)
	}

	deadline := time.NewTimer(50 * time.Millisecond)
	defer deadline.Stop()
	waiting := true
	for waiting {
		select {
		case event := <-events:
			if event.Phase == TaskLifecycleCompleted || event.Phase == TaskLifecycleFailed {
				t.Fatalf("terminal removal arrived before the real worker done barrier: %#v", event)
			}
		case <-deadline.C:
			waiting = false
		}
	}
	if adapter.cleanupCalls.Load() != 0 {
		t.Fatal("cleanup ran before the real worker exited")
	}
	if _, err := dm.GetTask("task-1"); err != nil {
		t.Fatalf("task was deleted before worker exit: %v", err)
	}

	close(releaseCallback)
	terminal := waitForLifecycleEvent(t, events, func(event StopEvent) bool {
		return event.Phase == TaskLifecycleCompleted
	})
	if !terminal.Removed || terminal.EffectiveReason != downloadtask.StopReasonTaskRemoval {
		t.Fatalf("unexpected terminal removal event: %#v", terminal)
	}
	if adapter.cleanupCalls.Load() != 1 {
		t.Fatalf("cleanup calls=%d, want exactly 1", adapter.cleanupCalls.Load())
	}
	if _, err := dm.GetTask("task-1"); err == nil {
		t.Fatal("task remained after joined removal")
	}
}

func TestRemovePersistenceFailureRetainsTaskAndEmitsFailedTerminal(t *testing.T) {
	directory := t.TempDir()
	dm := NewDownloadManager(directory, 1)
	dm.SetStopTimeouts(time.Second, time.Second)
	started := make(chan struct{})
	workerRelease := make(chan struct{})
	adapter := &lifecycleTestAdapter{
		id:            "remove-persist-failure",
		started:       started,
		releases:      map[string]<-chan struct{}{"task-1": workerRelease},
		ignoreContext: true,
	}
	if err := dm.RegisterPlatformAdapter(adapter); err != nil {
		t.Fatal(err)
	}
	dm.SetStatePath(filepath.Join(directory, "downloads.v2.json"))
	events := make(chan StopEvent, 16)
	dm.SetStopEventCallback(func(event StopEvent) { events <- event })
	task, err := createStrictTestTask(dm, "task-1", "https://example.com/video.mp4", "Video", adapter.id)
	if err != nil {
		t.Fatal(err)
	}
	if err := dm.StartTask("task-1"); err != nil {
		t.Fatal(err)
	}
	<-started
	dm.taskStore.writeFile = func(context.Context, string, []byte) error {
		return errors.New("injected terminal persistence failure")
	}
	if _, err := dm.RequestRemoveTask("task-1"); err != nil {
		t.Fatal(err)
	}
	close(workerRelease)
	terminal := waitForLifecycleEvent(t, events, func(event StopEvent) bool {
		return event.Phase == TaskLifecycleFailed && event.Error != nil && event.Error.Code == "task.persistence_failed"
	})
	if terminal.Removed {
		t.Fatalf("failed persistence claimed successful removal: %#v", terminal)
	}
	retained, err := dm.GetTask("task-1")
	if err != nil || retained != task {
		t.Fatalf("task was not retained for retry: task=%p retained=%p err=%v", task, retained, err)
	}
	if owner, ok := dm.outputAllocator.Owner(task.OutputPolicy.PlannedFinalPath); !ok || owner != task.ID {
		t.Fatalf("failed removal released reservation: owner=%q ok=%v", owner, ok)
	}
	persisted, err := NewTaskStore(dm.StatePath()).Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(persisted.Tasks) != 1 || persisted.Tasks[0].ID != task.ID {
		t.Fatalf("durable pre-removal snapshot was lost: %#v", persisted)
	}
}

func TestShutdownUsesOneGlobalDeadlineAndLeavesWorkersRecoverable(t *testing.T) {
	dm := NewDownloadManager(t.TempDir(), 2)
	releaseOne := make(chan struct{})
	releaseTwo := make(chan struct{})
	adapter := &lifecycleTestAdapter{
		id: "shutdown",
		releases: map[string]<-chan struct{}{
			"one": releaseOne,
			"two": releaseTwo,
		},
		ignoreContext: true,
	}
	if err := dm.RegisterPlatformAdapter(adapter); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"one", "two"} {
		if _, err := createStrictTestTask(dm, id, "https://example.com/"+id, id, adapter.id); err != nil {
			t.Fatal(err)
		}
		if err := dm.StartTask(id); err != nil {
			t.Fatal(err)
		}
	}
	startDeadline := time.Now().Add(2 * time.Second)
	for adapter.startedCount.Load() != 2 && time.Now().Before(startDeadline) {
		time.Sleep(time.Millisecond)
	}
	if adapter.startedCount.Load() != 2 {
		t.Fatal("shutdown test workers did not start")
	}
	deadline, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	startedAt := time.Now()
	result := dm.Shutdown(deadline)
	if result.Completed || len(result.TimedOutTaskIDs) != 2 {
		t.Fatalf("unexpected bounded shutdown result: %#v", result)
	}
	if elapsed := time.Since(startedAt); elapsed > 500*time.Millisecond {
		t.Fatalf("shutdown exceeded global bound: %s", elapsed)
	}
	if adapter.cleanupCalls.Load() != 0 {
		t.Fatal("shutdown ran cancel/remove cleanup before workers exited")
	}
	close(releaseOne)
	close(releaseTwo)
	deadlineAt := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadlineAt) {
		one, _ := dm.GetTask("one")
		two, _ := dm.GetTask("two")
		if one.GetStatus() == StatusPaused && two.GetStatus() == StatusPaused {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("shutdown coordinators did not finish after workers exited")
}

func waitForLifecycleEvent(t *testing.T, events <-chan StopEvent, match func(StopEvent) bool) StopEvent {
	t.Helper()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for {
		select {
		case event := <-events:
			if match(event) {
				return event
			}
		case <-timer.C:
			t.Fatal("timed out waiting for lifecycle event")
		}
	}
}
