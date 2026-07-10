package downloader

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	downloadtask "EasyDownload/internal/download/task"
)

type checkpointRecordingAdapter struct {
	id         downloadtask.PlatformID
	checkpoint downloadtask.PlatformCheckpointEnvelope
	recorded   chan error
}

func (adapter checkpointRecordingAdapter) ID() downloadtask.PlatformID          { return adapter.id }
func (checkpointRecordingAdapter) ValidateTask(downloadtask.TaskSnapshot) error { return nil }
func (adapter checkpointRecordingAdapter) RunTask(ctx context.Context, _ downloadtask.TaskSnapshot, execution downloadtask.TaskExecutionContext) error {
	err := execution.RecordCheckpoint(adapter.checkpoint)
	adapter.recorded <- err
	if err != nil {
		return err
	}
	<-ctx.Done()
	return ctx.Err()
}
func (checkpointRecordingAdapter) CleanupTask(context.Context, downloadtask.TaskSnapshot, downloadtask.StopReason) error {
	return nil
}

type persistedPayload struct {
	Payload string `json:"payload"`
}

type persistedSnapshotPublishingAdapter struct {
	id       downloadtask.PlatformID
	received chan downloadtask.TaskSnapshot
}

func (adapter persistedSnapshotPublishingAdapter) ID() downloadtask.PlatformID { return adapter.id }
func (persistedSnapshotPublishingAdapter) ValidateTask(snapshot downloadtask.TaskSnapshot) error {
	if snapshot.PlatformDataVersion != 1 {
		return fmt.Errorf("unsupported platform data version %d", snapshot.PlatformDataVersion)
	}
	_, err := decodePersistedPayload(snapshot.PlatformData)
	return err
}
func (adapter persistedSnapshotPublishingAdapter) RunTask(ctx context.Context, snapshot downloadtask.TaskSnapshot, execution downloadtask.TaskExecutionContext) error {
	data, err := decodePersistedPayload(snapshot.PlatformData)
	if err != nil {
		return err
	}
	if adapter.received != nil {
		adapter.received <- downloadtask.CloneSnapshot(snapshot)
	}
	temporaryPath := filepath.Join(snapshot.OutputPolicy.Directory, "."+snapshot.ID+".v2-restore.tmp")
	payload := []byte(data.Payload)
	if err := os.WriteFile(temporaryPath, payload, 0600); err != nil {
		return err
	}
	digest := sha256.Sum256(payload)
	_, err = execution.PublishFinal(ctx, temporaryPath, downloadtask.TaskArtifactDraft{
		ID:        "primary",
		MediaType: "application/octet-stream",
		Size:      int64(len(payload)),
		SHA256:    hex.EncodeToString(digest[:]),
		Primary:   true,
	})
	return err
}
func (persistedSnapshotPublishingAdapter) CleanupTask(_ context.Context, snapshot downloadtask.TaskSnapshot, _ downloadtask.StopReason) error {
	path := filepath.Join(snapshot.OutputPolicy.Directory, "."+snapshot.ID+".v2-restore.tmp")
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func decodePersistedPayload(raw json.RawMessage) (persistedPayload, error) {
	var data persistedPayload
	if err := json.Unmarshal(raw, &data); err != nil {
		return data, err
	}
	if strings.TrimSpace(data.Payload) == "" {
		return data, errors.New("persisted payload is required")
	}
	return data, nil
}

func jsonSemanticallyEqual(left, right []byte) bool {
	var leftValue, rightValue any
	if json.Unmarshal(left, &leftValue) != nil || json.Unmarshal(right, &rightValue) != nil {
		return false
	}
	return reflect.DeepEqual(leftValue, rightValue)
}

func checkpointsSemanticallyEqual(left, right *downloadtask.PlatformCheckpointEnvelope) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.Version == right.Version && jsonSemanticallyEqual(left.Data, right.Data)
}

