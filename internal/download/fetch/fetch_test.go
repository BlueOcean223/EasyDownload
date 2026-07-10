package fetch

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestDownload200ProducesVerifiedTemporaryFile(t *testing.T) {
	payload := []byte("hello")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") != "test-agent" {
			http.Error(w, "missing user agent", http.StatusBadRequest)
			return
		}
		if r.Header.Get("Accept-Encoding") != "identity" {
			http.Error(w, "byte transfers must use identity encoding", http.StatusBadRequest)
			return
		}
		w.Header().Set("ETag", `"v1"`)
		_, _ = w.Write(payload)
	}))
	defer server.Close()

	dir := t.TempDir()
	tempPath := filepath.Join(dir, "out.part")
	finalPath := filepath.Join(dir, "out.txt")
	result, err := New(server.Client()).Download(context.Background(), Request{
		URL: server.URL, Headers: map[string]string{"User-Agent": "test-agent", "Accept-Encoding": "gzip"},
	}, Destination{TemporaryPath: tempPath}, nil)
	require.NoError(t, err)
	require.Equal(t, int64(len(payload)), result.Downloaded)
	require.Equal(t, tempPath, result.TemporaryPath)
	require.Equal(t, sha256Hex(payload), result.SHA256)
	require.Equal(t, `"v1"`, result.Validator.ETag)
	require.FileExists(t, tempPath)
	require.NoFileExists(t, finalPath)
	require.FileExists(t, result.ResumeStatePath)
	require.Equal(t, payload, mustReadFile(t, tempPath))
}

func TestDownloadResumesWithETagAndIfRange(t *testing.T) {
	payload := []byte("hello world")
	var gotRange atomic.Value
	var gotIfRange atomic.Value
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotRange.Store(r.Header.Get("Range"))
		gotIfRange.Store(r.Header.Get("If-Range"))
		if r.Header.Get("Range") != "bytes=6-" || r.Header.Get("If-Range") != `"v1"` {
			http.Error(w, "bad range request", http.StatusBadRequest)
			return
		}
		w.Header().Set("ETag", `"v1"`)
		w.Header().Set("Content-Range", "bytes 6-10/11")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(payload[6:])
	}))
	defer server.Close()

	destination := testDestination(t)
	require.NoError(t, os.WriteFile(destination.TemporaryPath, payload[:6], 0o644))
	writeState(t, destination, resumeState{
		SelectedURL: server.URL, ETag: `"v1"`, Total: int64(len(payload)), TotalKnown: true,
	})

	result, err := New(server.Client()).Download(context.Background(), Request{
		URL: server.URL, Identity: ResourceIdentity{ExpectedSize: int64(len(payload))},
		ResumePolicy: ResumePolicy{Enabled: true, RequireRange: true},
	}, destination, nil)
	require.NoError(t, err)
	require.True(t, result.Resumed)
	require.Equal(t, "bytes=6-", gotRange.Load())
	require.Equal(t, `"v1"`, gotIfRange.Load())
	require.Equal(t, payload, mustReadFile(t, destination.TemporaryPath))
	require.Equal(t, sha256Hex(payload), result.SHA256)
}

func TestDownloadResumesWithLastModifiedIfRange(t *testing.T) {
	payload := []byte("abcdefghij")
	lastModified := "Wed, 21 Oct 2015 07:28:00 GMT"
	var gotIfRange atomic.Value
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotIfRange.Store(r.Header.Get("If-Range"))
		w.Header().Set("Last-Modified", lastModified)
		w.Header().Set("Content-Range", "bytes 4-9/10")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(payload[4:])
	}))
	defer server.Close()

	destination := testDestination(t)
	require.NoError(t, os.WriteFile(destination.TemporaryPath, payload[:4], 0o644))
	writeState(t, destination, resumeState{
		SelectedURL: server.URL, LastModified: lastModified, Total: 10, TotalKnown: true,
	})
	_, err := New(server.Client()).Download(context.Background(), Request{
		URL: server.URL, ResumePolicy: ResumePolicy{Enabled: true, RequireRange: true},
	}, destination, nil)
	require.NoError(t, err)
	require.Equal(t, lastModified, gotIfRange.Load())
	require.Equal(t, payload, mustReadFile(t, destination.TemporaryPath))
}

