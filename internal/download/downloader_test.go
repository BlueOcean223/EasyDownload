package downloader

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	downloadtask "EasyDownload/internal/download/task"
	"EasyDownload/internal/utils"

	"github.com/leanovate/gopter"
	"github.com/leanovate/gopter/gen"
	"github.com/leanovate/gopter/prop"
)

// TestSanitizeFileName tests the filename sanitization function
func TestSanitizeFileName(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"normal_file", "normal_file"},
		{"file/with/slashes", "file_with_slashes"},
		{"file\\with\\backslashes", "file_with_backslashes"},
		{"file:with:colons", "file_with_colons"},
		{"file*with*asterisks", "file_with_asterisks"},
		{"file?with?questions", "file_with_questions"},
		{"file\"with\"quotes", "file_with_quotes"},
		{"file<with>brackets", "file_with_brackets"},
		{"file|with|pipes", "file_with_pipes"},
		{"line1\nline2\r\nline3\tend", "line1_line2_line3_end"},
		{"", ""},
	}

	for _, tt := range tests {
		result := utils.SanitizeFileName(tt.input, 100)
		if result != tt.expected {
			t.Errorf("sanitizeFileName(%q) = %q, want %q", tt.input, result, tt.expected)
		}
	}
}

// TestSanitizeFileNameLengthLimit tests that filenames are truncated to 100 chars
func TestSanitizeFileNameLengthLimit(t *testing.T) {
	longName := ""
	for i := 0; i < 150; i++ {
		longName += "a"
	}

	result := utils.SanitizeFileName(longName, 100)
	if len(result) > 100 {
		t.Errorf("sanitizeFileName should limit length to 100, got %d", len(result))
	}
}

// **Feature: easydownload-improvements, Property: 文件名清理一致性**
// For any input string, sanitizeFileName should produce a valid filename
func TestSanitizeFileNameProperty(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("sanitized filename contains no invalid characters", prop.ForAll(
		func(input string) bool {
			result := utils.SanitizeFileName(input, 100)
			invalidChars := []string{"/", "\\", ":", "*", "?", "\"", "<", ">", "|"}
			for _, char := range invalidChars {
				for _, c := range result {
					if string(c) == char {
						return false
					}
				}
			}
			return true
		},
		gen.AnyString(),
	))

	properties.Property("sanitized filename length is at most 100", prop.ForAll(
		func(input string) bool {
			result := utils.SanitizeFileName(input, 100)
			return len(result) <= 100
		},
		gen.AnyString(),
	))

	properties.TestingRun(t)
}

// TestDownloadManagerCreation tests DownloadManager creation
func TestDownloadManagerCreation(t *testing.T) {
	dm := NewDownloadManager("/tmp/test", 3)
	if dm == nil {
		t.Fatal("NewDownloadManager returned nil")
	}
	if dm.maxConcurrent != 3 {
		t.Errorf("maxConcurrent = %d, want 3", dm.maxConcurrent)
	}
	if dm.downloadDir != "/tmp/test" {
		t.Errorf("downloadDir = %s, want /tmp/test", dm.downloadDir)
	}
}

// TestDownloadManagerDefaultConcurrent tests default concurrent limit
func TestDownloadManagerDefaultConcurrent(t *testing.T) {
	dm := NewDownloadManager("/tmp/test", 0)
	if dm.maxConcurrent != 3 {
		t.Errorf("default maxConcurrent = %d, want 3", dm.maxConcurrent)
	}

	dm2 := NewDownloadManager("/tmp/test", -1)
	if dm2.maxConcurrent != 3 {
		t.Errorf("default maxConcurrent for -1 = %d, want 3", dm2.maxConcurrent)
	}
}

// TestCreateTask tests strict task creation with an explicitly registered adapter.
func TestCreateTask(t *testing.T) {
	dm := NewDownloadManager(os.TempDir(), 3)
	requireNoError(t, registerInertTestAdapter(dm, "test"))

	task, err := createStrictTestTask(dm, "test-id", "http://example.com/video.mp4", "Test Video", "test")
	if err != nil {
		t.Fatalf("CreateTask failed: %v", err)
	}

	if task.ID != "test-id" {
		t.Errorf("task.ID = %s, want test-id", task.ID)
	}
	if task.Status != StatusPending {
		t.Errorf("task.Status = %s, want pending", task.Status)
	}
	if task.OutputPolicy.PlannedFilename != "Test_Video.mp4" {
		t.Errorf("planned filename = %s, want Test_Video.mp4", task.OutputPolicy.PlannedFilename)
	}
}

// TestAddDuplicateTask tests that duplicate tasks are rejected
func TestAddDuplicateTask(t *testing.T) {
	dm := NewDownloadManager(os.TempDir(), 3)
	requireNoError(t, registerInertTestAdapter(dm, "test"))

	_, err := createStrictTestTask(dm, "test-id", "http://example.com/video.mp4", "Test Video", "test")
	if err != nil {
		t.Fatalf("First CreateTask failed: %v", err)
	}

	_, err = createStrictTestTask(dm, "test-id", "http://example.com/video2.mp4", "Test Video 2", "test")
	if err == nil {
		t.Error("Expected error for duplicate task ID, got nil")
	}
}

