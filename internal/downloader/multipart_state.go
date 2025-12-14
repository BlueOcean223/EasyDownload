package downloader

import (
	"encoding/json"
	"os"
)

type multipartState struct {
	Version   int                   `json:"version"`
	TotalSize int64                 `json:"totalSize"`
	Threads   int                   `json:"threads"`
	Chunks    []multipartStateChunk `json:"chunks"`
}

type multipartStateChunk struct {
	Index      int   `json:"index"`
	Start      int64 `json:"start"`
	End        int64 `json:"end"`
	Downloaded int64 `json:"downloaded"`
	Done       bool  `json:"done"`
}

func multipartStatePath(outputPath string) string {
	return outputPath + ".edstate.json"
}

func multipartStateExists(outputPath string) bool {
	_, err := os.Stat(multipartStatePath(outputPath))
	return err == nil
}

func loadMultipartState(outputPath string) (*multipartState, error) {
	b, err := os.ReadFile(multipartStatePath(outputPath))
	if err != nil {
		return nil, err
	}
	var st multipartState
	if err := json.Unmarshal(b, &st); err != nil {
		return nil, err
	}
	return &st, nil
}

func saveMultipartState(outputPath string, st *multipartState) error {
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	tmp := multipartStatePath(outputPath) + ".tmp"
	if err := os.WriteFile(tmp, b, 0644); err != nil {
		return err
	}
	_ = os.Remove(multipartStatePath(outputPath))
	return os.Rename(tmp, multipartStatePath(outputPath))
}
