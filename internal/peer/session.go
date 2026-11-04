// Package peer drives a complete WebRTC offer/answer/ICE handshake over a
// sig.Transport and exposes a data channel once the connection is established.
//
// The handshake uses non-trickle ICE: all host candidates are gathered and
// embedded in the exchanged SDP, so no per-candidate signaling is required.
// This keeps the DNS signaling minimal (one Offer and one Answer per room)
// while remaining correct for the local P2P case that defines the MVP.
package peer

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/pion/webrtc/v4"

	"github.com/Crank-Git/wingman/internal/metrics"
	"github.com/Crank-Git/wingman/internal/sig"
)

// defaultLabel is the data channel label used when Config.Label is empty.
const defaultLabel = "wingman"

// iceGatherTimeout bounds how long Start waits for host candidates to be
// gathered before proceeding. Host gathering on loopback is fast; this only
// guards against machines whose interfaces never report a complete state.
const iceGatherTimeout = 10 * time.Second

// ErrAlreadyStarted reports a second startup attempt on a Session.
var ErrAlreadyStarted = errors.New("peer: session already started")

// peerConn is the subset of *webrtc.PeerConnection used by the handshake. It is
// a seam: newPeerConnection is a package variable so tests can substitute a
// fake and drive the failure branches (CreateDataChannel/CreateOffer/SetLocal
// Description/CreateAnswer failures) deterministically instead of racing the
// real ICE machinery.
type peerConn interface {
	Close() error
	OnConnectionStateChange(func(webrtc.PeerConnectionState))
	OnDataChannel(func(*webrtc.DataChannel))
	OnICECandidate(func(*webrtc.ICECandidate))
	CreateDataChannel(label string, options *webrtc.DataChannelInit) (*webrtc.DataChannel, error)
	CreateOffer(options *webrtc.OfferOptions) (webrtc.SessionDescription, error)
	CreateAnswer(options *webrtc.AnswerOptions) (webrtc.SessionDescription, error)
	SetLocalDescription(desc webrtc.SessionDescription) error
	LocalDescription() *webrtc.SessionDescription
	SetRemoteDescription(desc webrtc.SessionDescription) error
	ICEGatheringState() webrtc.ICEGatheringState
}

// newPeerConnection constructs a peer connection. It is a package variable so
// tests can inject a fake that fails specific methods (see peerConn above).
var newPeerConnection = func(cfg webrtc.Configuration) (peerConn, error) {
	return webrtc.NewPeerConnection(cfg)
}

// Role identifies whether a peer is the offerer (role A) or answerer (role B).
// The offerer creates the data channel; the answerer receives it.
type Role int

const (
	// RoleOfferer initiates the session and owns the data channel.
	RoleOfferer Role = iota
	// RoleAnswerer waits for an offer, then responds.
	RoleAnswerer
)

// String returns "offer" or "answer".
func (r Role) String() string {
	if r == RoleAnswerer {
		return "answer"
	}
	return "offer"
}

// Config holds everything a Session needs to run its handshake.
type Config struct {
	// Room is the signaling room name (shared by both peers).
	Room string
	// Zone is the authoritative DNS zone for the signaling server.
	Zone string
	// Transport carries the Offer/Answer exchange.
	Transport sig.Transport
	// ICEServers is optional. Empty yields host candidates only; populate it
	// for STUN/TURN (see M5).
	ICEServers []webrtc.ICEServer
	// Label is the data channel label. Defaults to "wingman".
	Label string
}

// MessageHandler receives data delivered over the data channel.
type MessageHandler func(data []byte)

// Session drives a single offer/answer/ICE handshake and owns the resulting
// data channel. Start permits one attempt, including an attempt that fails.
// Concurrent Start calls return ErrAlreadyStarted after the first call reserves startup.
type Session struct {
	cfg   Config
	role  Role
	label string

	// mu guards started, pc, dataChan, onMessage, and met.
	mu        sync.Mutex
	started   bool
	pc        peerConn
	dataChan  *webrtc.DataChannel
	onMessage MessageHandler
	met       *metrics.Metrics // mu guards collector replacement and callback snapshots.

	openCh chan struct{}
}

