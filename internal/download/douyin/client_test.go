package douyin

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const videoResponse = `{"status_code":0,"item_list":[{"aweme_id":"123456","desc":"hello","aweme_type":0,"author":{"nickname":"bob","uid":"u1"},"duration":15000,"video":{"duration":15000,"cover":{"url_list":["https://example.com/cover.jpg"]},"play_addr":{"url_list":["https://example.com/playwm/base"],"width":720,"height":1280},"bit_rate":[{"gear_name":"normal_720_0","bit_rate":1500000,"play_addr":{"url_list":["https://example.com/playwm/720"],"width":720,"height":1280}},{"gear_name":"normal_1080_0","bit_rate":2500000,"play_addr":{"url_list":["https://example.com/playwm/1080"],"width":1080,"height":1920}}]}}]}`

const videoDetailResponse = `{"status_code":0,"aweme_detail":{"aweme_id":"123456","desc":"hello","aweme_type":0,"author":{"nickname":"bob","uid":"u1"},"duration":15000,"video":{"duration":15000,"cover":{"url_list":["https://example.com/cover.jpg"]},"play_addr":{"url_list":["https://example.com/playwm/base"],"width":720,"height":1280},"bit_rate":[{"gear_name":"normal_720_0","bit_rate":1500000,"play_addr":{"url_list":["https://example.com/playwm/720"],"width":720,"height":1280}},{"gear_name":"normal_1080_0","bit_rate":2500000,"play_addr":{"url_list":["https://example.com/playwm/1080"],"width":1080,"height":1920}}]}}}`

const albumResponse = `{"status_code":0,"item_list":[{"aweme_id":"9988","desc":"album","aweme_type":68,"author":{"nickname":"alice","uid":"u2"},"images":[{"url_list":["https://example.com/img1.jpg"],"width":1080,"height":1920},{"url_list":["https://example.com/img2.jpg"],"width":1080,"height":1920}]}]}`

const multiVideoAlbumResponse = `{"status_code":0,"item_list":[{"aweme_id":"m1","desc":"mv","aweme_type":68,"author":{"nickname":"alice","uid":"u2"},"images":[{"url_list":[],"video":{"play_addr":{"url_list":["https://example.com/playwm/v1"],"width":720,"height":1280}},"width":720,"height":1280},{"url_list":[],"video":{"play_addr":{"uri":"vid123"}},"width":1080,"height":1920}]}]}`

const multiVideoAlbumImgBitrateResponse = `{"status_code":0,"item_list":[{"aweme_id":"m2","desc":"mv2","aweme_type":2,"author":{"nickname":"alice","uid":"u2"},"images":[{"url_list":["https://example.com/cover1.jpg"],"width":720,"height":1280},{"url_list":["https://example.com/cover2.jpg"],"width":1080,"height":1920}],"img_bitrate":[{"play_addr":{"url_list":["https://example.com/playwm/v1"],"width":720,"height":1280}},{"play_addr":{"uri":"vid456","width":1080,"height":1920}}]}]}`

const slidesInfoMultiVideoResponse = `{"status_code":0,"aweme_details":[{"aweme_id":"s1","desc":"slides","aweme_type":0,"author":{"nickname":"alice","uid":"u2"},"images":[{"url_list":["https://example.com/cover1.jpg"],"video":{"play_addr":{"url_list":["https://example.com/playwm/v1"],"width":720,"height":1280}},"width":720,"height":1280},{"url_list":["https://example.com/cover2.jpg"],"video":{"play_addr":{"uri":"vid789","width":1080,"height":1920}},"width":1080,"height":1920}]}]}`

const fallbackResponse = `{"status_code":0,"item_list":[{"aweme_id":"77","desc":"fallback","aweme_type":0,"author":{"nickname":"c","uid":"u3"},"video":{"duration":8000,"cover":{"url_list":["https://example.com/cover.jpg"]},"play_addr":{"url_list":["https://example.com/playwm/base"],"width":640,"height":360}}}]}`
const emptyListResponse = `{"status_code":0,"item_list":[]}`
const apiErrorResponse = `{"status_code":1,"status_msg":"unexpected error"}`

