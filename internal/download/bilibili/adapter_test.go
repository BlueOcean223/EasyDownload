package bilibili

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"EasyDownload/internal/download/fetch"
	downloadtask "EasyDownload/internal/download/task"

	"github.com/stretchr/testify/require"
)

type bilibiliTestExecution struct {
	fetcher fetch.Fetcher
	final   string
}

func (e *bilibiliTestExecution) Fetcher() fetch.Fetcher                  { return e.fetcher }
func (*bilibiliTestExecution) FFmpeg() downloadtask.FFmpegLocator        { return nil }
func (*bilibiliTestExecution) Credentials() downloadtask.CredentialStore { return nil }
func (*bilibiliTestExecution) UpdateTaskProgress(downloadtask.TaskProgressUpdate) error {
	return nil
}
func (*bilibiliTestExecution) RecordArtifact(downloadtask.TaskArtifact) error { return nil }
func (*bilibiliTestExecution) RecordPostPublishCleanupFailure(downloadtask.TaskArtifact) error {
	return nil
}
func (*bilibiliTestExecution) RecordCheckpoint(downloadtask.PlatformCheckpointEnvelope) error {
	return nil
}
func (e *bilibiliTestExecution) PublishFinal(_ context.Context, temporaryPath string, draft downloadtask.TaskArtifactDraft) (downloadtask.TaskArtifact, error) {
	if err := os.Rename(temporaryPath, e.final); err != nil {
		return downloadtask.TaskArtifact{}, err
	}
	return downloadtask.TaskArtifact{Kind: downloadtask.TaskArtifactFinal, Path: e.final, Size: draft.Size, Primary: true}, nil
}

func TestBilibiliAdapterUsesInjectedFetcherHeadersAndPublishesTemporary(t *testing.T) {
	payload := []byte("bilibili video")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/fresh.m4s", r.URL.Path, "persisted media URLs must not be reused")
		require.Equal(t, "https://www.bilibili.com/", r.Header.Get("Referer"))
		require.Empty(t, r.Header.Get("Cookie"), "session cookies must never be sent to media CDN requests")
		w.Header().Set("Content-Length", "14")
		_, _ = w.Write(payload)
	}))
	defer server.Close()

	apiCalls := 0
	downloader := NewBilibiliDownloader(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		apiCalls++
		require.Equal(t, "/x/player/playurl", req.URL.Path)
		require.Equal(t, "BV1refresh", req.URL.Query().Get("bvid"))
		require.Equal(t, "42", req.URL.Query().Get("cid"))
		body := fmt.Sprintf(`{"code":0,"data":{"quality":80,"accept_quality":[80],"accept_description":["1080P"],"dash":{"video":[{"id":80,"baseUrl":%q,"bandwidth":1000,"codecid":7,"mimeType":"video/mp4"}]}}}`, server.URL+"/fresh.m4s")
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    req,
		}, nil
	}))
	downloader.SetSessData("session-token")
	video := &BilibiliVideo{
		BV:    "BV1refresh",
		Title: "test",
		Parts: []BilibiliPart{{CID: 42, Duration: 10}},
		Streams: []BilibiliStream{{
			Quality:  80,
			VideoURL: server.URL + "/stale.m4s",
		}},
	}
	platformData, err := MarshalTaskData(video, 80, -1)
	require.NoError(t, err)
	dir := t.TempDir()
	finalPath := filepath.Join(dir, "video.mp4")
	execution := &bilibiliTestExecution{fetcher: fetch.New(server.Client()), final: finalPath}
	task := downloadtask.TaskSnapshot{
		PlatformDataVersion: downloadtask.CurrentPlatformDataVersion,
		ID:                  "bilibili-adapter-test",
		OutputPolicy:        downloadtask.OutputPolicy{PlannedFinalPath: finalPath},
		PlatformData:        platformData,
	}

	require.NoError(t, NewAdapter(downloader).RunTask(context.Background(), task, execution))
	require.Equal(t, 1, apiCalls, "ordinary task execution must refresh the playurl")
	require.Equal(t, payload, mustReadBilibiliFile(t, finalPath))
	temporaryPath := bilibiliTaskTemporaryPath(task.ID, finalPath)
	require.NoFileExists(t, temporaryPath)
	require.NoFileExists(t, temporaryPath+".resume.json")
}

func TestBilibiliPartResolutionHonorsTaskCancellation(t *testing.T) {
	requestStarted := make(chan struct{})
	doer := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		close(requestStarted)
		<-req.Context().Done()
		return nil, req.Context().Err()
	})
	downloader := NewBilibiliDownloader(doer)
	video := &BilibiliVideo{
		Title: "parted",
		BV:    "BV1cancel",
		Parts: []BilibiliPart{{CID: 42, Duration: 10}},
	}
	platformData, err := MarshalTaskData(video, 80, 0)
	require.NoError(t, err)
	dir := t.TempDir()
	finalPath := filepath.Join(dir, "video.mp4")
	task := downloadtask.TaskSnapshot{
		PlatformDataVersion: downloadtask.CurrentPlatformDataVersion,
		ID:                  "bilibili-context-test",
		OutputPolicy:        downloadtask.OutputPolicy{PlannedFinalPath: finalPath},
		PlatformData:        platformData,
	}

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- NewAdapter(downloader).RunTask(ctx, task, &bilibiliTestExecution{final: finalPath})
	}()
	<-requestStarted
	cancel()

	require.ErrorIs(t, <-errCh, context.Canceled)
}

func TestBilibiliForbiddenWithoutSessionReturnsStructuredAuthError(t *testing.T) {
	err := bilibiliExecutionError(NewBilibiliDownloader(), &fetch.Error{
		Kind:       fetch.ErrorStatusCode,
		StatusCode: http.StatusForbidden,
	})
	var taskErr *downloadtask.TaskError
	require.ErrorAs(t, err, &taskErr)
	require.Equal(t, "bilibili.auth_required", taskErr.Code)
	require.Equal(t, downloadtask.TaskErrorCategoryPlatform, taskErr.Category)
	require.Equal(t, "login_bilibili", taskErr.UserAction)
}

func TestBilibiliStreamResolutionClassifiesAuthAndRiskControl(t *testing.T) {
	tests := []struct {
		name string
		err  error
		code string
	}{
		{name: "HTTP rate limit", err: &bilibiliAPIError{StatusCode: http.StatusTooManyRequests}, code: "bilibili.risk_control"},
		{name: "business risk control", err: &bilibiliAPIError{Code: -412, Message: "request was banned"}, code: "bilibili.risk_control"},
		{name: "not logged in", err: &bilibiliAPIError{Code: -101, Message: "account not logged in"}, code: "bilibili.auth_required"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := bilibiliStreamResolutionError(NewBilibiliDownloader(), 0, test.err)
			var taskErr *downloadtask.TaskError
			require.ErrorAs(t, err, &taskErr)
			require.Equal(t, test.code, taskErr.Code)
			require.False(t, taskErr.Retryable)
		})
	}
}

func mustReadBilibiliFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return data
}
