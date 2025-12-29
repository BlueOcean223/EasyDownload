package credential

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"EasyDownload/internal/infra/logger"

	"github.com/zalando/go-keyring"
)

const (
	serviceName      = "EasyDownload"
	keyBiliSess      = "bilibili_sessdata"
	fallbackFileName = ".credentials.enc"
	fallbackDirPerm  = 0700 // Only owner can access
	fallbackFilePerm = 0600 // Only owner can read/write
)

var (
	// ErrKeyringUnavailable indicates system keyring is not available
	ErrKeyringUnavailable = errors.New("system keyring unavailable")
	// ErrInvalidCredential indicates credential format is invalid
	ErrInvalidCredential = errors.New("invalid credential format")

	// Global state for fallback mode
	fallbackMode     bool
	fallbackModeLock sync.RWMutex
)

// credentialStore represents the encrypted credential storage
type credentialStore struct {
	Credentials map[string]string `json:"credentials"`
}

// Store securely stores a credential to system keyring with fallback to encrypted file
func Store(key, value string) error {
	if key == "" || value == "" {
		return ErrInvalidCredential
	}

	// Try system keyring first
	err := keyring.Set(serviceName, key, value)
	if err == nil {
		logger.Debug("Credential [%s] stored securely in system keyring", key)
		return nil
	}

	// Keyring failed, use encrypted file fallback
	logger.Warn("System keyring unavailable (%v), using encrypted file fallback", err)
	setFallbackMode(true)

	if err := storeToEncryptedFile(key, value); err != nil {
		logger.Error("Failed to store credential to encrypted file: %v", err)
		return fmt.Errorf("credential storage failed: %w", err)
	}

	logger.Info("Credential [%s] stored securely in encrypted file", key)
	return nil
}

// Get retrieves a credential from system keyring with fallback to encrypted file
func Get(key string) (string, error) {
	if key == "" {
		return "", ErrInvalidCredential
	}

	// Check if we're in fallback mode
	if isFallbackMode() {
		return getFromEncryptedFile(key)
	}

	// Try system keyring first
	value, err := keyring.Get(serviceName, key)
	if err == nil {
		return value, nil
	}

	// If not found in keyring, it's expected
	if err == keyring.ErrNotFound {
		logger.Debug("Credential [%s] not found in keyring", key)
		// Try fallback file in case it was stored there
		if value, err := getFromEncryptedFile(key); err == nil && value != "" {
			return value, nil
		}
		return "", nil
	}

	// Keyring error, try fallback
	logger.Warn("Keyring error (%v), trying encrypted file fallback", err)
	setFallbackMode(true)
	return getFromEncryptedFile(key)
}

// Delete removes a credential from system keyring and encrypted file
func Delete(key string) error {
	if key == "" {
		return ErrInvalidCredential
	}

	var keyringErr, fileErr error

	// Try to delete from keyring
	keyringErr = keyring.Delete(serviceName, key)
	if keyringErr != nil && keyringErr != keyring.ErrNotFound {
		logger.Warn("Failed to delete from keyring: %v", keyringErr)
	}

	// Also try to delete from encrypted file
	fileErr = deleteFromEncryptedFile(key)
	if fileErr != nil {
		logger.Warn("Failed to delete from encrypted file: %v", fileErr)
	}

	// Success if deleted from at least one location
	if (keyringErr == nil || keyringErr == keyring.ErrNotFound) || fileErr == nil {
		logger.Debug("Credential [%s] deleted", key)
		return nil
	}

	return fmt.Errorf("failed to delete credential from all storages")
}

// StoreBilibiliSessData stores Bilibili SESSDATA securely
func StoreBilibiliSessData(sessData string) error {
	if sessData == "" {
		return ErrInvalidCredential
	}
	return Store(keyBiliSess, sessData)
}

// GetBilibiliSessData retrieves Bilibili SESSDATA from secure storage
func GetBilibiliSessData() (string, error) {
	return Get(keyBiliSess)
}

// DeleteBilibiliSessData removes Bilibili SESSDATA from secure storage
func DeleteBilibiliSessData() error {
	return Delete(keyBiliSess)
}

// HasBilibiliSessData checks if Bilibili SESSDATA exists (without exposing value)
func HasBilibiliSessData() bool {
	value, err := Get(keyBiliSess)
	return err == nil && value != ""
}

// Fallback mode management
func setFallbackMode(enabled bool) {
	fallbackModeLock.Lock()
	defer fallbackModeLock.Unlock()
	fallbackMode = enabled
}

func isFallbackMode() bool {
	fallbackModeLock.RLock()
	defer fallbackModeLock.RUnlock()
	return fallbackMode
}

