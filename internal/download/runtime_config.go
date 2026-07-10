package downloader

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

type RuntimeConfigPatch struct {
	DownloadDir   *string
	MaxConcurrent *int
}

type RuntimeConfigSnapshot struct {
	DownloadDir   string `json:"downloadDir"`
	MaxConcurrent int    `json:"maxConcurrent"`
}

// RuntimeConfigUpdate holds DownloadManager's config write lock from candidate
// apply through Settings' critical commit. Concurrent task creation/start paths
// cannot observe a candidate that is later rolled back.
type RuntimeConfigUpdate struct {
	dm        *DownloadManager
	old       RuntimeConfigSnapshot
	candidate RuntimeConfigSnapshot
	mu        sync.Mutex
	closed    bool
}

func (dm *DownloadManager) BeginRuntimeConfigUpdate(patch RuntimeConfigPatch) (*RuntimeConfigUpdate, error) {
	if dm == nil {
		return nil, errors.New("download manager is nil")
	}
	var preparedDirectory *string
	if patch.DownloadDir != nil {
		directory := strings.TrimSpace(*patch.DownloadDir)
		if directory == "" {
			return nil, errors.New("download directory is required")
		}
		absolute, err := filepath.Abs(filepath.Clean(directory))
		if err != nil {
			return nil, err
		}
		if err := os.MkdirAll(absolute, 0755); err != nil {
			return nil, err
		}
		info, err := os.Stat(absolute)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() {
			return nil, errors.New("download directory is not a directory")
		}
		preparedDirectory = &absolute
	}
	if patch.MaxConcurrent != nil && *patch.MaxConcurrent <= 0 {
		return nil, errors.New("max concurrent downloads must be positive")
	}

	dm.configMu.Lock()
	old := RuntimeConfigSnapshot{DownloadDir: dm.downloadDir, MaxConcurrent: dm.maxConcurrent}
	candidate := old
	if preparedDirectory != nil {
		candidate.DownloadDir = *preparedDirectory
	}
	if patch.MaxConcurrent != nil {
		candidate.MaxConcurrent = *patch.MaxConcurrent
	}
	dm.downloadDir = candidate.DownloadDir
	dm.maxConcurrent = candidate.MaxConcurrent
	return &RuntimeConfigUpdate{dm: dm, old: old, candidate: candidate}, nil
}

func (update *RuntimeConfigUpdate) Commit() error {
	if update == nil || update.dm == nil {
		return errors.New("runtime config update is nil")
	}
	update.mu.Lock()
	defer update.mu.Unlock()
	if update.closed {
		return errors.New("runtime config update is already closed")
	}
	update.closed = true
	shouldDispatch := update.candidate.MaxConcurrent > update.old.MaxConcurrent
	update.dm.configMu.Unlock()
	if shouldDispatch {
		update.dm.dispatchQueued()
	}
	return nil
}

func (update *RuntimeConfigUpdate) Rollback() error {
	if update == nil || update.dm == nil {
		return errors.New("runtime config update is nil")
	}
	update.mu.Lock()
	defer update.mu.Unlock()
	if update.closed {
		return errors.New("runtime config update is already closed")
	}
	update.dm.downloadDir = update.old.DownloadDir
	update.dm.maxConcurrent = update.old.MaxConcurrent
	update.closed = true
	update.dm.configMu.Unlock()
	return nil
}

func (dm *DownloadManager) RuntimeConfig() RuntimeConfigSnapshot {
	if dm == nil {
		return RuntimeConfigSnapshot{}
	}
	dm.configMu.RLock()
	defer dm.configMu.RUnlock()
	return RuntimeConfigSnapshot{DownloadDir: dm.downloadDir, MaxConcurrent: dm.maxConcurrent}
}