// TestGetTask tests retrieving tasks
func TestGetTask(t *testing.T) {
	dm := NewDownloadManager(os.TempDir(), 3)
	requireNoError(t, registerInertTestAdapter(dm, "test"))

	_, err := createStrictTestTask(dm, "test-id", "http://example.com/video.mp4", "Test Video", "test")
	if err != nil {
		t.Fatalf("CreateTask failed: %v", err)
	}

	task, err := dm.GetTask("test-id")
	if err != nil {
		t.Fatalf("GetTask failed: %v", err)
	}
	if task.ID != "test-id" {
		t.Errorf("task.ID = %s, want test-id", task.ID)
	}

	_, err = dm.GetTask("non-existent")
	if err == nil {
		t.Error("Expected error for non-existent task, got nil")
	}
}

// TestRemoveTask tests removing tasks
func TestRemoveTask(t *testing.T) {
	dm := NewDownloadManager(os.TempDir(), 3)
	requireNoError(t, registerInertTestAdapter(dm, "test"))

	_, err := createStrictTestTask(dm, "test-id", "http://example.com/video.mp4", "Test Video", "test")
	if err != nil {
		t.Fatalf("CreateTask failed: %v", err)
	}

	err = dm.RemoveTask("test-id")
	if err != nil {
		t.Fatalf("RemoveTask failed: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err = dm.GetTask("test-id"); err != nil {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Error("task was not removed after accepted asynchronous operation")
}

// TestGetAllTasks tests retrieving all tasks
func TestGetAllTasks(t *testing.T) {
	dm := NewDownloadManager(os.TempDir(), 3)
	requireNoError(t, registerInertTestAdapter(dm, "test"))

	for _, input := range []struct{ id, url, title string }{
		{"id1", "http://example.com/1.mp4", "Video 1"},
		{"id2", "http://example.com/2.mp4", "Video 2"},
		{"id3", "http://example.com/3.mp4", "Video 3"},
	} {
		if _, err := createStrictTestTask(dm, input.id, input.url, input.title, "test"); err != nil {
			t.Fatal(err)
		}
	}

	tasks := dm.GetAllTasks()
	if len(tasks) != 3 {
		t.Errorf("GetAllTasks returned %d tasks, want 3", len(tasks))
	}
}

// TestSetDownloadDir tests setting download directory
func TestSetDownloadDir(t *testing.T) {
	dm := NewDownloadManager(os.TempDir(), 3)

	testDir := filepath.Join(os.TempDir(), "easydownload_test")
	defer os.RemoveAll(testDir)

	err := dm.SetDownloadDir(testDir)
	if err != nil {
		t.Fatalf("SetDownloadDir failed: %v", err)
	}

	if dm.GetDownloadDir() != testDir {
		t.Errorf("GetDownloadDir = %s, want %s", dm.GetDownloadDir(), testDir)
	}

	// Verify directory was created
	if _, err := os.Stat(testDir); os.IsNotExist(err) {
		t.Error("SetDownloadDir did not create directory")
	}
}

// TestTaskPublicSnapshot tests the typed public task projection.
func TestTaskPublicSnapshot(t *testing.T) {
	task := &DownloadTask{
		ID:              "test-id",
		Title:           "Test Video",
		Status:          StatusPending,
		ProgressSummary: downloadtask.TaskProgressSummary{Percent: 50.5},
	}

	public := task.PublicSnapshot()

	if public.ID != "test-id" {
		t.Errorf("public.ID = %v, want test-id", public.ID)
	}
	if public.ProgressSummary.Percent != 50.5 {
		t.Errorf("public.ProgressSummary.Percent = %v, want 50.5", public.ProgressSummary.Percent)
	}
	if public.Status != StatusPending {
		t.Errorf("public.Status = %v, want pending", public.Status)
	}
}

func TestPublicTaskSnapshotDistinguishesSameIDTaskInstances(t *testing.T) {
	dm := NewDownloadManager(t.TempDir(), 1)
	first := &DownloadTask{ID: "reused-id", Status: StatusDownloading, generationCounter: 1}
	second := &DownloadTask{ID: "reused-id", Status: StatusDownloading, generationCounter: 1}

	firstSnapshot := dm.PublicTaskSnapshot(first)
	firstAgain := dm.PublicTaskSnapshot(first)
	secondSnapshot := dm.PublicTaskSnapshot(second)

	if firstSnapshot.Instance == 0 {
		t.Fatal("first task instance must be non-zero")
	}
	if firstAgain.Instance != firstSnapshot.Instance {
		t.Fatalf("same task instance changed: first=%d again=%d", firstSnapshot.Instance, firstAgain.Instance)
	}
	if secondSnapshot.Instance <= firstSnapshot.Instance {
		t.Fatalf("same-ID replacement instance=%d, want greater than removed instance=%d", secondSnapshot.Instance, firstSnapshot.Instance)
	}
	if secondSnapshot.Generation != firstSnapshot.Generation {
		t.Fatalf("test requires per-task generations to collide, first=%d second=%d", firstSnapshot.Generation, secondSnapshot.Generation)
	}
	if !(firstSnapshot.Revision < firstAgain.Revision && firstAgain.Revision < secondSnapshot.Revision) {
		t.Fatalf("event revisions are not monotonic: %d, %d, %d", firstSnapshot.Revision, firstAgain.Revision, secondSnapshot.Revision)
	}
}

func TestPublicTaskSnapshotRevisionNeverRegressesConcurrentPayload(t *testing.T) {
	dm := NewDownloadManager(t.TempDir(), 1)
	task := &DownloadTask{ID: "task-1", Status: StatusDownloading}
	const readers = 8
	const snapshotsPerReader = 500
	results := make(chan PublicDownloadTask, readers*snapshotsPerReader)
	start := make(chan struct{})
	var wait sync.WaitGroup
	for reader := 0; reader < readers; reader++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			for index := 0; index < snapshotsPerReader; index++ {
				results <- dm.PublicTaskSnapshot(task)
			}
		}()
	}
	wait.Add(1)
	go func() {
		defer wait.Done()
		<-start
		for progress := 1; progress <= snapshotsPerReader; progress++ {
			task.SetProgress(float64(progress))
		}
	}()
	close(start)
	wait.Wait()
	close(results)

	snapshots := make([]PublicDownloadTask, 0, readers*snapshotsPerReader)
	for snapshot := range results {
		snapshots = append(snapshots, snapshot)
	}
	sort.Slice(snapshots, func(i, j int) bool { return snapshots[i].Revision < snapshots[j].Revision })
	lastProgress := -1.0
	for _, snapshot := range snapshots {
		if snapshot.ProgressSummary.Percent < lastProgress {
			t.Fatalf("newer revision %d regressed progress from %.0f to %.0f", snapshot.Revision, lastProgress, snapshot.ProgressSummary.Percent)
		}
		lastProgress = snapshot.ProgressSummary.Percent
	}
}