func TestDownloadRejectsInvalid206WithoutAppending(t *testing.T) {
	tests := []struct {
		name         string
		contentRange string
		etag         string
	}{
		{name: "missing content range", etag: `"v1"`},
		{name: "malformed content range", contentRange: "bytes nope", etag: `"v1"`},
		{name: "wrong start", contentRange: "bytes 4-10/11", etag: `"v1"`},
		{name: "wrong total", contentRange: "bytes 5-11/12", etag: `"v1"`},
		{name: "missing validator", contentRange: "bytes 5-10/11"},
		{name: "changed validator", contentRange: "bytes 5-10/11", etag: `"v2"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prefix := []byte("hello")
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tt.contentRange != "" {
					w.Header().Set("Content-Range", tt.contentRange)
				}
				if tt.etag != "" {
					w.Header().Set("ETag", tt.etag)
				}
				w.WriteHeader(http.StatusPartialContent)
				_, _ = w.Write([]byte(" world"))
			}))
			defer server.Close()

			destination := testDestination(t)
			require.NoError(t, os.WriteFile(destination.TemporaryPath, prefix, 0o644))
			writeState(t, destination, resumeState{
				SelectedURL: server.URL, ETag: `"v1"`, Total: 11, TotalKnown: true,
			})
			_, err := New(server.Client()).Download(context.Background(), Request{
				URL: server.URL, ResumePolicy: ResumePolicy{Enabled: true, RequireRange: true},
			}, destination, nil)
			require.Error(t, err)
			var fetchErr *Error
			require.True(t, errors.As(err, &fetchErr))
			require.Contains(t, []ErrorKind{ErrorIntegrity, ErrorIdentityMismatch}, fetchErr.Kind)
			require.Equal(t, prefix, mustReadFile(t, destination.TemporaryPath))
		})
	}
}

func TestRangeIgnored200SafelyRestartsAndEmitsReset(t *testing.T) {
	payload := []byte("new-content")
	var gotRange atomic.Value
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotRange.Store(r.Header.Get("Range"))
		w.Header().Set("ETag", `"v2"`)
		_, _ = w.Write(payload)
	}))
	defer server.Close()

	destination := testDestination(t)
	require.NoError(t, os.WriteFile(destination.TemporaryPath, []byte("old"), 0o644))
	writeState(t, destination, resumeState{
		SelectedURL: server.URL, ETag: `"v1"`, Total: 11, TotalKnown: true,
	})
	var events []Progress
	result, err := New(server.Client()).Download(context.Background(), Request{
		URL: server.URL, ResumePolicy: ResumePolicy{Enabled: true, RestartWhenRangeIgnored: true},
	}, destination, func(progress Progress) {
		events = append(events, progress)
	})
	require.NoError(t, err)
	require.False(t, result.Resumed)
	require.Equal(t, "bytes=3-", gotRange.Load())
	require.Equal(t, payload, mustReadFile(t, destination.TemporaryPath))
	require.True(t, containsProgressKind(events, ProgressReset))
	state := readState(t, destination)
	require.Equal(t, `"v2"`, state.ETag)
	require.Equal(t, int64(len(payload)), state.Total)
}

func TestRequireRangePreservesPartialWhenServerReturns200(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"v2"`)
		_, _ = w.Write([]byte("replacement"))
	}))
	defer server.Close()

	destination := testDestination(t)
	prefix := []byte("old")
	require.NoError(t, os.WriteFile(destination.TemporaryPath, prefix, 0o644))
	writeState(t, destination, resumeState{SelectedURL: server.URL, ETag: `"v1"`, Total: 10, TotalKnown: true})
	_, err := New(server.Client()).Download(context.Background(), Request{
		URL: server.URL, ResumePolicy: ResumePolicy{Enabled: true, RequireRange: true},
	}, destination, nil)
	requireFetchErrorKind(t, err, ErrorRangeUnsupported)
	require.Equal(t, prefix, mustReadFile(t, destination.TemporaryPath))
	require.FileExists(t, destination.ResumeStatePath)
}

