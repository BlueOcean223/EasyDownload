package downloader

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	downloadtask "EasyDownload/internal/download/task"
)

func testTaskStoreEnvelope(revision uint64, id string) TaskStoreEnvelope {
	return TaskStoreEnvelope{
		SchemaVersion: TaskStoreSchemaVersion,
		Revision:      revision,
		Tasks: []downloadtask.TaskSnapshot{{
			ID:         id,
			PlatformID: downloadtask.PlatformGeneric,
			Status:     downloadtask.StatusPaused,
		}},
	}
}

func TestTaskStoreRejectsStaleRevision(t *testing.T) {
	store := NewTaskStore(filepath.Join(t.TempDir(), "downloads.v2.json"))
	if err := store.Save(context.Background(), testTaskStoreEnvelope(2, "new")); err != nil {
		t.Fatal(err)
	}
	for _, revision := range []uint64{1, 2} {
		if err := store.Save(context.Background(), testTaskStoreEnvelope(revision, "stale")); !errors.Is(err, ErrStaleTaskRevision) {
			t.Fatalf("revision %d error=%v, want ErrStaleTaskRevision", revision, err)
		}
	}
	loaded, err := store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Revision != 2 || len(loaded.Tasks) != 1 || loaded.Tasks[0].ID != "new" {
		t.Fatalf("stale save changed store: %#v", loaded)
	}
}

func TestTaskStoreV2RoundTripPreservesCreatedAndCompletedTimestamps(t *testing.T) {
	store := NewTaskStore(filepath.Join(t.TempDir(), "downloads.v2.json"))
	wantCreatedAt := int64(1_723_456_789)
	wantCompletedAt := int64(1_723_456_999)
	envelope := TaskStoreEnvelope{
		SchemaVersion: TaskStoreSchemaVersion,
		Revision:      1,
		Tasks: []downloadtask.TaskSnapshot{{
			ID:          "timestamp-round-trip",
			PlatformID:  downloadtask.PlatformGeneric,
			Status:      downloadtask.StatusCompleted,
			CreatedAt:   wantCreatedAt,
			CompletedAt: wantCompletedAt,
		}},
	}
	if err := store.Save(context.Background(), envelope); err != nil {
		t.Fatal(err)
	}

	loaded, err := store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Tasks) != 1 {
		t.Fatalf("loaded tasks=%d, want 1", len(loaded.Tasks))
	}
	got := loaded.Tasks[0]
	if got.CreatedAt != wantCreatedAt || got.CompletedAt != wantCompletedAt {
		t.Fatalf("timestamps after v2 round trip: createdAt=%d completedAt=%d, want %d/%d", got.CreatedAt, got.CompletedAt, wantCreatedAt, wantCompletedAt)
	}
}

func TestTaskStoreCorruptPrimaryNeverPoisonsLKGOnFailedSave(t *testing.T) {
	path := filepath.Join(t.TempDir(), "downloads.v2.json")
	store := NewTaskStore(path)
	if err := store.Save(context.Background(), testTaskStoreEnvelope(1, "one")); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(context.Background(), testTaskStoreEnvelope(2, "two")); err != nil {
		t.Fatal(err)
	}
	lkgBefore, err := os.ReadFile(path + ".lkg")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"schemaVersion":2,"revision":`), 0600); err != nil {
		t.Fatal(err)
	}

	loaded, err := store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Revision != 1 || loaded.Tasks[0].ID != "one" {
		t.Fatalf("fallback loaded %#v, want revision 1 LKG", loaded)
	}

	realWrite := store.writeFile
	backupWrites := 0
	store.writeFile = func(ctx context.Context, destination string, data []byte) error {
		if destination == store.backupPath {
			backupWrites++
		}
		if destination == store.path {
			return errors.New("injected main write failure")
		}
		return realWrite(ctx, destination, data)
	}
	if err := store.Save(context.Background(), testTaskStoreEnvelope(3, "three")); err == nil {
		t.Fatal("expected injected save failure")
	}
	if backupWrites != 0 {
		t.Fatalf("corrupt primary was copied over LKG %d times", backupWrites)
	}
	lkgAfter, err := os.ReadFile(path + ".lkg")
	if err != nil {
		t.Fatal(err)
	}
	if string(lkgAfter) != string(lkgBefore) {
		t.Fatal("LKG changed after corrupt-primary save failure")
	}

	fresh := NewTaskStore(path)
	recovered, err := fresh.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Revision != 1 || recovered.Tasks[0].ID != "one" {
		t.Fatalf("valid LKG no longer recoverable: %#v", recovered)
	}
}

func TestTaskStoreLoadsLKGWhenPrimaryIsMissing(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "downloads.v2.json")
	data, err := json.Marshal(testTaskStoreEnvelope(7, "backup-only"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".lkg", data, 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := NewTaskStore(path).Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Revision != 7 || loaded.Tasks[0].ID != "backup-only" {
		t.Fatalf("loaded %#v", loaded)
	}
}

func TestTaskStoreFailsClosedForUnknownPrimarySchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "downloads.v2.json")
	if err := os.WriteFile(path, []byte(`{"schemaVersion":99,"revision":8,"tasks":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".lkg", []byte(`{"schemaVersion":2,"revision":7,"tasks":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := NewTaskStore(path).Load(context.Background())
	if !errors.Is(err, ErrUnknownTaskSchema) {
		t.Fatalf("error=%v, want ErrUnknownTaskSchema", err)
	}
}

func TestAtomicWriteTreatsPostReplaceDirectorySyncFailureAsCommitted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "downloads.v2.json")
	if err := os.WriteFile(path, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	err := writeAtomicFileWithSync(context.Background(), path, []byte("new"), func(string) error {
		return errors.New("injected directory sync failure")
	})
	if err != nil {
		t.Fatalf("post-replace directory sync was reported as uncommitted: %v", err)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != "new" {
		t.Fatalf("atomic replace did not commit new bytes: %q", contents)
	}
}