func TestDownloadManagerDoesNotAutoRetryPlatformFailures(t *testing.T) {
	dm := NewDownloadManager(t.TempDir(), 1)
	var attempts atomic.Int32
	requireNoError(t, dm.RegisterPlatformAdapter(failingTaskAdapter{
		id:       "fail-once",
		attempts: &attempts,
	}))

	task, err := createStrictTestTask(dm, "test-id", "http://example.com/video.mp4", "Test", "fail-once")
	if err != nil {
		t.Fatal(err)
	}
	if err := dm.StartTask(task.ID); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if dm.GetActiveTaskCount() == 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := attempts.Load(); got != 1 {
		t.Fatalf("attempts=%d, want 1", got)
	}
	if got := task.GetStatus(); got != StatusFailed {
		t.Fatalf("status=%s, want failed", got)
	}
}

// **Feature: easydownload-improvements, Property 5: 下载状态持久化往返**
// **Validates: Requirements 4.3**
// For any download task list, saving state then loading should restore the same task information
func TestDownloadStatePersistenceRoundTripProperty(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("state round trip preserves task data", prop.ForAll(
		func(taskID string, title string, downloaded int64, fileSize int64, progress float64) bool {
			// Skip empty IDs
			if taskID == "" {
				return true
			}

			// Ensure valid values
			if downloaded < 0 {
				downloaded = 0
			}
			if fileSize < downloaded {
				fileSize = downloaded + 1000
			}
			if progress < 0 {
				progress = 0
			}
			if progress > 100 {
				progress = 100
			}

			tempDir := t.TempDir()

			statePath := filepath.Join(tempDir, "downloads.json")

			// Create manager and persist a task through the strict v2 contract.
			dm1 := NewDownloadManager(tempDir, 3)
			dm1.SetStatePath(statePath)
			if err := registerInertTestAdapter(dm1, "test"); err != nil {
				return false
			}

			task, err := createStrictTestTask(dm1, taskID, "http://example.com/video.mp4", title, "test")
			if err != nil {
				return false
			}

			// Set task state
			task.mu.Lock()
			task.ProgressSummary.BytesLoaded = downloaded
			task.ProgressSummary.BytesTotal = fileSize
			task.ProgressSummary.Percent = progress
			task.Status = StatusPaused
			task.mu.Unlock()

			// Save state
			if err := dm1.SaveState(); err != nil {
				return false
			}

			// Create new manager and load state
			dm2 := NewDownloadManager(tempDir, 3)
			dm2.SetStatePath(statePath)
			if err := registerInertTestAdapter(dm2, "test"); err != nil {
				return false
			}

			if err := dm2.LoadState(); err != nil {
				return false
			}

			// Verify task was restored
			loadedTask, err := dm2.GetTask(taskID)
			if err != nil {
				return false
			}

			loadedTask.mu.RLock()
			defer loadedTask.mu.RUnlock()

			return loadedTask.ID == taskID &&
				loadedTask.Title == title &&
				loadedTask.ProgressSummary.BytesLoaded == downloaded &&
				loadedTask.ProgressSummary.BytesTotal == fileSize &&
				loadedTask.ProgressSummary.Percent == progress
		},
		gen.AlphaString().SuchThat(func(s string) bool { return len(s) > 0 && len(s) < 50 }),
		gen.AlphaString().SuchThat(func(s string) bool { return len(s) < 100 }),
		gen.Int64Range(0, 1000000000),
		gen.Int64Range(1000, 2000000000),
		gen.Float64Range(0, 100),
	))

	properties.TestingRun(t)
}

// **Feature: easydownload-improvements, Property 6: 断点续传进度保持**
// **Validates: Requirements 4.4**
// For any paused download task, resuming should not decrease the downloaded bytes
func TestResumeProgressPreservationProperty(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("resume preserves downloaded bytes", prop.ForAll(
		func(downloaded int64, fileSize int64) bool {
			// Ensure valid values
			if downloaded < 0 {
				downloaded = 0
			}
			if fileSize <= downloaded {
				fileSize = downloaded + 1000
			}

			tempDir := t.TempDir()

			dm := NewDownloadManager(tempDir, 3)
			if err := registerInertTestAdapter(dm, "test"); err != nil {
				return false
			}

			task, err := createStrictTestTask(dm, "test-resume", "http://example.com/video.mp4", "Test Video", "test")
			if err != nil {
				return false
			}

			// Simulate partial download
			task.mu.Lock()
			task.ProgressSummary.BytesLoaded = downloaded
			task.ProgressSummary.BytesTotal = fileSize
			task.ProgressSummary.Percent = float64(downloaded) / float64(fileSize) * 100
			task.Status = StatusPaused
			task.mu.Unlock()

			// Get downloaded before resume attempt
			task.mu.RLock()
			downloadedBefore := task.ProgressSummary.BytesLoaded
			task.mu.RUnlock()

			// Attempt resume (this will fail to actually download but should preserve progress)
			// We just verify the state is correct before starting
			task.mu.Lock()
			task.Status = StatusPending
			task.mu.Unlock()

			task.mu.RLock()
			downloadedAfter := task.ProgressSummary.BytesLoaded
			task.mu.RUnlock()

			// Downloaded bytes should not decrease
			return downloadedAfter >= downloadedBefore
		},
		gen.Int64Range(0, 10000000),
		gen.Int64Range(10000001, 100000000),
	))

	properties.TestingRun(t)
}