func Test416CompletesAlreadyDownloadedPartial(t *testing.T) {
	payload := []byte("complete")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"v1"`)
		w.Header().Set("Content-Range", "bytes */8")
		w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
	}))
	defer server.Close()

	destination := testDestination(t)
	require.NoError(t, os.WriteFile(destination.TemporaryPath, payload, 0o644))
	writeState(t, destination, resumeState{SelectedURL: server.URL, ETag: `"v1"`, Total: 8, TotalKnown: true})
	result, err := New(server.Client()).Download(context.Background(), Request{
		URL: server.URL, Identity: ResourceIdentity{ExpectedSize: 8},
		ResumePolicy: ResumePolicy{Enabled: true, RequireRange: true},
	}, destination, nil)
	require.NoError(t, err)
	require.True(t, result.Resumed)
	require.Equal(t, sha256Hex(payload), result.SHA256)
	require.Equal(t, payload, mustReadFile(t, destination.TemporaryPath))
}

func TestInvalid416SafelyRestarts(t *testing.T) {
	payload := []byte("new-data")
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Header.Get("Range") != "" {
			w.Header().Set("ETag", `"v1"`)
			w.Header().Set("Content-Range", "bytes */8")
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
			return
		}
		w.Header().Set("ETag", `"v2"`)
		_, _ = w.Write(payload)
	}))
	defer server.Close()

	destination := testDestination(t)
	require.NoError(t, os.WriteFile(destination.TemporaryPath, []byte("old"), 0o644))
	writeState(t, destination, resumeState{SelectedURL: server.URL, ETag: `"v1"`, Total: 3, TotalKnown: true})
	result, err := New(server.Client()).Download(context.Background(), Request{
		URL: server.URL, ResumePolicy: ResumePolicy{Enabled: true, RestartWhenRangeIgnored: true},
	}, destination, nil)
	require.NoError(t, err)
	require.Equal(t, int32(2), requests.Load())
	require.False(t, result.Resumed)
	require.Equal(t, payload, mustReadFile(t, destination.TemporaryPath))
}

func TestEquivalentMirrorResumesOnlyWhenIdentityMatches(t *testing.T) {
	payload := []byte("abcdefghij")
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "primary unavailable", http.StatusServiceUnavailable)
	}))
	defer primary.Close()
	var mirrorRange atomic.Value
	mirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mirrorRange.Store(r.Header.Get("Range"))
		w.Header().Set("ETag", `"same"`)
		w.Header().Set("Content-Range", "bytes 5-9/10")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(payload[5:])
	}))
	defer mirror.Close()

	destination := testDestination(t)
	require.NoError(t, os.WriteFile(destination.TemporaryPath, payload[:5], 0o644))
	writeState(t, destination, resumeState{SelectedURL: primary.URL, ETag: `"same"`, Total: 10, TotalKnown: true})
	result, err := New(mirror.Client()).Download(context.Background(), Request{
		URL: primary.URL, EquivalentMirrorURLs: []string{mirror.URL},
		ResumePolicy: ResumePolicy{Enabled: true, RequireRange: true},
	}, destination, nil)
	require.NoError(t, err)
	require.Equal(t, mirror.URL, result.URL)
	require.Equal(t, "bytes=5-", mirrorRange.Load())
	require.Equal(t, payload, mustReadFile(t, destination.TemporaryPath))
}

func TestEquivalentMirrorChangedValidatorNeverSplicesPartial(t *testing.T) {
	oldPayload := []byte("AAAAABBBBB")
	newPayload := []byte("01234VWXYZ")
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "primary unavailable", http.StatusServiceUnavailable)
	}))
	defer primary.Close()
	var mirrorRequests atomic.Int32
	mirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mirrorRequests.Add(1)
		w.Header().Set("ETag", `"new"`)
		if r.Header.Get("Range") != "" {
			w.Header().Set("Content-Range", "bytes 5-9/10")
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(newPayload[5:])
			return
		}
		_, _ = w.Write(newPayload)
	}))
	defer mirror.Close()

	destination := testDestination(t)
	require.NoError(t, os.WriteFile(destination.TemporaryPath, oldPayload[:5], 0o644))
	writeState(t, destination, resumeState{SelectedURL: primary.URL, ETag: `"old"`, Total: 10, TotalKnown: true})
	var events []Progress
	result, err := New(mirror.Client()).Download(context.Background(), Request{
		URL: primary.URL, EquivalentMirrorURLs: []string{mirror.URL},
		ResumePolicy: ResumePolicy{Enabled: true, RestartWhenRangeIgnored: true},
	}, destination, func(progress Progress) {
		events = append(events, progress)
	})
	require.NoError(t, err)
	require.Equal(t, int32(2), mirrorRequests.Load())
	require.False(t, result.Resumed)
	require.Equal(t, newPayload, mustReadFile(t, destination.TemporaryPath))
	require.NotEqual(t, append(oldPayload[:5], newPayload[5:]...), mustReadFile(t, destination.TemporaryPath))
	require.True(t, containsProgressKind(events, ProgressReset))
}

func TestMissingOrCorruptSidecarForcesSafeRedownload(t *testing.T) {
	for _, test := range []struct {
		name    string
		sidecar []byte
	}{
		{name: "missing"},
		{name: "corrupt", sidecar: []byte("{")},
	} {
		t.Run(test.name, func(t *testing.T) {
			payload := []byte("fresh")
			var gotRange atomic.Value
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotRange.Store(r.Header.Get("Range"))
				w.Header().Set("ETag", `"fresh"`)
				_, _ = w.Write(payload)
			}))
			defer server.Close()
			destination := testDestination(t)
			require.NoError(t, os.WriteFile(destination.TemporaryPath, []byte("stale"), 0o644))
			if test.sidecar != nil {
				require.NoError(t, os.WriteFile(destination.ResumeStatePath, test.sidecar, 0o600))
			}
			_, err := New(server.Client()).Download(context.Background(), Request{
				URL: server.URL, ResumePolicy: ResumePolicy{Enabled: true, RestartWhenRangeIgnored: true},
			}, destination, nil)
			require.NoError(t, err)
			require.Equal(t, "", gotRange.Load())
			require.Equal(t, payload, mustReadFile(t, destination.TemporaryPath))
		})
	}
}

func TestNetworkShortReadRetriesWithValidatedRange(t *testing.T) {
	payload := []byte("0123456789")
	var requests atomic.Int32
	var secondRange atomic.Value
	var secondIfRange atomic.Value
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		current := requests.Add(1)
		w.Header().Set("ETag", `"v1"`)
		if current == 1 {
			w.Header().Set("Content-Length", "10")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(payload[:5])
			return
		}
		secondRange.Store(r.Header.Get("Range"))
		secondIfRange.Store(r.Header.Get("If-Range"))
		w.Header().Set("Content-Range", "bytes 5-9/10")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(payload[5:])
	}))
	defer server.Close()

	destination := testDestination(t)
	result, err := New(server.Client()).Download(context.Background(), Request{
		URL:          server.URL,
		ResumePolicy: ResumePolicy{Enabled: true, RequireRange: true},
		RetryPolicy:  RetryPolicy{MaxAttempts: 2, InitialBackoff: time.Millisecond},
	}, destination, nil)
	require.NoError(t, err)
	require.Equal(t, int32(2), requests.Load())
	require.Equal(t, "bytes=5-", secondRange.Load())
	require.Equal(t, `"v1"`, secondIfRange.Load())
	require.True(t, result.Resumed)
	require.Equal(t, payload, mustReadFile(t, destination.TemporaryPath))
}

func TestTooManyRequestsIsNotRetriedUnlessExplicitlyConfigured(t *testing.T) {
	t.Run("default policy does not retry platform risk control", func(t *testing.T) {
		var requests atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			requests.Add(1)
			w.WriteHeader(http.StatusTooManyRequests)
		}))
		defer server.Close()

		_, err := New(server.Client()).Download(context.Background(), Request{
			URL:                  server.URL,
			EquivalentMirrorURLs: []string{server.URL + "?mirror=1"},
			RetryPolicy:          RetryPolicy{MaxAttempts: 3, InitialBackoff: time.Millisecond},
		}, testDestination(t), nil)
		requireFetchErrorKind(t, err, ErrorStatusCode)
		require.Equal(t, int32(1), requests.Load())
		var fetchErr *Error
		require.ErrorAs(t, err, &fetchErr)
		require.Equal(t, 1, fetchErr.Attempts)
	})

	t.Run("caller may explicitly opt in", func(t *testing.T) {
		payload := []byte("ok")
		var requests atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			if requests.Add(1) < 3 {
				w.WriteHeader(http.StatusTooManyRequests)
				return
			}
			_, _ = w.Write(payload)
		}))
		defer server.Close()

		result, err := New(server.Client()).Download(context.Background(), Request{
			URL: server.URL,
			RetryPolicy: RetryPolicy{
				MaxAttempts:          3,
				InitialBackoff:       time.Millisecond,
				RetryableStatusCodes: []int{http.StatusTooManyRequests},
			},
		}, testDestination(t), nil)
		require.NoError(t, err)
		require.Equal(t, int32(3), requests.Load())
		require.Equal(t, 3, result.Attempts)
	})
}

func TestNoProgressTimeoutCancelsStalledBodyWithStructuredError(t *testing.T) {
	handlerDone := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "5")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		handlerDone <- struct{}{}
	}))
	defer server.Close()

	_, err := New(server.Client()).Download(context.Background(), Request{
		URL:               server.URL,
		NoProgressTimeout: 30 * time.Millisecond,
		RetryPolicy:       RetryPolicy{MaxAttempts: 1},
	}, testDestination(t), nil)
	requireFetchErrorKind(t, err, ErrorTimeout)
	select {
	case <-handlerDone:
	case <-time.After(time.Second):
		t.Fatal("stalled HTTP handler was not canceled")
	}
}

func TestTimeoutRetriesAfterSafeReset(t *testing.T) {
	payload := []byte("hello world")
	var requests atomic.Int32
	progressObserved := make(chan struct{}, 1)
	doer := httpDoerFunc(func(request *http.Request) (*http.Response, error) {
		current := requests.Add(1)
		var body io.ReadCloser
		if current == 1 {
			body = &timeoutAfterProgressBody{
				reader:           bytes.NewReader(payload[:5]),
				progressObserved: progressObserved,
			}
		} else {
			body = io.NopCloser(bytes.NewReader(payload))
		}
		return &http.Response{
			StatusCode:    http.StatusOK,
			Header:        make(http.Header),
			Body:          body,
			ContentLength: int64(len(payload)),
			Request:       request,
		}, nil
	})

	var progressEvents []Progress
	destination := testDestination(t)
	result, err := New(doer).Download(context.Background(), Request{
		URL: "https://example.test/video",
		ResumePolicy: ResumePolicy{
			Enabled:                 true,
			RestartWhenRangeIgnored: true,
		},
		RetryPolicy: RetryPolicy{MaxAttempts: 2, InitialBackoff: time.Nanosecond},
	}, destination, func(progress Progress) {
		progressEvents = append(progressEvents, progress)
		if progress.Kind == ProgressUpdate && progress.Downloaded == 5 {
			select {
			case progressObserved <- struct{}{}:
			default:
			}
		}
	})
	require.NoError(t, err)
	require.Equal(t, int32(2), requests.Load())
	require.Equal(t, 2, result.Attempts)
	require.Equal(t, payload, mustReadFile(t, destination.TemporaryPath))
	require.True(t, progressResetFollowsDownloadedBytes(progressEvents), "events=%+v", progressEvents)
}

type httpDoerFunc func(*http.Request) (*http.Response, error)

func (doer httpDoerFunc) Do(request *http.Request) (*http.Response, error) {
	return doer(request)
}

type timeoutAfterProgressBody struct {
	reader           *bytes.Reader
	progressObserved <-chan struct{}
}

func (body *timeoutAfterProgressBody) Read(buffer []byte) (int, error) {
	if body.reader.Len() > 0 {
		return body.reader.Read(buffer)
	}
	<-body.progressObserved
	return 0, context.DeadlineExceeded
}

func (*timeoutAfterProgressBody) Close() error {
	return nil
}

func TestNoProgressTimeoutResetsWhenBytesArrive(t *testing.T) {
	payload := []byte("12345")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(payload)))
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)
		for _, value := range payload {
			_, _ = w.Write([]byte{value})
			flusher.Flush()
			time.Sleep(50 * time.Millisecond)
		}
	}))
	defer server.Close()

	result, err := New(server.Client()).Download(context.Background(), Request{
		URL:               server.URL,
		NoProgressTimeout: 150 * time.Millisecond,
	}, testDestination(t), nil)
	require.NoError(t, err)
	require.Equal(t, int64(len(payload)), result.Downloaded)
}

func TestExpectedSHA256MismatchRemovesUntrustedPartial(t *testing.T) {
	payload := []byte("wrong")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"v1"`)
		_, _ = w.Write(payload)
	}))
	defer server.Close()

	destination := testDestination(t)
	_, err := New(server.Client()).Download(context.Background(), Request{
		URL: server.URL, Identity: ResourceIdentity{SHA256: sha256Hex([]byte("right"))},
	}, destination, nil)
	requireFetchErrorKind(t, err, ErrorIntegrity)
	require.NoFileExists(t, destination.TemporaryPath)
	require.NoFileExists(t, destination.ResumeStatePath)
}

