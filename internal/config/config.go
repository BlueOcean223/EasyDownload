package config

import (
	"EasyDownload/internal/infra/logger"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// Config represents the application configuration
type Config struct {
	// Proxy settings
	ProxyPort int `json:"proxyPort"`
	APIPort   int `json:"apiPort"`

	// Download settings
	DownloadDir   string `json:"downloadDir"`
	MaxConcurrent int    `json:"maxConcurrent"`

	// Bilibili settings - SESSDATA moved to secure credential storage
	// BilibiliSessData is no longer stored in config file for security

	// System settings
	MinimizeToTray   bool   `json:"minimizeToTray"`
	ShowNotification bool   `json:"showNotification"`
	FirstRunComplete bool   `json:"firstRunComplete"`
	CloseAction      string `json:"closeAction"`    // "exit", "minimize", or "" (ask)
	DontAskOnClose   bool   `json:"dontAskOnClose"` // Whether to skip the close confirmation dialog

	// Appearance settings
	Theme    string `json:"theme"`    // "dark" or "light"
	Language string `json:"language"` // "zh-CN" or "en-US"

	// Proxy chain settings
	UpstreamProxy    string `json:"upstreamProxy"`    // Upstream proxy URL, e.g., http://127.0.0.1:7890
	UseUpstreamProxy bool   `json:"useUpstreamProxy"` // Whether to use upstream proxy

	// Diagnostics
	ProxyDebug bool `json:"proxyDebug"` // Enable verbose proxy diagnostics logs

	// Cached detection results (for faster startup)
	FFmpegPath    string `json:"ffmpegPath,omitempty"` // Cached FFmpeg executable path
	CertInstalled bool   `json:"certInstalled"`        // Cached certificate installation status
	// Welcome wizard behavior
	DontRemindCertWizard bool `json:"dontRemindCertWizard"` // Whether to suppress the certificate welcome wizard prompt

	// Version info
	Version string `json:"version"`
}

// DefaultConfig returns the default configuration
func DefaultConfig() *Config {
	homeDir, _ := os.UserHomeDir()
	defaultDownloadDir := filepath.Join(homeDir, "Downloads", "EasyDownload")

	return &Config{
		ProxyPort:            8899,
		APIPort:              18899,
		DownloadDir:          defaultDownloadDir,
		MaxConcurrent:        3,
		MinimizeToTray:       true,
		ShowNotification:     true,
		FirstRunComplete:     false,
		CloseAction:          "",    // Empty means ask user
		DontAskOnClose:       false, // Show dialog by default
		Theme:                "dark",
		Language:             "zh-CN",
		UpstreamProxy:        "",
		UseUpstreamProxy:     false,
		ProxyDebug:           false,
		FFmpegPath:           "",
		CertInstalled:        false,
		DontRemindCertWizard: false,
		Version:              "1.0.0",
	}
}

// ConfigManager manages application configuration
type ConfigManager struct {
	config     *Config
	configPath string
	persister  Persister
	mu         sync.RWMutex
}

// NewConfigManager creates a new ConfigManager
func NewConfigManager(configPath string) *ConfigManager {
	return NewConfigManagerWithPersister(configPath, nil)
}

// NewConfigManagerWithPersister creates a manager with an injectable durable
// persistence boundary. A nil persister uses the production atomic-file
// implementation.
func NewConfigManagerWithPersister(configPath string, persister Persister) *ConfigManager {
	if persister == nil {
		persister = newAtomicFilePersister()
	}
	return &ConfigManager{
		config:     DefaultConfig(),
		configPath: configPath,
		persister:  persister,
	}
}

// Load loads configuration from file
func (cm *ConfigManager) Load() error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	data, err := os.ReadFile(cm.configPath)
	if err != nil {
		if os.IsNotExist(err) {
			// File doesn't exist, use defaults and create new file
			logger.Info("Config file not found, creating with defaults: %s", cm.configPath)
			return cm.saveInternal()
		}
		return fmt.Errorf("failed to read config file: %w", err)
	}

	// Try to parse the config
	var loadedConfig Config
	if err := json.Unmarshal(data, &loadedConfig); err != nil {
		// Config file is corrupted, backup and use defaults
		logger.Error("Config file corrupted, backing up and using defaults")
		backupPath := cm.configPath + ".backup"
		os.Rename(cm.configPath, backupPath)
		cm.config = DefaultConfig()
		return cm.saveInternal()
	}

	// Merge with defaults to ensure all fields have values
	cm.config = cm.mergeWithDefaults(&loadedConfig)
	logger.Debug("Configuration loaded from: %s", cm.configPath)
	return nil
}

