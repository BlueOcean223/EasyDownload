// Package downloader provides a robust download management system for handling
// file downloads from various sources including WeChat Video Channel, Bilibili,
// Douyin, and XiaoHongShu.
//
// The package supports:
//   - Concurrent download management with configurable limits and a pending queue
//   - Pause, resume, and cancel operations for download tasks
//   - User-initiated retry of failed tasks
//   - Progress tracking and speed calculation
//   - State persistence for recovery after application restart
//   - Platform adapters for special sources (e.g., Bilibili DASH, albums, decryption)
//   - Optional video decryption for encrypted content (decryption failures mark tasks failed)
//   - A shared Fetcher for sequential transfer and explicitly enabled multipart transfer
//
// Fetch always writes a verified temporary file. Resume is gated by resource
// identity, validators, If-Range, and an atomic sidecar; untrusted partials are
// reset rather than spliced. Multipart is used only when FetchRequest explicitly
// enables MultipartPolicy. Every part must return a matching 206 Content-Range
// for the same validator and total size before assembly.
//
// New task state is stored in downloads.v2.json as revisioned full snapshots.
// The legacy downloads.json is left byte-for-byte unchanged for rollback. Task
// creation data and runtime checkpoints are separate, so registered adapters can
// execute restored tasks without application-layer closure reconstruction.
// Adapters reject unknown PlatformDataVersion values before decoding private
// execution data, so a future or corrupted payload cannot run with v1 semantics.
// Output paths are reserved at creation. Adapters write only temporary files;
// PublishFinal persists an intent, performs a no-replace publish, and records the
// primary artifact and completed status atomically for crash recovery. Native
// no-replace rename primitives are used on Windows, Linux, and macOS; directory
// sync and fallback temporary-name cleanup happen after the visibility commit
// and are retained as diagnostics rather than reported as a failed download.
// After primary publication closes normal mutation sinks, adapters may only
// record a narrowly-scoped post-publish cleanup-failure artifact.
//
// Stop lifecycle:
//
// Pause, cancel, and remove return an accepted StopReceipt immediately. Wails
// commands compare the caller's expected task instance/generation atomically;
// queued work reserves the generation that automatic dispatch later reuses. A
// coordinator waits for the real worker to exit before cleanup and emits a
// revisioned StopEvent with an authoritative terminal PublicDownloadTask.
// Pause and shutdown preserve partials; cancel and remove clean exactly once. A
// timeout leaves the task stopping while background coordination continues.
// Generation-bound mutation sinks reject late writes. Public task events carry
// a manager-wide task instance, execution generation, and event revision. This
// lets clients reject old retry events and fence a removed task ID until a
// genuinely new task instance is created.
//
// Task status:
//
//	pending -> running -> completed
//	                    \-> failed -> (manual retry) -> pending
//	                    \-> paused -> (resume) -> running
//	any state -> canceled (user-initiated)
//
// Basic usage:
//
//	manager := NewDownloadManager("/path/to/downloads", 3)
//	manager.SetProgressCallback(func(task *DownloadTask) { ... })
//	manager.SetCompleteCallback(func(task *DownloadTask) { ... })
//	manager.SetErrorCallback(func(task *DownloadTask, err error) { ... })
//
//	adapter := NewGenericAdapter()
//	manager.RegisterPlatformAdapter(adapter)
//	data, err := MarshalGenericPlatformData(url, nil)
//	task, err := manager.CreateTask(TaskCreationInput{
//	    ID: id, PlatformID: adapter.ID(), Title: title, Cover: cover,
//	    DisplaySource: "generic", SuggestedFilename: title,
//	    SuggestedExtension: ".mp4", PlatformDataVersion: 1, PlatformData: data,
//	})
//	if err != nil {
//	    return err
//	}
//	manager.StartTask(task.ID)
//
// For sources requiring special handling (e.g., Bilibili DASH format), register
// a platform adapter and create tasks with serializable platform data:
//
//	bili := bilibili.NewBilibiliDownloader()
//	adapter := bilibili.NewAdapter(bili)
//	manager.RegisterPlatformAdapter(adapter)
//	data, err := bilibili.MarshalTaskData(videoInfo, quality, partIndex)
//	task, err := manager.CreateTask(TaskCreationInput{
//	    ID: id, PlatformID: adapter.ID(), Title: title, Cover: cover,
//	    DisplaySource: "bilibili", SuggestedFilename: title,
//	    SuggestedExtension: ".mp4", PlatformDataVersion: 1, PlatformData: data,
//	})
package downloader
