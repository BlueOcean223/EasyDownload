package downloader

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	downloadtask "EasyDownload/internal/download/task"
)

type recoveryCountingAdapter struct {
	runs *atomic.Int32
}

type postPublishErrorAdapter struct {
	id         downloadtask.PlatformID
	lateErrors chan []error
}

func (adapter postPublishErrorAdapter) ID() downloadtask.PlatformID          { return adapter.id }
func (postPublishErrorAdapter) ValidateTask(downloadtask.TaskSnapshot) error { return nil }
func (postPublishErrorAdapter) CleanupTask(context.Context, downloadtask.TaskSnapshot, downloadtask.StopReason) error {
	return nil
}
func (adapter postPublishErrorAdapter) RunTask(ctx context.Context, snapshot downloadtask.TaskSnapshot, execution downloadtask.TaskExecutionContext) error {
	payload := []byte("primary commit")
	temporary := filepath.Join(filepath.Dir(snapshot.OutputPolicy.PlannedFinalPath), ".post-publish.part")
	if err := os.WriteFile(temporary, payload, 0o600); err != nil {
		return err
	}
	if _, err := execution.PublishFinal(ctx, temporary, downloadtask.TaskArtifactDraft{
		Size: int64(len(payload)), SHA256: sha256Hex(payload), Primary: true,
	}); err != nil {
		return err
	}
	adapter.lateErrors <- []error{
		execution.UpdateTaskProgress(downloadtask.TaskProgressUpdate{StageID: "late", StagePercent: downloadtask.ProgressPercent(1)}),
		execution.RecordCheckpoint(downloadtask.PlatformCheckpointEnvelope{Version: 1, Data: json.RawMessage(`{"late":true}`)}),
		execution.RecordArtifact(downloadtask.TaskArtifact{Kind: downloadtask.TaskArtifactTemporary, Path: filepath.Join(filepath.Dir(temporary), ".late")}),
	}
	return errors.New("adapter failed after publishing primary")
}

func (recoveryCountingAdapter) ID() downloadtask.PlatformID { return downloadtask.PlatformGeneric }

func (recoveryCountingAdapter) ValidateTask(downloadtask.TaskSnapshot) error { return nil }

func (adapter recoveryCountingAdapter) RunTask(context.Context, downloadtask.TaskSnapshot, downloadtask.TaskExecutionContext) error {
	adapter.runs.Add(1)
	return nil
}

func (recoveryCountingAdapter) CleanupTask(context.Context, downloadtask.TaskSnapshot, downloadtask.StopReason) error {
	return nil
}

func sha256Hex(payload []byte) string {
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}

func newPublishExecution(t *testing.T, payload []byte) (*DownloadManager, *DownloadTask, *managerExecutionContext, string, string) {
	t.Helper()
	directory := t.TempDir()
	dm := NewDownloadManager(directory, 1)
	dm.SetStatePath(filepath.Join(directory, "downloads.v2.json"))
	policy, err := dm.outputAllocator.Reserve("task-1", directory, "video.mp4", downloadtask.ConflictStrategyAutoRename)
	if err != nil {
		t.Fatal(err)
	}
	task := &DownloadTask{
		ID:                  "task-1",
		PlatformID:          string(downloadtask.PlatformGeneric),
		PlatformDataVersion: 1,
		PlatformData:        []byte(`{"url":"https://example.com/video"}`),
		OutputPolicy:        policy,
		Status:              StatusDownloading,
	}
	task.mu.Lock()
	execution := newTaskExecutionLocked(task)
	task.mu.Unlock()
	dm.tasks[task.ID] = task
	if err := dm.SaveState(); err != nil {
		t.Fatal(err)
	}
	temporary := filepath.Join(directory, "payload.part")
	if err := os.WriteFile(temporary, payload, 0600); err != nil {
		t.Fatal(err)
	}
	return dm, task, newManagerExecutionContext(dm, task, execution), temporary, policy.PlannedFinalPath
}

