package downloader

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	downloadtask "EasyDownload/internal/download/task"
)

func TestCreateTaskUsesCustomOutputDirectoryAndFailsClosed(t *testing.T) {
	dm := NewDownloadManager(filepath.Join(t.TempDir(), "default"), 1)
	platformData, err := MarshalGenericPlatformData("https://example.com/video.mp4", nil)
	if err != nil {
		t.Fatal(err)
	}
	input := TaskCreationInput{
		ID:                  "custom-output",
		PlatformID:          downloadtask.PlatformGeneric,
		Title:               "Video",
		DisplaySource:       "Generic",
		OutputDirectory:     filepath.Join(t.TempDir(), "custom", "nested"),
		SuggestedFilename:   "video",
		SuggestedExtension:  ".mp4",
		PlatformDataVersion: 1,
		PlatformData:        platformData,
	}
	if _, err := dm.CreateTask(input); err == nil {
		t.Fatal("unregistered generic adapter creation did not fail closed")
	}
	if err := dm.RegisterPlatformAdapter(NewGenericAdapter()); err != nil {
		t.Fatal(err)
	}
	task, err := dm.CreateTask(input)
	if err != nil {
		t.Fatal(err)
	}
	wantDirectory, err := filepath.Abs(input.OutputDirectory)
	if err != nil {
		t.Fatal(err)
	}
	if task.OutputPolicy.Directory != wantDirectory || filepath.Dir(task.OutputPolicy.PlannedFinalPath) != wantDirectory {
		t.Fatalf("custom output policy=%#v, want directory %s", task.OutputPolicy, wantDirectory)
	}
	if info, err := os.Stat(wantDirectory); err != nil || !info.IsDir() {
		t.Fatalf("custom output directory missing: info=%v err=%v", info, err)
	}
}

func TestCreateTaskRejectsInvalidOutputDirectory(t *testing.T) {
	dm := NewDownloadManager(t.TempDir(), 1)
	if err := dm.RegisterPlatformAdapter(NewGenericAdapter()); err != nil {
		t.Fatal(err)
	}
	platformData, err := MarshalGenericPlatformData("https://example.com/video.mp4", nil)
	if err != nil {
		t.Fatal(err)
	}
	notDirectory := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(notDirectory, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err = dm.CreateTask(TaskCreationInput{
		ID:                  "bad-output",
		PlatformID:          downloadtask.PlatformGeneric,
		Title:               "Video",
		OutputDirectory:     notDirectory,
		SuggestedFilename:   "video",
		SuggestedExtension:  ".mp4",
		PlatformDataVersion: 1,
		PlatformData:        platformData,
	})
	if err == nil {
		t.Fatal("invalid output directory was accepted")
	}
}

func TestCreateTaskRollsBackMapAndReservationWhenPersistenceFails(t *testing.T) {
	directory := t.TempDir()
	dm := NewDownloadManager(directory, 1)
	if err := dm.RegisterPlatformAdapter(NewGenericAdapter()); err != nil {
		t.Fatal(err)
	}
	dm.SetStatePath(filepath.Join(directory, "downloads.v2.json"))
	dm.taskStore.writeFile = func(context.Context, string, []byte) error {
		return errors.New("injected persistence failure")
	}
	platformData, err := MarshalGenericPlatformData("https://example.com/video.mp4", nil)
	if err != nil {
		t.Fatal(err)
	}
	input := TaskCreationInput{
		ID:                  "rollback",
		PlatformID:          downloadtask.PlatformGeneric,
		Title:               "Video",
		SuggestedFilename:   "video",
		SuggestedExtension:  ".mp4",
		PlatformDataVersion: 1,
		PlatformData:        platformData,
	}
	if _, err := dm.CreateTask(input); err == nil {
		t.Fatal("expected persistence failure")
	}
	if _, err := dm.GetTask(input.ID); err == nil {
		t.Fatal("failed creation remained visible in task map")
	}
	planned := filepath.Join(directory, "video.mp4")
	if owner, ok := dm.outputAllocator.Owner(planned); ok {
		t.Fatalf("failed creation retained output reservation for %s", owner)
	}

	// A healthy retry can claim the original filename, proving rollback was
	// complete rather than leaking a hidden reservation.
	dm.taskStore.writeFile = writeAtomicFile
	task, err := dm.CreateTask(input)
	if err != nil {
		t.Fatal(err)
	}
	if task.OutputPolicy.PlannedFilename != "video.mp4" {
		t.Fatalf("retry filename=%q, want video.mp4", task.OutputPolicy.PlannedFilename)
	}
}
