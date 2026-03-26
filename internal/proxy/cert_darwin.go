//go:build darwin

package proxy

import (
	"crypto/sha1"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"os"
	"strings"
)

// IsCertInstalled checks whether the generated certificate is trusted by macOS.
func (cm *CertManager) IsCertInstalled() bool {
	cert, err := cm.loadCertificate()
	if err != nil {
		return false
	}

	output, err := runDarwinCommand("/usr/bin/security", "find-certificate", "-a", "-Z", "-c", cert.Subject.CommonName)
	if err != nil {
		return false
	}

	sha256Sum := sha256.Sum256(cert.Raw)
	sha1Sum := sha1.Sum(cert.Raw)
	sha256Hex := strings.ToUpper(hex.EncodeToString(sha256Sum[:]))
	sha1Hex := strings.ToUpper(hex.EncodeToString(sha1Sum[:]))
	upperOutput := strings.ToUpper(output)

	return strings.Contains(upperOutput, sha256Hex) || strings.Contains(upperOutput, sha1Hex)
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
