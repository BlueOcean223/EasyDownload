package config

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/leanovate/gopter"
	"github.com/leanovate/gopter/gen"
	"github.com/leanovate/gopter/prop"
)

type togglePersister struct {
	delegate Persister
	err      error
	calls    int
}

func (p *togglePersister) Persist(ctx context.Context, path string, data []byte, perm os.FileMode) error {
	p.calls++
	if p.err != nil {
		return p.err
	}
	return p.delegate.Persist(ctx, path, data, perm)
}

func TestCommitFailureKeepsDiskAndMemoryUnchanged(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	persister := &togglePersister{delegate: newAtomicFilePersister()}
	cm := NewConfigManagerWithPersister(path, persister)
	if err := cm.Load(); err != nil {
		t.Fatal(err)
	}
	beforeDisk, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	before := cm.Get()

	next := *before
	next.Theme = "light"
	persister.err = errors.New("injected persist failure")
	if err := cm.Commit(context.Background(), &next); err == nil {
		t.Fatal("Commit succeeded despite injected persistence failure")
	}

	if got := cm.Get().Theme; got != before.Theme {
		t.Fatalf("in-memory theme changed after failed commit: got %q want %q", got, before.Theme)
	}
	afterDisk, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(afterDisk) != string(beforeDisk) {
		t.Fatalf("config file changed after failed commit\nbefore: %s\nafter: %s", beforeDisk, afterDisk)
	}
}

func TestUpdateAlsoKeepsMemoryUnchangedWhenPersistenceFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	persister := &togglePersister{delegate: newAtomicFilePersister()}
	cm := NewConfigManagerWithPersister(path, persister)
	if err := cm.Load(); err != nil {
		t.Fatal(err)
	}
	before := cm.Get().Theme
	persister.err = errors.New("injected persist failure")
	if err := cm.Update(context.Background(), func(candidate *Config) error {
		candidate.Theme = "light"
		return nil
	}); err == nil {
		t.Fatal("Update succeeded despite injected persistence failure")
	}
	if got := cm.Get().Theme; got != before {
		t.Fatalf("in-memory theme changed after failed Update: got %q want %q", got, before)
	}
}

func TestTypedRuntimeMetadataWriterPreservesUserSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	cm := NewConfigManager(path)
	if err := cm.Load(); err != nil {
		t.Fatal(err)
	}
	if err := cm.Update(context.Background(), func(candidate *Config) error {
		candidate.Theme = "light"
		candidate.Language = "en-US"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	ffmpegPath := filepath.Join(t.TempDir(), "ffmpeg")
	installed := true
	version := "1.2.3"
	if err := cm.UpdateRuntimeMetadata(context.Background(), RuntimeMetadataPatch{
		FFmpegPath: &ffmpegPath, CertInstalled: &installed, Version: &version,
	}); err != nil {
		t.Fatal(err)
	}
	got := cm.Get()
	if got.Theme != "light" || got.Language != "en-US" {
		t.Fatalf("runtime metadata writer changed user settings: theme=%q language=%q", got.Theme, got.Language)
	}
	if got.FFmpegPath != ffmpegPath || !got.CertInstalled || got.Version != version {
		t.Fatalf("runtime metadata was not applied: %+v", got)
	}
}

func TestAtomicFilePersisterFailureStagesPreserveOldFile(t *testing.T) {
	for _, stage := range []atomicWriteStage{
		atomicWriteStageWrite,
		atomicWriteStageSync,
		atomicWriteStageReplace,
	} {
		t.Run(string(stage), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			if err := os.WriteFile(path, []byte("old"), 0600); err != nil {
				t.Fatal(err)
			}
			persister := &atomicFilePersister{beforeStage: func(current atomicWriteStage) error {
				if current == stage {
					return errors.New("injected " + string(stage) + " failure")
				}
				return nil
			}}
			if err := persister.Persist(context.Background(), path, []byte("new"), 0600); err == nil {
				t.Fatalf("Persist succeeded despite injected %s failure", stage)
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != "old" {
				t.Fatalf("target changed after %s failure: %q", stage, got)
			}
			matches, err := filepath.Glob(filepath.Join(filepath.Dir(path), ".easydownload-config-*.tmp"))
			if err != nil {
				t.Fatal(err)
			}
			if len(matches) != 0 {
				t.Fatalf("temporary config leaked after %s failure: %v", stage, matches)
			}
		})
	}
}

func TestAtomicFilePersisterReplacesExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := newAtomicFilePersister().Persist(context.Background(), path, []byte("new"), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new" {
		t.Fatalf("target was not replaced: %q", got)
	}
}

func TestAtomicFilePersisterSyncsParentDirectoryAfterReplace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	calledWith := ""
	persister := &atomicFilePersister{syncParent: func(path string) error {
		calledWith = path
		return nil
	}}
	if err := persister.Persist(context.Background(), path, []byte("new"), 0600); err != nil {
		t.Fatal(err)
	}
	if calledWith != filepath.Dir(path) {
		t.Fatalf("parent directory sync called with %q, want %q", calledWith, filepath.Dir(path))
	}
}