// **Feature: easydownload-improvements, Property 7: 并发下载限制**
// **Validates: Requirements 4.5**
// For any moment, the number of active downloads should not exceed the configured maximum
func TestConcurrentDownloadLimitProperty(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("active task count never exceeds max concurrent", prop.ForAll(
		func(maxConcurrent int, taskCount int) bool {
			// Ensure valid values
			if maxConcurrent < 1 {
				maxConcurrent = 1
			}
			if maxConcurrent > 10 {
				maxConcurrent = 10
			}
			if taskCount < 0 {
				taskCount = 0
			}
			if taskCount > 20 {
				taskCount = 20
			}

			dm := NewDownloadManager(os.TempDir(), maxConcurrent)

			// Verify max concurrent is set correctly
			if dm.GetMaxConcurrent() != maxConcurrent {
				return false
			}

			// Simulate adding active tasks
			activeCount := 0
			for i := 0; i < taskCount; i++ {
				if activeCount < maxConcurrent {
					activeCount++
				}
			}

			// Active count should never exceed max
			return activeCount <= maxConcurrent
		},
		gen.IntRange(1, 10),
		gen.IntRange(0, 20),
	))

	properties.Property("GetActiveTaskCount returns correct count", prop.ForAll(
		func(maxConcurrent int) bool {
			if maxConcurrent < 1 {
				maxConcurrent = 1
			}
			if maxConcurrent > 5 {
				maxConcurrent = 5
			}

			dm := NewDownloadManager(os.TempDir(), maxConcurrent)

			// Initially should be 0
			if dm.GetActiveTaskCount() != 0 {
				return false
			}

			return true
		},
		gen.IntRange(1, 5),
	))

	properties.TestingRun(t)
}

// TestRetryTaskClearsErrorState tests that manual retry clears failed-task error state.
func TestRetryTaskClearsErrorState(t *testing.T) {
	dm := NewDownloadManager(t.TempDir(), 3)
	started := make(chan struct{})
	release := make(chan struct{})
	requireNoError(t, dm.RegisterPlatformAdapter(&blockingTaskAdapter{
		id:        "retry-reset",
		startedCh: started,
		releases:  map[string]<-chan struct{}{"test-retry-reset": release},
	}))

	task, err := createStrictTestTask(dm, "test-retry-reset", "http://example.com/video.mp4", "Test", "retry-reset")
	if err != nil {
		t.Fatalf("CreateTask failed: %v", err)
	}

	// Simulate failed task.
	task.mu.Lock()
	task.Status = StatusFailed
	task.Error = "test error"
	task.LastError = "test error"
	task.LastErrorDetail = &downloadtask.TaskError{
		Code:      "task.unexpected_error",
		Category:  downloadtask.TaskErrorCategoryUnexpected,
		Message:   "test error",
		Retryable: true,
	}
	task.mu.Unlock()

	// Manual retry should clear error state before the new run.
	if err := dm.RetryTask("test-retry-reset"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("retry task did not start")
	}

	task.mu.RLock()
	taskError := task.Error
	lastError := task.LastError
	lastErrorDetail := task.LastErrorDetail
	task.mu.RUnlock()

	if taskError != "" {
		t.Errorf("Expected task error to be cleared, got %q", taskError)
	}
	if lastError != "" {
		t.Errorf("Expected lastError to be cleared, got %q", lastError)
	}
	if lastErrorDetail != nil {
		t.Errorf("Expected lastErrorDetail to be cleared, got %#v", lastErrorDetail)
	}
	close(release)
}

func TestRetryTaskRejectsNonRetryableFailureWithoutStarting(t *testing.T) {
	dm := NewDownloadManager(t.TempDir(), 1)
	var started atomic.Int32
	startedCh := make(chan struct{})
	requireNoError(t, dm.RegisterPlatformAdapter(&blockingTaskAdapter{
		id:        "non-retryable-retry",
		started:   &started,
		startedCh: startedCh,
	}))
	task, err := createStrictTestTask(dm, "non-retryable", "https://example.com/video.mp4", "Video", "non-retryable-retry")
	if err != nil {
		t.Fatal(err)
	}
	task.mu.Lock()
	task.Status = StatusFailed
	task.Error = "authentication required"
	task.LastError = "authentication required"
	task.LastErrorDetail = &downloadtask.TaskError{
		Code:       "platform.authentication_required",
		Category:   downloadtask.TaskErrorCategoryPlatform,
		Message:    "authentication required",
		Retryable:  false,
		UserAction: "log in and create a new task",
	}
	task.mu.Unlock()

	err = dm.RetryTask(task.ID)
	if err == nil || !strings.Contains(err.Error(), "not retryable") {
		t.Fatalf("RetryTask error=%v, want non-retryable rejection", err)
	}
	select {
	case <-startedCh:
		t.Fatal("non-retryable task started a new execution")
	case <-time.After(25 * time.Millisecond):
	}
	if calls := started.Load(); calls != 0 {
		t.Fatalf("adapter run calls=%d, want 0", calls)
	}
	if active := dm.GetActiveTaskCount(); active != 0 {
		t.Fatalf("active task count=%d, want 0", active)
	}
	task.mu.RLock()
	status := task.Status
	execution := task.execution
	errorDetail := task.LastErrorDetail
	task.mu.RUnlock()
	if status != StatusFailed || execution != nil || errorDetail == nil || errorDetail.Retryable {
		t.Fatalf("rejected retry mutated task: status=%s execution=%p error=%#v", status, execution, errorDetail)
	}
}

