package xiaohongshu

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
	Item           *XHSItem `json:"item"`
	SelectedImages []int    `json:"selectedImages,omitempty"`
	Quality        string   `json:"quality,omitempty"`
}

func MarshalTaskData(item *XHSItem, selectedImages []int, quality string) (json.RawMessage, error) {
	data, err := json.Marshal(TaskData{
		Item:           item,
		SelectedImages: append([]int(nil), selectedImages...),
		Quality:        quality,
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

func (Adapter) ID() downloadtask.PlatformID { return downloadtask.PlatformXiaohongshu }

func (Adapter) ValidateTask(task downloadtask.TaskSnapshot) error {
	data, err := decodeTaskData(task)
	if err != nil {
		return err
	}
	if data.Item == nil {
		return errors.New("missing xiaohongshu item data")
	}
	if strings.TrimSpace(plannedTaskPath(task)) == "" {
		return errors.New("xiaohongshu task output path is required")
	}
	if data.Item.IsAlbum() {
		if err := data.Item.ValidateSelectedImages(data.SelectedImages); err != nil {
			return err
		}
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
	quality := data.Quality
	targetPath := plannedTaskPath(task)
	temporaryPath := xhsTaskTemporaryPath(task.ID, targetPath)
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

	var (
		size       int64
		digest     string
		resumePath string
		mediaType  string
	)
	if data.Item.IsAlbum() {
		indices := normalizeSelection(data.SelectedImages, len(data.Item.Images))
		total := len(indices)
		progressFn := func(done int) {
			percent := 0.0
			if total > 0 {
				percent = float64(done) * 100 / float64(total)
			}
			reportUpdate(downloadtask.TaskProgressUpdate{
				StageID:      "album_download",
				StageLabel:   "下载小红书图集",
				StagePercent: downloadtask.ProgressPercent(percent),
				ItemsDone:    done,
				ItemsTotal:   total,
			})
		}
		if err := downloader.downloadAlbumZip(taskContext, execution.Fetcher(), data.Item, indices, temporaryPath, progressFn); err != nil {
			return xhsExecutionError(err)
		}
		progressMu.Lock()
		reportErr := progressErr
		progressMu.Unlock()
		if reportErr != nil {
			return reportErr
		}
		size, digest, err = inspectTemporaryArtifact(temporaryPath)
		mediaType = "application/zip"
	} else {
		stream := selectStream(data.Item, quality)
		if stream == nil || strings.TrimSpace(stream.URL) == "" {
			return xhsPlatformError("xiaohongshu.stream_fallback_exhausted", "小红书视频没有可用清晰度", false, "refresh_source", ErrStreamNotFound)
		}
		result, fetchErr := downloader.downloadAlbumMedia(
			taskContext,
			execution.Fetcher(),
			stream.URL,
			stream.BackupURLs,
			temporaryPath,
			downloader.maxVideoSize,
			func(downloaded, total int64) {
				percent := 0.0
				if total > 0 {
					percent = float64(downloaded) * 100 / float64(total)
				}
				reportUpdate(downloadtask.TaskProgressUpdate{
					StageID:      "video_download",
					StageLabel:   "下载小红书视频",
					StagePercent: downloadtask.ProgressPercent(percent),
					BytesLoaded:  downloaded,
					BytesTotal:   total,
				})
			},
		)
		progressMu.Lock()
		reportErr := progressErr
		progressMu.Unlock()
		if reportErr != nil {
			return reportErr
		}
		if fetchErr != nil {
			return xhsExecutionError(fetchErr)
		}
		size, digest, resumePath = result.Downloaded, result.SHA256, result.ResumeStatePath
		mediaType = "video/mp4"
	}
	if err != nil {
		return err
	}
	if _, err := execution.PublishFinal(ctx, temporaryPath, downloadtask.TaskArtifactDraft{
		MediaType: mediaType,
		Size:      size,
		SHA256:    digest,
		Primary:   true,
	}); err != nil {
		return err
	}
	if resumePath != "" {
		bestEffortRemoveXHSResumeState(execution, resumePath)
	}
	return nil
}

func bestEffortRemoveXHSResumeState(execution downloadtask.TaskExecutionContext, path string) {
	if strings.TrimSpace(path) == "" {
		return
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		_ = execution.RecordPostPublishCleanupFailure(downloadtask.TaskArtifact{
			Kind: downloadtask.TaskArtifactTemporary,
			Path: path,
			Metadata: map[string]string{
				"platform":     string(downloadtask.PlatformXiaohongshu),
				"cleanupError": err.Error(),
			},
		})
	}
}

func xhsExecutionError(err error) error {
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
			return xhsPlatformError("xiaohongshu.resource_expired", "小红书媒体地址已过期", false, "refresh_source", err)
		}
	}
	return err
}

func xhsPlatformError(code, message string, retryable bool, userAction string, cause error) error {
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

func (Adapter) CleanupTask(_ context.Context, task downloadtask.TaskSnapshot, reason downloadtask.StopReason) error {
	if reason != downloadtask.StopReasonCancel && reason != downloadtask.StopReasonTaskRemoval {
		return nil
	}
	temporaryPath := xhsTaskTemporaryPath(task.ID, plannedTaskPath(task))
	var cleanupErr error
	for _, path := range []string{
		temporaryPath,
		temporaryPath + ".resume.json",
		utils.AlbumTempDir(temporaryPath),
	} {
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
		return data, errors.New("missing xiaohongshu platform data")
	}
	if err := json.Unmarshal(task.PlatformData, &data); err != nil {
		return data, fmt.Errorf("invalid xiaohongshu platform data: %w", err)
	}
	return data, nil
}

func plannedTaskPath(task downloadtask.TaskSnapshot) string {
	return strings.TrimSpace(task.OutputPolicy.PlannedFinalPath)
}

func xhsTaskTemporaryPath(taskID, targetPath string) string {
	sum := sha256.Sum256([]byte(taskID))
	extension := filepath.Ext(targetPath)
	if extension == "" {
		extension = ".tmp"
	}
	return filepath.Join(filepath.Dir(targetPath), "."+hex.EncodeToString(sum[:8])+".xhs"+extension+".tmp")
}

func inspectTemporaryArtifact(path string) (int64, string, error) {
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
