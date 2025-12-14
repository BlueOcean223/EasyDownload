package downloader

import (
	"EasyDownload/internal/config"
	"EasyDownload/internal/logger"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

// BilibiliVideo represents a Bilibili video information
type BilibiliVideo struct {
	BV       string           `json:"bv"`
	AV       string           `json:"av"`
	Title    string           `json:"title"`
	Cover    string           `json:"cover"`
	Author   string           `json:"author"`
	Duration int              `json:"duration"`
	Desc     string           `json:"desc"`
	Parts    []BilibiliPart   `json:"parts"` // Multi-part video list
	Streams  []BilibiliStream `json:"streams"`
}

// BilibiliPart represents a video part (分P) information
type BilibiliPart struct {
	CID      int64  `json:"cid"`
	Page     int    `json:"page"`
	PartName string `json:"partName"`
	Duration int    `json:"duration"`
}

// BilibiliStream represents a video stream option
type BilibiliStream struct {
	Quality     int    `json:"quality"`
	QualityName string `json:"qualityName"`
	Format      string `json:"format"`
	Size        int64  `json:"size"`
	VideoURL    string `json:"videoUrl"`
	AudioURL    string `json:"audioUrl"`
}

// FFmpegManagerInterface defines the interface for FFmpeg management
type FFmpegManagerInterface interface {
	GetPath() string
	IsAvailable() bool
	Merge(videoPath, audioPath, outputPath string) error
}

// BilibiliDownloader handles Bilibili video downloads
type BilibiliDownloader struct {
	sessData      string // Cookie SESSDATA for higher quality
	ffmpegPath    string
	ffmpegManager FFmpegManagerInterface
	configManager ConfigManagerInterface
}

// ConfigManagerInterface defines the interface for config management
type ConfigManagerInterface interface {
	Get() *config.Config
	Set(key string, value any) error
}

// NewBilibiliDownloader creates a new BilibiliDownloader
func NewBilibiliDownloader() *BilibiliDownloader {
	return &BilibiliDownloader{}
}

// SetConfigManager sets the config manager for SESSDATA persistence
func (bd *BilibiliDownloader) SetConfigManager(cm ConfigManagerInterface) {
	bd.configManager = cm
}

// SaveSessData saves the SESSDATA to persistent storage via config manager
func (bd *BilibiliDownloader) SaveSessData(sessData string) error {
	bd.sessData = sessData
	if bd.configManager != nil {
		return bd.configManager.Set("bilibiliSessData", sessData)
	}
	return nil
}

// LoadSessData loads the SESSDATA from persistent storage via config manager
func (bd *BilibiliDownloader) LoadSessData() (string, error) {
	if bd.configManager != nil {
		cfg := bd.configManager.Get()
		if cfg != nil && cfg.BilibiliSessData != "" {
			bd.sessData = cfg.BilibiliSessData
			return bd.sessData, nil
		}
	}
	return bd.sessData, nil
}

// GetSessData returns the current SESSDATA
func (bd *BilibiliDownloader) GetSessData() string {
	return bd.sessData
}

// SetSessData sets the Bilibili session cookie for authenticated requests
func (bd *BilibiliDownloader) SetSessData(sessData string) {
	bd.sessData = sessData
}

// SetFFmpegPath sets the path to ffmpeg executable
func (bd *BilibiliDownloader) SetFFmpegPath(path string) {
	bd.ffmpegPath = path
}

// SetFFmpegManager sets the FFmpeg manager for video/audio merging
func (bd *BilibiliDownloader) SetFFmpegManager(fm FFmpegManagerInterface) {
	bd.ffmpegManager = fm
}

// GetFFmpegPath returns the ffmpeg path, checking common locations
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

// IsFFmpegAvailable checks if ffmpeg is available
func (bd *BilibiliDownloader) IsFFmpegAvailable() bool {
	// First check FFmpegManager
	if bd.ffmpegManager != nil && bd.ffmpegManager.IsAvailable() {
		return true
	}
	return bd.GetFFmpegPath() != ""
}

// ParseURL parses a Bilibili URL and returns the video ID
func (bd *BilibiliDownloader) ParseURL(url string) (string, error) {
	// BV format
	bvRegex := regexp.MustCompile(`BV[a-zA-Z0-9]+`)
	if matches := bvRegex.FindString(url); matches != "" {
		return matches, nil
	}

	// AV format
	avRegex := regexp.MustCompile(`av(\d+)`)
	if matches := avRegex.FindStringSubmatch(url); len(matches) > 1 {
		return "av" + matches[1], nil
	}

	return "", fmt.Errorf("invalid Bilibili URL")
}

// ParseURLMust parses a Bilibili URL and returns the video ID, panics on error
func (bd *BilibiliDownloader) ParseURLMust(url string) string {
	bvid, err := bd.ParseURL(url)
	if err != nil {
		return ""
	}
	return bvid
}

// GetVideoInfo fetches video information from Bilibili (first part only for backward compatibility)
func (bd *BilibiliDownloader) GetVideoInfo(bvid string) (*BilibiliVideo, error) {
	video, err := bd.GetVideoInfoWithParts(bvid)
	if err != nil {
		return nil, err
	}

	// Get stream info for the first part
	if len(video.Parts) > 0 {
		aid := int64(0)
		fmt.Sscanf(video.AV, "av%d", &aid)
		streams, err := bd.getStreamInfo(bvid, aid, video.Parts[0].CID)
		if err == nil {
			video.Streams = streams
		}
	}

	return video, nil
}

// GetVideoInfoWithParts fetches video information including all parts from Bilibili
func (bd *BilibiliDownloader) GetVideoInfoWithParts(bvid string) (*BilibiliVideo, error) {
	// API endpoint
	apiURL := fmt.Sprintf("https://api.bilibili.com/x/web-interface/view?bvid=%s", bvid)

	req, err := http.NewRequest("GET", apiURL, nil)
	if err != nil {
		return nil, err
	}

	bd.setHeaders(req)

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var result struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    struct {
			BVID     string `json:"bvid"`
			AID      int64  `json:"aid"`
			Title    string `json:"title"`
			Pic      string `json:"pic"`
			Desc     string `json:"desc"`
			Duration int    `json:"duration"`
			Owner    struct {
				Name string `json:"name"`
			} `json:"owner"`
			CID   int64 `json:"cid"`
			Pages []struct {
				CID      int64  `json:"cid"`
				Page     int    `json:"page"`
				Part     string `json:"part"`
				Duration int    `json:"duration"`
			} `json:"pages"`
		} `json:"data"`
	}

	if err := json.Unmarshal(body, &result); err != nil {
		return nil, err
	}

	if result.Code != 0 {
		return nil, fmt.Errorf("API error: %s", result.Message)
	}

	video := &BilibiliVideo{
		BV:       result.Data.BVID,
		AV:       fmt.Sprintf("av%d", result.Data.AID),
		Title:    result.Data.Title,
		Cover:    result.Data.Pic,
		Author:   result.Data.Owner.Name,
		Duration: result.Data.Duration,
		Desc:     result.Data.Desc,
		Parts:    make([]BilibiliPart, 0, len(result.Data.Pages)),
	}

	// Parse parts (分P)
	if len(result.Data.Pages) > 0 {
		for _, page := range result.Data.Pages {
			part := BilibiliPart{
				CID:      page.CID,
				Page:     page.Page,
				PartName: page.Part,
				Duration: page.Duration,
			}
			video.Parts = append(video.Parts, part)
		}
	} else {
		// Single part video - create a default part
		video.Parts = append(video.Parts, BilibiliPart{
			CID:      result.Data.CID,
			Page:     1,
			PartName: result.Data.Title,
			Duration: result.Data.Duration,
		})
	}

	return video, nil
}

