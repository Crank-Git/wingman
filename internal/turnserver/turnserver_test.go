package turnserver

import (
	"net"
	"testing"
	"time"

	"github.com/pion/turn/v5"
)

// startTestServer builds a server on an ephemeral loopback port for tests.
func startTestServer(t *testing.T, cfg Config) *Server {
	t.Helper()
	cfg.Addr = "127.0.0.1:0"
	s, err := New(cfg)
	if err != nil {
		t.Fatalf("New(%+v): %v", cfg, err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// newTestClient builds a pion TURN/STUN client pointed at addr, optionally with
// short-term credentials. The returned client must be closed by the caller.
func newTestClient(t *testing.T, addr, user, pass string) *turn.Client {
	t.Helper()
	conn, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("client listen: %v", err)
	}
	c, err := turn.NewClient(&turn.ClientConfig{
		STUNServerAddr: addr,
		TURNServerAddr: addr,
		Conn:           conn,
		Username:       user,
		Password:       pass,
	})
	if err != nil {
		_ = conn.Close()
		t.Fatalf("new client: %v", err)
	}
	if err := c.Listen(); err != nil {
		c.Close()
		t.Fatalf("client listen: %v", err)
	}
	t.Cleanup(func() { c.Close(); conn.Close() })
	return c
}

// TestSTUNWorksWithoutCredentials verifies a credentialless server still answers
// STUN binding requests, yielding a server-reflexive candidate.
func TestSTUNWorksWithoutCredentials(t *testing.T) {
	s := startTestServer(t, Config{})
	client := newTestClient(t, s.Addr(), "", "")

	flx, err := client.SendBindingRequest()
	if err != nil {
		t.Fatalf("STUN binding request failed: %v", err)
	}
	if flx == nil {
		t.Fatal("STUN binding request returned no address")
	}
}

// TestTURNRequiresCredentials confirms that without credentials the server runs
// in STUN-only mode and rejects TURN allocation requests.
func TestTURNRequiresCredentials(t *testing.T) {
	s := startTestServer(t, Config{})
	client := newTestClient(t, s.Addr(), "", "")

	if _, err := client.Allocate(); err == nil {
		t.Fatal("TURN allocation without server credentials: expected failure")
	}
}

// TestTurnAllocatesWithCredentials confirms that with credentials the server
// serves TURN relay allocations and reports them.
func TestTurnAllocatesWithCredentials(t *testing.T) {
	s := startTestServer(t, Config{Username: "alice", Password: "s3cret"})
	if !s.TURNEnabled() {
		t.Fatal("TURNEnabled() = false, want true when credentials are set")
	}
	client := newTestClient(t, s.Addr(), "alice", "s3cret")

	relay, err := client.Allocate()
	if err != nil {
		t.Fatalf("TURN allocation failed: %v", err)
	}
	if relay == nil {
		t.Fatal("TURN allocation returned no relay conn")
	}
	defer relay.Close()

	if got := s.AllocationCount(); got != 1 {
		t.Fatalf("AllocationCount() = %d, want 1", got)
	}
}

// TestWrongPasswordRejected confirms the server authenticates and rejects a
// client presenting the wrong password.
func TestWrongPasswordRejected(t *testing.T) {
	s := startTestServer(t, Config{Username: "alice", Password: "s3cret"})
	client := newTestClient(t, s.Addr(), "alice", "wrong")

	if _, err := client.Allocate(); err == nil {
		t.Fatal("TURN allocation with wrong password: expected failure")
	}
}

// TestAddrReflectsBoundPort confirms the advertised address uses the port the
// OS actually assigned when the caller asked for port 0.
func TestAddrReflectsBoundPort(t *testing.T) {
	s := startTestServer(t, Config{})
	host, portStr, err := net.SplitHostPort(s.Addr())
	if err != nil {
		t.Fatalf("Addr() %q not host:port: %v", s.Addr(), err)
	}
	if host != "127.0.0.1" {
		t.Errorf("Addr() host = %q, want 127.0.0.1", host)
	}
	if portStr == "0" {
		t.Errorf("Addr() port = 0, want an assigned port")
	}
}

// TestSTUNURIFormat checks the advertised STUN URI is well-formed.
func TestSTUNURIFormat(t *testing.T) {
	s := startTestServer(t, Config{})
	want := "stun:" + s.hostPort()
	if got := s.STUNURI(); got != want {
		t.Errorf("STUNURI() = %q, want %q", got, want)
	}
}

// TestTURNURIFormat checks the TURN URI is well-formed when enabled and empty
// when disabled.
func TestTURNURIFormat(t *testing.T) {
	disabled := startTestServer(t, Config{})
	if got, want := disabled.TURNURI(), ""; got != want {
		t.Errorf("TURNURI() without creds = %q, want %q", got, want)
	}
	enabled := startTestServer(t, Config{Username: "u", Password: "p"})
	if got := enabled.TURNURI(); got != "turn:"+enabled.hostPort() {
		t.Errorf("TURNURI() = %q, want turn:<addr>", got)
	}
}

// TestCloseStopsServer confirms that after Close the server no longer answers
// STUN requests.
func TestCloseStopsServer(t *testing.T) {
	s := startTestServer(t, Config{})
	addr := s.Addr()
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	client := newTestClient(t, addr, "", "")
	if _, err := client.SendBindingRequest(); err == nil {
		t.Fatal("STUN request after Close: expected failure")
	}
}

// TestRejectsWildcardWithoutExternal confirms a wildcard listen address without
// an explicit external address is rejected.
func TestRejectsWildcardWithoutExternal(t *testing.T) {
	if _, err := New(Config{Addr: "0.0.0.0:0"}); err == nil {
		t.Fatal("New(0.0.0.0:0): expected error without ExternalAddr")
	}
	if _, err := New(Config{Addr: "[::]:0"}); err == nil {
		t.Fatal("New([::]:0): expected error without ExternalAddr")
	}
}

// TestExternalAddrOverridesHost confirms the advertised address uses the
// configured external host even when bound to a wildcard interface.
func TestExternalAddrOverridesHost(t *testing.T) {
	s, err := New(Config{Addr: "0.0.0.0:0", ExternalAddr: "198.51.100.7"})
	if err != nil {
		t.Fatalf("New(0.0.0.0:0): %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	host, _, _ := net.SplitHostPort(s.hostPort())
	if host != "198.51.100.7" {
		t.Errorf("advertised host = %q, want 198.51.100.7", host)
	}
	if got, want := s.STUNURI(), "stun:198.51.100.7:"+s.port(); got != want {
		t.Errorf("STUNURI() = %q, want %q", got, want)
	}
}

// TestRejectsInvalidExternalAddr rejects a non-IP external address.
func TestRejectsInvalidExternalAddr(t *testing.T) {
	if _, err := New(Config{Addr: "127.0.0.1:0", ExternalAddr: "not-an-ip"}); err == nil {
		t.Fatal("New with non-IP ExternalAddr: expected error")
	}
}

// TestRejectsMissingAddr errors when no listen address is given.
func TestRejectsMissingAddr(t *testing.T) {
	if _, err := New(Config{}); err == nil {
		t.Fatal("New with no Addr: expected error")
	}
}

// TestRequiresCredsTogether errors when only one of user/pass is set.
func TestRequiresCredsTogether(t *testing.T) {
	if _, err := New(Config{Addr: "127.0.0.1:0", Username: "u"}); err == nil {
		t.Fatal("New with Username only: expected error")
	}
	if _, err := New(Config{Addr: "127.0.0.1:0", Password: "p"}); err == nil {
		t.Fatal("New with Password only: expected error")
	}
}

// TestNewRejectsUnbindableAddr confirms a socket bind failure is surfaced, not
// swallowed.
func TestNewRejectsUnbindableAddr(t *testing.T) {
	conn, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer conn.Close()
	if _, err := New(Config{Addr: conn.LocalAddr().String()}); err == nil {
		t.Fatal("New on an occupied address: expected error")
	}
}

// TestAuthRejectsUnknownUsername confirms the handler rejects a client that
// presents an unknown username.
func TestAuthRejectsUnknownUsername(t *testing.T) {
	s := startTestServer(t, Config{Username: "alice", Password: "s3cret"})
	client := newTestClient(t, s.Addr(), "bob", "whatever")
	if _, err := client.Allocate(); err == nil {
		t.Fatal("TURN allocation with unknown username: expected failure")
	}
}

// TestDefaultRealm confirms the realm defaults to "wingman".
func TestDefaultRealm(t *testing.T) {
	s := startTestServer(t, Config{})
	if s.realm != defaultRealm {
		t.Errorf("default realm = %q, want %q", s.realm, defaultRealm)
	}
}

// TestCustomRealm confirms a caller-supplied realm is honoured.
func TestCustomRealm(t *testing.T) {
	s := startTestServer(t, Config{Realm: "example.test"})
	if s.realm != "example.test" {
		t.Errorf("realm = %q, want example.test", s.realm)
	}
	if got := s.Realm(); got != "example.test" {
		t.Errorf("Realm() = %q, want example.test", got)
	}
}

// TestAllocationCountDecrementsWithRelayClose verifies the count drops once the
// relay conn is released.
func TestAllocationCountDecrementsWithRelayClose(t *testing.T) {
	s := startTestServer(t, Config{Username: "alice", Password: "s3cret"})
	client := newTestClient(t, s.Addr(), "alice", "s3cret")

	relay, err := client.Allocate()
	if err != nil {
		t.Fatalf("allocate: %v", err)
	}
	if s.AllocationCount() != 1 {
		t.Fatalf("AllocationCount() = %d, want 1", s.AllocationCount())
	}
	_ = relay.Close()

	// Give the server a moment to process the allocation teardown.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if s.AllocationCount() == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := s.AllocationCount(); got != 0 {
		t.Errorf("AllocationCount() after close = %d, want 0", got)
	}
}
