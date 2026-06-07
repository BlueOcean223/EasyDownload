package douyin

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
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

func TestParseShortLink(t *testing.T) {
	const awemeID = "9876543210"
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://www.douyin.com/video/"+awemeID+"?previous_page=copy_link", http.StatusFound)
	}))
	defer ts.Close()

	parser := newShortParser(ts)
	got, err := parser.Parse(ts.URL + "/short")
	if err != nil {
		t.Fatalf("Parse short link returned error: %v", err)
	}
	if got != awemeID {
		t.Fatalf("expected %s, got %s", awemeID, got)
	}
}

func TestParseLongLinks(t *testing.T) {
	parser := NewParser()
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"video", "https://www.douyin.com/video/1234567890123456789", "1234567890123456789"},
		{"note", "https://www.douyin.com/note/22334455/?previous_page=app_code_link", "22334455"},
		{"slides", "https://www.iesdouyin.com/share/slides/33445566/?region=CN&is_slides=1", "33445566"},
		{"modal", "https://www.douyin.com/user/abc?modal_id=99887766&something=else", "99887766"},
	}

	for _, tc := range cases {
		got, err := parser.Parse(tc.input)
		if err != nil {
			t.Fatalf("%s: unexpected error: %v", tc.name, err)
		}
		if got != tc.want {
			t.Fatalf("%s: expected %s, got %s", tc.name, tc.want, got)
		}
	}
}

func TestParseShortLinkToSlidesShare(t *testing.T) {
	const awemeID = "7581318093000712817"
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://www.iesdouyin.com/share/slides/"+awemeID+"/?is_slides=1&contains_video_type_clip=1", http.StatusFound)
	}))
	defer ts.Close()

	parser := newShortParser(ts)
	got, err := parser.Parse(ts.URL + "/short")
	if err != nil {
		t.Fatalf("Parse short link returned error: %v", err)
	}
	if got != awemeID {
		t.Fatalf("expected %s, got %s", awemeID, got)
	}
}

func TestParseShareText(t *testing.T) {
	parser := NewParser()
	text := "描述段落 https://example.com/other 跳转到抖音 https://www.douyin.com/note/77665544/ 再复制"

	id, err := parser.Parse(text)
	if err != nil {
		t.Fatalf("expected to parse from share text, got error: %v", err)
	}
	if id != "77665544" {
		t.Fatalf("expected 77665544, got %s", id)
	}
}

func TestParseInvalidInput(t *testing.T) {
	parser := NewParser()

	if _, err := parser.Parse("plain text only"); !errors.Is(err, ErrNoDouyinURL) {
		t.Fatalf("expected ErrNoDouyinURL, got %v", err)
	}

	if _, err := parser.Parse("https://www.douyin.com/video/"); !errors.Is(err, ErrAwemeIDNotFound) {
		t.Fatalf("expected ErrAwemeIDNotFound, got %v", err)
	}

	if _, err := parser.parseURL("https://example.com/video/123", 0); !errors.Is(err, ErrInvalidDouyinURL) {
		t.Fatalf("expected ErrInvalidDouyinURL, got %v", err)
	}

	if _, err := parser.Parse("https://douyin.com.evil.example/video/123456"); !errors.Is(err, ErrNoDouyinURL) {
		t.Fatalf("expected ErrNoDouyinURL for lookalike domain, got %v", err)
	}
}

func TestShortLinkMissingLocation(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusFound)
	}))
	defer ts.Close()

	parser := newShortParser(ts)
	if _, err := parser.Parse(ts.URL); !errors.Is(err, ErrShortLinkResolution) {
		t.Fatalf("expected ErrShortLinkResolution, got %v", err)
	}
}

func TestNewParserWithClientAddsRedirectCheck(t *testing.T) {
	client := &http.Client{}
	parser := NewParserWithClient(client)
	if parser.client.CheckRedirect == nil {
		t.Fatal("expected redirect check to be set on custom client")
	}
}

func TestParseEmptyInput(t *testing.T) {
	parser := NewParser()
	if _, err := parser.Parse(""); !errors.Is(err, ErrNoDouyinURL) {
		t.Fatalf("expected ErrNoDouyinURL for empty input, got %v", err)
	}
	if _, err := parser.Parse("   "); !errors.Is(err, ErrNoDouyinURL) {
		t.Fatalf("expected ErrNoDouyinURL for whitespace input, got %v", err)
	}
}

func TestParseTooManyRedirects(t *testing.T) {
	redirectCount := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirectCount++
		http.Redirect(w, r, r.URL.String()+"x", http.StatusFound)
	}))
	defer ts.Close()

	parser := newShortParser(ts)
	_, err := parser.Parse(ts.URL + "/loop")
	if err == nil {
		t.Fatal("expected error for too many redirects")
	}
	if !errors.Is(err, ErrShortLinkResolution) {
		t.Fatalf("expected ErrShortLinkResolution, got %v", err)
	}
}

func TestSetShortDomainsEmpty(t *testing.T) {
	parser := NewParser()
	parser.SetShortDomains([]string{})
	if _, ok := parser.shortDomains["v.douyin.com"]; !ok {
		t.Fatal("expected default v.douyin.com when empty domains provided")
	}
}

func TestIsShortDomainEmpty(t *testing.T) {
	parser := NewParser()
	if parser.isShortDomain("") {
		t.Fatal("empty string should not be a short domain")
	}
}

func TestNewParserWithNilClient(t *testing.T) {
	parser := NewParserWithClient(nil)
	if parser.client == nil {
		t.Fatal("expected non-nil client when nil passed")
	}
}

func TestKeepDigitsEmpty(t *testing.T) {
	if keepDigits("") != "" {
		t.Fatal("expected empty string for empty input")
	}
	if keepDigits("abc") != "" {
		t.Fatal("expected empty string for non-digit input")
	}
}

func TestExtractAwemeIDFromQuery(t *testing.T) {
	u, _ := url.Parse("https://www.douyin.com/discover?aweme_id=11223344")
	id := extractAwemeID(u)
	if id != "11223344" {
		t.Fatalf("expected 11223344, got %s", id)
	}
}

func TestShortLinkNon3xxStatus(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	parser := newShortParser(ts)
	_, err := parser.Parse(ts.URL)
	if !errors.Is(err, ErrShortLinkResolution) {
		t.Fatalf("expected ErrShortLinkResolution for non-3xx status, got %v", err)
	}
}

func TestParseRelativeRedirect(t *testing.T) {
	const awemeID = "55667788"
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/short" {
			w.Header().Set("Location", "/video/"+awemeID)
			w.WriteHeader(http.StatusFound)
		} else {
			http.Redirect(w, r, "https://www.douyin.com"+r.URL.Path, http.StatusFound)
		}
	}))
	defer ts.Close()

	parser := newShortParser(ts)
	got, err := parser.Parse(ts.URL + "/short")
	if err != nil {
		t.Fatalf("Parse relative redirect returned error: %v", err)
	}
	if got != awemeID {
		t.Fatalf("expected %s, got %s", awemeID, got)
	}
}
