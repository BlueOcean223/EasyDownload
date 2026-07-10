package downloader

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	downloadtask "EasyDownload/internal/download/task"
)

func TestOldGenerationRejectsEveryLateMutationSink(t *testing.T) {
	dm := NewDownloadManager(t.TempDir(), 1)
	platformID := downloadtask.PlatformID("late-mutation")
	if err := registerInertTestAdapter(dm, platformID); err != nil {
		t.Fatal(err)
	}
	task, err := createStrictTestTask(dm, "task-1", "https://example.com/video.mp4", "Video", platformID)
	if err != nil {
		t.Fatal(err)
	}

	task.mu.Lock()
	oldGeneration := newTaskExecutionLocked(task)
	oldContext := newManagerExecutionContext(dm, task, oldGeneration)
	currentGeneration := newTaskExecutionLocked(task)
	task.Status = StatusDownloading
	originalProgress := task.ProgressSummary
	task.mu.Unlock()
	defer func() {
		oldGeneration.cancel()
		oldGeneration.finish()
		currentGeneration.cancel()
		currentGeneration.finish()
		task.mu.Lock()
		task.execution = nil
		task.cancel = nil
		task.mu.Unlock()
	}()

	temporaryPath := filepath.Join(t.TempDir(), "late-generation.part")
	payload := []byte("late generation payload")
	if err := os.WriteFile(temporaryPath, payload, 0600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(payload)
	lateCalls := []struct {
		name string
		call func() error
	}{
		{name: "progress", call: func() error {
			return oldContext.UpdateTaskProgress(downloadtask.TaskProgressUpdate{
				StageID: "late", StagePercent: downloadtask.ProgressPercent(80), BytesLoaded: 8, BytesTotal: 10,
			})
		}},
		{name: "artifact", call: func() error {
			return oldContext.RecordArtifact(downloadtask.TaskArtifact{
				Kind: downloadtask.TaskArtifactTemporary, Path: temporaryPath,
			})
		}},
		{name: "post-publish cleanup diagnostic", call: func() error {
			return oldContext.RecordPostPublishCleanupFailure(downloadtask.TaskArtifact{
				Kind: downloadtask.TaskArtifactTemporary,
				Path: temporaryPath,
				Metadata: map[string]string{
					"cleanupError": "late cleanup failure",
				},
			})
		}},
		{name: "checkpoint", call: func() error {
			return oldContext.RecordCheckpoint(downloadtask.PlatformCheckpointEnvelope{
				Version: 1, Data: []byte(`{"cursor":"late"}`),
			})
		}},
		{name: "publish", call: func() error {
			_, err := oldContext.PublishFinal(context.Background(), temporaryPath, downloadtask.TaskArtifactDraft{
				Size: int64(len(payload)), SHA256: hex.EncodeToString(digest[:]), Primary: true,
			})
			return err
		}},
	}
	for _, lateCall := range lateCalls {
		if err := lateCall.call(); !errors.Is(err, ErrStaleExecution) {
			t.Errorf("late %s mutation error=%v, want ErrStaleExecution", lateCall.name, err)
		}
	}

	task.mu.RLock()
	gotProgress := task.ProgressSummary
	artifacts := append([]downloadtask.TaskArtifact(nil), task.Artifacts...)
	checkpoint := cloneCheckpoint(task.PlatformCheckpoint)
	intent := clonePublishIntent(task.PublishIntent)
	current := task.execution
	generationCount := task.generationCounter
	finalPath := task.OutputPolicy.PlannedFinalPath
	task.mu.RUnlock()
	if !reflect.DeepEqual(gotProgress, originalProgress) || len(artifacts) != 0 || checkpoint != nil || intent != nil {
		t.Fatalf("late generation mutated task state: progress=%#v artifacts=%#v checkpoint=%#v intent=%#v", gotProgress, artifacts, checkpoint, intent)
	}
	if current != currentGeneration || generationCount != 2 {
		t.Fatalf("late generation replaced current execution: current=%p want=%p generation=%d", current, currentGeneration, generationCount)
	}
	if _, err := os.Stat(finalPath); !os.IsNotExist(err) {
		t.Fatalf("late publish made a final path visible: %v", err)
	}
}

type cleanupFailureAdapter struct {
	id           downloadtask.PlatformID
	started      chan struct{}
	release      chan struct{}
	startedOnce  sync.Once
	runCalls     atomic.Int32
	cleanupCalls atomic.Int32
	cleanupErr   error
}

func (adapter *cleanupFailureAdapter) ID() downloadtask.PlatformID          { return adapter.id }
func (*cleanupFailureAdapter) ValidateTask(downloadtask.TaskSnapshot) error { return nil }
func (adapter *cleanupFailureAdapter) RunTask(context.Context, downloadtask.TaskSnapshot, downloadtask.TaskExecutionContext) error {
	adapter.runCalls.Add(1)
	adapter.startedOnce.Do(func() { close(adapter.started) })
	<-adapter.release
	return nil
}
func (adapter *cleanupFailureAdapter) CleanupTask(context.Context, downloadtask.TaskSnapshot, downloadtask.StopReason) error {
	adapter.cleanupCalls.Add(1)
	return adapter.cleanupErr
}

func TestCleanupFailureKeepsTaskAndReturnsStructuredTerminalFailure(t *testing.T) {
	dm := NewDownloadManager(t.TempDir(), 1)
	adapter := &cleanupFailureAdapter{
		id:         "cleanup-failure",
		started:    make(chan struct{}),
		release:    make(chan struct{}),
		cleanupErr: errors.New("private cleanup path failed"),
	}
	if err := dm.RegisterPlatformAdapter(adapter); err != nil {
		t.Fatal(err)
	}
	events := make(chan StopEvent, 8)
	dm.SetStopEventCallback(func(event StopEvent) { events <- event })
	task, err := createStrictTestTask(dm, "task-1", "https://example.com/video.mp4", "Video", adapter.id)
	if err != nil {
		t.Fatal(err)
	}
	if err := dm.StartTask(task.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-adapter.started:
	case <-time.After(time.Second):
		t.Fatal("worker did not start")
	}

	receipt, err := dm.RequestRemoveTask(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	close(adapter.release)

	var completedReceipt StopReceipt
	select {
	case completedReceipt = <-receipt.completion:
	case <-time.After(2 * time.Second):
		t.Fatal("terminal stop receipt did not arrive")
	}
	terminal := waitForLifecycleEvent(t, events, func(event StopEvent) bool {
		return event.Phase == TaskLifecycleFailed
	})
	for label, taskErr := range map[string]*downloadtask.TaskError{
		"receipt": completedReceipt.Error,
		"event":   terminal.Error,
	} {
		if taskErr == nil || taskErr.Code != "task.cleanup_failed" || taskErr.Cause != "" {
			t.Errorf("%s did not contain a sanitized cleanup failure: %#v", label, taskErr)
		}
	}
	if completedReceipt.ExecutionState != string(StopOperationFailed) || terminal.Removed || terminal.ResultStatus != downloadtask.StatusCanceled {
		t.Fatalf("unexpected cleanup terminal result: receipt=%#v event=%#v", completedReceipt, terminal)
	}
	retained, err := dm.GetTask(task.ID)
	if err != nil || retained != task {
		t.Fatalf("cleanup failure removed the task: retained=%p task=%p err=%v", retained, task, err)
	}
	task.mu.RLock()
	status := task.Status
	lastError := cloneTaskError(task.LastErrorDetail)
	execution := task.execution
	generationCount := task.generationCounter
	task.mu.RUnlock()
	if status != StatusCancelled || lastError == nil || lastError.Code != "task.cleanup_failed" {
		t.Fatalf("retained task lacks cleanup failure state: status=%s error=%#v", status, lastError)
	}
	if execution != nil || generationCount != 1 || adapter.runCalls.Load() != 1 || dm.GetActiveTaskCount() != 0 {
		t.Fatalf("cleanup failure leaked a generation: execution=%p generations=%d runs=%d active=%d", execution, generationCount, adapter.runCalls.Load(), dm.GetActiveTaskCount())
	}
	if adapter.cleanupCalls.Load() != 1 {
		t.Fatalf("cleanup calls=%d, want exactly one", adapter.cleanupCalls.Load())
	}
	if owner, ok := dm.outputAllocator.Owner(task.OutputPolicy.PlannedFinalPath); !ok || owner != task.ID {
		t.Fatalf("cleanup failure released reservation: owner=%q ok=%v", owner, ok)
	}
}
