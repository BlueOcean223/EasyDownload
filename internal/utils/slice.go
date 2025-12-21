package utils

import (
	"sort"
	"strings"
)

// FilterNonEmpty filters out empty strings from the input slice.
func FilterNonEmpty(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// SameIntSlice checks if two int slices are equal.
func SameIntSlice(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// NormalizeIndices returns a sorted, deduplicated copy of the input slice.
// This ensures consistent comparison regardless of input order.
func NormalizeIndices(indices []int) []int {
	if len(indices) == 0 {
		return nil
	}
	sorted := append([]int(nil), indices...)
	sort.Ints(sorted)
	// Deduplicate
	j := 0
	for i := range sorted {
		if i == 0 || sorted[i] != sorted[i-1] {
			sorted[j] = sorted[i]
			j++
		}
	}
	return sorted[:j]
}
