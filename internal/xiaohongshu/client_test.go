package xiaohongshu

import (
	"bytes"
	"compress/zlib"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestClient(ts *httptest.Server) *Client {
	c := NewClientWithClient(ts.Client())
	c.SetBaseURL(ts.URL)
	return c
}

func wrapInitialState(stateJSON string) string {
	return "<html><body><script>\nwindow.__INITIAL_STATE__ = " + stateJSON + "\n</script></body></html>"
}

func TestClientGetNoteInfoAlbum(t *testing.T) {
	const noteID = "0123456789abcdef01234567"

	state := fmt.Sprintf(`{
  "note": {
    "noteDetailMap": {
      %q: {
        "note": {
          "title": "hello",
          "desc": "desc",
          "user": {"nickname": "alice"},
          "imageList": [
            {"urlDefault": "https://example.com/img1.jpg", "width": 100, "height": 200},
            {"urlPre": "https://example.com/img2.jpg", "width": 300, "height": 400}
          ]
        }
      }
    }
  }
}`, noteID)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/explore/"+noteID {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		io.WriteString(w, wrapInitialState(state))
	}))
	defer ts.Close()

	c := newTestClient(ts)

	item, err := c.GetNoteInfo(noteID)
	require.NoError(t, err)

	assert.Equal(t, "image", item.Type)
	assert.Equal(t, noteID, item.ID)
	assert.Equal(t, "hello", item.Title)
	assert.Equal(t, "alice", item.Author)
	assert.Equal(t, "https://example.com/img1.jpg", item.Cover)
	require.Len(t, item.Images, 2)
	assert.Equal(t, "https://example.com/img1.jpg", item.Images[0].URL)
	assert.Equal(t, 100, item.Images[0].Width)
	assert.Equal(t, 200, item.Images[0].Height)
	assert.Equal(t, "https://example.com/img2.jpg", item.Images[1].URL)
	assert.Equal(t, 300, item.Images[1].Width)
	assert.Equal(t, 400, item.Images[1].Height)
}

func TestClientGetNoteInfoVideoStreams(t *testing.T) {
	const noteID = "0123456789abcdef01234568"

	state := fmt.Sprintf(`{
  "note": {
    "noteDetailMap": {
      %q: {
        "note": {
          "type": "video",
          "title": "video-title",
          "user": {"nickname": "bob"},
          "cover": "https://example.com/cover.jpg",
          "video": {
            "media": {
              "stream": [
                {"qualityType": "hd", "masterUrl": "https://example.com/hd.m3u8", "backupUrls": ["https://example.com/hd-backup.m3u8"]},
                {"qualityType": "sd", "masterUrl": "https://example.com/sd.mp4", "backupUrls": []}
              ]
            }
          }
        }
      }
    }
  }
}`, noteID)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/explore/"+noteID {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		io.WriteString(w, wrapInitialState(state))
	}))
	defer ts.Close()

	c := newTestClient(ts)

	item, err := c.GetNoteInfo(noteID)
	require.NoError(t, err)

	assert.Equal(t, "video", item.Type)
	assert.Equal(t, "bob", item.Author)
	assert.Equal(t, "video-title", item.Title)
	assert.Len(t, item.Streams, 2)
}

func TestClientGetNoteInfoVideoStreamsMapSchema(t *testing.T) {
	const noteID = "0123456789abcdef01234569"

	state := fmt.Sprintf(`{
  "note": {
    "noteDetailMap": {
      %q: {
        "note": {
          "type": "video",
          "title": "video-title",
          "user": {"nickname": "bob"},
          "imageList": [
            {"urlDefault": "https://example.com/cover.jpg", "width": 100, "height": 200}
          ],
          "video": {
            "media": {
              "stream": {
                "h264": [
                  {"qualityType": "HD", "masterUrl": "https://example.com/hd.mp4", "width": 720, "height": 1280, "size": 123, "format": "mp4"}
                ],
                "h265": []
              }
            }
          }
        }
      }
    }
  }
}`, noteID)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/explore/"+noteID {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		io.WriteString(w, wrapInitialState(state))
	}))
	defer ts.Close()

	c := newTestClient(ts)

	item, err := c.GetNoteInfo(noteID)
	require.NoError(t, err)

	assert.Equal(t, "video", item.Type)
	assert.Equal(t, "https://example.com/cover.jpg", item.Cover)
	require.Len(t, item.Streams, 1)
	assert.Equal(t, "hd", strings.ToLower(item.Streams[0].QualityKey))
	assert.Equal(t, "HD", item.Streams[0].QualityName)
	assert.Equal(t, 720, item.Streams[0].Width)
	assert.Equal(t, 1280, item.Streams[0].Height)
	assert.Equal(t, int64(123), item.Streams[0].Size)
	assert.Equal(t, "mp4", item.Streams[0].Format)
}

