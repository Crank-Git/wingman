package peer

import (
	"testing"

	"github.com/pion/webrtc/v4"
)

func TestEncodeDecodeSDPRoundTrip(t *testing.T) {
	cases := []struct {
		name string
		sd   webrtc.SessionDescription
	}{
		{"offer", webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: "v=0\r\no=- offer"}},
		{"answer", webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: "v=0\r\no=- answer"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			data, err := encodeSDP(tc.sd)
			if err != nil {
				t.Fatalf("encodeSDP: %v", err)
			}
			got, err := decodeSDP(data)
			if err != nil {
				t.Fatalf("decodeSDP: %v", err)
			}
			if got.Type != tc.sd.Type {
				t.Errorf("type = %v, want %v", got.Type, tc.sd.Type)
			}
			if got.SDP != tc.sd.SDP {
				t.Errorf("sdp = %q, want %q", got.SDP, tc.sd.SDP)
			}
		})
	}
}

func TestDecodeSDPRejectsUnknownType(t *testing.T) {
	_, err := decodeSDP([]byte(`{"type":"pranswer","sdp":"v=0"}`))
	if err == nil {
		t.Fatal("expected error for unknown SDP type")
	}
}

func TestDecodeSDPRejectsMalformedJSON(t *testing.T) {
	_, err := decodeSDP([]byte(`not json`))
	if err == nil {
		t.Fatal("expected error for malformed JSON")
	}
}

func TestEncodeSDPRejectsUnsupportedType(t *testing.T) {
	if _, err := encodeSDP(webrtc.SessionDescription{Type: webrtc.SDPTypePranswer}); err == nil {
		t.Fatal("expected error for unsupported SDP type")
	}
}
