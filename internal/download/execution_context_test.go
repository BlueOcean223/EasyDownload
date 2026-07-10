package downloader

import (
	"path/filepath"
	"testing"
	"time"

	downloadtask "EasyDownload/internal/download/task"
)

func TestProgressPersistenceUsesTimeOrPercentThreshold(t *testing.T) {
	directory := t.TempDir()
	dm := NewDownloadManager(directory, 1)
	started := make(chan struct{})
	release := make(chan struct{})
	adapter := &lifecycleTestAdapter{
		id:       "progress-persistence",
		started:  started,
		releases: map[string]<-chan struct{}{"task-1": release},
	}
	if err := dm.RegisterPlatformAdapter(adapter); err != nil {
		t.Fatal(err)
	}
	dm.SetStatePath(filepath.Join(directory, "downloads.v2.json"))
	if _, err := createStrictTestTask(dm, "task-1", "https://example.com/video.mp4", "Video", adapter.id); err != nil {
		t.Fatal(err)
	}
	if err := dm.StartTask("task-1"); err != nil {
		t.Fatal(err)
	}
	<-started
	task, _ := dm.GetTask("task-1")
	task.mu.RLock()
	execution := task.execution
	task.mu.RUnlock()
	ctx := newManagerExecutionContext(dm, task, execution)

	ctx.lastPersist = time.Now().Add(-3 * time.Second)
	before := dm.taskStore.CommittedRevision()
	percent := 0.5
	if err := ctx.UpdateTaskProgress(downloadtask.TaskProgressUpdate{
		StageID:        "download",
		StageLabel:     "下载中",
		StagePercent:   downloadtask.ProgressPercent(percent),
		OverallPercent: &percent,
		BytesLoaded:    5,
		BytesTotal:     1000,
	}); err != nil {
		t.Fatal(err)
	}
	afterTimeThreshold := dm.taskStore.CommittedRevision()
	if afterTimeThreshold <= before {
		t.Fatalf("time threshold did not persist progress: before=%d after=%d", before, afterTimeThreshold)
	}

	percent = 0.9
	if err := ctx.UpdateTaskProgress(downloadtask.TaskProgressUpdate{StageID: "download", StagePercent: downloadtask.ProgressPercent(percent), OverallPercent: &percent}); err != nil {
		t.Fatal(err)
	}
	if got := dm.taskStore.CommittedRevision(); got != afterTimeThreshold {
		t.Fatalf("sub-threshold progress persisted unexpectedly: got=%d want=%d", got, afterTimeThreshold)
	}

	percent = 1.6
	if err := ctx.UpdateTaskProgress(downloadtask.TaskProgressUpdate{StageID: "download", StagePercent: downloadtask.ProgressPercent(percent), OverallPercent: &percent}); err != nil {
		t.Fatal(err)
	}
	if got := dm.taskStore.CommittedRevision(); got <= afterTimeThreshold {
		t.Fatalf("one-percent threshold did not persist: got=%d previous=%d", got, afterTimeThreshold)
	}
	close(release)
	deadline := time.Now().Add(2 * time.Second)
	for dm.GetActiveTaskCount() != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if dm.GetActiveTaskCount() != 0 {
		t.Fatal("progress test worker did not exit")
	}
}

func TestRecordArtifactRejectsFinalAndRemotePaths(t *testing.T) {
	dm := NewDownloadManager(t.TempDir(), 1)
	task := &DownloadTask{ID: "task-1", Status: StatusDownloading}
	task.mu.Lock()
	execution := newTaskExecutionLocked(task)
	task.mu.Unlock()
	ctx := newManagerExecutionContext(dm, task, execution)

	if err := ctx.RecordArtifact(downloadtask.TaskArtifact{
		Kind: downloadtask.TaskArtifactFinal,
		Path: filepath.Join(t.TempDir(), "final.mp4"),
	}); err == nil {
		t.Fatal("RecordArtifact accepted a final artifact that bypasses PublishFinal")
	}
	if err := ctx.RecordArtifact(downloadtask.TaskArtifact{
		Kind: downloadtask.TaskArtifactTemporary,
		Path: "https://private.example/video?token=secret",
	}); err == nil {
		t.Fatal("RecordArtifact accepted a remote URL as an artifact path")
	}
}

func TestByteOnlyProgressUpdatePreservesCurrentPercentage(t *testing.T) {
	dm := NewDownloadManager(t.TempDir(), 1)
	task := &DownloadTask{
		ID:              "task-1",
		Status:          StatusDownloading,
		ProgressSummary: downloadtask.TaskProgressSummary{Percent: 37.5},
	}
	task.mu.Lock()
	execution := newTaskExecutionLocked(task)
	task.mu.Unlock()
	ctx := newManagerExecutionContext(dm, task, execution)

	if err := ctx.UpdateTaskProgress(downloadtask.TaskProgressUpdate{
		StageID:     "album_download",
		StageLabel:  "下载图集",
		BytesLoaded: 128,
		BytesTotal:  1024,
	}); err != nil {
		t.Fatal(err)
	}

	task.mu.RLock()
	defer task.mu.RUnlock()
	if got := task.ProgressSummary.Percent; got != 37.5 {
		t.Fatalf("byte-only update reset percentage: got=%v want=37.5", got)
	}
	if task.ProgressSummary.BytesLoaded != 128 || task.ProgressSummary.BytesTotal != 1024 {
		t.Fatalf("byte counters not applied: %+v", task.ProgressSummary)
	}
}
