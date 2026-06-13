package bilibili

import (
	"EasyDownload/internal/infra/credential"
	"EasyDownload/internal/infra/logger"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

// BilibiliDownloader handles Bilibili video parsing and downloading.
// It supports authenticated downloads for higher quality streams and provides
// resumable download capabilities with progress tracking.
//
// Authentication Flow:
// 1. Call GetQRCode() to generate a QR code for login
// 2. User scans QR code with Bilibili mobile app
// 3. Poll PollQRCodeStatus() until login succeeds
// 4. SESSDATA cookie is automatically saved to secure credential storage
// 5. Subsequent API calls include SESSDATA for authenticated access
//
// Download Flow:
// 1. Parse URL to extract video ID (BV/AV)
// 2. Fetch video info including available qualities
// 3. Select quality and download DASH streams (video + audio separately)
// 4. Merge streams using FFmpeg
type BilibiliDownloader struct {
	sessData      string                 // SESSDATA cookie for authenticated requests (enables higher quality)
	sessDataMu    sync.RWMutex           // Mutex for thread-safe sessData access
	ffmpegPath    string                 // Path to FFmpeg executable (cached after first lookup)
	ffmpegManager FFmpegManagerInterface // Optional FFmpeg manager for advanced merging
	configManager ConfigManagerInterface // Optional config manager for settings persistence
	rateLimiter   *QRCodeRateLimiter     // Rate limiter to prevent excessive QR code polling
}

// NewBilibiliDownloader creates a new BilibiliDownloader instance.
// The downloader is initialized with a rate limiter for QR code polling.
// Call SetSessData(), LoadSessData(), or use QR code login to authenticate.
func NewBilibiliDownloader() *BilibiliDownloader {
	return &BilibiliDownloader{
		rateLimiter: NewQRCodeRateLimiter(),
	}
}

// SetConfigManager sets the configuration manager for settings persistence.
// This enables saving SESSDATA and other settings across sessions.
func (bd *BilibiliDownloader) SetConfigManager(cm ConfigManagerInterface) {
	bd.configManager = cm
}

// SaveSessData saves the SESSDATA cookie to secure credential storage.
// On Windows, this uses Windows Credential Manager; on macOS, the Keychain.
// The SESSDATA is also cached in memory for immediate use.
// Returns an error if secure storage is unavailable.
func (bd *BilibiliDownloader) SaveSessData(sessData string) error {
	bd.sessDataMu.Lock()
	bd.sessData = sessData
	bd.sessDataMu.Unlock()

	// Store to secure credential storage (Windows Credential Manager, macOS Keychain, etc.)
	if err := credential.StoreBilibiliSessData(sessData); err != nil {
		logger.Error("Failed to store SESSDATA: credential storage unavailable")
		return fmt.Errorf("failed to store credential securely")
	}
	logger.Info("SESSDATA stored to secure credential storage")
	return nil
}

// LoadSessData loads the SESSDATA cookie from secure credential storage.
// If found, the SESSDATA is cached in memory for use in subsequent requests.
// Returns the loaded SESSDATA and an error if storage is unavailable.
func (bd *BilibiliDownloader) LoadSessData() (string, error) {
	// Load from secure credential storage
	sessData, err := credential.GetBilibiliSessData()
	if err != nil {
		logger.Error("Failed to load SESSDATA: credential storage unavailable")
		return "", fmt.Errorf("failed to load credential securely")
	}
	if sessData != "" {
		bd.sessDataMu.Lock()
		bd.sessData = sessData
		bd.sessDataMu.Unlock()
		logger.Debug("SESSDATA loaded from secure credential storage")
	}
	return sessData, nil
}

// GetSessData returns the current SESSDATA cookie value.
// This method is thread-safe and can be called concurrently.
func (bd *BilibiliDownloader) GetSessData() string {
	bd.sessDataMu.RLock()
	defer bd.sessDataMu.RUnlock()
	return bd.sessData
}

// SetSessData sets the SESSDATA cookie for authenticated API requests.
// This enables access to higher quality streams (1080P+, 4K, etc.).
// This method is thread-safe and can be called concurrently.
func (bd *BilibiliDownloader) SetSessData(sessData string) {
	bd.sessDataMu.Lock()
	defer bd.sessDataMu.Unlock()
	bd.sessData = sessData
}

// Logout clears the SESSDATA from both memory and secure storage.
// After logout, API requests will be unauthenticated and limited to lower qualities.
// Returns an error if credential deletion fails.
func (bd *BilibiliDownloader) Logout() error {
	bd.sessDataMu.Lock()
	bd.sessData = ""
	bd.sessDataMu.Unlock()

	if err := credential.DeleteBilibiliSessData(); err != nil {
		logger.Error("Failed to delete SESSDATA: credential storage unavailable")
		return fmt.Errorf("failed to delete credential securely")
	}
	logger.Info("SESSDATA deleted from secure credential storage")
	return nil
}

// SetFFmpegPath manually sets the path to the FFmpeg executable.
// FFmpeg is required for downloading DASH format videos (video + audio streams).
func (bd *BilibiliDownloader) SetFFmpegPath(path string) {
	bd.ffmpegPath = path
}

// SetFFmpegManager sets the FFmpeg manager interface for video/audio merging.
// The manager provides more advanced control over the merge process.
func (bd *BilibiliDownloader) SetFFmpegManager(fm FFmpegManagerInterface) {
	bd.ffmpegManager = fm
}

// GetFFmpegPath returns the path to the FFmpeg executable.
// It checks in the following order:
// 1. FFmpegManager (if set)
// 2. Manually set path via SetFFmpegPath
// 3. Common system locations (PATH, ./ffmpeg.exe, C:\ffmpeg\bin\, etc.)
// Returns an empty string if FFmpeg is not found.
func (bd *BilibiliDownloader) GetFFmpegPath() string {
	// First check if FFmpegManager is set
	if bd.ffmpegManager != nil {
		path := bd.ffmpegManager.GetPath()
		if path != "" {
			return path
		}
	}

	if bd.ffmpegPath != "" {
		return bd.ffmpegPath
	}

	// Check common locations
	paths := []string{
		"ffmpeg",
		"ffmpeg.exe",
		filepath.Join(".", "ffmpeg.exe"),
		filepath.Join(".", "bin", "ffmpeg.exe"),
	}

	if runtime.GOOS == "windows" {
		paths = append(paths,
			`C:\ffmpeg\bin\ffmpeg.exe`,
			`C:\Program Files\ffmpeg\bin\ffmpeg.exe`,
		)
	}

	for _, p := range paths {
		if _, err := exec.LookPath(p); err == nil {
			bd.ffmpegPath = p
			return p
		}
	}

	return ""
}

// IsFFmpegAvailable checks if FFmpeg is available on the system.
// FFmpeg is required for downloading DASH format videos where video and audio
// are separate streams that need to be merged.
func (bd *BilibiliDownloader) IsFFmpegAvailable() bool {
	// First check FFmpegManager
	if bd.ffmpegManager != nil && bd.ffmpegManager.IsAvailable() {
		return true
	}
	return bd.GetFFmpegPath() != ""
}

// DownloadPart downloads a specific part of a Bilibili video.
// Parameters:
//   - video: Video metadata obtained from GetVideoInfo or GetVideoInfoWithParts
//   - partIndex: 0-based index of the part to download
//   - quality: Desired quality level (qn value, e.g., 80 for 1080P, 120 for 4K)
//   - outputPath: Full output file path including filename and .mp4 extension
//   - onProgress: Callback for progress updates (0-100%)
//   - onSizeKnown: Callback when total download size is determined
//
// Returns the final output path and any error encountered.
func (bd *BilibiliDownloader) DownloadPart(video *BilibiliVideo, partIndex int, quality int, outputPath string, onProgress func(float64), onSizeKnown func(int64)) (string, error) {
	return bd.DownloadPartWithContext(context.Background(), video, partIndex, quality, outputPath, onProgress, onSizeKnown)
}

// DownloadPartWithContext downloads a specific part with cancellation support.
// The context can be used to cancel or pause the download.
// When cancelled, temporary files are preserved for resume capability.
// See DownloadPart for parameter descriptions.
func (bd *BilibiliDownloader) DownloadPartWithContext(ctx context.Context, video *BilibiliVideo, partIndex int, quality int, outputPath string, onProgress func(float64), onSizeKnown func(int64)) (string, error) {
	if partIndex < 0 || partIndex >= len(video.Parts) {
		return "", fmt.Errorf("invalid part index: %d (total parts: %d)", partIndex, len(video.Parts))
	}

	part := video.Parts[partIndex]
	logger.Info("Starting download for video: %s, Part %d: %s (quality: %d)", video.Title, part.Page, part.PartName, quality)

	return bd.downloadCore(ctx, video, partIndex, quality, outputPath, onProgress, onSizeKnown)
}

// downloadCore is the core download method that handles DASH format downloads.
// It downloads video and audio streams separately, then merges them using FFmpeg.
//
// Parameters:
//   - ctx: Context for cancellation support
//   - video: Video metadata
//   - partIndex: Part index (-1 uses video.Streams for backward compatibility, >=0 for specific part)
//   - quality: Desired quality level (qn value)
//   - outputPath: Full output path including filename (used to derive temp file paths)
//   - onProgress: Progress callback (0-100%)
//   - onSizeKnown: Callback when total size is known
//
// Download process:
// 1. Fetch stream URLs for the requested quality
// 2. Download video stream to {basePath}_video.m4s
// 3. Download audio stream to {basePath}_audio.m4s
// 4. Merge using FFmpeg to create final .mp4 file
// 5. Clean up temporary files
func (bd *BilibiliDownloader) downloadCore(
	ctx context.Context,
	video *BilibiliVideo,
	partIndex int,
	quality int,
	outputPath string,
	onProgress func(float64),
	onSizeKnown func(int64),
) (string, error) {
	// 获取流信息
	var streams []BilibiliStream
	var err error

	if partIndex == -1 {
		// 使用第一分P的流（向后兼容）
		streams = video.Streams
	} else {
		// 获取指定分P的流
		streams, err = bd.GetPartStreams(video, partIndex)
		if err != nil {
			return "", fmt.Errorf("failed to get streams for part %d: %w", partIndex, err)
		}
	}

	// 查找匹配质量的 stream
	var stream *BilibiliStream
	for i := range streams {
		if streams[i].Quality == quality {
			stream = &streams[i]
			break
		}
	}

	if stream == nil && len(streams) > 0 {
		stream = &streams[0]
		logger.Debug("Requested quality %d not found, using quality %d", quality, stream.Quality)
	}

	if stream == nil {
		logger.Error("No available stream for video: %s", video.Title)
		return "", fmt.Errorf("no available stream")
	}
	if (stream.BiliDRMURI != "" || stream.DRMTechType == 2) && stream.DRMKey == "" {
		logger.Error("DRM stream detected without decryption key for video: %s", video.Title)
		return "", fmt.Errorf("this stream is DRM-protected and cannot be downloaded yet")
	}

	// 校验 outputPath 必须以 .mp4 结尾
	if !strings.HasSuffix(outputPath, ".mp4") {
		logger.Warn("outputPath does not end with .mp4, appending: %s", outputPath)
		outputPath = outputPath + ".mp4"
	}

	// 使用传入的 outputPath，确保与 task.FilePath 一致
	// 从 outputPath 派生临时文件路径
	basePath := strings.TrimSuffix(outputPath, ".mp4")
	logger.Debug("Using output path: %s, base path: %s", outputPath, basePath)

	// 处理旧格式（无音频）
	if stream.AudioURL == "" && len(stream.AudioBackupURLs) == 0 {
		return bd.downloadFileWithFallback(ctx, stream.VideoURL, stream.BackupURLs, outputPath, 0, onProgress)
	}

	// DASH 格式 - 需要分别下载视频和音频，然后合并
	if !bd.IsFFmpegAvailable() {
		logger.Error("FFmpeg not found, cannot download DASH format video")
		return "", fmt.Errorf("ffmpeg is required for DASH format but not found")
	}
	logger.Debug("Using DASH format, will merge video and audio")

	// 获取内容长度以进行精确的进度计算；主 URL 获取失败时尝试备用 URL
	videoSize := bd.getContentLengthWithFallback(stream.VideoURL, stream.BackupURLs)
	audioSize := bd.getContentLengthWithFallback(stream.AudioURL, stream.AudioBackupURLs)
	logger.Debug("Video size: %d, Audio size: %d", videoSize, audioSize)

	// 通知调用者总文件大小
	if onSizeKnown != nil {
		onSizeKnown(videoSize + audioSize)
	}

	// 创建进度追踪器
	tracker := NewDASHProgressTracker(videoSize, audioSize, onProgress)

	// 临时文件路径 - 使用 basePath 确保与 task.FilePath 一致
	videoPath := basePath + "_video.m4s"
	audioPath := basePath + "_audio.m4s"
	logger.Debug("Temp files: video=%s, audio=%s", videoPath, audioPath)

	// 清理临时文件的辅助函数
	cleanupTempFiles := func() {
		os.Remove(videoPath)
		os.Remove(audioPath)
		os.Remove(videoPath + ".edstate.json")
		os.Remove(audioPath + ".edstate.json")
		logger.Debug("Cleaned up temp files: %s, %s", videoPath, audioPath)
	}

	// 下载视频
	logger.Debug("Downloading video stream to: %s (backups: %d)", videoPath, len(stream.BackupURLs))
	if _, err := bd.downloadFileWithFallback(ctx, stream.VideoURL, stream.BackupURLs, videoPath, videoSize, func(p float64) {
		tracker.UpdateVideoProgress(p)
	}); err != nil {
		// 如果上下文被取消（暂停/取消），保留临时文件以便恢复
		if ctx.Err() != nil {
			logger.Info("Video download interrupted, keeping temp files for resume")
			return "", err
		}
		logger.Error("Failed to download video stream: %v", err)
		cleanupTempFiles()
		return "", fmt.Errorf("failed to download video: %w", err)
	}

	// 在音频下载前检查取消
	select {
	case <-ctx.Done():
		logger.Info("Download paused/cancelled after video, keeping temp files")
		return "", ctx.Err()
	default:
	}

	// 下载音频
	logger.Debug("Downloading audio stream to: %s (backups: %d)", audioPath, len(stream.AudioBackupURLs))
	if _, err := bd.downloadFileWithFallback(ctx, stream.AudioURL, stream.AudioBackupURLs, audioPath, audioSize, func(p float64) {
		tracker.UpdateAudioProgress(p)
	}); err != nil {
		// 如果上下文被取消（暂停/取消），保留临时文件以便恢复
		if ctx.Err() != nil {
			logger.Info("Audio download interrupted, keeping temp files for resume")
			return "", err
		}
		logger.Error("Failed to download audio stream: %v", err)
		cleanupTempFiles()
		return "", fmt.Errorf("failed to download audio: %w", err)
	}

	// 在合并前检查取消
	select {
	case <-ctx.Done():
		logger.Info("Download paused/cancelled before merge, keeping temp files")
		return "", ctx.Err()
	default:
	}

	// 使用 ffmpeg 合并
	logger.Debug("Merging video and audio with FFmpeg")
	tracker.SetMergeProgress(0)

	// 如果可用，使用 FFmpegManager，否则回退到直接执行
	if bd.ffmpegManager != nil && stream.DRMKey == "" {
		var err error
		if ctxMerger, ok := bd.ffmpegManager.(interface {
			MergeWithContext(context.Context, string, string, string) error
		}); ok {
			err = ctxMerger.MergeWithContext(ctx, videoPath, audioPath, outputPath)
		} else {
			err = bd.ffmpegManager.Merge(videoPath, audioPath, outputPath)
		}
		if err != nil {
			if ctx.Err() != nil {
				logger.Info("Merge interrupted, keeping temp files")
				return "", ctx.Err()
			}
			logger.Error("FFmpeg merge failed: %v", err)
			cleanupTempFiles()
			return "", fmt.Errorf("failed to merge video and audio: %w", err)
		}
	} else if bd.ffmpegManager != nil && stream.DRMKey != "" {
		var err error
		if drmMerger, ok := bd.ffmpegManager.(interface {
			MergeWithDecryptionWithContext(context.Context, string, string, string, string) error
		}); ok {
			err = drmMerger.MergeWithDecryptionWithContext(ctx, videoPath, audioPath, outputPath, stream.DRMKey)
		} else if drmMerger, ok := bd.ffmpegManager.(interface {
			MergeWithDecryption(videoPath, audioPath, outputPath, decryptionKey string) error
		}); ok {
			err = drmMerger.MergeWithDecryption(videoPath, audioPath, outputPath, stream.DRMKey)
		} else {
			err = bd.mergeWithFFmpegCommand(ctx, videoPath, audioPath, outputPath, stream.DRMKey)
		}
		if err != nil {
			if ctx.Err() != nil {
				logger.Info("Merge interrupted, keeping temp files")
				return "", ctx.Err()
			}
			logger.Error("FFmpeg DRM merge failed: %v", err)
			cleanupTempFiles()
			return "", fmt.Errorf("failed to merge DRM video and audio: %w", err)
		}
	} else {
		if err := bd.mergeWithFFmpegCommand(ctx, videoPath, audioPath, outputPath, ""); err != nil {
			// 如果在合并期间上下文被取消，保留临时文件
			if ctx.Err() != nil {
				logger.Info("Merge interrupted, keeping temp files")
				return "", ctx.Err()
			}
			logger.Error("FFmpeg merge failed: %v", err)
			cleanupTempFiles()
			return "", fmt.Errorf("failed to merge video and audio: %w", err)
		}
	}

	tracker.SetMergeProgress(100)

	// 成功合并后清理临时文件
	cleanupTempFiles()

	logger.Info("Download completed: %s", outputPath)
	return outputPath, nil
}

func (bd *BilibiliDownloader) mergeWithFFmpegCommand(ctx context.Context, videoPath, audioPath, outputPath, decryptionKey string) error {
	args := make([]string, 0, 10)
	if decryptionKey != "" {
		args = append(args, "-decryption_key", decryptionKey)
	}
	args = append(args, "-i", videoPath)
	if decryptionKey != "" {
		args = append(args, "-decryption_key", decryptionKey)
	}
	args = append(args,
		"-i", audioPath,
		"-c", "copy",
		"-y",
		outputPath,
	)

	cmd := exec.CommandContext(ctx, bd.GetFFmpegPath(), args...)
	return cmd.Run()
}

// Download downloads the first part of a Bilibili video (for single-part videos).
// For multi-part videos, use DownloadPart to download specific parts.
// Parameters:
//   - video: Video metadata obtained from GetVideoInfo
//   - quality: Desired quality level (qn value)
//   - outputPath: Full output file path including filename and .mp4 extension
//   - onProgress: Callback for progress updates (0-100%)
//   - onSizeKnown: Callback when total download size is determined
func (bd *BilibiliDownloader) Download(video *BilibiliVideo, quality int, outputPath string, onProgress func(progress float64), onSizeKnown func(totalSize int64)) (string, error) {
	return bd.DownloadWithContext(context.Background(), video, quality, outputPath, onProgress, onSizeKnown)
}

// DownloadWithContext downloads the first part of a video with cancellation support.
// The context can be used to cancel or pause the download.
// See Download for parameter descriptions.
func (bd *BilibiliDownloader) DownloadWithContext(ctx context.Context, video *BilibiliVideo, quality int, outputPath string, onProgress func(progress float64), onSizeKnown func(totalSize int64)) (string, error) {
	logger.Info("Starting download for video: %s (quality: %d)", video.Title, quality)
	return bd.downloadCore(ctx, video, -1, quality, outputPath, onProgress, onSizeKnown)
}
