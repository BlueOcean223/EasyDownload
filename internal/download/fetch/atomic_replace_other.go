//go:build !windows

package fetch

import "os"

func atomicReplace(source, target string) error {
	return os.Rename(source, target)
}

func syncParentDir(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