// getFallbackFilePath returns the path to the encrypted credentials file
func getFallbackFilePath() (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to get home directory: %w", err)
	}

	credDir := filepath.Join(homeDir, ".easydownload")

	// Ensure directory exists with secure permissions
	if err := os.MkdirAll(credDir, fallbackDirPerm); err != nil {
		return "", fmt.Errorf("failed to create credential directory: %w", err)
	}

	return filepath.Join(credDir, fallbackFileName), nil
}

// deriveEncryptionKey derives an encryption key from machine-specific data
func deriveEncryptionKey() ([]byte, error) {
	// Use hostname as part of the key derivation
	hostname, err := os.Hostname()
	if err != nil {
		hostname = "default-host"
	}

	// Get user home directory as additional entropy
	homeDir, err := os.UserHomeDir()
	if err != nil {
		homeDir = "default-home"
	}

	// Combine machine-specific data
	keyMaterial := fmt.Sprintf("%s:%s:%s", serviceName, hostname, homeDir)

	// Derive 32-byte key using SHA-256
	hash := sha256.Sum256([]byte(keyMaterial))
	return hash[:], nil
}

// encrypt encrypts plaintext using AES-256-GCM
func encrypt(plaintext string) (string, error) {
	key, err := deriveEncryptionKey()
	if err != nil {
		return "", err
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}

	ciphertext := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.StdEncoding.EncodeToString(ciphertext), nil
}

// decrypt decrypts ciphertext using AES-256-GCM
func decrypt(ciphertext string) (string, error) {
	key, err := deriveEncryptionKey()
	if err != nil {
		return "", err
	}

	data, err := base64.StdEncoding.DecodeString(ciphertext)
	if err != nil {
		return "", err
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}

	nonceSize := gcm.NonceSize()
	if len(data) < nonceSize {
		return "", errors.New("ciphertext too short")
	}

	nonce, ciphertextBytes := data[:nonceSize], data[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, ciphertextBytes, nil)
	if err != nil {
		return "", err
	}

	return string(plaintext), nil
}

// loadCredentialStore loads the credential store from encrypted file
func loadCredentialStore() (*credentialStore, error) {
	filePath, err := getFallbackFilePath()
	if err != nil {
		return nil, err
	}

	// Check if file exists
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		return &credentialStore{Credentials: make(map[string]string)}, nil
	}

	// Read encrypted file
	encryptedData, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read credential file: %w", err)
	}

	// Decrypt
	decryptedData, err := decrypt(string(encryptedData))
	if err != nil {
		return nil, fmt.Errorf("failed to decrypt credentials: %w", err)
	}

	// Parse JSON
	var store credentialStore
	if err := json.Unmarshal([]byte(decryptedData), &store); err != nil {
		return nil, fmt.Errorf("failed to parse credentials: %w", err)
	}

	if store.Credentials == nil {
		store.Credentials = make(map[string]string)
	}

	return &store, nil
}

// saveCredentialStore saves the credential store to encrypted file
func saveCredentialStore(store *credentialStore) error {
	filePath, err := getFallbackFilePath()
	if err != nil {
		return err
	}

	// Marshal to JSON
	jsonData, err := json.Marshal(store)
	if err != nil {
		return fmt.Errorf("failed to marshal credentials: %w", err)
	}

	// Encrypt
	encryptedData, err := encrypt(string(jsonData))
	if err != nil {
		return fmt.Errorf("failed to encrypt credentials: %w", err)
	}

	// Write to file with secure permissions
	if err := os.WriteFile(filePath, []byte(encryptedData), fallbackFilePerm); err != nil {
		return fmt.Errorf("failed to write credential file: %w", err)
	}

	return nil
}

// storeToEncryptedFile stores a credential to encrypted file
func storeToEncryptedFile(key, value string) error {
	store, err := loadCredentialStore()
	if err != nil {
		return err
	}

	store.Credentials[key] = value

	return saveCredentialStore(store)
}

// getFromEncryptedFile retrieves a credential from encrypted file
func getFromEncryptedFile(key string) (string, error) {
	store, err := loadCredentialStore()
	if err != nil {
		// If file doesn't exist or can't be read, return empty
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}

	value, exists := store.Credentials[key]
	if !exists {
		return "", nil
	}

	return value, nil
}

// deleteFromEncryptedFile removes a credential from encrypted file
func deleteFromEncryptedFile(key string) error {
	store, err := loadCredentialStore()
	if err != nil {
		// If file doesn't exist, consider it deleted
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	delete(store.Credentials, key)

	return saveCredentialStore(store)
}
