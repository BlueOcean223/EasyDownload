package xiaohongshu

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newShortParser(ts *httptest.Server) *Parser {
	client := ts.Client()
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}
	p := NewParserWithClient(client)
	if parsed, err := url.Parse(ts.URL); err == nil {
		p.SetShortDomains([]string{parsed.Hostname()})
	}
	return p
}

func TestParserShortLinkRedirect(t *testing.T) {
	const noteID = "6579f5c5000000003200c3d2"
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://www.xiaohongshu.com/explore/"+noteID+"?xsec_token=abc", http.StatusFound)
	}))
	defer ts.Close()

	parser := newShortParser(ts)
	got, err := parser.Parse(ts.URL + "/short")
	require.NoError(t, err)
	assert.Equal(t, noteID, got)
}

func TestParserShortLinkHTMLFoundFallback(t *testing.T) {
	const noteID = "6579f5c5000000003200c3d2"
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// xhslink can return a non-3xx status with an embedded target URL.
		w.WriteHeader(http.StatusNotFound)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<a href="https://www.xiaohongshu.com/discovery/item/` + noteID + `?xsec_token=abc&amp;type=normal">Found</a>`))
	}))
	defer ts.Close()

	parser := newShortParser(ts)
	res, err := parser.ParseWithURL(ts.URL + "/o/short")
	require.NoError(t, err)
	assert.Equal(t, noteID, res.NoteID)
	assert.Equal(t, "abc", res.XsecToken)
	assert.Contains(t, res.FullURL, noteID)
}

func TestParserLongLinks(t *testing.T) {
	parser := NewParser()
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"explore", "https://www.xiaohongshu.com/explore/6411cf99000000001300b6d9", "6411cf99000000001300b6d9"},
		{"discovery", "https://www.xiaohongshu.com/discovery/item/674051740000000007027a15?xsec_token=foo", "674051740000000007027a15"},
		{"no-www", "https://xiaohongshu.com/explore/6411cf99000000001300b6d9/", "6411cf99000000001300b6d9"},
		{"uppercase", "https://www.xiaohongshu.com/explore/6579F5C5000000003200C3D2", "6579f5c5000000003200c3d2"},
		{"share-text", "desc https://www.xiaohongshu.com/explore/6411cf99000000001300b6d9 more", "6411cf99000000001300b6d9"},
	}

	for _, tc := range cases {
		got, err := parser.Parse(tc.input)
		require.NoError(t, err, tc.name)
		assert.Equal(t, tc.want, got, tc.name)
	}
}

func TestParserNoteIDOnly(t *testing.T) {
	parser := NewParser()
	const noteID = "6579f5c5000000003200c3d2"
	got, err := parser.Parse(noteID)
	require.NoError(t, err)
	assert.Equal(t, noteID, got)
}

func TestParserInvalidInput(t *testing.T) {
	parser := NewParser()

	_, err := parser.Parse("")
	require.ErrorIs(t, err, ErrNoXHSURL)

	_, err = parser.Parse("   ")
	require.ErrorIs(t, err, ErrNoXHSURL)

	_, err = parser.Parse("plain text only")
	require.ErrorIs(t, err, ErrNoXHSURL)

	_, err = parser.Parse("https://example.com/explore/6411cf99000000001300b6d9")
	require.Error(t, err)

	_, err = parser.Parse("https://xiaohongshu.com.evil.com/explore/6411cf99000000001300b6d9")
	require.ErrorIs(t, err, ErrNoXHSURL)

	_, err = parser.Parse("https://www.xiaohongshu.com/explore/")
	require.ErrorIs(t, err, ErrNoteIDNotFound)

	_, err = parser.Parse("https://www.xiaohongshu.com/explore/not-hex")
	require.ErrorIs(t, err, ErrNoteIDNotFound)
}

func TestShortLinkMissingLocation(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusFound)
	}))
	defer ts.Close()

	parser := newShortParser(ts)
	_, err := parser.Parse(ts.URL)
	require.ErrorIs(t, err, ErrShortLinkResolution)
}

func TestShortLinkNon3xxStatus(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	parser := newShortParser(ts)
	_, err := parser.Parse(ts.URL)
	require.ErrorIs(t, err, ErrShortLinkResolution)
}

func TestParseTooManyRedirects(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, r.URL.String()+"x", http.StatusFound)
	}))
	defer ts.Close()

	parser := newShortParser(ts)
	_, err := parser.Parse(ts.URL + "/loop")
	require.ErrorIs(t, err, ErrShortLinkResolution)
}

func TestNewParserWithClientAddsRedirectCheck(t *testing.T) {
	client := &http.Client{}
	parser := NewParserWithClient(client)
	assert.NotNil(t, parser.client.CheckRedirect)
}

func TestSetShortDomainsEmpty(t *testing.T) {
	parser := NewParser()
	parser.SetShortDomains([]string{})
	_, ok := parser.shortDomains["xhslink.com"]
	assert.True(t, ok, "expected default xhslink.com when empty domains provided")
}
