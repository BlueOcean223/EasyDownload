//go:build darwin

package downloader

import (
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
)

func commitNoReplace(source, destination string) (bool, error) {
	err := unix.RenamexNp(source, destination, unix.RENAME_EXCL)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, unix.EEXIST) {
		return false, fmt.Errorf("%w: %s", ErrOutputExists, destination)
	}
	if errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EINVAL) || errors.Is(err, unix.ENOTSUP) {
		return linkNoReplace(source, destination)
	}
	return false, fmt.Errorf("publish final file: %w", err)
}