func newTestClient(ts *httptest.Server) *Client {
	client := NewClientWithClient(ts.Client())
	client.baseURL = ts.URL
	client.SetShareBaseURL(ts.URL + "/share")
	return client
}

func TestClientGetItemInfoVideo(t *testing.T) {
	const awemeID = "123456"
	const ua = "Test-UA"

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("aweme_id") != awemeID {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if r.Header.Get("User-Agent") != ua {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, videoResponse)
	}))
	defer ts.Close()

	client := newTestClient(ts)
	client.SetUserAgent(ua)

	item, err := client.GetItemInfo(awemeID)
	if err != nil {
		t.Fatalf("GetItemInfo returned error: %v", err)
	}

	if item.Type != "video" {
		t.Fatalf("expected video type, got %s", item.Type)
	}
	if item.ID != awemeID {
		t.Fatalf("expected aweme_id %s, got %s", awemeID, item.ID)
	}
	if item.Duration != 15 {
		t.Fatalf("expected duration 15, got %d", item.Duration)
	}
	if len(item.Streams) != 2 {
		t.Fatalf("expected 2 streams, got %d", len(item.Streams))
	}
	if item.Streams[0].QualityKey != "1080p" {
		t.Fatalf("expected first stream 1080p, got %s", item.Streams[0].QualityKey)
	}
	if strings.Contains(item.Streams[0].URL, "playwm") {
		t.Fatalf("expected playwm to be replaced, got %s", item.Streams[0].URL)
	}
	if item.Cover == "" || item.Author == "" {
		t.Fatal("expected cover and author to be set")
	}
}

func TestClientGetItemInfoVideoNewFormat(t *testing.T) {
	const awemeID = "123456"

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("aweme_id") != awemeID {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, videoDetailResponse)
	}))
	defer ts.Close()

	client := newTestClient(ts)

	item, err := client.GetItemInfo(awemeID)
	if err != nil {
		t.Fatalf("GetItemInfo returned error: %v", err)
	}
	if item.Type != "video" {
		t.Fatalf("expected video type, got %s", item.Type)
	}
	if item.ID != awemeID {
		t.Fatalf("expected aweme_id %s, got %s", awemeID, item.ID)
	}
	if item.Duration != 15 {
		t.Fatalf("expected duration 15, got %d", item.Duration)
	}
	if len(item.Streams) != 2 {
		t.Fatalf("expected 2 streams, got %d", len(item.Streams))
	}
	if item.Streams[0].QualityKey != "1080p" {
		t.Fatalf("expected first stream 1080p, got %s", item.Streams[0].QualityKey)
	}
}

func TestClientGetItemInfoAlbum(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, albumResponse)
	}))
	defer ts.Close()

	client := newTestClient(ts)

	item, err := client.GetItemInfo("9988")
	if err != nil {
		t.Fatalf("GetItemInfo returned error: %v", err)
	}

	if item.Type != "album" {
		t.Fatalf("expected album type, got %s", item.Type)
	}
	if item.Duration != 0 {
		t.Fatalf("expected album duration 0, got %d", item.Duration)
	}
	if len(item.Images) != 2 {
		t.Fatalf("expected 2 images, got %d", len(item.Images))
	}
	if item.Cover != "https://example.com/img1.jpg" {
		t.Fatalf("expected cover from first image, got %s", item.Cover)
	}
}

func TestClientGetItemInfoAlbum_MultiVideoCollection(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, multiVideoAlbumResponse)
	}))
	defer ts.Close()

	client := newTestClient(ts)

	item, err := client.GetItemInfo("m1")
	if err != nil {
		t.Fatalf("GetItemInfo returned error: %v", err)
	}

	if item.Type != "album" {
		t.Fatalf("expected album type, got %s", item.Type)
	}
	if len(item.Images) != 2 {
		t.Fatalf("expected 2 media items, got %d", len(item.Images))
	}
	if item.Images[0].VideoURL != "https://example.com/play/v1" {
		t.Fatalf("expected playwm to be replaced, got %s", item.Images[0].VideoURL)
	}
	if item.Images[1].VideoURL != "https://aweme.snssdk.com/aweme/v1/play/?video_id=vid123&ratio=1080p&line=0" {
		t.Fatalf("expected constructed URL from uri, got %s", item.Images[1].VideoURL)
	}
	if item.Cover != "https://example.com/play/v1" {
		t.Fatalf("expected cover fallback to first video URL, got %s", item.Cover)
	}
}

