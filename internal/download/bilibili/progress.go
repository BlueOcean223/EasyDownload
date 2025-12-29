package bilibili

// DASHProgressTracker tracks combined download progress for DASH format videos.
// DASH videos have separate video and audio streams that are downloaded independently.
// This tracker combines both streams' progress into a unified percentage (0-100%).
//
// Progress allocation:
//   - 0-95%:   Download progress (video + audio combined by size ratio)
//   - 95-100%: FFmpeg merge operation
type DASHProgressTracker struct {
	videoSize       int64         // Expected video stream size in bytes
	audioSize       int64         // Expected audio stream size in bytes
	totalSize       int64         // Combined size (videoSize + audioSize)
	videoDownloaded int64         // Bytes downloaded for video stream
	audioDownloaded int64         // Bytes downloaded for audio stream
	onProgress      func(float64) // Callback to report progress percentage
	lastProgress    float64       // Last reported progress (ensures monotonic increase)
}

// NewDASHProgressTracker creates a new progress tracker for DASH format downloads.
// The tracker combines video and audio download progress based on their relative sizes.
func NewDASHProgressTracker(videoSize, audioSize int64, onProgress func(float64)) *DASHProgressTracker {
	return &DASHProgressTracker{
		videoSize:  videoSize,
		audioSize:  audioSize,
		totalSize:  videoSize + audioSize,
		onProgress: onProgress,
	}
}

// UpdateVideoProgress updates the video stream download progress.
// The progress parameter is a percentage (0-100) of the video stream completion.
func (t *DASHProgressTracker) UpdateVideoProgress(progress float64) {
	if t.videoSize > 0 {
		t.videoDownloaded = int64(progress / 100 * float64(t.videoSize))
	}
	t.reportProgress()
}

// UpdateAudioProgress updates the audio stream download progress.
// The progress parameter is a percentage (0-100) of the audio stream completion.
func (t *DASHProgressTracker) UpdateAudioProgress(progress float64) {
	if t.audioSize > 0 {
		t.audioDownloaded = int64(progress / 100 * float64(t.audioSize))
	}
	t.reportProgress()
}

// reportProgress calculates and reports the combined download progress.
// Progress is weighted by stream sizes and reserves 5% for the merge phase.
// Ensures progress only increases (never decreases) for smooth UI display.
func (t *DASHProgressTracker) reportProgress() {
	if t.onProgress == nil || t.totalSize == 0 {
		return
	}
	// Reserve 5% for merge operation
	downloadProgress := float64(t.videoDownloaded+t.audioDownloaded) / float64(t.totalSize) * 95
	// Ensure progress is monotonically increasing
	if downloadProgress > t.lastProgress {
		t.lastProgress = downloadProgress
		t.onProgress(downloadProgress)
	}
}

// SetMergeProgress sets progress during the FFmpeg merge phase.
// The progress parameter is 0-100% of the merge operation.
// This maps to 95-100% of the overall download progress.
func (t *DASHProgressTracker) SetMergeProgress(progress float64) {
	if t.onProgress == nil {
		return
	}
	mergeProgress := 95 + progress*0.05
	if mergeProgress > t.lastProgress {
		t.lastProgress = mergeProgress
		t.onProgress(mergeProgress)
	}
}