func TestPublishFinalRequiresExactSHA256AndPositiveSize(t *testing.T) {
	payload := []byte("verified payload")
	dm, task, execution, temporary, final := newPublishExecution(t, payload)
	for _, draft := range []downloadtask.TaskArtifactDraft{
		{Size: int64(len(payload)), SHA256: "", Primary: true},
		{Size: int64(len(payload)), SHA256: "not-a-sha", Primary: true},
		{Size: 0, SHA256: sha256Hex(payload), Primary: true},
	} {
		if _, err := execution.PublishFinal(context.Background(), temporary, draft); err == nil {
			t.Fatalf("invalid draft was accepted: %#v", draft)
		}
	}
	artifact, err := execution.PublishFinal(context.Background(), temporary, downloadtask.TaskArtifactDraft{
		Size:    int64(len(payload)),
		SHA256:  sha256Hex(payload),
		Primary: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if artifact.Path != final || artifact.Kind != downloadtask.TaskArtifactFinal {
		t.Fatalf("unexpected published artifact: %#v", artifact)
	}
	if _, err := os.Stat(final); err != nil {
		t.Fatal(err)
	}
	task.mu.RLock()
	defer task.mu.RUnlock()
	if task.PublishIntent != nil || !hasPrimaryFinalArtifact(task.Artifacts) {
		t.Fatalf("publish did not atomically clear intent/add artifact: intent=%#v artifacts=%#v", task.PublishIntent, task.Artifacts)
	}
	_ = dm
}

func TestPostPublishCleanupFailurePersistsWithoutReopeningMutationSinks(t *testing.T) {
	payload := []byte("published with a leftover sidecar")
	dm, task, execution, temporary, _ := newPublishExecution(t, payload)
	if _, err := execution.PublishFinal(context.Background(), temporary, downloadtask.TaskArtifactDraft{
		Size:    int64(len(payload)),
		SHA256:  sha256Hex(payload),
		Primary: true,
	}); err != nil {
		t.Fatal(err)
	}

	leftover := filepath.Join(filepath.Dir(temporary), ".resume-state")
	cleanupArtifact := downloadtask.TaskArtifact{
		Kind: downloadtask.TaskArtifactTemporary,
		Path: leftover,
		Metadata: map[string]string{
			"platform":     string(downloadtask.PlatformGeneric),
			"cleanupError": "directory is not empty",
		},
	}
	if err := execution.RecordPostPublishCleanupFailure(cleanupArtifact); err != nil {
		t.Fatal(err)
	}
	if err := execution.RecordArtifact(downloadtask.TaskArtifact{
		Kind: downloadtask.TaskArtifactTemporary,
		Path: filepath.Join(filepath.Dir(temporary), ".ordinary-late-artifact"),
	}); !errors.Is(err, ErrStaleExecution) {
		t.Fatalf("ordinary mutation error=%v, want ErrStaleExecution", err)
	}

	task.mu.RLock()
	status := task.Status
	artifacts := downloadtask.CloneSnapshot(downloadtask.TaskSnapshot{Artifacts: task.Artifacts}).Artifacts
	mutationOpen := task.execution.mutationOpen
	task.mu.RUnlock()
	if status != StatusCompleted || mutationOpen {
		t.Fatalf("cleanup diagnostic changed completion boundary: status=%s mutationOpen=%v", status, mutationOpen)
	}
	if len(artifacts) != 2 || artifacts[1].Path != leftover || artifacts[1].Metadata["cleanupError"] == "" {
		t.Fatalf("cleanup diagnostic artifact=%#v", artifacts)
	}
	publicArtifacts := task.PublicSnapshot().Artifacts
	if len(publicArtifacts) != 2 || !publicArtifacts[1].CleanupFailed {
		t.Fatalf("cleanup diagnostic is not observable in public snapshot: %#v", publicArtifacts)
	}

	restarted := NewDownloadManager(filepath.Dir(dm.StatePath()), 1)
	restarted.SetStatePath(dm.StatePath())
	if err := restarted.RegisterPlatformAdapter(NewGenericAdapter()); err != nil {
		t.Fatal(err)
	}
	if err := restarted.LoadState(); err != nil {
		t.Fatal(err)
	}
	recovered, err := restarted.GetTask(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	recovered.mu.RLock()
	recoveredArtifacts := downloadtask.CloneSnapshot(downloadtask.TaskSnapshot{Artifacts: recovered.Artifacts}).Artifacts
	recovered.mu.RUnlock()
	if len(recoveredArtifacts) != 2 || recoveredArtifacts[1].Path != leftover ||
		recoveredArtifacts[1].Metadata["cleanupError"] != "directory is not empty" {
		t.Fatalf("cleanup diagnostic was not durable: %#v", recoveredArtifacts)
	}
}

func TestPostPublishCleanupFailureRequiresExplicitDiagnosticMetadata(t *testing.T) {
	payload := []byte("published artifact")
	_, _, execution, temporary, _ := newPublishExecution(t, payload)
	if _, err := execution.PublishFinal(context.Background(), temporary, downloadtask.TaskArtifactDraft{
		Size:    int64(len(payload)),
		SHA256:  sha256Hex(payload),
		Primary: true,
	}); err != nil {
		t.Fatal(err)
	}
	err := execution.RecordPostPublishCleanupFailure(downloadtask.TaskArtifact{
		Kind: downloadtask.TaskArtifactTemporary,
		Path: filepath.Join(filepath.Dir(temporary), ".unclassified-sidecar"),
	})
	if err == nil || !strings.Contains(err.Error(), "cleanupError") {
		t.Fatalf("missing cleanup diagnostic metadata error=%v", err)
	}
}

func TestPrimaryPublishCannotBeDowngradedByPostPublishErrorOrMutation(t *testing.T) {
	directory := t.TempDir()
	dm := NewDownloadManager(directory, 1)
	dm.SetStatePath(filepath.Join(directory, "downloads.v2.json"))
	lateErrors := make(chan []error, 1)
	adapter := postPublishErrorAdapter{id: "post-publish", lateErrors: lateErrors}
	if err := dm.RegisterPlatformAdapter(adapter); err != nil {
		t.Fatal(err)
	}
	completed := make(chan struct{}, 1)
	errored := make(chan error, 1)
	dm.SetCompleteCallback(func(*DownloadTask) { completed <- struct{}{} })
	dm.SetErrorCallback(func(_ *DownloadTask, err error) { errored <- err })
	platformData := json.RawMessage(`{"source":"persisted"}`)
	task, err := dm.CreateAndStartTask(TaskCreationInput{
		ID: "post-publish", PlatformID: adapter.id, Title: "Post publish",
		SuggestedFilename: "post-publish", SuggestedExtension: ".mp4",
		PlatformDataVersion: 1, PlatformData: platformData,
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-completed:
	case err := <-errored:
		t.Fatalf("durable completion was downgraded to error: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("completion callback timed out")
	}
	for _, lateErr := range <-lateErrors {
		if !errors.Is(lateErr, ErrStaleExecution) {
			t.Fatalf("late mutation error=%v, want ErrStaleExecution", lateErr)
		}
	}
	task.mu.RLock()
	status := task.Status
	checkpoint := task.PlatformCheckpoint
	progress := task.ProgressSummary
	task.mu.RUnlock()
	if status != StatusCompleted || checkpoint != nil || progress.CurrentStage != "completed" || progress.Percent != 100 {
		t.Fatalf("post-publish task status=%s checkpoint=%#v progress=%#v", status, checkpoint, progress)
	}

	restarted := NewDownloadManager(directory, 1)
	restarted.SetStatePath(dm.StatePath())
	if err := restarted.RegisterPlatformAdapter(postPublishErrorAdapter{id: adapter.id, lateErrors: make(chan []error, 1)}); err != nil {
		t.Fatal(err)
	}
	if err := restarted.LoadState(); err != nil {
		t.Fatal(err)
	}
	recovered, err := restarted.GetTask(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if recoveredStatus := taskSnapshot(recovered).Status; recoveredStatus != downloadtask.StatusCompleted {
		t.Fatalf("durable status=%s, want completed", recoveredStatus)
	}
}

func TestLoadStateTreatsRunningTaskWithPrimaryFinalAsCompletedWithoutExecutingAdapter(t *testing.T) {
	directory := t.TempDir()
	statePath := filepath.Join(directory, "downloads.v2.json")
	finalPath := filepath.Join(directory, "already-published.mp4")
	payload := []byte("already published")
	if err := os.WriteFile(finalPath, payload, 0600); err != nil {
		t.Fatal(err)
	}
	reservationKey, canonicalFinalPath, err := canonicalOutputPath(finalPath)
	if err != nil {
		t.Fatal(err)
	}
	finalPath = canonicalFinalPath
	artifactCreatedAt := int64(1_723_456_999)
	snapshot := downloadtask.TaskSnapshot{
		ID:                  "published-running",
		PlatformID:          downloadtask.PlatformGeneric,
		PlatformDataVersion: 1,
		PlatformData:        json.RawMessage(`{"url":"https://example.com/video"}`),
		Status:              downloadtask.StatusRunning,
		OutputPolicy: downloadtask.OutputPolicy{
			Directory:        directory,
			PlannedFilename:  filepath.Base(finalPath),
			PlannedFinalPath: finalPath,
			ReservationKey:   reservationKey,
			ConflictStrategy: downloadtask.ConflictStrategyAutoRename,
		},
		Progress: downloadtask.TaskProgressSummary{Percent: 99},
		Artifacts: []downloadtask.TaskArtifact{{
			ID:        "primary",
			Kind:      downloadtask.TaskArtifactFinal,
			Path:      finalPath,
			Size:      int64(len(payload)),
			Primary:   true,
			CreatedAt: artifactCreatedAt,
		}},
	}
	if err := NewTaskStore(statePath).Save(context.Background(), TaskStoreEnvelope{
		SchemaVersion: TaskStoreSchemaVersion,
		Revision:      1,
		Tasks:         []downloadtask.TaskSnapshot{snapshot},
	}); err != nil {
		t.Fatal(err)
	}

	var runs atomic.Int32
	restarted := NewDownloadManager(directory, 1)
	restarted.SetStatePath(statePath)
	if err := restarted.RegisterPlatformAdapter(recoveryCountingAdapter{runs: &runs}); err != nil {
		t.Fatal(err)
	}
	if err := restarted.LoadState(); err != nil {
		t.Fatal(err)
	}
	recovered, err := restarted.GetTask(snapshot.ID)
	if err != nil {
		t.Fatal(err)
	}
	recovered.mu.RLock()
	status := recovered.Status
	progress := recovered.ProgressSummary
	completedAt := recovered.CompletedAt
	recovered.mu.RUnlock()
	if status != StatusCompleted || progress.Percent != 100 || progress.CurrentStage != "completed" || completedAt != artifactCreatedAt {
		t.Fatalf("recovered task status=%s progress=%#v completedAt=%d", status, progress, completedAt)
	}
	if err := restarted.StartTask(snapshot.ID); err == nil {
		t.Fatal("completed recovery unexpectedly allowed adapter execution")
	}
	if got := runs.Load(); got != 0 {
		t.Fatalf("adapter runs=%d, want 0", got)
	}
}

func TestPrimaryPublishFinalPersistsArtifactAndCompletionInSameRevision(t *testing.T) {
	payload := []byte("atomic primary completion")
	dm, _, execution, temporary, _ := newPublishExecution(t, payload)
	realWrite := dm.taskStore.writeFile
	commits := make([]TaskStoreEnvelope, 0, 2)
	dm.taskStore.writeFile = func(ctx context.Context, destination string, data []byte) error {
		if destination == dm.StatePath() {
			var envelope TaskStoreEnvelope
			if err := json.Unmarshal(data, &envelope); err != nil {
				t.Fatalf("decode persisted envelope: %v", err)
			}
			commits = append(commits, envelope)
		}
		return realWrite(ctx, destination, data)
	}

	if _, err := execution.PublishFinal(context.Background(), temporary, downloadtask.TaskArtifactDraft{
		Size:    int64(len(payload)),
		SHA256:  sha256Hex(payload),
		Primary: true,
	}); err != nil {
		t.Fatal(err)
	}

	var intentRevision, completionRevision uint64
	for _, envelope := range commits {
		if len(envelope.Tasks) != 1 {
			t.Fatalf("revision %d tasks=%d, want 1", envelope.Revision, len(envelope.Tasks))
		}
		snapshot := envelope.Tasks[0]
		if snapshot.PublishIntent != nil {
			intentRevision = envelope.Revision
		}
		if !hasPrimaryFinalArtifact(snapshot.Artifacts) {
			continue
		}
		completionRevision = envelope.Revision
		if snapshot.Status != downloadtask.StatusCompleted || snapshot.CompletedAt == 0 || snapshot.Progress.Percent != 100 || snapshot.PublishIntent != nil {
			t.Fatalf("primary artifact and completed state diverged at revision %d: %#v", envelope.Revision, snapshot)
		}
	}
	if intentRevision == 0 || completionRevision == 0 || completionRevision <= intentRevision {
		t.Fatalf("persisted revisions: intent=%d completion=%d commits=%#v", intentRevision, completionRevision, commits)
	}
	if got := dm.taskStore.CommittedRevision(); got != completionRevision {
		t.Fatalf("committed revision=%d, completion revision=%d", got, completionRevision)
	}
}

func TestPublishFinalKeepsIntentWhenArtifactSnapshotPersistenceFails(t *testing.T) {
	payload := []byte("crash-window payload")
	dm, task, execution, temporary, final := newPublishExecution(t, payload)
	realWrite := dm.taskStore.writeFile
	mainWrites := 0
	dm.taskStore.writeFile = func(ctx context.Context, destination string, data []byte) error {
		if destination == dm.StatePath() {
			mainWrites++
			if mainWrites == 2 {
				return errors.New("injected artifact snapshot failure")
			}
		}
		return realWrite(ctx, destination, data)
	}
	_, err := execution.PublishFinal(context.Background(), temporary, downloadtask.TaskArtifactDraft{
		Size:    int64(len(payload)),
		SHA256:  sha256Hex(payload),
		Primary: true,
	})
	if err == nil {
		t.Fatal("expected artifact snapshot persistence failure")
	}
	if _, err := os.Stat(final); err != nil {
		t.Fatalf("final file did not reach publish commit point: %v", err)
	}
	task.mu.RLock()
	intent := clonePublishIntent(task.PublishIntent)
	artifacts := append([]downloadtask.TaskArtifact(nil), task.Artifacts...)
	task.mu.RUnlock()
	if intent == nil || hasPrimaryFinalArtifact(artifacts) {
		t.Fatalf("failed artifact snapshot lost explainable intent: intent=%#v artifacts=%#v", intent, artifacts)
	}

	restarted := NewDownloadManager(filepath.Dir(final), 1)
	restarted.SetStatePath(dm.StatePath())
	if err := restarted.RegisterPlatformAdapter(NewGenericAdapter()); err != nil {
		t.Fatal(err)
	}
	if err := restarted.LoadState(); err != nil {
		t.Fatal(err)
	}
	recovered, err := restarted.GetTask(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	recovered.mu.RLock()
	defer recovered.mu.RUnlock()
	if recovered.Status != StatusCompleted || recovered.PublishIntent != nil || !hasPrimaryFinalArtifact(recovered.Artifacts) {
		t.Fatalf("restart did not recover published final: status=%s intent=%#v artifacts=%#v", recovered.Status, recovered.PublishIntent, recovered.Artifacts)
	}
}

func TestPublishRecoveryTreatsFinalPlusTemporaryAsExternalCollision(t *testing.T) {
	directory := t.TempDir()
	dm := NewDownloadManager(directory, 1)
	dm.SetStatePath(filepath.Join(directory, "downloads.v2.json"))
	policy, err := dm.outputAllocator.Reserve("task-1", directory, "video.mp4", downloadtask.ConflictStrategyAutoRename)
	if err != nil {
		t.Fatal(err)
	}
	temporary := filepath.Join(directory, ".task-1.part")
	payload := []byte("our validated temporary")
	if err := os.WriteFile(temporary, payload, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(policy.PlannedFinalPath, []byte("external occupant"), 0600); err != nil {
		t.Fatal(err)
	}
	task := &DownloadTask{
		ID:                  "task-1",
		PlatformID:          string(downloadtask.PlatformGeneric),
		PlatformDataVersion: 1,
		PlatformData:        []byte(`{"url":"https://example.com/video"}`),
		OutputPolicy:        policy,
		Status:              StatusPaused,
		PublishIntent: &downloadtask.PublishIntent{
			Generation:       1,
			TemporaryPath:    temporary,
			PlannedFinalPath: policy.PlannedFinalPath,
			Draft: downloadtask.TaskArtifactDraft{
				ID:      "primary",
				Size:    int64(len(payload)),
				SHA256:  sha256Hex(payload),
				Primary: true,
			},
		},
	}
	dm.tasks[task.ID] = task
	if err := dm.SaveState(); err != nil {
		t.Fatal(err)
	}

	restarted := NewDownloadManager(directory, 1)
	restarted.SetStatePath(dm.StatePath())
	if err := restarted.RegisterPlatformAdapter(NewGenericAdapter()); err != nil {
		t.Fatal(err)
	}
	if err := restarted.LoadState(); err != nil {
		t.Fatal(err)
	}
	external, err := os.ReadFile(policy.PlannedFinalPath)
	if err != nil || string(external) != "external occupant" {
		t.Fatalf("external final changed: %q err=%v", external, err)
	}
	recovered, err := restarted.GetTask(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	recovered.mu.RLock()
	reallocatedPath := recovered.OutputPolicy.PlannedFinalPath
	status := recovered.Status
	intent := recovered.PublishIntent
	recovered.mu.RUnlock()
	if reallocatedPath == policy.PlannedFinalPath || !strings.Contains(filepath.Base(reallocatedPath), "(1)") {
		t.Fatalf("recovery did not reallocate occupied final path: old=%s new=%s", policy.PlannedFinalPath, reallocatedPath)
	}
	reallocatedBytes, err := os.ReadFile(reallocatedPath)
	if err != nil || string(reallocatedBytes) != string(payload) {
		t.Fatalf("reallocated final mismatch: %q err=%v", reallocatedBytes, err)
	}
	if status != StatusCompleted || intent != nil {
		t.Fatalf("recovery status=%s intent=%#v", status, intent)
	}
}

func TestLoadStateRestoresAllReservationsBeforePublishIntentRecovery(t *testing.T) {
	directory := t.TempDir()
	statePath := filepath.Join(directory, "downloads.v2.json")
	policyFor := func(filename string) downloadtask.OutputPolicy {
		t.Helper()
		key, absolute, err := canonicalOutputPath(filepath.Join(directory, filename))
		if err != nil {
			t.Fatal(err)
		}
		return downloadtask.OutputPolicy{
			Directory:        directory,
			PlannedFilename:  filename,
			PlannedFinalPath: absolute,
			ReservationKey:   key,
			ConflictStrategy: downloadtask.ConflictStrategyAutoRename,
		}
	}

	earlierPolicy := policyFor("video.mp4")
	laterPolicy := policyFor("video (1).mp4")
	externalPayload := []byte("external occupant")
	if err := os.WriteFile(earlierPolicy.PlannedFinalPath, externalPayload, 0600); err != nil {
		t.Fatal(err)
	}
	publishPayload := []byte("earlier task temporary")
	temporaryPath := filepath.Join(directory, ".earlier.publish.part")
	if err := os.WriteFile(temporaryPath, publishPayload, 0600); err != nil {
		t.Fatal(err)
	}
	earlier := downloadtask.TaskSnapshot{
		ID:                  "earlier-with-intent",
		PlatformID:          downloadtask.PlatformGeneric,
		PlatformDataVersion: 1,
		PlatformData:        json.RawMessage(`{"url":"https://example.com/earlier"}`),
		Status:              downloadtask.StatusPaused,
		OutputPolicy:        earlierPolicy,
		PublishIntent: &downloadtask.PublishIntent{
			Generation:       1,
			TemporaryPath:    temporaryPath,
			PlannedFinalPath: earlierPolicy.PlannedFinalPath,
			Draft: downloadtask.TaskArtifactDraft{
				ID:      "earlier-primary",
				Size:    int64(len(publishPayload)),
				SHA256:  sha256Hex(publishPayload),
				Primary: true,
			},
		},
	}
	later := downloadtask.TaskSnapshot{
		ID:                  "later-reservation",
		PlatformID:          downloadtask.PlatformGeneric,
		PlatformDataVersion: 1,
		PlatformData:        json.RawMessage(`{"url":"https://example.com/later"}`),
		Status:              downloadtask.StatusPaused,
		OutputPolicy:        laterPolicy,
	}
	if err := NewTaskStore(statePath).Save(context.Background(), TaskStoreEnvelope{
		SchemaVersion: TaskStoreSchemaVersion,
		Revision:      1,
		// Envelope order is intentional: the earlier intent must not reallocate
		// onto the later task's persisted path before that path is restored.
		Tasks: []downloadtask.TaskSnapshot{earlier, later},
	}); err != nil {
		t.Fatal(err)
	}

	restarted := NewDownloadManager(directory, 1)
	restarted.SetStatePath(statePath)
	if err := restarted.RegisterPlatformAdapter(NewGenericAdapter()); err != nil {
		t.Fatal(err)
	}
	if err := restarted.LoadState(); err != nil {
		t.Fatal(err)
	}
	recoveredEarlier, err := restarted.GetTask(earlier.ID)
	if err != nil {
		t.Fatal(err)
	}
	recoveredLater, err := restarted.GetTask(later.ID)
	if err != nil {
		t.Fatal(err)
	}
	recoveredEarlier.mu.RLock()
	earlierFinalPath := recoveredEarlier.OutputPolicy.PlannedFinalPath
	earlierStatus := recoveredEarlier.Status
	recoveredEarlier.mu.RUnlock()
	recoveredLater.mu.RLock()
	laterFinalPath := recoveredLater.OutputPolicy.PlannedFinalPath
	laterStatus := recoveredLater.Status
	recoveredLater.mu.RUnlock()

	if filepath.Base(earlierFinalPath) != "video (2).mp4" || earlierStatus != StatusCompleted {
		t.Fatalf("earlier recovery path=%s status=%s, want video (2).mp4/completed", earlierFinalPath, earlierStatus)
	}
	if laterFinalPath != laterPolicy.PlannedFinalPath || laterStatus != StatusPaused {
		t.Fatalf("later reservation changed: path=%s status=%s, want %s/paused", laterFinalPath, laterStatus, laterPolicy.PlannedFinalPath)
	}
	if owner, ok := restarted.outputAllocator.Owner(laterPolicy.PlannedFinalPath); !ok || owner != later.ID {
		t.Fatalf("later persisted path owner=%q ok=%v, want %q", owner, ok, later.ID)
	}
	if got, err := os.ReadFile(earlierPolicy.PlannedFinalPath); err != nil || string(got) != string(externalPayload) {
		t.Fatalf("external occupant changed: bytes=%q err=%v", got, err)
	}
	if got, err := os.ReadFile(earlierFinalPath); err != nil || string(got) != string(publishPayload) {
		t.Fatalf("recovered publish bytes=%q err=%v", got, err)
	}
}

func TestPublishRecoveryRejectsMissingHashAndTemporaryMismatch(t *testing.T) {
	for _, testCase := range []struct {
		name string
		hash string
		size int64
	}{
		{name: "missing hash", hash: "", size: 7},
		{name: "hash mismatch", hash: strings.Repeat("a", 64), size: 7},
		{name: "size mismatch", hash: sha256Hex([]byte("payload")), size: 8},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			directory := t.TempDir()
			dm := NewDownloadManager(directory, 1)
			dm.SetStatePath(filepath.Join(directory, "downloads.v2.json"))
			policy, err := dm.outputAllocator.Reserve("task-1", directory, "video.mp4", downloadtask.ConflictStrategyAutoRename)
			if err != nil {
				t.Fatal(err)
			}
			temporary := filepath.Join(directory, ".task-1.part")
			if err := os.WriteFile(temporary, []byte("payload"), 0600); err != nil {
				t.Fatal(err)
			}
			dm.tasks["task-1"] = &DownloadTask{
				ID:                  "task-1",
				PlatformID:          string(downloadtask.PlatformGeneric),
				PlatformDataVersion: 1,
				PlatformData:        []byte(`{"url":"https://example.com/video"}`),
				OutputPolicy:        policy,
				Status:              StatusPaused,
				PublishIntent: &downloadtask.PublishIntent{
					Generation:       1,
					TemporaryPath:    temporary,
					PlannedFinalPath: policy.PlannedFinalPath,
					Draft: downloadtask.TaskArtifactDraft{
						ID:      "primary",
						Size:    testCase.size,
						SHA256:  testCase.hash,
						Primary: true,
					},
				},
			}
			if err := dm.SaveState(); err != nil {
				t.Fatal(err)
			}
			restarted := NewDownloadManager(directory, 1)
			restarted.SetStatePath(dm.StatePath())
			if err := restarted.RegisterPlatformAdapter(NewGenericAdapter()); err != nil {
				t.Fatal(err)
			}
			if err := restarted.LoadState(); err != nil {
				t.Fatal(err)
			}
			recovered, err := restarted.GetTask("task-1")
			if err != nil {
				t.Fatal(err)
			}
			recovered.mu.RLock()
			defer recovered.mu.RUnlock()
			if recovered.Status != StatusFailed || recovered.LastErrorDetail == nil || recovered.LastErrorDetail.Code != "task.publish_recovery_failed" {
				t.Fatalf("invalid intent was not failed closed: status=%s error=%#v", recovered.Status, recovered.LastErrorDetail)
			}
			if _, err := os.Stat(policy.PlannedFinalPath); !os.IsNotExist(err) {
				t.Fatalf("invalid temporary was published: %v", err)
			}
			if owner, reserved := restarted.outputAllocator.Owner(policy.PlannedFinalPath); reserved {
				t.Fatalf("failed publish recovery leaked reservation owned by %s", owner)
			}
		})
	}
}
