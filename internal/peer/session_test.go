package peer

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Crank-Git/wingman/internal/dns"
	"github.com/Crank-Git/wingman/internal/sig"
)

// retryingTransport wraps a Transport so that Poll retries on sig.ErrNotFound
// until the message appears or ctx is cancelled. StubTransport returns
// ErrNotFound immediately, whereas a real transport (the DNS one) blocks until
// the remote peer has published; this wrapper lets a StubTransport-backed test
// exercise the same rendezvous timing without depending on goroutine order.
type retryingTransport struct {
	base sig.Transport
}

func (r retryingTransport) Publish(ctx context.Context, room string, k sig.Kind, msg []byte) error {
	return r.base.Publish(ctx, room, k, msg)
}

func (r retryingTransport) Poll(ctx context.Context, room string, k sig.Kind) ([]byte, error) {
	for {
		data, err := r.base.Poll(ctx, room, k)
		if err == nil {
			return data, nil
		}
		if !errors.Is(err, sig.ErrNotFound) {
			return nil, err
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// badPollTransport wraps a base transport so Poll returns malformed bytes,
// exercising the caller's decodeSDP failure path without any DNS.
type badPollTransport struct {
	base sig.Transport
}

func (t badPollTransport) Publish(ctx context.Context, room string, k sig.Kind, msg []byte) error {
	return t.base.Publish(ctx, room, k, msg)
}

func (t badPollTransport) Poll(_ context.Context, _ string, _ sig.Kind) ([]byte, error) {
	return []byte("not valid json"), nil
}

// garbageSDPPTransport wraps a base transport so Poll returns a well-formed JSON
// envelope whose SDP body is not valid SDP. decodeSDP succeeds (the body is just
// a string), but pion's SetRemoteDescription rejects the malformed body,
// exercising the caller's set-remote-description failure path.
type garbageSDPPTransport struct {
	base sig.Transport
	msg  []byte
}

func (t garbageSDPPTransport) Publish(ctx context.Context, room string, k sig.Kind, msg []byte) error {
	return t.base.Publish(ctx, room, k, msg)
}

func (t garbageSDPPTransport) Poll(_ context.Context, _ string, _ sig.Kind) ([]byte, error) {
	return t.msg, nil
}

// testTimeout bounds the whole handshake in integration tests.
const testTimeout = 20 * time.Second

// newTestTransport returns a WriteOverQuery transport that polls quickly and
// points at the given server address.
func newTestTransport(addr, zone string) *dns.WriteOverQuery {
	return &dns.WriteOverQuery{
		Resolver:     &dns.Dialer{Addr: addr, Net: "udp", Timeout: 2 * time.Second},
		Zone:         zone,
		PollInterval: 10 * time.Millisecond,
	}
}

// runHandshake starts an offerer and an answerer concurrently and returns when
// both data channels are open or the context is cancelled.
func runHandshake(t *testing.T, ctx context.Context, offerer, answerer *Session) {
	t.Helper()
	offerErr := startInBackground(t, ctx, offerer)
	answerErr := startInBackground(t, ctx, answerer)

	for i := 0; i < 2; i++ {
		var err error
		select {
		case err = <-offerErr:
		case err = <-answerErr:
		}
		if err != nil {
			t.Fatalf("handshake failed: %v", err)
		}
	}
}

// startInBackground runs a session's Start in a goroutine and returns a channel
// that receives its error result.
func startInBackground(t *testing.T, ctx context.Context, s *Session) chan error {
	t.Helper()
	ch := make(chan error, 1)
	go func() {
		_, err := s.Start(ctx)
		ch <- err
	}()
	return ch
}

// TestSessionEndToEndDNS exercises the full path: both peers signal over a real
// in-process write-over-query DNS server, gather host candidates, and exchange a
// message over the resulting data channel.
func TestSessionEndToEndDNS(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	srv := dns.NewServer()
	if err := srv.Start(ctx); err != nil {
		t.Fatalf("start server: %v", err)
	}
	defer srv.Stop()

	const (
		room = "e2e-room"
		zone = "example.com"
	)

	offerer := NewSession(RoleOfferer, Config{
		Room:      room,
		Zone:      zone,
		Transport: newTestTransport(srv.Addr(), zone),
		Label:     "wingman",
	})
	answerer := NewSession(RoleAnswerer, Config{
		Room:      room,
		Zone:      zone,
		Transport: newTestTransport(srv.Addr(), zone),
		Label:     "wingman",
	})

	got := make(chan string, 1)
	answerer.OnMessage(func(data []byte) { got <- string(data) })

	runHandshake(t, ctx, offerer, answerer)

	offerDC, ok := offerer.DataChannel()
	if !ok {
		t.Fatal("offerer has no data channel")
	}
	if err := offerDC.SendText("hello wingman"); err != nil {
		t.Fatalf("send: %v", err)
	}

	select {
	case msg := <-got:
		if msg != "hello wingman" {
			t.Fatalf("received %q, want %q", msg, "hello wingman")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("answerer never received the message")
	}
}

// TestSessionRoundTripBothDirections sends a message in each direction to
// confirm both data channels are fully connected.
func TestSessionRoundTripBothDirections(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	srv := dns.NewServer()
	if err := srv.Start(ctx); err != nil {
		t.Fatalf("start server: %v", err)
	}
	defer srv.Stop()

	const (
		room = "roundtrip"
		zone = "example.com"
	)

	offerer := NewSession(RoleOfferer, Config{
		Room: room, Zone: zone,
		Transport: newTestTransport(srv.Addr(), zone), Label: "wingman",
	})
	answerer := NewSession(RoleAnswerer, Config{
		Room: room, Zone: zone,
		Transport: newTestTransport(srv.Addr(), zone), Label: "wingman",
	})

	toAnswerer := make(chan string, 1)
	toOfferer := make(chan string, 1)
	answerer.OnMessage(func(data []byte) { toAnswerer <- string(data) })
	offerer.OnMessage(func(data []byte) { toOfferer <- string(data) })

	runHandshake(t, ctx, offerer, answerer)

	if err := offerer.SendText("to answerer"); err != nil {
		t.Fatalf("offerer send: %v", err)
	}
	if err := answerer.SendText("to offerer"); err != nil {
		t.Fatalf("answerer send: %v", err)
	}

	select {
	case msg := <-toAnswerer:
		if msg != "to answerer" {
			t.Fatalf("answerer got %q", msg)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("answerer did not receive offerer message")
	}
	select {
	case msg := <-toOfferer:
		if msg != "to offerer" {
			t.Fatalf("offerer got %q", msg)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("offerer did not receive answerer message")
	}
}

// TestSessionStubTransportHandshake drives the handshake over an in-memory
// StubTransport, exercising the role logic without any DNS.
func TestSessionStubTransportHandshake(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	// StubTransport.Poll returns ErrNotFound immediately, so wrap it in a
	// retrying transport to model the rendezvous timing the DNS transport
	// provides internally.
	tr := retryingTransport{base: sig.NewStubTransport()}

	offerer := NewSession(RoleOfferer, Config{Room: "stub", Zone: "example.com", Transport: tr})
	answerer := NewSession(RoleAnswerer, Config{Room: "stub", Zone: "example.com", Transport: tr})

	runHandshake(t, ctx, offerer, answerer)

	if _, ok := offerer.DataChannel(); !ok {
		t.Fatal("offerer has no data channel")
	}
	if _, ok := answerer.DataChannel(); !ok {
		t.Fatal("answerer has no data channel")
	}
}

// TestSessionContextCancel verifies a cancelled context tears the session down
// instead of blocking forever.
func TestSessionContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	tr := sig.NewStubTransport()
	offerer := NewSession(RoleOfferer, Config{Room: "cancel", Zone: "example.com", Transport: tr})

	done := make(chan error, 1)
	go func() {
		_, err := offerer.Start(ctx)
		done <- err
	}()

	// Cancel immediately; Start must return.
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Start did not return after context cancellation")
	}
}

// TestRunOffererDecodeError verifies that malformed answer bytes are surfaced
// by the offerer's decodeSDP instead of being swallowed.
func TestRunOffererDecodeError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	offerer := NewSession(RoleOfferer, Config{Room: "bad", Zone: "example.com", Transport: badPollTransport{base: sig.NewStubTransport()}})

	_, err := offerer.Start(ctx)
	if err == nil {
		t.Fatal("expected decodeSDP error")
	}
}

// TestRunAnswererDecodeError verifies that malformed offer bytes are surfaced by
// the answerer's decodeSDP instead of being swallowed.
func TestRunAnswererDecodeError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	answerer := NewSession(RoleAnswerer, Config{Room: "bad", Zone: "example.com", Transport: badPollTransport{base: sig.NewStubTransport()}})

	_, err := answerer.Start(ctx)
	if err == nil {
		t.Fatal("expected decodeSDP error")
	}
}

// TestRoleString covers both branches of Role.String.
func TestRoleString(t *testing.T) {
	if got := RoleOfferer.String(); got != "offer" {
		t.Fatalf("offerer String = %q, want %q", got, "offer")
	}
	if got := RoleAnswerer.String(); got != "answer" {
		t.Fatalf("answerer String = %q, want %q", got, "answer")
	}
}

// TestDataChannelNotOpen returns a false when no channel exists yet.
func TestDataChannelNotOpen(t *testing.T) {
	s := NewSession(RoleOfferer, Config{Room: "n/a", Zone: "example.com"})
	dc, ok := s.DataChannel()
	if dc != nil || ok {
		t.Fatalf("DataChannel = (%v, %v), want (nil, false)", dc, ok)
	}
}

// TestSendTextNotOpen returns errNotOpen when no channel exists yet.
func TestSendTextNotOpen(t *testing.T) {
	s := NewSession(RoleOfferer, Config{Room: "n/a", Zone: "example.com"})
	if err := s.SendText("hi"); !errors.Is(err, errNotOpen) {
		t.Fatalf("SendText err = %v, want errNotOpen", err)
	}
}

// TestRunOffererPollError verifies that an offerer surfaces a transport error
// from polling the answer instead of blocking.
func TestRunOffererPollError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	// StubTransport returns ErrNotFound immediately (no answer is published).
	offerer := NewSession(RoleOfferer, Config{Room: "empty", Zone: "example.com", Transport: sig.NewStubTransport()})

	_, err := offerer.Start(ctx)
	if err == nil {
		t.Fatal("expected poll answer error")
	}
}

// TestRunAnswererPollError verifies that an answerer surfaces a transport error
// from polling the offer instead of blocking.
func TestRunAnswererPollError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	// StubTransport returns ErrNotFound immediately (no offer is published).
	answerer := NewSession(RoleAnswerer, Config{Room: "empty", Zone: "example.com", Transport: sig.NewStubTransport()})

	_, err := answerer.Start(ctx)
	if err == nil {
		t.Fatal("expected poll offer error")
	}
}

