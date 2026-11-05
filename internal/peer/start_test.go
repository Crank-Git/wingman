package peer

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"
)

func TestStartRejectsInvalidRoleBeforeAllocation(t *testing.T) {
	var allocations int
	saved := newPeerConnection
	newPeerConnection = func(webrtc.Configuration) (peerConn, error) {
		allocations++
		return nil, errors.New("allocation must not run")
	}
	t.Cleanup(func() { newPeerConnection = saved })
	for _, role := range []Role{-1, 2} {
		s := NewSession(role, Config{})
		_, err := s.Start(context.Background())
		if err == nil || !strings.Contains(err.Error(), "invalid role") {
			t.Errorf("role %d: error = %v, want invalid role", role, err)
		}
	}
	if allocations != 0 {
		t.Fatalf("allocations = %d, want 0", allocations)
	}
}

func TestStartRejectsRepeatAfterAllocationFailure(t *testing.T) {
	boom := errors.New("allocation fails")
	allocations := 0
	saved := newPeerConnection
	newPeerConnection = func(webrtc.Configuration) (peerConn, error) {
		allocations++
		return nil, boom
	}
	t.Cleanup(func() { newPeerConnection = saved })
	s := NewSession(RoleOfferer, Config{})
	if _, err := s.Start(context.Background()); !errors.Is(err, boom) {
		t.Fatalf("first error = %v, want original cause", err)
	}
	_, err := s.Start(context.Background())
	if err == nil || !strings.Contains(err.Error(), "already started") {
		t.Errorf("repeat error = %v, want already started", err)
	}
	if allocations != 1 {
		t.Fatalf("allocations = %d, want 1", allocations)
	}
}

func TestStartRejectsConcurrentAttemptBeforeAllocationCompletes(t *testing.T) {
	boom := errors.New("allocation fails")
	var allocations atomic.Int32
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	saved := newPeerConnection
	newPeerConnection = func(webrtc.Configuration) (peerConn, error) {
		allocations.Add(1)
		entered <- struct{}{}
		<-release
		return nil, boom
	}
	t.Cleanup(func() { newPeerConnection = saved })
	s := NewSession(RoleAnswerer, Config{})
	first := startInBackground(t, context.Background(), s)
	<-entered
	second := startInBackground(t, context.Background(), s)
	select {
	case err := <-second:
		if err == nil || !strings.Contains(err.Error(), "already started") {
			t.Errorf("concurrent error = %v, want already started", err)
		}
		close(release)
	case <-time.After(time.Second):
		close(release)
		<-second
		t.Error("concurrent Start waits for allocation")
	}
	if err := <-first; !errors.Is(err, boom) {
		t.Errorf("first error = %v, want original cause", err)
	}
	if got := allocations.Load(); got != 1 {
		t.Fatalf("allocations = %d, want 1", got)
	}
}

func TestStartRejectsRepeatAfterHandshakeFailure(t *testing.T) {
	boom := errors.New("handshake fails")
	restore := injectFailingPC(t, "CreateDataChannel", boom)
	defer restore()
	s := NewSession(RoleOfferer, Config{})
	if _, err := s.Start(context.Background()); !errors.Is(err, boom) {
		t.Fatalf("first error = %v, want original cause", err)
	}
	_, err := s.Start(context.Background())
	if err == nil || !strings.Contains(err.Error(), "already started") {
		t.Fatalf("repeat error = %v, want already started", err)
	}
}
