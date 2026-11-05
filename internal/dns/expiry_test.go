package dns

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/miekg/dns"
)

func TestExpiryRemovesIdleEntries(t *testing.T) {
	for _, total := range []uint16{1, 3} {
		t.Run(string(rune('0'+total)), func(t *testing.T) {
			s := NewServer()
			now := time.Unix(1000, 0)
			s.now = func() time.Time { return now }
			directQuery(s, fragmentQuery("one.test", "first", 0, total))
			now = now.Add(119 * time.Second)
			s.expire(now)
			if len(s.frags) != 1 || s.retainedBytes != 5 {
				t.Fatal("entry expires before 120 seconds")
			}
			directQuery(s, fragmentQuery("one.test", "first", 0, total))
			now = now.Add(time.Second)
			s.expire(now)
			if len(s.frags) != 0 || s.retainedBytes != 0 {
				t.Fatal("idle entry retains fragments or bytes after expiry")
			}
		})
	}
}

func TestExpiryAllowsRoomReuse(t *testing.T) {
	s := NewServer()
	now := time.Unix(1000, 0)
	s.now = func() time.Time { return now }
	directQuery(s, fragmentQuery("one.test", "first", 0, 3))
	now = now.Add(120 * time.Second)
	if got := directQuery(s, queryMsg("room.offer.sig.one.test")); got.Rcode != dns.RcodeNameError {
		t.Fatal("expired entry remains visible before the sweep")
	}
	if got := directQuery(s, fragmentQuery("one.test", "new", 0, 1)); got.Rcode != dns.RcodeSuccess {
		t.Fatal("expired entry rejects a new total")
	}
	if s.retainedBytes != 3 {
		t.Fatalf("retained bytes = %d, want 3", s.retainedBytes)
	}
	now = now.Add(120 * time.Second)
	if got := directQuery(s, fragmentQuery("one.test", "replacement", 0, 2)); got.Rcode != dns.RcodeSuccess {
		t.Fatal("write does not expire the old entry")
	}
}

func TestExpirySweepStopsWithServer(t *testing.T) {
	s := NewServer()
	var seconds atomic.Int64
	s.now = func() time.Time { return time.Unix(seconds.Load(), 0) }
	ticks := make(chan time.Time)
	stopped := make(chan struct{})
	s.newTicker = func(interval time.Duration) (<-chan time.Time, func()) {
		if interval != time.Second {
			t.Fatalf("sweep interval = %v", interval)
		}
		return ticks, func() { close(stopped) }
	}
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Stop)
	directQuery(s, fragmentQuery("one.test", "first", 0, 3))
	seconds.Store(120)
	ticks <- time.Unix(120, 0)
	// A second receive proves that the first sweep completes.
	ticks <- time.Unix(120, 0)
	s.mu.Lock()
	remaining := len(s.frags)
	s.mu.Unlock()
	if remaining != 0 {
		t.Fatal("background sweep retains an idle entry")
	}
	s.Stop()
	s.Stop()
	select {
	case <-stopped:
	default:
		t.Fatal("Stop retains the ticker")
	}
	select {
	case ticks <- time.Unix(240, 0):
		t.Fatal("sweep receives a tick after Stop")
	default:
	}
}

func TestExpiryReleasesOnlyExpiredBytes(t *testing.T) {
	s := NewServer()
	now := time.Unix(1000, 0)
	s.now = func() time.Time { return now }
	directQuery(s, fragmentQuery("one.test", "first", 0, 3))
	directQuery(s, fragmentQuery("one.test", "second", 1, 3))
	directQuery(s, fragmentQuery("one.test", "conflict", 0, 3))
	now = now.Add(time.Second)
	directQuery(s, fragmentQuery("two.test", "new", 0, 1))
	s.expire(now.Add(119 * time.Second))
	if len(s.frags) != 1 || s.retainedBytes != 3 {
		t.Fatalf("remaining keys = %d, bytes = %d", len(s.frags), s.retainedBytes)
	}
}

func TestServerCancellationStopsSweep(t *testing.T) {
	s := NewServer()
	s.Stop()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Stop)
	if err := s.Start(ctx); err == nil {
		t.Fatal("Start accepts an active server")
	}
	cancel()
	select {
	case <-s.done:
	case <-time.After(5 * time.Second):
		t.Fatal("context cancellation retains the server")
	}
	var stops sync.WaitGroup
	for range 3 {
		stops.Go(s.Stop)
	}
	stops.Wait()
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestListenerReportsStartupFailure(t *testing.T) {
	if err := activateListener(&dns.Server{}); err == nil {
		t.Fatal("listener without a socket starts")
	}
}

func TestExpiryAllowsRoomReuseOverDNS(t *testing.T) {
	s := NewServer()
	var seconds atomic.Int64
	s.now = func() time.Time { return time.Unix(seconds.Load(), 0) }
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Stop)
	serveQuery(t, s, fragmentQuery("one.test", "first", 0, 3).Question[0].Name)
	seconds.Store(120)
	if got := serveQuery(t, s, "room.offer.sig.one.test"); got.Rcode != dns.RcodeNameError {
		t.Fatal("DNS poll returns an expired entry")
	}
	if got := serveQuery(t, s, fragmentQuery("one.test", "new", 0, 1).Question[0].Name); got.Rcode != dns.RcodeSuccess {
		t.Fatal("DNS write rejects room reuse")
	}
	if got := serveQuery(t, s, "room.offer.sig.one.test"); len(got.Answer) != 1 {
		t.Fatal("DNS poll does not return the replacement")
	}
}
