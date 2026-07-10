package downloader

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	downloadtask "EasyDownload/internal/download/task"
)

type creationAtomicityAdapter struct {
	id       downloadtask.PlatformID
	started  chan string
	releases map[string]<-chan struct{}
}

func (adapter *creationAtomicityAdapter) ID() downloadtask.PlatformID { return adapter.id }
func (*creationAtomicityAdapter) ValidateTask(downloadtask.TaskSnapshot) error {
	return nil
}
func (adapter *creationAtomicityAdapter) RunTask(ctx context.Context, snapshot downloadtask.TaskSnapshot, _ downloadtask.TaskExecutionContext) error {
	if adapter.started != nil {
		select {
		case adapter.started <- snapshot.ID:
		default:
		}
	}
	release := adapter.releases[snapshot.ID]
	if release == nil {
		return nil
	}
	select {
	case <-release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (*creationAtomicityAdapter) CleanupTask(context.Context, downloadtask.TaskSnapshot, downloadtask.StopReason) error {
	return nil
}

func TestCreateTaskFailureCannotBeResurrectedByConcurrentSaveState(t *testing.T) {
	directory := t.TempDir()
	dm := NewDownloadManager(directory, 1)
	platformID := downloadtask.PlatformID("create-linearization")
	if err := registerInertTestAdapter(dm, platformID); err != nil {
		t.Fatal(err)
	}
	dm.SetStatePath(filepath.Join(directory, "downloads.v2.json"))

	realWrite := dm.taskStore.writeFile
	writeEntered := make(chan struct{})
	releaseWrite := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(releaseWrite) }) })
	var intercepted atomic.Bool
	dm.taskStore.writeFile = func(ctx context.Context, destination string, data []byte) error {
		if intercepted.CompareAndSwap(false, true) {
			close(writeEntered)
			<-releaseWrite
			return errors.New("injected first creation persistence failure")
		}
		return realWrite(ctx, destination, data)
	}

	input := atomicCreationInput("failed-create", platformID)
	createDone := make(chan error, 1)
	go func() {
		_, err := dm.CreateTask(input)
		createDone <- err
	}()
	waitForSignal(t, writeEntered, "first creation write")

	saveStarted := make(chan struct{})
	saveDone := make(chan error, 1)
	go func() {
		close(saveStarted)
		saveDone <- dm.SaveState()
	}()
	waitForSignal(t, saveStarted, "concurrent SaveState start")
	select {
	case err := <-saveDone:
		t.Fatalf("concurrent SaveState bypassed the creation transaction: %v", err)
	case <-time.After(25 * time.Millisecond):
	}

	releaseOnce.Do(func() { close(releaseWrite) })
	if err := <-createDone; err == nil {
		t.Fatal("CreateTask unexpectedly succeeded")
	}
	if err := <-saveDone; err != nil {
		t.Fatalf("concurrent SaveState failed after creation rollback: %v", err)
	}

	assertTaskFullyAbsent(t, dm, input.ID, filepath.Join(directory, input.SuggestedFilename+input.SuggestedExtension))
	assertDurableTaskIDs(t, dm.StatePath())
}

