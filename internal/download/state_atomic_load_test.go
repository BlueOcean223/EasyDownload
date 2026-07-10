package downloader

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	downloadtask "EasyDownload/internal/download/task"
)

func TestLoadStateRejectsInvalidBatchWithoutPartialMutation(t *testing.T) {
	directory := t.TempDir()
	statePath := filepath.Join(directory, "downloads.v2.json")
	validPath := filepath.Join(directory, "valid.mp4")
	valid := downloadtask.TaskSnapshot{
		ID: "valid", PlatformID: downloadtask.PlatformGeneric,
		PlatformDataVersion: 1, PlatformData: json.RawMessage(`{"url":"https://example.com/video"}`),
		Status: downloadtask.StatusPaused,
		OutputPolicy: downloadtask.OutputPolicy{
			Directory: directory, PlannedFilename: "valid.mp4", PlannedFinalPath: validPath,
			ConflictStrategy: downloadtask.ConflictStrategyAutoRename,
		},
	}
	invalid := valid
	invalid.ID = ""
	if err := NewTaskStore(statePath).Save(context.Background(), TaskStoreEnvelope{
		SchemaVersion: TaskStoreSchemaVersion, Revision: 1,
		Tasks: []downloadtask.TaskSnapshot{valid, invalid},
	}); err != nil {
		t.Fatal(err)
	}

	dm := NewDownloadManager(directory, 1)
	dm.SetStatePath(statePath)
	if err := dm.LoadState(); err == nil {
		t.Fatal("invalid task batch unexpectedly loaded")
	}
	if tasks := dm.GetAllTasks(); len(tasks) != 0 {
		t.Fatalf("invalid batch partially mutated manager: %#v", tasks)
	}
	if owner, reserved := dm.outputAllocator.Owner(validPath); reserved {
		t.Fatalf("invalid batch leaked reservation owned by %s", owner)
	}
}

func TestLoadStateMakesDurablePendingTaskExplicitlyResumable(t *testing.T) {
	directory := t.TempDir()
	statePath := filepath.Join(directory, "downloads.v2.json")
	source := NewDownloadManager(directory, 1)
	if err := source.RegisterPlatformAdapter(NewGenericAdapter()); err != nil {
		t.Fatal(err)
	}
	source.SetStatePath(statePath)
	data, err := MarshalGenericPlatformData("https://example.com/video.mp4", nil)
	if err != nil {
		t.Fatal(err)
	}
	created, err := source.CreateTask(TaskCreationInput{
		ID: "durable-pending", PlatformID: downloadtask.PlatformGeneric,
		Title: "Pending", SuggestedFilename: "pending", SuggestedExtension: ".mp4",
		PlatformDataVersion: 1, PlatformData: data,
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.Status != StatusPending {
		t.Fatalf("source status=%s, want pending", created.Status)
	}

	restarted := NewDownloadManager(directory, 1)
	if err := restarted.RegisterPlatformAdapter(NewGenericAdapter()); err != nil {
		t.Fatal(err)
	}
	restarted.SetStatePath(statePath)
	if err := restarted.LoadState(); err != nil {
		t.Fatal(err)
	}
	recovered, err := restarted.GetTask(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	recovered.mu.RLock()
	status := recovered.Status
	stage := recovered.ProgressSummary.CurrentStage
	recovered.mu.RUnlock()
	if status != StatusPaused || stage != "paused" {
		t.Fatalf("recovered pending task status=%s stage=%s, want paused/paused", status, stage)
	}
}