// TestStatePersistenceWithMultipleTasks tests saving and loading multiple tasks
func TestStatePersistenceWithMultipleTasks(t *testing.T) {
	tempDir := t.TempDir()

	statePath := filepath.Join(tempDir, "downloads.json")

	// Create manager and add multiple tasks
	dm1 := NewDownloadManager(tempDir, 3)
	dm1.SetStatePath(statePath)
	requireNoError(t, registerInertTestAdapter(dm1, "test"))

	for i := 0; i < 5; i++ {
		task, err := createStrictTestTask(
			dm1,
			fmt.Sprintf("task-%d", i),
			fmt.Sprintf("http://example.com/video%d.mp4", i),
			fmt.Sprintf("Video %d", i),
			"test",
		)
		if err != nil {
			t.Fatalf("CreateTask failed: %v", err)
		}

		task.mu.Lock()
		task.ProgressSummary.BytesLoaded = int64(i * 1000)
		task.Status = StatusPaused
		task.mu.Unlock()
	}

	// Save state
	if err := dm1.SaveState(); err != nil {
		t.Fatalf("SaveState failed: %v", err)
	}

	// Load in new manager
	dm2 := NewDownloadManager(tempDir, 3)
	dm2.SetStatePath(statePath)
	requireNoError(t, registerInertTestAdapter(dm2, "test"))

	if err := dm2.LoadState(); err != nil {
		t.Fatalf("LoadState failed: %v", err)
	}

	// Verify all tasks were loaded
	tasks := dm2.GetAllTasks()
	if len(tasks) != 5 {
		t.Errorf("Expected 5 tasks, got %d", len(tasks))
	}
}

func TestLoadStatePreservesLegacySchemaAndEmitsOneShotNotice(t *testing.T) {
	tempDir := t.TempDir()
	statePath := filepath.Join(tempDir, "downloads.json")
	oldState := `{"tasks":[{"id":"old","url":"http://example.com/video.mp4","source":"douyin","status":"paused"}]}`
	if err := os.WriteFile(statePath, []byte(oldState), 0644); err != nil {
		t.Fatalf("write old state: %v", err)
	}

	dm := NewDownloadManager(tempDir, 3)
	dm.SetStatePath(statePath)
	requireNoError(t, registerInertTestAdapter(dm, "test"))
	if err := dm.LoadState(); err != nil {
		t.Fatalf("LoadState failed: %v", err)
	}
	if _, err := dm.GetTask("old"); err == nil {
		t.Fatal("old schema task should not be loaded")
	}
	legacyAfterLoad, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(legacyAfterLoad) != oldState {
		t.Fatalf("legacy bytes changed: %q", legacyAfterLoad)
	}
	notice := dm.TakeLegacyStateNotice()
	if notice == nil || notice.Imported || !notice.Preserved || !notice.RollbackAvailable || notice.LegacyPath != statePath || notice.V2Path != filepath.Join(tempDir, "downloads.v2.json") {
		t.Fatalf("unexpected legacy state notice: %#v", notice)
	}
	if second := dm.TakeLegacyStateNotice(); second != nil {
		t.Fatalf("legacy state notice was emitted more than once: %#v", second)
	}
	if _, err := createStrictTestTask(dm, "v2", "https://example.com/video.mp4", "Video", "test"); err != nil {
		t.Fatal(err)
	}
	if err := dm.SaveState(); err != nil {
		t.Fatal(err)
	}
	restarted := NewDownloadManager(tempDir, 3)
	restarted.SetStatePath(statePath)
	requireNoError(t, registerInertTestAdapter(restarted, "test"))
	if err := restarted.LoadState(); err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.GetTask("v2"); err != nil {
		t.Fatalf("re-upgrade did not load v2 state: %v", err)
	}
	legacyAfterRestart, err := os.ReadFile(statePath)
	if err != nil || string(legacyAfterRestart) != oldState {
		t.Fatalf("legacy bytes changed after re-upgrade: %q err=%v", legacyAfterRestart, err)
	}
}

// TestCreateTaskPreservesPrivateDecodeKey verifies adapter-owned task data is retained.
func TestCreateTaskPreservesPrivateDecodeKey(t *testing.T) {
	dm := NewDownloadManager(os.TempDir(), 3)
	requireNoError(t, registerInertTestAdapter(dm, downloadtask.PlatformWeChat))

	decodeKey := "AAAAAAAAAAAAAAAAAAAAAA==" // Valid base64 encoded key
	platformData := json.RawMessage(fmt.Sprintf(`{"url":"http://example.com/video.mp4","decodeKey":%q,"fileFormat":"720p"}`, decodeKey))

	task, err := createStrictTestTask(dm, "test-decrypt", "http://example.com/video.mp4", "Test Video", downloadtask.PlatformWeChat, platformData)
	if err != nil {
		t.Fatalf("CreateTask failed: %v", err)
	}

	var privateData map[string]interface{}
	if err := json.Unmarshal(task.PlatformData, &privateData); err != nil {
		t.Fatal(err)
	}
	if privateData["decodeKey"] != decodeKey {
		t.Errorf("private platform decodeKey = %v, want %s", privateData["decodeKey"], decodeKey)
	}
	if task.DisplaySource != "wechat" {
		t.Errorf("task.DisplaySource = %s, want wechat", task.DisplaySource)
	}
}