func TestSidecarAtomicReplacementLeavesLatestValidState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "download.resume.json")
	store := fileSidecarStore{}
	require.NoError(t, store.Save(path, resumeState{SelectedURL: "https://one.example", ETag: `"one"`}))
	require.NoError(t, store.Save(path, resumeState{SelectedURL: "https://two.example", ETag: `"two"`, Total: 42, TotalKnown: true}))
	state, err := store.Load(path)
	require.NoError(t, err)
	require.Equal(t, "https://two.example", state.SelectedURL)
	require.Equal(t, `"two"`, state.ETag)
	require.Equal(t, int64(42), state.Total)
	leftovers, err := filepath.Glob(filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*"))
	require.NoError(t, err)
	require.Empty(t, leftovers)
}

func TestSidecarSaveFaultsPreserveOldIdentityAndPreventAppend(t *testing.T) {
	for _, stage := range []sidecarSaveStage{
		sidecarStageCreate,
		sidecarStageWrite,
		sidecarStageSync,
		sidecarStageReplace,
	} {
		t.Run(string(stage), func(t *testing.T) {
			payload := []byte("hello world")
			primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "temporary failure", http.StatusServiceUnavailable)
			}))
			defer primary.Close()
			mirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				require.Equal(t, "bytes=5-", r.Header.Get("Range"))
				require.Equal(t, `"v1"`, r.Header.Get("If-Range"))
				w.Header().Set("ETag", `"v1"`)
				w.Header().Set("Content-Range", "bytes 5-10/11")
				w.WriteHeader(http.StatusPartialContent)
				_, _ = w.Write(payload[5:])
			}))
			defer mirror.Close()

			destination := testDestination(t)
			require.NoError(t, os.WriteFile(destination.TemporaryPath, payload[:5], 0o644))
			initialState := resumeState{
				SelectedURL: primary.URL,
				ETag:        `"v1"`,
				Total:       int64(len(payload)),
				TotalKnown:  true,
			}
			writeState(t, destination, initialState)
			oldSidecar := mustReadFile(t, destination.ResumeStatePath)

			client := &Client{
				doer: primary.Client(),
				sidecars: fileSidecarStore{beforeSaveStage: func(current sidecarSaveStage) error {
					if current == stage {
						return errors.New("injected sidecar failure")
					}
					return nil
				}},
			}
			_, err := client.Download(context.Background(), Request{
				URL:                  primary.URL,
				EquivalentMirrorURLs: []string{mirror.URL},
				ResumePolicy:         ResumePolicy{Enabled: true, RequireRange: true},
			}, destination, nil)
			requireFetchErrorKind(t, err, ErrorResumeState)
			require.Equal(t, payload[:5], mustReadFile(t, destination.TemporaryPath), "failed identity update must not append bytes")
			require.Equal(t, oldSidecar, mustReadFile(t, destination.ResumeStatePath), "atomic save must preserve the previous complete sidecar")
			leftovers, globErr := filepath.Glob(filepath.Join(filepath.Dir(destination.ResumeStatePath), "."+filepath.Base(destination.ResumeStatePath)+".tmp-*"))
			require.NoError(t, globErr)
			require.Empty(t, leftovers)
		})
	}
}

