//go:build darwin

package proxy

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"al.essio.dev/pkg/shellescape"
)

func runDarwinCommand(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	output, err := cmd.CombinedOutput()
	trimmed := strings.TrimSpace(string(output))
	if err != nil {
		if trimmed == "" {
			return "", err
		}
		return "", fmt.Errorf("%w: %s", err, trimmed)
	}
	return trimmed, nil
}

func runDarwinPrivilegedCommands(commands ...[]string) error {
	lines := make([]string, 0, len(commands))
	for _, command := range commands {
		if len(command) == 0 {
			continue
		}
		lines = append(lines, shellescape.QuoteCommand(command))
	}
	if len(lines) == 0 {
		return nil
	}
	return runDarwinPrivilegedShell(strings.Join(lines, " ; "))
}

func runDarwinPrivilegedShell(script string) error {
	script = strings.TrimSpace(script)
	if script == "" {
		return nil
	}

	if os.Geteuid() == 0 {
		cmd := exec.Command("/bin/sh", "-c", script)
		output, err := cmd.CombinedOutput()
		if err != nil {
			trimmed := strings.TrimSpace(string(output))
			if trimmed == "" {
				return err
			}
			return fmt.Errorf("%w: %s", err, trimmed)
		}
		return nil
	}

	appleScript := fmt.Sprintf(`do shell script "%s" with administrator privileges`, escapeAppleScriptString(script))
	cmd := exec.Command("/usr/bin/osascript", "-e", appleScript)
	output, err := cmd.CombinedOutput()
	if err != nil {
		trimmed := strings.TrimSpace(string(output))
		if strings.Contains(strings.ToLower(trimmed), "user canceled") {
			return fmt.Errorf("administrator authorization was canceled")
		}
		if trimmed == "" {
			return fmt.Errorf("administrator authorization failed: %w", err)
		}
		return fmt.Errorf("administrator authorization failed: %s", trimmed)
	}
	return nil
}

func escapeAppleScriptString(s string) string {
	replacer := strings.NewReplacer(
		"\\", "\\\\",
		"\"", "\\\"",
	)
	return replacer.Replace(s)
}