func TestClientGetItemInfoAlbum_MultiVideoCollection_ImgBitrate(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, multiVideoAlbumImgBitrateResponse)
	}))
	defer ts.Close()

	client := newTestClient(ts)

	item, err := client.GetItemInfo("m2")
	if err != nil {
		t.Fatalf("GetItemInfo returned error: %v", err)
	}

	if item.Type != "album" {
		t.Fatalf("expected album type, got %s", item.Type)
	}
	if len(item.Images) != 2 {
		t.Fatalf("expected 2 media items, got %d", len(item.Images))
	}
	if item.Images[0].VideoURL != "https://example.com/play/v1" {
		t.Fatalf("expected playwm to be replaced, got %s", item.Images[0].VideoURL)
	}
	if item.Images[1].VideoURL != "https://aweme.snssdk.com/aweme/v1/play/?video_id=vid456&ratio=1080p&line=0" {
		t.Fatalf("expected constructed URL from uri, got %s", item.Images[1].VideoURL)
	}
}

func TestClientFallbackToSlidesInfo(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Force the primary API to fail so the client tries slidesinfo.
		if r.URL.Query().Get("aweme_id") != "" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/web/api/v2/aweme/slidesinfo/") {
			if r.URL.Query().Get("aweme_ids") != "[s1]" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, slidesInfoMultiVideoResponse)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer ts.Close()

	client := newTestClient(ts)

	item, err := client.GetItemInfo("s1")
	if err != nil {
		t.Fatalf("GetItemInfo returned error: %v", err)
	}

	if item.Type != "album" {
		t.Fatalf("expected album type, got %s", item.Type)
	}
	if len(item.Images) != 2 {
		t.Fatalf("expected 2 media items, got %d", len(item.Images))
	}
	if item.Images[0].VideoURL != "https://example.com/play/v1" {
		t.Fatalf("expected playwm to be replaced, got %s", item.Images[0].VideoURL)
	}
	if item.Images[1].VideoURL != "https://aweme.snssdk.com/aweme/v1/play/?video_id=vid789&ratio=1080p&line=0" {
		t.Fatalf("expected constructed URL from uri, got %s", item.Images[1].VideoURL)
	}
}

func TestClientNotFound(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, emptyListResponse)
	}))
	defer ts.Close()

	client := newTestClient(ts)
	_, err := client.GetItemInfo("missing")
	if !errors.Is(err, ErrItemNotFound) {
		t.Fatalf("expected ErrItemNotFound, got %v", err)
	}
}

func TestClientRateLimited(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer ts.Close()

	client := newTestClient(ts)
	_, err := client.GetItemInfo("123")
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("expected ErrRateLimited, got %v", err)
	}
}

func TestClientTimeout(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(50 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, emptyListResponse)
	}))
	defer ts.Close()

	httpClient := ts.Client()
	httpClient.Timeout = 10 * time.Millisecond

	client := NewClientWithClient(httpClient)
	client.baseURL = ts.URL

	_, err := client.GetItemInfo("123")
	if !errors.Is(err, ErrRequestTimeout) {
		t.Fatalf("expected ErrRequestTimeout, got %v", err)
	}
}

func TestClientFallbackToPlayAddr(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, fallbackResponse)
	}))
	defer ts.Close()

	client := newTestClient(ts)
	item, err := client.GetItemInfo("77")
	if err != nil {
		t.Fatalf("GetItemInfo returned error: %v", err)
	}
	if len(item.Streams) != 1 {
		t.Fatalf("expected 1 stream, got %d", len(item.Streams))
	}
	if item.Streams[0].QualityKey != "360p" {
		t.Fatalf("expected 360p stream, got %s", item.Streams[0].QualityKey)
	}
}

