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
          "ipLocation": "上海",
          "tagList": [{"id": "tag1", "name": "旅行", "type": "topic"}],
          "interactInfo": {"likedCount": "123", "collectedCount": 45, "commentCount": "6", "shareCount": 7},
          "imageList": [
            {"urlDefault": "https://example.com/img1.jpg", "urlPre": "https://example.com/img1-pre.jpg", "infoList": [{"url": "https://example.com/img1-info"}], "width": 100, "height": 200, "fileId": "file1", "livePhoto": true, "stream": {"h264": [{"masterUrl": "https://example.com/live.mp4"}]}},
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
	assert.Equal(t, "上海", item.IPLocation)
	require.Len(t, item.Tags, 1)
	assert.Equal(t, "旅行", item.Tags[0].Name)
	assert.Equal(t, "123", item.InteractInfo.LikedCount)
	assert.Equal(t, "45", item.InteractInfo.CollectedCount)
	assert.Equal(t, "6", item.InteractInfo.CommentCount)
	assert.Equal(t, "7", item.InteractInfo.ShareCount)
	assert.Equal(t, "https://example.com/img1.jpg", item.Cover)
	require.Len(t, item.Images, 2)
	assert.Equal(t, "https://example.com/img1.jpg", item.Images[0].URL)
	assert.Equal(t, []string{"https://example.com/img1-pre.jpg", "https://example.com/img1-info"}, item.Images[0].BackupURLs)
	assert.Equal(t, 100, item.Images[0].Width)
	assert.Equal(t, 200, item.Images[0].Height)
	assert.Equal(t, "file1", item.Images[0].FileID)
	assert.True(t, item.Images[0].LivePhoto)
	assert.Equal(t, "https://example.com/live.mp4", item.Images[0].LivePhotoURL)
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
                  {"qualityType": "HD", "masterUrl": "https://example.com/hd.mp4", "backupUrls": ["https://example.com/hd-backup.mp4"], "width": 720, "height": 1280, "size": 123, "format": "mp4", "fps": 30, "videoCodec": "h264", "videoBitrate": 1044072, "audioCodec": "aac", "audioBitrate": 64011, "streamDesc": "WM_X264_MP4_web", "streamType": 259, "weight": 62, "duration": 829489, "defaultStream": 0, "hdrType": 0, "rotate": 0}
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
	assert.Equal(t, "hd_259", strings.ToLower(item.Streams[0].QualityKey))
	assert.Equal(t, "HD", item.Streams[0].QualityName)
	assert.Equal(t, 720, item.Streams[0].Width)
	assert.Equal(t, 1280, item.Streams[0].Height)
	assert.Equal(t, "https://example.com/hd.mp4", item.Streams[0].URL)
	assert.Equal(t, []string{"https://example.com/hd-backup.mp4"}, item.Streams[0].BackupURLs)
	assert.Equal(t, int64(123), item.Streams[0].Size)
	assert.Equal(t, "mp4", item.Streams[0].Format)
	assert.Equal(t, 30, item.Streams[0].FPS)
	assert.Equal(t, "h264", item.Streams[0].VideoCodec)
	assert.Equal(t, int64(1044072), item.Streams[0].VideoBitrate)
	assert.Equal(t, "aac", item.Streams[0].AudioCodec)
	assert.Equal(t, int64(64011), item.Streams[0].AudioBitrate)
	assert.Equal(t, "WM_X264_MP4_web", item.Streams[0].StreamDesc)
	assert.Equal(t, 259, item.Streams[0].StreamType)
	assert.Equal(t, 62, item.Streams[0].Weight)
	assert.Equal(t, int64(829489), item.Streams[0].Duration)
}

