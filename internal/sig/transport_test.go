package sig

import (
	"context"
	"sync"
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

func TestStubTransportCopiesPublishedBytes(t *testing.T) {
	s := NewStubTransport()
	ctx := context.Background()
	msg := []byte("offer")
	if err := s.Publish(ctx, "room", Offer, msg); err != nil {
		t.Fatal(err)
	}
	msg[0] = 'x'
	got, err := s.Poll(ctx, "room", Offer)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "offer" {
		t.Fatalf("Poll = %q, want offer", got)
	}
}

func TestStubTransportCopiesPolledBytes(t *testing.T) {
	s := NewStubTransport()
	ctx := context.Background()
	if err := s.Publish(ctx, "room", Offer, []byte("offer")); err != nil {
		t.Fatal(err)
	}
	got, err := s.Poll(ctx, "room", Offer)
	if err != nil {
		t.Fatal(err)
	}
	got[0] = 'x'
	again, err := s.Poll(ctx, "room", Offer)
	if err != nil {
		t.Fatal(err)
	}
	if string(again) != "offer" {
		t.Fatalf("Poll = %q, want offer", again)
	}
}

func TestStubTransportSupportsConcurrentPublishAndPoll(t *testing.T) {
	s := NewStubTransport()
	ctx := context.Background()
	start := make(chan struct{})
	var workers sync.WaitGroup
	workers.Add(2)
	go func() {
		defer workers.Done()
		<-start
		for range 100 {
			if err := s.Publish(ctx, "room", Offer, []byte("offer")); err != nil {
				t.Error(err)
			}
		}
	}()
	go func() {
		defer workers.Done()
		<-start
		for range 100 {
			got, err := s.Poll(ctx, "room", Offer)
			if err == ErrNotFound {
				continue
			}
			if err != nil {
				t.Error(err)
				continue
			}
			if string(got) != "offer" {
				t.Errorf("Poll = %q, want offer", got)
			}
		}
	}()
	close(start)
	workers.Wait()
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
