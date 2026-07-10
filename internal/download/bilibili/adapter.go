package bilibili

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"EasyDownload/internal/download/fetch"
	downloadtask "EasyDownload/internal/download/task"
)

type TaskData struct {
	Video     *BilibiliVideo `json:"video"`
	Quality   int            `json:"quality"`
	PartIndex int            `json:"partIndex"`
}

func MarshalTaskData(video *BilibiliVideo, quality int, partIndex int) (json.RawMessage, error) {
	data, err := json.Marshal(TaskData{Video: video, Quality: quality, PartIndex: partIndex})
	if err != nil {
		return nil, err
	}
	return json.RawMessage(data), nil
}

type Adapter struct {
	downloader *BilibiliDownloader
}

func NewAdapter(downloaders ...*BilibiliDownloader) Adapter {
	var dl *BilibiliDownloader
	if len(downloaders) > 0 {
		dl = downloaders[0]
	}
	if dl == nil {
		dl = NewBilibiliDownloader()
	}
	return Adapter{downloader: dl}
}

func (Adapter) ID() downloadtask.PlatformID { return downloadtask.PlatformBilibili }

func (Adapter) ValidateTask(task downloadtask.TaskSnapshot) error {
	data, err := decodeTaskData(task)
	if err != nil {
		return bilibiliPlatformError("bilibili.invalid_task_data", "B站任务数据无效", false, "refresh_source", err)
	}
	if data.Video == nil {
		return bilibiliPlatformError("bilibili.invalid_task_data", "缺少B站视频信息", false, "refresh_source", errors.New("missing bilibili video data"))
	}
	if strings.TrimSpace(task.OutputPolicy.PlannedFinalPath) == "" {
		return errors.New("bilibili task output path is required")
	}
	if data.PartIndex < -1 {
		return bilibiliPlatformError("bilibili.invalid_part", "B站分P索引无效", false, "refresh_source", fmt.Errorf("part index %d", data.PartIndex))
	}
	resolvedPartIndex := data.PartIndex
	if resolvedPartIndex < 0 {
		resolvedPartIndex = 0
	}
	if resolvedPartIndex >= len(data.Video.Parts) {
		return bilibiliPlatformError("bilibili.invalid_part", "B站分P索引无效", false, "refresh_source", fmt.Errorf("part index %d", data.PartIndex))
	}
	part := data.Video.Parts[resolvedPartIndex]
	if part.CID <= 0 || strings.TrimSpace(selectString(part.BV, data.Video.BV)) == "" {
		return bilibiliPlatformError("bilibili.invalid_task_data", "B站任务缺少可刷新的视频标识", false, "refresh_source", errors.New("missing stable bvid/cid metadata"))
	}
	return nil
}

