package wechat

import (
	"encoding/binary"
	"fmt"
	"os"
	"strconv"
	"strings"
)

const (
	// DefaultEncryptedLength is the number of bytes that WeChat encrypts at the start of video files
	// WeChat videos encrypt the first 128KB (131072 bytes)
	DefaultEncryptedLength = 131072 // 128 * 1024
)

// VideoDecryptor handles decryption of WeChat video files
type VideoDecryptor struct{}

// NewVideoDecryptor creates a new VideoDecryptor instance
func NewVideoDecryptor() *VideoDecryptor {
	return &VideoDecryptor{}
}

// IsEncrypted checks if a video needs decryption based on the decodeKey
func (vd *VideoDecryptor) IsEncrypted(decodeKey string) bool {
	return decodeKey != ""
}

// ParseDecodeKey parses the decodeKey string and returns the uint64 key
// Supports multiple formats:
// - Decimal number string: "12345678901234567890"
// - Hex number string: "0x..." or "0X..."
// - Negative number (treated as unsigned): "-12345"
func ParseDecodeKey(decodeKey string) (uint64, error) {
	decodeKey = strings.TrimSpace(decodeKey)
	if decodeKey == "" {
		return 0, fmt.Errorf("empty decodeKey")
	}

	// Try hex format first (0x prefix)
	if strings.HasPrefix(decodeKey, "0x") || strings.HasPrefix(decodeKey, "0X") {
		key, err := strconv.ParseUint(decodeKey[2:], 16, 64)
		if err != nil {
			return 0, fmt.Errorf("failed to parse hex decodeKey: %w", err)
		}
		return key, nil
	}

	// Try decimal format (may be negative which will be treated as unsigned)
	// WeChat sometimes provides negative numbers which should be interpreted as unsigned
	if strings.HasPrefix(decodeKey, "-") {
		// Parse as signed int64 first, then convert to uint64
		signedKey, err := strconv.ParseInt(decodeKey, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("failed to parse signed decodeKey: %w", err)
		}
		return uint64(signedKey), nil
	}

	// Parse as unsigned decimal
	key, err := strconv.ParseUint(decodeKey, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("failed to parse decodeKey: %w", err)
	}

	return key, nil
}

// DecryptFile decrypts a video file using the ISAAC64 XOR algorithm with the provided decodeKey
// The decodeKey is expected to be a numeric string (decimal or hex with 0x prefix)
// WeChat video decodeKey is a uint64 seed value for the ISAAC64 PRNG
func (vd *VideoDecryptor) DecryptFile(filePath string, decodeKey string) error {
	if !vd.IsEncrypted(decodeKey) {
		return nil // No decryption needed
	}

	// Parse the decodeKey as uint64
	encKey, err := ParseDecodeKey(decodeKey)
	if err != nil {
		return fmt.Errorf("failed to parse decodeKey '%s': %w", decodeKey, err)
	}

	// Read the file
	original, err := os.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("failed to read file: %w", err)
	}

	// Progressive decryption: WeChat may encrypt more than the initial header area for some builds.
	// Try increasing prefix sizes and only write back once the result looks like a real container.
	candidates := []int{
		DefaultEncryptedLength,
		512 * 1024,
		2 * 1024 * 1024,
		8 * 1024 * 1024,
		len(original),
	}
	seen := make(map[int]struct{}, len(candidates))
	for _, cand := range candidates {
		if cand <= 0 {
			continue
		}
		if cand > len(original) {
			cand = len(original)
		}
		if _, ok := seen[cand]; ok {
			continue
		}
		seen[cand] = struct{}{}

		data := make([]byte, len(original))
		copy(data, original)

		DecryptData(data, uint32(cand), encKey)

		if isLikelyValidContainer(data) {
			// Write the decrypted data back
			if err := os.WriteFile(filePath, data, 0644); err != nil {
				return fmt.Errorf("failed to write decrypted file: %w", err)
			}
			return nil
		}
	}

	return fmt.Errorf("decryption did not produce a valid video container")
}

// DecryptBytes decrypts a byte slice in-place using the XOR algorithm
// This is useful for testing the round-trip property
func (vd *VideoDecryptor) DecryptBytes(data []byte, encLen uint32, encKey uint64) {
	DecryptData(data, encLen, encKey)
}

// ValidateVideoFormat checks if a file is a valid video format by examining magic bytes
func ValidateVideoFormat(filePath string) bool {
	file, err := os.Open(filePath)
	if err != nil {
		return false
	}
	defer file.Close()

	// Read first 12 bytes for magic number detection
	header := make([]byte, 12)
	n, err := file.Read(header)
	if err != nil || n < 4 {
		return false
	}

	return IsValidVideoHeader(header[:n])
}

