package downloader

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"EasyDownload/internal/download/fetch"
	downloadtask "EasyDownload/internal/download/task"

	"github.com/stretchr/testify/require"
)

type recordingFetcher struct {
	request     fetch.FetchRequest
	destination fetch.Destination
	resumeDir   bool
}

func (f *recordingFetcher) Download(_ context.Context, request fetch.FetchRequest, destination fetch.Destination, progress fetch.ProgressReporter) (fetch.FetchResult, error) {
	f.request = request
	f.destination = destination
	payload := []byte("generic payload")
	if err := os.WriteFile(destination.TemporaryPath, payload, 0o600); err != nil {
		return fetch.FetchResult{}, err
	}
	if f.resumeDir {
		if err := os.MkdirAll(filepath.Join(destination.ResumeStatePath, "kept"), 0o700); err != nil {
			return fetch.FetchResult{}, err
		}
	} else {
		if err := os.WriteFile(destination.ResumeStatePath, []byte(`{"version":1}`), 0o600); err != nil {
			return fetch.FetchResult{}, err
		}
	}
	if progress != nil {
		progress(fetch.Progress{Downloaded: int64(len(payload)), Total: int64(len(payload)), Kind: fetch.ProgressUpdate})
	}
	digest := sha256.Sum256(payload)
	return fetch.FetchResult{
		TemporaryPath:   destination.TemporaryPath,
		ResumeStatePath: destination.ResumeStatePath,
		Downloaded:      int64(len(payload)),
		Total:           int64(len(payload)),
		SHA256:          hex.EncodeToString(digest[:]),
	}, nil
}

func (*recordingFetcher) Probe(context.Context, fetch.ProbeRequest) (fetch.ProbeResult, error) {
	return fetch.ProbeResult{}, nil
}

type genericTestExecution struct {
	fetcher   fetch.Fetcher
	final     string
	published bool
	artifacts []downloadtask.TaskArtifact
}

func (e *genericTestExecution) Fetcher() fetch.Fetcher                  { return e.fetcher }
func (*genericTestExecution) FFmpeg() downloadtask.FFmpegLocator        { return nil }
func (*genericTestExecution) Credentials() downloadtask.CredentialStore { return nil }
func (*genericTestExecution) UpdateTaskProgress(downloadtask.TaskProgressUpdate) error {
	return nil
}
func (e *genericTestExecution) RecordArtifact(artifact downloadtask.TaskArtifact) error {
	e.artifacts = append(e.artifacts, artifact)
	return nil
}
func (e *genericTestExecution) RecordPostPublishCleanupFailure(artifact downloadtask.TaskArtifact) error {
	e.artifacts = append(e.artifacts, artifact)
	return nil
}
func (*genericTestExecution) RecordCheckpoint(downloadtask.PlatformCheckpointEnvelope) error {
	return nil
}

func TestGenericAdapterSidecarCleanupFailureDoesNotReversePublishedSuccess(t *testing.T) {
	dir := t.TempDir()
	finalPath := filepath.Join(dir, "final.bin")
	platformData, err := MarshalGenericPlatformData("https://media.example/file", nil)
	require.NoError(t, err)
	fetcher := &recordingFetcher{resumeDir: true}
	execution := &genericTestExecution{fetcher: fetcher, final: finalPath}
	task := downloadtask.TaskSnapshot{
		PlatformDataVersion: 1,
		ID:                  "generic-cleanup-failure",
		OutputPolicy:        downloadtask.OutputPolicy{PlannedFinalPath: finalPath},
		PlatformData:        platformData,
	}

	require.NoError(t, NewGenericAdapter().RunTask(context.Background(), task, execution))
	require.True(t, execution.published)
	require.FileExists(t, finalPath)
	require.DirExists(t, fetcher.destination.ResumeStatePath)
	require.Len(t, execution.artifacts, 1)
	require.Equal(t, downloadtask.TaskArtifactTemporary, execution.artifacts[0].Kind)
}
func (e *genericTestExecution) PublishFinal(_ context.Context, temporaryPath string, draft downloadtask.TaskArtifactDraft) (downloadtask.TaskArtifact, error) {
	if err := os.Rename(temporaryPath, e.final); err != nil {
		return downloadtask.TaskArtifact{}, err
	}
	e.published = true
	return downloadtask.TaskArtifact{
		Kind:      downloadtask.TaskArtifactFinal,
		Path:      e.final,
		Size:      draft.Size,
		MediaType: draft.MediaType,
		Primary:   draft.Primary,
	}, nil
}

func TestGenericAdapterForwardsRequestHeadersAndRemovesResumeStateAfterPublish(t *testing.T) {
	dir := t.TempDir()
	finalPath := filepath.Join(dir, "final.bin")
	platformData, err := MarshalGenericPlatformData("https://media.example/file", map[string]string{
		"X-New-Platform-Token": "opaque-token",
		"Referer":              "https://platform.example/",
	})
	require.NoError(t, err)
	fetcher := &recordingFetcher{}
	execution := &genericTestExecution{fetcher: fetcher, final: finalPath}
	task := downloadtask.TaskSnapshot{
		PlatformDataVersion: 1,
		ID:                  "generic-header-test",
		OutputPolicy:        downloadtask.OutputPolicy{PlannedFinalPath: finalPath},
		PlatformData:        platformData,
	}

	require.NoError(t, NewGenericAdapter().RunTask(context.Background(), task, execution))
	require.True(t, execution.published)
	require.Equal(t, "opaque-token", fetcher.request.Headers["X-New-Platform-Token"])
	require.Equal(t, "https://platform.example/", fetcher.request.Headers["Referer"])
	require.FileExists(t, finalPath)
	require.NoFileExists(t, fetcher.destination.ResumeStatePath)
}
