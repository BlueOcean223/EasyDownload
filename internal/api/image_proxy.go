package api

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const maxProxyImageSize = 20 * 1024 * 1024

// ImageProxyHandler handles image proxy requests
type ImageProxyHandler struct {
	client               *http.Client
	allowPrivateNetworks bool
}

// NewImageProxyHandler creates a new image proxy handler
func NewImageProxyHandler() *ImageProxyHandler {
	iph := &ImageProxyHandler{}
	iph.client = &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			DialContext: iph.safeDialContext,
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return fmt.Errorf("too many redirects")
			}
			return iph.validateImageURL(req.URL.String())
		},
	}
	return iph
}

// ProxyImage proxies an external image request and returns the image data
// Returns: image data, content type, error
func (iph *ImageProxyHandler) ProxyImage(imageURL string) ([]byte, string, error) {
	if imageURL == "" {
		return nil, "", fmt.Errorf("image URL is empty")
	}
	if err := iph.validateImageURL(imageURL); err != nil {
		return nil, "", err
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
	if resp.ContentLength > maxProxyImageSize {
		return nil, "", fmt.Errorf("image too large")
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxProxyImageSize+1))
	if err != nil {
		return nil, "", fmt.Errorf("failed to read image data: %w", err)
	}
	if len(data) > maxProxyImageSize {
		return nil, "", fmt.Errorf("image too large")
	}

	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "image/jpeg" // Default content type
	}
	mediaType := strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0]))
	if mediaType != "" && !strings.HasPrefix(mediaType, "image/") {
		return nil, "", fmt.Errorf("proxied resource is not an image")
	}

	return data, contentType, nil
}

func (iph *ImageProxyHandler) validateImageURL(raw string) error {
	if !isHTTPURL(raw) {
		return fmt.Errorf("unsupported image URL")
	}
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return fmt.Errorf("invalid image URL: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := iph.lookupSafeIPs(ctx, u.Hostname()); err != nil {
		return err
	}
	return nil
}

func (iph *ImageProxyHandler) safeDialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	ips, err := iph.lookupSafeIPs(ctx, host)
	if err != nil {
		return nil, err
	}

	dialer := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
	var firstErr error
	for _, ip := range ips {
		conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		if err == nil {
			return conn, nil
		}
		if firstErr == nil {
			firstErr = err
		}
	}
	if firstErr != nil {
		return nil, firstErr
	}
	return nil, fmt.Errorf("no safe IP address found for %s", host)
}

func (iph *ImageProxyHandler) lookupSafeIPs(ctx context.Context, host string) ([]net.IP, error) {
	host = strings.TrimSpace(strings.TrimSuffix(host, "."))
	if host == "" {
		return nil, fmt.Errorf("empty host")
	}

	var ips []net.IP
	if ip := net.ParseIP(host); ip != nil {
		ips = []net.IP{ip}
	} else {
		addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve image host: %w", err)
		}
		ips = make([]net.IP, 0, len(addrs))
		for _, addr := range addrs {
			ips = append(ips, addr.IP)
		}
	}

	if len(ips) == 0 {
		return nil, fmt.Errorf("image host resolved to no addresses")
	}

	safe := make([]net.IP, 0, len(ips))
	for _, ip := range ips {
		if iph.allowPrivateNetworks || !isUnsafeProxyIP(ip) {
			safe = append(safe, ip)
			continue
		}
		return nil, fmt.Errorf("image host resolves to blocked address: %s", ip.String())
	}
	if len(safe) == 0 {
		return nil, fmt.Errorf("image host has no safe addresses")
	}
	return safe, nil
}

func isUnsafeProxyIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsInterfaceLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return true
	}
	if v4 := ip.To4(); v4 != nil {
		// Explicit IPv4 checks also cover IPv4-mapped IPv6 addresses.
		if v4[0] == 10 || v4[0] == 127 || v4[0] == 0 || v4[0] >= 224 {
			return true
		}
		if v4[0] == 172 && v4[1] >= 16 && v4[1] <= 31 {
			return true
		}
		if v4[0] == 192 && v4[1] == 168 {
			return true
		}
		if v4[0] == 169 && v4[1] == 254 {
			return true
		}
		// Carrier-grade NAT (100.64.0.0/10) should not be reachable through the proxy.
		if v4[0] == 100 && v4[1]&0xc0 == 64 {
			return true
		}
	}
	return false
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
func isBilibiliURL(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return false
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	bilibiliDomains := []string{
		"hdslb.com",
		"bilibili.com",
		"bilivideo.com",
		"biliimg.com",
	}

	for _, domain := range bilibiliDomains {
		if host == domain || strings.HasSuffix(host, "."+domain) {
			return true
		}
	}
	return false
}

func isHTTPURL(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return false
	}
	scheme := strings.ToLower(u.Scheme)
	return (scheme == "http" || scheme == "https") && u.Hostname() != ""
}

// IsBilibiliURL is exported for testing purposes
func IsBilibiliURL(url string) bool {
	return isBilibiliURL(url)
}
