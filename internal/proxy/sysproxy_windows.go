//go:build windows

package proxy

import (
	"fmt"
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows/registry"
)

// SystemProxy manages Windows system proxy settings
type SystemProxy struct {
	originalEnabled  uint64
	originalServer   string
	originalOverride string
	isSet            bool
}

// NewSystemProxy creates a new SystemProxy instance
func NewSystemProxy() *SystemProxy {
	return &SystemProxy{}
}

// Enable enables the system proxy with the given address
func (sp *SystemProxy) Enable(proxyAddress string) error {
	// Open the Internet Settings registry key
	key, err := registry.OpenKey(registry.CURRENT_USER,
		`Software\Microsoft\Windows\CurrentVersion\Internet Settings`,
		registry.ALL_ACCESS)
	if err != nil {
		return fmt.Errorf("failed to open registry: %w", err)
	}
	defer key.Close()

	// Save original settings
	sp.originalEnabled, _, _ = key.GetIntegerValue("ProxyEnable")
	sp.originalServer, _, _ = key.GetStringValue("ProxyServer")
	sp.originalOverride, _, _ = key.GetStringValue("ProxyOverride")

	// Set new proxy settings
	if err := key.SetDWordValue("ProxyEnable", 1); err != nil {
		return fmt.Errorf("failed to enable proxy: %w", err)
	}

	if err := key.SetStringValue("ProxyServer", proxyAddress); err != nil {
		return fmt.Errorf("failed to set proxy server: %w", err)
	}

	// Set bypass list (localhost should not go through proxy)
	if err := key.SetStringValue("ProxyOverride", "localhost;127.*;10.*;172.16.*;172.17.*;172.18.*;172.19.*;172.20.*;172.21.*;172.22.*;172.23.*;172.24.*;172.25.*;172.26.*;172.27.*;172.28.*;172.29.*;172.30.*;172.31.*;192.168.*;<local>"); err != nil {
		return fmt.Errorf("failed to set proxy override: %w", err)
	}

	sp.isSet = true

	// Notify the system of the change
	sp.notifyProxyChange()

	return nil
}

// Disable disables the system proxy and restores original settings
func (sp *SystemProxy) Disable() error {
	key, err := registry.OpenKey(registry.CURRENT_USER,
		`Software\Microsoft\Windows\CurrentVersion\Internet Settings`,
		registry.ALL_ACCESS)
	if err != nil {
		return fmt.Errorf("failed to open registry: %w", err)
	}
	defer key.Close()

	// Restore original settings
	if sp.isSet {
		if err := key.SetDWordValue("ProxyEnable", uint32(sp.originalEnabled)); err != nil {
			return fmt.Errorf("failed to restore proxy enable: %w", err)
		}

		if sp.originalServer != "" {
			if err := key.SetStringValue("ProxyServer", sp.originalServer); err != nil {
				return fmt.Errorf("failed to restore proxy server: %w", err)
			}
		}

		if sp.originalOverride != "" {
			if err := key.SetStringValue("ProxyOverride", sp.originalOverride); err != nil {
				return fmt.Errorf("failed to restore proxy override: %w", err)
			}
		}

		sp.isSet = false
	} else {
		// Just disable proxy
		if err := key.SetDWordValue("ProxyEnable", 0); err != nil {
			return fmt.Errorf("failed to disable proxy: %w", err)
		}
	}

	// Notify the system of the change
	sp.notifyProxyChange()

	return nil
}

// IsEnabled checks if system proxy is currently enabled
func (sp *SystemProxy) IsEnabled() bool {
	key, err := registry.OpenKey(registry.CURRENT_USER,
		`Software\Microsoft\Windows\CurrentVersion\Internet Settings`,
		registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer key.Close()

	enabled, _, err := key.GetIntegerValue("ProxyEnable")
	if err != nil {
		return false
	}

	return enabled == 1
}

// GetCurrentProxy returns the current system proxy address
func (sp *SystemProxy) GetCurrentProxy() string {
	key, err := registry.OpenKey(registry.CURRENT_USER,
		`Software\Microsoft\Windows\CurrentVersion\Internet Settings`,
		registry.QUERY_VALUE)
	if err != nil {
		return ""
	}
	defer key.Close()

	server, _, _ := key.GetStringValue("ProxyServer")
	return server
}

// notifyProxyChange notifies Windows of proxy setting changes
func (sp *SystemProxy) notifyProxyChange() {
	// Use netsh to refresh proxy settings
	cmd := exec.Command("cmd", "/c", "netsh", "winhttp", "import", "proxy", "source=ie")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	cmd.Run()

	// Alternative: Use InternetSetOption API through a helper
	// This is a simplified approach; in production, you might want to use
	// the Windows API directly through syscall
}