// getStreamInfo fetches available stream qualities
func (bd *BilibiliDownloader) getStreamInfo(bvid string, _ int64, cid int64) ([]BilibiliStream, error) {
	// Play URL API
	apiURL := fmt.Sprintf("https://api.bilibili.com/x/player/playurl?bvid=%s&cid=%d&fnval=4048&fnver=0&fourk=1", bvid, cid)

	req, err := http.NewRequest("GET", apiURL, nil)
	if err != nil {
		return nil, err
	}

	bd.setHeaders(req)

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var result struct {
		Code int `json:"code"`
		Data struct {
			Quality       int      `json:"quality"`
			AcceptQuality []int    `json:"accept_quality"`
			AcceptDesc    []string `json:"accept_description"`
			Dash          struct {
				Video []struct {
					ID        int    `json:"id"`
					BaseURL   string `json:"baseUrl"`
					Bandwidth int64  `json:"bandwidth"`
					Codecs    string `json:"codecs"`
				} `json:"video"`
				Audio []struct {
					ID        int    `json:"id"`
					BaseURL   string `json:"baseUrl"`
					Bandwidth int64  `json:"bandwidth"`
				} `json:"audio"`
			} `json:"dash"`
		} `json:"data"`
	}

	if err := json.Unmarshal(body, &result); err != nil {
		return nil, err
	}

	if result.Code != 0 {
		return nil, fmt.Errorf("failed to get stream info")
	}

	qualityNames := map[int]string{
		127: "8K",
		126: "杜比视界",
		125: "HDR",
		120: "4K",
		116: "1080P60",
		112: "1080P+",
		80:  "1080P",
		74:  "720P60",
		64:  "720P",
		32:  "480P",
		16:  "360P",
	}

	var streams []BilibiliStream

	// Create stream entries for each quality
	for i, q := range result.Data.AcceptQuality {
		stream := BilibiliStream{
			Quality:     q,
			QualityName: qualityNames[q],
			Format:      "dash",
		}

		if i < len(result.Data.AcceptDesc) {
			stream.QualityName = result.Data.AcceptDesc[i]
		}

		// Find matching video stream
		for _, v := range result.Data.Dash.Video {
			if v.ID == q {
				stream.VideoURL = v.BaseURL
				break
			}
		}

		// Get first audio stream
		if len(result.Data.Dash.Audio) > 0 {
			stream.AudioURL = result.Data.Dash.Audio[0].BaseURL
		}

		if stream.VideoURL != "" {
			streams = append(streams, stream)
		}
	}

	return streams, nil
}

