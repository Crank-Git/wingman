package peer

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"

	"github.com/Crank-Git/wingman/internal/dns"
	"github.com/Crank-Git/wingman/internal/metrics"
	"github.com/Crank-Git/wingman/internal/sig"
)

type candidateCallbackPC struct {
	peerConn
	callback func(*webrtc.ICECandidate)
}

func (p *candidateCallbackPC) Close() error                                             { return nil }
func (p *candidateCallbackPC) OnConnectionStateChange(func(webrtc.PeerConnectionState)) {}
func (p *candidateCallbackPC) OnDataChannel(func(*webrtc.DataChannel))                  {}
func (p *candidateCallbackPC) OnICECandidate(h func(*webrtc.ICECandidate)) {
	p.callback = h
}
func (p *candidateCallbackPC) CreateDataChannel(string, *webrtc.DataChannelInit) (*webrtc.DataChannel, error) {
	return nil, errors.New("stop after callback registration")
}

func TestConcurrentCollectorUpdatesRecordCandidates(t *testing.T) {
	pc := &candidateCallbackPC{}
	saved := newPeerConnection
	newPeerConnection = func(webrtc.Configuration) (peerConn, error) { return pc, nil }
	t.Cleanup(func() { newPeerConnection = saved })
	s := NewSession(RoleOfferer, Config{})
	if _, err := s.Start(context.Background()); err == nil {
		t.Fatal("Start must report the fake connection error")
	}
	first, second := metrics.New(), metrics.New()
	pc.callback(&webrtc.ICECandidate{Typ: webrtc.ICECandidateTypeHost})
	pc.callback(nil)
	s.WithMetrics(first)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 10000; i++ {
			s.WithMetrics(second)
			s.WithMetrics(nil)
			s.WithMetrics(first)
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 10000; i++ {
			pc.callback(&webrtc.ICECandidate{Typ: webrtc.ICECandidateTypeHost})
			pc.callback(nil)
		}
	}()
	wg.Wait()
	before := first.Candidates()["host"]
	s.WithMetrics(first)
	pc.callback(&webrtc.ICECandidate{Typ: webrtc.ICECandidateTypeHost})
	if got := first.Candidates()["host"]; got != before+1 {
		t.Fatalf("host count = %d, want %d", got, before+1)
	}
}

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

	for _, s := range []*Session{offerer, answerer} {
		if _, err := s.Start(ctx); err == nil || !strings.Contains(err.Error(), "already started") {
			t.Fatalf("repeat Start error = %v, want already started", err)
		}
	}

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
	base   sig.Transport
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

// TestSessionRecordsGatheredCandidates verifies that ICE candidate types are
// recorded on the metrics collector during a real handshake. This is the test
// that would have caught the candidate-distribution feature being half-wired:
// the metrics collector must observe host (and possibly srflx/relay)
// candidates gathered over the wire, not only when RecordCandidate is called
// directly.
func TestSessionRecordsGatheredCandidates(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	srv := dns.NewServer()
	if err := srv.Start(ctx); err != nil {
		t.Fatalf("start server: %v", err)
	}
	defer srv.Stop()

	const (
		room = "cand-room"
		zone = "example.com"
	)

	m := &metrics.Metrics{}
	offerer := NewSession(RoleOfferer, Config{
		Room: room, Zone: zone,
		Transport: newTestTransport(srv.Addr(), zone), Label: "wingman",
	}).WithMetrics(m)
	answerer := NewSession(RoleAnswerer, Config{
		Room: room, Zone: zone,
		Transport: newTestTransport(srv.Addr(), zone), Label: "wingman",
	}).WithMetrics(m)

	runHandshake(t, ctx, offerer, answerer)

	cands := m.Candidates()
	if cands["host"] == 0 {
		t.Fatalf("host candidates not recorded during run: %v", cands)
	}
	if !strings.Contains(m.Text(), "# TYPE wingman_candidates_host counter\nwingman_candidates_host ") {
		t.Fatalf("handshake metrics omit host candidates: %s", m.Text())
	}
}

