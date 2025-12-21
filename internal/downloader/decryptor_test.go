package downloader

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/leanovate/gopter"
	"github.com/leanovate/gopter/gen"
	"github.com/leanovate/gopter/prop"
)

// **Feature: video-capture-fix, Property 3: XOR 解密往返**
// **Validates: Requirements 2.2**
// For any byte array and decryption key, encrypting then decrypting with XOR
// should return the original data (XOR(XOR(data, key), key) == data)
func TestXORDecryptRoundTrip(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("XOR decrypt round trip returns original data", prop.ForAll(
		func(data []byte, keyVal uint64) bool {
			// Skip empty data
			if len(data) == 0 {
				return true
			}

			// Make a copy of original data
			original := make([]byte, len(data))
			copy(original, data)

			// Determine encryption length (use full data length for test)
			encLen := uint32(len(data))

			// First XOR operation (encrypt)
			DecryptData(data, encLen, keyVal)

			// Second XOR operation (decrypt) - should restore original
			DecryptData(data, encLen, keyVal)

			// Verify data matches original
			return bytes.Equal(data, original)
		},
		gen.SliceOf(gen.UInt8()),
		gen.UInt64(),
	))

	properties.Property("XOR decrypt with partial length preserves unencrypted portion", prop.ForAll(
		func(data []byte, keyVal uint64, encLenRatio float64) bool {
			// Skip data that's too small
			if len(data) < 16 {
				return true
			}

			// Calculate encryption length as a portion of data
			encLen := uint32(float64(len(data)) * encLenRatio)
			if encLen < 8 {
				encLen = 8
			}
			if encLen > uint32(len(data)) {
				encLen = uint32(len(data))
			}

			// Make a copy of original data
			original := make([]byte, len(data))
			copy(original, data)

			// First XOR operation (encrypt)
			DecryptData(data, encLen, keyVal)

			// Verify unencrypted portion is unchanged
			if encLen < uint32(len(data)) {
				if !bytes.Equal(data[encLen:], original[encLen:]) {
					return false
				}
			}

			// Second XOR operation (decrypt)
			DecryptData(data, encLen, keyVal)

			// Verify full data matches original
			return bytes.Equal(data, original)
		},
		gen.SliceOfN(100, gen.UInt8()),
		gen.UInt64(),
		gen.Float64Range(0.1, 0.9),
	))

	properties.TestingRun(t)
}

// TestIsEncrypted tests the IsEncrypted method
func TestIsEncrypted(t *testing.T) {
	vd := NewVideoDecryptor()

	tests := []struct {
		decodeKey string
		expected  bool
	}{
		{"", false},
		{"somekey", true},
		{"YWJjZGVmZ2g=", true}, // base64 encoded "abcdefgh"
	}

	for _, tt := range tests {
		result := vd.IsEncrypted(tt.decodeKey)
		if result != tt.expected {
			t.Errorf("IsEncrypted(%q) = %v, want %v", tt.decodeKey, result, tt.expected)
		}
	}
}

// TestDecryptFileWithValidKey tests file decryption with a valid key
func TestDecryptFileWithValidKey(t *testing.T) {
	vd := NewVideoDecryptor()

	// Create a temporary file with test data
	tempDir := t.TempDir()
	testFile := filepath.Join(tempDir, "test_video.mp4")

	// Create a small valid MP4 container (ftyp + moov boxes) as plaintext.
	ftypBox := make([]byte, 24)
	binary.BigEndian.PutUint32(ftypBox[0:4], uint32(len(ftypBox)))
	copy(ftypBox[4:8], []byte("ftyp"))
	copy(ftypBox[8:12], []byte("isom"))
	binary.BigEndian.PutUint32(ftypBox[12:16], 0)
	copy(ftypBox[16:20], []byte("isom"))
	copy(ftypBox[20:24], []byte("iso2"))

	moovBox := make([]byte, 16)
	binary.BigEndian.PutUint32(moovBox[0:4], uint32(len(moovBox)))
	copy(moovBox[4:8], []byte("moov"))

	testData := append(ftypBox, moovBox...)

	// Save original for comparison
	original := make([]byte, len(testData))
	copy(original, testData)

	// Use a known key (WeChat decodeKey is a numeric string / uint64 seed)
	decodeKey := "1"
	encKey, err := ParseDecodeKey(decodeKey)
	if err != nil {
		t.Fatalf("Failed to parse decodeKey: %v", err)
	}

	encrypted := make([]byte, len(testData))
	copy(encrypted, testData)
	DecryptData(encrypted, uint32(len(encrypted)), encKey)

	// Write encrypted test data to file
	if err := os.WriteFile(testFile, encrypted, 0644); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	// Decrypt the file
	if err := vd.DecryptFile(testFile, decodeKey); err != nil {
		t.Fatalf("DecryptFile failed: %v", err)
	}

	// Read the decrypted file
	decrypted, err := os.ReadFile(testFile)
	if err != nil {
		t.Fatalf("Failed to read decrypted file: %v", err)
	}

	// Should match original
	if !bytes.Equal(decrypted, original) {
		t.Error("Decryption did not restore original data")
	}
}