// GetPartStreams fetches available stream qualities for a specific part
func (bd *BilibiliDownloader) GetPartStreams(video *BilibiliVideo, partIndex int) ([]BilibiliStream, error) {
	if partIndex < 0 || partIndex >= len(video.Parts) {
		return nil, fmt.Errorf("invalid part index: %d (total parts: %d)", partIndex, len(video.Parts))
	}

	aid := int64(0)
	fmt.Sscanf(video.AV, "av%d", &aid)

	return bd.getStreamInfo(video.BV, aid, video.Parts[partIndex].CID)
}

// DownloadPart downloads a specific part of a Bilibili video
func (bd *BilibiliDownloader) DownloadPart(video *BilibiliVideo, partIndex int, quality int, outputDir string, onProgress func(float64), onSizeKnown func(int64)) (string, error) {
	return bd.DownloadPartWithContext(context.Background(), video, partIndex, quality, outputDir, onProgress, onSizeKnown)
}

// DownloadPartWithContext downloads a specific part of a Bilibili video with context support for cancellation
func (bd *BilibiliDownloader) DownloadPartWithContext(ctx context.Context, video *BilibiliVideo, partIndex int, quality int, outputDir string, onProgress func(float64), onSizeKnown func(int64)) (string, error) {
	if partIndex < 0 || partIndex >= len(video.Parts) {
		return "", fmt.Errorf("invalid part index: %d (total parts: %d)", partIndex, len(video.Parts))
	}

	part := video.Parts[partIndex]
	logger.Info("Starting download for video: %s, Part %d: %s (quality: %d)", video.Title, part.Page, part.PartName, quality)

	// Get streams for this specific part
	streams, err := bd.GetPartStreams(video, partIndex)
	if err != nil {
		return "", fmt.Errorf("failed to get streams for part %d: %w", partIndex, err)
	}

	// Find the requested quality stream
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
		logger.Error("No available stream for video part: %s - %s", video.Title, part.PartName)
		return "", fmt.Errorf("no available stream")
	}

	// Sanitize filename - include part number if multi-part
	var fileName string
	if len(video.Parts) > 1 {
		fileName = sanitizeFileName(fmt.Sprintf("%s_P%d_%s", video.Title, part.Page, part.PartName))
	} else {
		fileName = sanitizeFileName(video.Title)
	}
	outputPath := filepath.Join(outputDir, fileName+".mp4")

	// If no audio (old format), direct download
	if stream.AudioURL == "" {
		return bd.downloadFileWithContext(ctx, stream.VideoURL, outputPath, 0, onProgress)
	}

	// DASH format - need to download video and audio separately, then merge
	if !bd.IsFFmpegAvailable() {
		logger.Error("FFmpeg not found, cannot download DASH format video")
		return "", fmt.Errorf("ffmpeg is required for DASH format but not found")
	}
	logger.Debug("Using DASH format, will merge video and audio")

	// Get content lengths for accurate progress calculation
	videoSize, _ := bd.getContentLength(stream.VideoURL)
	audioSize, _ := bd.getContentLength(stream.AudioURL)
	logger.Debug("Video size: %d, Audio size: %d", videoSize, audioSize)

	// Notify caller of total file size
	if onSizeKnown != nil {
		onSizeKnown(videoSize + audioSize)
	}

	// Create progress tracker
	tracker := NewDASHProgressTracker(videoSize, audioSize, onProgress)

	// Temp file paths
	videoPath := filepath.Join(outputDir, fileName+"_video.m4s")
	audioPath := filepath.Join(outputDir, fileName+"_audio.m4s")

	// Helper to clean up temp files
	cleanupTempFiles := func() {
		os.Remove(videoPath)
		os.Remove(audioPath)
	}

	// Download video
	logger.Debug("Downloading video stream to: %s", videoPath)
	if _, err := bd.downloadFileWithContext(ctx, stream.VideoURL, videoPath, videoSize, func(p float64) {
		tracker.UpdateVideoProgress(p)
	}); err != nil {
		// If context was cancelled (pause/cancel), keep temp files for potential resume
		if ctx.Err() != nil {
			logger.Info("Video download interrupted, keeping temp files for resume")
			return "", err
		}
		logger.Error("Failed to download video stream: %v", err)
		cleanupTempFiles()
		return "", fmt.Errorf("failed to download video: %w", err)
	}

	// Check for cancellation before audio download
	select {
	case <-ctx.Done():
		logger.Info("Download paused/cancelled after video, keeping temp files")
		return "", ctx.Err()
	default:
	}

	// Download audio
	logger.Debug("Downloading audio stream to: %s", audioPath)
	if _, err := bd.downloadFileWithContext(ctx, stream.AudioURL, audioPath, audioSize, func(p float64) {
		tracker.UpdateAudioProgress(p)
	}); err != nil {
		// If context was cancelled (pause/cancel), keep temp files for potential resume
		if ctx.Err() != nil {
			logger.Info("Audio download interrupted, keeping temp files for resume")
			return "", err
		}
		logger.Error("Failed to download audio stream: %v", err)
		cleanupTempFiles()
		return "", fmt.Errorf("failed to download audio: %w", err)
	}

	// Check for cancellation before merge
	select {
	case <-ctx.Done():
		logger.Info("Download paused/cancelled before merge, keeping temp files")
		return "", ctx.Err()
	default:
	}

	// Merge with ffmpeg
	logger.Debug("Merging video and audio with FFmpeg")
	tracker.SetMergeProgress(0)

	// Use FFmpegManager if available, otherwise fall back to direct exec
	if bd.ffmpegManager != nil {
		if err := bd.ffmpegManager.Merge(videoPath, audioPath, outputPath); err != nil {
			logger.Error("FFmpeg merge failed: %v", err)
			cleanupTempFiles()
			return "", fmt.Errorf("failed to merge video and audio: %w", err)
		}
	} else {
		cmd := exec.CommandContext(ctx, bd.GetFFmpegPath(),
			"-i", videoPath,
			"-i", audioPath,
			"-c", "copy",
			"-y",
			outputPath,
		)

		if err := cmd.Run(); err != nil {
			// If context was cancelled during merge, keep temp files
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

	// Clean up temp files after successful merge
	cleanupTempFiles()

	logger.Info("Download completed: %s", outputPath)
	return outputPath, nil
}

// Download downloads a Bilibili video (first part for backward compatibility)
func (bd *BilibiliDownloader) Download(video *BilibiliVideo, quality int, outputDir string, onProgress func(progress float64), onSizeKnown func(totalSize int64)) (string, error) {
	return bd.DownloadWithContext(context.Background(), video, quality, outputDir, onProgress, onSizeKnown)
}

// DownloadWithContext downloads a Bilibili video with context support for cancellation
func (bd *BilibiliDownloader) DownloadWithContext(ctx context.Context, video *BilibiliVideo, quality int, outputDir string, onProgress func(progress float64), onSizeKnown func(totalSize int64)) (string, error) {
	logger.Info("Starting download for video: %s (quality: %d)", video.Title, quality)

	// Find the requested quality stream
	var stream *BilibiliStream
	for i := range video.Streams {
		if video.Streams[i].Quality == quality {
			stream = &video.Streams[i]
			break
		}
	}

	if stream == nil && len(video.Streams) > 0 {
		stream = &video.Streams[0]
		logger.Debug("Requested quality %d not found, using quality %d", quality, stream.Quality)
	}

	if stream == nil {
		logger.Error("No available stream for video: %s", video.Title)
		return "", fmt.Errorf("no available stream")
	}

	// Sanitize filename
	fileName := sanitizeFileName(video.Title)
	outputPath := filepath.Join(outputDir, fileName+".mp4")

	// If no audio (old format), direct download
	if stream.AudioURL == "" {
		return bd.downloadFileWithContext(ctx, stream.VideoURL, outputPath, 0, onProgress)
	}

	// DASH format - need to download video and audio separately, then merge
	if !bd.IsFFmpegAvailable() {
		logger.Error("FFmpeg not found, cannot download DASH format video")
		return "", fmt.Errorf("ffmpeg is required for DASH format but not found")
	}
	logger.Debug("Using DASH format, will merge video and audio")

	// Get content lengths for accurate progress calculation
	videoSize, _ := bd.getContentLength(stream.VideoURL)
	audioSize, _ := bd.getContentLength(stream.AudioURL)
	logger.Debug("Video size: %d, Audio size: %d", videoSize, audioSize)

	// Notify caller of total file size
	if onSizeKnown != nil {
		onSizeKnown(videoSize + audioSize)
	}

	// Create progress tracker
	tracker := NewDASHProgressTracker(videoSize, audioSize, onProgress)

	// Temp file paths
	videoPath := filepath.Join(outputDir, fileName+"_video.m4s")
	audioPath := filepath.Join(outputDir, fileName+"_audio.m4s")

	// Helper to clean up temp files
	cleanupTempFiles := func() {
		os.Remove(videoPath)
		os.Remove(audioPath)
	}

	// Download video
	logger.Debug("Downloading video stream to: %s", videoPath)
	if _, err := bd.downloadFileWithContext(ctx, stream.VideoURL, videoPath, videoSize, func(p float64) {
		tracker.UpdateVideoProgress(p)
	}); err != nil {
		// If context was cancelled (pause/cancel), keep temp files for potential resume
		if ctx.Err() != nil {
			logger.Info("Video download interrupted, keeping temp files for resume")
			return "", err
		}
		logger.Error("Failed to download video stream: %v", err)
		cleanupTempFiles()
		return "", fmt.Errorf("failed to download video: %w", err)
	}

	// Check for cancellation before audio download
	select {
	case <-ctx.Done():
		logger.Info("Download paused/cancelled after video, keeping temp files")
		return "", ctx.Err()
	default:
	}

	// Download audio
	logger.Debug("Downloading audio stream to: %s", audioPath)
	if _, err := bd.downloadFileWithContext(ctx, stream.AudioURL, audioPath, audioSize, func(p float64) {
		tracker.UpdateAudioProgress(p)
	}); err != nil {
		// If context was cancelled (pause/cancel), keep temp files for potential resume
		if ctx.Err() != nil {
			logger.Info("Audio download interrupted, keeping temp files for resume")
			return "", err
		}
		logger.Error("Failed to download audio stream: %v", err)
		cleanupTempFiles()
		return "", fmt.Errorf("failed to download audio: %w", err)
	}

	// Check for cancellation before merge
	select {
	case <-ctx.Done():
		logger.Info("Download paused/cancelled before merge, keeping temp files")
		return "", ctx.Err()
	default:
	}

	// Merge with ffmpeg
	logger.Debug("Merging video and audio with FFmpeg")
	tracker.SetMergeProgress(0)

	// Use FFmpegManager if available, otherwise fall back to direct exec
	if bd.ffmpegManager != nil {
		if err := bd.ffmpegManager.Merge(videoPath, audioPath, outputPath); err != nil {
			logger.Error("FFmpeg merge failed: %v", err)
			cleanupTempFiles()
			return "", fmt.Errorf("failed to merge video and audio: %w", err)
		}
	} else {
		cmd := exec.CommandContext(ctx, bd.GetFFmpegPath(),
			"-i", videoPath,
			"-i", audioPath,
			"-c", "copy",
			"-y",
			outputPath,
		)

		if err := cmd.Run(); err != nil {
			// If context was cancelled during merge, keep temp files
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

	// Clean up temp files after successful merge
	cleanupTempFiles()

	logger.Info("Download completed: %s", outputPath)
	return outputPath, nil
}

// getContentLength fetches the content length of a URL without downloading
func (bd *BilibiliDownloader) getContentLength(url string) (int64, error) {
	req, err := http.NewRequest("HEAD", url, nil)
	if err != nil {
		return 0, err
	}

	bd.setHeaders(req)
	req.Header.Set("Referer", "https://www.bilibili.com/")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	return resp.ContentLength, nil
}

// downloadFile downloads a file from URL to path
func (bd *BilibiliDownloader) downloadFile(url, path string, onProgress func(float64)) (string, error) {
	return bd.downloadFileWithContext(context.Background(), url, path, 0, onProgress)
}

// downloadFileWithSize downloads a file with known total size for accurate progress
func (bd *BilibiliDownloader) downloadFileWithSize(url, path string, knownSize int64, onProgress func(float64)) (string, error) {
	return bd.downloadFileWithContext(context.Background(), url, path, knownSize, onProgress)
}

// downloadFileWithContext downloads a file with context support for cancellation and resume
// It automatically uses multipart download for large files that support range requests
func (bd *BilibiliDownloader) downloadFileWithContext(ctx context.Context, url, path string, knownSize int64, onProgress func(float64)) (string, error) {
	// Check if file already exists for resume
	var existingSize int64 = 0
	if fileInfo, err := os.Stat(path); err == nil {
		existingSize = fileInfo.Size()
	}
	stateExists := multipartStateExists(path)

	// If we already have the full file, skip download
	if knownSize > 0 && existingSize >= knownSize && !stateExists {
		logger.Info("File already complete: %s (%d bytes)", path, existingSize)
		if onProgress != nil {
			onProgress(100)
		}
		return path, nil
	}

	// If resuming a multipart download (resume state exists), continue with multipart.
	if stateExists {
		md := NewMultipartDownloader()
		md.SetHeaders(map[string]string{
			"Referer":         "https://www.bilibili.com/",
			"Origin":          "https://www.bilibili.com",
			"Accept":          "application/json, text/plain, */*",
			"Accept-Language": "zh-CN,zh;q=0.9,en;q=0.8",
		})
		if bd.sessData != "" {
			md.SetHeaders(map[string]string{
				"Cookie": fmt.Sprintf("SESSDATA=%s", bd.sessData),
			})
		}
		totalSize := knownSize
		if totalSize <= 0 {
			if st, err := loadMultipartState(path); err == nil && st != nil {
				totalSize = st.TotalSize
			}
		}
		if totalSize <= 0 {
			checkResult := md.CheckRangeSupport(ctx, url)
			if checkResult.Error != nil {
				logger.Debug("Failed to check range support: %v, falling back to sequential", checkResult.Error)
				return bd.downloadFileSequential(ctx, url, path, 0, 0, onProgress)
			}
			totalSize = checkResult.ContentLength
		}
		return bd.downloadFileMultipart(ctx, url, path, totalSize, md, onProgress)
	}

	// If resuming, use sequential download
	if existingSize > 0 {
		logger.Debug("Resuming download from %d bytes, using sequential download", existingSize)
		return bd.downloadFileSequential(ctx, url, path, knownSize, existingSize, onProgress)
	}

	// For new downloads, check if multipart download is beneficial
	md := NewMultipartDownloader()
	md.SetHeaders(map[string]string{
		"Referer":         "https://www.bilibili.com/",
		"Origin":          "https://www.bilibili.com",
		"Accept":          "application/json, text/plain, */*",
		"Accept-Language": "zh-CN,zh;q=0.9,en;q=0.8",
	})
	if bd.sessData != "" {
		md.SetHeaders(map[string]string{
			"Cookie": fmt.Sprintf("SESSDATA=%s", bd.sessData),
		})
	}

	// Determine file size - use knownSize if provided, otherwise check
	totalSize := knownSize
	supportsRange := true

	if totalSize <= 0 {
		checkResult := md.CheckRangeSupport(ctx, url)
		if checkResult.Error != nil {
			logger.Debug("Failed to check range support: %v, falling back to sequential", checkResult.Error)
			return bd.downloadFileSequential(ctx, url, path, 0, 0, onProgress)
		}
		totalSize = checkResult.ContentLength
		supportsRange = checkResult.SupportsRange
	}

	// Decide whether to use multipart download
	if ShouldUseMultipart(supportsRange, totalSize) {
		logger.Info("Using multipart download for Bilibili (size: %d bytes)", totalSize)
		return bd.downloadFileMultipart(ctx, url, path, totalSize, md, onProgress)
	}

	// Fall back to sequential download
	logger.Debug("Using sequential download (size: %d, supports range: %v)", totalSize, supportsRange)
	return bd.downloadFileSequential(ctx, url, path, totalSize, 0, onProgress)
}

// downloadFileMultipart performs multipart download for Bilibili videos
func (bd *BilibiliDownloader) downloadFileMultipart(ctx context.Context, url, path string, totalSize int64, md *MultipartDownloader, onProgress func(float64)) (string, error) {
	result := md.Download(ctx, url, path, totalSize, func(downloaded, total int64) {
		if onProgress != nil && total > 0 {
			progress := float64(downloaded) / float64(total) * 100
			onProgress(progress)
		}
	})

	if result.Error != nil {
		return "", result.Error
	}

	return path, nil
}

// downloadFileSequential performs sequential download (original logic)
func (bd *BilibiliDownloader) downloadFileSequential(ctx context.Context, url, path string, knownSize int64, existingSize int64, onProgress func(float64)) (string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return "", err
	}

	bd.setHeaders(req)
	req.Header.Set("Referer", "https://www.bilibili.com/")

	// Set Range header for resume
	if existingSize > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", existingSize))
		logger.Info("Resuming download from %d bytes: %s", existingSize, path)
	}

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	// Check response status
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		return "", fmt.Errorf("unexpected status: %d", resp.StatusCode)
	}

	// Determine total size
	totalSize := knownSize
	if totalSize <= 0 {
		if resp.StatusCode == http.StatusPartialContent {
			// Parse Content-Range header
			totalSize = existingSize + resp.ContentLength
		} else {
			totalSize = resp.ContentLength
		}
	}

	// Open file for append or create
	os.MkdirAll(filepath.Dir(path), 0755)
	var file *os.File
	if existingSize > 0 && resp.StatusCode == http.StatusPartialContent {
		file, err = os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0644)
	} else {
		// Server doesn't support range, start from beginning
		existingSize = 0
		file, err = os.Create(path)
	}
	if err != nil {
		return "", err
	}
	defer file.Close()

	// Download with progress
	downloaded := existingSize
	buf := make([]byte, 32*1024)

	for {
		// Check for cancellation
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		default:
		}

		n, err := resp.Body.Read(buf)
		if n > 0 {
			file.Write(buf[:n])
			downloaded += int64(n)

			if totalSize > 0 && onProgress != nil {
				progress := float64(downloaded) / float64(totalSize) * 100
				onProgress(progress)
			}
		}

		if err != nil {
			if err == io.EOF {
				break
			}
			return "", err
		}
	}

	return path, nil
}