func (a Adapter) RunTask(ctx context.Context, task downloadtask.TaskSnapshot, execution downloadtask.TaskExecutionContext) error {
	data, err := decodeTaskData(task)
	if err != nil {
		return bilibiliPlatformError("bilibili.invalid_task_data", "B站任务数据无效", false, "refresh_source", err)
	}
	if err := a.ValidateTask(task); err != nil {
		return err
	}
	downloader := a.downloader
	if downloader == nil {
		downloader = NewBilibiliDownloader()
	}
	quality := data.Quality
	if quality == 0 {
		quality = 80
	}

	// API/quality fallback is platform semantics. Resolve it before handing a
	// single stream and its byte-equivalent CDN backups to Fetch.
	stream, err := resolveAdapterStream(ctx, downloader, data.Video, data.PartIndex, quality)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return bilibiliStreamResolutionError(downloader, data.PartIndex, err)
	}
	if stream == nil {
		return bilibiliPlatformError("bilibili.stream_fallback_exhausted", "B站视频没有可用清晰度", false, "refresh_source", errors.New("no available stream"))
	}

	if locator := execution.FFmpeg(); locator != nil {
		if ffmpegPath, locateErr := locator.Locate(ctx); locateErr == nil && strings.TrimSpace(ffmpegPath) != "" {
			downloader.SetFFmpegPath(ffmpegPath)
		}
	}
	if (stream.BiliDRMURI != "" || stream.DRMTechType == 2) && stream.DRMKey == "" {
		return bilibiliPlatformError("bilibili.drm_unsupported", "该B站视频受 DRM 保护，暂不支持下载", false, "choose_another_quality", errors.New("DRM key unavailable"))
	}
	if (stream.AudioURL != "" || len(stream.AudioBackupURLs) > 0) && !downloader.IsFFmpegAvailable() {
		return bilibiliPlatformError("bilibili.ffmpeg_required", "下载该B站视频需要 FFmpeg", false, "install_ffmpeg", errors.New("ffmpeg unavailable"))
	}

	temporaryPath := bilibiliTaskTemporaryPath(task.ID, task.OutputPolicy.PlannedFinalPath)
	taskContext, cancelTask := context.WithCancel(ctx)
	defer cancelTask()
	var progressMu sync.Mutex
	var progressErr error
	report := func(update downloadtask.TaskProgressUpdate) {
		progressMu.Lock()
		defer progressMu.Unlock()
		if progressErr != nil {
			return
		}
		progressErr = execution.UpdateTaskProgress(update)
		if progressErr != nil {
			cancelTask()
		}
	}
	var totalSize int64
	onSizeKnown := func(size int64) {
		totalSize = size
		report(downloadtask.TaskProgressUpdate{
			StageID:     "media_download",
			StageLabel:  "下载B站音视频",
			BytesTotal:  size,
			BytesLoaded: 0,
		})
	}
	onProgress := func(percent float64) {
		loaded := int64(0)
		if totalSize > 0 {
			loaded = int64(percent / 100 * float64(totalSize))
		}
		report(downloadtask.TaskProgressUpdate{
			StageID:      "media_download",
			StageLabel:   "下载并合并B站视频",
			StagePercent: downloadtask.ProgressPercent(percent),
			BytesLoaded:  loaded,
			BytesTotal:   totalSize,
		})
	}

	outputPath, err := downloader.downloadResolvedStream(taskContext, execution.Fetcher(), data.Video.Title, stream, temporaryPath, onProgress, onSizeKnown)
	progressMu.Lock()
	reportErr := progressErr
	progressMu.Unlock()
	if reportErr != nil {
		return reportErr
	}
	if err != nil {
		return bilibiliExecutionError(downloader, err)
	}
	size, digest, err := inspectBilibiliArtifact(outputPath)
	if err != nil {
		return err
	}
	if _, err := execution.PublishFinal(ctx, outputPath, downloadtask.TaskArtifactDraft{
		MediaType: "video/mp4",
		Size:      size,
		SHA256:    digest,
		Primary:   true,
	}); err != nil {
		return err
	}
	bestEffortRemoveBilibiliResumeState(execution, outputPath+".resume.json")
	return nil
}

func bilibiliStreamResolutionError(downloader *BilibiliDownloader, partIndex int, err error) error {
	var apiErr *bilibiliAPIError
	if errors.As(err, &apiErr) {
		if apiErr.StatusCode == http.StatusTooManyRequests || apiErr.StatusCode == http.StatusPreconditionFailed || apiErr.Code == -412 {
			return bilibiliPlatformError("bilibili.risk_control", "B站请求触发风控，请稍后重试", false, "retry_later", err)
		}
		if apiErr.StatusCode == http.StatusUnauthorized || apiErr.StatusCode == http.StatusForbidden || apiErr.Code == -101 {
			return bilibiliPlatformError("bilibili.auth_required", "该B站清晰度或分P需要登录", false, "login_bilibili", err)
		}
		if apiErr.StatusCode == http.StatusNotFound || apiErr.StatusCode == http.StatusGone {
			return bilibiliPlatformError("bilibili.resource_expired", "B站播放信息已失效", false, "refresh_source", err)
		}
	}
	if partIndex >= 0 && downloader.GetSessData() == "" {
		return bilibiliPlatformError("bilibili.auth_required", "该B站清晰度或分P需要登录", false, "login_bilibili", err)
	}
	return bilibiliPlatformError("bilibili.api_failed", "获取B站媒体流失败", true, "retry", err)
}