// IsValidVideoHeader checks if the byte slice starts with a valid video format magic number
func IsValidVideoHeader(header []byte) bool {
	if len(header) < 4 {
		return false
	}

	// MP4/MOV: starts with ftyp box (offset 4-7 = "ftyp")
	if len(header) >= 8 && string(header[4:8]) == "ftyp" {
		return true
	}

	// WebM: starts with 0x1A 0x45 0xDF 0xA3
	if header[0] == 0x1A && header[1] == 0x45 && header[2] == 0xDF && header[3] == 0xA3 {
		return true
	}

	// AVI: starts with "RIFF" and contains "AVI "
	if len(header) >= 12 && string(header[0:4]) == "RIFF" && string(header[8:12]) == "AVI " {
		return true
	}

	// FLV: starts with "FLV"
	if string(header[0:3]) == "FLV" {
		return true
	}

	// MKV: same as WebM (0x1A 0x45 0xDF 0xA3)
	// Already covered by WebM check

	return false
}

// isLikelyValidContainer checks if decrypted data appears to be a valid video container.
// It first validates the magic bytes header, then for MP4/MOV files, performs additional
// box structure validation to avoid false positives from coincidental "ftyp" matches.
func isLikelyValidContainer(data []byte) bool {
	if len(data) < 12 {
		return false
	}
	if !IsValidVideoHeader(data[:12]) {
		return false
	}
	// For MP4/MOV, validate a small prefix of box structure to avoid accepting
	// "ftyp" coincidences after wrong decryption.
	if len(data) >= 8 && string(data[4:8]) == "ftyp" {
		return looksLikeValidMP4Boxes(data)
	}
	return true
}

// looksLikeValidMP4Boxes validates MP4 box structure by parsing the first few megabytes.
// MP4 files consist of nested boxes (atoms), each with a 4-byte size and 4-byte type.
// This function checks that boxes have valid ASCII types and reasonable sizes,
// and looks for essential boxes like "moov" (metadata) or "mdat" (media data).
func looksLikeValidMP4Boxes(data []byte) bool {
	const maxScan = 4 * 1024 * 1024 // Only scan first 4MB for performance
	limit := len(data)
	if limit > maxScan {
		limit = maxScan
	}
	off := 0
	boxes := 0
	seenMoov := false
	seenMdat := false
	for off+8 <= limit && boxes < 256 {
		// Read box header: 4-byte size + 4-byte type
		size32 := binary.BigEndian.Uint32(data[off : off+4])
		typ := data[off+4 : off+8]
		// Box type should be printable ASCII (0x20-0x7E)
		for _, b := range typ {
			if b < 0x20 || b > 0x7E {
				return false
			}
		}
		boxType := string(typ)
		headerSize := 8
		var boxSize uint64
		switch size32 {
		case 0:
			// size=0 means box extends to end of file
			boxSize = uint64(len(data) - off)
		case 1:
			// size=1 means 64-bit extended size follows the type
			if off+16 > len(data) {
				return false
			}
			headerSize = 16
			boxSize = binary.BigEndian.Uint64(data[off+8 : off+16])
		default:
			boxSize = uint64(size32)
		}
		if boxSize < uint64(headerSize) {
			return false
		}
		if off+int(boxSize) > len(data) {
			return false
		}
		// Track essential MP4 boxes
		if boxType == "moov" {
			seenMoov = true
		}
		if boxType == "mdat" {
			seenMdat = true
		}
		off += int(boxSize)
		boxes++
		// Early exit: valid if we've seen 2+ boxes and at least one essential box
		if boxes >= 2 && (seenMoov || seenMdat) && off >= 32 {
			return true
		}
	}
	return boxes >= 2 && (seenMoov || seenMdat)
}

// RandCtx64 is the ISAAC64 random number generator context
type RandCtx64 struct {
	RandCnt uint64
	Seed    [256]uint64
	MM      [256]uint64
	AA      uint64
	BB      uint64
	CC      uint64
}

// CreateISAacInst creates a new ISAAC64 instance with the given encryption key
func CreateISAacInst(encKey uint64) *RandCtx64 {
	ctx := &RandCtx64{
		RandCnt: 255,
		AA:      0,
		BB:      0,
		CC:      0,
	}
	rand64Init(ctx, encKey)
	return ctx
}

// ISAacRandom generates the next random number
func (ctx *RandCtx64) ISAacRandom() uint64 {
	result := ctx.Seed[ctx.RandCnt]
	if ctx.RandCnt == 0 {
		ctx.isAAC64()
		ctx.RandCnt = 255
	} else {
		ctx.RandCnt--
	}
	return result
}