func TestRemovalDurableExclusionCannotBeReintroducedByConcurrentSaveState(t *testing.T) {
	directory := t.TempDir()
	dm := NewDownloadManager(directory, 1)
	platformID := downloadtask.PlatformID("remove-linearization")
	if err := registerInertTestAdapter(dm, platformID); err != nil {
		t.Fatal(err)
	}
	dm.SetStatePath(filepath.Join(directory, "downloads.v2.json"))
	task, err := dm.CreateTask(atomicCreationInput("remove-me", platformID))
	if err != nil {
		t.Fatal(err)
	}

	realWrite := dm.taskStore.writeFile
	exclusionWriteEntered := make(chan struct{})
	releaseExclusionWrite := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(releaseExclusionWrite) }) })
	var blocked atomic.Bool
	dm.taskStore.writeFile = func(ctx context.Context, destination string, data []byte) error {
		if filepath.Clean(destination) == filepath.Clean(dm.StatePath()) {
			var envelope TaskStoreEnvelope
			if err := json.Unmarshal(data, &envelope); err != nil {
				return err
			}
			if !envelopeContainsTask(envelope, task.ID) && blocked.CompareAndSwap(false, true) {
				close(exclusionWriteEntered)
				<-releaseExclusionWrite
			}
		}
		return realWrite(ctx, destination, data)
	}

	receipt, err := dm.RequestRemoveTask(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	waitForSignal(t, exclusionWriteEntered, "durable removal exclusion write")

	saveStarted := make(chan struct{})
	saveDone := make(chan error, 1)
	go func() {
		close(saveStarted)
		saveDone <- dm.SaveState()
	}()
	waitForSignal(t, saveStarted, "concurrent SaveState start")
	select {
	case err := <-saveDone:
		t.Fatalf("concurrent SaveState bypassed the removal transaction: %v", err)
	case <-time.After(25 * time.Millisecond):
	}

	releaseOnce.Do(func() { close(releaseExclusionWrite) })
	if err := <-saveDone; err != nil {
		t.Fatalf("concurrent SaveState failed after removal: %v", err)
	}
	select {
	case terminal := <-receipt.completion:
		if terminal.Error != nil || terminal.ExecutionState != string(StopOperationCompleted) {
			t.Fatalf("removal did not complete cleanly: %#v", terminal)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for removal completion")
	}

	assertTaskFullyAbsent(t, dm, task.ID, task.OutputPolicy.PlannedFinalPath)
	assertDurableTaskIDs(t, dm.StatePath())
}

func TestRemovalAndSameIDRecreateCannotReleaseNewReservation(t *testing.T) {
	directory := t.TempDir()
	dm := NewDownloadManager(directory, 1)
	platformID := downloadtask.PlatformID("remove-recreate-aba")
	if err := registerInertTestAdapter(dm, platformID); err != nil {
		t.Fatal(err)
	}
	dm.SetStatePath(filepath.Join(directory, "downloads.v2.json"))
	oldTask, err := dm.CreateTask(atomicCreationInput("same-id", platformID))
	if err != nil {
		t.Fatal(err)
	}

	realWrite := dm.taskStore.writeFile
	exclusionEntered := make(chan struct{})
	releaseExclusion := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(releaseExclusion) }) })
	var blocked atomic.Bool
	dm.taskStore.writeFile = func(ctx context.Context, destination string, data []byte) error {
		if filepath.Clean(destination) == filepath.Clean(dm.StatePath()) {
			var envelope TaskStoreEnvelope
			if err := json.Unmarshal(data, &envelope); err != nil {
				return err
			}
			if !envelopeContainsTask(envelope, oldTask.ID) && blocked.CompareAndSwap(false, true) {
				close(exclusionEntered)
				<-releaseExclusion
			}
		}
		return realWrite(ctx, destination, data)
	}

	receipt, err := dm.RequestRemoveTask(oldTask.ID)
	if err != nil {
		t.Fatal(err)
	}
	waitForSignal(t, exclusionEntered, "same-ID removal exclusion")

	recreatedInput := atomicCreationInput(oldTask.ID, platformID)
	recreatedInput.SuggestedFilename = "recreated"
	type createResult struct {
		task *DownloadTask
		err  error
	}
	createDone := make(chan createResult, 1)
	go func() {
		task, createErr := dm.CreateTask(recreatedInput)
		createDone <- createResult{task: task, err: createErr}
	}()
	select {
	case result := <-createDone:
		t.Fatalf("same-ID create bypassed removal transaction: task=%#v err=%v", result.task, result.err)
	case <-time.After(25 * time.Millisecond):
	}

	releaseOnce.Do(func() { close(releaseExclusion) })
	select {
	case terminal := <-receipt.completion:
		if terminal.Error != nil || terminal.ExecutionState != string(StopOperationCompleted) {
			t.Fatalf("removal failed: %#v", terminal)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for removal")
	}
	result := <-createDone
	if result.err != nil {
		t.Fatal(result.err)
	}
	if result.task == nil {
		t.Fatal("same-ID recreation returned nil task")
	}
	if owner, reserved := dm.outputAllocator.Owner(result.task.OutputPolicy.PlannedFinalPath); !reserved || owner != result.task.ID {
		t.Fatalf("new reservation owner=%q reserved=%v, want %q", owner, reserved, result.task.ID)
	}
	assertDurableTaskIDs(t, dm.StatePath(), result.task.ID)
}

