//go:build !windows && !darwin

package proxy

// SystemProxy is a no-op implementation for non-Windows platforms.
type SystemProxy struct{}

// NewSystemProxy creates a new no-op SystemProxy instance.
func NewSystemProxy() *SystemProxy {
	return &SystemProxy{}
}

// Enable is a no-op on non-Windows platforms because this project currently
// only manages system proxy settings on Windows.
func (sp *SystemProxy) Enable(proxyAddress string) error {
	return nil
}

// Disable is a no-op on non-Windows platforms.
func (sp *SystemProxy) Disable() error {
	return nil
}

// IsEnabled always reports false on non-Windows platforms.
func (sp *SystemProxy) IsEnabled() bool {
	return false
}

// GetCurrentProxy is unsupported on non-Windows platforms.
func (sp *SystemProxy) GetCurrentProxy() string {
	return ""
}
