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

// Image represents a single image in a Douyin album (photo collection).
// Albums contain multiple images that are downloaded as a ZIP archive.
type Image struct {
	URL    string // Direct download URL for the image
	Width  int    // Image width in pixels
	Height int    // Image height in pixels
}