func TestCreateAndStartTaskPersistenceFailureRollsBackAllState(t *testing.T) {
	t.Run("initial running transition", func(t *testing.T) {
		directory := t.TempDir()
		dm := NewDownloadManager(directory, 1)
		platformID := downloadtask.PlatformID("create-start-running-failure")
		adapter := &creationAtomicityAdapter{id: platformID}
		if err := dm.RegisterPlatformAdapter(adapter); err != nil {
			t.Fatal(err)
		}
		dm.SetStatePath(filepath.Join(directory, "downloads.v2.json"))
		dm.taskStore.writeFile = func(context.Context, string, []byte) error {
			return errors.New("injected initial running persistence failure")
		}

		input := atomicCreationInput("running-failure", platformID)
		if _, err := dm.CreateAndStartTask(input); err == nil {
			t.Fatal("CreateAndStartTask unexpectedly succeeded")
		}
		assertManagerSchedulingState(t, dm, 0)
		assertTaskFullyAbsent(t, dm, input.ID, filepath.Join(directory, input.SuggestedFilename+input.SuggestedExtension))
		assertDurableTaskIDs(t, dm.StatePath())
	})

	t.Run("initial queued transition", func(t *testing.T) {
		directory := t.TempDir()
		dm := NewDownloadManager(directory, 1)
		platformID := downloadtask.PlatformID("create-start-queued-failure")
		blockerRelease := make(chan struct{})
		var releaseOnce sync.Once
		adapter := &creationAtomicityAdapter{
			id:       platformID,
			started:  make(chan string, 4),
			releases: map[string]<-chan struct{}{"blocker": blockerRelease},
		}
		if err := dm.RegisterPlatformAdapter(adapter); err != nil {
			t.Fatal(err)
		}
		dm.SetStatePath(filepath.Join(directory, "downloads.v2.json"))
		if _, err := dm.CreateAndStartTask(atomicCreationInput("blocker", platformID)); err != nil {
			t.Fatal(err)
		}
		waitForStartedTask(t, adapter.started, "blocker")

		realWrite := dm.taskStore.writeFile
		t.Cleanup(func() {
			dm.taskStore.writeFile = realWrite
			releaseOnce.Do(func() { close(blockerRelease) })
		})
		dm.taskStore.writeFile = func(context.Context, string, []byte) error {
			return errors.New("injected initial queued persistence failure")
		}

		input := atomicCreationInput("queued-failure", platformID)
		if _, err := dm.CreateAndStartTask(input); err == nil {
			t.Fatal("CreateAndStartTask unexpectedly succeeded")
		}
		assertManagerSchedulingState(t, dm, 1)
		assertTaskFullyAbsent(t, dm, input.ID, filepath.Join(directory, input.SuggestedFilename+input.SuggestedExtension))
		assertDurableTaskIDs(t, dm.StatePath(), "blocker")

		dm.taskStore.writeFile = realWrite
		releaseOnce.Do(func() { close(blockerRelease) })
		waitForActiveCount(t, dm, 0)
	})
}