func TestClientVideoStreamSelectionPrefersCodecAndWeight(t *testing.T) {
	const noteID = "0123456789abcdef01234571"

	state := fmt.Sprintf(`{
  "note": {
    "noteDetailMap": {
      %q: {
        "note": {
          "type": "video",
          "title": "video-title",
          "video": {
            "media": {
              "stream": {
                "h264": [
                  {"qualityType": "HD", "masterUrl": "https://example.com/h264-720.mp4", "width": 1280, "height": 720, "size": 115000000, "videoCodec": "h264", "streamType": 259, "weight": 62, "videoBitrate": 1044072}
                ],
                "h265": [
                  {"qualityType": "HD", "masterUrl": "https://example.com/h265-720.mp4", "width": 1280, "height": 720, "size": 71000000, "videoCodec": "hevc", "streamType": 114, "weight": 62, "videoBitrate": 584000},
                  {"qualityType": "HD", "masterUrl": "https://example.com/h265-1080.mp4", "width": 1920, "height": 1080, "size": 85000000, "videoCodec": "hevc", "streamType": 115, "weight": 70, "videoBitrate": 724000}
                ],
                "av1": [],
                "h266": []
              }
            }
          }
        }
      }
    }
  }
}`, noteID)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		io.WriteString(w, wrapInitialState(state))
	}))
	defer ts.Close()

	c := newTestClient(ts)
	item, err := c.GetNoteInfo(noteID)
	require.NoError(t, err)

	require.Len(t, item.Streams, 3)
	assert.Equal(t, "hd_115", item.Streams[0].QualityKey)
	assert.Equal(t, "https://example.com/h265-1080.mp4", item.Streams[0].URL)
	assert.Equal(t, 115, item.Streams[0].StreamType)
	assert.Equal(t, "hevc", item.Streams[0].VideoCodec)
	assert.Equal(t, "hd_114", item.Streams[1].QualityKey)
	assert.Equal(t, "https://example.com/h265-720.mp4", item.Streams[1].URL)
	assert.Equal(t, "hd_259", item.Streams[2].QualityKey)
	assert.Equal(t, "https://example.com/h264-720.mp4", item.Streams[2].URL)
}

func TestStreamBetterThanPrefersH265ForSameResolution(t *testing.T) {
	h264 := XHSStream{URL: "h264", Width: 1280, Height: 720, Size: 115, VideoCodec: "h264", Weight: 62, StreamType: 259}
	h265 := XHSStream{URL: "h265", Width: 1280, Height: 720, Size: 71, VideoCodec: "hevc", Weight: 62, StreamType: 114}

	assert.True(t, streamBetterThan(h265, h264))
	assert.False(t, streamBetterThan(h264, h265))
}

func TestStreamBetterThanDoesNotPreferFutureCodecsByDefault(t *testing.T) {
	h265 := XHSStream{URL: "h265", Width: 1280, Height: 720, VideoCodec: "hevc", Weight: 62}
	h264 := XHSStream{URL: "h264", Width: 1280, Height: 720, VideoCodec: "h264", Weight: 62}
	av1 := XHSStream{URL: "av1", Width: 1280, Height: 720, VideoCodec: "av1", Weight: 62}
	h266 := XHSStream{URL: "h266", Width: 1280, Height: 720, VideoCodec: "h266", Weight: 62}

	assert.True(t, streamBetterThan(h265, av1))
	assert.True(t, streamBetterThan(h264, av1))
	assert.True(t, streamBetterThan(h265, h266))
}

func TestStreamBetterThanUsesDefaultStreamAsTieBreaker(t *testing.T) {
	preferred := XHSStream{URL: "a", Width: 1280, Height: 720, VideoCodec: "hevc", Weight: 62, StreamType: 114, DefaultStream: 1}
	other := XHSStream{URL: "b", Width: 1280, Height: 720, VideoCodec: "hevc", Weight: 62, StreamType: 114}

	assert.True(t, streamBetterThan(preferred, other))
	assert.False(t, streamBetterThan(other, preferred))
}

func TestFirstMediaURLUsesDeterministicCodecOrder(t *testing.T) {
	stream := map[string]any{
		"h264": []any{map[string]any{"masterUrl": "https://example.com/h264.mp4"}},
		"h265": []any{map[string]any{"masterUrl": "https://example.com/h265.mp4"}},
	}

	assert.Equal(t, "https://example.com/h265.mp4", firstMediaURL(stream))
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
