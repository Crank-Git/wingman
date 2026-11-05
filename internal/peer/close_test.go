package peer

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Crank-Git/wingman/internal/sig"
	"github.com/pion/webrtc/v4"
)

type closePC struct {
	peerConn
	closes   atomic.Int32
	callback func(webrtc.PeerConnectionState)
	closeErr error
	startErr error
	states   []webrtc.PeerConnectionState
}

func (p *closePC) Close() error {
	p.closes.Add(1)
	if p.callback != nil {
		p.callback(webrtc.PeerConnectionStateClosed)
	}
	return p.closeErr
}
func (p *closePC) OnConnectionStateChange(h func(webrtc.PeerConnectionState)) {
	p.callback = h
	for _, state := range p.states {
		h(state)
	}
}
func (p *closePC) OnDataChannel(func(*webrtc.DataChannel))   {}
func (p *closePC) OnICECandidate(func(*webrtc.ICECandidate)) {}
func (p *closePC) CreateDataChannel(string, *webrtc.DataChannelInit) (*webrtc.DataChannel, error) {
	return nil, p.startErr
}

func TestCloseBeforeStartPreventsAllocation(t *testing.T) {
	s := NewSession(RoleOfferer, Config{})
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	_, err := s.Start(context.Background())
	if !errors.Is(err, ErrClosed) {
		t.Fatalf("Start error = %v", err)
	}
}

func TestCloseDuringAllocationReleasesConnection(t *testing.T) {
	p := &closePC{}
	entered, release := make(chan struct{}), make(chan struct{})
	saved := newPeerConnection
	newPeerConnection = func(webrtc.Configuration) (peerConn, error) {
		close(entered)
		<-release
		return p, nil
	}
	t.Cleanup(func() { newPeerConnection = saved })
	s := NewSession(RoleOfferer, Config{})
	done := startInBackground(t, context.Background(), s)
	<-entered
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-done; !errors.Is(err, ErrClosed) {
		t.Fatalf("Start error = %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if p.closes.Load() != 1 {
		t.Fatalf("close count = %d", p.closes.Load())
	}
}

func TestFailedStartClosesOnceAndPreservesCause(t *testing.T) {
	boom := errors.New("handshake fails")
	p := &closePC{startErr: boom, closeErr: errors.New("close fails")}
	saved := newPeerConnection
	newPeerConnection = func(webrtc.Configuration) (peerConn, error) { return p, nil }
	t.Cleanup(func() { newPeerConnection = saved })
	s := NewSession(RoleOfferer, Config{})
	if _, err := s.Start(context.Background()); !errors.Is(err, boom) {
		t.Fatalf("Start error = %v", err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _ = s.Close() }()
	}
	wg.Wait()
	if p.closes.Load() != 1 {
		t.Fatalf("close count = %d", p.closes.Load())
	}
}

type closePollTransport struct{ entered chan struct{} }

func (p closePollTransport) Publish(context.Context, string, sig.Kind, []byte) error { return nil }
func (p closePollTransport) Poll(ctx context.Context, _ string, _ sig.Kind) ([]byte, error) {
	close(p.entered)
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestCloseCancelsStartup(t *testing.T) {
	p := &closePC{}
	saved := newPeerConnection
	newPeerConnection = func(webrtc.Configuration) (peerConn, error) { return p, nil }
	t.Cleanup(func() { newPeerConnection = saved })
	tr := closePollTransport{entered: make(chan struct{})}
	s := NewSession(RoleAnswerer, Config{Transport: tr})
	done := startInBackground(t, context.Background(), s)
	<-tr.entered
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Start error = %v", err)
	}
	if p.closes.Load() != 1 {
		t.Fatalf("close count = %d", p.closes.Load())
	}
}

func TestCancellationReleasesConnectionOnce(t *testing.T) {
	p := &closePC{}
	saved := newPeerConnection
	newPeerConnection = func(webrtc.Configuration) (peerConn, error) { return p, nil }
	t.Cleanup(func() { newPeerConnection = saved })
	tr := closePollTransport{entered: make(chan struct{})}
	s := NewSession(RoleAnswerer, Config{Transport: tr})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := startInBackground(t, ctx, s)
	<-tr.entered
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Start error = %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if p.closes.Load() != 1 {
		t.Fatalf("close count = %d", p.closes.Load())
	}
}

func TestStartPreservesFirstConnectionOutcome(t *testing.T) {
	p := &closePC{startErr: errors.New("later handshake error"), states: []webrtc.PeerConnectionState{
		webrtc.PeerConnectionStateFailed, webrtc.PeerConnectionStateClosed,
	}}
	saved := newPeerConnection
	newPeerConnection = func(webrtc.Configuration) (peerConn, error) { return p, nil }
	t.Cleanup(func() { newPeerConnection = saved })
	s := NewSession(RoleOfferer, Config{})
	_, err := s.Start(context.Background())
	if err == nil || err.Error() != "peer: connection failed" {
		t.Fatalf("Start error = %v", err)
	}
	if p.closes.Load() != 1 {
		t.Fatalf("close count = %d", p.closes.Load())
	}
}

func TestClosePreservesConnectionCause(t *testing.T) {
	boom := errors.New("close fails")
	p := &closePC{closeErr: boom}
	s := NewSession(RoleOfferer, Config{})
	s.pc = p
	if err := s.Close(); !errors.Is(err, boom) {
		t.Fatalf("Close error = %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if p.closes.Load() != 1 {
		t.Fatalf("close count = %d", p.closes.Load())
	}
}

func TestCloseAfterTextExchangeReleasesConnection(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	tr := retryingTransport{base: sig.NewStubTransport()}
	a := NewSession(RoleOfferer, Config{Room: "close-text", Transport: tr})
	b := NewSession(RoleAnswerer, Config{Room: "close-text", Transport: tr})
	t.Cleanup(func() { _ = a.Close(); _ = b.Close() })
	received := make(chan string, 1)
	b.OnMessage(func(data []byte) { received <- string(data) })
	runHandshake(t, ctx, a, b)
	a.mu.Lock()
	pc := a.pc.(*webrtc.PeerConnection)
	a.mu.Unlock()
	if err := a.SendText("lifecycle text"); err != nil {
		t.Fatal(err)
	}
	select {
	case text := <-received:
		if text != "lifecycle text" {
			t.Fatal(text)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	if pc.ConnectionState() != webrtc.PeerConnectionStateClosed {
		t.Fatal("connection remains open")
	}
	if _, ok := a.DataChannel(); ok {
		t.Fatal("closed session exposes channel")
	}
	if err := a.SendText("closed"); !errors.Is(err, errNotOpen) {
		t.Fatalf("SendText error = %v", err)
	}
}
