// Package douyin provides video and album downloading functionality for Douyin (抖音).
//
// This package implements the Douyin content parser and downloader, which supports:
//   - Parsing Douyin share URLs/share text and resolving aweme_id values
//   - Fetching metadata via share-page SSR first, then aweme/detail and slidesinfo fallbacks
//   - Probing ratio-based video streams when bit_rate data is unavailable
//   - Downloading videos with quality selection (720p, 1080p, etc.)
//   - Downloading image albums with concurrent image fetching
//   - Validator/sidecar-gated resume and no-progress timeout through shared Fetch
//   - ZIP packaging for album downloads
//   - Platform adapter integration for unified task management
//
// Supported Content Types:
//   - Single videos: downloaded as .mp4 files with selected quality
//   - Image albums (图集): downloaded as .zip archives containing all images
//   - Mixed albums: supports albums with both images and embedded videos
//
// Album Download Features:
//   - Concurrent image downloads (configurable concurrency)
//   - Partial download support (select specific images by index)
//   - Fetch-owned short retry with exponential backoff; HTTP 429 is opt-in
//   - State persistence via temp directory for resume support
//   - Temporary ZIP creation followed by manager-owned PublishFinal
//
// TaskData persists the parsed item, quality, and selected album indices in the
// v2 snapshot. RunTask selects semantic media candidates, uses only the injected
// Fetcher for bytes, and publishes through the reserved no-replace output path.
// The adapter rejects an unknown PlatformDataVersion before decoding TaskData.
// Pause/shutdown preserve resumable state; cancel/removal clean after worker join.
//
// Usage with DownloadManager:
//
//	parser := douyin.NewParser()
//	awemeID, err := parser.Parse(shareText)
//	if err != nil {
//	    return err
//	}
//
//	client := douyin.NewClient()
//	item, err := client.GetItemInfo(awemeID)
//	if err != nil {
//	    return err
//	}
//
//	platformDownloader := douyin.NewDownloader()
//	adapter := douyin.NewAdapter(platformDownloader)
//	manager.RegisterPlatformAdapter(adapter)
//	data, err := douyin.MarshalTaskData(item, "1080p", nil, false)
//	manager.CreateTask(downloader.TaskCreationInput{
//	    ID: id, PlatformID: adapter.ID(), Title: item.Title, Cover: item.Cover,
//	    DisplaySource: "douyin", SuggestedFilename: item.Title,
//	    SuggestedExtension: ".mp4", PlatformDataVersion: 1, PlatformData: data,
//	})
package douyin
