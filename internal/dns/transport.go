package dns

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/miekg/dns"

	"github.com/Crank-Git/wingman/internal/sig"
)

// WriteOverQuery is a sig.Transport that encodes writes as DNS queries sent to
// a write-over-query authoritative server. Each fragment is published as a
// query for its rendezvous name; the server stores the fragment. Polling sends
// a query for the room's poll name and reassembles the fragments the server
// returns.

// edns0UDPSize is the EDNS0 UDP payload advertised by queries and responses.
// Responses larger than this are truncated on UDP (the TC bit is set) and the
// client falls back to TCP, which has no size limit.
const edns0UDPSize = 4096

type WriteOverQuery struct {
	// Resolver sends queries; defaults to a UDP Dialer to 127.0.0.1:53.
	Resolver Resolver
	// Zone is the authoritative zone, e.g. "example.com".
	Zone string

	// PollInterval is the delay between poll attempts when nothing is found.
	PollInterval time.Duration
	// PollRetries bounds consecutive misses before giving up. 0 means unlimited.
	PollRetries int
}

func (t *WriteOverQuery) withDefaults() *WriteOverQuery {
	if t.Resolver == nil {
		t.Resolver = &Dialer{}
	}
	if t.PollInterval == 0 {
		t.PollInterval = 250 * time.Millisecond
	}
	return t
}

// Publish fragments msg and writes each fragment as a DNS query.
// If ctx is cancelled, Publish returns its error before the next resolver call.
// Publish rejects messages above the identity's byte limit before fragmentation.
func (t *WriteOverQuery) Publish(ctx context.Context, room string, k sig.Kind, msg []byte) error {
	t.withDefaults()
	key := storageKey{zone: canonicalReplyName(t.Zone), room: strings.ToLower(room), kind: k}
	limit := messageLimit(key, defaultStorageLimits())
	if len(msg) > limit {
		return fmt.Errorf("wingman: publish message has %d bytes; limit for %s is %d bytes", len(msg), sig.PollName(key.zone, key.room, key.kind), limit)
	}
	frags, err := sig.Fragments(msg, 0)
	if err != nil {
		return err
	}
	for _, f := range frags {
		payload, err := sig.EncodeLabel(f.Data)
		if err != nil {
			return err
		}
		name := sig.RendezvousName(t.Zone, room, k, f.Seq, f.Total, payload)
		if err := t.writeFragment(ctx, name); err != nil {
			return err
		}
	}
	return nil
}

// writeFragment sends a query for a single fragment name and checks the rcode.
func (t *WriteOverQuery) writeFragment(ctx context.Context, name string) error {
	m := new(dns.Msg)
	m.SetQuestion(dns.Fqdn(name), dns.TypeTXT)
	m.SetEdns0(edns0UDPSize, false)

	if err := ctx.Err(); err != nil {
		return err
	}
	resp, err := t.Resolver.Exchange(ctx, m)
	if err != nil {
		return err
	}
	if resp == nil {
		return fmt.Errorf("wingman: fragment write %s: nil resolver response", name)
	}
	if resp.Rcode != dns.RcodeSuccess {
		return &PublishError{Name: name, Rcode: resp.Rcode}
	}
	return nil
}

// Poll polls for the complete SDP of the given kind under room, reassembling
// it from the fragments the server returns.
func (t *WriteOverQuery) Poll(ctx context.Context, room string, k sig.Kind) ([]byte, error) {
	t.withDefaults()
	pollName := sig.PollName(t.Zone, room, k)

	retries := 0
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		frags, found, err := t.pollOnce(ctx, pollName)
		if err != nil {
			return nil, err
		}
		if !found {
			retries++
			if t.PollRetries > 0 && retries >= t.PollRetries {
				return nil, &PollError{Room: room, Kind: k.String(), Retries: retries}
			}
			wait(ctx, t.PollInterval)
			continue
		}
		data, err := sig.Reassemble(frags)
		if err != nil {
			// An incomplete message is transient: the publisher may still be
			// writing fragments, or the UDP response was truncated and the next
			// poll falls back to TCP. Keep polling until the message completes or
			// ctx expires.
			if errors.Is(err, sig.ErrIncomplete) {
				retries++
				if t.PollRetries > 0 && retries >= t.PollRetries {
					return nil, &PollError{Room: room, Kind: k.String(), Retries: retries}
				}
				wait(ctx, t.PollInterval)
				continue
			}
			return nil, err
		}
		return data, nil
	}
}

// pollOnce sends one poll query. found is false when the server reports the
// room is empty (caller should retry); an error indicates a transport failure.
func (t *WriteOverQuery) pollOnce(ctx context.Context, pollName string) (frags []sig.Fragment, found bool, err error) {
	m := new(dns.Msg)
	m.SetQuestion(dns.Fqdn(pollName), dns.TypeTXT)
	m.SetEdns0(edns0UDPSize, false)

	resp, err := t.Resolver.Exchange(ctx, m)
	if err != nil {
		return nil, false, err
	}
	if resp == nil {
		return nil, false, fmt.Errorf("wingman: poll %s: nil resolver response", pollName)
	}
	switch resp.Rcode {
	case dns.RcodeSuccess:
		// fall through to reassemble the fragments
	case dns.RcodeNameError:
		// NXDOMAIN: the room is not ready yet. Report a miss so the caller
		// retries (respecting PollRetries / ctx) rather than failing outright.
		return nil, false, nil
	default:
		return nil, false, fmt.Errorf("wingman: poll %s: unexpected rcode %s", pollName, dns.RcodeToString[resp.Rcode])
	}

	frags = make([]sig.Fragment, 0, len(resp.Answer))
	for _, rr := range resp.Answer {
		txt, ok := rr.(*dns.TXT)
		if !ok || len(txt.Txt) != 1 {
			continue
		}
		if canonicalReplyName(txt.Hdr.Name) != canonicalReplyName(pollName) {
			return nil, false, fmt.Errorf("wingman: poll %s: TXT owner identity mismatch", pollName)
		}
		parsed, perr := sig.ParseRendezvousName(canonicalReplyName(txt.Txt[0]))
		if perr != nil {
			continue
		}
		if canonicalReplyName(sig.PollName(parsed.Zone, parsed.Room, parsed.Kind)) != canonicalReplyName(pollName) {
			return nil, false, fmt.Errorf("wingman: poll %s: fragment identity mismatch", pollName)
		}
		payload, derr := sig.DecodeLabel(parsed.Payload)
		if derr != nil {
			continue
		}
		frags = append(frags, sig.Fragment{Seq: parsed.Seq, Total: parsed.Total, Data: payload})
	}
	return frags, true, nil
}

func canonicalReplyName(name string) string {
	return strings.ToLower(strings.TrimSuffix(name, "."))
}

// wait sleeps or returns early on ctx cancellation.
func wait(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

// PublishError indicates a fragment write was rejected by the server.
type PublishError struct {
	Name  string
	Rcode int
}

func (e *PublishError) Error() string {
	return "wingman: fragment write failed: " + e.Name + " (rcode " + dns.RcodeToString[e.Rcode] + ")"
}

// PollError indicates polling exceeded the retry budget.
type PollError struct {
	Room    string
	Kind    string
	Retries int
}

func (e *PollError) Error() string {
	return "wingman: poll timed out for room " + e.Room + " kind " + e.Kind + " after " + strconv.Itoa(e.Retries) + " retries"
}
