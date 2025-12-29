//go:build !windows

package utils

import (
	"errors"
	"os"
)

// IsAdmin checks if the current process has administrator/root privileges
func IsAdmin() bool {
	return os.Geteuid() == 0
}

// RestartAsAdmin is not supported on non-Windows platforms
func RestartAsAdmin() error {
	return errors.New("restart as admin is only supported on Windows")
}

// CanRestartAsAdmin returns false on non-Windows platforms.
func CanRestartAsAdmin() bool {
	return false
}
