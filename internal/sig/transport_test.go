package sig

import (
	"context"
	"testing"
)

func TestStubTransportPublishPoll(t *testing.T) {
	s := NewStubTransport()
	msg := []byte("session-description-payload")
	ctx := context.Background()

	if _, err := s.Poll(ctx, "room", Offer); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}

	if err := s.Publish(ctx, "room", Offer, msg); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	got, err := s.Poll(ctx, "room", Offer)
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if string(got) != string(msg) {
		t.Fatalf("poll = %q, want %q", got, msg)
	}
}

func TestStubTransportIsolation(t *testing.T) {
	s := NewStubTransport()
	ctx := context.Background()
	_ = s.Publish(ctx, "roomA", Offer, []byte("a"))

	if _, err := s.Poll(ctx, "roomB", Offer); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound for different room, got %v", err)
	}
	if _, err := s.Poll(ctx, "roomA", Answer); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound for different kind, got %v", err)
	}
}
