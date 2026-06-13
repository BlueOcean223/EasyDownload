package downloader

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestDownloadFileResumable416LocalLargerIsNotSuccess(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Range", "bytes */5")
		w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
	}))
	defer ts.Close()

	dest := filepath.Join(t.TempDir(), "file.bin")
	if err := os.WriteFile(dest, []byte("0123456789"), 0644); err != nil {
		t.Fatal(err)
	}

	downloaded, total, err := DownloadFileResumable(context.Background(), ts.Client(), ts.URL, dest, ResumableDownloadOptions{}, nil)
	if err == nil {
		t.Fatalf("expected error, got nil (downloaded=%d total=%d)", downloaded, total)
	}
	if total != 5 {
		t.Fatalf("total=%d, want 5", total)
	}
}

func TestDownloadFileResumable416ExactSizeIsSuccess(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Range", "bytes */5")
		w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
	}))
	defer ts.Close()

	dest := filepath.Join(t.TempDir(), "file.bin")
	if err := os.WriteFile(dest, []byte("01234"), 0644); err != nil {
		t.Fatal(err)
	}

	downloaded, total, err := DownloadFileResumable(context.Background(), ts.Client(), ts.URL, dest, ResumableDownloadOptions{}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if downloaded != 5 || total != 5 {
		t.Fatalf("downloaded=%d total=%d, want 5/5", downloaded, total)
	}
}
