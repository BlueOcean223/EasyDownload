package douyin

// DouyinItem represents a parsed Douyin video or album with all relevant metadata.
// This is the primary data structure returned by the Client after fetching item info.
type DouyinItem struct {
	Type     string   // Content type: "video" or "album"
	ID       string   // Unique aweme_id identifying the content
	Title    string   // Video description or album title
	Cover    string   // URL to the cover/thumbnail image
	Author   string   // Content creator's display name
	AuthorID string   // Content creator's user ID
	Duration int      // Video duration in seconds (0 for albums)
	Streams  []Stream // Available video quality streams (empty for albums)
	Images   []Image  // Album images (empty for videos)
}

// Stream represents a single video quality option for download.
// Videos typically have multiple streams at different resolutions/bitrates.
type Stream struct {
	QualityKey  string // Quality identifier (e.g., "720p", "1080p", "source")
	QualityName string // Display name for the quality level
	Width       int    // Video width in pixels
	Height      int    // Video height in pixels
	Bitrate     int    // Video bitrate in bits per second
	URL         string // Direct download URL for this quality
	Size        int64  // File size in bytes (estimated via HEAD request, may be 0)
}

// Image represents a single media item in a Douyin album.
// For mixed content (aweme_type 68), items can be either images or videos.
// If VideoURL is non-empty, the item is a video; otherwise it's an image.
// Albums are downloaded as a ZIP archive containing both images and videos.
type Image struct {
	URL      string // Direct download URL for the image (empty for video-only items)
	VideoURL string // Direct download URL for the video (empty for image items)
	Width    int    // Media width in pixels
	Height   int    // Media height in pixels
}
