package fetch

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const resumeStateVersion = 1

type resumeState struct {
	Version        int    `json:"version"`
	SelectedURL    string `json:"selectedUrl"`
	ETag           string `json:"etag,omitempty"`
	LastModified   string `json:"lastModified,omitempty"`
	Total          int64  `json:"total,omitempty"`
	TotalKnown     bool   `json:"totalKnown,omitempty"`
	ExpectedSHA256 string `json:"expectedSha256,omitempty"`
}

type sidecarStore interface {
	Load(path string) (resumeState, error)
	Save(path string, state resumeState) error
	Remove(path string) error
}

type sidecarSaveStage string

const (
	sidecarStageCreate  sidecarSaveStage = "create"
	sidecarStageWrite   sidecarSaveStage = "write"
	sidecarStageSync    sidecarSaveStage = "sync"
	sidecarStageReplace sidecarSaveStage = "replace"
	sidecarStageDirSync sidecarSaveStage = "directory_sync"
)

// fileSidecarStore keeps the fault boundary deliberately narrower than the
// filesystem API. Production leaves beforeSaveStage nil; tests use it to
// prove that every crash-sensitive step preserves the previous complete
// sidecar instead of exposing a partially-written identity record.
type fileSidecarStore struct {
	beforeSaveStage func(sidecarSaveStage) error
}

func (fileSidecarStore) Load(path string) (resumeState, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return resumeState{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var state resumeState
	if err := decoder.Decode(&state); err != nil {
		return resumeState{}, fmt.Errorf("decode resume sidecar: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return resumeState{}, errors.New("decode resume sidecar: trailing JSON value")
		}
		return resumeState{}, fmt.Errorf("decode resume sidecar trailing data: %w", err)
	}
	if state.Version != resumeStateVersion {
		return resumeState{}, fmt.Errorf("unsupported resume sidecar version %d", state.Version)
	}
	state.SelectedURL = strings.TrimSpace(state.SelectedURL)
	state.ETag = normalizeStrongETag(state.ETag)
	state.LastModified = strings.TrimSpace(state.LastModified)
	state.ExpectedSHA256 = normalizeSHA256(state.ExpectedSHA256)
	if state.SelectedURL == "" {
		return resumeState{}, errors.New("resume sidecar selected URL is empty")
	}
	if state.Total < 0 {
		return resumeState{}, errors.New("resume sidecar total is negative")
	}
	return state, nil
}

func (store fileSidecarStore) Save(path string, state resumeState) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("resume sidecar path is empty")
	}
	state.Version = resumeStateVersion
	data, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("encode resume sidecar: %w", err)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create resume sidecar directory: %w", err)
	}
	if err := store.runBeforeSaveStage(sidecarStageCreate); err != nil {
		return fmt.Errorf("create resume sidecar temp file: %w", err)
	}
	file, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create resume sidecar temp file: %w", err)
	}
	tempPath := file.Name()
	removeTemp := true
	defer func() {
		_ = file.Close()
		if removeTemp {
			_ = os.Remove(tempPath)
		}
	}()
	if err := file.Chmod(0o600); err != nil {
		return fmt.Errorf("chmod resume sidecar temp file: %w", err)
	}
	if err := store.runBeforeSaveStage(sidecarStageWrite); err != nil {
		return fmt.Errorf("write resume sidecar temp file: %w", err)
	}
	if _, err := file.Write(data); err != nil {
		return fmt.Errorf("write resume sidecar temp file: %w", err)
	}
	if err := store.runBeforeSaveStage(sidecarStageSync); err != nil {
		return fmt.Errorf("sync resume sidecar temp file: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync resume sidecar temp file: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close resume sidecar temp file: %w", err)
	}
	if err := store.runBeforeSaveStage(sidecarStageReplace); err != nil {
		return fmt.Errorf("replace resume sidecar: %w", err)
	}
	if err := atomicReplace(tempPath, path); err != nil {
		return fmt.Errorf("replace resume sidecar: %w", err)
	}
	removeTemp = false
	// atomicReplace is the commit/linearization boundary. Reporting an error
	// after it would tell the caller that the old identity is still active even
	// though readers already observe the new one. Directory sync remains a
	// best-effort durability strengthening step; a failure cannot roll back the
	// committed replace and therefore must not be surfaced as an uncommitted
	// Save failure.
	if err := store.runBeforeSaveStage(sidecarStageDirSync); err == nil {
		_ = syncParentDir(dir)
	}
	return nil
}

func (store fileSidecarStore) runBeforeSaveStage(stage sidecarSaveStage) error {
	if store.beforeSaveStage == nil {
		return nil
	}
	return store.beforeSaveStage(stage)
}

func (fileSidecarStore) Remove(path string) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return syncParentDir(filepath.Dir(path))
}
