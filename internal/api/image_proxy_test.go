package api

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

func TestImageProxyRejectsUnsupportedScheme(t *testing.T) {
	handler := NewImageProxyHandler()

	_, _, err := handler.ProxyImage("file:///etc/passwd")
	if err == nil {
		t.Fatal("expected unsupported scheme error")
	}
}

func TestImageProxyRejectsOversizedImage(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(maxProxyImageSize+1))
	}))
	defer ts.Close()

	handler := NewImageProxyHandler()
	handler.allowPrivateNetworks = true
	_, _, err := handler.ProxyImage(ts.URL)
	if err == nil {
		t.Fatal("expected oversized image error")
	}
}

func TestImageProxyRejectsLocalhostSSRF(t *testing.T) {
	handler := NewImageProxyHandler()
	_, _, err := handler.ProxyImage("http://127.0.0.1:8080/image.jpg")
	if err == nil {
		t.Fatal("expected localhost SSRF protection error")
	}
}

func TestImageProxyBilibiliDomainValidation(t *testing.T) {
	if !IsBilibiliURL("https://i0.hdslb.com/image.jpg") {
		t.Fatal("expected real Bilibili image host to be detected")
	}
	if IsBilibiliURL("https://hdslb.com.evil.example/image.jpg") {
		t.Fatal("lookalike Bilibili host should not be detected")
	}
}
