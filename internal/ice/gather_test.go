package ice

import (
	"context"
	"errors"
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

// TestGatherHostCandidatesDeterministic substitutes a fixed set of interfaces
// so the parsing branches that depend on the host's live network configuration
// are exercised on every machine and CI runner: a down interface (skipped), an
// unspecified 0.0.0.0 address (skipped), and both *net.IPNet and *net.IPAddr
// address types (parsed).
func TestGatherHostCandidatesDeterministic(t *testing.T) {
	orig := enumerateInterfaces
	defer func() { enumerateInterfaces = orig }()

	down := net.Interface{Name: "down0", Flags: 0} // FlagUp not set -> skipped
	lo := net.Interface{Name: "lo", Flags: net.FlagUp | net.FlagLoopback}
	eth := net.Interface{Name: "eth0", Flags: net.FlagUp}
	dummy := net.Interface{Name: "dummy0", Flags: net.FlagUp}

	// 127.0.0.1 arrives as an *net.IPNet; 192.168.1.9 as an *net.IPAddr, so both
	// type-switch cases are covered. 0.0.0.0 must be skipped as unspecified.
	enumerateInterfaces = func() ([]ifaceAddr, error) {
		return []ifaceAddr{
			{iface: down, addrs: []net.Addr{&net.IPNet{IP: net.ParseIP("10.0.0.5"), Mask: net.CIDRMask(24, 32)}}},
			{iface: lo, addrs: []net.Addr{&net.IPNet{IP: net.ParseIP("127.0.0.1"), Mask: net.CIDRMask(8, 32)}}},
			{iface: eth, addrs: []net.Addr{&net.IPAddr{IP: net.ParseIP("192.168.1.9")}}},
			{iface: dummy, addrs: []net.Addr{&net.IPNet{IP: net.ParseIP("0.0.0.0"), Mask: net.CIDRMask(0, 32)}}},
		}, nil
	}

	cands, err := GatherHostCandidates(context.Background(), nil, 100)
	if err != nil {
		t.Fatalf("GatherHostCandidates: %v", err)
	}

	addrs := map[string]bool{}
	for _, c := range cands {
		addrs[c.Address] = true
	}

	// The down interface's 10.0.0.5 and the unspecified 0.0.0.0 must not appear.
	if addrs["10.0.0.5"] {
		t.Errorf("down-interface candidate 10.0.0.5 should have been skipped")
	}
	if addrs["0.0.0.0"] {
		t.Errorf("unspecified 0.0.0.0 should have been skipped")
	}
	// The loopback (IPNet) and ethernet (IPAddr) candidates must both appear,
	// proving both address-type branches were parsed.
	if !addrs["127.0.0.1"] {
		t.Error("missing loopback 127.0.0.1 candidate (IPNet branch)")
	}
	if !addrs["192.168.1.9"] {
		t.Error("missing 192.168.1.9 candidate (IPAddr branch)")
	}
}

// TestGatherHostCandidatesEnumerateError checks that a failure enumerating the
// interfaces is surfaced rather than swallowed.
func TestGatherHostCandidatesEnumerateError(t *testing.T) {
	orig := enumerateInterfaces
	defer func() { enumerateInterfaces = orig }()

	enumerateInterfaces = func() ([]ifaceAddr, error) {
		return nil, errors.New("enumerate boom")
	}

	if _, err := GatherHostCandidates(context.Background(), nil, 100); err == nil {
		t.Fatal("expected enumerateInterfaces error to surface")
	}
}
