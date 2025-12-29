//go:build !windows

package ffmpeg

import "os/exec"

func applyNoWindow(cmd *exec.Cmd) {
	// no-op on non-Windows platforms
}