// failPublishTransport wraps a base transport so that Publish fails with a fixed
// error while Poll is delegated unchanged, exercising the caller's transport
// error path without any DNS.
type failPublishTransport struct {
	base sig.Transport
	pubErr error
}

func (t failPublishTransport) Publish(context.Context, string, sig.Kind, []byte) error {
	return t.pubErr
}

func (t failPublishTransport) Poll(ctx context.Context, room string, k sig.Kind) ([]byte, error) {
	return t.base.Poll(ctx, room, k)
}

// TestRunOffererIceGatheringCancel verifies that a context cancelled while ICE
// is still gathering is surfaced by the offerer rather than being swallowed by
// a blocking gather.
func TestRunOffererIceGatheringCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancelled before Start: pion's create/offer/setlocal do not take
	// a context, but waitGathering observes the cancelled ctx and returns it.

	offerer := NewSession(RoleOfferer, Config{Room: "cancel-ice", Zone: "example.com", Transport: sig.NewStubTransport()})
	_, err := offerer.Start(ctx)
	if err == nil {
		t.Fatal("expected ctx error from ICE gathering cancellation")
	}
}

// TestRunAnswererInvalidOffer verifies that a well-formed-but-invalid offer
// (valid JSON envelope, malformed SDP body) is surfaced by SetRemoteDescription
// instead of being swallowed.
func TestRunAnswererInvalidOffer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	answerer := NewSession(RoleAnswerer, Config{
		Room: "bad-offer", Zone: "example.com",
		Transport: garbageSDPPTransport{base: sig.NewStubTransport(), msg: []byte(`{"type":"offer","sdp":"this is not valid sdp"}`)},
	})
	_, err := answerer.Start(ctx)
	if err == nil {
		t.Fatal("expected SetRemoteDescription error for invalid offer")
	}
}

