// Package sig implements the DNS-based signaling layer for Wingman: SDP
// offer/answer framing, chunking and reassembly, rendezvous naming, and the
// transport abstraction that carries fragments over DNS.
package sig

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"sort"
)

// crcSize is the number of trailer bytes carrying the CRC32 of a message.
const crcSize = 4

// defaultChunkSize is the largest payload (in bytes) that fits in a single
// DNS-label-sized fragment after the CRC trailer is appended to the final
// fragment. It is derived from the 63-character DNS label limit.
var defaultChunkSize = maxBytesForLabel(63) - crcSize

// maxBytesForLabel returns the maximum number of bytes whose base32 encoding
// fits within maxLen DNS-label characters. base32 expands data by 8/5, and each
// 5-byte group becomes 8 characters, so the largest group count is maxLen/8.
func maxBytesForLabel(maxLen int) int {
	if maxLen <= 0 {
		return 0
	}
	return (maxLen / 8) * 5
}

// Fragment is one ordered piece of a reassembled message. Data holds this
// fragment's payload bytes; for the final fragment Data also carries the
// CRC32 trailer appended by Fragments.
type Fragment struct {
	Seq   uint16
	Total uint16
	Data  []byte
}

// Fragments splits msg into Total fragments of at most chunkSize payload
// bytes each. A CRC32 trailer is appended to the final fragment's payload so
// Reassemble can verify integrity after reassembly. The message must be
// non-empty and small enough to fit in a uint16 fragment count.
func Fragments(msg []byte, chunkSize int) ([]Fragment, error) {
	if len(msg) == 0 {
		return nil, errors.New("sig: cannot fragment an empty message")
	}
	if chunkSize <= 0 {
		chunkSize = defaultChunkSize
	}
	if chunkSize > maxBytesForLabel(63)-crcSize {
		return nil, fmt.Errorf("sig: chunkSize %d too large", chunkSize)
	}

	total := (len(msg)-1)/chunkSize + 1
	if total > 0xFFFF {
		return nil, errors.New("sig: message too large to fragment")
	}

	frags := make([]Fragment, 0, total)
	crc := crc32.ChecksumIEEE(msg)
	for i := 0; i < total; i++ {
		start := i * chunkSize
		end := start + chunkSize
		if end > len(msg) {
			end = len(msg)
		}
		data := make([]byte, end-start)
		copy(data, msg[start:end])
		if i == total-1 {
			trailer := make([]byte, crcSize)
			binary.BigEndian.PutUint32(trailer, crc)
			data = append(data, trailer...)
		}
		frags = append(frags, Fragment{
			Seq:   uint16(i),
			Total: uint16(total),
			Data:  data,
		})
	}
	return frags, nil
}

// Reassemble orders fragments by Seq, concatenates their payloads, verifies
// the CRC32 trailer, and returns the original message. Fragments may arrive
// out of order or with gaps; both are reported as errors.
func Reassemble(frags []Fragment) ([]byte, error) {
	if len(frags) == 0 {
		return nil, errors.New("sig: no fragments")
	}

	total := frags[0].Total
	for _, f := range frags {
		if f.Total != total {
			return nil, errors.New("sig: inconsistent fragment total")
		}
	}

	sorted := make([]Fragment, len(frags))
	copy(sorted, frags)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Seq < sorted[j].Seq })

	for i, f := range sorted {
		if f.Seq != uint16(i) {
			return nil, fmt.Errorf("sig: gap in fragment sequence at position %d (have seq %d)", i, f.Seq)
		}
	}

	// The final fragment (seq == total-1) carries the CRC32 trailer. If only a
	// contiguous prefix has arrived -- e.g. the publisher is still writing or a
	// response was truncated to drop the highest sequence numbers -- there is no
	// trailer yet. Report incompleteness so the caller keeps polling rather than
	// misreading trailing payload bytes as a CRC.
	if sorted[len(sorted)-1].Seq != total-1 {
		return nil, ErrIncomplete
	}

	var buf bytes.Buffer
	for _, f := range sorted {
		buf.Write(f.Data)
	}
	full := buf.Bytes()
	if len(full) < crcSize {
		return nil, errors.New("sig: reassembled message too short for CRC")
	}
	payload := full[:len(full)-crcSize]
	stored := binary.BigEndian.Uint32(full[len(full)-crcSize:])
	if got := crc32.ChecksumIEEE(payload); got != stored {
		return nil, fmt.Errorf("sig: CRC mismatch: got %08x want %08x", got, stored)
	}
	return payload, nil
}
