//go:build windows

package proxy

import (
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

// IsCertInstalled checks if the CA certificate is installed in the system trust store
// using Windows CryptoAPI. Checks both LocalMachine and CurrentUser stores.
func (cm *CertManager) IsCertInstalled() bool {
	// Load our certificate to get its data for comparison
	certPEM, err := os.ReadFile(cm.CertPath)
	if err != nil {
		return false
	}

	block, _ := pem.Decode(certPEM)
	if block == nil {
		return false
	}

	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return false
	}

	// Check LocalMachine store first (where we install certs with admin privileges)
	if cm.isCertInStore(cert, windows.CERT_SYSTEM_STORE_LOCAL_MACHINE) {
		return true
	}

	// Also check CurrentUser store as fallback
	return cm.isCertInStore(cert, windows.CERT_SYSTEM_STORE_CURRENT_USER)
}

// isCertInStore checks if a certificate is in a specific Windows certificate store
func (cm *CertManager) isCertInStore(cert *x509.Certificate, storeLocation uint32) bool {
	rootStorePtr, err := windows.UTF16PtrFromString("ROOT")
	if err != nil {
		return false
	}

	store, err := windows.CertOpenStore(
		windows.CERT_STORE_PROV_SYSTEM,
		0,
		0,
		storeLocation,
		uintptr(unsafe.Pointer(rootStorePtr)),
	)
	if err != nil {
		return false
	}
	defer windows.CertCloseStore(store, 0)

	// Search for our certificate by subject name
	subjectPtr, err := windows.UTF16PtrFromString(cert.Subject.CommonName)
	if err != nil {
		return false
	}

	// Find certificate by subject CN
	var prevContext *windows.CertContext
	for {
		certContext, err := windows.CertFindCertificateInStore(
			store,
			windows.X509_ASN_ENCODING|windows.PKCS_7_ASN_ENCODING,
			0,
			windows.CERT_FIND_SUBJECT_STR,
			unsafe.Pointer(subjectPtr),
			prevContext,
		)
		if err != nil || certContext == nil {
			break
		}
		prevContext = certContext

		// Compare the certificate data
		storedCertData := unsafe.Slice(certContext.EncodedCert, certContext.Length)
		if len(storedCertData) == len(cert.Raw) {
			match := true
			for i := range storedCertData {
				if storedCertData[i] != cert.Raw[i] {
					match = false
					break
				}
			}
			if match {
				windows.CertFreeCertificateContext(certContext)
				return true
			}
		}
	}

	return false
}

// InstallCert installs the CA certificate to the system trust store
// using Windows CryptoAPI. This requires administrator privileges.
func (cm *CertManager) InstallCert() error {
	// Read and parse the certificate
	certPEM, err := os.ReadFile(cm.CertPath)
	if err != nil {
		return fmt.Errorf("failed to read certificate file: %w", err)
	}

	block, _ := pem.Decode(certPEM)
	if block == nil {
		return fmt.Errorf("failed to parse certificate PEM")
	}

	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return fmt.Errorf("failed to parse certificate: %w", err)
	}

	// Open the LocalMachine Root certificate store
	rootStorePtr, err := windows.UTF16PtrFromString("ROOT")
	if err != nil {
		return fmt.Errorf("failed to create store name: %w", err)
	}

	store, err := windows.CertOpenStore(
		windows.CERT_STORE_PROV_SYSTEM,
		0,
		0,
		windows.CERT_SYSTEM_STORE_LOCAL_MACHINE,
		uintptr(unsafe.Pointer(rootStorePtr)),
	)
	if err != nil {
		return fmt.Errorf("failed to open certificate store (需要管理员权限): %w", err)
	}
	defer windows.CertCloseStore(store, 0)

	// Create a certificate context from the raw certificate data
	certContext, err := windows.CertCreateCertificateContext(
		windows.X509_ASN_ENCODING|windows.PKCS_7_ASN_ENCODING,
		&cert.Raw[0],
		uint32(len(cert.Raw)),
	)
	if err != nil {
		return fmt.Errorf("failed to create certificate context: %w", err)
	}
	defer windows.CertFreeCertificateContext(certContext)

	// Add the certificate to the store
	err = windows.CertAddCertificateContextToStore(
		store,
		certContext,
		windows.CERT_STORE_ADD_REPLACE_EXISTING,
		nil,
	)
	if err != nil {
		return fmt.Errorf("failed to add certificate to store (需要管理员权限): %w", err)
	}

	return nil
}

// UninstallCert removes the CA certificate from the system trust store
// using Windows CryptoAPI. This requires administrator privileges.
func (cm *CertManager) UninstallCert() error {
	// Read and parse the certificate to get its data
	certPEM, err := os.ReadFile(cm.CertPath)
	if err != nil {
		return fmt.Errorf("failed to read certificate file: %w", err)
	}

	block, _ := pem.Decode(certPEM)
	if block == nil {
		return fmt.Errorf("failed to parse certificate PEM")
	}

	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return fmt.Errorf("failed to parse certificate: %w", err)
	}

	// Open the LocalMachine Root certificate store
	rootStorePtr, err := windows.UTF16PtrFromString("ROOT")
	if err != nil {
		return fmt.Errorf("failed to create store name: %w", err)
	}

	store, err := windows.CertOpenStore(
		windows.CERT_STORE_PROV_SYSTEM,
		0,
		0,
		windows.CERT_SYSTEM_STORE_LOCAL_MACHINE,
		uintptr(unsafe.Pointer(rootStorePtr)),
	)
	if err != nil {
		return fmt.Errorf("failed to open certificate store (需要管理员权限): %w", err)
	}
	defer windows.CertCloseStore(store, 0)

	// Search for our certificate by subject name
	subjectPtr, err := windows.UTF16PtrFromString(cert.Subject.CommonName)
	if err != nil {
		return fmt.Errorf("failed to create subject name: %w", err)
	}

	// Find and delete all matching certificates
	deleted := false
	var prevContext *windows.CertContext
	for {
		certContext, err := windows.CertFindCertificateInStore(
			store,
			windows.X509_ASN_ENCODING|windows.PKCS_7_ASN_ENCODING,
			0,
			windows.CERT_FIND_SUBJECT_STR,
			unsafe.Pointer(subjectPtr),
			prevContext,
		)
		if err != nil || certContext == nil {
			break
		}

		// Duplicate the context before deleting (CertDeleteCertificateFromStore frees the context)
		dupContext := windows.CertDuplicateCertificateContext(certContext)
		if dupContext == nil {
			prevContext = certContext
			continue
		}

		// Delete the certificate from store
		err = windows.CertDeleteCertificateFromStore(dupContext)
		if err == nil {
			deleted = true
		}

		// Don't update prevContext since we deleted this one
		// The next search will start fresh
		prevContext = nil
	}

	if !deleted {
		return fmt.Errorf("certificate not found in store")
	}

	return nil
}
