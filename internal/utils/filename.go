package utils

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// SanitizeFileName normalizes a filename/component for safe filesystem use.
// maxBytes limits the UTF-8 byte length; pass <=0 for no limit.
func SanitizeFileName(name string, maxBytes int) string {
	result := strings.TrimSpace(name)
	if result == "" {
		return ""
	}

	result = strings.NewReplacer(
		"\r", " ",
		"\n", " ",
		"\t", " ",
		"\u2028", " ",
		"\u2029", " ",
	).Replace(result)

	var b strings.Builder
	b.Grow(len(result))
	prevUnderscore := false
	for _, r := range result {
		if r == utf8.RuneError || r == 0xFFFD {
			continue
		}
		if r < 32 || r == 127 {
			continue
		}
		if unicode.IsSpace(r) {
			if !prevUnderscore {
				b.WriteByte('_')
				prevUnderscore = true
			}
			continue
		}
		switch r {
		case '/', '\\', ':', '*', '?', '"', '<', '>', '|':
			if !prevUnderscore {
				b.WriteByte('_')
				prevUnderscore = true
			}
			continue
		}

		b.WriteRune(r)
		prevUnderscore = false
	}

	out := strings.Trim(b.String(), " ._")
	if out == "" {
		return ""
	}

	// Check Windows reserved names (must check base name without extension)
	baseName := out
	if dotIdx := strings.LastIndex(out, "."); dotIdx > 0 {
		baseName = out[:dotIdx]
	}
	upper := strings.ToUpper(baseName)
	switch upper {
	case "CON", "PRN", "AUX", "NUL",
		"COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9",
		"LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9":
		out = "_" + out
	}

	if maxBytes > 0 && len(out) > maxBytes {
		var cut strings.Builder
		cut.Grow(maxBytes)
		n := 0
		for _, r := range out {
			rl := utf8.RuneLen(r)
			if rl <= 0 || n+rl > maxBytes {
				break
			}
			cut.WriteRune(r)
			n += rl
		}
		out = strings.Trim(cut.String(), " ._")
	}

	return out
}
