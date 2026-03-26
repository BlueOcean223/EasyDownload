//go:build !darwin

package ffmpeg

import "fmt"

func (fm *FFmpegManager) installPlatformFFmpeg() (string, error) {
	return "", fmt.Errorf("automatic ffmpeg installation is only implemented for macOS")
}
