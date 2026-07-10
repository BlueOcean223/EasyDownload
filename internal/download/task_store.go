package downloader

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	downloadtask "EasyDownload/internal/download/task"
)

const TaskStoreSchemaVersion = 2

var (
	ErrStaleTaskRevision = errors.New("stale task-store revision")
	ErrUnknownTaskSchema = errors.New("unknown task-store schema")
)

type TaskStoreEnvelope struct {
	SchemaVersion int                         `json:"schemaVersion"`
	Revision      uint64                      `json:"revision"`
	Tasks         []downloadtask.TaskSnapshot `json:"tasks"`
}

// TaskStore is a single-writer, revision-aware, crash-safe snapshot store.
// Save is synchronous: success means the atomic replace has completed.
type TaskStore struct {
	mu                sync.Mutex
	path              string
	backupPath        string
	committedRevision uint64
	writeFile         func(context.Context, string, []byte) error
}

func NewTaskStore(path string) *TaskStore {
	return &TaskStore{
		path:       path,
		backupPath: path + ".lkg",
		writeFile:  writeAtomicFile,
	}
}

func (s *TaskStore) Path() string {
	if s == nil {
		return ""
	}
	return s.path
}

func (s *TaskStore) CommittedRevision() uint64 {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.committedRevision
}

func (s *TaskStore) Save(ctx context.Context, envelope TaskStoreEnvelope) error {
	if s == nil || s.path == "" {
		return errors.New("task store path is not configured")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if envelope.SchemaVersion == 0 {
		envelope.SchemaVersion = TaskStoreSchemaVersion
	}
	if envelope.SchemaVersion != TaskStoreSchemaVersion {
		return fmt.Errorf("%w: %d", ErrUnknownTaskSchema, envelope.SchemaVersion)
	}
	data, err := json.MarshalIndent(cloneTaskStoreEnvelope(envelope), "", "  ")
	if err != nil {
		return fmt.Errorf("marshal task store: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if envelope.Revision <= s.committedRevision {
		return fmt.Errorf("%w: got=%d committed=%d", ErrStaleTaskRevision, envelope.Revision, s.committedRevision)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	currentData, currentErr := os.ReadFile(s.path)
	if currentErr != nil && !os.IsNotExist(currentErr) {
		return fmt.Errorf("read current task store: %w", currentErr)
	}
	currentValid := false
	if currentErr == nil {
		currentEnvelope, decodeErr := decodeTaskStoreData(s.path, currentData)
		if decodeErr == nil {
			currentValid = true
			if envelope.Revision <= currentEnvelope.Revision {
				return fmt.Errorf("%w: got=%d on_disk=%d", ErrStaleTaskRevision, envelope.Revision, currentEnvelope.Revision)
			}
			if err := s.writeFile(ctx, s.backupPath, currentData); err != nil {
				return fmt.Errorf("write last-known-good task store: %w", err)
			}
		} else if errors.Is(decodeErr, ErrUnknownTaskSchema) {
			// A newer schema is not corruption. Never overwrite it with a v2
			// writer that cannot understand its rollback semantics.
			return decodeErr
		}
	}
	// If primary is absent or corrupt, the LKG still participates in stale
	// detection but is never overwritten by the bad primary bytes.
	if !currentValid {
		if backupEnvelope, backupErr := readTaskStoreFile(s.backupPath); backupErr == nil {
			if envelope.Revision <= backupEnvelope.Revision {
				return fmt.Errorf("%w: got=%d lkg=%d", ErrStaleTaskRevision, envelope.Revision, backupEnvelope.Revision)
			}
		} else if !os.IsNotExist(backupErr) && errors.Is(backupErr, ErrUnknownTaskSchema) {
			return backupErr
		}
	}
	if err := s.writeFile(ctx, s.path, data); err != nil {
		return err
	}
	s.committedRevision = envelope.Revision
	return nil
}

func (s *TaskStore) Load(ctx context.Context) (TaskStoreEnvelope, error) {
	if s == nil || s.path == "" {
		return TaskStoreEnvelope{}, errors.New("task store path is not configured")
	}
	if err := ctx.Err(); err != nil {
		return TaskStoreEnvelope{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	envelope, primaryErr := readTaskStoreFile(s.path)
	if primaryErr != nil {
		if errors.Is(primaryErr, ErrUnknownTaskSchema) {
			return TaskStoreEnvelope{}, primaryErr
		}
		backup, backupErr := readTaskStoreFile(s.backupPath)
		if backupErr != nil {
			if os.IsNotExist(primaryErr) && os.IsNotExist(backupErr) {
				return TaskStoreEnvelope{SchemaVersion: TaskStoreSchemaVersion}, nil
			}
			return TaskStoreEnvelope{}, fmt.Errorf("task store unreadable (primary: %v; backup: %v)", primaryErr, backupErr)
		}
		envelope = backup
	}
	if envelope.SchemaVersion != TaskStoreSchemaVersion {
		return TaskStoreEnvelope{}, fmt.Errorf("%w: %d", ErrUnknownTaskSchema, envelope.SchemaVersion)
	}
	s.committedRevision = envelope.Revision
	return cloneTaskStoreEnvelope(envelope), nil
}

func readTaskStoreFile(path string) (TaskStoreEnvelope, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return TaskStoreEnvelope{}, err
	}
	return decodeTaskStoreData(path, data)
}

func decodeTaskStoreData(path string, data []byte) (TaskStoreEnvelope, error) {
	var envelope TaskStoreEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		return TaskStoreEnvelope{}, fmt.Errorf("decode %s: %w", path, err)
	}
	if envelope.SchemaVersion != TaskStoreSchemaVersion {
		return TaskStoreEnvelope{}, fmt.Errorf("%w: %d", ErrUnknownTaskSchema, envelope.SchemaVersion)
	}
	return envelope, nil
}

func writeAtomicFile(ctx context.Context, path string, data []byte) (err error) {
	return writeAtomicFileWithSync(ctx, path, data, syncParentDirectory)
}

func writeAtomicFileWithSync(ctx context.Context, path string, data []byte, syncDirectory func(string) error) (err error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0700); err != nil {
		return fmt.Errorf("create task-store directory: %w", err)
	}
	temporary, err := os.CreateTemp(directory, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("create task-store temporary file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() {
		_ = temporary.Close()
		_ = os.Remove(temporaryPath)
	}()
	if err := temporary.Chmod(0600); err != nil {
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		return fmt.Errorf("write task-store temporary file: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync task-store temporary file: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close task-store temporary file: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := atomicReplaceFile(temporaryPath, path); err != nil {
		return fmt.Errorf("replace task store: %w", err)
	}
	if syncDirectory != nil {
		if err := syncDirectory(path); err != nil {
			// Atomic replace is the commit boundary: the new complete snapshot is
			// already visible. A parent-directory sync failure reduces crash
			// durability but must not make callers retry as if the old revision were
			// still committed (which would fork revision/LKG bookkeeping).
			return nil
		}
	}
	return nil
}

func cloneTaskStoreEnvelope(envelope TaskStoreEnvelope) TaskStoreEnvelope {
	clone := TaskStoreEnvelope{
		SchemaVersion: envelope.SchemaVersion,
		Revision:      envelope.Revision,
		Tasks:         make([]downloadtask.TaskSnapshot, len(envelope.Tasks)),
	}
	for index := range envelope.Tasks {
		clone.Tasks[index] = downloadtask.CloneSnapshot(envelope.Tasks[index])
	}
	return clone
}
