package downloader

import (
	"path/filepath"
	"testing"
	"time"

	downloadtask "EasyDownload/internal/download/task"
)

func TestRuntimeConfigRollbackHidesCandidateDirectoryFromConcurrentCreate(t *testing.T) {
	oldDirectory := filepath.Join(t.TempDir(), "old")
	candidateDirectory := filepath.Join(t.TempDir(), "candidate")
	dm := NewDownloadManager(oldDirectory, 1)
	if err := dm.RegisterPlatformAdapter(NewGenericAdapter()); err != nil {
		t.Fatal(err)
	}
	update, err := dm.BeginRuntimeConfigUpdate(RuntimeConfigPatch{DownloadDir: &candidateDirectory})
	if err != nil {
		t.Fatal(err)
	}
	platformData, err := MarshalGenericPlatformData("https://example.com/video.mp4", nil)
	if err != nil {
		t.Fatal(err)
	}
	type creationResult struct {
		task *DownloadTask
		err  error
	}
	created := make(chan creationResult, 1)
	go func() {
		task, createErr := dm.CreateTask(TaskCreationInput{
			ID:                  "concurrent-create",
			PlatformID:          downloadtask.PlatformGeneric,
			Title:               "Video",
			SuggestedFilename:   "video",
			SuggestedExtension:  ".mp4",
			PlatformDataVersion: 1,
			PlatformData:        platformData,
		})
		created <- creationResult{task: task, err: createErr}
	}()
	select {
	case result := <-created:
		t.Fatalf("CreateTask observed uncommitted candidate: %#v", result)
	case <-time.After(30 * time.Millisecond):
	}
	if err := update.Rollback(); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-created:
		if result.err != nil {
			t.Fatal(result.err)
		}
		want, _ := filepath.Abs(oldDirectory)
		if result.task.OutputPolicy.Directory != want {
			t.Fatalf("created task used rolled-back directory: got=%s want=%s", result.task.OutputPolicy.Directory, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("CreateTask did not resume after rollback")
	}
}

func TestRuntimeConfigMaxIncreaseRollbackDoesNotDispatchQueuedTask(t *testing.T) {
	dm := NewDownloadManager(t.TempDir(), 1)
	firstRelease := make(chan struct{})
	secondRelease := make(chan struct{})
	adapter := &lifecycleTestAdapter{
		id: "config-max",
		releases: map[string]<-chan struct{}{
			"one": firstRelease,
			"two": secondRelease,
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
	deadline := time.Now().Add(2 * time.Second)
	for adapter.startedCount.Load() != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if adapter.startedCount.Load() != 1 {
		t.Fatal("first task did not start")
	}
	candidateMax := 2
	update, err := dm.BeginRuntimeConfigUpdate(RuntimeConfigPatch{MaxConcurrent: &candidateMax})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(30 * time.Millisecond)
	if adapter.startedCount.Load() != 1 {
		t.Fatal("candidate max dispatched queued work before settings commit")
	}
	if err := update.Rollback(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(30 * time.Millisecond)
	if adapter.startedCount.Load() != 1 || dm.GetMaxConcurrent() != 1 {
		t.Fatalf("rollback dispatched queued work or kept candidate max: started=%d max=%d", adapter.startedCount.Load(), dm.GetMaxConcurrent())
	}
	close(firstRelease)
	deadline = time.Now().Add(2 * time.Second)
	for adapter.startedCount.Load() != 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if adapter.startedCount.Load() != 2 {
		t.Fatal("queued task did not start after the original slot became free")
	}
	close(secondRelease)
}