// TestDecryptFileWithEmptyKey tests that empty key skips decryption
func TestDecryptFileWithEmptyKey(t *testing.T) {
	vd := NewVideoDecryptor()

	tempDir := t.TempDir()
	testFile := filepath.Join(tempDir, "test_video.mp4")

	testData := []byte("test video data")
	if err := os.WriteFile(testFile, testData, 0644); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	// Decrypt with empty key should be a no-op
	if err := vd.DecryptFile(testFile, ""); err != nil {
		t.Fatalf("DecryptFile with empty key failed: %v", err)
	}

	// File should be unchanged
	result, err := os.ReadFile(testFile)
	if err != nil {
		t.Fatalf("Failed to read file: %v", err)
	}

	if !bytes.Equal(result, testData) {
		t.Error("File was modified when decodeKey was empty")
	}
}

// TestDecryptFileWithInvalidKey tests error handling for invalid keys
func TestDecryptFileWithInvalidKey(t *testing.T) {
	vd := NewVideoDecryptor()

	tempDir := t.TempDir()
	testFile := filepath.Join(tempDir, "test_video.mp4")

	testData := []byte("test video data")
	if err := os.WriteFile(testFile, testData, 0644); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	// Invalid base64 should return error
	err := vd.DecryptFile(testFile, "not-valid-base64!!!")
	if err == nil {
		t.Error("Expected error for invalid base64 key, got nil")
	}

	// Key too short (less than 8 bytes when decoded)
	err = vd.DecryptFile(testFile, "YWJj") // "abc" in base64
	if err == nil {
		t.Error("Expected error for short key, got nil")
	}
}

