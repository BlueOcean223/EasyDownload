package proxy

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"time"
)

// CertManager handles CA certificate generation and installation
type CertManager struct {
	CertDir  string
	CertPath string
	KeyPath  string
}

// NewCertManager creates a new CertManager instance
func NewCertManager(certDir string) *CertManager {
	return &CertManager{
		CertDir:  certDir,
		CertPath: filepath.Join(certDir, "ca.crt"),
		KeyPath:  filepath.Join(certDir, "ca.key"),
	}
}

// EnsureCertDir ensures the certificate directory exists
func (cm *CertManager) EnsureCertDir() error {
	return os.MkdirAll(cm.CertDir, 0755)
}

// CertExists checks if CA certificate already exists
func (cm *CertManager) CertExists() bool {
	_, certErr := os.Stat(cm.CertPath)
	_, keyErr := os.Stat(cm.KeyPath)
	return certErr == nil && keyErr == nil
}

// GenerateCACert generates a new CA certificate and private key
func (cm *CertManager) GenerateCACert() error {
	if err := cm.EnsureCertDir(); err != nil {
		return fmt.Errorf("failed to create cert directory: %w", err)
	}

	// Generate RSA private key
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return fmt.Errorf("failed to generate private key: %w", err)
	}

	// Create certificate template
	serialNumber, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return fmt.Errorf("failed to generate serial number: %w", err)
	}

	template := x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			Organization:  []string{"EasyDownload"},
			Country:       []string{"CN"},
			Province:      []string{""},
			Locality:      []string{""},
			CommonName:    "EasyDownload Root CA",
			StreetAddress: []string{""},
			PostalCode:    []string{""},
		},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().AddDate(10, 0, 0), // Valid for 10 years
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature | x509.KeyUsageCRLSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
	}

	// Create certificate
	certDER, err := x509.CreateCertificate(rand.Reader, &template, &template, &privateKey.PublicKey, privateKey)
	if err != nil {
		return fmt.Errorf("failed to create certificate: %w", err)
	}

	// Save certificate
	certFile, err := os.Create(cm.CertPath)
	if err != nil {
		return fmt.Errorf("failed to create cert file: %w", err)
	}
	defer certFile.Close()

	if err := pem.Encode(certFile, &pem.Block{Type: "CERTIFICATE", Bytes: certDER}); err != nil {
		return fmt.Errorf("failed to write cert file: %w", err)
	}

	// Save private key
	keyFile, err := os.Create(cm.KeyPath)
	if err != nil {
		return fmt.Errorf("failed to create key file: %w", err)
	}
	defer keyFile.Close()

	keyDER := x509.MarshalPKCS1PrivateKey(privateKey)
	if err := pem.Encode(keyFile, &pem.Block{Type: "RSA PRIVATE KEY", Bytes: keyDER}); err != nil {
		return fmt.Errorf("failed to write key file: %w", err)
	}

	return nil
}

// LoadCACert loads the CA certificate and private key
func (cm *CertManager) LoadCACert() (*x509.Certificate, *rsa.PrivateKey, error) {
	// Load certificate
	certPEM, err := os.ReadFile(cm.CertPath)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read cert file: %w", err)
	}

	certBlock, _ := pem.Decode(certPEM)
	if certBlock == nil {
		return nil, nil, fmt.Errorf("failed to decode cert PEM")
	}

	cert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to parse certificate: %w", err)
	}

	// Load private key
	keyPEM, err := os.ReadFile(cm.KeyPath)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read key file: %w", err)
	}

	keyBlock, _ := pem.Decode(keyPEM)
	if keyBlock == nil {
		return nil, nil, fmt.Errorf("failed to decode key PEM")
	}

	privateKey, err := x509.ParsePKCS1PrivateKey(keyBlock.Bytes)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to parse private key: %w", err)
	}

	return cert, privateKey, nil
}

// GetCertPath returns the certificate path
func (cm *CertManager) GetCertPath() string {
	return cm.CertPath
}

// GetKeyPath returns the key path
func (cm *CertManager) GetKeyPath() string {
	return cm.KeyPath
}
