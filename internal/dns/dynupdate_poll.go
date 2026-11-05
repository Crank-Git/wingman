package dns

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Crank-Git/wingman/internal/sig"
	"github.com/miekg/dns"
)

// Poll returns the validated message from an authenticated TXT RRset.
// Missing records retry until ctx expires. Invalid complete responses fail immediately.
func (t *DynUpdate) Poll(ctx context.Context, room string, kind sig.Kind) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("DynUpdate poll: %w", err)
	}
	owner, err := t.owner(room, kind)
	if err != nil {
		return nil, err
	}
	query := new(dns.Msg)
	query.SetQuestion(owner, dns.TypeTXT)
	if err := t.checkWireSize("query", query); err != nil {
		return nil, err
	}
	for {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("DynUpdate poll: %w", err)
		}
		reply, err := t.Resolver.Exchange(ctx, query)
		if err != nil {
			return nil, dynUpdateExchangeError("poll", err)
		}
		if reply == nil {
			return nil, errors.New("DynUpdate poll: nil resolver response")
		}
		if err := checkDynUpdateResponseSize(reply); err != nil {
			return nil, err
		}
		if reply.Truncated {
			return nil, errors.New("DynUpdate poll: truncated TCP response")
		}
		if reply.Rcode == dns.RcodeNameError || reply.Rcode == dns.RcodeSuccess && len(reply.Answer) == 0 {
			wait(ctx, 250*time.Millisecond)
			continue
		}
		if reply.Rcode != dns.RcodeSuccess {
			return nil, &DynUpdateRcodeError{Operation: "poll", Rcode: reply.Rcode}
		}
		message, err := reassembleDynUpdate(owner, reply.Answer)
		if err != nil {
			return nil, err
		}
		// A cleanup failure must not discard a validated message.
		if err := t.cleanupKinds(ctx, room, kind); err != nil && t.OnCleanupFailure != nil {
			t.OnCleanupFailure()
		}
		return message, nil
	}
}

func checkDynUpdateResponseSize(m *dns.Msg) error {
	// Keep every received record and the actual TSIG data in the measurement.
	wire, err := m.Pack()
	if err != nil {
		return fmt.Errorf("DynUpdate pack response: %w", err)
	}
	limit := defaultStorageLimits().responseBytes
	if len(wire) > dns.MaxMsgSize {
		limit = dns.MaxMsgSize
	}
	if len(wire) > limit {
		return &DynUpdateSizeError{Operation: "response", Size: len(wire), Limit: limit}
	}
	return nil
}

func reassembleDynUpdate(owner string, records []dns.RR) ([]byte, error) {
	// Every fragment carries at least one byte. The final fragment includes four CRC bytes.
	if len(records) > maxMessageBytes+4 {
		return nil, errors.New("DynUpdate poll: fragment count exceeds limit")
	}
	fragments := make([]sig.Fragment, 0, len(records))
	seen := make(map[uint16]bool, len(records))
	size := 0
	for _, rr := range records {
		txt, ok := rr.(*dns.TXT)
		if !ok || txt == nil || txt.Hdr.Rrtype != dns.TypeTXT || txt.Hdr.Class != dns.ClassINET || canonicalReplyName(txt.Hdr.Name) != canonicalReplyName(owner) {
			return nil, errors.New("DynUpdate poll: invalid answer identity or type")
		}
		f, err := parseDynUpdateFragment(txt, len(records))
		if err != nil {
			return nil, err
		}
		if seen[f.Seq] {
			return nil, errors.New("DynUpdate poll: duplicate fragment sequence")
		}
		seen[f.Seq] = true
		size += len(f.Data)
		if size > maxMessageBytes+4 {
			return nil, &DynUpdateSizeError{Operation: "message", Size: size - 4, Limit: maxMessageBytes}
		}
		fragments = append(fragments, f)
	}
	message, err := sig.Reassemble(fragments)
	if err != nil {
		return nil, fmt.Errorf("DynUpdate poll reassemble: %w", err)
	}
	return message, nil
}

func parseDynUpdateFragment(txt *dns.TXT, count int) (sig.Fragment, error) {
	var f sig.Fragment
	invalid := errors.New("DynUpdate poll: invalid fragment framing")
	if len(txt.Txt) != 1 {
		return f, invalid
	}
	metadata, payload, ok := strings.Cut(txt.Txt[0], ":")
	if !ok || len(payload) == 0 || len(payload) > 56 || strings.TrimSpace(payload) != payload {
		return f, invalid
	}
	seqText, totalText, ok := strings.Cut(metadata, "-")
	if !ok {
		return f, invalid
	}
	seq, err := strconv.ParseUint(seqText, 10, 16)
	if err != nil {
		return f, invalid
	}
	total, err := strconv.ParseUint(totalText, 10, 16)
	if err != nil || total != uint64(count) || seq >= total || strconv.FormatUint(seq, 10) != seqText || strconv.FormatUint(total, 10) != totalText {
		return f, invalid
	}
	data, err := sig.DecodeLabel(payload)
	if err != nil || len(data) == 0 {
		return f, invalid
	}
	encoded, err := sig.EncodeLabel(data)
	if err != nil || encoded != payload {
		return f, invalid
	}
	return sig.Fragment{Seq: uint16(seq), Total: uint16(total), Data: data}, nil
}