func TestSidecarDirectorySyncFaultOccursAfterCommittedReplace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "download.resume.json")
	require.NoError(t, (fileSidecarStore{}).Save(path, resumeState{SelectedURL: "https://old.example", ETag: `"old"`}))
	store := fileSidecarStore{beforeSaveStage: func(stage sidecarSaveStage) error {
		if stage == sidecarStageDirSync {
			return errors.New("injected directory sync failure")
		}
		return nil
	}}

	// Replace already committed, so Save must not claim the old state remains
	// active merely because the post-commit durability sync could not run.
	require.NoError(t, store.Save(path, resumeState{SelectedURL: "https://new.example", ETag: `"new"`}))
	state, err := (fileSidecarStore{}).Load(path)
	require.NoError(t, err)
	require.Equal(t, "https://new.example", state.SelectedURL)
	require.Equal(t, `"new"`, state.ETag)
}

func TestMultipartDownloadsValidatedPartsAndAssemblesInOrder(t *testing.T) {
	payload := []byte("0123456789abcdef")
	var rangedRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var start, end int64
		_, err := fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &start, &end)
		require.NoError(t, err)
		require.GreaterOrEqual(t, start, int64(0))
		require.Less(t, end, int64(len(payload)))
		if start > 0 || end > 0 {
			require.Equal(t, `"entity-v1"`, r.Header.Get("If-Range"))
			rangedRequests.Add(1)
		}
		w.Header().Set("ETag", `"entity-v1"`)
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(payload)))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(payload[start : end+1])
	}))
	defer server.Close()

	destination := testDestination(t)
	var progressEvents []Progress
	result, err := New(server.Client()).Download(context.Background(), Request{
		URL:             server.URL,
		Identity:        ResourceIdentity{ExpectedSize: int64(len(payload)), SHA256: sha256Hex(payload)},
		MultipartPolicy: MultipartPolicy{Enabled: true, PartSize: 4, Threshold: 1},
	}, destination, func(progress Progress) {
		progressEvents = append(progressEvents, progress)
	})
	require.NoError(t, err)
	require.GreaterOrEqual(t, rangedRequests.Load(), int32(4))
	require.Equal(t, payload, mustReadFile(t, destination.TemporaryPath))
	require.Equal(t, sha256Hex(payload), result.SHA256)
	require.Equal(t, int64(len(payload)), result.Total)
	require.Equal(t, int64(len(payload)), progressEvents[len(progressEvents)-1].Downloaded)
	for index := 1; index < len(progressEvents); index++ {
		if progressEvents[index].Kind == ProgressUpdate && progressEvents[index-1].Kind == ProgressUpdate {
			require.GreaterOrEqual(t, progressEvents[index].Downloaded, progressEvents[index-1].Downloaded)
		}
	}
	require.NoDirExists(t, destination.TemporaryPath+".multipart")
}