func TestBuildStreamsUsesVideoResolutionFallback(t *testing.T) {
	item := itemInfoItem{
		AwemeID: "1",
		Video: videoInfo{
			Width:  1920,
			Height: 1080,
			PlayAddr: playAddr{
				URLList: []string{"https://example.com/playwm/abc"},
			},
		},
	}

	result := buildDouyinItem(item)
	if len(result.Streams) != 1 {
		t.Fatalf("expected 1 stream, got %d", len(result.Streams))
	}
	if result.Streams[0].QualityKey != "1080p" {
		t.Fatalf("expected 1080p stream, got %s", result.Streams[0].QualityKey)
	}
	if result.Streams[0].Width != 1920 || result.Streams[0].Height != 1080 {
		t.Fatalf("expected 1920x1080, got %dx%d", result.Streams[0].Width, result.Streams[0].Height)
	}
}

func TestClientAPIError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, apiErrorResponse)
	}))
	defer ts.Close()

	client := newTestClient(ts)
	_, err := client.GetItemInfo("123")
	if !errors.Is(err, ErrAPIError) {
		t.Fatalf("expected ErrAPIError, got %v", err)
	}
}

func TestClientFallbackToSharePage(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("aweme_id") != "" {
			w.Header().Set("Content-Type", "application/json")
			return
		}
		if strings.HasPrefix(r.URL.Path, "/share/video/") {
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprintf(w, `<html><script>window._ROUTER_DATA = {"loaderData":{"video_page":{"videoInfoRes":%s}}};</script></html>`, videoResponse)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer ts.Close()

	client := newTestClient(ts)

	item, err := client.GetItemInfo("123456")
	if err != nil {
		t.Fatalf("GetItemInfo returned error: %v", err)
	}
	if item.ID != "123456" {
		t.Fatalf("expected aweme_id 123456, got %s", item.ID)
	}
	if item.Type != "video" {
		t.Fatalf("expected video type, got %s", item.Type)
	}
}

func TestClientGetItemInfoSharePageFirst(t *testing.T) {
	var apiCalled bool
	var ts *httptest.Server
	ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/media/") {
			w.Header().Set("Content-Type", "video/mp4")
			w.Header().Set("Content-Range", "bytes 0-1/1234")
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write([]byte("ok"))
			return
		}
		if r.URL.Query().Get("aweme_id") != "" {
			apiCalled = true
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, videoResponse)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/share/video/") {
			shareResponse := fmt.Sprintf(`{"status_code":0,"item_list":[{"aweme_id":"sf1","desc":"share first","aweme_type":0,"author":{"nickname":"bob","uid":"u1"},"duration":1000,"video":{"duration":1000,"cover":{"url_list":["%s/cover.jpg"]},"play_addr":{"url_list":["%s/media/source.mp4"],"width":720,"height":1280}}}]}`, ts.URL, ts.URL)
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprintf(w, `<html><script>window._ROUTER_DATA = {"loaderData":{"video_page":{"videoInfoRes":%s}}};</script></html>`, shareResponse)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer ts.Close()

	client := newTestClient(ts)
	item, err := client.GetItemInfo("sf1")
	if err != nil {
		t.Fatalf("GetItemInfo returned error: %v", err)
	}
	if apiCalled {
		t.Fatal("expected share page to be used before API")
	}
	if item.ID != "sf1" || item.Title != "share first" {
		t.Fatalf("unexpected item: %+v", item)
	}
}

func TestClientFallbackToAPIWhenSharePageFails(t *testing.T) {
	const response = `{"status_code":0,"item_list":[{"aweme_id":"api1","desc":"api fallback","aweme_type":0,"author":{"nickname":"bob","uid":"u1"},"duration":1000,"video":{"duration":1000,"cover":{"url_list":["https://example.com/cover.jpg"]},"play_addr":{"url_list":["https://example.com/playwm/base"],"width":720,"height":1280}}}]}`
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/share/") {
			w.Header().Set("Content-Type", "text/html")
			io.WriteString(w, `<html>empty shell</html>`)
			return
		}
		if r.URL.Query().Get("aweme_id") != "" {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, response)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer ts.Close()

	client := newTestClient(ts)
	item, err := client.GetItemInfo("api1")
	if err != nil {
		t.Fatalf("GetItemInfo returned error: %v", err)
	}
	if item.ID != "api1" || item.Title != "api fallback" {
		t.Fatalf("unexpected item: %+v", item)
	}
}

