package sig

import "context"

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
type StubTransport struct {
	store map[string][]byte
}

// NewStubTransport returns an empty in-memory Transport.
func NewStubTransport() *StubTransport {
	return &StubTransport{store: make(map[string][]byte)}
}

// Publish stores msg keyed by (room, kind).
func (s *StubTransport) Publish(_ context.Context, room string, k Kind, msg []byte) error {
	s.store[key(room, k)] = msg
	return nil
}

// Poll returns the stored message for (room, kind), or ErrNotFound if none.
func (s *StubTransport) Poll(_ context.Context, room string, k Kind) ([]byte, error) {
	msg, ok := s.store[key(room, k)]
	if !ok {
		return nil, ErrNotFound
	}
	return msg, nil
}

// key builds the store key for a room/kind pair.
func key(room string, k Kind) string {
	return room + "/" + k.String()
}