func TestMultipartRejectsPartIdentityOrTotalChangeBeforeAssembly(t *testing.T) {
	for _, test := range []struct {
		name string
		kind ErrorKind
		edit func(http.Header, int64, int64, int64)
	}{
		{
			name: "validator changes",
			kind: ErrorIdentityMismatch,
			edit: func(header http.Header, start, _, _ int64) {
				if start >= 4 {
					header.Set("ETag", `"entity-v2"`)
				}
			},
		},
		{
			name: "total changes",
			kind: ErrorIntegrity,
			edit: func(header http.Header, start, end, total int64) {
				if start >= 4 {
					header.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, total+1))
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			payload := []byte("0123456789abcdef")
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var start, end int64
				_, err := fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &start, &end)
				require.NoError(t, err)
				w.Header().Set("ETag", `"entity-v1"`)
				w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(payload)))
				test.edit(w.Header(), start, end, int64(len(payload)))
				w.WriteHeader(http.StatusPartialContent)
				_, _ = w.Write(payload[start : end+1])
			}))
			defer server.Close()

			destination := testDestination(t)
			_, err := New(server.Client()).Download(context.Background(), Request{
				URL:             server.URL,
				MultipartPolicy: MultipartPolicy{Enabled: true, PartSize: 4, Threshold: 1},
			}, destination, nil)
			requireFetchErrorKind(t, err, test.kind)
			require.NoFileExists(t, destination.TemporaryPath)
			require.NoDirExists(t, destination.TemporaryPath+".multipart")
		})
	}
}