// NewSession creates a Session for the given role and configuration.
func NewSession(role Role, cfg Config) *Session {
	label := cfg.Label
	if label == "" {
		label = defaultLabel
	}
	return &Session{
		cfg:    cfg,
		role:   role,
		label:  label,
		openCh: make(chan struct{}),
	}
}

// OnMessage registers a handler for data received over the data channel.
func (s *Session) OnMessage(h MessageHandler) {
	s.mu.Lock()
	s.onMessage = h
	s.mu.Unlock()
}

// WithMetrics sets the collector and returns the session.
// It is safe to call during ICE callbacks. A nil collector disables observations.
func (s *Session) WithMetrics(m *metrics.Metrics) *Session {
	s.mu.Lock()
	s.met = m
	s.mu.Unlock()
	return s
}

// DataChannel returns the connected data channel, if the session has completed.
func (s *Session) DataChannel() (*webrtc.DataChannel, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dataChan == nil {
		return nil, false
	}
	return s.dataChan, true
}

// SendText sends a UTF-8 message over the data channel.
func (s *Session) SendText(text string) error {
	s.mu.Lock()
	dc := s.dataChan
	s.mu.Unlock()
	if dc == nil {
		return errNotOpen
	}
	return dc.SendText(text)
}

// Start runs the full handshake and blocks until the data channel is open or
// ctx is canceled. It returns the open data channel on success.
// An invalid role fails before allocation. A repeated call returns ErrAlreadyStarted.
func (s *Session) Start(ctx context.Context) (*webrtc.DataChannel, error) {
	if err := s.reserveStart(); err != nil {
		return nil, err
	}
	pc, err := newPeerConnection(configuration(s.cfg))
	if err != nil {
		return nil, fmt.Errorf("peer: new peer connection: %w", err)
	}
	s.mu.Lock()
	s.pc = pc
	s.mu.Unlock()

	done := make(chan error, 1)
	// signal records the outcome without touching the peer connection. On
	// success the channel must stay open so the caller can use it.
	signal := func(err error) {
		select {
		case <-done:
		default:
			done <- err
		}
	}
	// fail records an error and tears the connection down. Signal first: closing
	// the peer connection fires OnConnectionStateChange(Closed), which would
	// otherwise race this signal and win the buffered channel, masking the real
	// handshake error with "connection closed".
	fail := func(err error) {
		signal(err)
		_ = pc.Close()
	}

	pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		switch state {
		case webrtc.PeerConnectionStateFailed, webrtc.PeerConnectionStateClosed:
			fail(fmt.Errorf("peer: connection %s", state))
		}
	})

	pc.OnDataChannel(func(dc *webrtc.DataChannel) {
		s.setDataChannel(dc)
		s.watchChannel(dc)
	})

	// Record each gathered candidate's type so callers can report the
	// candidate distribution. The handler fires with a nil candidate once
	// gathering completes, which we ignore.
	pc.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c == nil {
			return
		}
		s.mu.Lock()
		m := s.met
		s.mu.Unlock()
		if m != nil {
			m.RecordCandidate(c.Typ.String())
		}
	})

	go func() {
		var handshakeErr error
		switch s.role {
		case RoleOfferer:
			handshakeErr = s.runOfferer(ctx, pc)
		case RoleAnswerer:
			handshakeErr = s.runAnswerer(ctx, pc)
		}
		if handshakeErr != nil {
			fail(handshakeErr)
			return
		}
		select {
		case <-s.openCh:
			// The data channel opened. Signal success without closing the peer
			// connection: the caller needs the channel to stay alive.
			signal(nil)
		case <-ctx.Done():
			fail(ctx.Err())
		}
	}()

	if err := <-done; err != nil {
		return nil, err
	}
	dc, ok := s.DataChannel()
	if !ok {
		return nil, errNotOpen
	}
	return dc, nil
}

func (s *Session) reserveStart() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.role != RoleOfferer && s.role != RoleAnswerer {
		return fmt.Errorf("peer: invalid role %d", s.role)
	}
	if s.started {
		return ErrAlreadyStarted
	}
	// Allocation can fail, but the Session still owns this attempt.
	s.started = true
	return nil
}

