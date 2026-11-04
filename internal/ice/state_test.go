package ice

import "testing"

func TestConnectionStateStringRoundTrip(t *testing.T) {
	states := []ConnectionState{
		ConnectionStateNew,
		ConnectionStateConnecting,
		ConnectionStateConnected,
		ConnectionStateDisconnected,
		ConnectionStateFailed,
		ConnectionStateClosed,
	}
	for _, s := range states {
		if !s.Valid() {
			t.Fatalf("%q failed Valid()", s)
		}
		parsed, err := ParseConnectionState(s.String())
		if err != nil {
			t.Fatalf("ParseConnectionState(%q): %v", s, err)
		}
		if parsed != s {
			t.Fatalf("round trip: %q -> %v", s, parsed)
		}
	}
}

func TestConnectionStateUnknown(t *testing.T) {
	if got := ConnectionState(99).String(); got != "unknown" {
		t.Fatalf("unknown state String() = %q, want unknown", got)
	}
	if ConnectionState(99).Valid() {
		t.Fatal("out-of-range state should be invalid")
	}
}

func TestParseConnectionStateInvalid(t *testing.T) {
	if _, err := ParseConnectionState("nope"); err == nil {
		t.Fatal("expected error for invalid state")
	}
}

func TestAllowTransitions(t *testing.T) {
	ok := []struct{ from, to ConnectionState }{
		{ConnectionStateNew, ConnectionStateConnecting},
		{ConnectionStateNew, ConnectionStateFailed},
		{ConnectionStateConnecting, ConnectionStateConnected},
		{ConnectionStateConnected, ConnectionStateDisconnected},
		{ConnectionStateDisconnected, ConnectionStateConnecting},
		{ConnectionStateFailed, ConnectionStateClosed},
	}
	for _, tc := range ok {
		if !Allow(tc.from, tc.to) {
			t.Fatalf("Allow(%v -> %v) = false, want true", tc.from, tc.to)
		}
	}

	bad := []struct{ from, to ConnectionState }{
		{ConnectionStateConnected, ConnectionStateNew},       // no rollback
		{ConnectionStateClosed, ConnectionStateConnecting},   // terminal
		{ConnectionStateNew, ConnectionStateConnected},       // must connect first
		{ConnectionStateFailed, ConnectionStateDisconnected}, // no rollback
	}
	for _, tc := range bad {
		if Allow(tc.from, tc.to) {
			t.Fatalf("Allow(%v -> %v) = true, want false", tc.from, tc.to)
		}
	}
}
