package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"EasyDownload/internal/api"
	"EasyDownload/internal/detection"
	"EasyDownload/internal/download"
	"EasyDownload/internal/download/fetch"
	downloadtask "EasyDownload/internal/download/task"
	"EasyDownload/internal/download/wechat"

	"github.com/stretchr/testify/require"
)

// fixtureDetectionAdapter is a real second ingress adapter used to prove that
// Store, public event DTO and the opaque download command are source-agnostic.
type fixtureDetectionAdapter struct{}

func (fixtureDetectionAdapter) Adapt(secretURL, headerSecret, decodeSecret string) detection.Video {
	return detection.Video{
		Source: detection.Source("fixture_adapter"), Platform: "wechat", Title: "Fixture video",
		Candidates: []detection.Resource{{
			ID: "fixture-original", URL: secretURL,
			Headers: map[string]string{"Authorization": headerSecret}, DecodeKey: decodeSecret,
			FileFormat: "hd", Default: true,
		}},
	}
}

type blockingAppFetcher struct{}

func (blockingAppFetcher) Download(ctx context.Context, _ fetch.FetchRequest, _ fetch.Destination, _ fetch.ProgressReporter) (fetch.FetchResult, error) {
	<-ctx.Done()
	return fetch.FetchResult{}, ctx.Err()
}

func (blockingAppFetcher) Probe(context.Context, fetch.ProbeRequest) (fetch.ProbeResult, error) {
	return fetch.ProbeResult{}, nil
}

func TestSecondDetectionAdapterUsesPublicEventAndOpaqueV2DownloadCommand(t *testing.T) {
	store := detection.NewMemoryStore(10)
	internalAPI := api.NewInternalAPI(0, store)
	var publicEvent detection.PublicChange
	internalAPI.SetVideoCallback(func(change detection.Change) { publicEvent = change.Public() })

	manager := downloader.NewDownloadManager(t.TempDir(), 1)
	require.NoError(t, manager.RegisterPlatformAdapter(wechat.NewAdapter()))
	manager.SetExecutionCapabilities(blockingAppFetcher{}, nil, nil)
	manager.SetStatePath(t.TempDir() + "/downloads.json")
	app := &App{detectionStore: store, internalAPI: internalAPI, downloadManager: manager}

	secretURL := "https://private.example/video.mp4?signature=media-secret"
	headerSecret := "Bearer header-secret"
	decodeSecret := "987654321"
	_, err := internalAPI.Ingest(context.Background(), (fixtureDetectionAdapter{}).Adapt(secretURL, headerSecret, decodeSecret))
	require.NoError(t, err)
	require.Equal(t, detection.Source("fixture_adapter"), publicEvent.Snapshot.Videos[0].Source)
	publicJSON, err := json.Marshal(publicEvent)
	require.NoError(t, err)
	for _, forbidden := range []string{"private.example", "media-secret", "header-secret", decodeSecret, "authorization", "decodekey", "headers"} {
		require.NotContains(t, strings.ToLower(string(publicJSON)), forbidden)
	}

	publicVideo := publicEvent.Snapshot.Videos[0]
	result, err := app.StartDetectedDownload(publicVideo.ID, publicVideo.Candidates[0].ID)
	require.NoError(t, err)
	require.Equal(t, "wechat", result.PlatformID)
	privateTask, err := manager.GetTask(result.ID)
	require.NoError(t, err)
	var platformData wechat.PlatformData
	require.NoError(t, json.Unmarshal(privateTask.PlatformData, &platformData))
	require.Equal(t, secretURL, platformData.URL)
	require.Equal(t, headerSecret, platformData.Headers["Authorization"])
	require.Equal(t, decodeSecret, platformData.DecodeKey)

	resultJSON, err := json.Marshal(result)
	require.NoError(t, err)
	for _, forbidden := range []string{"private.example", "media-secret", "header-secret", decodeSecret, "platformdata"} {
		require.NotContains(t, strings.ToLower(string(resultJSON)), forbidden)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.True(t, manager.Shutdown(shutdownCtx).Completed)
}

func TestStartDetectedDownloadReturnsStructuredExpiredCandidateError(t *testing.T) {
	app := &App{detectionStore: detection.NewMemoryStore(1)}
	_, err := app.StartDetectedDownload("missing", "candidate")
	require.Error(t, err)
	taskError, ok := err.(*downloadtask.TaskError)
	require.True(t, ok)
	require.Equal(t, "detection.candidate_expired", taskError.Code)
	require.Equal(t, "请重新检测后再下载", taskError.UserAction)
}
