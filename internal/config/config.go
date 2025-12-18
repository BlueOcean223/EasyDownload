package config

import (
	"EasyDownload/internal/logger"
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
	AutoRetry     bool   `json:"autoRetry"`
	MaxRetryCount int    `json:"maxRetryCount"`

	// Bilibili settings - SESSDATA moved to secure credential storage
	// BilibiliSessData is no longer stored in config file for security

	// System settings
	MinimizeToTray   bool `json:"minimizeToTray"`
	ShowNotification bool `json:"showNotification"`
	FirstRunComplete bool `json:"firstRunComplete"`

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

	// Version info
	Version string `json:"version"`
}

// DefaultConfig returns the default configuration
func DefaultConfig() *Config {
	homeDir, _ := os.UserHomeDir()
	defaultDownloadDir := filepath.Join(homeDir, "Downloads", "EasyDownload")

	return &Config{
		ProxyPort:        8899,
		APIPort:          18899,
		DownloadDir:      defaultDownloadDir,
		MaxConcurrent:    3,
		AutoRetry:        true,
		MaxRetryCount:    3,
		MinimizeToTray:   true,
		ShowNotification: true,
		FirstRunComplete: false,
		Theme:            "dark",
		Language:         "zh-CN",
		UpstreamProxy:    "",
		UseUpstreamProxy: false,
		ProxyDebug:       false,
		FFmpegPath:       "",
		CertInstalled:    false,
		Version:          "1.0.0",
	}
}

// ConfigManager manages application configuration
type ConfigManager struct {
	config     *Config
	configPath string
	mu         sync.RWMutex
}

// NewConfigManager creates a new ConfigManager
func NewConfigManager(configPath string) *ConfigManager {
	return &ConfigManager{
		config:     DefaultConfig(),
		configPath: configPath,
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
	if loaded.MaxRetryCount == 0 {
		loaded.MaxRetryCount = defaults.MaxRetryCount
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
	// Ensure directory exists with secure permissions (only owner can access)
	dir := filepath.Dir(cm.configPath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	data, err := json.MarshalIndent(cm.config, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	// Write config file with secure permissions (only owner can read/write)
	if err := os.WriteFile(cm.configPath, data, 0600); err != nil {
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

// Set sets a configuration value by key
func (cm *ConfigManager) Set(key string, value any) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	switch key {
	case "proxyPort":
		if v, ok := value.(int); ok {
			cm.config.ProxyPort = v
		} else if v, ok := value.(float64); ok {
			cm.config.ProxyPort = int(v)
		} else {
			return fmt.Errorf("invalid type for proxyPort")
		}
	case "apiPort":
		if v, ok := value.(int); ok {
			cm.config.APIPort = v
		} else if v, ok := value.(float64); ok {
			cm.config.APIPort = int(v)
		} else {
			return fmt.Errorf("invalid type for apiPort")
		}
	case "downloadDir":
		if v, ok := value.(string); ok {
			if err := ValidateDownloadDir(v); err != nil {
				return err
			}
			cm.config.DownloadDir = v
		} else {
			return fmt.Errorf("invalid type for downloadDir")
		}
	case "maxConcurrent":
		if v, ok := value.(int); ok {
			cm.config.MaxConcurrent = v
		} else if v, ok := value.(float64); ok {
			cm.config.MaxConcurrent = int(v)
		} else {
			return fmt.Errorf("invalid type for maxConcurrent")
		}
	case "autoRetry":
		if v, ok := value.(bool); ok {
			cm.config.AutoRetry = v
		} else {
			return fmt.Errorf("invalid type for autoRetry")
		}
	case "maxRetryCount":
		if v, ok := value.(int); ok {
			cm.config.MaxRetryCount = v
		} else if v, ok := value.(float64); ok {
			cm.config.MaxRetryCount = int(v)
		} else {
			return fmt.Errorf("invalid type for maxRetryCount")
		}
	// Note: bilibiliSessData removed - now stored in secure credential storage
	case "minimizeToTray":
		if v, ok := value.(bool); ok {
			cm.config.MinimizeToTray = v
		} else {
			return fmt.Errorf("invalid type for minimizeToTray")
		}
	case "showNotification":
		if v, ok := value.(bool); ok {
			cm.config.ShowNotification = v
		} else {
			return fmt.Errorf("invalid type for showNotification")
		}
	case "firstRunComplete":
		if v, ok := value.(bool); ok {
			cm.config.FirstRunComplete = v
		} else {
			return fmt.Errorf("invalid type for firstRunComplete")
		}
	case "version":
		if v, ok := value.(string); ok {
			cm.config.Version = v
		} else {
			return fmt.Errorf("invalid type for version")
		}
	case "theme":
		if v, ok := value.(string); ok {
			if v != "dark" && v != "light" {
				return fmt.Errorf("invalid theme value: must be 'dark' or 'light'")
			}
			cm.config.Theme = v
		} else {
			return fmt.Errorf("invalid type for theme")
		}
	case "language":
		if v, ok := value.(string); ok {
			if v != "zh-CN" && v != "en-US" {
				return fmt.Errorf("invalid language value: must be 'zh-CN' or 'en-US'")
			}
			cm.config.Language = v
		} else {
			return fmt.Errorf("invalid type for language")
		}
	case "upstreamProxy":
		if v, ok := value.(string); ok {
			cm.config.UpstreamProxy = v
		} else {
			return fmt.Errorf("invalid type for upstreamProxy")
		}
	case "useUpstreamProxy":
		if v, ok := value.(bool); ok {
			cm.config.UseUpstreamProxy = v
		} else {
			return fmt.Errorf("invalid type for useUpstreamProxy")
		}
	case "proxyDebug":
		if v, ok := value.(bool); ok {
			cm.config.ProxyDebug = v
		} else {
			return fmt.Errorf("invalid type for proxyDebug")
		}
	case "ffmpegPath":
		if v, ok := value.(string); ok {
			cm.config.FFmpegPath = v
		} else {
			return fmt.Errorf("invalid type for ffmpegPath")
		}
	case "certInstalled":
		if v, ok := value.(bool); ok {
			cm.config.CertInstalled = v
		} else {
			return fmt.Errorf("invalid type for certInstalled")
		}
	default:
		return fmt.Errorf("unknown config key: %s", key)
	}

	return cm.saveInternal()
}

// Reset resets configuration to defaults
func (cm *ConfigManager) Reset() error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	cm.config = DefaultConfig()
	return cm.saveInternal()
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

	// Check if path exists
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			// Try to create the directory
			if err := os.MkdirAll(path, 0755); err != nil {
				return fmt.Errorf("directory does not exist and cannot be created: %w", err)
			}
			return nil
		}
		return fmt.Errorf("cannot access directory: %w", err)
	}

	// Check if it's a directory
	if !info.IsDir() {
		return fmt.Errorf("path is not a directory")
	}

	// Check if writable by creating a temp file
	testFile := filepath.Join(path, ".easydownload_write_test")
	f, err := os.Create(testFile)
	if err != nil {
		return fmt.Errorf("directory is not writable: %w", err)
	}
	f.Close()
	os.Remove(testFile)

	return nil
}

// SetDownloadDir sets the download directory with validation
func (cm *ConfigManager) SetDownloadDir(path string) error {
	if err := ValidateDownloadDir(path); err != nil {
		return err
	}
	return cm.Set("downloadDir", path)
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

	cm.config = cm.mergeWithDefaults(&importedConfig)
	return cm.saveInternal()
}
