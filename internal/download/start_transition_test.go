package downloader

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	downloadtask "EasyDownload/internal/download/task"
)

func TestRetryAndResumeRestoreCompleteStateWhenStartPersistenceFails(t *testing.T) {
	testCases := []struct {
		name   string
		status DownloadStatus
		start  func(*DownloadManager, string) error
	}{
		{name: "retry", status: StatusFailed, start: (*DownloadManager).RetryTask},
		{name: "resume", status: StatusPaused, start: (*DownloadManager).ResumeTask},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			directory := t.TempDir()
			dm := NewDownloadManager(directory, 1)
			platformID := downloadtask.PlatformID("start-transition-" + testCase.name)
			if err := registerInertTestAdapter(dm, platformID); err != nil {
				t.Fatal(err)
			}
			dm.SetStatePath(filepath.Join(directory, "downloads.v2.json"))
			task, err := createStrictTestTask(dm, "task-1", "https://example.com/video.mp4", "Video", platformID)
			if err != nil {
				t.Fatal(err)
			}

			originalProgress := downloadtask.TaskProgressSummary{
				Percent:      37.5,
				BytesLoaded:  375,
				BytesTotal:   1000,
				CurrentStage: "original-stage",
				StageLabel:   "原始阶段",
				ItemsDone:    3,
				ItemsTotal:   8,
			}
			originalDetail := &downloadtask.TaskError{
				Code:       "platform.original_failure",
				Category:   downloadtask.TaskErrorCategoryPlatform,
				Message:    "原始错误",
				Retryable:  true,
				UserAction: "retry",
				Cause:      "private diagnostic",
				Metadata:   map[string]string{"attempt": "7"},
			}
			task.mu.Lock()
			task.Status = testCase.status
			task.Error = "原始公开错误"
			task.LastError = "原始最近错误"
			task.LastErrorDetail = cloneTaskError(originalDetail)
			task.ProgressSummary = originalProgress
			task.Speed = 12345
			task.mu.Unlock()
			if err := dm.SaveState(); err != nil {
				t.Fatal(err)
			}

			dm.taskStore.writeFile = func(context.Context, string, []byte) error {
				return errors.New("injected start persistence failure")
			}
			if err := testCase.start(dm, task.ID); err == nil {
				t.Fatal("start unexpectedly succeeded")
			}

			task.mu.RLock()
			gotStatus := task.Status
			gotError := task.Error
			gotLastError := task.LastError
			gotDetail := cloneTaskError(task.LastErrorDetail)
			gotProgress := task.ProgressSummary
			gotSpeed := task.Speed
			gotExecution := task.execution
			task.mu.RUnlock()
			if gotStatus != testCase.status || gotError != "原始公开错误" || gotLastError != "原始最近错误" {
				t.Fatalf("task state was not restored: status=%s error=%q lastError=%q", gotStatus, gotError, gotLastError)
			}
			if !reflect.DeepEqual(gotDetail, originalDetail) {
				t.Fatalf("structured error was not restored: got=%#v want=%#v", gotDetail, originalDetail)
			}
			if !reflect.DeepEqual(gotProgress, originalProgress) || gotSpeed != 12345 {
				t.Fatalf("progress/speed were not restored: progress=%#v speed=%d", gotProgress, gotSpeed)
			}
			if gotExecution != nil || dm.GetActiveTaskCount() != 0 {
				t.Fatalf("failed start left an active execution: execution=%p active=%d", gotExecution, dm.GetActiveTaskCount())
			}
			dm.activeTasksMu.Lock()
			_, queued := dm.queuedTaskIDs[task.ID]
			dm.activeTasksMu.Unlock()
			if queued {
				t.Fatal("failed start left the task queued")
			}

			// The failed transition must also leave the previously committed state
			// authoritative on disk.
			persisted, err := NewTaskStore(dm.StatePath()).Load(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if len(persisted.Tasks) != 1 || DownloadStatus(persisted.Tasks[0].Status) != testCase.status {
				t.Fatalf("durable state changed after failed start: %#v", persisted.Tasks)
			}
		})
	}
}
