package config

import (
	"EasyDownload/internal/infra/logger"
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// Persister is the durable boundary used by ConfigManager. Implementations
// must leave the previous target intact whenever they return an error before
// the replace operation succeeds.
type Persister interface {
	Persist(ctx context.Context, path string, data []byte, perm fs.FileMode) error
}

type atomicWriteStage string

const (
	atomicWriteStageWrite   atomicWriteStage = "write"
	atomicWriteStageSync    atomicWriteStage = "sync"
	atomicWriteStageReplace atomicWriteStage = "replace"
)

type atomicFilePersister struct {
	beforeStage func(atomicWriteStage) error
	syncParent  func(string) error
}

func newAtomicFilePersister() *atomicFilePersister {
	return &atomicFilePersister{}
}

func (p *atomicFilePersister) Persist(ctx context.Context, path string, data []byte, perm fs.FileMode) (resultErr error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}

	temp, err := os.CreateTemp(dir, ".easydownload-config-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary config: %w", err)
	}
	tempPath := temp.Name()
	closed := false
	defer func() {
		if !closed {
			_ = temp.Close()
		}
		if resultErr != nil {
			_ = os.Remove(tempPath)
		}
	}()

	if err := temp.Chmod(perm); err != nil {
		return fmt.Errorf("set temporary config permissions: %w", err)
	}
	if err := p.runHook(atomicWriteStageWrite); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := writeAll(temp, data); err != nil {
		return fmt.Errorf("write temporary config: %w", err)
	}

	if err := p.runHook(atomicWriteStageSync); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := temp.Sync(); err != nil {
		return fmt.Errorf("sync temporary config: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close temporary config: %w", err)
	}
	closed = true

	if err := p.runHook(atomicWriteStageReplace); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := replaceFile(tempPath, path); err != nil {
		return fmt.Errorf("replace config: %w", err)
	}
	syncParent := syncParentDirectory
	if p != nil && p.syncParent != nil {
		syncParent = p.syncParent
	}
	if err := syncParent(dir); err != nil {
		// Atomic replace is the durable commit boundary for this repository.
		// Returning an error now would keep ConfigManager's old in-memory value
		// even though the visible target already contains the candidate. Keep
		// disk and memory converged and report the extra crash-durability flush
		// as a best-effort diagnostic instead.
		logger.Warn("Config replaced but parent directory sync failed: %v", err)
	}
	return nil
}

func (p *atomicFilePersister) runHook(stage atomicWriteStage) error {
	if p != nil && p.beforeStage != nil {
		if err := p.beforeStage(stage); err != nil {
			return fmt.Errorf("%s temporary config: %w", stage, err)
		}
	}
	return nil
}

func writeAll(file *os.File, data []byte) error {
	for len(data) > 0 {
		n, err := file.Write(data)
		if err != nil {
			return err
		}
		if n == 0 {
			return fmt.Errorf("short write")
		}
		data = data[n:]
	}
	return nil
}
