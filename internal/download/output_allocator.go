package downloader

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	downloadtask "EasyDownload/internal/download/task"
)

var (
	ErrOutputReserved = errors.New("output path is reserved")
	ErrOutputExists   = errors.New("output path already exists")
)

// OutputPathAllocator owns final-path conflict handling for every task managed
// by a DownloadManager. Platform adapters never allocate or rename final files.
type OutputPathAllocator struct {
	mu           sync.Mutex
	reservations map[string]string // canonical path -> task id
	byTask       map[string]string // task id -> canonical path
}

func NewOutputPathAllocator() *OutputPathAllocator {
	return &OutputPathAllocator{
		reservations: make(map[string]string),
		byTask:       make(map[string]string),
	}
}

func (a *OutputPathAllocator) Reserve(taskID, directory, filename string, strategy downloadtask.ConflictStrategy) (downloadtask.OutputPolicy, error) {
	if a == nil {
		return downloadtask.OutputPolicy{}, errors.New("output path allocator is nil")
	}
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return downloadtask.OutputPolicy{}, errors.New("task id is required for output reservation")
	}
	directory, err := prepareOutputDirectory(directory)
	if err != nil {
		return downloadtask.OutputPolicy{}, err
	}
	filename = filepath.Base(strings.TrimSpace(filename))
	if filename == "." || filename == "" {
		return downloadtask.OutputPolicy{}, errors.New("output filename is required")
	}
	if strategy == "" {
		strategy = downloadtask.ConflictStrategyAutoRename
	}
	if strategy != downloadtask.ConflictStrategyAutoRename {
		return downloadtask.OutputPolicy{}, fmt.Errorf("unsupported conflict strategy %q", strategy)
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	return a.reserveLocked(taskID, directory, filename, strategy)
}

func (a *OutputPathAllocator) Restore(taskID string, policy downloadtask.OutputPolicy) error {
	if a == nil {
		return errors.New("output path allocator is nil")
	}
	taskID = strings.TrimSpace(taskID)
	if taskID == "" || strings.TrimSpace(policy.PlannedFinalPath) == "" {
		return errors.New("task id and planned final path are required")
	}
	key, path, err := canonicalOutputPath(policy.PlannedFinalPath)
	if err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if owner, ok := a.reservations[key]; ok && owner != taskID {
		return fmt.Errorf("%w: %s owned by %s", ErrOutputReserved, path, owner)
	}
	if oldKey, ok := a.byTask[taskID]; ok && oldKey != key {
		delete(a.reservations, oldKey)
	}
	a.reservations[key] = taskID
	a.byTask[taskID] = key
	return nil
}

func (a *OutputPathAllocator) Reallocate(taskID string, policy downloadtask.OutputPolicy) (downloadtask.OutputPolicy, error) {
	if a == nil {
		return downloadtask.OutputPolicy{}, errors.New("output path allocator is nil")
	}
	directory := policy.Directory
	filename := policy.PlannedFilename
	if directory == "" || filename == "" {
		directory = filepath.Dir(policy.PlannedFinalPath)
		filename = filepath.Base(policy.PlannedFinalPath)
	}
	directory, err := prepareOutputDirectory(directory)
	if err != nil {
		return downloadtask.OutputPolicy{}, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	// reserveLocked does not replace the old reservation until it has found and
	// validated a new candidate. Any filesystem error therefore leaves the old
	// reservation intact.
	return a.reserveLocked(taskID, directory, filename, downloadtask.ConflictStrategyAutoRename)
}

func prepareOutputDirectory(directory string) (string, error) {
	directory, err := filepath.Abs(strings.TrimSpace(directory))
	if err != nil || strings.TrimSpace(directory) == "" {
		return "", fmt.Errorf("invalid output directory: %w", err)
	}
	if err := os.MkdirAll(directory, 0755); err != nil {
		return "", fmt.Errorf("create output directory: %w", err)
	}
	info, err := os.Stat(directory)
	if err != nil {
		return "", fmt.Errorf("inspect output directory: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("output directory is not a directory: %s", directory)
	}
	return directory, nil
}

func (a *OutputPathAllocator) Release(taskID string) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if key, ok := a.byTask[taskID]; ok {
		if a.reservations[key] == taskID {
			delete(a.reservations, key)
		}
		delete(a.byTask, taskID)
	}
}

func (a *OutputPathAllocator) Owner(path string) (string, bool) {
	if a == nil {
		return "", false
	}
	key, _, err := canonicalOutputPath(path)
	if err != nil {
		return "", false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	owner, ok := a.reservations[key]
	return owner, ok
}

func (a *OutputPathAllocator) reserveLocked(taskID, directory, filename string, strategy downloadtask.ConflictStrategy) (downloadtask.OutputPolicy, error) {
	directory, err := filepath.Abs(filepath.Clean(directory))
	if err != nil {
		return downloadtask.OutputPolicy{}, err
	}
	base, ext := splitFilename(filename)
	for index := 0; ; index++ {
		candidate := filename
		if index > 0 {
			candidate = fmt.Sprintf("%s (%d)%s", base, index, ext)
		}
		path := filepath.Join(directory, candidate)
		key, absolute, err := canonicalOutputPath(path)
		if err != nil {
			return downloadtask.OutputPolicy{}, err
		}
		if owner, ok := a.reservations[key]; ok && owner != taskID {
			continue
		}
		if _, err := os.Lstat(absolute); err == nil {
			continue
		} else if !os.IsNotExist(err) {
			return downloadtask.OutputPolicy{}, err
		}
		if oldKey, ok := a.byTask[taskID]; ok && oldKey != key {
			delete(a.reservations, oldKey)
		}
		a.reservations[key] = taskID
		a.byTask[taskID] = key
		return downloadtask.OutputPolicy{
			Directory:        directory,
			PlannedFilename:  candidate,
			PlannedFinalPath: absolute,
			ReservationKey:   key,
			ConflictStrategy: strategy,
		}, nil
	}
}

func canonicalOutputPath(path string) (key string, absolute string, err error) {
	absolute, err = filepath.Abs(filepath.Clean(strings.TrimSpace(path)))
	if err != nil {
		return "", "", err
	}
	key = absolute
	if runtime.GOOS == "windows" {
		key = strings.ToLower(key)
	}
	return key, absolute, nil
}

func splitFilename(filename string) (string, string) {
	filename = filepath.Base(filename)
	ext := filepath.Ext(filename)
	base := strings.TrimSuffix(filename, ext)
	if base == "" {
		base = "download"
	}
	return base, ext
}

func safePathToken(value string) string {
	value = strings.TrimSpace(value)
	var builder strings.Builder
	for _, character := range value {
		if character < 32 || strings.ContainsRune(`<>:"/\|?*`, character) {
			builder.WriteByte('_')
			continue
		}
		builder.WriteRune(character)
	}
	value = strings.TrimRight(builder.String(), " .")
	if value == "" {
		return "task"
	}
	if len([]rune(value)) > 64 {
		runes := []rune(value)
		hash := sha256.Sum256([]byte(value))
		value = string(runes[:48]) + "-" + hex.EncodeToString(hash[:6])
	}
	return value
}
