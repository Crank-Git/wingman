package peer

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"

	"github.com/Crank-Git/wingman/internal/metrics"
	"github.com/Crank-Git/wingman/internal/sig"
	"github.com/Crank-Git/wingman/internal/turnserver"
)

type missingTransportPC struct {
	peerConn
	sctp *webrtc.SCTPTransport
}

func (p missingTransportPC) SCTP() *webrtc.SCTPTransport  { return p.sctp }
func (p missingTransportPC) GetStats() webrtc.StatsReport { return nil }

func TestSelectedPairRejectsMissingTransports(t *testing.T) {
	api := webrtc.NewAPI()
	for _, pc := range []peerConn{
		missingTransportPC{},
		missingTransportPC{sctp: &webrtc.SCTPTransport{}},
		missingTransportPC{sctp: api.NewSCTPTransport(&webrtc.DTLSTransport{})},
	} {
		s := NewSession(RoleOfferer, Config{})
		s.pc = pc
		if _, ok := s.SelectedCandidatePair(); ok {
			t.Fatal("missing transport is available")
		}
	}
}

func TestSelectedPairResolvesCandidateTypes(t *testing.T) {
	for _, tc := range []struct {
		typ  webrtc.ICECandidateType
		want string
	}{
		{webrtc.ICECandidateTypeHost, "host"},
		{webrtc.ICECandidateTypeSrflx, "srflx"},
		{webrtc.ICECandidateTypePrflx, "prflx"},
		{webrtc.ICECandidateTypeRelay, "relay"},
		{webrtc.ICECandidateType(99), "unknown"},
	} {
		t.Run(tc.want, func(t *testing.T) {
			pair := webrtc.ICECandidatePairStats{LocalCandidateID: "local", RemoteCandidateID: "remote"}
			report := webrtc.StatsReport{
				"local":  webrtc.ICECandidateStats{CandidateType: tc.typ},
				"remote": webrtc.ICECandidateStats{CandidateType: webrtc.ICECandidateTypeRelay},
			}
			got, ok := candidatePairTypes(pair, report)
			if !ok || got.Local != tc.want || got.Remote != "relay" {
				t.Fatalf("pair = %+v, %v", got, ok)
			}
		})
	}
}

func TestSelectedPairRejectsMissingCandidateStats(t *testing.T) {
	pair := webrtc.ICECandidatePairStats{LocalCandidateID: "local", RemoteCandidateID: "remote"}
	for _, report := range []webrtc.StatsReport{
		nil,
		{"local": webrtc.ICECandidateStats{}},
		{"local": webrtc.CodecStats{}, "remote": webrtc.ICECandidateStats{}},
		{"local": webrtc.ICECandidateStats{}, "remote": webrtc.CodecStats{}},
	} {
		if got, ok := candidatePairTypes(pair, report); ok || got != (CandidatePairTypes{}) {
			t.Fatalf("missing stats = %+v, %v", got, ok)
		}
	}
}

func TestSelectedPairDoesNotUseGatheredCounts(t *testing.T) {
	m := metrics.New()
	m.RecordCandidate("relay")
	s := NewSession(RoleOfferer, Config{}).WithMetrics(m)
	if got, ok := s.SelectedCandidatePair(); ok || got != (CandidatePairTypes{}) {
		t.Fatalf("unconnected pair = %+v, %v", got, ok)
	}
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	s.pc = pc
	t.Cleanup(func() { _ = s.Close() })
	if _, ok := s.SelectedCandidatePair(); ok {
		t.Fatal("unconnected pair is available")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.SelectedCandidatePair(); ok {
		t.Fatal("closed pair is available")
	}
}

func TestRelayOnlyTransferReportsSelectedRelay(t *testing.T) {
	srv, err := turnserver.New(turnserver.Config{Addr: "127.0.0.1:0", Username: "local-peer", Password: "local-password"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	saved := newPeerConnection
	newPeerConnection = func(cfg webrtc.Configuration) (peerConn, error) {
		cfg.ICETransportPolicy = webrtc.ICETransportPolicyRelay
		return webrtc.NewPeerConnection(cfg)
	}
	t.Cleanup(func() { newPeerConnection = saved })
	transport := retryingTransport{base: sig.NewStubTransport()}
	cfg := Config{Room: "selected-relay", Transport: transport, ICEServers: []webrtc.ICEServer{{URLs: []string{srv.TURNURI()}, Username: "local-peer", Credential: "local-password"}}}
	a, b := NewSession(RoleOfferer, cfg), NewSession(RoleAnswerer, cfg)
	t.Cleanup(func() { _ = a.Close(); _ = b.Close() })
	received := make(chan string, 1)
	b.OnMessage(func(data []byte) { received <- string(data) })
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	runHandshake(t, ctx, a, b)
	const message = "known relay-only data"
	if err := a.SendText(message); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-received:
		if got != message {
			t.Fatalf("message = %q", got)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	for _, session := range []*Session{a, b} {
		got, ok := session.SelectedCandidatePair()
		if !ok || got.Local != "relay" || got.Remote != "relay" {
			t.Fatalf("selected pair = %+v, %v", got, ok)
		}
	}
	var observers sync.WaitGroup
	observers.Add(1)
	go func() {
		defer observers.Done()
		for i := 0; i < 100; i++ {
			a.SelectedCandidatePair()
		}
	}()
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	observers.Wait()
}
