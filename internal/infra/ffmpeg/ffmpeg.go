package ffmpeg

import (
	"EasyDownload/internal/infra/logger"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
)

// EmbeddedFS is the interface for embedded file system
// This allows injection of the embedded FS from the assets package
type EmbeddedFS interface {
	ReadFile(name string) ([]byte, error)
}

// embeddedFFmpeg holds the embedded FFmpeg binary (to be set by SetEmbeddedFS)
var embeddedFFmpeg EmbeddedFS

// SetEmbeddedFS sets the embedded file system for FFmpeg extraction
func SetEmbeddedFS(efs fs.FS) {
	embeddedFFmpeg = &fsWrapper{efs}
}

// fsWrapper wraps fs.FS to implement EmbeddedFS
type fsWrapper struct {
	fs fs.FS
}

func (w *fsWrapper) ReadFile(name string) ([]byte, error) {
	return fs.ReadFile(w.fs, name)
}

// FFmpegManager manages FFmpeg binary extraction and execution
type FFmpegManager struct {
	ffmpegPath string
	embedded   bool
	extractDir string
	mu         sync.RWMutex
}

// NewFFmpegManager creates a new FFmpeg manager
func NewFFmpegManager() *FFmpegManager {
	return &FFmpegManager{}
}

// SetExtractDir sets the directory where embedded FFmpeg will be extracted
func (fm *FFmpegManager) SetExtractDir(dir string) {
	fm.mu.Lock()
	defer fm.mu.Unlock()
	fm.extractDir = dir
}

// GetPath returns the path to the FFmpeg executable
func (fm *FFmpegManager) GetPath() string {
	fm.mu.RLock()
	if fm.ffmpegPath != "" {
		defer fm.mu.RUnlock()
		return fm.ffmpegPath
	}
	fm.mu.RUnlock()

	// Try to find FFmpeg
	path := fm.findFFmpeg()
	if path != "" {
		fm.mu.Lock()
		fm.ffmpegPath = path
		fm.mu.Unlock()
	}
	return path
}

// SetPath manually sets the FFmpeg path
func (fm *FFmpegManager) SetPath(path string) {
	fm.mu.Lock()
	defer fm.mu.Unlock()
	fm.ffmpegPath = path
}

// IsAvailable checks if FFmpeg is available
func (fm *FFmpegManager) IsAvailable() bool {
	return fm.GetPath() != ""
}

// IsEmbedded returns whether the current FFmpeg is from embedded resources
func (fm *FFmpegManager) IsEmbedded() bool {
	fm.mu.RLock()
	defer fm.mu.RUnlock()
	return fm.embedded
}

// findFFmpeg searches for FFmpeg in common locations
func (fm *FFmpegManager) findFFmpeg() string {
	// First check if we have an extracted embedded version
	if fm.extractDir != "" {
		extractedPath := fm.getExtractedPath()
		if _, err := os.Stat(extractedPath); err == nil {
			if fm.verifyFFmpeg(extractedPath) {
				fm.mu.Lock()
				fm.embedded = true
				fm.mu.Unlock()
				return extractedPath
			}
		}
	}

	// Check common locations
	paths := []string{
		"ffmpeg",
	}

	if runtime.GOOS == "windows" {
		paths = append(paths,
			"ffmpeg.exe",
			filepath.Join(".", "ffmpeg.exe"),
			filepath.Join(".", "bin", "ffmpeg.exe"),
			`C:\ffmpeg\bin\ffmpeg.exe`,
			`C:\Program Files\ffmpeg\bin\ffmpeg.exe`,
		)
	} else {
		paths = append(paths,
			"/usr/bin/ffmpeg",
			"/usr/local/bin/ffmpeg",
			"/opt/homebrew/bin/ffmpeg",
		)
	}

	for _, p := range paths {
		if absPath, err := exec.LookPath(p); err == nil {
			return absPath
		}
	}

	return ""
}

// getExtractedPath returns the path where embedded FFmpeg should be extracted
func (fm *FFmpegManager) getExtractedPath() string {
	ffmpegName := "ffmpeg"
	if runtime.GOOS == "windows" {
		ffmpegName = "ffmpeg.exe"
	}
	return filepath.Join(fm.extractDir, ffmpegName)
}

// verifyFFmpeg checks if the FFmpeg binary exists and is valid
// First checks file existence, then tries to run it with full path
func (fm *FFmpegManager) verifyFFmpeg(path string) bool {
	// First check if file exists
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	// Check it's a file, not a directory, and has content
	if info.IsDir() || info.Size() == 0 {
		return false
	}

	// Try to run ffmpeg -version with full absolute path
	// exec.Command handles absolute paths correctly regardless of working directory
	cmd := exec.Command(path, "-version")
	applyNoWindow(cmd)
	// Set working directory to the directory containing ffmpeg
	cmd.Dir = filepath.Dir(path)
	err = cmd.Run()

	return err == nil
}

