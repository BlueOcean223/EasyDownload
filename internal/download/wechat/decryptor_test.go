package wechat

import (
	"bytes"
	"testing"
)

func TestParseDecodeKey(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    uint64
		wantErr bool
	}{
		{"empty", "", 0, true},
		{"decimal", "123", 123, false},
		{"hexLower", "0x10", 16, false},
		{"hexUpper", "0Xff", 255, false},
		{"negative", "-1", ^uint64(0), false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseDecodeKey(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error for %q", tc.in)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error for %q: %v", tc.in, err)
			}
			if got != tc.want {
				t.Fatalf("expected %d, got %d", tc.want, got)
			}
		})
	}
}

func TestDecryptData_RoundTrip(t *testing.T) {
	orig := make([]byte, 4096)
	for i := range orig {
		orig[i] = byte(i % 251)
	}

	data := make([]byte, len(orig))
	copy(data, orig)

	const (
		encLen = 131072
		key    = uint64(0x123456789abcdef0)
	)

	DecryptData(data, encLen, key)
	DecryptData(data, encLen, key)

	if !bytes.Equal(data, orig) {
		t.Fatal("expected decrypt(encrypt(x)) to equal original")
	}
}
