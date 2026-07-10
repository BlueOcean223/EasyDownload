package wechat

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"EasyDownload/internal/download/fetch"
	downloadtask "EasyDownload/internal/download/task"
	"EasyDownload/internal/infra/logger"
)

// PlatformData is the complete persisted execution contract for a WeChat
// download. Runtime execution never reads legacy top-level DecodeKey/Quality.
type PlatformData struct {
	URL        string            `json:"url"`
	Headers    map[string]string `json:"headers,omitempty"`
	DecodeKey  string            `json:"decodeKey,omitempty"`
	FileFormat string            `json:"fileFormat,omitempty"`
}

func MarshalPlatformData(rawURL string, headers map[string]string, decodeKey, fileFormat string) (json.RawMessage, error) {
	data := PlatformData{
		URL:        strings.TrimSpace(rawURL),
		Headers:    cleanHeaders(headers),
		DecodeKey:  strings.TrimSpace(decodeKey),
		FileFormat: strings.TrimSpace(fileFormat),
	}
	if err := data.Validate(); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("marshal wechat platform data: %w", err)
	}
	return encoded, nil
}

func (data PlatformData) Validate() error {
	if strings.TrimSpace(data.URL) == "" {
		return errors.New("wechat task url is required")
	}
	if ok, reason := IsValidVODURL(data.URL); !ok {
		return fmt.Errorf("invalid wechat video url (%s)", reason)
	}
	if data.DecodeKey != "" {
		if _, err := ParseDecodeKey(data.DecodeKey); err != nil {
			return fmt.Errorf("invalid wechat decode key: %w", err)
		}
	}
	return nil
}

type Adapter struct{}

func NewAdapter() Adapter { return Adapter{} }

func (Adapter) ID() downloadtask.PlatformID { return downloadtask.PlatformWeChat }

func (Adapter) ValidateTask(task downloadtask.TaskSnapshot) error {
	data, err := taskPlatformData(task)
	if err != nil {
		return wechatPlatformError("wechat.invalid_task_data", "微信任务数据无效", false, "refresh_source", err)
	}
	if err := data.Validate(); err != nil {
		return wechatPlatformError("wechat.invalid_resource", "微信视频资源无效或已过期", false, "refresh_source", err)
	}
	if strings.TrimSpace(task.OutputPolicy.PlannedFinalPath) == "" {
		return errors.New("wechat task output path is required")
	}
	return nil
}

func (a Adapter) RunTask(ctx context.Context, task downloadtask.TaskSnapshot, execution downloadtask.TaskExecutionContext) error {
	data, err := taskPlatformData(task)
	if err != nil {
		return wechatPlatformError("wechat.invalid_task_data", "微信任务数据无效", false, "refresh_source", err)
	}
	if err := data.Validate(); err != nil {
		return wechatPlatformError("wechat.invalid_resource", "微信视频资源无效或已过期", false, "refresh_source", err)
	}
	targetPath := plannedPath(task)
	temporaryPath := wechatTemporaryPath(task.ID, targetPath)
	fetchContext, cancelFetch := context.WithCancel(ctx)
	defer cancelFetch()
	var progressMu sync.Mutex
	var progressErr error
	reportProgress := func(progress fetch.Progress) {
		progressMu.Lock()
		defer progressMu.Unlock()
		if progressErr != nil {
			return
		}
		stagePercent := 0.0
		if progress.Total > 0 {
			stagePercent = float64(progress.Downloaded) * 100 / float64(progress.Total)
		}
		progressErr = execution.UpdateTaskProgress(downloadtask.TaskProgressUpdate{
			StageID:      "download",
			StageLabel:   "下载微信视频",
			StagePercent: downloadtask.ProgressPercent(stagePercent),
			BytesLoaded:  progress.Downloaded,
			BytesTotal:   progress.Total,
		})
		if progressErr != nil {
			cancelFetch()
		}
	}
	result, err := execution.Fetcher().Download(fetchContext, fetch.FetchRequest{
		URL:     prepareDownloadURL(data.URL, data.FileFormat),
		Headers: wechatDownloadHeaders(data.Headers),
		ResumePolicy: fetch.ResumePolicy{
			Enabled:                 true,
			RestartWhenRangeIgnored: true,
		},
		RetryPolicy: fetch.RetryPolicy{
			MaxAttempts:    3,
			InitialBackoff: 200 * time.Millisecond,
			MaxBackoff:     2 * time.Second,
		},
	}, fetch.Destination{
		TemporaryPath:   temporaryPath,
		ResumeStatePath: temporaryPath + ".resume.json",
	}, reportProgress)
	progressMu.Lock()
	reportErr := progressErr
	progressMu.Unlock()
	if reportErr != nil {
		return reportErr
	}
	if err != nil {
		return err
	}

	publishPath := result.TemporaryPath
	publishHash := result.SHA256
	if data.DecodeKey != "" && !ValidateVideoFormat(result.TemporaryPath) {
		decryptPath := wechatDecryptTemporaryPath(result.TemporaryPath)
		if err := copyEncryptedTemporary(result.TemporaryPath, decryptPath); err != nil {
			return fmt.Errorf("prepare wechat decrypt temporary: %w", err)
		}
		if err := NewVideoDecryptor().DecryptFile(decryptPath, data.DecodeKey); err != nil {
			_ = os.Remove(decryptPath)
			return wechatPlatformError("wechat.decrypt_failed", "微信视频解密失败", false, "refresh_decode_key", err)
		}
		if !ValidateVideoFormat(decryptPath) {
			_ = os.Remove(decryptPath)
			return wechatPlatformError("wechat.invalid_resource", "微信视频解密后格式无效", false, "refresh_source", errors.New("decrypted file is not a valid video format"))
		}
		publishPath = decryptPath
		publishHash, err = sha256File(decryptPath)
		if err != nil {
			return fmt.Errorf("hash decrypted video: %w", err)
		}
	}
	info, err := os.Stat(publishPath)
	if err != nil {
		return err
	}
	if _, err := execution.PublishFinal(ctx, publishPath, downloadtask.TaskArtifactDraft{
		MediaType: "video/mp4",
		Size:      info.Size(),
		SHA256:    publishHash,
		Primary:   true,
	}); err != nil {
		return err
	}
	if publishPath != result.TemporaryPath {
		bestEffortRemoveWeChatTemporary(execution, result.TemporaryPath, "encrypted-fetch")
	}
	bestEffortRemoveWeChatTemporary(execution, result.ResumeStatePath, "resume-sidecar")
	return nil
}

