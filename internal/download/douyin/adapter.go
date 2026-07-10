package douyin

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
	"EasyDownload/internal/utils"
)

type TaskData struct {
	Item            *DouyinItem `json:"item"`
	QualityKey      string      `json:"qualityKey,omitempty"`
	SelectedIndices []int       `json:"selectedIndices,omitempty"`
	Partial         bool        `json:"partial,omitempty"`
}

func MarshalTaskData(item *DouyinItem, qualityKey string, selectedIndices []int, partial bool) (json.RawMessage, error) {
	data, err := json.Marshal(TaskData{
		Item:            item,
		QualityKey:      qualityKey,
		SelectedIndices: append([]int(nil), selectedIndices...),
		Partial:         partial,
	})
	if err != nil {
		return nil, err
	}
	return json.RawMessage(data), nil
}

type Adapter struct {
	downloader *Downloader
}

func NewAdapter(downloaders ...*Downloader) Adapter {
	var dl *Downloader
	if len(downloaders) > 0 {
		dl = downloaders[0]
	}
	if dl == nil {
		dl = NewDownloader()
	}
	return Adapter{downloader: dl}
}

func (Adapter) ID() downloadtask.PlatformID { return downloadtask.PlatformDouyin }

func (Adapter) ValidateTask(task downloadtask.TaskSnapshot) error {
	data, err := decodeTaskData(task)
	if err != nil {
		return err
	}
	if data.Item == nil {
		return errors.New("missing douyin item data")
	}
	if strings.TrimSpace(task.OutputPolicy.PlannedFinalPath) == "" {
		return errors.New("douyin task output path is required")
	}
	if isAlbumItem(data.Item) {
		if len(data.Item.Images) == 0 {
			return ErrNoImages
		}
		if data.Partial && len(data.SelectedIndices) == 0 {
			return errors.New("douyin partial album selection is empty")
		}
	} else if selectStream(data.Item, data.QualityKey) == nil {
		return douyinPlatformError("douyin.stream_fallback_exhausted", "抖音视频没有可用清晰度", false, "refresh_source", ErrStreamNotFound)
	}
	return nil
}

