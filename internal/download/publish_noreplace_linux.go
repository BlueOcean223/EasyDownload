//go:build linux

package downloader

import (
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
)

func commitNoReplace(source, destination string) (bool, error) {
	err := unix.Renameat2(unix.AT_FDCWD, source, unix.AT_FDCWD, destination, unix.RENAME_NOREPLACE)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, unix.EEXIST) {
		return false, fmt.Errorf("%w: %s", ErrOutputExists, destination)
	}
	if errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EINVAL) || errors.Is(err, unix.EOPNOTSUPP) {
		return linkNoReplace(source, destination)
	}
	return false, fmt.Errorf("publish final file: %w", err)
}