func (Adapter) CleanupTask(_ context.Context, task downloadtask.TaskSnapshot, reason downloadtask.StopReason) error {
	if reason != downloadtask.StopReasonCancel && reason != downloadtask.StopReasonTaskRemoval {
		return nil
	}
	temporaryPath := wechatTemporaryPath(task.ID, plannedPath(task))
	var cleanupErr error
	for _, path := range []string{temporaryPath, temporaryPath + ".resume.json", wechatDecryptTemporaryPath(temporaryPath)} {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			cleanupErr = errors.Join(cleanupErr, err)
		}
	}
	return cleanupErr
}

func taskPlatformData(task downloadtask.TaskSnapshot) (PlatformData, error) {
	var data PlatformData
	if err := downloadtask.RequireCurrentPlatformDataVersion(task); err != nil {
		return data, err
	}
	if len(task.PlatformData) == 0 {
		return data, errors.New("missing wechat platform data")
	}
	if err := json.Unmarshal(task.PlatformData, &data); err != nil {
		return data, fmt.Errorf("invalid wechat platform data: %w", err)
	}
	data.URL = strings.TrimSpace(data.URL)
	data.DecodeKey = strings.TrimSpace(data.DecodeKey)
	data.FileFormat = strings.TrimSpace(data.FileFormat)
	data.Headers = cleanHeaders(data.Headers)
	return data, nil
}

func prepareDownloadURL(rawURL, fileFormat string) string {
	rawURL = strings.TrimSpace(rawURL)
	parsedURL, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	query := parsedURL.Query()
	query.Del("X-snsvideoflag")
	if fileFormat != "" {
		query.Set("X-snsvideoflag", fileFormat)
		logger.Info("[WeChat Download] Using quality format: %s", fileFormat)
	}
	parsedURL.RawQuery = query.Encode()
	return parsedURL.String()
}

func wechatDownloadHeaders(overrides map[string]string) map[string]string {
	headers := map[string]string{
		"User-Agent": "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36",
		"Referer":    "https://channels.weixin.qq.com/",
	}
	for key, value := range cleanHeaders(overrides) {
		headers[key] = value
	}
	return headers
}

func cleanHeaders(headers map[string]string) map[string]string {
	result := make(map[string]string, len(headers))
	for key, value := range headers {
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		if key != "" && value != "" {
			result[key] = value
		}
	}
	return result
}

func plannedPath(task downloadtask.TaskSnapshot) string {
	return strings.TrimSpace(task.OutputPolicy.PlannedFinalPath)
}

func wechatTemporaryPath(taskID, targetPath string) string {
	sum := sha256.Sum256([]byte(taskID))
	return filepath.Join(filepath.Dir(targetPath), "."+hex.EncodeToString(sum[:8])+".wechat.part")
}

func wechatDecryptTemporaryPath(encryptedPath string) string {
	return encryptedPath + ".decrypt"
}

func copyEncryptedTemporary(sourcePath, destinationPath string) error {
	if err := os.Remove(destinationPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	source, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	defer source.Close()
	destination, err := os.OpenFile(destinationPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	removeDestination := true
	defer func() {
		_ = destination.Close()
		if removeDestination {
			_ = os.Remove(destinationPath)
		}
	}()
	if _, err := io.Copy(destination, source); err != nil {
		return err
	}
	if err := destination.Sync(); err != nil {
		return err
	}
	if err := destination.Close(); err != nil {
		return err
	}
	removeDestination = false
	return nil
}

func bestEffortRemoveWeChatTemporary(execution downloadtask.TaskExecutionContext, path, role string) {
	if strings.TrimSpace(path) == "" {
		return
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		_ = execution.RecordPostPublishCleanupFailure(downloadtask.TaskArtifact{
			Kind: downloadtask.TaskArtifactTemporary,
			Path: path,
			Metadata: map[string]string{
				"platform":     string(downloadtask.PlatformWeChat),
				"role":         role,
				"cleanupError": err.Error(),
			},
		})
	}
}

func wechatPlatformError(code, message string, retryable bool, userAction string, cause error) error {
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

func sha256File(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