func (a Adapter) RunTask(ctx context.Context, task downloadtask.TaskSnapshot, execution downloadtask.TaskExecutionContext) error {
	data, err := decodeTaskData(task)
	if err != nil {
		return err
	}
	if err := a.ValidateTask(task); err != nil {
		return err
	}
	downloader := a.downloader
	if downloader == nil {
		downloader = NewDownloader()
	}
	targetPath := strings.TrimSpace(task.OutputPolicy.PlannedFinalPath)
	temporaryPath := douyinTaskTemporaryPath(task.ID, targetPath)
	taskContext, cancelTask := context.WithCancel(ctx)
	defer cancelTask()

	var progressMu sync.Mutex
	var progressErr error
	reportUpdate := func(update downloadtask.TaskProgressUpdate) {
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
	progressFailure := func() error {
		progressMu.Lock()
		defer progressMu.Unlock()
		return progressErr
	}

	if isAlbumItem(data.Item) {
		total := len(data.Item.Images)
		if data.Partial {
			total = len(normalizeDouyinIndices(data.SelectedIndices, len(data.Item.Images)))
		}
		percentProgress := func(percent float64) {
			completed := int(percent/100*float64(total) + 0.5)
			if completed > total {
				completed = total
			}
			reportUpdate(downloadtask.TaskProgressUpdate{
				StageID:      "album_download",
				StageLabel:   "下载抖音图集",
				StagePercent: downloadtask.ProgressPercent(percent),
				ItemsDone:    completed,
				ItemsTotal:   total,
			})
		}
		byteProgress := func(downloaded, totalBytes int64) {
			reportUpdate(downloadtask.TaskProgressUpdate{
				StageID:     "album_download",
				StageLabel:  "下载抖音图集",
				BytesLoaded: downloaded,
				BytesTotal:  totalBytes,
			})
		}
		if data.Partial {
			err = downloader.downloadAlbumPartialWithFetcher(taskContext, execution.Fetcher(), data.Item, data.SelectedIndices, temporaryPath, percentProgress, byteProgress)
		} else {
			err = downloader.downloadAlbumWithFetcher(taskContext, execution.Fetcher(), data.Item, temporaryPath, percentProgress, byteProgress)
		}
		if reportErr := progressFailure(); reportErr != nil {
			return reportErr
		}
		if err != nil {
			return douyinExecutionError(err)
		}
		size, digest, err := inspectDouyinArtifact(temporaryPath)
		if err != nil {
			return err
		}
		_, err = execution.PublishFinal(ctx, temporaryPath, downloadtask.TaskArtifactDraft{
			MediaType: "application/zip",
			Size:      size,
			SHA256:    digest,
			Primary:   true,
		})
		return err
	}

	// Quality/stream fallback is platform semantics and is deliberately
	// resolved here before one byte entity is handed to Fetch.
	stream := selectStream(data.Item, data.QualityKey)
	if stream == nil || strings.TrimSpace(stream.URL) == "" {
		return douyinPlatformError("douyin.stream_fallback_exhausted", "抖音视频没有可用清晰度", false, "refresh_source", ErrStreamNotFound)
	}
	result, err := downloader.downloadVideoURLWithFetcher(
		taskContext,
		execution.Fetcher(),
		stream.URL,
		temporaryPath,
		stream.Size,
		nil,
		func(downloaded, total int64) {
			percent := 0.0
			if total > 0 {
				percent = float64(downloaded) * 100 / float64(total)
			}
			reportUpdate(downloadtask.TaskProgressUpdate{
				StageID:      "video_download",
				StageLabel:   "下载抖音视频",
				StagePercent: downloadtask.ProgressPercent(percent),
				BytesLoaded:  downloaded,
				BytesTotal:   total,
			})
		},
	)
	if reportErr := progressFailure(); reportErr != nil {
		return reportErr
	}
	if err != nil {
		return douyinExecutionError(err)
	}
	if _, err := execution.PublishFinal(ctx, result.TemporaryPath, downloadtask.TaskArtifactDraft{
		MediaType: "video/mp4",
		Size:      result.Downloaded,
		SHA256:    result.SHA256,
		Primary:   true,
	}); err != nil {
		return err
	}
	bestEffortRemoveResumeState(execution, result.ResumeStatePath, "douyin")
	return nil
}

func (Adapter) CleanupTask(_ context.Context, task downloadtask.TaskSnapshot, reason downloadtask.StopReason) error {
	if reason != downloadtask.StopReasonCancel && reason != downloadtask.StopReasonTaskRemoval {
		return nil
	}
	temporaryPath := douyinTaskTemporaryPath(task.ID, task.OutputPolicy.PlannedFinalPath)
	var cleanupErr error
	for _, path := range []string{temporaryPath, temporaryPath + ".resume.json", utils.AlbumTempDir(temporaryPath)} {
		if err := os.RemoveAll(path); err != nil {
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
		return data, errors.New("missing douyin platform data")
	}
	if err := json.Unmarshal(task.PlatformData, &data); err != nil {
		return data, fmt.Errorf("invalid douyin platform data: %w", err)
	}
	data.QualityKey = strings.TrimSpace(data.QualityKey)
	return data, nil
}

func isAlbumItem(item *DouyinItem) bool {
	return item != nil && (strings.EqualFold(strings.TrimSpace(item.Type), "album") || len(item.Images) > 0)
}

func normalizeDouyinIndices(indices []int, imageCount int) []int {
	seen := make(map[int]struct{}, len(indices))
	result := make([]int, 0, len(indices))
	for _, index := range indices {
		if index < 0 || index >= imageCount {
			continue
		}
		if _, exists := seen[index]; exists {
			continue
		}
		seen[index] = struct{}{}
		result = append(result, index)
	}
	return result
}

func douyinTaskTemporaryPath(taskID, targetPath string) string {
	sum := sha256.Sum256([]byte(taskID))
	extension := filepath.Ext(targetPath)
	if extension == "" {
		extension = ".tmp"
	}
	return filepath.Join(filepath.Dir(targetPath), "."+hex.EncodeToString(sum[:8])+".douyin"+extension+".tmp")
}

func inspectDouyinArtifact(path string) (int64, string, error) {
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

func bestEffortRemoveResumeState(execution downloadtask.TaskExecutionContext, path, platform string) {
	if strings.TrimSpace(path) == "" {
		return
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		_ = execution.RecordPostPublishCleanupFailure(downloadtask.TaskArtifact{
			Kind: downloadtask.TaskArtifactTemporary,
			Path: path,
			Metadata: map[string]string{
				"platform":     platform,
				"cleanupError": err.Error(),
			},
		})
	}
}

func douyinExecutionError(err error) error {
	if err == nil {
		return nil
	}
	var taskErr *downloadtask.TaskError
	if errors.As(err, &taskErr) {
		return err
	}
	var fetchErr *fetch.Error
	if errors.As(err, &fetchErr) {
		switch fetchErr.StatusCode {
		case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusGone:
			return douyinPlatformError("douyin.resource_expired", "抖音媒体地址已过期", false, "refresh_source", err)
		case http.StatusTooManyRequests:
			return douyinPlatformError("douyin.risk_control", "抖音请求触发风控", false, "retry_later", err)
		}
	}
	return err
}

func douyinPlatformError(code, message string, retryable bool, userAction string, cause error) error {
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