func TestCreateAndStartTaskPersistsOnlyInitialExecutionState(t *testing.T) {
	t.Run("running", func(t *testing.T) {
		directory := t.TempDir()
		dm := NewDownloadManager(directory, 1)
		platformID := downloadtask.PlatformID("create-start-running-success")
		release := make(chan struct{})
		var releaseOnce sync.Once
		adapter := &creationAtomicityAdapter{
			id:       platformID,
			started:  make(chan string, 4),
			releases: map[string]<-chan struct{}{"running-success": release},
		}
		if err := dm.RegisterPlatformAdapter(adapter); err != nil {
			t.Fatal(err)
		}
		dm.SetStatePath(filepath.Join(directory, "downloads.v2.json"))
		restore, writes := recordPrimaryTaskStoreWrites(dm)
		t.Cleanup(func() {
			restore()
			releaseOnce.Do(func() { close(release) })
		})

		task, err := dm.CreateAndStartTask(atomicCreationInput("running-success", platformID))
		if err != nil {
			t.Fatal(err)
		}
		recorded := writes()
		assertSingleInitialDurableState(t, recorded, task.ID, StatusDownloading, "running")
		restore()
		waitForStartedTask(t, adapter.started, task.ID)
		releaseOnce.Do(func() { close(release) })
		waitForActiveCount(t, dm, 0)
	})

	t.Run("queued", func(t *testing.T) {
		directory := t.TempDir()
		dm := NewDownloadManager(directory, 1)
		platformID := downloadtask.PlatformID("create-start-queued-success")
		blockerRelease := make(chan struct{})
		queuedRelease := make(chan struct{})
		var blockerOnce sync.Once
		var queuedOnce sync.Once
		adapter := &creationAtomicityAdapter{
			id:      platformID,
			started: make(chan string, 4),
			releases: map[string]<-chan struct{}{
				"blocker":        blockerRelease,
				"queued-success": queuedRelease,
			},
		}
		if err := dm.RegisterPlatformAdapter(adapter); err != nil {
			t.Fatal(err)
		}
		dm.SetStatePath(filepath.Join(directory, "downloads.v2.json"))
		if _, err := dm.CreateAndStartTask(atomicCreationInput("blocker", platformID)); err != nil {
			t.Fatal(err)
		}
		waitForStartedTask(t, adapter.started, "blocker")

		restore, writes := recordPrimaryTaskStoreWrites(dm)
		t.Cleanup(func() {
			restore()
			queuedOnce.Do(func() { close(queuedRelease) })
			blockerOnce.Do(func() { close(blockerRelease) })
		})
		task, err := dm.CreateAndStartTask(atomicCreationInput("queued-success", platformID))
		if err != nil {
			t.Fatal(err)
		}
		recorded := writes()
		assertSingleInitialDurableState(t, recorded, task.ID, StatusPending, "queued")
		assertManagerSchedulingState(t, dm, 1, task.ID)

		restore()
		queuedOnce.Do(func() { close(queuedRelease) })
		blockerOnce.Do(func() { close(blockerRelease) })
		waitForActiveCount(t, dm, 0)
	})
}

func TestPausePendingTaskDoesNotDequeueIt(t *testing.T) {
	dm := NewDownloadManager(t.TempDir(), 1)
	platformID := downloadtask.PlatformID("pause-pending-queue")
	blockerRelease := make(chan struct{})
	queuedRelease := make(chan struct{})
	var blockerOnce sync.Once
	var queuedOnce sync.Once
	adapter := &creationAtomicityAdapter{
		id:      platformID,
		started: make(chan string, 4),
		releases: map[string]<-chan struct{}{
			"blocker": blockerRelease,
			"queued":  queuedRelease,
		},
	}
	if err := dm.RegisterPlatformAdapter(adapter); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		queuedOnce.Do(func() { close(queuedRelease) })
		blockerOnce.Do(func() { close(blockerRelease) })
	})

	if _, err := dm.CreateAndStartTask(atomicCreationInput("blocker", platformID)); err != nil {
		t.Fatal(err)
	}
	waitForStartedTask(t, adapter.started, "blocker")
	queuedTask, err := dm.CreateAndStartTask(atomicCreationInput("queued", platformID))
	if err != nil {
		t.Fatal(err)
	}
	assertManagerSchedulingState(t, dm, 1, queuedTask.ID)

	if receipt, err := dm.RequestPauseTask(queuedTask.ID); err == nil {
		t.Fatalf("Pause(pending) unexpectedly succeeded: %#v", receipt)
	}
	if status := queuedTask.GetStatus(); status != StatusPending {
		t.Fatalf("Pause(pending) changed status=%s, want %s", status, StatusPending)
	}
	assertManagerSchedulingState(t, dm, 1, queuedTask.ID)

	blockerOnce.Do(func() { close(blockerRelease) })
	waitForStartedTask(t, adapter.started, queuedTask.ID)
	if status := queuedTask.GetStatus(); status != StatusDownloading {
		t.Fatalf("queued task status after slot release=%s, want %s", status, StatusDownloading)
	}
	queuedOnce.Do(func() { close(queuedRelease) })
	waitForActiveCount(t, dm, 0)
}

