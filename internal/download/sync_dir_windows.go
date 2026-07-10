//go:build windows

package downloader

// Windows does not provide a generally usable directory fsync equivalent.
// atomicReplaceFile uses MOVEFILE_WRITE_THROUGH, which waits for the move to be
// flushed before returning, so no additional directory handle flush is needed.
func syncParentDirectory(string) error { return nil }
