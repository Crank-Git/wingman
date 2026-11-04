package ice

import (
	"net"
	"testing"
)

func host(addr string, port uint16, localPref uint16) Candidate {
	return NewHost(net.ParseIP(addr), port, localPref)
}

func TestRankHostBeforeRelay(t *testing.T) {
	relay := Candidate{Type: CandidateTypeRelay, Network: "udp", Address: "203.0.113.1", Port: 3478, Component: 1, Priority: mustCandidatePriority(t, 0, 65535, 1)}
	h1 := host("192.168.1.10", 5000, 65535)
	h2 := host("10.0.0.5", 5000, 65535)

	ranked := Rank([]Candidate{relay, h1, h2})
	if ranked[0].Type != CandidateTypeHost || ranked[1].Type != CandidateTypeHost {
		t.Fatalf("hosts not ranked first: %+v", ranked)
	}
	if ranked[2].Type != CandidateTypeRelay {
		t.Fatalf("relay not ranked last: %+v", ranked)
	}
}

func TestRankTieBreakByPriority(t *testing.T) {
	// Same type, different local preference: higher local preference wins.
	a := host("192.168.1.10", 5000, 100)
	b := host("192.168.1.11", 5000, 200)

	ranked := Rank([]Candidate{a, b})
	if ranked[0].Address != "192.168.1.11" {
		t.Fatalf("higher local preference should rank first, got %s", ranked[0].Address)
	}
}

func TestRankTieBreakByNetwork(t *testing.T) {
	// Same type and priority: network name orders the tie (tcp < udp).
	tcp := Candidate{Type: CandidateTypeHost, Network: "tcp", Address: "192.168.1.10", Port: 5000, Component: 1, Priority: mustCandidatePriority(t, 126, 65535, 1)}
	udp := Candidate{Type: CandidateTypeHost, Network: "udp", Address: "192.168.1.10", Port: 5000, Component: 1, Priority: mustCandidatePriority(t, 126, 65535, 1)}

	ranked := Rank([]Candidate{udp, tcp})
	if ranked[0].Network != "tcp" {
		t.Fatalf("tcp should precede udp on a priority tie, got %s", ranked[0].Network)
	}
}

func TestPreferred(t *testing.T) {
	relay := Candidate{Type: CandidateTypeRelay, Network: "udp", Address: "203.0.113.1", Port: 3478, Component: 1, Priority: mustCandidatePriority(t, 0, 65535, 1)}
	h := host("192.168.1.10", 5000, 65535)

	if got := Preferred([]Candidate{relay, h}); got.Type != CandidateTypeHost {
		t.Fatalf("Preferred = %v, want host", got.Type)
	}
	// nil input yields the zero Candidate (unspecified type, empty key).
	got := Preferred(nil)
	if got.Type != CandidateTypeUnspecified {
		t.Fatalf("Preferred(nil).Type = %v, want unspecified", got.Type)
	}
	if got.Key() != "::0" {
		t.Fatalf("Preferred(nil).Key = %q, want empty-key form", got.Key())
	}
}

func TestByType(t *testing.T) {
	h := host("192.168.1.10", 5000, 65535)
	relay := Candidate{Type: CandidateTypeRelay, Network: "udp", Address: "203.0.113.1", Port: 3478, Component: 1, Priority: mustCandidatePriority(t, 0, 65535, 1)}
	g := ByType([]Candidate{h, relay, h})

	if len(g[CandidateTypeHost]) != 2 {
		t.Fatalf("host group size = %d, want 2", len(g[CandidateTypeHost]))
	}
	if len(g[CandidateTypeRelay]) != 1 {
		t.Fatalf("relay group size = %d, want 1", len(g[CandidateTypeRelay]))
	}
}
