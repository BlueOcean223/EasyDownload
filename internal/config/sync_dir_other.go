//go:build !windows

package config

import "os"

// syncParentDirectory makes the rename durable across a host crash. The
// temporary file itself is synced before replace; POSIX additionally requires
// syncing the containing directory for the directory entry update.
func syncParentDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	if err := directory.Sync(); err != nil {
		_ = directory.Close()
		return err
	}
	return directory.Close()
}
