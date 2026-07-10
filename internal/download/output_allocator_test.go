package downloader

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	downloadtask "EasyDownload/internal/download/task"
)

func TestOutputPathAllocatorReservesAndRestoresUniqueNames(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "nested", "downloads")
	allocator := NewOutputPathAllocator()
	first, err := allocator.Reserve("one", directory, "video.mp4", downloadtask.ConflictStrategyAutoRename)
	if err != nil {
		t.Fatal(err)
	}
	second, err := allocator.Reserve("two", directory, "video.mp4", downloadtask.ConflictStrategyAutoRename)
	if err != nil {
		t.Fatal(err)
	}
	if first.PlannedFilename != "video.mp4" || second.PlannedFilename != "video (1).mp4" {
		t.Fatalf("unexpected reservations: first=%#v second=%#v", first, second)
	}
	if info, err := os.Stat(directory); err != nil || !info.IsDir() {
		t.Fatalf("custom output directory was not prepared: info=%v err=%v", info, err)
	}
	allocator.Release("one")
	restored := NewOutputPathAllocator()
	if err := restored.Restore("two", second); err != nil {
		t.Fatal(err)
	}
	if owner, ok := restored.Owner(second.PlannedFinalPath); !ok || owner != "two" {
		t.Fatalf("restored owner=%q ok=%v", owner, ok)
	}
}

func TestOutputPathAllocatorConcurrentSameNameReservationsAreUnique(t *testing.T) {
	const count = 32
	allocator := NewOutputPathAllocator()
	directory := t.TempDir()
	paths := make(chan string, count)
	errs := make(chan error, count)
	var workers sync.WaitGroup
	for index := 0; index < count; index++ {
		workers.Add(1)
		go func(index int) {
			defer workers.Done()
			policy, err := allocator.Reserve(
				fmt.Sprintf("task-%d", index),
				directory,
				"video.mp4",
				downloadtask.ConflictStrategyAutoRename,
			)
			if err != nil {
				errs <- err
				return
			}
			paths <- policy.PlannedFinalPath
		}(index)
	}
	workers.Wait()
	close(paths)
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	unique := make(map[string]struct{}, count)
	for path := range paths {
		if _, exists := unique[path]; exists {
			t.Fatalf("duplicate concurrent reservation: %s", path)
		}
		unique[path] = struct{}{}
	}
	if len(unique) != count {
		t.Fatalf("unique reservations=%d, want %d", len(unique), count)
	}
}

func TestOutputPathAllocatorWindowsKeysAreCaseInsensitive(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows path canonicalization")
	}
	allocator := NewOutputPathAllocator()
	directory := t.TempDir()
	first, err := allocator.Reserve("one", directory, "Video.mp4", downloadtask.ConflictStrategyAutoRename)
	if err != nil {
		t.Fatal(err)
	}
	conflicting := first
	conflicting.PlannedFinalPath = strings.ToUpper(first.PlannedFinalPath)
	if err := allocator.Restore("two", conflicting); !errors.Is(err, ErrOutputReserved) {
		t.Fatalf("case-only conflict error=%v, want ErrOutputReserved", err)
	}
}

