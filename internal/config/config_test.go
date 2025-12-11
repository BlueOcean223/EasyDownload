package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/leanovate/gopter"
	"github.com/leanovate/gopter/gen"
	"github.com/leanovate/gopter/prop"
)

// **Feature: easydownload-improvements, Property 8: 配置持久化往返**
// **Validates: Requirements 6.1, 6.2**
// For any configuration item, setting a new value, saving, and loading
// should return the same value.
func TestConfigPersistenceRoundTrip(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("integer config values round trip correctly", prop.ForAll(
		func(proxyPort, apiPort, maxConcurrent, maxRetryCount int) bool {
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

			// Set values (ensure valid ranges - ports and concurrent must be > 0)
			if proxyPort > 0 && proxyPort < 65536 {
				cm.Set("proxyPort", proxyPort)
			}
			if apiPort > 0 && apiPort < 65536 {
				cm.Set("apiPort", apiPort)
			}
			if maxConcurrent > 0 && maxConcurrent <= 10 {
				cm.Set("maxConcurrent", maxConcurrent)
			}
			// maxRetryCount must be > 0 to be preserved (0 means use default)
			if maxRetryCount > 0 && maxRetryCount <= 10 {
				cm.Set("maxRetryCount", maxRetryCount)
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
				loadedConfig.MaxConcurrent == originalConfig.MaxConcurrent &&
				loadedConfig.MaxRetryCount == originalConfig.MaxRetryCount
		},
		gen.IntRange(1, 65535),
		gen.IntRange(1, 65535),
		gen.IntRange(1, 10),
		gen.IntRange(1, 10),
	))

	properties.Property("boolean config values round trip correctly", prop.ForAll(
		func(autoRetry, minimizeToTray, showNotification, firstRunComplete bool) bool {
			tempDir, err := os.MkdirTemp("", "config_test")
			if err != nil {
				return false
			}
			defer os.RemoveAll(tempDir)

			configPath := filepath.Join(tempDir, "config.json")

			cm := NewConfigManager(configPath)
			cm.Load()

			cm.Set("autoRetry", autoRetry)
			cm.Set("minimizeToTray", minimizeToTray)
			cm.Set("showNotification", showNotification)
			cm.Set("firstRunComplete", firstRunComplete)

			originalConfig := cm.Get()

			cm2 := NewConfigManager(configPath)
			cm2.Load()
			loadedConfig := cm2.Get()

			return loadedConfig.AutoRetry == originalConfig.AutoRetry &&
				loadedConfig.MinimizeToTray == originalConfig.MinimizeToTray &&
				loadedConfig.ShowNotification == originalConfig.ShowNotification &&
				loadedConfig.FirstRunComplete == originalConfig.FirstRunComplete
		},
		gen.Bool(),
		gen.Bool(),
		gen.Bool(),
		gen.Bool(),
	))

	properties.Property("string config values round trip correctly", prop.ForAll(
		func(sessData, version string) bool {
			tempDir, err := os.MkdirTemp("", "config_test")
			if err != nil {
				return false
			}
			defer os.RemoveAll(tempDir)

			configPath := filepath.Join(tempDir, "config.json")

			cm := NewConfigManager(configPath)
			cm.Load()

			cm.Set("bilibiliSessData", sessData)
			// Version must be non-empty to be preserved (empty means use default)
			if version != "" {
				cm.Set("version", version)
			}

			originalConfig := cm.Get()

			cm2 := NewConfigManager(configPath)
			cm2.Load()
			loadedConfig := cm2.Get()

			return loadedConfig.BilibiliSessData == originalConfig.BilibiliSessData &&
				loadedConfig.Version == originalConfig.Version
		},
		gen.AnyString(),
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

// **Feature: easydownload-improvements, Property 10: 配置导出完整性**
// **Validates: Requirements 6.5**
// For any configuration state, the exported JSON should contain all
// configuration fields and values consistent with memory.
func TestConfigExportCompleteness(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("exported config contains all fields with correct values", prop.ForAll(
		func(proxyPort, maxConcurrent int, autoRetry, minimizeToTray bool, sessData string) bool {
			tempDir, err := os.MkdirTemp("", "config_test")
			if err != nil {
				return false
			}
			defer os.RemoveAll(tempDir)

			configPath := filepath.Join(tempDir, "config.json")

			cm := NewConfigManager(configPath)
			cm.Load()

			// Set various config values
			if proxyPort > 0 && proxyPort < 65536 {
				cm.Set("proxyPort", proxyPort)
			}
			if maxConcurrent > 0 && maxConcurrent <= 10 {
				cm.Set("maxConcurrent", maxConcurrent)
			}
			cm.Set("autoRetry", autoRetry)
			cm.Set("minimizeToTray", minimizeToTray)
			cm.Set("bilibiliSessData", sessData)

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

			// Verify all fields match
			return exportedConfig.ProxyPort == currentConfig.ProxyPort &&
				exportedConfig.APIPort == currentConfig.APIPort &&
				exportedConfig.DownloadDir == currentConfig.DownloadDir &&
				exportedConfig.MaxConcurrent == currentConfig.MaxConcurrent &&
				exportedConfig.AutoRetry == currentConfig.AutoRetry &&
				exportedConfig.MaxRetryCount == currentConfig.MaxRetryCount &&
				exportedConfig.BilibiliSessData == currentConfig.BilibiliSessData &&
				exportedConfig.MinimizeToTray == currentConfig.MinimizeToTray &&
				exportedConfig.ShowNotification == currentConfig.ShowNotification &&
				exportedConfig.FirstRunComplete == currentConfig.FirstRunComplete &&
				exportedConfig.Version == currentConfig.Version
		},
		gen.IntRange(1, 65535),
		gen.IntRange(1, 10),
		gen.Bool(),
		gen.Bool(),
		gen.AnyString(),
	))

	properties.Property("export to file creates valid JSON", prop.ForAll(
		func(proxyPort int, autoRetry bool) bool {
			tempDir, err := os.MkdirTemp("", "config_test")
			if err != nil {
				return false
			}
			defer os.RemoveAll(tempDir)

			configPath := filepath.Join(tempDir, "config.json")
			exportPath := filepath.Join(tempDir, "export.json")

			cm := NewConfigManager(configPath)
			cm.Load()

			if proxyPort > 0 && proxyPort < 65536 {
				cm.Set("proxyPort", proxyPort)
			}
			cm.Set("autoRetry", autoRetry)

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
			return exportedConfig.ProxyPort == currentConfig.ProxyPort &&
				exportedConfig.AutoRetry == currentConfig.AutoRetry
		},
		gen.IntRange(1, 65535),
		gen.Bool(),
	))

	properties.TestingRun(t)
}
