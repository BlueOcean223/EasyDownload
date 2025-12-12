package api

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
)

// DetectedVideo represents a video detected by the proxy
type DetectedVideo struct {
	ID           string  `json:"id"`
	Title        string  `json:"title"`
	Cover        string  `json:"cover"`
	URL          string  `json:"url"`
	Source       string  `json:"source"`
	Quality      string  `json:"quality"`
	Duration     int     `json:"duration"`
	Author       string  `json:"author"`
	AuthorAvatar string  `json:"authorAvatar"` // Author avatar URL
	Timestamp    int64   `json:"timestamp"`
	DecodeKey    string  `json:"decodeKey"` // Decryption key for WeChat videos
	FileSize     float64 `json:"fileSize"`  // File size in bytes
	Width        int     `json:"width"`     // Video width in pixels
	Height       int     `json:"height"`    // Video height in pixels
	IsCurrent    bool    `json:"isCurrentVideo"`
}

// InternalAPI provides an HTTP API for receiving data from injected scripts
type InternalAPI struct {
	server *http.Server
	port   int
	mu     sync.RWMutex

	// Detected videos storage
	detectedVideos []DetectedVideo
	videosMu       sync.RWMutex

	// Callback for new videos
	onVideoDetected func(DetectedVideo)

	// Image proxy handler
	imageProxy *ImageProxyHandler
}

// NewInternalAPI creates a new internal API server
func NewInternalAPI(port int) *InternalAPI {
	return &InternalAPI{
		port:           port,
		detectedVideos: make([]DetectedVideo, 0),
		imageProxy:     NewImageProxyHandler(),
	}
}

// SetVideoCallback sets the callback for new detected videos
func (api *InternalAPI) SetVideoCallback(callback func(DetectedVideo)) {
	api.onVideoDetected = callback
}

// Start starts the internal API server
func (api *InternalAPI) Start() error {
	api.mu.Lock()
	defer api.mu.Unlock()

	mux := http.NewServeMux()

	// CORS middleware
	corsHandler := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

			if r.Method == "OPTIONS" {
				w.WriteHeader(http.StatusOK)
				return
			}

			h(w, r)
		}
	}

	// Routes
	mux.HandleFunc("/api/detect", corsHandler(api.handleDetect))
	mux.HandleFunc("/api/videos", corsHandler(api.handleGetVideos))
	mux.HandleFunc("/api/clear", corsHandler(api.handleClear))
	mux.HandleFunc("/api/health", corsHandler(api.handleHealth))
	mux.HandleFunc("/api/proxy-image", corsHandler(api.handleProxyImage))

	api.server = &http.Server{
		Addr:    fmt.Sprintf(":%d", api.port),
		Handler: mux,
	}

	go func() {
		log.Printf("Internal API server started on port %d", api.port)
		if err := api.server.ListenAndServe(); err != http.ErrServerClosed {
			log.Printf("Internal API server error: %v", err)
		}
	}()

	return nil
}

// Stop stops the internal API server
func (api *InternalAPI) Stop() error {
	api.mu.Lock()
	defer api.mu.Unlock()

	if api.server != nil {
		return api.server.Close()
	}
	return nil
}

// GetPort returns the API server port
func (api *InternalAPI) GetPort() int {
	return api.port
}

// handleDetect handles POST /api/detect - receives detected videos from injected script
func (api *InternalAPI) handleDetect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var video DetectedVideo
	if err := json.NewDecoder(r.Body).Decode(&video); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	// Add to storage
	api.videosMu.Lock()
	// Check for duplicates
	isDuplicate := false
	for _, v := range api.detectedVideos {
		if v.URL == video.URL {
			isDuplicate = true
			break
		}
	}
	if video.Source == "wechat" {
		// Ensure only ONE WeChat item is marked as current at any time.
		if video.IsCurrent {
			for i := range api.detectedVideos {
				if api.detectedVideos[i].Source == "wechat" {
					api.detectedVideos[i].IsCurrent = false
				}
			}
		}

		// Keep history but dedupe/update by stable ID (pageKey) to avoid spam
		updated := false
		if video.ID != "" {
			for i, v := range api.detectedVideos {
				if v.Source == "wechat" && v.ID == video.ID {
					api.detectedVideos[i] = video
					updated = true
					break
				}
			}
		}
		if !updated && !isDuplicate {
			api.detectedVideos = append(api.detectedVideos, video)
		}
		// Cap list size (avoid unbounded growth)
		if len(api.detectedVideos) > 80 {
			api.detectedVideos = api.detectedVideos[len(api.detectedVideos)-80:]
		}
		if api.onVideoDetected != nil {
			api.onVideoDetected(video)
		}
	} else if !isDuplicate {
		api.detectedVideos = append(api.detectedVideos, video)
		log.Printf("Detected new video: %s", video.Title)

		// Trigger callback
		if api.onVideoDetected != nil {
			api.onVideoDetected(video)
		}
	}
	api.videosMu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// handleGetVideos handles GET /api/videos - returns all detected videos
func (api *InternalAPI) handleGetVideos(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	api.videosMu.RLock()
	videos := api.detectedVideos
	api.videosMu.RUnlock()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(videos)
}

// handleClear handles POST /api/clear - clears all detected videos
func (api *InternalAPI) handleClear(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	api.videosMu.Lock()
	api.detectedVideos = make([]DetectedVideo, 0)
	api.videosMu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// handleHealth handles GET /api/health - health check
func (api *InternalAPI) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "healthy"})
}

// handleProxyImage handles GET /api/proxy-image - proxies external images
func (api *InternalAPI) handleProxyImage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	imageURL := r.URL.Query().Get("url")
	if imageURL == "" {
		http.Error(w, "Missing url parameter", http.StatusBadRequest)
		return
	}

	data, contentType, err := api.imageProxy.ProxyImage(imageURL)
	if err != nil {
		log.Printf("Image proxy error: %v", err)
		http.Error(w, "Failed to fetch image", http.StatusBadGateway)
		return
	}

	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "public, max-age=86400") // Cache for 24 hours
	w.Write(data)
}

// GetDetectedVideos returns all detected videos
func (api *InternalAPI) GetDetectedVideos() []DetectedVideo {
	api.videosMu.RLock()
	defer api.videosMu.RUnlock()

	videos := make([]DetectedVideo, len(api.detectedVideos))
	copy(videos, api.detectedVideos)
	return videos
}

// ClearVideos clears all detected videos
func (api *InternalAPI) ClearVideos() {
	api.videosMu.Lock()
	api.detectedVideos = make([]DetectedVideo, 0)
	api.videosMu.Unlock()
}

// RemoveVideo removes a video by ID
func (api *InternalAPI) RemoveVideo(id string) {
	api.videosMu.Lock()
	defer api.videosMu.Unlock()

	for i, v := range api.detectedVideos {
		if v.ID == id {
			api.detectedVideos = append(api.detectedVideos[:i], api.detectedVideos[i+1:]...)
			break
		}
	}
}
