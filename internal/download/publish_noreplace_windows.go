//go:build windows

package downloader

import (
	"errors"
	"fmt"

	"golang.org/x/sys/windows"
)

func commitNoReplace(source, destination string) (bool, error) {
	sourcePtr, err := windows.UTF16PtrFromString(source)
	if err != nil {
		return false, err
	}
	destinationPtr, err := windows.UTF16PtrFromString(destination)
	if err != nil {
		return false, err
	}
	err = windows.MoveFileEx(sourcePtr, destinationPtr, windows.MOVEFILE_WRITE_THROUGH)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, windows.ERROR_ALREADY_EXISTS) || errors.Is(err, windows.ERROR_FILE_EXISTS) {
		return false, fmt.Errorf("%w: %s", ErrOutputExists, destination)
	}
	return false, fmt.Errorf("publish final file: %w", err)
}