func TestParentDirectorySyncFailureKeepsCommittedDiskAndMemoryConverged(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	failDirectorySync := false
	persister := &atomicFilePersister{syncParent: func(string) error {
		if failDirectorySync {
			return errors.New("injected directory sync failure")
		}
		return nil
	}}
	cm := NewConfigManagerWithPersister(path, persister)
	if err := cm.Load(); err != nil {
		t.Fatal(err)
	}

	next := cm.Get()
	next.Theme = "light"
	failDirectorySync = true
	if err := cm.Commit(context.Background(), next); err != nil {
		t.Fatalf("post-replace directory sync must not report an uncommitted transaction: %v", err)
	}
	if got := cm.Get().Theme; got != "light" {
		t.Fatalf("in-memory config did not advance after replace: %q", got)
	}
	disk, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var persisted Config
	if err := json.Unmarshal(disk, &persisted); err != nil {
		t.Fatal(err)
	}
	if persisted.Theme != "light" {
		t.Fatalf("disk and memory diverged after directory sync failure: disk theme %q", persisted.Theme)
	}
}

// **Feature: easydownload-improvements, Property 8: 配置持久化往返**
// **Validates: Requirements 6.1, 6.2**
// For any configuration item, setting a new value, saving, and loading
// should return the same value.
func TestConfigPersistenceRoundTrip(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("integer config values round trip correctly", prop.ForAll(
		func(proxyPort, apiPort, maxConcurrent int) bool {
			// Create temp directory for test
			tempDir, err := os.MkdirTemp("", "config_test")
			if err != nil {
				return false
			}
			defer os.RemoveAll(tempDir)

			configPath := filepath.Join(tempDir, "config.json")

			// Create and configure manager
			cm := NewConfigManager(configPath)
			cm.Load()

			if err := cm.Update(context.Background(), func(candidate *Config) error {
				candidate.ProxyPort = proxyPort
				candidate.APIPort = apiPort
				candidate.MaxConcurrent = maxConcurrent
				return nil
			}); err != nil {
				return false
			}
			// Get current config
			originalConfig := cm.Get()

			// Create new manager and load
			cm2 := NewConfigManager(configPath)
			if err := cm2.Load(); err != nil {
				return false
			}

			loadedConfig := cm2.Get()

			// Verify values match
			return loadedConfig.ProxyPort == originalConfig.ProxyPort &&
				loadedConfig.APIPort == originalConfig.APIPort &&
				loadedConfig.MaxConcurrent == originalConfig.MaxConcurrent
		},
		gen.IntRange(1, 65535),
		gen.IntRange(1, 65535),
		gen.IntRange(1, 10),
	))

	properties.Property("boolean config values round trip correctly", prop.ForAll(
		func(minimizeToTray, showNotification, firstRunComplete bool) bool {
			tempDir, err := os.MkdirTemp("", "config_test")
			if err != nil {
				return false
			}
			defer os.RemoveAll(tempDir)

			configPath := filepath.Join(tempDir, "config.json")

			cm := NewConfigManager(configPath)
			cm.Load()

			if err := cm.Update(context.Background(), func(candidate *Config) error {
				candidate.MinimizeToTray = minimizeToTray
				candidate.ShowNotification = showNotification
				candidate.FirstRunComplete = firstRunComplete
				return nil
			}); err != nil {
				return false
			}

			originalConfig := cm.Get()

			cm2 := NewConfigManager(configPath)
			cm2.Load()
			loadedConfig := cm2.Get()

			return loadedConfig.MinimizeToTray == originalConfig.MinimizeToTray &&
				loadedConfig.ShowNotification == originalConfig.ShowNotification &&
				loadedConfig.FirstRunComplete == originalConfig.FirstRunComplete
		},
		gen.Bool(),
		gen.Bool(),
		gen.Bool(),
	))

	properties.Property("string config values round trip correctly", prop.ForAll(
		func(version string) bool {
			tempDir, err := os.MkdirTemp("", "config_test")
			if err != nil {
				return false
			}
			defer os.RemoveAll(tempDir)

			configPath := filepath.Join(tempDir, "config.json")

			cm := NewConfigManager(configPath)
			cm.Load()

			if err := cm.UpdateRuntimeMetadata(context.Background(), RuntimeMetadataPatch{Version: &version}); err != nil {
				return false
			}

			originalConfig := cm.Get()

			cm2 := NewConfigManager(configPath)
			cm2.Load()
			loadedConfig := cm2.Get()

			return loadedConfig.Version == originalConfig.Version
		},
		gen.AnyString().SuchThat(func(s string) bool { return len(s) > 0 }),
	))

	properties.TestingRun(t)
}

