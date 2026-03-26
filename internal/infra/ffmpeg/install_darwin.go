//go:build darwin

package ffmpeg

import (
	"archive/zip"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"EasyDownload/internal/infra/logger"
)

const evermeetReleaseZipURL = "https://evermeet.cx/ffmpeg/getrelease/zip"

func (fm *FFmpegManager) installPlatformFFmpeg() (string, error) {
	if path := fm.GetPath(); path != "" {
		return path, nil
	}

	if brew := findHomebrew(); brew != "" {
		if err := installFFmpegWithHomebrew(brew); err != nil {
			return "", err
		}
		fm.SetPath("")
		if path := fm.GetPath(); path != "" {
			return path, nil
		}
		return "", fmt.Errorf("ffmpeg was installed with Homebrew, but the executable was not found afterwards")
	}

	if runtime.GOARCH == "amd64" {
		return fm.installFFmpegFromEvermeet()
	}

	return "", fmt.Errorf("automatic FFmpeg installation on Apple Silicon requires Homebrew; brew was not found")
}

func findHomebrew() string {
	if path, err := exec.LookPath("brew"); err == nil {
		return path
	}

	for _, candidate := range []string{"/opt/homebrew/bin/brew", "/usr/local/bin/brew"} {
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}

	return ""
}

func installFFmpegWithHomebrew(brewPath string) error {
	cmd := exec.Command(brewPath, "install", "ffmpeg")
	cmd.Env = append(os.Environ(), "HOMEBREW_NO_AUTO_UPDATE=1")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to install ffmpeg via Homebrew: %s", strings.TrimSpace(string(output)))
	}
	return nil
}

func (fm *FFmpegManager) installFFmpegFromEvermeet() (string, error) {
	if fm.extractDir == "" {
		return "", fmt.Errorf("extract directory not set")
	}
	if err := os.MkdirAll(fm.extractDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create ffmpeg directory: %w", err)
	}

	tmpDir, err := os.MkdirTemp(fm.extractDir, "download-*")
	if err != nil {
		return "", fmt.Errorf("failed to create temporary ffmpeg directory: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	archivePath := filepath.Join(tmpDir, "ffmpeg.zip")
	if err := downloadFile(archivePath, evermeetReleaseZipURL); err != nil {
		return "", err
	}

	targetPath := fm.getExtractedPath()
	if err := extractZipBinary(archivePath, "ffmpeg", targetPath); err != nil {
		return "", err
	}
	_ = exec.Command("/usr/bin/xattr", "-dr", "com.apple.quarantine", targetPath).Run()

	if !fm.verifyFFmpeg(targetPath) {
		_ = os.Remove(targetPath)
		return "", fmt.Errorf("downloaded ffmpeg is not usable")
	}

	fm.mu.Lock()
	fm.ffmpegPath = targetPath
	fm.embedded = false
	fm.mu.Unlock()

	logger.Info("FFmpeg installed from evermeet to: %s", targetPath)
	return targetPath, nil
}

func downloadFile(targetPath string, rawURL string) error {
	client := &http.Client{Timeout: 2 * time.Minute}
	resp, err := client.Get(rawURL)
	if err != nil {
		return fmt.Errorf("failed to download ffmpeg: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("failed to download ffmpeg: unexpected status %s", resp.Status)
	}

	file, err := os.Create(targetPath)
	if err != nil {
		return fmt.Errorf("failed to create temporary ffmpeg archive: %w", err)
	}
	defer file.Close()

	if _, err := io.Copy(file, resp.Body); err != nil {
		return fmt.Errorf("failed to save ffmpeg archive: %w", err)
	}
	return nil
}

func extractZipBinary(archivePath string, entryName string, targetPath string) error {
	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		return fmt.Errorf("failed to open ffmpeg archive: %w", err)
	}
	defer reader.Close()

	for _, file := range reader.File {
		if filepath.Base(file.Name) != entryName {
			continue
		}

		rc, err := file.Open()
		if err != nil {
			return fmt.Errorf("failed to read ffmpeg archive entry: %w", err)
		}

		out, err := os.OpenFile(targetPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
		if err != nil {
			rc.Close()
			return fmt.Errorf("failed to create ffmpeg binary: %w", err)
		}

		_, copyErr := io.Copy(out, rc)
		closeErr := out.Close()
		rcErr := rc.Close()
		if copyErr != nil {
			return fmt.Errorf("failed to extract ffmpeg binary: %w", copyErr)
		}
		if closeErr != nil {
			return fmt.Errorf("failed to finalize ffmpeg binary: %w", closeErr)
		}
		if rcErr != nil {
			return fmt.Errorf("failed to close ffmpeg archive entry: %w", rcErr)
		}
		return nil
	}

	return fmt.Errorf("ffmpeg binary not found in downloaded archive")
}
