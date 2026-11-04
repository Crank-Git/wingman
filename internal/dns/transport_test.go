package dns

import (
	"bytes"
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/miekg/dns"

	"github.com/Crank-Git/wingman/internal/sig"
)

// newTestServer starts an in-process server on a background context.
func newTestServer(t *testing.T) *Server {
	t.Helper()
	srv := NewServer()
	if err := srv.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(srv.Stop)
	return srv
}

func newClient(srv *Server) *WriteOverQuery {
	return &WriteOverQuery{
		Resolver:     &Dialer{Addr: srv.Addr()},
		Zone:         "example.com",
		PollInterval: 10 * time.Millisecond,
	}
}

func TestWriteOverQueryRoundTrip(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	srv := newTestServer(t)
	tr := newClient(srv)

	offer := []byte("v=0\no=- 0 0 IN TXT wingman-offer\ns=-\nc=IN NET\n")
	if err := tr.Publish(ctx, "room1", sig.Offer, offer); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	got, err := tr.Poll(ctx, "room1", sig.Offer)
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if !bytes.Equal(got, offer) {
		t.Fatalf("poll = %q, want %q", got, offer)
	}
}

func TestWriteOverQueryMultiFragment(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	srv := newTestServer(t)
	tr := newClient(srv)

	// A multi-KB SDP with many ICE candidates forces several fragments.
	offer := bytes.Repeat([]byte("a=candidate:usr 1 udp 1 192.168.1.1 8000 typ host end-of-candidates\n"), 50)
	if err := tr.Publish(ctx, "room2", sig.Offer, offer); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	got, err := tr.Poll(ctx, "room2", sig.Offer)
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if !bytes.Equal(got, offer) {
		t.Fatalf("multi-fragment poll mismatch: got %d bytes, want %d", len(got), len(offer))
	}
}

func TestWriteOverQueryAnswerExchange(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	srv := newTestServer(t)
	offerer := newClient(srv)
	answerer := newClient(srv)

	offer := []byte("offer-sdp")
	if err := offerer.Publish(ctx, "room3", sig.Offer, offer); err != nil {
		t.Fatalf("Publish offer: %v", err)
	}
	gotOffer, err := answerer.Poll(ctx, "room3", sig.Offer)
	if err != nil {
		t.Fatalf("Poll offer: %v", err)
	}
	if !bytes.Equal(gotOffer, offer) {
		t.Fatalf("offer mismatch")
	}

	answer := []byte("answer-sdp")
	if err := answerer.Publish(ctx, "room3", sig.Answer, answer); err != nil {
		t.Fatalf("Publish answer: %v", err)
	}
	gotAnswer, err := offerer.Poll(ctx, "room3", sig.Answer)
	if err != nil {
		t.Fatalf("Poll answer: %v", err)
	}
	if !bytes.Equal(gotAnswer, answer) {
		t.Fatalf("answer mismatch")
	}
}

func TestPollBeforePublishTimesOut(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	srv := newTestServer(t)
	tr := &WriteOverQuery{
		Resolver:     &Dialer{Addr: srv.Addr()},
		Zone:         "example.com",
		PollInterval: 10 * time.Millisecond,
		PollRetries:  3,
	}
	_, err := tr.Poll(ctx, "empty-room", sig.Offer)
	var pe *PollError
	if !errors.As(err, &pe) {
		t.Fatalf("expected *PollError, got %v", err)
	}
	if !strings.Contains(err.Error(), "poll timed out") {
		t.Fatalf("unexpected error message: %q", err.Error())
	}
}

func TestPollContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	srv := newTestServer(t)
	tr := &WriteOverQuery{
		Resolver:     &Dialer{Addr: srv.Addr()},
		Zone:         "example.com",
		PollInterval: time.Hour, // ensures we are sleeping when ctx cancels
	}
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	if _, err := tr.Poll(ctx, "room", sig.Offer); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

// TestWriteFragmentRejectCovered exercises the non-success rcode path using a
// resolver that rejects writes.
func TestWriteFragmentRejectCovered(t *testing.T) {
	ctx := context.Background()
	srv := newTestServer(t)
	_ = srv // keep server for symmetry
	reject := &fixedResolver{resp: &dns.Msg{MsgHdr: dns.MsgHdr{Rcode: dns.RcodeRefused}}}
	tr := &WriteOverQuery{Resolver: reject, Zone: "example.com"}
	err := tr.Publish(ctx, "room", sig.Offer, []byte("x"))
	var pe *PublishError
	if !errors.As(err, &pe) {
		t.Fatalf("expected *PublishError, got %v", err)
	}
	if !strings.Contains(err.Error(), "fragment write failed") {
		t.Fatalf("unexpected error message: %q", err.Error())
	}
}

// TestPollOnceTransportError covers the transport-error path in pollOnce.
func TestPollOnceTransportError(t *testing.T) {
	ctx := context.Background()
	tr := &WriteOverQuery{Resolver: &fixedResolver{err: errBoom}}
	if _, err := tr.Poll(ctx, "room", sig.Offer); !errors.Is(err, errBoom) {
		t.Fatalf("expected errBoom, got %v", err)
	}
}