// runOfferer creates the data channel, publishes the offer, waits for the
// answer, and returns. ICE then proceeds and the channel opens.
func (s *Session) runOfferer(ctx context.Context, pc peerConn) error {
	dc, err := pc.CreateDataChannel(s.label, nil)
	if err != nil {
		return fmt.Errorf("peer: create data channel: %w", err)
	}
	s.setDataChannel(dc)
	s.watchChannel(dc)

	offer, err := pc.CreateOffer(nil)
	if err != nil {
		return fmt.Errorf("peer: create offer: %w", err)
	}
	if err := pc.SetLocalDescription(offer); err != nil {
		return fmt.Errorf("peer: set local description: %w", err)
	}
	if err := s.waitGathering(ctx, pc); err != nil {
		return err
	}
	full, err := encodeSDP(*pc.LocalDescription())
	if err != nil {
		return err
	}
	if err := s.cfg.Transport.Publish(ctx, s.cfg.Room, sig.Offer, full); err != nil {
		return fmt.Errorf("peer: publish offer: %w", err)
	}

	ans, err := s.cfg.Transport.Poll(ctx, s.cfg.Room, sig.Answer)
	if err != nil {
		return fmt.Errorf("peer: poll answer: %w", err)
	}
	answer, err := decodeSDP(ans)
	if err != nil {
		return err
	}
	if err := pc.SetRemoteDescription(answer); err != nil {
		return fmt.Errorf("peer: set remote answer: %w", err)
	}
	return nil
}

// runAnswerer waits for the offer, responds with an answer, and returns. The
// offerer-created data channel arrives via OnDataChannel.
func (s *Session) runAnswerer(ctx context.Context, pc peerConn) error {
	offer, err := s.cfg.Transport.Poll(ctx, s.cfg.Room, sig.Offer)
	if err != nil {
		return fmt.Errorf("peer: poll offer: %w", err)
	}
	sd, err := decodeSDP(offer)
	if err != nil {
		return err
	}
	if err := pc.SetRemoteDescription(sd); err != nil {
		return fmt.Errorf("peer: set remote offer: %w", err)
	}
	answer, err := pc.CreateAnswer(nil)
	if err != nil {
		return fmt.Errorf("peer: create answer: %w", err)
	}
	if err := pc.SetLocalDescription(answer); err != nil {
		return fmt.Errorf("peer: set local description: %w", err)
	}
	if err := s.waitGathering(ctx, pc); err != nil {
		return err
	}
	full, err := encodeSDP(*pc.LocalDescription())
	if err != nil {
		return err
	}
	if err := s.cfg.Transport.Publish(ctx, s.cfg.Room, sig.Answer, full); err != nil {
		return fmt.Errorf("peer: publish answer: %w", err)
	}
	return nil
}

// waitGathering blocks until ICE host gathering completes or ctx/timeout fires.
func (s *Session) waitGathering(ctx context.Context, pc peerConn) error {
	deadline := time.After(iceGatherTimeout)
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		if pc.ICEGatheringState() == webrtc.ICEGatheringStateComplete {
			return nil
		}
		select {
		case <-deadline:
			return fmt.Errorf("peer: ICE gathering timed out")
		case <-ticker.C:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// setDataChannel records the session's data channel.
func (s *Session) setDataChannel(dc *webrtc.DataChannel) {
	s.mu.Lock()
	s.dataChan = dc
	s.mu.Unlock()
}

// watchChannel registers open/message/close callbacks on a data channel.
func (s *Session) watchChannel(dc *webrtc.DataChannel) {
	dc.OnOpen(func() {
		select {
		case <-s.openCh:
		default:
			close(s.openCh)
		}
	})
	dc.OnMessage(func(msg webrtc.DataChannelMessage) {
		s.mu.Lock()
		h := s.onMessage
		s.mu.Unlock()
		if h != nil {
			h(append([]byte(nil), msg.Data...))
		}
	})
}

// configuration builds a pion Configuration from the peer config.
func configuration(cfg Config) webrtc.Configuration {
	return webrtc.Configuration{
		ICEServers: cfg.ICEServers,
	}
}