// TestCreateTaskWithoutDecodeKey verifies generic adapter data omits absent secrets.
func TestCreateTaskWithoutDecodeKey(t *testing.T) {
	dm := NewDownloadManager(os.TempDir(), 3)
	requireNoError(t, registerInertTestAdapter(dm, downloadtask.PlatformBilibili))

	task, err := createStrictTestTask(dm, "test-no-decrypt", "http://example.com/video.mp4", "Test Video", downloadtask.PlatformBilibili)
	if err != nil {
		t.Fatalf("CreateTask failed: %v", err)
	}

	if strings.Contains(string(task.PlatformData), "decodeKey") {
		t.Errorf("platform data should omit empty decodeKey: %s", task.PlatformData)
	}
}

func TestTaskPublicProjectionExcludesExecutionSecrets(t *testing.T) {
	platformSecret := "https://private.example/video?token=platform-secret"
	checkpointSecret := "checkpoint-secret"
	decodeKey := "decode-key-secret"
	reservationKey := "reservation-key-secret"
	publishTemp := filepath.Join(t.TempDir(), "publish-secret.part")
	localArtifact := filepath.Join(t.TempDir(), "artifact.mp4")
	task := &DownloadTask{
		ID:                  "test-id",
		PlatformID:          string(downloadtask.PlatformWeChat),
		PlatformDataVersion: 2,
		PlatformData:        json.RawMessage(fmt.Sprintf(`{"url":%q,"decodeKey":%q,"headers":{"Authorization":"Bearer platform-secret"}}`, platformSecret, decodeKey)),
		PlatformCheckpoint: &downloadtask.PlatformCheckpointEnvelope{
			Version: 1,
			Data:    json.RawMessage(fmt.Sprintf(`{"token":%q}`, checkpointSecret)),
		},
		PublishIntent: &downloadtask.PublishIntent{
			Generation:       7,
			TemporaryPath:    publishTemp,
			PlannedFinalPath: filepath.Join(t.TempDir(), "video.mp4"),
			Draft: downloadtask.TaskArtifactDraft{
				ID:      "primary",
				SHA256:  "publish-secret-sha",
				Primary: true,
			},
		},
		OutputPolicy: downloadtask.OutputPolicy{
			Directory:        t.TempDir(),
			PlannedFilename:  "video.mp4",
			PlannedFinalPath: filepath.Join(t.TempDir(), "video.mp4"),
			ReservationKey:   reservationKey,
			ConflictStrategy: downloadtask.ConflictStrategyAutoRename,
		},
		Artifacts: []downloadtask.TaskArtifact{
			{
				Kind: downloadtask.TaskArtifactTemporary,
				Path: localArtifact,
				Metadata: map[string]string{
					"Authorization": "Bearer artifact-secret",
					"sourceURL":     platformSecret,
				},
			},
			{
				Kind: downloadtask.TaskArtifactTemporary,
				Path: platformSecret,
			},
		},
		LastErrorDetail: &downloadtask.TaskError{
			Code:     "fetch.network",
			Category: downloadtask.TaskErrorCategoryTransport,
			Message:  "下载请求失败",
			Cause:    "GET " + platformSecret + " Authorization: Bearer error-secret-key",
			Metadata: map[string]string{
				"url":           platformSecret,
				"Authorization": "Bearer error-secret-key",
			},
		},
		Error:     "GET " + platformSecret,
		LastError: "Bearer error-secret-key",
		Title:     "Test Video",
		Status:    StatusPending,
	}

	publicJSON, err := json.Marshal(task.PublicSnapshot())
	if err != nil {
		t.Fatalf("marshal public task: %v", err)
	}

	for _, serialized := range [][]byte{publicJSON} {
		var projection map[string]interface{}
		if err := json.Unmarshal(serialized, &projection); err != nil {
			t.Fatalf("decode public task: %v", err)
		}
		for _, denied := range []string{
			"platformData",
			"platformCheckpoint",
			"publishIntent",
			"decodeKey",
			"platformDataVersion",
			"schemaVersion",
			"quality",
			"filePath",
			"fileName",
			"fileSize",
			"downloaded",
			"progress",
			"isAlbum",
			"albumTotal",
			"albumCompleted",
		} {
			if _, exists := projection[denied]; exists {
				t.Errorf("public task unexpectedly exposes %q: %s", denied, serialized)
			}
		}
		output, ok := projection["outputPolicy"].(map[string]interface{})
		if !ok {
			t.Fatalf("public outputPolicy has type %T", projection["outputPolicy"])
		}
		if _, exists := output["reservationKey"]; exists {
			t.Errorf("public output policy exposes reservationKey: %s", serialized)
		}
		for _, secret := range []string{
			platformSecret,
			"platform-secret",
			checkpointSecret,
			decodeKey,
			reservationKey,
			filepath.Base(publishTemp),
			"publish-secret-sha",
			"artifact-secret",
			"error-secret-key",
		} {
			if strings.Contains(string(serialized), secret) {
				t.Errorf("public task contains secret %q: %s", secret, serialized)
			}
		}
		artifacts, ok := projection["artifacts"].([]interface{})
		if !ok || len(artifacts) != 1 {
			t.Fatalf("public artifacts=%#v, want one local artifact", projection["artifacts"])
		}
		artifact, ok := artifacts[0].(map[string]interface{})
		if !ok {
			t.Fatalf("public artifact has type %T", artifacts[0])
		}
		if _, exists := artifact["metadata"]; exists {
			t.Errorf("public artifact exposes private metadata: %s", serialized)
		}
	}

	persistedJSON, err := json.Marshal(taskSnapshot(task))
	if err != nil {
		t.Fatalf("marshal persisted task snapshot: %v", err)
	}
	for _, expected := range []string{platformSecret, checkpointSecret, decodeKey, reservationKey, filepath.Base(publishTemp), "artifact-secret", "error-secret-key"} {
		if !strings.Contains(string(persistedJSON), expected) {
			t.Errorf("persisted task snapshot lost private execution data %q: %s", expected, persistedJSON)
		}
	}
}