// overridePC wraps a real *webrtc.PeerConnection and fails one or more methods
// with a configured error so the handshake's failure branches can be exercised
// deterministically. Untuned methods delegate to the real peer connection.
type overridePC struct {
	*webrtc.PeerConnection
	createDataChannelErr error
	createOfferErr       error
	createAnswerErr      error
	setLocalErr          error
}

func (f *overridePC) CreateDataChannel(label string, opts *webrtc.DataChannelInit) (*webrtc.DataChannel, error) {
	if f.createDataChannelErr != nil {
		return nil, f.createDataChannelErr
	}
	return f.PeerConnection.CreateDataChannel(label, opts)
}

func (f *overridePC) CreateOffer(opts *webrtc.OfferOptions) (webrtc.SessionDescription, error) {
	if f.createOfferErr != nil {
		return webrtc.SessionDescription{}, f.createOfferErr
	}
	return f.PeerConnection.CreateOffer(opts)
}

func (f *overridePC) CreateAnswer(opts *webrtc.AnswerOptions) (webrtc.SessionDescription, error) {
	if f.createAnswerErr != nil {
		return webrtc.SessionDescription{}, f.createAnswerErr
	}
	return f.PeerConnection.CreateAnswer(opts)
}

func (f *overridePC) SetLocalDescription(desc webrtc.SessionDescription) error {
	if f.setLocalErr != nil {
		return f.setLocalErr
	}
	return f.PeerConnection.SetLocalDescription(desc)
}

// injectFailingPC installs a peer-connection factory that returns a real peer
// connection wrapped so that the named method fails with boom. It returns a
// restore function that callers must defer.
func injectFailingPC(t *testing.T, failOn string, boom error) func() {
	t.Helper()
	cfg := Config{Room: "inject", Zone: "example.com"}
	realPC, err := webrtc.NewPeerConnection(configuration(cfg))
	if err != nil {
		t.Fatalf("new pc for injection: %v", err)
	}
	overlay := &overridePC{PeerConnection: realPC}
	switch failOn {
	case "CreateDataChannel":
		overlay.createDataChannelErr = boom
	case "CreateOffer":
		overlay.createOfferErr = boom
	case "SetLocalDescription":
		overlay.setLocalErr = boom
	case "CreateAnswer":
		overlay.createAnswerErr = boom
	default:
		t.Fatalf("unknown failOn %q", failOn)
	}
	saved := newPeerConnection
	newPeerConnection = func(webrtc.Configuration) (peerConn, error) {
		return overlay, nil
	}
	return func() { newPeerConnection = saved }
}

// makeValidOffer builds a complete offer SDP envelope by driving a real peer
// connection, so it can be handed to an answerer via a fixed transport.
func makeValidOffer(t *testing.T) []byte {
	t.Helper()
	pc, err := webrtc.NewPeerConnection(configuration(Config{Room: "offer", Zone: "example.com"}))
	if err != nil {
		t.Fatalf("new pc: %v", err)
	}
	defer pc.Close()
	if _, err := pc.CreateDataChannel(defaultLabel, nil); err != nil {
		t.Fatalf("create data channel: %v", err)
	}
	offer, err := pc.CreateOffer(nil)
	if err != nil {
		t.Fatalf("create offer: %v", err)
	}
	if err := pc.SetLocalDescription(offer); err != nil {
		t.Fatalf("set local description: %v", err)
	}
	deadline := time.After(5 * time.Second)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if pc.ICEGatheringState() == webrtc.ICEGatheringStateComplete {
			break
		}
		select {
		case <-deadline:
			t.Fatal("ICE gathering timed out")
		case <-ticker.C:
		}
	}
	full, err := encodeSDP(*pc.LocalDescription())
	if err != nil {
		t.Fatalf("encode offer: %v", err)
	}
	return full
}

// fixedOfferTransport returns a prepared offer on every Poll.
type fixedOfferTransport struct {
	offer []byte
}

func (t fixedOfferTransport) Publish(context.Context, string, sig.Kind, []byte) error { return nil }
func (t fixedOfferTransport) Poll(_ context.Context, _ string, _ sig.Kind) ([]byte, error) {
	return t.offer, nil
}

