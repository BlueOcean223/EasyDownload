// Package xiaohongshu provides content downloading functionality for XiaoHongShu (小红书).
//
// This package implements the XiaoHongShu content parser and downloader, which supports:
//   - Parsing XiaoHongShu share URLs and extracting note metadata
//   - Downloading videos with quality selection
//   - Downloading image albums with resumable support
//   - ZIP packaging for album downloads
//   - Integration with DownloadManager for unified task management
//
// Supported Content Types:
//   - Video notes: downloaded as .mp4 files
//   - Image notes (图文笔记): downloaded as .zip archives containing all images
//
// Safety Features:
//   - Maximum video size limit (configurable, default 2GiB)
//   - No-progress timeout detection for stalled downloads
//   - Image size limit to prevent memory exhaustion
//
// Album Download Features:
//   - Resumable downloads with state persistence
//   - Partial download support (select specific images by index)
//   - Sequential image downloading to respect rate limits
//   - Atomic ZIP creation for crash safety
//
// Usage with DownloadManager:
//
//	client := xiaohongshu.NewClient()
//	item, err := client.ParseURL(shareURL)
//	if err != nil {
//	    return err
//	}
//
//	downloader := xiaohongshu.NewDownloader()
//	downloadFunc := downloader.BuildDownloadFunc(item, nil, "1080p", outputDir)
//	manager.AddTaskWithDownloader(id, url, title, cover, "xiaohongshu", quality, downloadFunc)
package xiaohongshu