// rand64Init initializes the ISAAC64 PRNG context with the given encryption key.
// The initialization process follows the standard ISAAC64 algorithm:
// 1. Set seed[0] to the encryption key, rest to zero
// 2. Initialize 8 working variables with the golden ratio constant
// 3. Mix the working variables 4 times to scramble initial state
// 4. Two passes over the 256-element arrays to fill MM with mixed values
// 5. Generate the first batch of random numbers
func rand64Init(ctx *RandCtx64, encKey uint64) {
	// Golden ratio constant for ISAAC64 initialization
	const golden = uint64(0x9e3779b97f4a7c13)
	a, b, c, d := golden, golden, golden, golden
	e, f, g, h := golden, golden, golden, golden

	// Place encryption key in seed array
	ctx.Seed[0] = encKey
	for i := 1; i < 256; i++ {
		ctx.Seed[i] = 0
	}

	// Initial mixing rounds to scramble the golden ratio values
	for range 4 {
		mix(&a, &b, &c, &d, &e, &f, &g, &h)
	}

	// First pass: mix seed values into MM array
	for i := 0; i < 256; i += 8 {
		a += ctx.Seed[i]
		b += ctx.Seed[i+1]
		c += ctx.Seed[i+2]
		d += ctx.Seed[i+3]
		e += ctx.Seed[i+4]
		f += ctx.Seed[i+5]
		g += ctx.Seed[i+6]
		h += ctx.Seed[i+7]
		mix(&a, &b, &c, &d, &e, &f, &g, &h)
		ctx.MM[i] = a
		ctx.MM[i+1] = b
		ctx.MM[i+2] = c
		ctx.MM[i+3] = d
		ctx.MM[i+4] = e
		ctx.MM[i+5] = f
		ctx.MM[i+6] = g
		ctx.MM[i+7] = h
	}

	// Second pass: mix MM values back into itself for better diffusion
	for i := 0; i < 256; i += 8 {
		a += ctx.MM[i]
		b += ctx.MM[i+1]
		c += ctx.MM[i+2]
		d += ctx.MM[i+3]
		e += ctx.MM[i+4]
		f += ctx.MM[i+5]
		g += ctx.MM[i+6]
		h += ctx.MM[i+7]
		mix(&a, &b, &c, &d, &e, &f, &g, &h)
		ctx.MM[i] = a
		ctx.MM[i+1] = b
		ctx.MM[i+2] = c
		ctx.MM[i+3] = d
		ctx.MM[i+4] = e
		ctx.MM[i+5] = f
		ctx.MM[i+6] = g
		ctx.MM[i+7] = h
	}

	// Generate first batch of random numbers
	ctx.isAAC64()
}

// isAAC64 is the core ISAAC64 generation function that produces 256 new random numbers.
// It uses a combination of:
// - Accumulator (AA) with rotating bit operations based on position
// - Counter (CC) that increments each generation round
// - Carry (BB) that accumulates intermediate results
// - Indirect addressing through MM array for non-linear mixing
func (ctx *RandCtx64) isAAC64() {
	ctx.CC++ // Increment counter each round
	ctx.BB += ctx.CC

	for i := range 256 {
		// Rotate AA differently based on position (mod 4) for varied mixing
		switch i % 4 {
		case 0:
			ctx.AA = ^(ctx.AA ^ (ctx.AA << 21))
		case 1:
			ctx.AA ^= ctx.AA >> 5
		case 2:
			ctx.AA ^= ctx.AA << 12
		case 3:
			ctx.AA ^= ctx.AA >> 33
		}

		// Add value from opposite half of MM array
		ctx.AA += ctx.MM[(i+128)%256]
		x := ctx.MM[i]
		// Indirect lookup using x's bits for non-linear mixing
		y := ctx.MM[(x>>3)%256] + ctx.AA + ctx.BB
		ctx.MM[i] = y
		// Another indirect lookup for the output value
		ctx.BB = ctx.MM[(y>>11)%256] + x
		ctx.Seed[i] = ctx.BB
	}
}

// mix performs the ISAAC64 mixing function on 8 uint64 values.
// It uses a series of subtractions, XORs with shifts, and additions
// to thoroughly mix the values together. Each operation depends on
// previous results, creating an avalanche effect where small input
// changes propagate to all outputs.
func mix(a, b, c, d, e, f, g, h *uint64) {
	*a -= *e
	*f ^= *h >> 9
	*h += *a
	*b -= *f
	*g ^= *a << 9
	*a += *b
	*c -= *g
	*h ^= *b >> 23
	*b += *c
	*d -= *h
	*a ^= *c << 15
	*c += *d
	*e -= *a
	*b ^= *d >> 14
	*d += *e
	*f -= *b
	*c ^= *e << 20
	*e += *f
	*g -= *c
	*d ^= *f >> 17
	*f += *g
	*h -= *d
	*e ^= *g << 14
	*g += *h
}

// DecryptData decrypts data in-place using the ISAAC64 XOR algorithm
func DecryptData(data []byte, encLen uint32, key uint64) {
	if len(data) == 0 || uint32(len(data)) < encLen {
		if len(data) > 0 {
			encLen = uint32(len(data))
		} else {
			return
		}
	}

	aaInst := CreateISAacInst(key)

	for i := uint32(0); i < encLen; i += 8 {
		randNumber := aaInst.ISAacRandom()
		tempNumber := make([]byte, 8)
		binary.BigEndian.PutUint64(tempNumber, randNumber)

		for j := 0; j < 8; j++ {
			realIndex := i + uint32(j)
			if realIndex >= encLen {
				return
			}
			data[realIndex] ^= tempNumber[j]
		}
	}
}