func TestProbeReturnsValidator(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodHead {
			http.Error(w, "expected HEAD", http.StatusBadRequest)
			return
		}
		require.Equal(t, "identity", r.Header.Get("Accept-Encoding"))
		w.Header().Set("Accept-Ranges", "bytes")
		w.Header().Set("Content-Type", "video/mp4")
		w.Header().Set("Content-Length", "12")
		w.Header().Set("ETag", `"probe"`)
	}))
	defer server.Close()
	probe, err := New(server.Client()).Probe(context.Background(), ProbeRequest{
		URL:     server.URL,
		Headers: map[string]string{"Accept-Encoding": "gzip"},
	})
	require.NoError(t, err)
	require.True(t, probe.AcceptRanges)
	require.Equal(t, int64(12), probe.ContentSize)
	require.Equal(t, "video/mp4", probe.ContentType)
	require.Equal(t, `"probe"`, probe.Validator.ETag)
}

func testDestination(t *testing.T) Destination {
	t.Helper()
	dir := t.TempDir()
	return Destination{
		TemporaryPath:   filepath.Join(dir, "download.part"),
		ResumeStatePath: filepath.Join(dir, "download.resume.json"),
	}
}

func writeState(t *testing.T, destination Destination, state resumeState) {
	t.Helper()
	state.Version = resumeStateVersion
	require.NoError(t, (fileSidecarStore{}).Save(destination.ResumeStatePath, state))
}

