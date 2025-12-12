package api

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ImageProxyHandler handles image proxy requests
type ImageProxyHandler struct {
	client *http.Client
}

// NewImageProxyHandler creates a new image proxy handler
func NewImageProxyHandler() *ImageProxyHandler {
	return &ImageProxyHandler{
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// ProxyImage proxies an external image request and returns the image data
// Returns: image data, content type, error
func (iph *ImageProxyHandler) ProxyImage(imageURL string) ([]byte, string, error) {
	if imageURL == "" {
		return nil, "", fmt.Errorf("image URL is empty")
	}

	req, err := http.NewRequest(http.MethodGet, imageURL, nil)
	if err != nil {
		return nil, "", fmt.Errorf("failed to create request: %w", err)
	}

	// Set headers based on the image source
	iph.SetRequestHeaders(req, imageURL)

	resp, err := iph.client.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("failed to fetch image: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("image request failed with status: %d", resp.StatusCode)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", fmt.Errorf("failed to read image data: %w", err)
	}

	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "image/jpeg" // Default content type
	}

	return data, contentType, nil
}

// SetRequestHeaders sets appropriate headers for the image request based on the URL
func (iph *ImageProxyHandler) SetRequestHeaders(req *http.Request, imageURL string) {
	// Set common headers
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "image/avif,image/webp,image/apng,image/svg+xml,image/*,*/*;q=0.8")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")

	// Set Bilibili-specific headers
	if isBilibiliURL(imageURL) {
		iph.SetBilibiliHeaders(req)
	}
}

// SetBilibiliHeaders sets Bilibili-specific headers for image requests
func (iph *ImageProxyHandler) SetBilibiliHeaders(req *http.Request) {
	req.Header.Set("Referer", "https://www.bilibili.com/")
	req.Header.Set("Origin", "https://www.bilibili.com")
}

// isBilibiliURL checks if the URL is a Bilibili image URL
func isBilibiliURL(url string) bool {
	bilibiliDomains := []string{
		"hdslb.com",
		"bilibili.com",
		"bilivideo.com",
		"biliimg.com",
	}

	for _, domain := range bilibiliDomains {
		if strings.Contains(url, domain) {
			return true
		}
	}
	return false
}

// IsBilibiliURL is exported for testing purposes
func IsBilibiliURL(url string) bool {
	return isBilibiliURL(url)
}