// **Feature: video-capture-fix, Property 4: 视频格式验证**
// **Validates: Requirements 2.3**
// For any file, the validation function should correctly identify MP4, WebM,
// and other video formats by their magic bytes (file headers)
func TestVideoFormatValidation(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	// Property: MP4/MOV files with "ftyp" at offset 4-7 are recognized
	properties.Property("MP4/MOV files with ftyp header are recognized", prop.ForAll(
		func(prefix []byte, suffix []byte) bool {
			// Construct a valid MP4 header: [4 bytes size][ftyp][rest]
			header := make([]byte, 0, 12+len(suffix))
			// First 4 bytes can be anything (box size)
			if len(prefix) >= 4 {
				header = append(header, prefix[:4]...)
			} else {
				header = append(header, 0, 0, 0, 0)
			}
			// "ftyp" magic bytes
			header = append(header, 'f', 't', 'y', 'p')
			// Additional bytes
			header = append(header, suffix...)

			return IsValidVideoHeader(header)
		},
		gen.SliceOfN(4, gen.UInt8()),
		gen.SliceOfN(4, gen.UInt8()),
	))

	// Property: WebM files with correct magic bytes are recognized
	properties.Property("WebM files with correct magic bytes are recognized", prop.ForAll(
		func(suffix []byte) bool {
			// WebM magic: 0x1A 0x45 0xDF 0xA3
			header := []byte{0x1A, 0x45, 0xDF, 0xA3}
			header = append(header, suffix...)

			return IsValidVideoHeader(header)
		},
		gen.SliceOfN(8, gen.UInt8()),
	))

	// Property: AVI files with RIFF...AVI header are recognized
	properties.Property("AVI files with RIFF AVI header are recognized", prop.ForAll(
		func(middleBytes []byte) bool {
			// AVI header: RIFF[4 bytes]AVI
			header := []byte{'R', 'I', 'F', 'F'}
			// 4 bytes in the middle (file size)
			if len(middleBytes) >= 4 {
				header = append(header, middleBytes[:4]...)
			} else {
				header = append(header, 0, 0, 0, 0)
			}
			header = append(header, 'A', 'V', 'I', ' ')

			return IsValidVideoHeader(header)
		},
		gen.SliceOfN(4, gen.UInt8()),
	))

	// Property: FLV files with FLV header are recognized
	properties.Property("FLV files with FLV header are recognized", prop.ForAll(
		func(suffix []byte) bool {
			// FLV magic: "FLV"
			header := []byte{'F', 'L', 'V'}
			header = append(header, suffix...)

			return IsValidVideoHeader(header)
		},
		gen.SliceOfN(9, gen.UInt8()),
	))

	// Property: Random data without valid headers is rejected
	properties.Property("random data without valid video headers is rejected", prop.ForAll(
		func(data []byte) bool {
			// Skip if data accidentally matches a valid format
			if len(data) >= 8 && string(data[4:8]) == "ftyp" {
				return true // Skip this case
			}
			if len(data) >= 4 && data[0] == 0x1A && data[1] == 0x45 && data[2] == 0xDF && data[3] == 0xA3 {
				return true // Skip WebM
			}
			if len(data) >= 12 && string(data[0:4]) == "RIFF" && string(data[8:12]) == "AVI " {
				return true // Skip AVI
			}
			if len(data) >= 3 && string(data[0:3]) == "FLV" {
				return true // Skip FLV
			}

			// Random data should not be recognized as valid video
			return !IsValidVideoHeader(data)
		},
		gen.SliceOfN(20, gen.UInt8()),
	))

	properties.TestingRun(t)
}

// TestIsValidVideoHeaderEdgeCases tests edge cases for video header validation
func TestIsValidVideoHeaderEdgeCases(t *testing.T) {
	tests := []struct {
		name     string
		header   []byte
		expected bool
	}{
		{"empty slice", []byte{}, false},
		{"too short", []byte{0x1A, 0x45}, false},
		{"exactly 4 bytes non-video", []byte{0x00, 0x00, 0x00, 0x00}, false},
		{"valid MP4 ftyp", []byte{0x00, 0x00, 0x00, 0x20, 'f', 't', 'y', 'p'}, true},
		{"valid WebM", []byte{0x1A, 0x45, 0xDF, 0xA3}, true},
		{"valid AVI", []byte{'R', 'I', 'F', 'F', 0, 0, 0, 0, 'A', 'V', 'I', ' '}, true},
		{"valid FLV", []byte{'F', 'L', 'V', 0x01, 0x05}, true},
		{"partial ftyp", []byte{0x00, 0x00, 0x00, 0x00, 'f', 't', 'y'}, false},
		{"wrong AVI middle", []byte{'R', 'I', 'F', 'F', 0, 0, 0, 0, 'W', 'A', 'V', 'E'}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsValidVideoHeader(tt.header)
			if result != tt.expected {
				t.Errorf("IsValidVideoHeader(%v) = %v, want %v", tt.header, result, tt.expected)
			}
		})
	}
}

// **Feature: wechat-video-optimization, Property 2: ISAAC64 解密一致性**
// **Validates: Requirements 4.1**
// For any valid decodeKey and same input data, using ISAAC64 algorithm to decrypt
// should produce deterministic results (same input produces same output)
func TestISAAC64DecryptConsistencyProperty(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("ISAAC64 decryption is deterministic - same key and data produce same result", prop.ForAll(
		func(data []byte, keyVal uint64) bool {
			// Skip empty data
			if len(data) == 0 {
				return true
			}

			// Make two copies of the original data
			data1 := make([]byte, len(data))
			data2 := make([]byte, len(data))
			copy(data1, data)
			copy(data2, data)

			// Determine encryption length
			encLen := uint32(len(data))
			if encLen > DefaultEncryptedLength {
				encLen = DefaultEncryptedLength
			}

			// Decrypt both copies with the same key
			DecryptData(data1, encLen, keyVal)
			DecryptData(data2, encLen, keyVal)

			// Both results should be identical
			return bytes.Equal(data1, data2)
		},
		gen.SliceOfN(256, gen.UInt8()),
		gen.UInt64(),
	))

	properties.TestingRun(t)
}