// TestRunOffererInvalidAnswer verifies that a well-formed-but-invalid answer is
// surfaced by the offerer's SetRemoteDescription instead of being swallowed.
func TestRunOffererInvalidAnswer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	offerer := NewSession(RoleOfferer, Config{
		Room: "bad-answer", Zone: "example.com",
		Transport: garbageSDPPTransport{base: sig.NewStubTransport(), msg: []byte(`{"type":"answer","sdp":"this is not valid sdp"}`)},
	})
	_, err := offerer.Start(ctx)
	if err == nil {
		t.Fatal("expected SetRemoteDescription error for invalid answer")
	}
}

// TestRunAnswererPublishError verifies that an answerer surfaces a transport
// error from publishing its answer instead of blocking or swallowing it. A
// real offer is published by a background offerer over a shared in-memory
// store; the answerer polls that offer but fails when publishing.
func TestRunAnswererPublishError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	store := sig.NewStubTransport()
	offerer := NewSession(RoleOfferer, Config{Room: "pub-err", Zone: "example.com", Transport: retryingTransport{base: store}})
	answerer := NewSession(RoleAnswerer, Config{Room: "pub-err", Zone: "example.com", Transport: failPublishTransport{base: retryingTransport{base: store}, pubErr: errors.New("publish boom")}})

	offerDone := make(chan error, 1)
	go func() {
		_, err := offerer.Start(ctx)
		offerDone <- err
	}()

	_, err := answerer.Start(ctx)
	if err == nil {
		t.Fatal("expected publish error")
	}

	// The offerer is now blocked polling for an answer that will never arrive;
	// cancelling unblocks it.
	cancel()
	select {
	case <-offerDone:
	case <-time.After(5 * time.Second):
		t.Fatal("offerer did not return after cancel")
	}
}
