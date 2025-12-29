package logger

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/leanovate/gopter"
	"github.com/leanovate/gopter/gen"
	"github.com/leanovate/gopter/prop"
)

// **Feature: easydownload-improvements, Property 11: 日志文件大小限制**
// **Validates: Requirements 7.3**
// For any log write operation, a single log file size should not exceed
// the configured maximum size limit.
func TestLogFileSizeLimit(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("log file size never exceeds max size plus one message after rotation", prop.ForAll(
		func(messageCount int, messageLen int) bool {
			// Create temp directory for test
			tempDir, err := os.MkdirTemp("", "logger_test")
			if err != nil {
				return false
			}
			defer os.RemoveAll(tempDir)

			// Create logger with small max size for testing (1KB)
			// Use a size that's larger than any single message to ensure rotation works
			maxSize := int64(1024)
			logger := NewLogger(tempDir)
			logger.SetMaxSize(maxSize)
			logger.SetMaxBackups(3)

			if err := logger.Init(); err != nil {
				return false
			}
			defer logger.Close()

			// Generate message of specified length
			message := strings.Repeat("x", messageLen)

			// Calculate approximate single message size (timestamp + level + message + newline)
			// Format: "2025/12/11 11:26:48 [INFO] Test message X: xxx...\n"
			singleMessageSize := int64(len(message) + 100) // overhead for timestamp, level, etc.

			// Write multiple log messages
			for i := 0; i < messageCount; i++ {
				logger.Info("Test message %d: %s", i, message)
			}

			// Check that current log file size is within limit
			size, err := logger.GetCurrentFileSize()
			if err != nil {
				return false
			}

			// The file size should be less than maxSize + one message size
			// (because rotation happens after writing, the file may contain
			// up to one message beyond the limit before rotation occurs)
			return size <= maxSize+singleMessageSize
		},
		gen.IntRange(1, 50),   // number of messages
		gen.IntRange(10, 100), // message length
	))

	properties.Property("rotation creates backup files correctly", prop.ForAll(
		func(rotationCount int) bool {
			tempDir, err := os.MkdirTemp("", "logger_test")
			if err != nil {
				return false
			}
			defer os.RemoveAll(tempDir)

			logger := NewLogger(tempDir)
			logger.SetMaxSize(100) // Very small for quick rotation
			logger.SetMaxBackups(3)

			if err := logger.Init(); err != nil {
				return false
			}
			defer logger.Close()

			// Write enough to trigger rotations
			for i := 0; i < rotationCount; i++ {
				// Write a message that will trigger rotation
				logger.Info("This is a test message that should trigger rotation: %d", i)
			}

			// Count backup files
			entries, err := os.ReadDir(tempDir)
			if err != nil {
				return false
			}

			backupCount := 0
			for _, entry := range entries {
				if strings.HasPrefix(entry.Name(), "easydownload.log.") {
					backupCount++
				}
			}

			// Backup count should not exceed maxBackups
			return backupCount <= logger.maxBackups
		},
		gen.IntRange(1, 20),
	))

	properties.Property("log level filtering works correctly", prop.ForAll(
		func(level int) bool {
			tempDir, err := os.MkdirTemp("", "logger_test")
			if err != nil {
				return false
			}
			defer os.RemoveAll(tempDir)

			logger := NewLogger(tempDir)
			logger.SetMaxSize(10 * 1024) // 10KB

			// Set level based on input (0=Debug, 1=Info, 2=Error)
			logLevel := LogLevel(level % 3)
			logger.SetLevel(logLevel)

			if err := logger.Init(); err != nil {
				return false
			}
			defer logger.Close()

			// Write messages at all levels
			logger.Debug("Debug message")
			logger.Info("Info message")
			logger.Error("Error message")

			// Read log file content
			logPath := logger.GetLogPath()
			content, err := os.ReadFile(logPath)
			if err != nil {
				return false
			}

			contentStr := string(content)

			// Verify filtering based on level
			switch logLevel {
			case LevelDebug:
				// All messages should be present
				return strings.Contains(contentStr, "DEBUG") &&
					strings.Contains(contentStr, "INFO") &&
					strings.Contains(contentStr, "ERROR")
			case LevelInfo:
				// Debug should be filtered out
				return !strings.Contains(contentStr, "DEBUG") &&
					strings.Contains(contentStr, "INFO") &&
					strings.Contains(contentStr, "ERROR")
			case LevelError:
				// Only error should be present
				return !strings.Contains(contentStr, "DEBUG") &&
					!strings.Contains(contentStr, "INFO") &&
					strings.Contains(contentStr, "ERROR")
			}

			return true
		},
		gen.IntRange(0, 2),
	))

	properties.TestingRun(t)
}

