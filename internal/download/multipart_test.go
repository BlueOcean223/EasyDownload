package downloader

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckRangeSupportDoesNotTrustHeadAlone(t *testing.T) {
	const totalSize = 2 * 1024 * 1024
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodHead:
			w.Header().Set("Accept-Ranges", "bytes")
			w.Header().Set("Content-Length", "2097152")
			w.WriteHeader(http.StatusOK)
		case http.MethodGet:
			if r.Header.Get("Range") == "" {
				w.WriteHeader(http.StatusOK)
				return
			}
			// Server ignores Range even though HEAD claimed support.
			w.Header().Set("Content-Length", "2097152")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("x"))
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	defer ts.Close()

	md := NewMultipartDownloader()
	result := md.CheckRangeSupport(context.Background(), ts.URL)
	if result.Error != nil {
		t.Fatalf("CheckRangeSupport returned error: %v", result.Error)
	}
	if result.SupportsRange {
		t.Fatal("GET Range returned 200; multipart range support must be false")
	}
	if result.ContentLength != totalSize {
		t.Fatalf("ContentLength=%d, want %d", result.ContentLength, totalSize)
	}
}

func TestDownloadChunkShortReadFails(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Range"); got != "bytes=0-9" {
			http.Error(w, "bad range", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Range", "bytes 0-9/10")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write([]byte("short"))
	}))
	defer ts.Close()

	md := NewMultipartDownloader()
	outputPath := filepath.Join(t.TempDir(), "out.bin")
	if err := os.WriteFile(outputPath, make([]byte, 10), 0644); err != nil {
		t.Fatal(err)
	}

	err := md.downloadChunk(context.Background(), ts.URL, outputPath, &ChunkInfo{Index: 0, Start: 0, End: 9}, 0, nil)
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("error=%v, want io.ErrUnexpectedEOF", err)
	}
}

func TestDownloadChunkContentRangeMismatchFails(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Range", "bytes 1-10/11")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write([]byte("0123456789"))
	}))
	defer ts.Close()

	md := NewMultipartDownloader()
	outputPath := filepath.Join(t.TempDir(), "out.bin")
	if err := os.WriteFile(outputPath, make([]byte, 10), 0644); err != nil {
		t.Fatal(err)
	}

	err := md.downloadChunk(context.Background(), ts.URL, outputPath, &ChunkInfo{Index: 0, Start: 0, End: 9}, 0, nil)
	if err == nil || !strings.Contains(err.Error(), "Content-Range mismatch") {
		t.Fatalf("error=%v, want Content-Range mismatch", err)
	}
}