func TestRecordCheckpointCommitsToV2AndReachesRestartedAdapter(t *testing.T) {
	directory := t.TempDir()
	statePath := filepath.Join(directory, "downloads.v2.json")
	platformID := downloadtask.PlatformID("checkpoint-roundtrip")
	checkpoint := downloadtask.PlatformCheckpointEnvelope{
		Version: 3,
		Data:    json.RawMessage(`{"cursor":"opaque-resume-cursor","part":2}`),
	}
	recorded := make(chan error, 1)
	dm := NewDownloadManager(directory, 1)
	if err := dm.RegisterPlatformAdapter(checkpointRecordingAdapter{
		id: platformID, checkpoint: checkpoint, recorded: recorded,
	}); err != nil {
		t.Fatal(err)
	}
	dm.SetStatePath(statePath)
	platformData := json.RawMessage(`{"payload":"checkpoint restart payload"}`)
	task, err := createStrictTestTask(dm, "checkpoint-task", "", "Checkpoint", platformID, platformData)
	if err != nil {
		t.Fatal(err)
	}
	if err := dm.StartTask(task.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-recorded:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("checkpoint was not recorded")
	}

	// RecordCheckpoint returns only after the successful SaveState call, so a
	// separately opened v2 store must already contain the exact checkpoint.
	persisted, err := NewTaskStore(statePath).Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(persisted.Tasks) != 1 || !checkpointsSemanticallyEqual(persisted.Tasks[0].PlatformCheckpoint, &checkpoint) {
		t.Fatalf("checkpoint was not synchronously committed: %#v", persisted.Tasks)
	}

	pauseReceipt, err := dm.RequestPauseTask(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case terminal := <-pauseReceipt.completion:
		if terminal.ExecutionState != string(StopOperationCompleted) || terminal.EffectiveReason != downloadtask.StopReasonPause {
			t.Fatalf("unexpected pause terminal receipt: %#v", terminal)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("pause did not finish")
	}

	received := make(chan downloadtask.TaskSnapshot, 1)
	restarted := NewDownloadManager(directory, 1)
	if err := restarted.RegisterPlatformAdapter(persistedSnapshotPublishingAdapter{id: platformID, received: received}); err != nil {
		t.Fatal(err)
	}
	restarted.SetStatePath(statePath)
	if err := restarted.LoadState(); err != nil {
		t.Fatal(err)
	}
	completed := make(chan struct{})
	var completedOnce sync.Once
	restarted.SetCompleteCallback(func(*DownloadTask) { completedOnce.Do(func() { close(completed) }) })
	if err := restarted.ResumeTask(task.ID); err != nil {
		t.Fatal(err)
	}
	var adapterSnapshot downloadtask.TaskSnapshot
	select {
	case adapterSnapshot = <-received:
	case <-time.After(2 * time.Second):
		t.Fatal("restarted adapter did not run")
	}
	if !checkpointsSemanticallyEqual(adapterSnapshot.PlatformCheckpoint, &checkpoint) {
		t.Fatalf("restarted adapter received checkpoint=%#v, want=%#v", adapterSnapshot.PlatformCheckpoint, checkpoint)
	}
	select {
	case <-completed:
	case <-time.After(2 * time.Second):
		t.Fatal("restarted checkpoint task did not complete")
	}
	recovered, err := restarted.GetTask(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	recovered.mu.RLock()
	status := recovered.Status
	artifacts := append([]downloadtask.TaskArtifact(nil), recovered.Artifacts...)
	recovered.mu.RUnlock()
	if status != StatusCompleted || !hasPrimaryFinalArtifact(artifacts) {
		t.Fatalf("restarted task did not publish a primary final: status=%s artifacts=%#v", status, artifacts)
	}
}

func TestFreshManagerExecutesAndCompletesFromV2SnapshotOnly(t *testing.T) {
	directory := t.TempDir()
	statePath := filepath.Join(directory, "downloads.v2.json")
	platformID := downloadtask.PlatformID("snapshot-only")
	finalPath := filepath.Join(directory, "snapshot-only.bin")
	reservationKey, finalPath, err := canonicalOutputPath(finalPath)
	if err != nil {
		t.Fatal(err)
	}
	payload := "bytes reconstructed only from persisted PlatformData"
	snapshot := downloadtask.TaskSnapshot{
		ID:                  "snapshot-only-task",
		PlatformID:          platformID,
		Title:               "Snapshot only",
		DisplaySource:       "fixture",
		CreatedAt:           time.Now().Unix(),
		Status:              downloadtask.StatusPaused,
		PlatformDataVersion: 1,
		PlatformData:        json.RawMessage(`{"payload":"` + payload + `"}`),
		OutputPolicy: downloadtask.OutputPolicy{
			Directory:        directory,
			PlannedFilename:  filepath.Base(finalPath),
			PlannedFinalPath: finalPath,
			ReservationKey:   reservationKey,
			ConflictStrategy: downloadtask.ConflictStrategyAutoRename,
		},
		Progress: downloadtask.TaskProgressSummary{CurrentStage: "paused", StageLabel: "已暂停"},
	}
	if err := NewTaskStore(statePath).Save(context.Background(), TaskStoreEnvelope{
		SchemaVersion: TaskStoreSchemaVersion,
		Revision:      41,
		Tasks:         []downloadtask.TaskSnapshot{snapshot},
	}); err != nil {
		t.Fatal(err)
	}

	received := make(chan downloadtask.TaskSnapshot, 1)
	dm := NewDownloadManager(directory, 1)
	if err := dm.RegisterPlatformAdapter(persistedSnapshotPublishingAdapter{id: platformID, received: received}); err != nil {
		t.Fatal(err)
	}
	dm.SetStatePath(statePath)
	if err := dm.LoadState(); err != nil {
		t.Fatal(err)
	}
	completed := make(chan struct{})
	var completedOnce sync.Once
	dm.SetCompleteCallback(func(*DownloadTask) { completedOnce.Do(func() { close(completed) }) })
	if err := dm.ResumeTask(snapshot.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-received:
		if !jsonSemanticallyEqual(got.PlatformData, snapshot.PlatformData) || got.ID != snapshot.ID || got.PlatformID != platformID {
			t.Fatalf("adapter did not receive the persisted contract: %#v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("snapshot-only adapter did not run")
	}
	select {
	case <-completed:
	case <-time.After(2 * time.Second):
		t.Fatal("snapshot-only task did not complete")
	}
	if content, err := os.ReadFile(finalPath); err != nil || string(content) != payload {
		t.Fatalf("published final content=%q err=%v", content, err)
	}
	task, err := dm.GetTask(snapshot.ID)
	if err != nil {
		t.Fatal(err)
	}
	task.mu.RLock()
	status := task.Status
	artifacts := append([]downloadtask.TaskArtifact(nil), task.Artifacts...)
	task.mu.RUnlock()
	if status != StatusCompleted || !hasPrimaryFinalArtifact(artifacts) {
		t.Fatalf("snapshot-only execution did not complete: status=%s artifacts=%#v", status, artifacts)
	}
	persisted, err := NewTaskStore(statePath).Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(persisted.Tasks) != 1 || persisted.Tasks[0].Status != downloadtask.StatusCompleted || !hasPrimaryFinalArtifact(persisted.Tasks[0].Artifacts) {
		t.Fatalf("completed snapshot was not committed: %#v", persisted.Tasks)
	}
}
