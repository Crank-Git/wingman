package ice

import (
	"fmt"
	"strings"
)

// ConnectionState is an ICE connection state per RFC 8445 §7.2.1.
type ConnectionState int

// ICE connection states.
const (
	ConnectionStateNew ConnectionState = iota
	ConnectionStateConnecting
	ConnectionStateConnected
	ConnectionStateDisconnected
	ConnectionStateFailed
	ConnectionStateClosed
)

// String returns the lower-case WebRTC state name for s.
func (s ConnectionState) String() string {
	switch s {
	case ConnectionStateNew:
		return "new"
	case ConnectionStateConnecting:
		return "connecting"
	case ConnectionStateConnected:
		return "connected"
	case ConnectionStateDisconnected:
		return "disconnected"
	case ConnectionStateFailed:
		return "failed"
	case ConnectionStateClosed:
		return "closed"
	}
	return "unknown"
}

// ParseConnectionState parses a state name from String (case-insensitive).
func ParseConnectionState(s string) (ConnectionState, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "new":
		return ConnectionStateNew, nil
	case "connecting":
		return ConnectionStateConnecting, nil
	case "connected":
		return ConnectionStateConnected, nil
	case "disconnected":
		return ConnectionStateDisconnected, nil
	case "failed":
		return ConnectionStateFailed, nil
	case "closed":
		return ConnectionStateClosed, nil
	default:
		return ConnectionStateNew, fmt.Errorf("ice: unknown connection state %q", s)
	}
}

// Valid reports whether s is a recognized connection state.
func (s ConnectionState) Valid() bool {
	return s >= ConnectionStateNew && s <= ConnectionStateClosed
}

// Transition describes a legal ICE state change. The zero ConnectionState has no
// outgoing transitions; every other state lists the states it may move to.
var transitions = map[ConnectionState][]ConnectionState{
	ConnectionStateNew:          {ConnectionStateConnecting, ConnectionStateFailed, ConnectionStateClosed},
	ConnectionStateConnecting:   {ConnectionStateConnected, ConnectionStateDisconnected, ConnectionStateFailed, ConnectionStateClosed},
	ConnectionStateConnected:    {ConnectionStateDisconnected, ConnectionStateClosed},
	ConnectionStateDisconnected: {ConnectionStateConnecting, ConnectionStateFailed, ConnectionStateClosed},
	ConnectionStateFailed:       {ConnectionStateClosed},
	ConnectionStateClosed:       nil,
}

// Allow reports whether the agent may move from current to next.
func Allow(current, next ConnectionState) bool {
	for _, n := range transitions[current] {
		if n == next {
			return true
		}
	}
	return false
}