func TestQualityFromGear(t *testing.T) {
	if qualityFromGear("normal_720_0") != "720p" {
		t.Fatal("expected 720p from gear")
	}
	if qualityFromGear("") != "" {
		t.Fatal("expected empty for blank gear")
	}
}

func TestPickNoWatermarkURL(t *testing.T) {
	url := pickNoWatermarkURL([]string{"https://example.com/playwm/123", "https://example.com/play/123"})
	if strings.Contains(url, "playwm") {
		t.Fatalf("expected playwm to be replaced, got %s", url)
	}
}

func TestNewClient(t *testing.T) {
	c := NewClient()
	if c.httpClient == nil {
		t.Fatal("expected non-nil http client")
	}
	if c.userAgent != defaultUserAgent {
		t.Fatal("expected default user agent")
	}
}

func TestSetEmptyValues(t *testing.T) {
	c := NewClient()
	originalUA := c.userAgent
	c.SetUserAgent("")
	if c.userAgent != originalUA {
		t.Fatal("expected user agent unchanged for empty value")
	}

	c.SetHTTPClient(nil)
	if c.httpClient == nil {
		t.Fatal("expected http client unchanged for nil value")
	}

	originalBase := c.baseURL
	c.SetBaseURL("")
	if c.baseURL != originalBase {
		t.Fatal("expected base url unchanged for empty value")
	}
}

func TestInvalidAwemeID(t *testing.T) {
	c := NewClient()
	_, err := c.GetItemInfo("")
	if !errors.Is(err, ErrInvalidAwemeID) {
		t.Fatalf("expected ErrInvalidAwemeID, got %v", err)
	}
	_, err = c.GetItemInfo("   ")
	if !errors.Is(err, ErrInvalidAwemeID) {
		t.Fatalf("expected ErrInvalidAwemeID for whitespace, got %v", err)
	}
}

func TestFirstNonEmpty(t *testing.T) {
	if firstNonEmpty("", "", "a") != "a" {
		t.Fatal("expected 'a'")
	}
	if firstNonEmpty("", "", "") != "" {
		t.Fatal("expected empty string")
	}
}

func TestFirstNonZero(t *testing.T) {
	if firstNonZero(0, 0, 5) != 5 {
		t.Fatal("expected 5")
	}
	if firstNonZero(0, 0, 0) != 0 {
		t.Fatal("expected 0")
	}
}

func TestNormalizeDuration(t *testing.T) {
	if normalizeDuration(0) != 0 {
		t.Fatal("expected 0")
	}
	if normalizeDuration(-1) != 0 {
		t.Fatal("expected 0 for negative")
	}
	if normalizeDuration(500) != 500 {
		t.Fatal("expected 500")
	}
	if normalizeDuration(15000) != 15 {
		t.Fatal("expected 15 for 15000ms")
	}
}

func TestResolutionValue(t *testing.T) {
	if resolutionValue(1080, 1920) != 1080 {
		t.Fatal("expected 1080 for portrait")
	}
	if resolutionValue(1920, 1080) != 1080 {
		t.Fatal("expected 1080 for landscape")
	}
	if resolutionValue(720, 0) != 720 {
		t.Fatal("expected 720 when height is 0")
	}
	if resolutionValue(0, 480) != 480 {
		t.Fatal("expected 480 when width is 0")
	}
}

func TestLooksNotFound(t *testing.T) {
	if !looksNotFound("item not found") {
		t.Fatal("expected true for 'item not found'")
	}
	if !looksNotFound("does not exist") {
		t.Fatal("expected true for 'does not exist'")
	}
	if looksNotFound("success") {
		t.Fatal("expected false for 'success'")
	}
}

func TestIsTimeout(t *testing.T) {
	if isTimeout(nil) {
		t.Fatal("expected false for nil error")
	}
	if isTimeout(errors.New("other error")) {
		t.Fatal("expected false for non-timeout error")
	}
}

func TestBuildImagesEmpty(t *testing.T) {
	images := buildImages([]imageInfo{{URLList: []string{""}}}, nil)
	if len(images) != 0 {
		t.Fatal("expected empty images for blank URLs")
	}
}