// **Feature: wechat-video-optimization, Property 3: 解密范围正确性**
// **Validates: Requirements 4.2**
// For any video file larger than 128KB, decryption should only modify the first 131072 bytes,
// leaving the rest unchanged
func TestDecryptRangeCorrectnessProperty(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("decryption only modifies first 131072 bytes, rest remains unchanged", prop.ForAll(
		func(extraBytes []byte, keyVal uint64) bool {
			// Create data larger than DefaultEncryptedLength
			// Use a fixed prefix of DefaultEncryptedLength + extra bytes
			totalLen := DefaultEncryptedLength + len(extraBytes)
			data := make([]byte, totalLen)

			// Fill with predictable pattern
			for i := range data {
				data[i] = byte(i % 256)
			}

			// Save the portion after DefaultEncryptedLength
			originalTail := make([]byte, len(extraBytes))
			copy(originalTail, data[DefaultEncryptedLength:])

			// Decrypt with DefaultEncryptedLength
			DecryptData(data, DefaultEncryptedLength, keyVal)

			// Verify the tail portion is unchanged
			return bytes.Equal(data[DefaultEncryptedLength:], originalTail)
		},
		gen.SliceOfN(100, gen.UInt8()), // Extra bytes beyond 128KB
		gen.UInt64(),
	))

	properties.TestingRun(t)
}

// **Feature: wechat-video-optimization, Property 5: 空 decodeKey 处理**
// **Validates: Requirements 4.4**
// For any empty string or invalid decodeKey, the decrypt function should skip decryption
// and keep the original data unchanged
func TestEmptyDecodeKeyHandlingProperty(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	vd := NewVideoDecryptor()

	properties.Property("empty decodeKey skips decryption and preserves original data", prop.ForAll(
		func(data []byte) bool {
			// Skip empty data
			if len(data) == 0 {
				return true
			}

			// Create a temp file
			tempDir := t.TempDir()
			testFile := filepath.Join(tempDir, "test_video.mp4")

			// Save original data
			original := make([]byte, len(data))
			copy(original, data)

			// Write test data to file
			if err := os.WriteFile(testFile, data, 0644); err != nil {
				t.Logf("Failed to write test file: %v", err)
				return false
			}

			// Decrypt with empty key - should be a no-op
			if err := vd.DecryptFile(testFile, ""); err != nil {
				t.Logf("DecryptFile with empty key failed: %v", err)
				return false
			}

			// Read the result
			result, err := os.ReadFile(testFile)
			if err != nil {
				t.Logf("Failed to read file: %v", err)
				return false
			}

			// File should be unchanged
			return bytes.Equal(result, original)
		},
		gen.SliceOfN(100, gen.UInt8()),
	))

	properties.TestingRun(t)
}