func (Adapter) CleanupTask(_ context.Context, task downloadtask.TaskSnapshot, reason downloadtask.StopReason) error {
	if reason != downloadtask.StopReasonCancel && reason != downloadtask.StopReasonTaskRemoval {
		return nil
	}
	temporaryPath := bilibiliTaskTemporaryPath(task.ID, task.OutputPolicy.PlannedFinalPath)
	basePath := strings.TrimSuffix(temporaryPath, ".mp4")
	var cleanupErr error
	for _, path := range []string{
		temporaryPath,
		temporaryPath + ".resume.json",
		basePath + "_video.m4s",
		basePath + "_audio.m4s",
		basePath + "_video.m4s.resume.json",
		basePath + "_audio.m4s.resume.json",
		basePath + "_video.m4s.edstate.json",
		basePath + "_audio.m4s.edstate.json",
	} {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			cleanupErr = errors.Join(cleanupErr, err)
		}
	}
	return cleanupErr
}

func decodeTaskData(task downloadtask.TaskSnapshot) (TaskData, error) {
	var data TaskData
	if err := downloadtask.RequireCurrentPlatformDataVersion(task); err != nil {
		return data, err
	}
	if len(task.PlatformData) == 0 {
		return data, errors.New("missing bilibili platform data")
	}
	if err := json.Unmarshal(task.PlatformData, &data); err != nil {
		return data, fmt.Errorf("invalid bilibili platform data: %w", err)
	}
	return data, nil
}

func resolveAdapterStream(ctx context.Context, downloader *BilibiliDownloader, video *BilibiliVideo, partIndex, quality int) (*BilibiliStream, error) {
	// Ordinary single-video tasks historically use -1 to mean the first part.
	// Direct media URLs are short-lived, so every execution must resolve them
	// again from the stable BV/CID metadata instead of reusing persisted Streams.
	resolvedPartIndex := partIndex
	if resolvedPartIndex < 0 {
		resolvedPartIndex = 0
	}
	streams, err := downloader.GetPartStreamsContext(ctx, video, resolvedPartIndex)
	if err != nil {
		return nil, err
	}
	for index := range streams {
		if streams[index].Quality == quality {
			return &streams[index], nil
		}
	}
	if len(streams) > 0 {
		return &streams[0], nil
	}
	return nil, nil
}

func bilibiliTaskTemporaryPath(taskID, targetPath string) string {
	sum := sha256.Sum256([]byte(taskID))
	return filepath.Join(filepath.Dir(targetPath), "."+hex.EncodeToString(sum[:8])+".bilibili.tmp.mp4")
}

func inspectBilibiliArtifact(path string) (int64, string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, "", err
	}
	file, err := os.Open(path)
	if err != nil {
		return 0, "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return 0, "", err
	}
	return info.Size(), hex.EncodeToString(hash.Sum(nil)), nil
}

func bestEffortRemoveBilibiliResumeState(execution downloadtask.TaskExecutionContext, path string) {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		_ = execution.RecordPostPublishCleanupFailure(downloadtask.TaskArtifact{
			Kind: downloadtask.TaskArtifactTemporary,
			Path: path,
			Metadata: map[string]string{
				"platform":     string(downloadtask.PlatformBilibili),
				"cleanupError": err.Error(),
			},
		})
	}
}

func bilibiliExecutionError(downloader *BilibiliDownloader, err error) error {
	var taskErr *downloadtask.TaskError
	if errors.As(err, &taskErr) {
		return err
	}
	var fetchErr *fetch.Error
	if errors.As(err, &fetchErr) {
		switch fetchErr.StatusCode {
		case http.StatusUnauthorized, http.StatusForbidden:
			if downloader.GetSessData() == "" {
				return bilibiliPlatformError("bilibili.auth_required", "该B站资源需要登录", false, "login_bilibili", err)
			}
			return bilibiliPlatformError("bilibili.resource_expired", "B站媒体地址已过期", false, "refresh_source", err)
		case http.StatusNotFound, http.StatusGone:
			return bilibiliPlatformError("bilibili.resource_expired", "B站媒体地址已过期", false, "refresh_source", err)
		}
	}
	return err
}

func bilibiliPlatformError(code, message string, retryable bool, userAction string, cause error) error {
	taskErr := &downloadtask.TaskError{
		Code:       code,
		Category:   downloadtask.TaskErrorCategoryPlatform,
		Message:    message,
		Retryable:  retryable,
		UserAction: userAction,
	}
	if cause != nil {
		taskErr.Cause = cause.Error()
	}
	return taskErr
}