// DASHProgressTracker tracks combined progress for DASH video+audio downloads
type DASHProgressTracker struct {
	videoSize       int64
	audioSize       int64
	totalSize       int64
	videoDownloaded int64
	audioDownloaded int64
	onProgress      func(float64)
	lastProgress    float64
}

// NewDASHProgressTracker creates a new progress tracker for DASH downloads
func NewDASHProgressTracker(videoSize, audioSize int64, onProgress func(float64)) *DASHProgressTracker {
	return &DASHProgressTracker{
		videoSize:  videoSize,
		audioSize:  audioSize,
		totalSize:  videoSize + audioSize,
		onProgress: onProgress,
	}
}

// UpdateVideoProgress updates progress for video download
func (t *DASHProgressTracker) UpdateVideoProgress(progress float64) {
	if t.videoSize > 0 {
		t.videoDownloaded = int64(progress / 100 * float64(t.videoSize))
	}
	t.reportProgress()
}

// UpdateAudioProgress updates progress for audio download
func (t *DASHProgressTracker) UpdateAudioProgress(progress float64) {
	if t.audioSize > 0 {
		t.audioDownloaded = int64(progress / 100 * float64(t.audioSize))
	}
	t.reportProgress()
}

// reportProgress calculates and reports combined progress
func (t *DASHProgressTracker) reportProgress() {
	if t.onProgress == nil || t.totalSize == 0 {
		return
	}
	// Reserve 5% for merge operation
	downloadProgress := float64(t.videoDownloaded+t.audioDownloaded) / float64(t.totalSize) * 95
	// Ensure progress is monotonically increasing
	if downloadProgress > t.lastProgress {
		t.lastProgress = downloadProgress
		t.onProgress(downloadProgress)
	}
}