func TestResolutionKeyEmpty(t *testing.T) {
	if resolutionKey(0, 0) != "" {
		t.Fatal("expected empty string for 0x0")
	}
}

func TestPickNoWatermarkURLNoPlaywm(t *testing.T) {
	url := pickNoWatermarkURL([]string{"https://example.com/play/123"})
	if url != "https://example.com/play/123" {
		t.Fatalf("expected clean url, got %s", url)
	}
}

func TestPickNoWatermarkURLEmpty(t *testing.T) {
	url := pickNoWatermarkURL([]string{})
	if url != "" {
		t.Fatal("expected empty string for empty list")
	}
	url = pickNoWatermarkURL([]string{"", "  "})
	if url != "" {
		t.Fatal("expected empty string for blank urls")
	}
}

func TestSetHTTPClientValid(t *testing.T) {
	c := NewClient()
	newClient := &http.Client{Timeout: 30 * time.Second}
	c.SetHTTPClient(newClient)
	if c.httpClient == nil {
		t.Fatal("expected http client to be updated")
	}
	if c.httpClient == newClient {
		t.Fatal("expected http client to be cloned")
	}
	if c.httpClient.Timeout != 30*time.Second {
		t.Fatal("expected timeout to be preserved")
	}
	if c.httpClient.Jar == nil {
		t.Fatal("expected cookie jar to be installed")
	}
	if newClient.Jar != nil {
		t.Fatal("expected original client to remain unchanged")
	}
}

func TestNewClientWithClientClonesAndAddsCookieJar(t *testing.T) {
	original := &http.Client{Timeout: 20 * time.Second}
	c := NewClientWithClient(original)
	if c.httpClient == original {
		t.Fatal("expected custom client to be cloned")
	}
	if c.httpClient.Timeout != original.Timeout {
		t.Fatal("expected timeout to be preserved")
	}
	if c.httpClient.Jar == nil {
		t.Fatal("expected cookie jar to be installed")
	}
	if original.Jar != nil {
		t.Fatal("expected original client to remain unchanged")
	}
}

func TestFetchContentSizeRangeGET(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("expected GET, got %s", r.Method)
		}
		if r.Header.Get("Range") != "bytes=0-1" {
			t.Fatalf("expected ranged request, got %s", r.Header.Get("Range"))
		}
		if r.Header.Get("Accept") != "*/*" {
			t.Fatalf("expected Accept */*, got %s", r.Header.Get("Accept"))
		}
		w.Header().Set("Content-Type", "video/mp4")
		w.Header().Set("Content-Range", "bytes 0-1/5678")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write([]byte("ok"))
	}))
	defer ts.Close()

	c := NewClient()
	if size := c.fetchContentSize(ts.Client(), ts.URL); size != 5678 {
		t.Fatalf("expected size 5678, got %d", size)
	}
}

func TestFetchContentSizeDoesNotUseRedirectContentLength(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "/cdn/video.mp4")
		w.Header().Set("Content-Length", "616")
		w.WriteHeader(http.StatusFound)
	}))
	defer ts.Close()

	client := ts.Client()
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}
	c := NewClient()
	if size := c.fetchContentSize(client, ts.URL); size != 0 {
		t.Fatalf("expected redirect content length to be ignored, got %d", size)
	}
}

func TestFetchStreamSizesPreservesExistingSize(t *testing.T) {
	var hits int
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer ts.Close()

	c := NewClientWithClient(ts.Client())
	item := &DouyinItem{
		Type:    "video",
		Streams: []Stream{{QualityKey: "1080p", URL: ts.URL, Size: 123}},
	}
	c.fetchStreamSizes(item)
	if hits != 0 {
		t.Fatalf("expected existing size to skip probing, got %d hits", hits)
	}
	if item.Streams[0].Size != 123 {
		t.Fatalf("expected existing size to be preserved, got %d", item.Streams[0].Size)
	}
}

