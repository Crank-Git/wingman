package dns

import (
	"testing"

	"github.com/miekg/dns"
)

type probeWriter struct {
	dns.ResponseWriter
	wrote *dns.Msg
}

func (w *probeWriter) WriteMsg(m *dns.Msg) error { w.wrote = m; return nil }

func TestHandleEmptyQuestionDirect(t *testing.T) {
	srv := NewServer()
	req := new(dns.Msg)
	w := &probeWriter{}
	srv.handle(w, req, edns0UDPSize)
	if w.wrote == nil {
		t.Fatal("handler did not write a response")
	}
	if len(req.Question) != 0 {
		t.Fatalf("req has %d questions, want 0", len(req.Question))
	}
	if w.wrote.Rcode != dns.RcodeFormatError {
		t.Fatalf("rcode = %d, want FormatError", w.wrote.Rcode)
	}
}
