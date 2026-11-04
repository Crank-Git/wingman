package sig

import (
	"bytes"
	"context"
	"sync"
)

// Transport carries SDP fragments between peers over a signaling channel. Two
// implementations are provided: WriteOverQuery (the default, which encodes
// writes as DNS queries) and DynUpdate (an optional RFC 2136 dynamic-update
// backend).
type Transport interface {
	// Publish writes a complete SDP message (offer or answer) under room.
	Publish(ctx context.Context, room string, k Kind, msg []byte) error

	// Poll waits until a complete SDP message of the given kind is available
	// under room and returns it. It must respect ctx cancellation.
	Poll(ctx context.Context, room string, k Kind) ([]byte, error)
}

// StubTransport is an in-memory Transport used for unit tests and for local
// single-host experiments. It stores one message per (room, kind).
// Concurrent Publish and Poll calls are safe. Each call copies message bytes.
type StubTransport struct {
	// mu protects store and its message bytes from concurrent access.
	mu    sync.RWMutex
	store map[string][]byte
}

// NewStubTransport returns an empty in-memory Transport.
func NewStubTransport() *StubTransport {
	return &StubTransport{store: make(map[string][]byte)}
}

// Publish stores msg keyed by (room, kind).
func (s *StubTransport) Publish(_ context.Context, room string, k Kind, msg []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.store[key(room, k)] = bytes.Clone(msg)
	return nil
}

// Poll returns the stored message for (room, kind), or ErrNotFound if none.
func (s *StubTransport) Poll(_ context.Context, room string, k Kind) ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	msg, ok := s.store[key(room, k)]
	if !ok {
		return nil, ErrNotFound
	}
	return bytes.Clone(msg), nil
}

// key builds the store key for a room/kind pair.
func key(room string, k Kind) string {
	return room + "/" + k.String()
}