// TestPollOnceBadPayload covers pollOnce skipping a fragment whose label does
// not decode as base32 (the DecodeLabel error path).
func TestPollOnceBadPayload(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	// A well-formed rendezvous name whose payload label is not valid base32:
	// ParseRendezvousName accepts it, DecodeLabel rejects it, so pollOnce skips
	// the fragment and reassembly of the empty set fails.
	resp := new(dns.Msg)
	resp.SetReply(&dns.Msg{Question: []dns.Question{{Name: "room.offer.sig.example.com", Qtype: dns.TypeTXT, Qclass: dns.ClassINET}}})
	resp.SetEdns0(4096, false)
	resp.Rcode = dns.RcodeSuccess
	resp.Answer = append(resp.Answer, &dns.TXT{
		Hdr: dns.RR_Header{Name: "room.offer.sig.example.com", Rrtype: dns.TypeTXT, Class: dns.ClassINET},
		Txt: []string{"room.0-1.%%%%.offer.sig.example.com"},
	})
	tr := &WriteOverQuery{Resolver: &fixedResolver{resp: resp}, Zone: "example.com"}
	if _, err := tr.Poll(ctx, "room", sig.Offer); err == nil {
		t.Fatal("expected error for undecodable payload")
	}
}

// TestPollOnceMalformedAnswer covers pollOnce ignoring an unparseable answer.
func TestPollOnceMalformedAnswer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	// NOERROR but with a single unparseable fragment answer: pollOnce skips it,
	// so the poller reassembles an empty set and reassembly fails.
	tr := &WriteOverQuery{Resolver: &fixedResolver{resp: malformedAnswer()}, Zone: "example.com"}
	if _, err := tr.Poll(ctx, "room", sig.Offer); err == nil {
		t.Fatal("expected error for malformed answer")
	}
}

type fixedResolver struct {
	resp *dns.Msg
	err  error
}

func (f *fixedResolver) Exchange(_ context.Context, _ *dns.Msg) (*dns.Msg, error) {
	return f.resp, f.err
}

// TestWithDefaultsNilResolver covers the nil-Resolver default in withDefaults.
func TestWithDefaultsNilResolver(t *testing.T) {
	tr := &WriteOverQuery{}
	tr.withDefaults()
	if tr.Resolver == nil {
		t.Fatal("withDefaults: nil Resolver was not defaulted")
	}
	if tr.PollInterval != 250*time.Millisecond {
		t.Fatalf("withDefaults: PollInterval = %v, want 250ms", tr.PollInterval)
	}
}

// TestPublishEmptyMessage covers the empty-message error path in Publish.
func TestPublishEmptyMessage(t *testing.T) {
	ctx := context.Background()
	srv := newTestServer(t)
	tr := newClient(srv)
	if err := tr.Publish(ctx, "room", sig.Offer, nil); err == nil {
		t.Fatal("expected error publishing empty message")
	}
}

// TestPublishTransportError covers the exchange-error path in writeFragment.
func TestPublishTransportError(t *testing.T) {
	ctx := context.Background()
	tr := &WriteOverQuery{Resolver: &fixedResolver{err: errBoom}, Zone: "example.com"}
	if err := tr.Publish(ctx, "room", sig.Offer, []byte("x")); !errors.Is(err, errBoom) {
		t.Fatalf("expected errBoom, got %v", err)
	}
}

// TestPollOnceNonTXTAnswer covers pollOnce skipping an answer record that is
// not a TXT (or a TXT with the wrong number of strings): the !ok / len != 1
// branch at the top of the answer loop.
func TestPollOnceNonTXTAnswer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	// NOERROR with a non-TXT answer: pollOnce skips it, reassembles an empty
	// set, and reassembly fails.
	resp := new(dns.Msg)
	resp.SetReply(&dns.Msg{Question: []dns.Question{{Name: "room.offer.sig.example.com", Qtype: dns.TypeTXT, Qclass: dns.ClassINET}}})
	resp.SetEdns0(4096, false)
	resp.Rcode = dns.RcodeSuccess
	resp.Answer = append(resp.Answer, &dns.A{
		Hdr: dns.RR_Header{Name: "room.offer.sig.example.com", Rrtype: dns.TypeA, Class: dns.ClassINET},
		A:   net.IPv4(127, 0, 0, 1),
	})
	tr := &WriteOverQuery{Resolver: &fixedResolver{resp: resp}}
	if _, err := tr.Poll(ctx, "room", sig.Offer); err == nil {
		t.Fatal("expected error for non-TXT answer")
	}
}

// TestPollOnceUnexpectedRcode covers the default rcode path in pollOnce.
func TestPollOnceUnexpectedRcode(t *testing.T) {
	ctx := context.Background()
	resp := &dns.Msg{MsgHdr: dns.MsgHdr{Rcode: dns.RcodeRefused}}
	tr := &WriteOverQuery{Resolver: &fixedResolver{resp: resp}, Zone: "example.com"}
	if _, err := tr.Poll(ctx, "room", sig.Offer); err == nil {
		t.Fatal("expected error for unexpected rcode")
	}
}

// TestDialerExchangeFails covers the UDP exchange-error path in Dialer.Exchange
// by pointing at a port with nothing listening.
func TestDialerExchangeFails(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	d := &Dialer{Addr: "127.0.0.1:1", Net: "udp", Timeout: 2 * time.Second}
	m := new(dns.Msg)
	m.SetQuestion("example.com.", dns.TypeTXT)
	if _, err := d.Exchange(ctx, m); err == nil {
		t.Fatal("expected exchange error to a dead port")
	}
}

var errBoom = errors.New("boom")

func malformedAnswer() *dns.Msg {
	resp := new(dns.Msg)
	resp.SetReply(&dns.Msg{Question: []dns.Question{{Name: "room.offer.sig.example.com", Qtype: dns.TypeTXT, Qclass: dns.ClassINET}}})
	resp.SetEdns0(4096, false)
	resp.Rcode = dns.RcodeSuccess
	// A TXT answer whose single string is not a valid rendezvous name.
	resp.Answer = append(resp.Answer, &dns.TXT{
		Hdr: dns.RR_Header{Name: "room.offer.sig.example.com", Rrtype: dns.TypeTXT, Class: dns.ClassINET},
		Txt: []string{"not-a-valid-fragment-name"},
	})
	return resp
}
