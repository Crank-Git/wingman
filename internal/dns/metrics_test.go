package dns

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Crank-Git/wingman/internal/metrics"
	mdns "github.com/miekg/dns"
)

func TestDNSMetricsCountSignedQueriesOnce(t *testing.T) {
	s := NewServer()
	s.Zone, s.TSIGKey, s.Metrics = "one.test", testTSIGKey(), metrics.New()
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer s.Stop()
	d := &Dialer{Addr: s.Addr(), TSIGKey: s.TSIGKey}
	for _, q := range []*mdns.Msg{fragmentQuery("one.test", "abc", 0, 1), queryMsg("room.offer.sig.one.test"), queryMsg("empty.offer.sig.one.test"), fragmentQuery("other.test", "x", 0, 1), queryMsg("bad.one.test")} {
		if _, err := d.Exchange(context.Background(), q); err != nil {
			t.Fatal(err)
		}
	}
	unsigned := &Dialer{Addr: s.Addr()}
	if _, err := unsigned.Exchange(context.Background(), fragmentQuery("one.test", "abc", 0, 1)); err != nil {
		t.Fatal(err)
	}
	wrongKey := *s.TSIGKey
	wrongKey.Secret = "d3Jvbmc="
	wrong := &Dialer{Addr: s.Addr(), TSIGKey: &wrongKey}
	if _, err := wrong.Exchange(context.Background(), queryMsg("room.offer.sig.one.test")); err == nil {
		t.Fatal("wrong key succeeds")
	}
	got := s.Metrics.Snapshot()
	if got.DNSRequests != [3][2]int64{{1, 2}, {2, 1}, {0, 1}} || got.DNSRetainedPayloadBytes != 3 {
		t.Fatal(got)
	}
	if strings.Contains(s.Metrics.Text(), "one.test") {
		t.Fatal("zone becomes a label")
	}
}

func TestDNSMetricsTrackRetentionMutations(t *testing.T) {
	s := NewServer()
	s.Metrics = metrics.New()
	now := time.Now()
	s.now = func() time.Time { return now }
	s.limits.retainedBytes = 3
	for _, step := range []struct {
		data       string
		seq, total uint16
		want       int64
	}{
		{"abc", 0, 1, 3}, {"abc", 0, 1, 3}, {"def", 0, 1, 3}, {"x", 1, 2, 3},
	} {
		directQuery(s, fragmentQuery("one.test", step.data, step.seq, step.total))
		if got := s.Metrics.Snapshot().DNSRetainedPayloadBytes; got != step.want || int(got) != s.retainedBytes {
			t.Fatalf("gauge=%d storage=%d", got, s.retainedBytes)
		}
	}
	directQuery(s, fragmentQuery("other.test", "x", 0, 1))
	if got := s.Metrics.Snapshot().DNSRetainedPayloadBytes; got != 3 {
		t.Fatal(got)
	}
	now = now.Add(fragmentLifetime)
	directQuery(s, fragmentQuery("one.test", "z", 0, 1))
	if got := s.Metrics.Snapshot().DNSRetainedPayloadBytes; got != 1 {
		t.Fatal(got)
	}
	s.expire(now.Add(fragmentLifetime))
	if got := s.Metrics.Snapshot().DNSRetainedPayloadBytes; got != 0 {
		t.Fatal(got)
	}
}

func TestDNSMetricsClassifyRejectedQueries(t *testing.T) {
	s := NewServer()
	s.Metrics = metrics.New()
	unsupported := queryMsg("room.offer.sig.one.test")
	unsupported.Question[0].Qtype = mdns.TypeA
	for _, q := range []*mdns.Msg{new(mdns.Msg), unsupported, queryMsg("bad..name"), queryMsg("room.unknown.sig.one.test"), queryMsg("bad.one.test")} {
		s.makeHandler(0)(&probeWriter{}, q)
	}
	s.limits.retainedBytes = 1
	s.makeHandler(0)(&probeWriter{}, fragmentQuery("one.test", "abc", 0, 1))
	if got := s.Metrics.Snapshot(); got.DNSRequests != [3][2]int64{{0, 1}, {0, 0}, {0, 5}} || got.DNSRetainedPayloadBytes != 0 {
		t.Fatal(got)
	}
}

func TestDNSMetricsKeepConcurrentRetentionOrder(t *testing.T) {
	s := NewServer()
	s.Metrics = metrics.New()
	var wg sync.WaitGroup
	for i := range 64 {
		wg.Go(func() {
			directQuery(s, fragmentQuery(fmt.Sprintf("zone%d.test", i), "abc", 0, 1))
			s.mu.Lock()
			defer s.mu.Unlock()
			if got := s.Metrics.Snapshot().DNSRetainedPayloadBytes; int(got) != s.retainedBytes {
				t.Errorf("gauge=%d storage=%d", got, s.retainedBytes)
			}
		})
	}
	wg.Wait()
	if got := s.Metrics.Snapshot().DNSRetainedPayloadBytes; got != 192 {
		t.Fatal(got)
	}
}
