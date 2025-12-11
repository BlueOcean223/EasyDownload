package logger

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// LogLevel represents the severity of a log message
type LogLevel int

const (
	LevelDebug LogLevel = iota
	LevelInfo
	LevelError
)

// DefaultMaxSize is the default maximum log file size (10MB)
const DefaultMaxSize int64 = 10 * 1024 * 1024

// DefaultMaxBackups is the default number of backup files to keep
const DefaultMaxBackups int = 5

// Logger manages application logging with file rotation
type Logger struct {
	logDir     string
	logFile    *os.File
	logger     *log.Logger
	maxSize    int64
	maxBackups int
	level      LogLevel
	mu         sync.Mutex
}

// NewLogger creates a new Logger instance
func NewLogger(logDir string) *Logger {
	return &Logger{
		logDir:     logDir,
		maxSize:    DefaultMaxSize,
		maxBackups: DefaultMaxBackups,
		level:      LevelInfo,
	}
}

// SetMaxSize sets the maximum log file size in bytes
func (l *Logger) SetMaxSize(size int64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if size > 0 {
		l.maxSize = size
	}
}

// GetMaxSize returns the maximum log file size in bytes
func (l *Logger) GetMaxSize() int64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.maxSize
}

// SetMaxBackups sets the maximum number of backup files to keep
func (l *Logger) SetMaxBackups(count int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if count >= 0 {
		l.maxBackups = count
	}
}

// SetLevel sets the minimum log level
func (l *Logger) SetLevel(level LogLevel) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.level = level
}

// Init initializes the logger and opens the log file
func (l *Logger) Init() error {
	l.mu.Lock()
	defer l.mu.Unlock()

	// Ensure log directory exists
	if err := os.MkdirAll(l.logDir, 0755); err != nil {
		return fmt.Errorf("failed to create log directory: %w", err)
	}

	// Open or create log file
	logPath := l.getLogPath()
	file, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("failed to open log file: %w", err)
	}

	l.logFile = file
	l.logger = log.New(io.MultiWriter(file, os.Stdout), "", log.LstdFlags)

	return nil
}

// Close closes the log file
func (l *Logger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.logFile != nil {
		err := l.logFile.Close()
		l.logFile = nil
		l.logger = nil
		return err
	}
	return nil
}

// getLogPath returns the current log file path
func (l *Logger) getLogPath() string {
	return filepath.Join(l.logDir, "easydownload.log")
}

// GetLogPath returns the current log file path (public)
func (l *Logger) GetLogPath() string {
	return l.getLogPath()
}

// GetLogDir returns the log directory
func (l *Logger) GetLogDir() string {
	return l.logDir
}

// Info logs an informational message
func (l *Logger) Info(format string, args ...interface{}) {
	l.log(LevelInfo, "INFO", format, args...)
}

// Error logs an error message
func (l *Logger) Error(format string, args ...interface{}) {
	l.log(LevelError, "ERROR", format, args...)
}

// Debug logs a debug message
func (l *Logger) Debug(format string, args ...interface{}) {
	l.log(LevelDebug, "DEBUG", format, args...)
}

// log writes a log message with the specified level
func (l *Logger) log(level LogLevel, levelStr, format string, args ...interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()

	// Check if this level should be logged
	if level < l.level {
		return
	}

	// Check if rotation is needed before writing
	if err := l.checkRotation(); err != nil {
		// Log rotation failed, but continue logging
		fmt.Fprintf(os.Stderr, "Log rotation failed: %v\n", err)
	}

	message := fmt.Sprintf(format, args...)
	logLine := fmt.Sprintf("[%s] %s", levelStr, message)

	if l.logger != nil {
		l.logger.Println(logLine)
	} else {
		// Fallback to stdout if logger not initialized
		log.Println(logLine)
	}
}

// checkRotation checks if log rotation is needed and performs it
func (l *Logger) checkRotation() error {
	if l.logFile == nil {
		return nil
	}

	info, err := l.logFile.Stat()
	if err != nil {
		return err
	}

	if info.Size() >= l.maxSize {
		return l.rotateInternal()
	}

	return nil
}

// Rotate performs log file rotation
func (l *Logger) Rotate() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.rotateInternal()
}

// rotateInternal performs the actual rotation (must be called with lock held)
func (l *Logger) rotateInternal() error {
	if l.logFile == nil {
		return nil
	}

	// Close current log file
	l.logFile.Close()

	logPath := l.getLogPath()

	// Rotate existing backup files
	for i := l.maxBackups - 1; i >= 1; i-- {
		oldPath := fmt.Sprintf("%s.%d", logPath, i)
		newPath := fmt.Sprintf("%s.%d", logPath, i+1)
		os.Rename(oldPath, newPath)
	}

	// Rename current log to .1
	if err := os.Rename(logPath, logPath+".1"); err != nil && !os.IsNotExist(err) {
		// If rename fails, try to continue anyway
		fmt.Fprintf(os.Stderr, "Failed to rename log file: %v\n", err)
	}

	// Remove oldest backup if it exceeds maxBackups
	oldestBackup := fmt.Sprintf("%s.%d", logPath, l.maxBackups+1)
	os.Remove(oldestBackup)

	// Open new log file
	file, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("failed to create new log file: %w", err)
	}

	l.logFile = file
	l.logger = log.New(io.MultiWriter(file, os.Stdout), "", log.LstdFlags)

	return nil
}

// GetCurrentFileSize returns the current log file size in bytes
func (l *Logger) GetCurrentFileSize() (int64, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.logFile == nil {
		return 0, fmt.Errorf("log file not initialized")
	}

	info, err := l.logFile.Stat()
	if err != nil {
		return 0, err
	}

	return info.Size(), nil
}

// CleanOldLogs removes log files older than the specified duration
func (l *Logger) CleanOldLogs(maxAge time.Duration) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	entries, err := os.ReadDir(l.logDir)
	if err != nil {
		return fmt.Errorf("failed to read log directory: %w", err)
	}

	cutoff := time.Now().Add(-maxAge)

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		info, err := entry.Info()
		if err != nil {
			continue
		}

		if info.ModTime().Before(cutoff) {
			logPath := filepath.Join(l.logDir, entry.Name())
			// Don't delete the current log file
			if logPath != l.getLogPath() {
				os.Remove(logPath)
			}
		}
	}

	return nil
}

// Global logger instance
var globalLogger *Logger
var globalLoggerOnce sync.Once

// GetGlobalLogger returns the global logger instance
func GetGlobalLogger() *Logger {
	globalLoggerOnce.Do(func() {
		homeDir, _ := os.UserHomeDir()
		logDir := filepath.Join(homeDir, ".easydownload", "logs")
		globalLogger = NewLogger(logDir)
		globalLogger.Init()
	})
	return globalLogger
}

// SetGlobalLogger sets the global logger instance
func SetGlobalLogger(logger *Logger) {
	globalLogger = logger
}

// Info logs an informational message using the global logger
func Info(format string, args ...interface{}) {
	GetGlobalLogger().Info(format, args...)
}

// Error logs an error message using the global logger
func Error(format string, args ...interface{}) {
	GetGlobalLogger().Error(format, args...)
}

// Debug logs a debug message using the global logger
func Debug(format string, args ...interface{}) {
	GetGlobalLogger().Debug(format, args...)
}