func TestParseTotalFromContentRange(t *testing.T) {
	cases := map[string]int64{
		"bytes 0-1/123456": 123456,
		"bytes */987654":   987654,
		"bytes 0-1/*":      0,
		"":                 0,
		"invalid":          0,
	}
	for input, expected := range cases {
		if got := parseTotalFromContentRange(input); got != expected {
			t.Fatalf("parseTotalFromContentRange(%q)=%d, expected %d", input, got, expected)
		}
	}
}

func TestSetBaseURLValid(t *testing.T) {
	c := NewClient()
	newURL := "https://custom.api.com/"
	c.SetBaseURL(newURL)
	if c.baseURL != newURL {
		t.Fatal("expected base url to be updated")
	}
}

func TestClientTooManyRequests(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer ts.Close()

	client := newTestClient(ts)
	_, err := client.GetItemInfo("123")
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("expected ErrRateLimited for 429, got %v", err)
	}
}

func TestClientUnexpectedStatus(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer ts.Close()

	client := newTestClient(ts)
	_, err := client.GetItemInfo("123")
	if err == nil {
		t.Fatal("expected error for 500 status")
	}
}

func TestClientInvalidJSON(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, "not valid json")
	}))
	defer ts.Close()

	client := newTestClient(ts)
	_, err := client.GetItemInfo("123")
	if err == nil {
		t.Fatal("expected error for invalid json")
	}
}

func TestClientNotFoundMessage(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"status_code":1,"status_msg":"item not found"}`)
	}))
	defer ts.Close()

	client := newTestClient(ts)
	_, err := client.GetItemInfo("123")
	if !errors.Is(err, ErrItemNotFound) {
		t.Fatalf("expected ErrItemNotFound, got %v", err)
	}
}

func TestClientEmptyErrorMessage(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"status_code":1,"status_msg":""}`)
	}))
	defer ts.Close()

	client := newTestClient(ts)
	_, err := client.GetItemInfo("123")
	if !errors.Is(err, ErrAPIError) {
		t.Fatalf("expected ErrAPIError, got %v", err)
	}
}

func TestQualityFromGearWithP(t *testing.T) {
	if qualityFromGear("1080p") != "1080p" {
		t.Fatal("expected 1080p")
	}
}

func TestSortStreamsByResolution(t *testing.T) {
	streams := []Stream{
		{Width: 720, Height: 1280, Bitrate: 1000},
		{Width: 1080, Height: 1920, Bitrate: 2000},
		{Width: 720, Height: 1280, Bitrate: 1500},
	}
	sortStreamsByResolution(streams)
	if streams[0].Width != 1080 {
		t.Fatal("expected 1080 first")
	}
	if streams[1].Bitrate != 1500 {
		t.Fatal("expected higher bitrate 720p second")
	}
}

func TestBuildStreamsEmptyBitRate(t *testing.T) {
	video := videoInfo{
		PlayAddr: playAddr{
			URLList: []string{},
			Width:   0,
			Height:  0,
		},
		BitRate: []bitRateInfo{},
	}
	streams := buildStreams(video)
	if len(streams) != 0 {
		t.Fatal("expected no streams for empty video info")
	}
}

func TestApplyHeadersEmptyUA(t *testing.T) {
	c := NewClient()
	c.userAgent = ""
	req, _ := http.NewRequest("GET", "http://example.com", nil)
	c.applyHeaders(req)
	if req.Header.Get("User-Agent") != defaultUserAgent {
		t.Fatal("expected default UA when empty")
	}
}

func TestBuildItemInfoURLEmptyBase(t *testing.T) {
	c := NewClient()
	c.baseURL = ""
	url, err := c.buildItemInfoURL("123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(url, "aweme_id=123") {
		t.Fatal("expected aweme_id in url")
	}
	if !strings.Contains(url, "aid=6383") {
		t.Fatal("expected aid in url")
	}
}

func TestBuildStreamsDoesNotBlindlyConstructURI(t *testing.T) {
	video := videoInfo{
		Width:  1920,
		Height: 1080,
		PlayAddr: playAddr{
			Width:  1920,
			Height: 1080,
			URI:    "v0200fg10000test123456789",
		},
		BitRate: []bitRateInfo{},
	}
	streams := buildStreams(video)
	if len(streams) != 0 {
		t.Fatalf("expected no streams from URI without probing, got %d", len(streams))
	}
}