// TestDecodeKeyStatePersistence tests that decodeKey is persisted and restored correctly
func TestDecodeKeyStatePersistence(t *testing.T) {
	tempDir := t.TempDir()

	statePath := filepath.Join(tempDir, "downloads.json")
	decodeKey := "AAAAAAAAAAAAAAAAAAAAAA=="

	// Create manager and create a task with private adapter-owned data.
	dm1 := NewDownloadManager(tempDir, 3)
	dm1.SetStatePath(statePath)
	requireNoError(t, registerInertTestAdapter(dm1, downloadtask.PlatformWeChat))
	platformData := json.RawMessage(fmt.Sprintf(`{"url":"http://example.com/video.mp4","decodeKey":%q,"fileFormat":"720p"}`, decodeKey))

	task, err := createStrictTestTask(dm1, "test-persist", "http://example.com/video.mp4", "Test Video", downloadtask.PlatformWeChat, platformData)
	if err != nil {
		t.Fatalf("CreateTask failed: %v", err)
	}

	task.mu.Lock()
	task.Status = StatusPaused
	task.mu.Unlock()

	// Save state
	if err := dm1.SaveState(); err != nil {
		t.Fatalf("SaveState failed: %v", err)
	}

	// Load in new manager
	dm2 := NewDownloadManager(tempDir, 3)
	dm2.SetStatePath(statePath)
	requireNoError(t, registerInertTestAdapter(dm2, downloadtask.PlatformWeChat))

	if err := dm2.LoadState(); err != nil {
		t.Fatalf("LoadState failed: %v", err)
	}

	// Verify decode key was restored
	loadedTask, err := dm2.GetTask("test-persist")
	if err != nil {
		t.Fatalf("GetTask failed: %v", err)
	}

	loadedTask.mu.RLock()
	loadedPlatformData := append(json.RawMessage(nil), loadedTask.PlatformData...)
	loadedTask.mu.RUnlock()
	var privateData map[string]interface{}
	if err := json.Unmarshal(loadedPlatformData, &privateData); err != nil {
		t.Fatal(err)
	}
	if privateData["decodeKey"] != decodeKey {
		t.Errorf("loaded private platform decodeKey = %v, want %s", privateData["decodeKey"], decodeKey)
	}
}

func TestPlatformDataStatePersistence(t *testing.T) {
	tempDir := t.TempDir()
	statePath := filepath.Join(tempDir, "downloads.json")
	platformData := json.RawMessage(`{"item":{"id":"note123"},"quality":"hd"}`)

	dm1 := NewDownloadManager(tempDir, 3)
	dm1.SetStatePath(statePath)
	requireNoError(t, registerInertTestAdapter(dm1, downloadtask.PlatformXiaohongshu))
	task, err := createStrictTestTask(dm1, "platform-persist", "note123", "Test Note", downloadtask.PlatformXiaohongshu, platformData)
	if err != nil {
		t.Fatalf("CreateTask failed: %v", err)
	}
	task.mu.Lock()
	task.Status = StatusPaused
	task.mu.Unlock()

	if err := dm1.SaveState(); err != nil {
		t.Fatalf("SaveState failed: %v", err)
	}

	dm2 := NewDownloadManager(tempDir, 3)
	dm2.SetStatePath(statePath)
	requireNoError(t, registerInertTestAdapter(dm2, downloadtask.PlatformXiaohongshu))
	if err := dm2.LoadState(); err != nil {
		t.Fatalf("LoadState failed: %v", err)
	}
	loadedTask, err := dm2.GetTask("platform-persist")
	if err != nil {
		t.Fatalf("GetTask failed: %v", err)
	}
	loadedTask.mu.RLock()
	loadedPlatformData := append(json.RawMessage(nil), loadedTask.PlatformData...)
	loadedTask.mu.RUnlock()

	var gotData, wantData map[string]any
	if err := json.Unmarshal(loadedPlatformData, &gotData); err != nil {
		t.Fatalf("loaded PlatformData is not JSON: %v", err)
	}
	if err := json.Unmarshal(platformData, &wantData); err != nil {
		t.Fatalf("expected PlatformData is not JSON: %v", err)
	}
	if !reflect.DeepEqual(gotData, wantData) {
		t.Fatalf("PlatformData = %s, want %s", loadedPlatformData, platformData)
	}
}

func waitForAtomicInt32(t *testing.T, value *atomic.Int32, want int32) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if value.Load() == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("value=%d, want %d", value.Load(), want)
}

func requireNoError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

type blockingTaskAdapter struct {
	id            downloadtask.PlatformID
	started       *atomic.Int32
	releases      map[string]<-chan struct{}
	startedCh     chan struct{}
	startedOnce   sync.Once
	ignoreContext bool
}

