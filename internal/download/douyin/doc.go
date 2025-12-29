// Package douyin provides video and album downloading functionality for Douyin (抖音).
//
// This package implements the Douyin content parser and downloader, which supports:
//   - Parsing Douyin share URLs and extracting video/album metadata
//   - Downloading videos with quality selection (720p, 1080p, etc.)
//   - Downloading image albums with concurrent image fetching
//   - Resumable downloads with state persistence for recovery
//   - ZIP packaging for album downloads
//   - Integration with DownloadManager for unified task management
//
// Supported Content Types:
//   - Single videos: downloaded as .mp4 files with selected quality
//   - Image albums (图集): downloaded as .zip archives containing all images
//   - Mixed albums: supports albums with both images and embedded videos
//
// Album Download Features:
//   - Concurrent image downloads (configurable concurrency)
//   - Partial download support (select specific images by index)
//   - Automatic retry with exponential backoff on failure
//   - State persistence via temp directory for resume support
//   - Atomic ZIP creation with temp file and rename
//
// Usage with DownloadManager:
//
//	client := douyin.NewClient()
//	item, err := client.ParseURL(shareURL)
//	if err != nil {
//	    return err
//	}
//
//	downloader := douyin.NewDownloader()
//	downloadFunc := downloader.BuildDownloadFunc(item, "1080p", outputDir)
//	manager.AddTaskWithDownloader(id, url, title, cover, "douyin", quality, downloadFunc)
package douyin
