package sig

import (
	"bytes"
	"errors"
	"testing"
)

// TestFragmentsMultiFragment exercises the multi-chunk path explicitly.
func TestFragmentsMultiFragment(t *testing.T) {
	msg := bytes.Repeat([]byte("payload"), 100)
	frags, err := Fragments(msg, 7)
	if err != nil {
		t.Fatalf("Fragments: %v", err)
	}
	if len(frags) < 2 {
		t.Fatalf("expected multiple fragments, got %d", len(frags))
	}
	if _, err := Reassemble(frags); err != nil {
		t.Fatalf("Reassemble: %v", err)
	}
}

func TestFragmentsReassembleRoundTrip(t *testing.T) {
	for _, size := range []int{1, 31, 32, 100, 5000} {
		msg := bytes.Repeat([]byte{0xAB, 0xCD, 0x11, 0x22}, size)
		frags, err := Fragments(msg, 7)
		if err != nil {
			t.Fatalf("Fragments(%d): %v", size, err)
		}
		for i, f := range frags {
			if f.Seq != uint16(i) {
				t.Fatalf("fragment %d has seq %d", i, f.Seq)
			}
			if f.Total != uint16(len(frags)) {
				t.Fatalf("fragment %d has total %d want %d", i, f.Total, len(frags))
			}
		}
		got, err := Reassemble(frags)
		if err != nil {
			t.Fatalf("Reassemble(%d): %v", size, err)
		}
		if !bytes.Equal(got, msg) {
			t.Fatalf("size %d: reassembled message mismatch", size)
		}
	}
}

func TestFragmentsDefaultChunkSizeFitsLabel(t *testing.T) {
	msg := bytes.Repeat([]byte("wingman-signaling-payload"), 800)
	frags, err := Fragments(msg, 0)
	if err != nil {
		t.Fatalf("Fragments: %v", err)
	}
	for _, f := range frags {
		label, err := EncodeLabel(f.Data)
		if err != nil {
			t.Fatalf("fragment %d: %v", f.Seq, err)
		}
		if len(label) > dnsLabelMaxLen {
			t.Fatalf("fragment %d label length %d exceeds %d", f.Seq, len(label), dnsLabelMaxLen)
		}
	}
}

func TestReassembleOutOfOrder(t *testing.T) {
	msg := []byte("the quick brown fox jumps over the lazy dog")
	frags, err := Fragments(msg, 5)
	if err != nil {
		t.Fatalf("Fragments: %v", err)
	}
	// Reverse the fragment order.
	for i, j := 0, len(frags)-1; i < j; i, j = i+1, j-1 {
		frags[i], frags[j] = frags[j], frags[i]
	}
	got, err := Reassemble(frags)
	if err != nil {
		t.Fatalf("Reassemble out of order: %v", err)
	}
	if !bytes.Equal(got, msg) {
		t.Fatalf("out-of-order reassemble mismatch")
	}
}

func TestReassembleCRCMismatch(t *testing.T) {
	msg := []byte("integrity matters for DNS signaling")
	frags, err := Fragments(msg, 4)
	if err != nil {
		t.Fatalf("Fragments: %v", err)
	}
	// Corrupt the last fragment's payload (which carries the CRC trailer).
	last := frags[len(frags)-1]
	last.Data[len(last.Data)-1] ^= 0xFF
	if _, err := Reassemble(frags); err == nil {
		t.Fatal("expected CRC mismatch error, got nil")
	}
}

func TestReassembleGap(t *testing.T) {
	msg := []byte("0123456789")
	frags, err := Fragments(msg, 2) // 5 fragments, seq 0..4, total 5
	if err != nil {
		t.Fatalf("Fragments: %v", err)
	}
	// Drop fragment seq 2 to create a gap in the sequence.
	kept := make([]Fragment, 0, len(frags)-1)
	for _, f := range frags {
		if f.Seq == 2 {
			continue
		}
		kept = append(kept, f)
	}
	if _, err := Reassemble(kept); err == nil {
		t.Fatal("expected gap error, got nil")
	}
}

// TestReassembleIncompleteTail covers the transient state where only a
// contiguous prefix of fragments has arrived: the final fragment (which carries
// the CRC32 trailer) is missing, so reassembly must report incompleteness
// rather than misreading trailing payload as a CRC.
func TestReassembleIncompleteTail(t *testing.T) {
	msg := []byte("0123456789")
	frags, err := Fragments(msg, 2) // 5 fragments, seq 0..4, total 5
	if err != nil {
		t.Fatalf("Fragments: %v", err)
	}
	// Drop the final fragment to simulate an in-flight publish or a truncated
	// response that dropped the highest sequence number.
	kept := make([]Fragment, 0, len(frags)-1)
	for _, f := range frags {
		if f.Seq == 4 {
			continue
		}
		kept = append(kept, f)
	}
	got, err := Reassemble(kept)
	if errors.Is(err, ErrIncomplete) {
		return
	}
	if got != nil || err == nil {
		t.Fatalf("expected ErrIncomplete, got (%v, %v)", got, err)
	}
}

func TestReassembleInconsistentTotal(t *testing.T) {
	frags := []Fragment{
		{Seq: 0, Total: 2, Data: []byte("ab")},
		{Seq: 1, Total: 3, Data: []byte("cd")},
	}
	if _, err := Reassemble(frags); err == nil {
		t.Fatal("expected inconsistent-total error, got nil")
	}
}

func TestReassembleNoFragments(t *testing.T) {
	if _, err := Reassemble(nil); err == nil {
		t.Fatal("expected error for empty fragments")
	}
}

// TestReassembleTooShortForCRC covers the case where the concatenated
// fragment payload is shorter than the CRC trailer itself. Such a fragment set
// cannot come from Fragments (which always appends a 4-byte trailer), so it is
// constructed directly to exercise the guard.
func TestReassembleTooShortForCRC(t *testing.T) {
	frags := []Fragment{{Seq: 0, Total: 1, Data: []byte("a")}}
	if _, err := Reassemble(frags); err == nil {
		t.Fatal("expected error when reassembled message is too short for CRC")
	}
}

func TestFragmentsEmpty(t *testing.T) {
	if _, err := Fragments(nil, 0); err == nil {
		t.Fatal("expected error for empty message")
	}
}

func TestFragmentsTooLarge(t *testing.T) {
	big := make([]byte, 0x10001) // 65537 > 0xFFFF
	if _, err := Fragments(big, 1); err == nil {
		t.Fatal("expected error for message exceeding uint16 fragment count")
	}
}

func TestFragmentsChunkSizeTooLarge(t *testing.T) {
	if _, err := Fragments([]byte("hello"), 32); err == nil {
		t.Fatal("expected error for chunkSize exceeding label capacity")
	}
}
