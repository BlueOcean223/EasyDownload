//go:build darwin

package proxy

import (
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
)

// IsCertInstalled checks whether the generated certificate is trusted by macOS.
func (cm *CertManager) IsCertInstalled() bool {
	if _, err := cm.loadCertificate(); err != nil {
		return false
	}

	command := buildDarwinCertTrustCommand(cm.CertPath)
	_, err := runDarwinCommand(command[0], command[1:]...)
	return err == nil
}

func buildDarwinCertTrustCommand(certPath string) []string {
	return []string{"/usr/bin/security", "verify-cert", "-c", certPath, "-p", "basic", "-l", "-L", "-q"}
}

// InstallCert installs the CA certificate into the macOS System keychain.
func (cm *CertManager) InstallCert() error {
	if cm.IsCertInstalled() {
		return nil
	}
	if !cm.CertExists() {
		return fmt.Errorf("certificate file does not exist")
	}

	return runDarwinPrivilegedCommands(
		[]string{
			"/usr/bin/security",
			"add-trusted-cert",
			"-d",
			"-r",
			"trustRoot",
			"-k",
			"/Library/Keychains/System.keychain",
			cm.CertPath,
		},
	)
}

// UninstallCert removes the CA certificate from the macOS System keychain.
func (cm *CertManager) UninstallCert() error {
	if !cm.CertExists() || !cm.IsCertInstalled() {
		return nil
	}

	return runDarwinPrivilegedCommands(
		[]string{
			"/usr/bin/security",
			"remove-trusted-cert",
			"-d",
			cm.CertPath,
		},
	)
}

func (cm *CertManager) loadCertificate() (*x509.Certificate, error) {
	certPEM, err := os.ReadFile(cm.CertPath)
	if err != nil {
		return nil, err
	}

	block, _ := pem.Decode(certPEM)
	if block == nil {
		return nil, fmt.Errorf("failed to decode certificate PEM")
	}

	return x509.ParseCertificate(block.Bytes)
}
