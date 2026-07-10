//go:build !windows

package downloader

import "os"

func atomicReplaceFile(source, destination string) error {
	return os.Rename(source, destination)
}