func TestQueuedGenerationRemainsStableAcrossAutomaticDispatch(t *testing.T) {
	dm := NewDownloadManager(t.TempDir(), 1)
	platformID := downloadtask.PlatformID("queued-generation-fence")
	blockerRelease := make(chan struct{})
	queuedRelease := make(chan struct{})
	adapter := &creationAtomicityAdapter{
		id:      platformID,
		started: make(chan string, 4),
		releases: map[string]<-chan struct{}{
			"blocker": blockerRelease,
			"queued":  queuedRelease,
		},
	}
	if err := dm.RegisterPlatformAdapter(adapter); err != nil {
		t.Fatal(err)
	}
	if _, err := dm.CreateAndStartTask(atomicCreationInput("blocker", platformID)); err != nil {
		t.Fatal(err)
	}
	waitForStartedTask(t, adapter.started, "blocker")
	queuedTask, err := dm.CreateAndStartTask(atomicCreationInput("queued", platformID))
	if err != nil {
		t.Fatal(err)
	}
	queuedReference := dm.PublicTaskSnapshot(queuedTask)
	if queuedReference.Status != StatusPending || queuedReference.Generation == 0 {
		t.Fatalf("queued public reference=%#v, want pending reserved generation", queuedReference)
	}

	close(blockerRelease)
	waitForStartedTask(t, adapter.started, queuedTask.ID)
	current := dm.PublicTaskSnapshot(queuedTask)
	if current.Status != StatusDownloading || current.Generation != queuedReference.Generation {
		t.Fatalf("automatic dispatch changed generation: queued=%#v running=%#v", queuedReference, current)
	}

	cancel, err := dm.RequestCancelTaskExpected(queuedTask.ID, queuedReference.Instance, queuedReference.Generation)
	if err != nil {
		t.Fatalf("queued reference could not cancel auto-dispatched task: %v", err)
	}
	if cancel.TaskGeneration != queuedReference.Generation {
		t.Fatalf("cancel receipt generation=%d, want reserved %d", cancel.TaskGeneration, queuedReference.Generation)
	}
	remove, err := dm.RequestRemoveTaskExpected(queuedTask.ID, cancel.TaskInstance, cancel.TaskGeneration)
	if err != nil {
		t.Fatalf("cancel -> remove upgrade failed: %v", err)
	}
	if remove.OperationID != cancel.OperationID || remove.EffectiveReason != downloadtask.StopReasonTaskRemoval {
		t.Fatalf("unexpected upgraded removal: cancel=%#v remove=%#v", cancel, remove)
	}
	select {
	case terminal := <-cancel.completion:
		if terminal.ExecutionState != string(StopOperationCompleted) {
			t.Fatalf("unexpected terminal receipt: %#v", terminal)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("queued task removal did not finish")
	}
	close(queuedRelease)
	waitForActiveCount(t, dm, 0)
}

func atomicCreationInput(id string, platformID downloadtask.PlatformID) TaskCreationInput {
	return TaskCreationInput{
		ID:                  id,
		PlatformID:          platformID,
		Title:               id,
		DisplaySource:       string(platformID),
		SuggestedFilename:   id,
		SuggestedExtension:  ".mp4",
		PlatformDataVersion: 1,
		PlatformData:        json.RawMessage(`{"url":"https://example.com/video.mp4"}`),
	}
}

func assertTaskFullyAbsent(t *testing.T, dm *DownloadManager, taskID, plannedPath string) {
	t.Helper()
	if _, err := dm.GetTask(taskID); err == nil {
		t.Fatalf("task %q remained in the manager map", taskID)
	}
	if owner, ok := dm.outputAllocator.Owner(plannedPath); ok {
		t.Fatalf("task %q retained output reservation owned by %q", taskID, owner)
	}
	dm.activeTasksMu.Lock()
	_, queued := dm.queuedTaskIDs[taskID]
	for _, task := range dm.queuedTasks {
		if task != nil && task.ID == taskID {
			queued = true
		}
	}
	dm.activeTasksMu.Unlock()
	if queued {
		t.Fatalf("task %q remained queued", taskID)
	}
}

func assertManagerSchedulingState(t *testing.T, dm *DownloadManager, wantActive int, wantQueued ...string) {
	t.Helper()
	want := make(map[string]struct{}, len(wantQueued))
	for _, id := range wantQueued {
		want[id] = struct{}{}
	}
	dm.activeTasksMu.Lock()
	active := dm.activeTasks
	queuedIDs := make(map[string]struct{}, len(dm.queuedTaskIDs))
	for id := range dm.queuedTaskIDs {
		queuedIDs[id] = struct{}{}
	}
	queuedLength := len(dm.queuedTasks)
	dm.activeTasksMu.Unlock()
	if active != wantActive || len(queuedIDs) != len(want) || queuedLength != len(want) {
		t.Fatalf("scheduling state active=%d queuedIDs=%v queuedLength=%d, want active=%d queued=%v", active, queuedIDs, queuedLength, wantActive, want)
	}
	for id := range want {
		if _, ok := queuedIDs[id]; !ok {
			t.Fatalf("expected queued task %q, got %v", id, queuedIDs)
		}
	}
}

func assertDurableTaskIDs(t *testing.T, path string, wantIDs ...string) {
	t.Helper()
	envelope, err := NewTaskStore(path).Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := make(map[string]struct{}, len(wantIDs))
	for _, id := range wantIDs {
		want[id] = struct{}{}
	}
	got := make(map[string]struct{}, len(envelope.Tasks))
	for _, snapshot := range envelope.Tasks {
		got[snapshot.ID] = struct{}{}
	}
	if len(got) != len(want) {
		t.Fatalf("durable task IDs=%v, want %v (revision=%d)", got, want, envelope.Revision)
	}
	for id := range want {
		if _, ok := got[id]; !ok {
			t.Fatalf("durable task IDs=%v, missing %q", got, id)
		}
	}
}

func envelopeContainsTask(envelope TaskStoreEnvelope, taskID string) bool {
	for _, snapshot := range envelope.Tasks {
		if snapshot.ID == taskID {
			return true
		}
	}
	return false
}

func recordPrimaryTaskStoreWrites(dm *DownloadManager) (func(), func() []TaskStoreEnvelope) {
	realWrite := dm.taskStore.writeFile
	var mu sync.Mutex
	writes := make([]TaskStoreEnvelope, 0, 2)
	dm.taskStore.writeFile = func(ctx context.Context, destination string, data []byte) error {
		if filepath.Clean(destination) == filepath.Clean(dm.StatePath()) {
			var envelope TaskStoreEnvelope
			if err := json.Unmarshal(data, &envelope); err != nil {
				return err
			}
			mu.Lock()
			writes = append(writes, cloneTaskStoreEnvelope(envelope))
			mu.Unlock()
		}
		return realWrite(ctx, destination, data)
	}
	var restoreOnce sync.Once
	restore := func() {
		restoreOnce.Do(func() { dm.taskStore.writeFile = realWrite })
	}
	snapshot := func() []TaskStoreEnvelope {
		mu.Lock()
		defer mu.Unlock()
		result := make([]TaskStoreEnvelope, len(writes))
		for index := range writes {
			result[index] = cloneTaskStoreEnvelope(writes[index])
		}
		return result
	}
	return restore, snapshot
}

func assertSingleInitialDurableState(t *testing.T, writes []TaskStoreEnvelope, taskID string, wantStatus DownloadStatus, wantStage string) {
	t.Helper()
	if len(writes) != 1 {
		t.Fatalf("primary durable writes=%d, want exactly one atomic create/start snapshot: %#v", len(writes), writes)
	}
	for _, snapshot := range writes[0].Tasks {
		if snapshot.ID != taskID {
			continue
		}
		if DownloadStatus(snapshot.Status) != wantStatus || snapshot.Progress.CurrentStage != wantStage {
			t.Fatalf("initial durable task state status=%s stage=%q, want %s/%q", snapshot.Status, snapshot.Progress.CurrentStage, wantStatus, wantStage)
		}
		return
	}
	t.Fatalf("atomic create/start snapshot omitted task %q: %#v", taskID, writes[0])
}

func waitForSignal(t *testing.T, signal <-chan struct{}, description string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", description)
	}
}

func waitForStartedTask(t *testing.T, started <-chan string, taskID string) {
	t.Helper()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for {
		select {
		case id := <-started:
			if id == taskID {
				return
			}
		case <-timer.C:
			t.Fatalf("timed out waiting for task %q to start", taskID)
		}
	}
}

func waitForActiveCount(t *testing.T, dm *DownloadManager, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if dm.GetActiveTaskCount() == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("active task count=%d, want %d", dm.GetActiveTaskCount(), want)
}
