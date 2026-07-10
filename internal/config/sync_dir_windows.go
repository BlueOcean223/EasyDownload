//go:build windows

package config

// replaceFile uses MoveFileEx with MOVEFILE_WRITE_THROUGH on Windows, which is
// the platform durability boundary; directory handles cannot be synced with
// os.File.Sync in the same portable way.
func syncParentDirectory(string) error { return nil }
