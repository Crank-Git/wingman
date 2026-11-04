package peer

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/pion/webrtc/v4"
)

// errNotOpen is returned when an operation requires an open data channel that
// does not yet exist.
var errNotOpen = errors.New("peer: data channel not open")

// sdpMessage is the on-the-wire representation of a session description. It is
// a small JSON envelope carrying the SDP type and body so the exchange survives
// the DNS transport (which carries opaque byte payloads) unchanged.
type sdpMessage struct {
	Type string `json:"type"` // "offer" or "answer"
	SDP  string `json:"sdp"`
}

// encodeSDP serializes a webrtc SessionDescription to JSON bytes.
func encodeSDP(sd webrtc.SessionDescription) ([]byte, error) {
	var typ string
	switch sd.Type {
	case webrtc.SDPTypeOffer:
		typ = "offer"
	case webrtc.SDPTypeAnswer:
		typ = "answer"
	default:
		return nil, fmt.Errorf("peer: unsupported SDP type %v", sd.Type)
	}
	data, err := json.Marshal(sdpMessage{Type: typ, SDP: sd.SDP})
	if err != nil {
		return nil, fmt.Errorf("peer: marshal SDP: %w", err)
	}
	return data, nil
}

// decodeSDP parses JSON bytes back into a webrtc SessionDescription.
func decodeSDP(data []byte) (webrtc.SessionDescription, error) {
	var msg sdpMessage
	if err := json.Unmarshal(data, &msg); err != nil {
		return webrtc.SessionDescription{}, fmt.Errorf("peer: decode SDP: %w", err)
	}
	var t webrtc.SDPType
	switch msg.Type {
	case "offer":
		t = webrtc.SDPTypeOffer
	case "answer":
		t = webrtc.SDPTypeAnswer
	default:
		return webrtc.SessionDescription{}, fmt.Errorf("peer: unknown SDP type %q", msg.Type)
	}
	return webrtc.SessionDescription{Type: t, SDP: msg.SDP}, nil
}