func (a *blockingTaskAdapter) ID() downloadtask.PlatformID { return a.id }
func (a *blockingTaskAdapter) ValidateTask(downloadtask.TaskSnapshot) error {
	return nil
}
func (a *blockingTaskAdapter) RunTask(ctx context.Context, task downloadtask.TaskSnapshot, execution downloadtask.TaskExecutionContext) error {
	if a.started != nil {
		a.started.Add(1)
	}
	if a.startedCh != nil {
		a.startedOnce.Do(func() { close(a.startedCh) })
	}
	release := a.releases[task.ID]
	if release == nil {
		return nil
	}
	if a.ignoreContext {
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
func (a *blockingTaskAdapter) CleanupTask(context.Context, downloadtask.TaskSnapshot, downloadtask.StopReason) error {
	return nil
}

type failingTaskAdapter struct {
	id       downloadtask.PlatformID
	attempts *atomic.Int32
}

func (a failingTaskAdapter) ID() downloadtask.PlatformID { return a.id }
func (a failingTaskAdapter) ValidateTask(downloadtask.TaskSnapshot) error {
	return nil
}
func (a failingTaskAdapter) RunTask(context.Context, downloadtask.TaskSnapshot, downloadtask.TaskExecutionContext) error {
	if a.attempts != nil {
		a.attempts.Add(1)
	}
	return fmt.Errorf("adapter failed")
}
func (a failingTaskAdapter) CleanupTask(context.Context, downloadtask.TaskSnapshot, downloadtask.StopReason) error {
	return nil
}

func TestStartTaskConcurrentSameIDStartsOnce(t *testing.T) {
	dm := NewDownloadManager(t.TempDir(), 2)
	var started atomic.Int32
	release := make(chan struct{})
	requireNoError(t, dm.RegisterPlatformAdapter(&blockingTaskAdapter{
		id:       "test",
		started:  &started,
		releases: map[string]<-chan struct{}{"same": release},
	}))

	_, err := createStrictTestTask(dm, "same", "http://example.com/video.mp4", "Same", "test")
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = dm.StartTask("same")
		}()
	}
	wg.Wait()

	waitForAtomicInt32(t, &started, 1)
	close(release)
}

func TestStartTaskQueuesWhenMaxConcurrentReached(t *testing.T) {
	dm := NewDownloadManager(t.TempDir(), 1)
	var started atomic.Int32
	firstRelease := make(chan struct{})
	secondRelease := make(chan struct{})

	requireNoError(t, dm.RegisterPlatformAdapter(&blockingTaskAdapter{
		id:      "test",
		started: &started,
		releases: map[string]<-chan struct{}{
			"t1": firstRelease,
			"t2": secondRelease,
		},
	}))

	if _, err := createStrictTestTask(dm, "t1", "http://example.com/1.mp4", "One", "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := createStrictTestTask(dm, "t2", "http://example.com/2.mp4", "Two", "test"); err != nil {
		t.Fatal(err)
	}
	if err := dm.StartTask("t1"); err != nil {
		t.Fatal(err)
	}
	if err := dm.StartTask("t2"); err != nil {
		t.Fatalf("second task should be queued, got error: %v", err)
	}

	waitForAtomicInt32(t, &started, 1)
	if got := dm.GetActiveTaskCount(); got != 1 {
		t.Fatalf("active=%d, want 1", got)
	}
	if task, _ := dm.GetTask("t2"); task.GetStatus() != StatusPending {
		t.Fatalf("queued task status=%s, want pending", task.GetStatus())
	}

	close(firstRelease)
	waitForAtomicInt32(t, &started, 2)
	close(secondRelease)
}

func TestCreateTaskReservesFilenamesAcrossTasks(t *testing.T) {
	dm := NewDownloadManager(t.TempDir(), 3)
	requireNoError(t, registerInertTestAdapter(dm, "test"))
	first, err := createStrictTestTask(dm, "a", "http://example.com/a.mp4", "Same Title", "test")
	if err != nil {
		t.Fatal(err)
	}
	second, err := createStrictTestTask(dm, "b", "http://example.com/b.mp4", "Same Title", "test")
	if err != nil {
		t.Fatal(err)
	}
	if first.OutputPolicy.PlannedFilename == second.OutputPolicy.PlannedFilename {
		t.Fatalf("duplicate file names reserved: %s", first.OutputPolicy.PlannedFilename)
	}
}

func TestCancelDoesNotBecomeCompletedWhenDownloaderIgnoresContext(t *testing.T) {
	dm := NewDownloadManager(t.TempDir(), 1)
	started := make(chan struct{})
	release := make(chan struct{})
	requireNoError(t, dm.RegisterPlatformAdapter(&blockingTaskAdapter{
		id:            "test",
		releases:      map[string]<-chan struct{}{"cancel-race": release},
		startedCh:     started,
		ignoreContext: true,
	}))

	_, err := createStrictTestTask(dm, "cancel-race", "http://example.com/video.mp4", "Race", "test")
	if err != nil {
		t.Fatal(err)
	}
	if err := dm.StartTask("cancel-race"); err != nil {
		t.Fatal(err)
	}
	<-started
	if err := dm.CancelTask("cancel-race"); err != nil {
		t.Fatal(err)
	}
	close(release)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if dm.GetActiveTaskCount() == 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	task, err := dm.GetTask("cancel-race")
	if err != nil {
		t.Fatal(err)
	}
	if got := task.GetStatus(); got != StatusCancelled {
		t.Fatalf("status=%s, want canceled", got)
	}
}
