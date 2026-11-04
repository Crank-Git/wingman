package dns

import (
	"bytes"
	"context"
	"encoding/base32"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Crank-Git/wingman/internal/sig"
	"github.com/miekg/dns"
)

func TestCapacityRejectsWithoutGrowth(t *testing.T) {
	for _, tc := range []struct {
		name          string
		limits        storageLimits
		first, second *dns.Msg
		wantBytes     int
	}{
		{"keys", storageLimits{64, 1, 64, 61440}, fragmentQuery("one.test", "abc", 0, 1), fragmentQuery("two.test", "d", 0, 1), 3},
		{"bytes", storageLimits{64, 2, 3, 61440}, fragmentQuery("one.test", "abc", 0, 2), fragmentQuery("one.test", "d", 1, 2), 3},
		{"message", storageLimits{32, 2, 64, 61440}, fragmentQuery("one.test", strings.Repeat("x", 35), 0, 2), fragmentQuery("one.test", "xx", 1, 2), 35},
		{"total", storageLimits{31, 2, 64, 61440}, nil, fragmentQuery("one.test", "x", 0, 2), 0},
		{"response", storageLimits{64, 2, 64, 1}, nil, fragmentQuery("one.test", "x", 0, 1), 0},
		{"fragment", storageLimits{64, 2, 64, 61440}, nil, queryMsg(sig.RendezvousName("one.test", "room", sig.Offer, 0, 1, strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(bytes.Repeat([]byte("x"), 36))))), 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := NewServer()
			s.limits = tc.limits
			if tc.first != nil {
				if r := directQuery(s, tc.first); r.Rcode != dns.RcodeSuccess {
					t.Fatal(r)
				}
			}
			keys := len(s.frags)
			if r := directQuery(s, tc.second); r.Rcode != dns.RcodeServerFailure {
				t.Fatalf("write: %v", r)
			}
			if len(s.frags) != keys || s.retainedBytes != tc.wantBytes {
				t.Fatalf("keys=%d bytes=%d", len(s.frags), s.retainedBytes)
			}
		})
	}
}

func TestCapacityKeepsDuplicatesAtQuota(t *testing.T) {
	s := NewServer()
	s.limits = storageLimits{31, 1, 3, 61440}
	q := fragmentQuery("one.test", "abc", 0, 1)
	for range 2 {
		if r := directQuery(s, q); r.Rcode != dns.RcodeSuccess {
			t.Fatal(r)
		}
	}
	if s.retainedBytes != 3 || len(s.frags) != 1 {
		t.Fatal("duplicate changes quota")
	}
	if r := directQuery(s, fragmentQuery("one.test", "xyz", 0, 1)); r.Rcode != dns.RcodeFormatError {
		t.Fatal(r)
	}
	if s.retainedBytes != 3 {
		t.Fatal("conflict changes quota")
	}
}

func TestCapacityReleasesExpiredQuota(t *testing.T) {
	s := NewServer()
	s.limits = storageLimits{31, 1, 3, 61440}
	now := time.Now()
	s.now = func() time.Time { return now }
	directQuery(s, fragmentQuery("one.test", "abc", 0, 1))
	now = now.Add(fragmentLifetime)
	if r := directQuery(s, fragmentQuery("two.test", "xyz", 0, 1)); r.Rcode != dns.RcodeSuccess {
		t.Fatal(r)
	}
	if s.retainedBytes != 3 || len(s.frags) != 1 {
		t.Fatal("expiry retains quota")
	}
}

func TestCapacityAdmitsConcurrentWritesAtomically(t *testing.T) {
	s := NewServer()
	s.limits = storageLimits{31, 100, 7, 61440}
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func() { defer wg.Done(); directQuery(s, fragmentQuery(string(rune('a'+i))+".test", "x", 0, 1)) }()
	}
	wg.Wait()
	if s.retainedBytes != 7 || len(s.frags) != 7 {
		t.Fatalf("bytes=%d keys=%d", s.retainedBytes, len(s.frags))
	}
}

