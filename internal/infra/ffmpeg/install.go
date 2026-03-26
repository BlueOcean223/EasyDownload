package ffmpeg

import "fmt"

// EnsureAvailable returns a usable FFmpeg path, trying embedded and platform-specific
// installation flows when FFmpeg is not already present.
func (fm *FFmpegManager) EnsureAvailable() (string, error) {
	if path := fm.GetPath(); path != "" {
		return path, nil
	}

	if embeddedFFmpeg != nil && fm.extractDir != "" {
		if err := fm.ExtractEmbedded(); err == nil {
			if path := fm.GetPath(); path != "" {
				return path, nil
			}
		}
	}

	path, err := fm.installPlatformFFmpeg()
	if err != nil {
		return "", err
	}
	if path == "" {
		return "", fmt.Errorf("ffmpeg installation did not produce an executable")
	}
	return path, nil
}
