package bilibili

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"

	"EasyDownload/internal/download/fetch"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDownloadFileWithFallbackNoURL(t *testing.T) {
	bd := NewBilibiliDownloader()
	destPath := filepath.Join(t.TempDir(), "out.m4s")

	_, err := bd.downloadFileWithFallback(context.Background(), nil, "", []string{"", " "}, destPath, 0, nil)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "no download URL available")
}

func TestGetContentLengthWithFallbackUsesBackup(t *testing.T) {
	var primaryHEADs atomic.Int32
	var backupHEADs atomic.Int32

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		switch r.URL.Path {
		case "/primary":
			primaryHEADs.Add(1)
			w.WriteHeader(http.StatusForbidden)
		case "/backup":
			backupHEADs.Add(1)
			w.Header().Set("Content-Length", "123")
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer ts.Close()

	bd := NewBilibiliDownloader()
	got := bd.getContentLengthWithFallback(context.Background(), fetch.New(ts.Client()), ts.URL+"/primary", []string{ts.URL + "/backup"})

	assert.Equal(t, int64(123), got)
	assert.Equal(t, int32(1), primaryHEADs.Load())
	assert.Equal(t, int32(1), backupHEADs.Load())
}
