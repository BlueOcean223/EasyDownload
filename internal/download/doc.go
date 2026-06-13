// Package downloader provides a robust download management system for handling
// file downloads from various sources including WeChat Video Channel, Bilibili,
// Douyin, and XiaoHongShu.
//
// The package supports:
//   - Concurrent download management with configurable limits and a pending queue
//   - Pause, resume, and cancel operations for download tasks
//   - Automatic retry with exponential backoff on failure
//   - Progress tracking and speed calculation
//   - State persistence for recovery after application restart
//   - Custom downloader functions for special sources (e.g., Bilibili DASH)
//   - Optional video decryption for encrypted content (decryption failures mark tasks failed)
//   - Both sequential and strictly validated multipart (chunked) download strategies
//
// Download Strategies:
//   - Sequential: Simple HTTP download with Range support for resume
//   - Multipart: Parallel chunk downloads for large files (faster throughput)
//
// The package automatically selects the best strategy based on:
//   - Server Range request support verified by GET Range (not HEAD alone)
//   - File size (multipart for files > 1MB)
//   - Existing partial download state
//
// Multipart downloads require each chunk to return 206 Partial Content with a
// matching Content-Range. Short reads fail with io.ErrUnexpectedEOF to avoid
// silently producing corrupt output files.
//
// State persistence stores serializable task metadata only. Custom downloader
// functions must be reattached by the application layer after restart, or the
// restored task should be marked as requiring re-parse.
//
// Task Lifecycle:
//
//	pending -> downloading -> completed
//	                      \-> failed -> (manual retry) -> pending
//	                      \-> paused -> (resume) -> downloading
//	downloading -> retrying -> downloading (automatic retry)
//	any state -> cancelled (user-initiated)
//
// Basic usage:
//
//	manager := NewDownloadManager("/path/to/downloads", 3)
//	manager.SetProgressCallback(func(task *DownloadTask) { ... })
//	manager.SetCompleteCallback(func(task *DownloadTask) { ... })
//	manager.SetErrorCallback(func(task *DownloadTask, err error) { ... })
//
//	task, err := manager.AddTask(id, url, title, cover, "wechat", "")
//	if err != nil {
//	    return err
//	}
//	manager.StartTask(task.ID)
//
// For sources requiring special handling (e.g., Bilibili DASH format):
//
//	downloader := bilibili.NewDownloader()
//	downloadFunc := downloader.BuildDownloadFunc(videoInfo, quality, outputDir)
//	task, err := manager.AddTaskWithDownloader(id, url, title, cover, "bilibili", quality, downloadFunc)
package downloader