// **Feature: wechat-video-optimization, Property 4: 视频格式验证**
// **Validates: Requirements 4.3**
// For any valid MP4 file header (containing "ftyp" magic), the format validation function should return true
func TestVideoFormatValidationProperty(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	// Property: Any header with "ftyp" at offset 4-7 is recognized as valid MP4
	properties.Property("MP4 headers with ftyp magic at offset 4-7 are recognized as valid", prop.ForAll(
		func(sizeBytes []byte, brandBytes []byte) bool {
			// Construct a valid MP4 header: [4 bytes size][ftyp][brand bytes]
			header := make([]byte, 0, 12)
			// First 4 bytes are box size (can be any value)
			if len(sizeBytes) >= 4 {
				header = append(header, sizeBytes[:4]...)
			} else {
				header = append(header, 0, 0, 0, 0)
			}
			// "ftyp" magic bytes at offset 4-7
			header = append(header, 'f', 't', 'y', 'p')
			// Brand bytes (e.g., "isom", "mp42", etc.)
			if len(brandBytes) >= 4 {
				header = append(header, brandBytes[:4]...)
			}

			return IsValidVideoHeader(header)
		},
		gen.SliceOfN(4, gen.UInt8()),
		gen.SliceOfN(4, gen.UInt8()),
	))

	// Property: Headers without valid video magic bytes are rejected
	properties.Property("headers without valid video magic bytes are rejected", prop.ForAll(
		func(data []byte) bool {
			// Skip if data accidentally matches a valid format
			if len(data) >= 8 && string(data[4:8]) == "ftyp" {
				return true // Skip - this is a valid MP4
			}
			if len(data) >= 4 && data[0] == 0x1A && data[1] == 0x45 && data[2] == 0xDF && data[3] == 0xA3 {
				return true // Skip - this is a valid WebM
			}
			if len(data) >= 12 && string(data[0:4]) == "RIFF" && string(data[8:12]) == "AVI " {
				return true // Skip - this is a valid AVI
			}
			if len(data) >= 3 && string(data[0:3]) == "FLV" {
				return true // Skip - this is a valid FLV
			}

			// Random data should not be recognized as valid video
			return !IsValidVideoHeader(data)
		},
		gen.SliceOfN(20, gen.UInt8()),
	))

	// Property: File-based validation works correctly for valid MP4 files
	properties.Property("ValidateVideoFormat returns true for files with valid MP4 headers", prop.ForAll(
		func(sizeBytes []byte, extraData []byte) bool {
			// Create a temp file with valid MP4 header
			tempDir := t.TempDir()
			testFile := filepath.Join(tempDir, "test.mp4")

			// Construct valid MP4 header
			header := make([]byte, 0, 12+len(extraData))
			if len(sizeBytes) >= 4 {
				header = append(header, sizeBytes[:4]...)
			} else {
				header = append(header, 0, 0, 0, 0)
			}
			header = append(header, 'f', 't', 'y', 'p')
			header = append(header, 'i', 's', 'o', 'm') // brand
			header = append(header, extraData...)

			if err := os.WriteFile(testFile, header, 0644); err != nil {
				t.Logf("Failed to write test file: %v", err)
				return false
			}

			return ValidateVideoFormat(testFile)
		},
		gen.SliceOfN(4, gen.UInt8()),
		gen.SliceOfN(100, gen.UInt8()),
	))

	properties.TestingRun(t)
}

// TestValidateVideoFormatWithFile tests the file-based validation
func TestValidateVideoFormatWithFile(t *testing.T) {
	tempDir := t.TempDir()

	// Test with valid MP4 header
	mp4File := filepath.Join(tempDir, "test.mp4")
	mp4Header := []byte{0x00, 0x00, 0x00, 0x20, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm'}
	if err := os.WriteFile(mp4File, mp4Header, 0644); err != nil {
		t.Fatalf("Failed to write MP4 test file: %v", err)
	}
	if !ValidateVideoFormat(mp4File) {
		t.Error("ValidateVideoFormat should return true for valid MP4")
	}

	// Test with valid WebM header
	webmFile := filepath.Join(tempDir, "test.webm")
	webmHeader := []byte{0x1A, 0x45, 0xDF, 0xA3, 0x01, 0x02, 0x03, 0x04}
	if err := os.WriteFile(webmFile, webmHeader, 0644); err != nil {
		t.Fatalf("Failed to write WebM test file: %v", err)
	}
	if !ValidateVideoFormat(webmFile) {
		t.Error("ValidateVideoFormat should return true for valid WebM")
	}

	// Test with invalid file
	invalidFile := filepath.Join(tempDir, "test.txt")
	if err := os.WriteFile(invalidFile, []byte("not a video"), 0644); err != nil {
		t.Fatalf("Failed to write invalid test file: %v", err)
	}
	if ValidateVideoFormat(invalidFile) {
		t.Error("ValidateVideoFormat should return false for non-video file")
	}

	// Test with non-existent file
	if ValidateVideoFormat(filepath.Join(tempDir, "nonexistent.mp4")) {
		t.Error("ValidateVideoFormat should return false for non-existent file")
	}
}