func TestCapacityUsesCanonicalResponseBudget(t *testing.T) {
	key := storageKey{"one.test", "room", sig.Offer}
	total := uint16(2)
	budget := pollResponseSize(key, total)
	for _, delta := range []int{0, -1} {
		s := NewServer()
		s.limits.responseBytes = budget + delta
		q := fragmentQuery("one.test", "x", 0, total)
		q.Question[0].Name = strings.ToUpper(q.Question[0].Name)
		want := dns.RcodeSuccess
		if delta < 0 {
			want = dns.RcodeServerFailure
		}
		if r := directQuery(s, q); r.Rcode != want {
			t.Fatal(r)
		}
	}
	s := NewServer()
	payload := strings.Repeat("x", 35)
	for seq := range total {
		directQuery(s, fragmentQuery("one.test", payload, seq, total))
	}
	r := directQuery(s, queryMsg(sig.PollName(key.zone, key.room, key.kind)))
	packed, err := r.Pack()
	if err != nil {
		t.Fatal(err)
	}
	if len(packed) != budget {
		t.Fatalf("packed=%d budget=%d", len(packed), budget)
	}
}

func TestPublishReportsEffectiveLimitBeforeExchange(t *testing.T) {
	for _, zone := range []string{"example.com", strings.Repeat("z", 63) + "." + strings.Repeat("z", 40)} {
		key := storageKey{zone, "room", sig.Offer}
		limit := messageLimit(key, defaultStorageLimits())
		r := &cancelPublishResolver{cancel: func() {}}
		tr := &WriteOverQuery{Zone: zone, Resolver: r}
		if err := tr.Publish(context.Background(), "room", sig.Offer, bytes.Repeat([]byte("x"), limit+1)); err == nil || !strings.Contains(err.Error(), "limit") {
			t.Fatalf("error=%v", err)
		}
		if r.calls != 0 {
			t.Fatal("oversize message reaches resolver")
		}
		if limit >= 16384 {
			t.Fatal("response budget does not reduce limit")
		}
		if err := tr.Publish(context.Background(), "room", sig.Offer, bytes.Repeat([]byte("x"), maxMessageBytes+1)); err == nil {
			t.Fatal("message exceeds 16 KiB")
		}
	}
}

func TestCapacityReducesLimitForLongNames(t *testing.T) {
	limits := defaultStorageLimits()
	short := storageKey{"one.test", "room", sig.Offer}
	long := storageKey{strings.Repeat("z", 63) + ".test", strings.Repeat("r", 63), sig.Offer}
	shortLimit, longLimit := messageLimit(short, limits), messageLimit(long, limits)
	if longLimit >= shortLimit || longLimit == 0 {
		t.Fatalf("short limit=%d long limit=%d", shortLimit, longLimit)
	}
	total := uint16(longLimit / messageChunkBytes)
	s := NewServer()
	payload, err := sig.EncodeLabel([]byte("x"))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		total uint16
		code  int
	}{{total, dns.RcodeSuccess}, {total + 1, dns.RcodeServerFailure}} {
		name := sig.RendezvousName(long.zone, long.room, long.kind, 0, tc.total, payload)
		if tc.code == dns.RcodeServerFailure {
			s = NewServer()
		}
		if got := directQuery(s, queryMsg(name)); got.Rcode != tc.code {
			t.Fatal(got)
		}
		if tc.code == dns.RcodeServerFailure && len(s.frags) != 0 {
			t.Fatal("response rejection retains a key")
		}
	}
}

func TestCapacityRoundTripAtEffectiveLimit(t *testing.T) {
	s := newTestServer(t)
	tr := newClient(s)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	limit := messageLimit(storageKey{tr.Zone, "room", sig.Offer}, defaultStorageLimits())
	msg := bytes.Repeat([]byte("x"), limit)
	if err := tr.Publish(ctx, "room", sig.Offer, msg); err != nil {
		t.Fatal(err)
	}
	got, err := tr.Poll(ctx, "room", sig.Offer)
	if err != nil || !bytes.Equal(got, msg) {
		t.Fatalf("poll bytes=%d error=%v", len(got), err)
	}
}
