//go:build !windows && !linux && !darwin

package downloader

func commitNoReplace(source, destination string) (bool, error) {
	return linkNoReplace(source, destination)
}
