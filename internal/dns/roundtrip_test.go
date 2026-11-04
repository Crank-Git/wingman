package dns

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/miekg/dns"

	"github.com/Crank-Git/wingman/internal/sig"
)

// newLoopbackTransport returns a WriteOverQuery client pointed at srv with fast
// polling, suitable for round-trip tests.
func newLoopbackTransport(t *testing.T, srv *Server) *WriteOverQuery {
	t.Helper()
	return &WriteOverQuery{
		Resolver:     &Dialer{Addr: srv.Addr(), Net: "udp", Timeout: 2 * time.Second},
		Zone:         "example.com",
		PollInterval: 10 * time.Millisecond,
	}
}

// TestWriteOverQueryRoundTrip writes a multi-fragment message through a real
// in-process server and reassembles it, exercising the full UDP path including
// any TCP fallback and end-gap truncation.
func TestWriteOverQueryRoundTripLarge(t *testing.T) {
	cases := []struct {
		name string
		size int
	}{
		{"small", 100},
		{"medium", 700},
		{"large", 4000},
		{"xlarge", 6000},
		{"xxlarge", 9000},
		{"fragment-boundary", 93},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()

			srv := NewServer()
			if err := srv.Start(ctx); err != nil {
				t.Fatalf("start server: %v", err)
			}
			defer srv.Stop()

			pub := newLoopbackTransport(t, srv)
			sub := newLoopbackTransport(t, srv)

			msg := make([]byte, tc.size)
			for i := range msg {
				msg[i] = byte((i * 31) & 0xff)
			}

			if err := pub.Publish(ctx, "rt-room", sig.Offer, msg); err != nil {
				t.Fatalf("publish: %v", err)
			}
			got, err := sub.Poll(ctx, "rt-room", sig.Offer)
			if err != nil {
				t.Fatalf("poll: %v", err)
			}
			if !bytes.Equal(got, msg) {
				t.Fatalf("reassembled %d bytes, want %d", len(got), len(msg))
			}
		})
	}
}

// TestPollRetriesOnIncompleteMessage verifies that Poll, when the server holds
// only a contiguous prefix of a message (the final fragment carrying the CRC
// trailer not yet published), keeps polling until the remaining fragments
// arrive rather than failing on a false CRC mismatch.
func TestPollRetriesOnIncompleteMessage(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	srv := newTestServer(t)

	msg := []byte("0123456789")
	frags, err := sig.Fragments(msg, 2) // 5 fragments, seq 0..4, total 5
	if err != nil {
		t.Fatalf("Fragments: %v", err)
	}

	writeOne := func(f sig.Fragment) {
		payload, err := sig.EncodeLabel(f.Data)
		if err != nil {
			t.Fatalf("EncodeLabel: %v", err)
		}
		name := sig.RendezvousName("example.com", "incom-room", sig.Offer, f.Seq, f.Total, payload)
		m := new(dns.Msg)
		m.SetQuestion(dns.Fqdn(name), dns.TypeTXT)
		m.SetEdns0(4096, false)
		client := &Dialer{Addr: srv.Addr(), Net: "udp", Timeout: 2 * time.Second}
		if _, err := client.Exchange(ctx, m); err != nil {
			t.Fatalf("write fragment %d: %v", f.Seq, err)
		}
	}

	// Publish only the first three fragments; the final one is still missing.
	for i := 0; i < 3; i++ {
		writeOne(frags[i])
	}

	sub := newClient(srv)
	done := make(chan error, 1)
	go func() {
		_, err := sub.Poll(ctx, "incom-room", sig.Offer)
		done <- err
	}()

	// Let the poller observe the incomplete state and retry a few times.
	time.Sleep(150 * time.Millisecond)
	// Now publish the remaining fragments.
	for i := 3; i < len(frags); i++ {
		writeOne(frags[i])
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Poll after completion: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Poll did not complete after fragments were published")
	}
}

// gapResolver returns a fixed set of rendezvous names as TXT answers, letting a
// test exercise Poll's reassembly-error path without a live server. It models a
// response that is complete enough to be contiguous but corrupted (here, with a
// gap in the sequence).
type gapResolver struct {
	names []string
}

func (r gapResolver) Exchange(_ context.Context, m *dns.Msg) (*dns.Msg, error) {
	resp := new(dns.Msg)
	resp.SetReply(m)
	for _, name := range r.names {
		resp.Answer = append(resp.Answer, &dns.TXT{
			Hdr: dns.RR_Header{Name: dns.Fqdn(name), Rrtype: dns.TypeTXT, Class: dns.ClassINET, Ttl: 1},
			Txt: []string{name},
		})
	}
	return resp, nil
}

// TestPollSurfacesReassemblyError verifies that Poll returns a reassembly error
// (such as a gap) rather than retrying forever on a corrupted response.
func TestPollSurfacesReassemblyError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	build := func(seq, total uint16, payload string) string {
		enc, err := sig.EncodeLabel([]byte(payload))
		if err != nil {
			t.Fatalf("EncodeLabel: %v", err)
		}
		return sig.RendezvousName("example.com", "gap-room", sig.Offer, seq, total, enc)
	}

	// Fragments 0 and 2 of 3: seq 1 is missing, so Reassemble reports a gap.
	names := []string{
		build(0, 3, "aaaa"),
		build(2, 3, "cccc"),
	}

	tr := &WriteOverQuery{
		Resolver:     gapResolver{names: names},
		Zone:         "example.com",
		PollInterval: 10 * time.Millisecond,
	}

	_, err := tr.Poll(ctx, "gap-room", sig.Offer)
	if err == nil {
		t.Fatal("expected a reassembly gap error, got nil")
	}
}

// incompleteResolver always returns a single (incomplete) fragment, modelling a
// publisher that never finishes writing so Reassemble keeps returning
// sig.ErrIncomplete.
type incompleteResolver struct {
	room string
}

func (r incompleteResolver) Exchange(_ context.Context, m *dns.Msg) (*dns.Msg, error) {
	resp := new(dns.Msg)
	resp.SetReply(m)
	enc, err := sig.EncodeLabel([]byte("hi"))
	if err != nil {
		return nil, err
	}
	fragName := sig.RendezvousName("example.com", r.room, sig.Offer, 0, 2, enc)
	resp.Answer = append(resp.Answer, &dns.TXT{
		Hdr: dns.RR_Header{Name: dns.Fqdn(fragName), Rrtype: dns.TypeTXT, Class: dns.ClassINET, Ttl: 1},
		Txt: []string{fragName},
	})
	return resp, nil
}

// TestPollRetriesUntilBudgetExhausted verifies that Poll keeps retrying an
// incomplete message and eventually returns a PollError once the retry budget
// (PollRetries) is exhausted.
func TestPollRetriesUntilBudgetExhausted(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	tr := &WriteOverQuery{
		Resolver:     incompleteResolver{room: "incom-room"},
		Zone:         "example.com",
		PollInterval: time.Millisecond,
		PollRetries:  3,
	}

	_, err := tr.Poll(ctx, "incom-room", sig.Offer)
	if _, ok := err.(*PollError); !ok {
		t.Fatalf("Poll err = %T, want *PollError", err)
	}
}
