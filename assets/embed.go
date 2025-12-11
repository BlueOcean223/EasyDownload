package assets

import (
	"embed"
)

// FFmpegFS holds the embedded FFmpeg binary files
// The embed directive will include any ffmpeg/ffmpeg.exe or ffmpeg/ffmpeg files if present
//
//go:embed ffmpeg/*
var FFmpegFS embed.FS

// HasEmbeddedFFmpeg checks if FFmpeg binary is embedded
func HasEmbeddedFFmpeg() bool {
	// Try to read the Windows version
	if _, err := FFmpegFS.ReadFile("ffmpeg/ffmpeg.exe"); err == nil {
		return true
	}
	// Try to read the Unix version
	if _, err := FFmpegFS.ReadFile("ffmpeg/ffmpeg"); err == nil {
		return true
	}
	return false
}
