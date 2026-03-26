//go:build !windows && !darwin

package proxy

import "fmt"

// IsCertInstalled reports whether the generated CA files exist locally.
// Non-Windows builds do not currently integrate with the OS trust store.
func (cm *CertManager) IsCertInstalled() bool {
	return cm.CertExists()
}

// InstallCert is not implemented outside Windows yet.
func (cm *CertManager) InstallCert() error {
	return fmt.Errorf("certificate installation is not supported on this platform yet")
}

// UninstallCert is not implemented outside Windows yet.
func (cm *CertManager) UninstallCert() error {
	return fmt.Errorf("certificate uninstallation is not supported on this platform yet")
}