// assertErrorContains checks that err is non-nil and wraps the expected
// failing operation, so a regression that swallows the real error and falls
// through to a later non-nil error still fails the test.
func assertErrorContains(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error containing %q, got nil", want)
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("expected error containing %q, got %v", want, err)
	}
}

// TestRunOffererCreateDataChannelError verifies that a failure creating the
// offerer's data channel is surfaced instead of being swallowed.
func TestRunOffererCreateDataChannelError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	restore := injectFailingPC(t, "CreateDataChannel", errors.New("create dc boom"))
	defer restore()

	offerer := NewSession(RoleOfferer, Config{Room: "dc", Zone: "example.com", Transport: sig.NewStubTransport()})
	_, err := offerer.Start(ctx)
	assertErrorContains(t, err, "create data channel")
}

// TestRunOffererCreateOfferError verifies that a failure creating the offer is
// surfaced by the offerer rather than being swallowed.
func TestRunOffererCreateOfferError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	restore := injectFailingPC(t, "CreateOffer", errors.New("create offer boom"))
	defer restore()

	offerer := NewSession(RoleOfferer, Config{Room: "offer", Zone: "example.com", Transport: sig.NewStubTransport()})
	_, err := offerer.Start(ctx)
	assertErrorContains(t, err, "create offer")
}

// TestRunOffererSetLocalDescriptionError verifies that a failure setting the
// local offer is surfaced by the offerer.
func TestRunOffererSetLocalDescriptionError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	restore := injectFailingPC(t, "SetLocalDescription", errors.New("set local boom"))
	defer restore()

	offerer := NewSession(RoleOfferer, Config{Room: "local", Zone: "example.com", Transport: sig.NewStubTransport()})
	_, err := offerer.Start(ctx)
	assertErrorContains(t, err, "set local description")
}

// TestRunOffererPublishError verifies that an offerer surfaces a transport
// error from publishing its offer instead of blocking or swallowing it.
func TestRunOffererPublishError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	offerer := NewSession(RoleOfferer, Config{
		Room: "pub-offer", Zone: "example.com",
		Transport: failPublishTransport{base: sig.NewStubTransport(), pubErr: errors.New("publish offer boom")},
	})
	_, err := offerer.Start(ctx)
	assertErrorContains(t, err, "publish offer")
}

// TestStartNewPeerConnectionError verifies that an invalid ICE configuration
// (a TURN server without credentials) is surfaced by NewPeerConnection rather
// than being swallowed.
func TestStartNewPeerConnectionError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	offerer := NewSession(RoleOfferer, Config{
		Room: "bad-ice", Zone: "example.com",
		ICEServers: []webrtc.ICEServer{{
			URLs:           []string{"turn:example.com:3478"},
			CredentialType: webrtc.ICECredentialTypePassword,
		}},
	})
	_, err := offerer.Start(ctx)
	assertErrorContains(t, err, "new peer connection")
}

// TestRunAnswererCreateAnswerError verifies that a failure creating the answer
// is surfaced by the answerer rather than being swallowed.
func TestRunAnswererCreateAnswerError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	offer := makeValidOffer(t)
	restore := injectFailingPC(t, "CreateAnswer", errors.New("create answer boom"))
	defer restore()

	answerer := NewSession(RoleAnswerer, Config{Room: "cand", Zone: "example.com", Transport: fixedOfferTransport{offer: offer}})
	_, err := answerer.Start(ctx)
	assertErrorContains(t, err, "create answer")
}

// TestRunAnswererSetLocalDescriptionError verifies that a failure setting the
// local answer is surfaced by the answerer.
func TestRunAnswererSetLocalDescriptionError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	offer := makeValidOffer(t)
	restore := injectFailingPC(t, "SetLocalDescription", errors.New("set local answer boom"))
	defer restore()

	answerer := NewSession(RoleAnswerer, Config{Room: "local-ans", Zone: "example.com", Transport: fixedOfferTransport{offer: offer}})
	_, err := answerer.Start(ctx)
	assertErrorContains(t, err, "set local description")
}
