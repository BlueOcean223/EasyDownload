package wechat

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"EasyDownload/internal/download/fetch"
	downloadtask "EasyDownload/internal/download/task"

	"github.com/stretchr/testify/require"
)

type preservingEncryptedFetcher struct {
	payload []byte
	calls   int
	dest    fetch.Destination
}

func (f *preservingEncryptedFetcher) Download(_ context.Context, _ fetch.FetchRequest, destination fetch.Destination, progress fetch.ProgressReporter) (fetch.FetchResult, error) {
	f.calls++
	f.dest = destination
	if f.calls == 1 {
		if err := os.WriteFile(destination.TemporaryPath, f.payload, 0o600); err != nil {
			return fetch.FetchResult{}, err
		}
		if err := os.WriteFile(destination.ResumeStatePath, []byte(`{"version":1}`), 0o600); err != nil {
			return fetch.FetchResult{}, err
		}
	} else {
		current, err := os.ReadFile(destination.TemporaryPath)
		if err != nil {
			return fetch.FetchResult{}, err
		}
		if !bytes.Equal(current, f.payload) {
			return fetch.FetchResult{}, os.ErrInvalid
		}
	}
	if progress != nil {
		progress(fetch.Progress{Downloaded: int64(len(f.payload)), Total: int64(len(f.payload)), Kind: fetch.ProgressUpdate})
	}
	digest := sha256.Sum256(f.payload)
	return fetch.FetchResult{
		TemporaryPath:   destination.TemporaryPath,
		ResumeStatePath: destination.ResumeStatePath,
		Downloaded:      int64(len(f.payload)),
		Total:           int64(len(f.payload)),
		SHA256:          hex.EncodeToString(digest[:]),
	}, nil
}

func (*preservingEncryptedFetcher) Probe(context.Context, fetch.ProbeRequest) (fetch.ProbeResult, error) {
	return fetch.ProbeResult{}, nil
}

type wechatTestExecution struct {
	fetcher fetch.Fetcher
	final   string
}

func (e *wechatTestExecution) Fetcher() fetch.Fetcher                  { return e.fetcher }
func (*wechatTestExecution) FFmpeg() downloadtask.FFmpegLocator        { return nil }
func (*wechatTestExecution) Credentials() downloadtask.CredentialStore { return nil }
func (*wechatTestExecution) UpdateTaskProgress(downloadtask.TaskProgressUpdate) error {
	return nil
}
func (*wechatTestExecution) RecordArtifact(downloadtask.TaskArtifact) error { return nil }
func (*wechatTestExecution) RecordPostPublishCleanupFailure(downloadtask.TaskArtifact) error {
	return nil
}
func (*wechatTestExecution) RecordCheckpoint(downloadtask.PlatformCheckpointEnvelope) error {
	return nil
}
func (e *wechatTestExecution) PublishFinal(_ context.Context, temporaryPath string, draft downloadtask.TaskArtifactDraft) (downloadtask.TaskArtifact, error) {
	if err := os.Rename(temporaryPath, e.final); err != nil {
		return downloadtask.TaskArtifact{}, err
	}
	return downloadtask.TaskArtifact{Kind: downloadtask.TaskArtifactFinal, Path: e.final, Size: draft.Size, Primary: true}, nil
}

func TestWechatDecryptFailurePreservesTrustedEncryptedFetchForRetry(t *testing.T) {
	const correctKey = uint64(123456789)
	plain := testMP4Payload()
	encrypted := append([]byte(nil), plain...)
	DecryptData(encrypted, uint32(len(encrypted)), correctKey)

	dir := t.TempDir()
	finalPath := filepath.Join(dir, "video.mp4")
	fetcher := &preservingEncryptedFetcher{payload: encrypted}
	execution := &wechatTestExecution{fetcher: fetcher, final: finalPath}
	makeTask := func(key string) downloadtask.TaskSnapshot {
		platformData, err := MarshalPlatformData(
			"https://finder.video.qq.com/123/stodownload?encfilekey=key123&m=mmm",
			nil,
			key,
			"",
		)
		require.NoError(t, err)
		return downloadtask.TaskSnapshot{
			PlatformDataVersion: downloadtask.CurrentPlatformDataVersion,
			ID:                  "wechat-decrypt-retry",
			OutputPolicy:        downloadtask.OutputPolicy{PlannedFinalPath: finalPath},
			PlatformData:        platformData,
		}
	}

	err := NewAdapter().RunTask(context.Background(), makeTask("987654321"), execution)
	var taskErr *downloadtask.TaskError
	require.ErrorAs(t, err, &taskErr)
	require.Equal(t, "wechat.decrypt_failed", taskErr.Code)
	require.Equal(t, encrypted, mustReadWechatFile(t, fetcher.dest.TemporaryPath))
	require.FileExists(t, fetcher.dest.ResumeStatePath)
	require.NoFileExists(t, wechatDecryptTemporaryPath(fetcher.dest.TemporaryPath))

	// Simulate a process crash that left an incomplete derived file. The next
	// attempt must discard it and derive again from the unchanged fetch bytes.
	require.NoError(t, os.WriteFile(wechatDecryptTemporaryPath(fetcher.dest.TemporaryPath), []byte("crash residue"), 0o600))
	require.NoError(t, NewAdapter().RunTask(context.Background(), makeTask("123456789"), execution))
	require.Equal(t, plain, mustReadWechatFile(t, finalPath))
	require.Equal(t, 2, fetcher.calls)
	require.NoFileExists(t, fetcher.dest.TemporaryPath)
	require.NoFileExists(t, fetcher.dest.ResumeStatePath)
}

func testMP4Payload() []byte {
	payload := make([]byte, 32)
	binary.BigEndian.PutUint32(payload[0:4], 16)
	copy(payload[4:8], "ftyp")
	copy(payload[8:16], "isom0000")
	binary.BigEndian.PutUint32(payload[16:20], 16)
	copy(payload[20:24], "mdat")
	copy(payload[24:32], "payload!")
	return payload
}

func mustReadWechatFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return data
}