// mergeWithDefaults merges loaded config with defaults for missing fields
func (cm *ConfigManager) mergeWithDefaults(loaded *Config) *Config {
	defaults := DefaultConfig()

	if loaded.ProxyPort == 0 {
		loaded.ProxyPort = defaults.ProxyPort
	}
	if loaded.APIPort == 0 {
		loaded.APIPort = defaults.APIPort
	}
	if loaded.DownloadDir == "" {
		loaded.DownloadDir = defaults.DownloadDir
	}
	if loaded.MaxConcurrent == 0 {
		loaded.MaxConcurrent = defaults.MaxConcurrent
	}
	if loaded.Version == "" {
		loaded.Version = defaults.Version
	}
	if loaded.Theme == "" {
		loaded.Theme = defaults.Theme
	}
	if loaded.Language == "" {
		loaded.Language = defaults.Language
	}

	return loaded
}

// Save saves configuration to file
func (cm *ConfigManager) Save() error {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	return cm.saveInternal()
}

// saveInternal saves config without locking (must be called with lock held)
func (cm *ConfigManager) saveInternal() error {
	return cm.persistLocked(context.Background(), cm.config)
}

func (cm *ConfigManager) persistLocked(ctx context.Context, candidate *Config) error {
	data, err := json.MarshalIndent(candidate, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}
	if err := cm.persister.Persist(ctx, cm.configPath, data, 0600); err != nil {
		return fmt.Errorf("failed to write config file: %w", err)
	}
	return nil
}

// Get returns a copy of the current configuration
func (cm *ConfigManager) Get() *Config {
	cm.mu.RLock()
	defer cm.mu.RUnlock()

	// Return a copy to prevent external modification
	configCopy := *cm.config
	return &configCopy
}