// ExtractEmbedded extracts the embedded FFmpeg binary to the extract directory
func (fm *FFmpegManager) ExtractEmbedded() error {
	if fm.extractDir == "" {
		return fmt.Errorf("extract directory not set")
	}

	if embeddedFFmpeg == nil {
		return fmt.Errorf("embedded FFmpeg FS not set")
	}

	// Determine the embedded file name based on OS
	embeddedName := "ffmpeg"
	if runtime.GOOS == "windows" {
		embeddedName = "ffmpeg.exe"
	}

	embeddedPath := "ffmpeg/" + embeddedName

	// Try to read from embedded FS
	data, err := embeddedFFmpeg.ReadFile(embeddedPath)
	if err != nil {
		logger.Debug("No embedded FFmpeg found: %v", err)
		return fmt.Errorf("embedded FFmpeg not available: %w", err)
	}

	// Ensure extract directory exists
	if err := os.MkdirAll(fm.extractDir, 0755); err != nil {
		return fmt.Errorf("failed to create extract directory: %w", err)
	}

	extractPath := fm.getExtractedPath()

	// Check if already extracted and valid
	if _, err := os.Stat(extractPath); err == nil {
		if fm.verifyFFmpeg(extractPath) {
			logger.Debug("Embedded FFmpeg already extracted and valid")
			fm.mu.Lock()
			fm.ffmpegPath = extractPath
			fm.embedded = true
			fm.mu.Unlock()
			return nil
		}
		// Remove invalid file
		os.Remove(extractPath)
	}

	// Write the embedded binary
	logger.Info("Extracting embedded FFmpeg to: %s", extractPath)
	if err := os.WriteFile(extractPath, data, 0755); err != nil {
		return fmt.Errorf("failed to write FFmpeg binary: %w", err)
	}

	// Verify the extracted binary
	if !fm.verifyFFmpeg(extractPath) {
		os.Remove(extractPath)
		return fmt.Errorf("extracted FFmpeg binary is not valid")
	}

	fm.mu.Lock()
	fm.ffmpegPath = extractPath
	fm.embedded = true
	fm.mu.Unlock()

	logger.Info("FFmpeg extracted successfully")
	return nil
}

// Merge merges video and audio files into a single output file
func (fm *FFmpegManager) Merge(videoPath, audioPath, outputPath string) error {
	ffmpegPath := fm.GetPath()
	if ffmpegPath == "" {
		return fmt.Errorf("FFmpeg not available")
	}

	// Ensure output directory exists
	outputDir := filepath.Dir(outputPath)
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("failed to create output directory: %w", err)
	}

	logger.Debug("Merging video and audio: %s + %s -> %s", videoPath, audioPath, outputPath)

	cmd := exec.Command(ffmpegPath,
		"-i", videoPath,
		"-i", audioPath,
		"-c", "copy",
		"-y",
		outputPath,
	)
	applyNoWindow(cmd)

	output, err := cmd.CombinedOutput()
	if err != nil {
		logger.Error("FFmpeg merge failed: %v, output: %s", err, string(output))
		return fmt.Errorf("FFmpeg merge failed: %w", err)
	}

	logger.Debug("Merge completed successfully")
	return nil
}

// MergeWithProgress merges video and audio files with progress callback
func (fm *FFmpegManager) MergeWithProgress(videoPath, audioPath, outputPath string, onProgress func(float64)) error {
	// For now, just call Merge since FFmpeg progress parsing is complex
	// Progress callback is called at start and end
	if onProgress != nil {
		onProgress(0)
	}

	err := fm.Merge(videoPath, audioPath, outputPath)

	if onProgress != nil && err == nil {
		onProgress(100)
	}

	return err
}

// ExtractAudio extracts audio from a video file
func (fm *FFmpegManager) ExtractAudio(inputPath, outputPath string) error {
	ffmpegPath := fm.GetPath()
	if ffmpegPath == "" {
		return fmt.Errorf("FFmpeg not available")
	}

	// Ensure output directory exists
	outputDir := filepath.Dir(outputPath)
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("failed to create output directory: %w", err)
	}

	logger.Debug("Extracting audio: %s -> %s", inputPath, outputPath)

	cmd := exec.Command(ffmpegPath,
		"-i", inputPath,
		"-vn",
		"-acodec", "copy",
		"-y",
		outputPath,
	)
	applyNoWindow(cmd)

	output, err := cmd.CombinedOutput()
	if err != nil {
		logger.Error("FFmpeg audio extraction failed: %v, output: %s", err, string(output))
		return fmt.Errorf("FFmpeg audio extraction failed: %w", err)
	}

	logger.Debug("Audio extraction completed successfully")
	return nil
}

// ConvertToMP3 converts an audio file to MP3 format
func (fm *FFmpegManager) ConvertToMP3(inputPath, outputPath string, bitrate string) error {
	ffmpegPath := fm.GetPath()
	if ffmpegPath == "" {
		return fmt.Errorf("FFmpeg not available")
	}

	if bitrate == "" {
		bitrate = "192k"
	}

	// Ensure output directory exists
	outputDir := filepath.Dir(outputPath)
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("failed to create output directory: %w", err)
	}

	logger.Debug("Converting to MP3: %s -> %s (bitrate: %s)", inputPath, outputPath, bitrate)

	cmd := exec.Command(ffmpegPath,
		"-i", inputPath,
		"-vn",
		"-acodec", "libmp3lame",
		"-ab", bitrate,
		"-y",
		outputPath,
	)
	applyNoWindow(cmd)

	output, err := cmd.CombinedOutput()
	if err != nil {
		logger.Error("FFmpeg MP3 conversion failed: %v, output: %s", err, string(output))
		return fmt.Errorf("FFmpeg MP3 conversion failed: %w", err)
	}

	logger.Debug("MP3 conversion completed successfully")
	return nil
}

// GetVersion returns the FFmpeg version string
func (fm *FFmpegManager) GetVersion() (string, error) {
	ffmpegPath := fm.GetPath()
	if ffmpegPath == "" {
		return "", fmt.Errorf("FFmpeg not available")
	}

	cmd := exec.Command(ffmpegPath, "-version")
	applyNoWindow(cmd)
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to get FFmpeg version: %w", err)
	}

	return string(output), nil
}

// CopyFile is a helper function to copy a file
func CopyFile(src, dst string) error {
	sourceFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer sourceFile.Close()

	// Ensure destination directory exists
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}

	destFile, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer destFile.Close()

	_, err = io.Copy(destFile, sourceFile)
	return err
}
