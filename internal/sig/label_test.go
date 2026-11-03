package sig

import (
	"bytes"
	"testing"
)

func TestEncodeDecodeLabelRoundTrip(t *testing.T) {
	for _, in := range [][]byte{
		{0},
		[]byte("hello"),
		bytes.Repeat([]byte{0xFF}, maxBytesForLabel(dnsLabelMaxLen)),
	} {
		label, err := EncodeLabel(in)
		if err != nil {
			t.Fatalf("EncodeLabel(%d bytes): %v", len(in), err)
		}
		if len(label) > dnsLabelMaxLen {
			t.Fatalf("label length %d exceeds %d", len(label), dnsLabelMaxLen)
		}
		if !isDNSLabelSafe(label) {
			t.Fatalf("label %q contains non-DNS characters", label)
		}
		got, err := DecodeLabel(label)
		if err != nil {
			t.Fatalf("DecodeLabel: %v", err)
		}
		if !bytes.Equal(got, in) {
			t.Fatalf("round-trip mismatch for %v", in)
		}
	}
}

func TestEncodeLabelTooLarge(t *testing.T) {
	big := make([]byte, maxBytesForLabel(dnsLabelMaxLen)+1)
	if _, err := EncodeLabel(big); err == nil {
		t.Fatal("expected error for oversized label data")
	}
}

func TestDecodeLabelEmpty(t *testing.T) {
	if _, err := DecodeLabel(""); err == nil {
		t.Fatal("expected error for empty label")
	}
}

func TestDecodeLabelInvalid(t *testing.T) {
	// '@' is not in the base32 alphabet.
	if _, err := DecodeLabel("@@@@@@"); err == nil {
		t.Fatal("expected error for invalid base32 label")
	}
}

func TestMaxBytesForLabel(t *testing.T) {
	// 63 chars -> 7 groups of 8 chars -> 35 bytes; 36 bytes would need 64 chars.
	if got := maxBytesForLabel(63); got != 35 {
		t.Fatalf("maxBytesForLabel(63) = %d, want 35", got)
	}
	if got := maxBytesForLabel(0); got != 0 {
		t.Fatalf("maxBytesForLabel(0) = %d, want 0", got)
	}
	if got := maxBytesForLabel(-5); got != 0 {
		t.Fatalf("maxBytesForLabel(-5) = %d, want 0", got)
	}
}

func TestDefaultChunkSize(t *testing.T) {
	if defaultChunkSize != maxBytesForLabel(63)-crcSize {
		t.Fatalf("defaultChunkSize = %d, want %d", defaultChunkSize, maxBytesForLabel(63)-crcSize)
	}
}

func isDNSLabelSafe(s string) bool {
	if len(s) > dnsLabelMaxLen {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= '2' && r <= '7':
		default:
			return false
		}
	}
	return len(s) > 0
}