// TestLoggerBasicFunctionality tests basic logger operations
func TestLoggerBasicFunctionality(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "logger_test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	logger := NewLogger(tempDir)
	if err := logger.Init(); err != nil {
		t.Fatalf("Failed to init logger: %v", err)
	}
	defer logger.Close()

	// Test basic logging
	logger.Info("Test info message")
	logger.Error("Test error message")
	logger.Debug("Test debug message")

	// Verify log file exists
	logPath := logger.GetLogPath()
	if _, err := os.Stat(logPath); os.IsNotExist(err) {
		t.Error("Log file was not created")
	}

	// Verify log directory
	if logger.GetLogDir() != tempDir {
		t.Errorf("Expected log dir %s, got %s", tempDir, logger.GetLogDir())
	}
}

// TestLoggerRotation tests log file rotation
func TestLoggerRotation(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "logger_test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	logger := NewLogger(tempDir)
	logger.SetMaxSize(100) // Very small for testing
	logger.SetMaxBackups(2)

	if err := logger.Init(); err != nil {
		t.Fatalf("Failed to init logger: %v", err)
	}
	defer logger.Close()

	// Write enough to trigger rotation
	for i := 0; i < 10; i++ {
		logger.Info("This is a test message number %d that should trigger rotation", i)
	}

	// Check that backup files exist
	logPath := logger.GetLogPath()
	backup1 := logPath + ".1"

	if _, err := os.Stat(backup1); os.IsNotExist(err) {
		t.Error("Backup file .1 was not created after rotation")
	}
}

// TestLoggerGetCurrentFileSize tests getting current file size
func TestLoggerGetCurrentFileSize(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "logger_test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	logger := NewLogger(tempDir)
	if err := logger.Init(); err != nil {
		t.Fatalf("Failed to init logger: %v", err)
	}
	defer logger.Close()

	// Get initial size
	initialSize, err := logger.GetCurrentFileSize()
	if err != nil {
		t.Fatalf("Failed to get initial file size: %v", err)
	}

	// Write some content
	logger.Info("Test message for size check")

	// Get new size
	newSize, err := logger.GetCurrentFileSize()
	if err != nil {
		t.Fatalf("Failed to get new file size: %v", err)
	}

	// Size should have increased
	if newSize <= initialSize {
		t.Errorf("File size did not increase after writing: initial=%d, new=%d", initialSize, newSize)
	}
}

// TestLoggerMaxSizeConfiguration tests max size configuration
func TestLoggerMaxSizeConfiguration(t *testing.T) {
	logger := NewLogger(".")

	// Test default max size
	if logger.GetMaxSize() != DefaultMaxSize {
		t.Errorf("Expected default max size %d, got %d", DefaultMaxSize, logger.GetMaxSize())
	}

	// Test setting max size
	logger.SetMaxSize(5 * 1024 * 1024) // 5MB
	if logger.GetMaxSize() != 5*1024*1024 {
		t.Errorf("Expected max size 5MB, got %d", logger.GetMaxSize())
	}

	// Test that invalid size (0 or negative) is ignored
	logger.SetMaxSize(0)
	if logger.GetMaxSize() != 5*1024*1024 {
		t.Error("SetMaxSize should ignore 0 value")
	}

	logger.SetMaxSize(-100)
	if logger.GetMaxSize() != 5*1024*1024 {
		t.Error("SetMaxSize should ignore negative value")
	}
}

// TestLoggerCleanOldLogs tests cleaning old log files
func TestLoggerCleanOldLogs(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "logger_test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Create some old log files with an old modification time
	oldLogPath := filepath.Join(tempDir, "old.log")
	os.WriteFile(oldLogPath, []byte("old log content"), 0644)

	// Set the modification time to 2 hours ago to ensure it's "old"
	oldTime := time.Now().Add(-2 * time.Hour)
	os.Chtimes(oldLogPath, oldTime, oldTime)

	logger := NewLogger(tempDir)
	if err := logger.Init(); err != nil {
		t.Fatalf("Failed to init logger: %v", err)
	}
	defer logger.Close()

	// Clean logs older than 1 hour (should clean the old.log file)
	logger.CleanOldLogs(1 * time.Hour)

	// Old log should be removed
	if _, err := os.Stat(oldLogPath); !os.IsNotExist(err) {
		t.Error("Old log file should have been removed")
	}

	// Current log should still exist
	if _, err := os.Stat(logger.GetLogPath()); os.IsNotExist(err) {
		t.Error("Current log file should not be removed")
	}
}