func TestClientProbeRatioStreamsWhenBitRateEmpty(t *testing.T) {
	const responseWithURI = `{"status_code":0,"item_list":[{"aweme_id":"123456","desc":"test","aweme_type":0,"author":{"nickname":"bob","uid":"u1"},"duration":15000,"video":{"duration":15000,"width":1920,"height":1080,"cover":{"url_list":["https://example.com/cover.jpg"]},"play_addr":{"url_list":["https://example.com/playwm/base"],"width":1920,"height":1080,"uri":"v0200fg10000test123456789"}}}]}`

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/aweme/v1/play/") {
			if r.Method != http.MethodGet {
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			if r.Header.Get("Range") != "bytes=0-1" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			sizes := map[string]string{
				"1080p": "300",
				"720p":  "200",
				"540p":  "100",
				"480p":  "300",
				"360p":  "300",
			}
			size := sizes[r.URL.Query().Get("ratio")]
			if size == "" {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "video/mp4")
			w.Header().Set("Content-Range", "bytes 0-1/"+size)
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write([]byte("ok"))
			return
		}
		if r.URL.Query().Get("aweme_id") != "" {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, responseWithURI)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer ts.Close()

	client := newTestClient(ts)
	client.playBaseURL = ts.URL + "/aweme/v1/play/"
	item, err := client.GetItemInfo("123456")
	if err != nil {
		t.Fatalf("GetItemInfo returned error: %v", err)
	}

	if len(item.Streams) != 3 {
		t.Fatalf("expected 3 probed streams, got %d", len(item.Streams))
	}
	expectedQualities := []string{"1080p", "720p", "540p"}
	for i, expected := range expectedQualities {
		if item.Streams[i].QualityKey != expected {
			t.Fatalf("expected stream[%d] to be %s, got %s", i, expected, item.Streams[i].QualityKey)
		}
	}
	if item.Streams[0].Size != 300 || item.Streams[1].Size != 200 || item.Streams[2].Size != 100 {
		t.Fatalf("unexpected stream sizes: %+v", item.Streams)
	}
}

func TestClientProbeRatioStreamsWhenBitRateURLsEmpty(t *testing.T) {
	const responseWithURIOnlyBitRate = `{"status_code":0,"item_list":[{"aweme_id":"123456","desc":"test","aweme_type":0,"author":{"nickname":"bob","uid":"u1"},"duration":15000,"video":{"duration":15000,"width":1920,"height":1080,"cover":{"url_list":["https://example.com/cover.jpg"]},"play_addr":{"url_list":[],"width":1920,"height":1080,"uri":"v0200fg10000test123456789"},"bit_rate":[{"gear_name":"normal_720_0","bit_rate":1000,"play_addr":{"url_list":[],"width":1280,"height":720}},{"gear_name":"normal_1080_0","bit_rate":2000,"play_addr":{"url_list":[],"width":1920,"height":1080}}]}}]}`

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/aweme/v1/play/") {
			if r.Method != http.MethodGet {
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			if r.Header.Get("Range") != "bytes=0-1" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			sizes := map[string]string{
				"1080p": "300",
				"720p":  "200",
				"540p":  "100",
			}
			size := sizes[r.URL.Query().Get("ratio")]
			if size == "" {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "video/mp4")
			w.Header().Set("Content-Range", "bytes 0-1/"+size)
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write([]byte("ok"))
			return
		}
		if r.URL.Query().Get("aweme_id") != "" {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, responseWithURIOnlyBitRate)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer ts.Close()

	client := newTestClient(ts)
	client.playBaseURL = ts.URL + "/aweme/v1/play/"
	item, err := client.GetItemInfo("123456")
	if err != nil {
		t.Fatalf("GetItemInfo returned error: %v", err)
	}

	if len(item.Streams) != 3 {
		t.Fatalf("expected 3 probed streams, got %d", len(item.Streams))
	}
	expectedQualities := []string{"1080p", "720p", "540p"}
	for i, expected := range expectedQualities {
		if item.Streams[i].QualityKey != expected {
			t.Fatalf("expected stream[%d] to be %s, got %s", i, expected, item.Streams[i].QualityKey)
		}
	}
}