// Commit durably persists next and only then swaps the in-memory snapshot.
// A failed commit leaves both the previous file and in-memory snapshot intact.
func (cm *ConfigManager) Commit(ctx context.Context, next *Config) error {
	if next == nil {
		return fmt.Errorf("config cannot be nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	cm.mu.Lock()
	defer cm.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}

	configCopy := *next
	candidate := cm.mergeWithDefaults(&configCopy)
	if err := cm.persistLocked(ctx, candidate); err != nil {
		return err
	}
	cm.config = candidate
	return nil
}

// Update derives and commits a new snapshot while holding the repository lock.
// It is intended for bounded, in-memory mutations only; callers must not perform
// I/O or call back into ConfigManager from mutate.
func (cm *ConfigManager) Update(ctx context.Context, mutate func(candidate *Config) error) error {
	if mutate == nil {
		return fmt.Errorf("config mutation cannot be nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	cm.mu.Lock()
	defer cm.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}

	candidate := *cm.config
	if err := mutate(&candidate); err != nil {
		return err
	}
	return cm.commitLocked(ctx, &candidate)
}

// RuntimeMetadataPatch is the deliberately narrow write surface for cached
// runtime/application metadata. User settings must be mutated through the
// Settings module instead of ConfigManager.
type RuntimeMetadataPatch struct {
	FFmpegPath    *string
	CertInstalled *bool
	Version       *string
}

// UpdateRuntimeMetadata durably applies typed non-user metadata without
// exposing the old stringly-typed ConfigManager.Set escape hatch.
func (cm *ConfigManager) UpdateRuntimeMetadata(ctx context.Context, patch RuntimeMetadataPatch) error {
	if patch.FFmpegPath == nil && patch.CertInstalled == nil && patch.Version == nil {
		return nil
	}
	return cm.Update(ctx, func(candidate *Config) error {
		if patch.FFmpegPath != nil {
			candidate.FFmpegPath = *patch.FFmpegPath
		}
		if patch.CertInstalled != nil {
			candidate.CertInstalled = *patch.CertInstalled
		}
		if patch.Version != nil {
			candidate.Version = *patch.Version
		}
		return nil
	})
}

func (cm *ConfigManager) commitLocked(ctx context.Context, next *Config) error {
	configCopy := *next
	candidate := cm.mergeWithDefaults(&configCopy)
	if err := cm.persistLocked(ctx, candidate); err != nil {
		return err
	}
	cm.config = candidate
	return nil
}

// Reset resets configuration to defaults
func (cm *ConfigManager) Reset() error {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	return cm.commitLocked(context.Background(), DefaultConfig())
}

// GetConfigPath returns the config file path
func (cm *ConfigManager) GetConfigPath() string {
	return cm.configPath
}

// ValidateDownloadDir validates that a path exists and is writable
func ValidateDownloadDir(path string) error {
	if path == "" {
		return fmt.Errorf("download directory path cannot be empty")
	}
	if !filepath.IsAbs(path) {
		return fmt.Errorf("download directory path must be absolute")
	}

	// Check if path exists
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			// Try to create the directory
			if err := os.MkdirAll(path, 0755); err != nil {
				return fmt.Errorf("directory does not exist and cannot be created: %w", err)
			}
			info, err = os.Stat(path)
			if err != nil {
				return fmt.Errorf("cannot access created directory: %w", err)
			}
		} else {
			return fmt.Errorf("cannot access directory: %w", err)
		}
	}

	// Check if it's a directory
	if !info.IsDir() {
		return fmt.Errorf("path is not a directory")
	}

	// Use a unique probe so validation can never truncate a user-owned file and
	// concurrent validations do not contend on one fixed name.
	f, err := os.CreateTemp(path, ".easydownload-write-test-*")
	if err != nil {
		return fmt.Errorf("directory is not writable: %w", err)
	}
	testFile := f.Name()
	closed := false
	defer func() {
		if !closed {
			_ = f.Close()
		}
		_ = os.Remove(testFile)
	}()
	if _, err := f.Write([]byte{0}); err != nil {
		return fmt.Errorf("directory write probe failed: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close directory write probe: %w", err)
	}
	closed = true

	return nil
}

// Export exports the configuration to a JSON file
func (cm *ConfigManager) Export(exportPath string) error {
	cm.mu.RLock()
	defer cm.mu.RUnlock()

	data, err := json.MarshalIndent(cm.config, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal config for export: %w", err)
	}

	// Ensure directory exists with secure permissions
	dir := filepath.Dir(exportPath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("failed to create export directory: %w", err)
	}

	// Write export file with secure permissions
	if err := os.WriteFile(exportPath, data, 0600); err != nil {
		return fmt.Errorf("failed to write export file: %w", err)
	}

	return nil
}

// ExportToBytes exports the configuration as JSON bytes
func (cm *ConfigManager) ExportToBytes() ([]byte, error) {
	cm.mu.RLock()
	defer cm.mu.RUnlock()

	data, err := json.MarshalIndent(cm.config, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("failed to marshal config for export: %w", err)
	}

	return data, nil
}

// Import imports configuration from a JSON file
func (cm *ConfigManager) Import(importPath string) error {
	data, err := os.ReadFile(importPath)
	if err != nil {
		return fmt.Errorf("failed to read import file: %w", err)
	}

	cm.mu.Lock()
	defer cm.mu.Unlock()

	var importedConfig Config
	if err := json.Unmarshal(data, &importedConfig); err != nil {
		return fmt.Errorf("failed to parse import file: %w", err)
	}

	// Validate download directory if specified
	if importedConfig.DownloadDir != "" {
		if err := ValidateDownloadDir(importedConfig.DownloadDir); err != nil {
			// Use default if invalid
			importedConfig.DownloadDir = DefaultConfig().DownloadDir
		}
	}

	return cm.commitLocked(context.Background(), &importedConfig)
}