// **Feature: easydownload-improvements, Property 9: 下载目录验证**
// **Validates: Requirements 6.4**
// For any path string, if the path does not exist or is not writable,
// setting the download directory should return an error.
func TestDownloadDirValidation(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("valid writable directories are accepted", prop.ForAll(
		func(subdir string) bool {
			tempDir, err := os.MkdirTemp("", "config_test")
			if err != nil {
				return false
			}
			defer os.RemoveAll(tempDir)

			// Create a valid subdirectory
			validDir := filepath.Join(tempDir, "downloads")
			os.MkdirAll(validDir, 0755)

			err = ValidateDownloadDir(validDir)
			return err == nil
		},
		gen.AlphaString().SuchThat(func(s string) bool {
			return len(s) > 0 && len(s) < 50
		}),
	))

	properties.Property("empty path is rejected", prop.ForAll(
		func(_ bool) bool {
			err := ValidateDownloadDir("")
			return err != nil
		},
		gen.Bool(),
	))

	properties.Property("non-existent paths that can be created are accepted", prop.ForAll(
		func(subdir string) bool {
			tempDir, err := os.MkdirTemp("", "config_test")
			if err != nil {
				return false
			}
			defer os.RemoveAll(tempDir)

			// Path that doesn't exist yet but can be created
			newDir := filepath.Join(tempDir, subdir)

			err = ValidateDownloadDir(newDir)
			if err != nil {
				return false
			}

			// Verify directory was created
			info, err := os.Stat(newDir)
			return err == nil && info.IsDir()
		},
		gen.AlphaString().SuchThat(func(s string) bool {
			return len(s) > 0 && len(s) < 50
		}),
	))

	properties.TestingRun(t)
}

func TestValidateDownloadDirDoesNotOverwriteLegacyProbeName(t *testing.T) {
	directory := t.TempDir()
	sentinelPath := filepath.Join(directory, ".easydownload_write_test")
	const sentinel = "user-owned-content"
	if err := os.WriteFile(sentinelPath, []byte(sentinel), 0600); err != nil {
		t.Fatal(err)
	}
	if err := ValidateDownloadDir(directory); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(sentinelPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != sentinel {
		t.Fatalf("legacy probe-name file was modified: %q", content)
	}
}

// **Feature: easydownload-improvements, Property 10: 配置导出完整性**
// **Validates: Requirements 6.5**
// For any configuration state, the exported JSON should contain all
// configuration fields and values consistent with memory.
func TestConfigExportCompleteness(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("exported config contains all fields with correct values", prop.ForAll(
		func(proxyPort, maxConcurrent int, minimizeToTray bool) bool {
			tempDir, err := os.MkdirTemp("", "config_test")
			if err != nil {
				return false
			}
			defer os.RemoveAll(tempDir)

			configPath := filepath.Join(tempDir, "config.json")

			cm := NewConfigManager(configPath)
			cm.Load()

			if err := cm.Update(context.Background(), func(candidate *Config) error {
				candidate.ProxyPort = proxyPort
				candidate.MaxConcurrent = maxConcurrent
				candidate.MinimizeToTray = minimizeToTray
				return nil
			}); err != nil {
				return false
			}
			// Note: bilibiliSessData removed - now stored in secure credential storage

			// Export to bytes
			exportedData, err := cm.ExportToBytes()
			if err != nil {
				return false
			}

			// Parse exported JSON
			var exportedConfig Config
			if err := json.Unmarshal(exportedData, &exportedConfig); err != nil {
				return false
			}

			// Get current config
			currentConfig := cm.Get()

			// Verify all fields match (excluding BilibiliSessData which is now in secure storage)
			return exportedConfig.ProxyPort == currentConfig.ProxyPort &&
				exportedConfig.APIPort == currentConfig.APIPort &&
				exportedConfig.DownloadDir == currentConfig.DownloadDir &&
				exportedConfig.MaxConcurrent == currentConfig.MaxConcurrent &&
				exportedConfig.MinimizeToTray == currentConfig.MinimizeToTray &&
				exportedConfig.ShowNotification == currentConfig.ShowNotification &&
				exportedConfig.FirstRunComplete == currentConfig.FirstRunComplete &&
				exportedConfig.Version == currentConfig.Version
		},
		gen.IntRange(1, 65535),
		gen.IntRange(1, 10),
		gen.Bool(),
	))

	properties.Property("export to file creates valid JSON", prop.ForAll(
		func(proxyPort int) bool {
			tempDir, err := os.MkdirTemp("", "config_test")
			if err != nil {
				return false
			}
			defer os.RemoveAll(tempDir)

			configPath := filepath.Join(tempDir, "config.json")
			exportPath := filepath.Join(tempDir, "export.json")

			cm := NewConfigManager(configPath)
			cm.Load()

			if err := cm.Update(context.Background(), func(candidate *Config) error {
				candidate.ProxyPort = proxyPort
				return nil
			}); err != nil {
				return false
			}
			// Export to file
			if err := cm.Export(exportPath); err != nil {
				return false
			}

			// Read and parse exported file
			data, err := os.ReadFile(exportPath)
			if err != nil {
				return false
			}

			var exportedConfig Config
			if err := json.Unmarshal(data, &exportedConfig); err != nil {
				return false
			}

			currentConfig := cm.Get()
			return exportedConfig.ProxyPort == currentConfig.ProxyPort
		},
		gen.IntRange(1, 65535),
	))

	properties.TestingRun(t)
}