func TestClientGetNoteInfoNotFound(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer ts.Close()

	c := newTestClient(ts)
	_, err := c.GetNoteInfo("0123456789abcdef01234567")
	require.ErrorIs(t, err, ErrNoteNotFound)
}

func TestClientGetNoteInfoRateLimited(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer ts.Close()

	c := newTestClient(ts)
	_, err := c.GetNoteInfo("0123456789abcdef01234567")
	require.ErrorIs(t, err, ErrRateLimited)
}

func TestClientGetNoteInfoForbiddenBlocked(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer ts.Close()

	c := newTestClient(ts)
	_, err := c.GetNoteInfo("0123456789abcdef01234567")
	require.ErrorIs(t, err, ErrNoteBlocked)
}

func TestClientGetNoteInfoMissingInitialState(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		io.WriteString(w, "<html><body>nope</body></html>")
	}))
	defer ts.Close()

	c := newTestClient(ts)
	_, err := c.GetNoteInfo("0123456789abcdef01234567")
	require.ErrorIs(t, err, ErrInitialStateNotFound)
}

func TestClientGetNoteInfoBadJSON(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		io.WriteString(w, "<script>window.__INITIAL_STATE__ = {not-json}</script>")
	}))
	defer ts.Close()

	c := newTestClient(ts)
	_, err := c.GetNoteInfo("0123456789abcdef01234567")
	var jsonErr *json.SyntaxError
	require.ErrorAs(t, err, &jsonErr)
}

func TestClientGetNoteInfoMissingNoteInMap(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		io.WriteString(w, wrapInitialState(`{"note":{"noteDetailMap":{"other":{"note":{"title":"t","user":{"nickname":"n"}}}}}}`))
	}))
	defer ts.Close()

	c := newTestClient(ts)
	_, err := c.GetNoteInfo("0123456789abcdef01234567")
	require.ErrorIs(t, err, ErrNoteNotFound)
}

func TestClientGetNoteInfoInvalidNoteID(t *testing.T) {
	c := NewClient()
	_, err := c.GetNoteInfo("   ")
	require.ErrorIs(t, err, ErrInvalidNoteID)
}

func TestClientGetNoteInfoDeflateZlib(t *testing.T) {
	const noteID = "0123456789abcdef01234570"
	state := fmt.Sprintf(`{"note":{"noteDetailMap":{%q:{"note":{"title":"t","desc":"d","user":{"nickname":"n"},"imageList":[{"urlDefault":"https://example.com/img.jpg"}]}}}}}`, noteID)
	htmlBody := wrapInitialState(state)

	var buf bytes.Buffer
	zw := zlib.NewWriter(&buf)
	_, _ = zw.Write([]byte(htmlBody))
	_ = zw.Close()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Encoding", "deflate")
		_, _ = w.Write(buf.Bytes())
	}))
	defer ts.Close()

	c := newTestClient(ts)
	item, err := c.GetNoteInfo(noteID)
	require.NoError(t, err)
	require.NotNil(t, item)
	assert.Equal(t, noteID, item.ID)
	assert.Equal(t, "image", item.Type)
}

func TestClientSetters(t *testing.T) {
	c := NewClient()
	c.SetUserAgent("CustomUA")
	c.SetBaseURL("https://custom.example.com")
	c.SetHTTPClient(nil)

	assert.Equal(t, "CustomUA", c.userAgent)
	assert.Equal(t, "https://custom.example.com", c.baseURL)
}

func TestJsToJSON(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "undefined value",
			input:    `{"key": undefined}`,
			expected: `{"key": null}`,
		},
		{
			name:     "undefined in array",
			input:    `[undefined, "test"]`,
			expected: `[null, "test"]`,
		},
		{
			name:     "void 0 value",
			input:    `{"key": void 0}`,
			expected: `{"key": null}`,
		},
		{
			name:     "trailing comma in object",
			input:    `{"key": "value",}`,
			expected: `{"key": "value"}`,
		},
		{
			name:     "trailing comma in array",
			input:    `["a", "b",]`,
			expected: `["a", "b"]`,
		},
		{
			name:     "multiple undefined",
			input:    `{"a": undefined, "b": undefined}`,
			expected: `{"a": null, "b": null}`,
		},
		{
			name:     "nested undefined",
			input:    `{"outer": {"inner": undefined}}`,
			expected: `{"outer": {"inner": null}}`,
		},
		{
			name:     "valid json unchanged",
			input:    `{"key": "value", "num": 123}`,
			expected: `{"key": "value", "num": 123}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := jsToJSON([]byte(tt.input))
			assert.Equal(t, tt.expected, string(result))
		})
	}
}
