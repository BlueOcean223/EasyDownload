package utils

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const albumStateFileName = "state.json"

type AlbumState struct {
	Total     int    `json:"total"`
	Completed []int  `json:"completed"`
	DestPath  string `json:"destPath"`
	TempDir   string `json:"tempDir"`
	Indices   []int  `json:"indices,omitempty"`
	UpdatedAt int64  `json:"updatedAt"`
}

func AlbumTempDir(destPath string) string {
	return destPath + ".albumtmp"
}

func AlbumImageTempPath(tempDir string, index int) string {
	return filepath.Join(tempDir, fmt.Sprintf("%d.img", index))
}

func AlbumStatePath(tempDir string) string {
	return filepath.Join(tempDir, albumStateFileName)
}

func LoadAlbumState(tempDir string) (*AlbumState, error) {
	statePath := AlbumStatePath(tempDir)
	data, err := os.ReadFile(statePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var state AlbumState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, err
	}
	return &state, nil
}

func SaveAlbumState(state *AlbumState) error {
	if state == nil {
		return fmt.Errorf("nil album state")
	}
	if state.TempDir == "" {
		return fmt.Errorf("empty temp dir")
	}
	if err := os.MkdirAll(state.TempDir, 0700); err != nil {
		return err
	}

	state.UpdatedAt = time.Now().Unix()
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}

	tmpPath := AlbumStatePath(state.TempDir) + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0600); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, AlbumStatePath(state.TempDir)); err != nil {
		_ = os.Remove(tmpPath) // cleanup temp file on rename failure
		return err
	}
	return nil
}
