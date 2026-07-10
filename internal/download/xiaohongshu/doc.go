// Package xiaohongshu provides content downloading functionality for XiaoHongShu (小红书).
//
// This package implements the XiaoHongShu content parser and downloader, which supports:
//   - Parsing XiaoHongShu share URLs and extracting note metadata
//   - Downloading videos with codec/weight-aware quality selection
//   - Byte-equivalent backup CDN mirrors with validator-aware resume
//   - Downloading image albums with resumable support and image URL fallback
//   - ZIP packaging for album downloads, including Live Photo video sidecars when present
//   - Platform adapter integration for unified task management
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
//   - Temporary ZIP creation followed by manager-owned PublishFinal
//
// TaskData persists the parsed note, selected images, and quality in the v2
// snapshot. Semantic stream selection stays in this package; all media bytes use
// the injected Fetcher with the configured no-progress timeout (two minutes by
// default); timeout remains compatible with ErrNoProgressTimeout at this boundary.
// EquivalentMirrorURLs must identify the same byte entity. Final MP4/ZIP output
// uses the manager reservation and no-replace PublishFinal transaction.
// The adapter rejects an unknown PlatformDataVersion before decoding TaskData.
//
// Usage with DownloadManager:
//
//	client := xiaohongshu.NewClient()
//	item, err := client.ParseURL(shareURL)
//	if err != nil {
//	    return err
//	}
//
//	platformDownloader := xiaohongshu.NewDownloader()
//	adapter := xiaohongshu.NewAdapter(platformDownloader)
//	manager.RegisterPlatformAdapter(adapter)
//	data, err := xiaohongshu.MarshalTaskData(item, nil, "hd_115")
//	manager.CreateTask(downloader.TaskCreationInput{
//	    ID: id, PlatformID: adapter.ID(), Title: title, Cover: cover,
//	    DisplaySource: "xiaohongshu", SuggestedFilename: title,
//	    SuggestedExtension: ".mp4", PlatformDataVersion: 1, PlatformData: data,
//	})
package xiaohongshu