func readState(t *testing.T, destination Destination) resumeState {
	t.Helper()
	state, err := (fileSidecarStore{}).Load(destination.ResumeStatePath)
	require.NoError(t, err)
	return state
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return data
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func requireFetchErrorKind(t *testing.T, err error, kind ErrorKind) {
	t.Helper()
	require.Error(t, err)
	var fetchErr *Error
	require.True(t, errors.As(err, &fetchErr), fmt.Sprintf("error type: %T", err))
	require.Equal(t, kind, fetchErr.Kind)
}

func containsProgressKind(events []Progress, kind ProgressKind) bool {
	for _, event := range events {
		if event.Kind == kind {
			return true
		}
	}
	return false
}

func progressResetFollowsDownloadedBytes(events []Progress) bool {
	sawDownloadedBytes := false
	for _, event := range events {
		if event.Kind == ProgressUpdate && event.Downloaded > 0 {
			sawDownloadedBytes = true
		}
		if sawDownloadedBytes && event.Kind == ProgressReset {
			return true
		}
	}
	return false
}

func TestValidateSHA256RejectsMalformedValue(t *testing.T) {
	destination := testDestination(t)
	_, err := New(nil).Download(context.Background(), Request{
		URL: "https://example.invalid", Identity: ResourceIdentity{SHA256: strings.Repeat("x", 64)},
	}, destination, nil)
	require.ErrorContains(t, err, "SHA256")
}
