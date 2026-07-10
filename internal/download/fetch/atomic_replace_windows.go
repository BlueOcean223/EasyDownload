//go:build windows

package fetch

import (
	"golang.org/x/sys/windows"
)

func atomicReplace(source, target string) error {
	sourcePtr, err := windows.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	targetPtr, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(
		sourcePtr,
		targetPtr,
		windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH,
	)
}

func syncParentDir(string) error {
	// MoveFileEx with MOVEFILE_WRITE_THROUGH is the durability boundary on
	// Windows. Opening directory handles portably would require broader ACLs.
	return nil
}
