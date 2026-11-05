package ice

import (
	"math"
	"net"
	"testing"
)

func TestTypePreferenceOrdersHostOverRelay(t *testing.T) {
	// host > prflx > srflx > relay, per RFC 8445 §4.1.2.2 and pion/ice.
	weights := []CandidateType{
		CandidateTypeHost, CandidateTypePeerReflexive,
		CandidateTypeServerReflexive, CandidateTypeRelay,
	}
	for i := 1; i < len(weights); i++ {
		if !(weights[i-1].Preference() > weights[i].Preference()) {
			t.Fatalf("%s (%d) should outrank %s (%d)",
				weights[i-1], weights[i-1].Preference(), weights[i], weights[i].Preference())
		}
	}
	if CandidateTypeRelay.Preference() != 0 {
		t.Fatalf("relay preference = %d, want 0", CandidateTypeRelay.Preference())
	}
	if CandidateTypePeerReflexive.Preference() != 110 {
		t.Fatalf("prflx preference = %d, want 110", CandidateTypePeerReflexive.Preference())
	}
}

func TestTypeStringRoundTrip(t *testing.T) {
	for _, ct := range []CandidateType{
		CandidateTypeHost, CandidateTypeServerReflexive,
		CandidateTypePeerReflexive, CandidateTypeRelay, CandidateTypeUnspecified,
	} {
		parsed, err := ParseCandidateType(ct.String())
		if err != nil {
			t.Fatalf("ParseCandidateType(%q): %v", ct, err)
		}
		if parsed != ct {
			t.Fatalf("round trip: %q -> %v -> %v", ct, parsed, ct)
		}
	}
}

func TestParseCandidateTypeAliases(t *testing.T) {
	for _, s := range []string{"PRFLX", "peer-reflexive", "PEERREFLEXIVE"} {
		if _, err := ParseCandidateType(s); err != nil {
			t.Fatalf("ParseCandidateType(%q): %v", s, err)
		}
	}
	for _, s := range []string{"SRFLX", "server-reflexive", "RELAY", "TURN"} {
		if _, err := ParseCandidateType(s); err != nil {
			t.Fatalf("ParseCandidateType(%q): %v", s, err)
		}
	}
}

func TestParseCandidateTypeInvalid(t *testing.T) {
	if _, err := ParseCandidateType("not-a-type"); err == nil {
		t.Fatal("expected error for invalid type")
	}
}

func TestCandidatePriorityMatchesPionFormula(t *testing.T) {
	// priority = (2^24)*typePreference + (2^8)*localPreference + (256 - component)
	got := mustCandidatePriority(t, 126, 65535, 1)
	want := uint32((1<<24)*126 + (1<<8)*65535 + (256 - 1))
	if got != want {
		t.Fatalf("priority = %d, want %d", got, want)
	}
}

func TestCandidatePriorityRejectsInvalidComponents(t *testing.T) {
	for _, component := range []uint16{0, 257, math.MaxUint16} {
		priority, err := CandidatePriority(126, 65535, component)
		if err == nil {
			t.Errorf("component %d: expected validation error", component)
		}
		if priority != 0 {
			t.Errorf("component %d: priority = %d, want 0", component, priority)
		}
	}
}

func TestCandidatePriorityPreservesValidComponents(t *testing.T) {
	for component := uint16(1); component <= 256; component++ {
		priority, err := CandidatePriority(126, 65535, component)
		if err != nil {
			t.Fatalf("component %d: %v", component, err)
		}
		want := uint32((1<<24)*126+(1<<8)*65535) + 256 - uint32(component)
		if priority != want {
			t.Errorf("component %d: priority = %d, want %d", component, priority, want)
		}
	}
}

func TestCandidatePriorityHostBeatsRelay(t *testing.T) {
	host := mustCandidatePriority(t, CandidateTypeHost.Preference(), 0, 1)
	relay := mustCandidatePriority(t, CandidateTypeRelay.Preference(), math.MaxUint16, 1)
	if host <= relay {
		t.Fatalf("host priority %d must exceed relay priority %d", host, relay)
	}
}

func TestCandidatePriorityLocalPreferenceBreaksTies(t *testing.T) {
	a := mustCandidatePriority(t, CandidateTypeHost.Preference(), 100, 1)
	b := mustCandidatePriority(t, CandidateTypeHost.Preference(), 200, 1)
	if a >= b {
		t.Fatalf("higher local preference should win: %d vs %d", a, b)
	}
}

func TestNewHost(t *testing.T) {
	c := NewHost(net.ParseIP("192.168.1.10"), 5000, 65535)
	if c.Type != CandidateTypeHost {
		t.Fatalf("type = %v, want host", c.Type)
	}
	if c.Address != "192.168.1.10" || c.Port != 5000 {
		t.Fatalf("address/port = %s:%d", c.Address, c.Port)
	}
	if c.Priority != mustCandidatePriority(t, 126, 65535, 1) {
		t.Fatal("priority not computed")
	}
}

func TestCandidateKeyAndEqual(t *testing.T) {
	a := NewHost(net.ParseIP("192.168.1.10"), 5000, 1)
	b := Candidate{Type: CandidateTypeHost, Network: "UDP", Address: "192.168.1.10", Port: 5000, Component: 1}
	c := Candidate{Type: CandidateTypeHost, Network: "udp", Address: "192.168.1.10", Port: 5001, Component: 1}

	if a.Key() != "udp:192.168.1.10:5000" {
		t.Fatalf("key = %q", a.Key())
	}
	if !a.Equal(b) {
		t.Fatal("network case must not affect equality")
	}
	if a.Equal(c) {
		t.Fatal("different ports must not be equal")
	}
}

func TestNetworkLower(t *testing.T) {
	if got := NewHost(net.ParseIP("10.0.0.1"), 1, 1).NetworkLower(); got != "udp" {
		t.Fatalf("NetworkLower = %q, want udp", got)
	}
}

func mustCandidatePriority(t *testing.T, typePreference, localPreference, component uint16) uint32 {
	t.Helper()
	priority, err := CandidatePriority(typePreference, localPreference, component)
	if err != nil {
		t.Fatal(err)
	}
	return priority
}
