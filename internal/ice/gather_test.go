package ice

import (
	"context"
	"net"
	"testing"
)

// hasLoopback reports whether any gathered candidate is a loopback host. Almost
// every machine (and every CI runner) has at least one loopback interface, so
// this asserts a real, non-empty result rather than a synthetic one.
func TestGatherHostCandidatesIncludesLoopback(t *testing.T) {
	cands, err := GatherHostCandidates(context.Background(), nil, 65535)
	if err != nil {
		t.Fatalf("GatherHostCandidates: %v", err)
	}
	if len(cands) == 0 {
		t.Fatal("expected at least one host candidate")
	}
	for _, c := range cands {
		if c.Type != CandidateTypeHost {
			t.Fatalf("candidate %v is not host", c)
		}
		if ip := net.ParseIP(c.Address); ip == nil {
			t.Fatalf("candidate address %q is not a valid IP", c.Address)
		}
	}

	// Loopback must be present: it is the one path guaranteed on any host.
	found := false
	for _, c := range cands {
		if ip := net.ParseIP(c.Address); ip != nil && ip.IsLoopback() {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("no loopback host candidate found among %+v", cands)
	}
}

func TestGatherHostCandidatesNetworkFilter(t *testing.T) {
	udpOnly, err := GatherHostCandidates(context.Background(), []string{"udp"}, 65535)
	if err != nil {
		t.Fatalf("GatherHostCandidates(udp): %v", err)
	}
	for _, c := range udpOnly {
		if c.Network != "udp" {
			t.Fatalf("udp filter leaked %s candidate", c.Network)
		}
	}

	tcpOnly, err := GatherHostCandidates(context.Background(), []string{"tcp"}, 65535)
	if err != nil {
		t.Fatalf("GatherHostCandidates(tcp): %v", err)
	}
	for _, c := range tcpOnly {
		if c.Network != "tcp" {
			t.Fatalf("tcp filter leaked %s candidate", c.Network)
		}
	}
}

func TestGatherHostCandidatesDeduplicates(t *testing.T) {
	// Both "udp" and "tcp" enumeration may surface the same IP; the per-(network,
	// address) key must keep them distinct, and a single network must not repeat.
	once, err := GatherHostCandidates(context.Background(), []string{"udp"}, 65535)
	if err != nil {
		t.Fatalf("GatherHostCandidates: %v", err)
	}
	seen := make(map[string]struct{})
	for _, c := range once {
		if _, ok := seen[c.Key()]; ok {
			t.Fatalf("duplicate candidate key %q", c.Key())
		}
		seen[c.Key()] = struct{}{}
	}
}

func TestGatherHostCandidatesContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := GatherHostCandidates(ctx, nil, 65535); err == nil {
		t.Fatal("expected context-cancellation error")
	}
}