// SetMergeProgress sets progress during merge phase (95-100%)
func (t *DASHProgressTracker) SetMergeProgress(progress float64) {
	if t.onProgress == nil {
		return
	}
	mergeProgress := 95 + progress*0.05
	if mergeProgress > t.lastProgress {
		t.lastProgress = mergeProgress
		t.onProgress(mergeProgress)
	}
}

// CalculateProgress calculates the combined progress percentage
// This is a pure function for testing purposes
func CalculateProgress(videoDownloaded, audioDownloaded, videoSize, audioSize int64) float64 {
	totalSize := videoSize + audioSize
	if totalSize == 0 {
		return 0
	}
	totalDownloaded := videoDownloaded + audioDownloaded
	// Reserve 5% for merge operation
	return float64(totalDownloaded) / float64(totalSize) * 95
}

// setHeaders sets common headers for Bilibili requests
func (bd *BilibiliDownloader) setHeaders(req *http.Request) {
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
	req.Header.Set("Origin", "https://www.bilibili.com")
	req.Header.Set("Referer", "https://www.bilibili.com/")

	if bd.sessData != "" {
		req.Header.Set("Cookie", fmt.Sprintf("SESSDATA=%s", bd.sessData))
	}
}

// IsBilibiliURL checks if a URL is a Bilibili video URL
func IsBilibiliURL(url string) bool {
	return strings.Contains(url, "bilibili.com") || strings.Contains(url, "b23.tv")
}
