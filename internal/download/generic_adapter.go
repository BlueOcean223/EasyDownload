package downloader

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"EasyDownload/internal/download/fetch"
	downloadtask "EasyDownload/internal/download/task"
)

type genericAdapter struct{}

// GenericPlatformData is the adapter-owned creation contract for ordinary URL
// downloads. The alias keeps the persisted JSON schema in the task contract
// while giving composition roots a platform-neutral builder.
type GenericPlatformData = downloadtask.GenericPlatformData

// MarshalGenericPlatformData validates and serializes a generic download
// request without exposing task internals to the composition root.
func MarshalGenericPlatformData(rawURL string, headers map[string]string) (json.RawMessage, error) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return nil, errors.New("generic task url is required")
	}
	cleanHeaders := make(map[string]string, len(headers))
	for key, value := range headers {
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		if key != "" && value != "" {
			cleanHeaders[key] = value
		}
	}
	data, err := json.Marshal(GenericPlatformData{URL: rawURL, Headers: cleanHeaders})
	if err != nil {
		return nil, fmt.Errorf("marshal generic platform data: %w", err)
	}
	return data, nil
}

// NewGenericAdapter returns the ordinary URL adapter. DownloadManager does not
// register it implicitly; the composition root must opt in explicitly.
func NewGenericAdapter() downloadtask.PlatformAdapter {
	return genericAdapter{}
}

func (genericAdapter) ID() downloadtask.PlatformID {
	return downloadtask.PlatformGeneric
}

func (genericAdapter) ValidateTask(task downloadtask.TaskSnapshot) error {
	data, err := genericTaskData(task)
	if err != nil {
		return err
	}
	if strings.TrimSpace(data.URL) == "" {
		return fmt.Errorf("generic task url is required")
	}
	if strings.TrimSpace(task.OutputPolicy.PlannedFinalPath) == "" {
		return fmt.Errorf("generic task output path is required")
	}
	return nil
}

func (genericAdapter) RunTask(ctx context.Context, task downloadtask.TaskSnapshot, execution downloadtask.TaskExecutionContext) error {
	data, err := genericTaskData(task)
	if err != nil {
		return err
	}
	targetPath := task.OutputPolicy.PlannedFinalPath
	requestHeaders := defaultDownloadHeaders()
	for key, value := range data.Headers {
		if strings.TrimSpace(key) != "" && strings.TrimSpace(value) != "" {
			requestHeaders[key] = value
		}
	}
	if err := execution.UpdateTaskProgress(downloadtask.TaskProgressUpdate{
		StageID:      "download",
		StageLabel:   "下载中",
		StagePercent: downloadtask.ProgressPercent(task.Progress.Percent),
		BytesLoaded:  task.Progress.BytesLoaded,
		BytesTotal:   task.Progress.BytesTotal,
	}); err != nil {
		return err
	}
	temporaryPath := filepath.Join(filepath.Dir(targetPath), "."+safePathToken(task.ID)+".part")
	resumeStatePath := temporaryPath + ".resume.json"
	fetchContext, cancelFetch := context.WithCancel(ctx)
	defer cancelFetch()
	var progressMu sync.Mutex
	var progressErr error
	result, err := execution.Fetcher().Download(fetchContext, fetch.FetchRequest{
		URL:     data.URL,
		Headers: requestHeaders,
		Identity: fetch.ResourceIdentity{
			ExpectedSize: task.Progress.BytesTotal,
		},
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
		ResumeStatePath: resumeStatePath,
	}, func(progress fetch.Progress) {
		progressMu.Lock()
		defer progressMu.Unlock()
		if progressErr != nil {
			return
		}
		percent := float64(0)
		if progress.Total > 0 {
			percent = float64(progress.Downloaded) / float64(progress.Total) * 100
		}
		progressErr = execution.UpdateTaskProgress(downloadtask.TaskProgressUpdate{
			StageID:        "download",
			StageLabel:     "下载中",
			StagePercent:   downloadtask.ProgressPercent(percent),
			OverallPercent: &percent,
			BytesLoaded:    progress.Downloaded,
			BytesTotal:     progress.Total,
		})
		if progressErr != nil {
			cancelFetch()
		}
	})
	progressMu.Lock()
	reportErr := progressErr
	progressMu.Unlock()
	if reportErr != nil {
		return reportErr
	}
	if err != nil {
		return err
	}
	if _, err := execution.PublishFinal(ctx, result.TemporaryPath, downloadtask.TaskArtifactDraft{
		Size:    result.Downloaded,
		SHA256:  result.SHA256,
		Primary: true,
	}); err != nil {
		return err
	}
	bestEffortRemoveGenericResumeState(execution, result.ResumeStatePath)
	return nil
}

func bestEffortRemoveGenericResumeState(execution downloadtask.TaskExecutionContext, path string) {
	if strings.TrimSpace(path) == "" {
		return
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		_ = execution.RecordPostPublishCleanupFailure(downloadtask.TaskArtifact{
			Kind: downloadtask.TaskArtifactTemporary,
			Path: path,
			Metadata: map[string]string{
				"platform":     string(downloadtask.PlatformGeneric),
				"cleanupError": err.Error(),
			},
		})
	}
}

func (genericAdapter) CleanupTask(_ context.Context, task downloadtask.TaskSnapshot, _ downloadtask.StopReason) error {
	targetPath := task.OutputPolicy.PlannedFinalPath
	temporaryPath := filepath.Join(filepath.Dir(targetPath), "."+safePathToken(task.ID)+".part")
	var cleanupErr error
	for _, path := range []string{temporaryPath, temporaryPath + ".resume.json"} {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			cleanupErr = errors.Join(cleanupErr, err)
		}
	}
	return cleanupErr
}

func genericTaskData(task downloadtask.TaskSnapshot) (downloadtask.GenericPlatformData, error) {
	var data downloadtask.GenericPlatformData
	if err := downloadtask.RequireCurrentPlatformDataVersion(task); err != nil {
		return data, err
	}
	if len(task.PlatformData) > 0 {
		if err := json.Unmarshal(task.PlatformData, &data); err != nil {
			return data, fmt.Errorf("invalid generic platform data: %w", err)
		}
	}
	return data, nil
}

func defaultDownloadHeaders() map[string]string {
	return map[string]string{
		"User-Agent": "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36",
	}
}