func TestOutputPathAllocatorReallocateFailureKeepsOldReservation(t *testing.T) {
	allocator := NewOutputPathAllocator()
	policy, err := allocator.Reserve("task", t.TempDir(), "video.mp4", downloadtask.ConflictStrategyAutoRename)
	if err != nil {
		t.Fatal(err)
	}
	notDirectory := filepath.Join(t.TempDir(), "ordinary-file")
	if err := os.WriteFile(notDirectory, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	broken := policy
	broken.Directory = notDirectory
	if _, err := allocator.Reallocate("task", broken); err == nil {
		t.Fatal("expected reallocation failure")
	}
	if owner, ok := allocator.Owner(policy.PlannedFinalPath); !ok || owner != "task" {
		t.Fatalf("old reservation was lost after reallocation error: owner=%q ok=%v", owner, ok)
	}
}

func TestSafePathTokenRejectsWindowsIllegalCharacters(t *testing.T) {
	token := safePathToken(`  bad<>:"/\|?*` + string(rune(1)) + `. `)
	if token == "" || strings.ContainsAny(token, `<>:"/\|?*`) {
		t.Fatalf("unsafe token %q", token)
	}
	if strings.HasSuffix(token, ".") || strings.HasSuffix(token, " ") {
		t.Fatalf("unsafe trailing token character: %q", token)
	}
}

func TestPublishNoReplaceNeverOverwritesExternalFile(t *testing.T) {
	directory := t.TempDir()
	temporary := filepath.Join(directory, "temporary.part")
	final := filepath.Join(directory, "final.mp4")
	if err := os.WriteFile(temporary, []byte("ours"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(final, []byte("external"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := publishNoReplace(temporary, final); err == nil {
		t.Fatal("expected no-replace conflict")
	}
	finalBytes, err := os.ReadFile(final)
	if err != nil {
		t.Fatal(err)
	}
	if string(finalBytes) != "external" {
		t.Fatalf("external final was overwritten: %q", finalBytes)
	}
	if _, err := os.Stat(temporary); err != nil {
		t.Fatalf("temporary file was lost on conflict: %v", err)
	}
}

func TestPublishNoReplaceMovesTemporaryAndPreservesPayload(t *testing.T) {
	directory := t.TempDir()
	temporary := filepath.Join(directory, "temporary.part")
	final := filepath.Join(directory, "final.mp4")
	payload := []byte("portable atomic publish")
	if err := os.WriteFile(temporary, payload, 0600); err != nil {
		t.Fatal(err)
	}
	outcome, err := publishNoReplace(temporary, final)
	if err != nil {
		t.Fatal(err)
	}
	if !outcome.Committed {
		t.Fatal("successful publish did not report the visibility commit")
	}
	if len(outcome.Warnings) != 0 {
		t.Fatalf("unexpected publish warnings: %#v", outcome.Warnings)
	}
	if _, err := os.Stat(temporary); !os.IsNotExist(err) {
		t.Fatalf("temporary path remained after publish: %v", err)
	}
	got, err := os.ReadFile(final)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(payload) {
		t.Fatalf("published payload=%q, want %q", got, payload)
	}
}

func TestPublishNoReplaceAllowsExactlyOneConcurrentCommit(t *testing.T) {
	directory := t.TempDir()
	final := filepath.Join(directory, "final.mp4")
	temporaryPaths := []string{
		filepath.Join(directory, "first.part"),
		filepath.Join(directory, "second.part"),
	}
	for index, path := range temporaryPaths {
		if err := os.WriteFile(path, []byte(fmt.Sprintf("payload-%d", index)), 0600); err != nil {
			t.Fatal(err)
		}
	}
	errs := make(chan error, len(temporaryPaths))
	var workers sync.WaitGroup
	for _, path := range temporaryPaths {
		workers.Add(1)
		go func(path string) {
			defer workers.Done()
			_, err := publishNoReplace(path, final)
			errs <- err
		}(path)
	}
	workers.Wait()
	close(errs)

	succeeded, conflicted := 0, 0
	for err := range errs {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, ErrOutputExists):
			conflicted++
		default:
			t.Fatalf("unexpected concurrent publish error: %v", err)
		}
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatalf("publish results: succeeded=%d conflicted=%d", succeeded, conflicted)
	}
	if _, err := os.Stat(final); err != nil {
		t.Fatal(err)
	}
}

func TestFinishPublishedFileReportsPostCommitFailuresAsWarnings(t *testing.T) {
	temporary := filepath.Join(t.TempDir(), "temporary.part")
	final := filepath.Join(t.TempDir(), "final.mp4")
	removeCalled := false
	outcome := finishPublishedFile(
		temporary,
		final,
		true,
		func(path string) error { return fmt.Errorf("sync failed for %s", filepath.Base(path)) },
		func(string) error {
			removeCalled = true
			return errors.New("unexpected remove")
		},
	)
	if removeCalled {
		t.Fatal("rename-style commit attempted fallback source cleanup")
	}
	if !outcome.Committed {
		t.Fatal("post-commit maintenance failure erased the visibility commit")
	}
	if len(outcome.Warnings) != 2 {
		t.Fatalf("warnings=%#v, want final and temporary directory sync diagnostics", outcome.Warnings)
	}
	if outcome.Warnings[0].Code != "publish.final_directory_sync_failed" ||
		outcome.Warnings[1].Code != "publish.temporary_directory_sync_failed" {
		t.Fatalf("unexpected warning codes: %#v", outcome.Warnings)
	}
}

func TestFinishHardLinkFallbackKeepsCommitWhenTemporaryCleanupFails(t *testing.T) {
	temporary := filepath.Join(t.TempDir(), "temporary.part")
	final := filepath.Join(filepath.Dir(temporary), "final.mp4")
	outcome := finishPublishedFile(
		temporary,
		final,
		false,
		func(string) error { return nil },
		func(string) error { return errors.New("temporary is busy") },
	)
	if !outcome.Committed {
		t.Fatal("fallback cleanup failure erased the visibility commit")
	}
	if len(outcome.Warnings) != 1 || outcome.Warnings[0].Code != "publish.temporary_cleanup_failed" {
		t.Fatalf("unexpected fallback cleanup warnings: %#v", outcome.Warnings)
	}
}
