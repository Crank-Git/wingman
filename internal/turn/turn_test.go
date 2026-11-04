package turn

import (
	"strings"
	"testing"
)

func TestParseValidURIs(t *testing.T) {
	cases := []struct {
		uri        string
		wantScheme string
		wantHost   string
		wantPort   int
	}{
		{"stun:example.com:3478", "stun", "example.com", 3478},
		{"turn:example.com:3478", "turn", "example.com", 3478},
		{"turns:turn.example.org:443", "turns", "turn.example.org", 443},
		{"turn:192.168.1.5:3478", "turn", "192.168.1.5", 3478},
		// IPv6 literals keep their brackets in the normalized URI so the result
		// stays parseable (a bare "::1" would become the unparseable ":::1").
		{"stun:[::1]:3478", "stun", "[::1]", 3478},
		{"turn:[2001:db8::1]:3478", "turn", "[2001:db8::1]", 3478},
		// A missing port defaults to the conventional TURN port.
		{"turn:example.com", "turn", "example.com", 3478},
	}
	for _, tc := range cases {
		s, err := Parse(tc.uri, "user", "pass")
		if err != nil {
			t.Fatalf("Parse(%q): unexpected error: %v", tc.uri, err)
		}
		if len(s.URIs) != 1 {
			t.Fatalf("Parse(%q): expected 1 URI, got %d", tc.uri, len(s.URIs))
		}
		got := s.URIs[0]
		want := tc.wantScheme + ":" + tc.wantHost + ":" + itoa(tc.wantPort)
		if got != want {
			t.Fatalf("Parse(%q): URI = %q, want %q", tc.uri, got, want)
		}
		if s.Username != "user" || s.Password != "pass" {
			t.Fatalf("Parse(%q): credentials = %q/%q", tc.uri, s.Username, s.Password)
		}
	}
}

// TestParseIPv6URIRoundTrips verifies that a normalized IPv6 URI is itself
// parseable. This catches the regression where the host was emitted without
// brackets ("stun:::1"), which is unparseable and would be dropped by the ICE
// engine instead of yielding srflx/relay candidates.
func TestParseIPv6URIRoundTrips(t *testing.T) {
	for _, uri := range []string{"stun:[::1]:3478", "turn:[2001:db8::1]:3478"} {
		s, err := Parse(uri, "u", "p")
		if err != nil {
			t.Fatalf("Parse(%q): %v", uri, err)
		}
		if _, err := Parse(s.URIs[0], "u", "p"); err != nil {
			t.Fatalf("re-parsing normalized URI %q: %v", s.URIs[0], err)
		}
	}
}

func TestParseRejectsBadScheme(t *testing.T) {
	for _, uri := range []string{"http://example.com:3478", "example.com:3478", "turnt:example.com:3478"} {
		if _, err := Parse(uri, "u", "p"); err == nil {
			t.Fatalf("Parse(%q): expected error for bad scheme", uri)
		}
	}
}

func TestParseRejectsMissingHost(t *testing.T) {
	for _, uri := range []string{"turn:", "turn::3478"} {
		if _, err := Parse(uri, "u", "p"); err == nil {
			t.Fatalf("Parse(%q): expected error for missing host", uri)
		}
	}
}

func TestParseRejectsBadPort(t *testing.T) {
	for _, uri := range []string{"turn:example.com:0", "turn:example.com:70000", "turn:example.com:abc"} {
		if _, err := Parse(uri, "u", "p"); err == nil {
			t.Fatalf("Parse(%q): expected error for bad port", uri)
		}
	}
}

func TestParseRejectsBadHost(t *testing.T) {
	// A host with spaces or a leading dot is not a valid hostname.
	for _, uri := range []string{"turn:exa mple.com:3478", "turn:-bad:3478"} {
		if _, err := Parse(uri, "u", "p"); err == nil {
			t.Fatalf("Parse(%q): expected error for bad host", uri)
		}
	}
}

func TestValidateRequiresAtLeastOneServer(t *testing.T) {
	s := &Server{}
	if err := s.Validate(); err == nil {
		t.Fatal("expected error validating an empty server")
	}
}

func TestICEServersBuildsPionConfig(t *testing.T) {
	s, err := Parse("turns:turn.example.org:443", "cred-user", "cred-pass")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	ice := s.ICEServers()
	if len(ice) != 1 {
		t.Fatalf("ICEServers: got %d entries, want 1", len(ice))
	}
	e := ice[0]
	if e.URLs[0] != "turns:turn.example.org:443" {
		t.Fatalf("ICEServers: URL = %q", e.URLs[0])
	}
	if e.Username != "cred-user" || e.Credential != "cred-pass" {
		t.Fatalf("ICEServers: creds = %q/%q", e.Username, e.Credential)
	}
}

func TestICEServersEmptyWhenNoServers(t *testing.T) {
	var c Config
	if ice := c.ICEServers(); ice != nil {
		t.Fatalf("ICEServers on empty config: got %v, want nil", ice)
	}
}

func TestConfigValidateMultipleServers(t *testing.T) {
	c := Config{Servers: []Server{
		{URIs: []string{"turn:a.example.com:3478"}, Username: "u", Password: "p"},
		{URIs: []string{"stun:b.example.com:3478"}},
	}}
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if ice := c.ICEServers(); len(ice) != 2 {
		t.Fatalf("ICEServers: got %d, want 2", len(ice))
	}
}

// ParseServers turns raw --turn style URIs into validated servers.
func TestParseServers(t *testing.T) {
	servers, err := ParseServers([]string{"turn:a.example.com:3478", "turns:b.example.org:443"}, "u", "p")
	if err != nil {
		t.Fatalf("ParseServers: %v", err)
	}
	if len(servers) != 2 {
		t.Fatalf("ParseServers: got %d, want 2", len(servers))
	}
}

func TestParseServersPropagatesError(t *testing.T) {
	if _, err := ParseServers([]string{"http://bad"}, "u", "p"); err == nil {
		t.Fatal("expected error from ParseServers with a bad URI")
	}
}

func TestICEServersEmptyWhenNoURIs(t *testing.T) {
	var s Server
	if ice := s.ICEServers(); ice != nil {
		t.Fatalf("ICEServers on server with no URIs: got %v, want nil", ice)
	}
}

func TestServerValidateRejectsBadURI(t *testing.T) {
	s := &Server{URIs: []string{"not-a-uri"}}
	if err := s.Validate(); err == nil {
		t.Fatal("expected error validating a server with a bad URI")
	}
}

func TestConfigValidateReportsServerIndex(t *testing.T) {
	c := Config{Servers: []Server{{URIs: []string{"turn:ok.example.com:3478"}}, {URIs: nil}}}
	if err := c.Validate(); err == nil {
		t.Fatal("expected error validating a config with an empty server")
	}
}

func TestValidHostIPv4AndIPv6(t *testing.T) {
	for _, h := range []string{"192.168.0.1", "10.0.0.255", "::1", "2001:db8::1"} {
		if !validHost(h) {
			t.Errorf("validHost(%q): want true", h)
		}
	}
}

func TestValidHostTooLongLabel(t *testing.T) {
	long := strings.Repeat("a", 64)
	if validHost(long + ".com") {
		t.Error("validHost: 64-char label should be rejected")
	}
	if !validHost(strings.Repeat("a", 63) + ".com") {
		t.Error("validHost: 63-char label should be accepted")
	}
}

// itoa is a tiny helper to avoid importing strconv in the table above.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if neg {
		b = append([]byte{'-'}, b...)
	}
	return string(b)
}
