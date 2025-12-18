package douyin

type DouyinItem struct {
	Type     string
	ID       string
	Title    string
	Cover    string
	Author   string
	AuthorID string
	Duration int
	Streams  []Stream
	Images   []Image
}

type Stream struct {
	QualityKey  string
	QualityName string
	Width       int
	Height      int
	Bitrate     int
	URL         string
	Size        int64 // File size in bytes (estimated via HEAD request)
}

type Image struct {
	URL    string
	Width  int
	Height int
}
