package dns

import (
	"context"
	"testing"

	"github.com/miekg/dns"

	"github.com/Crank-Git/wingman/internal/sig"
)

func queryMsg(name string) *dns.Msg {
	m := new(dns.Msg)
	m.SetQuestion(dns.Fqdn(name), dns.TypeTXT)
	m.SetEdns0(4096, false)
	return m
}

func serveQuery(t *testing.T, srv *Server, name string) *dns.Msg {
	t.Helper()
	d := &Dialer{Addr: srv.Addr()}
	resp, err := d.Exchange(context.Background(), queryMsg(name))
	if err != nil {
		t.Fatalf("Exchange(%q): %v", name, err)
	}
	return resp
}

func TestServerHandleEmptyQuestion(t *testing.T) {
	srv := newTestServer(t)
	d := &Dialer{Addr: srv.Addr()}
	resp, err := d.Exchange(context.Background(), new(dns.Msg))
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	if resp.Rcode != dns.RcodeFormatError {
		t.Fatalf("rcode = %d, want FormatError", resp.Rcode)
	}
}

func TestServerHandleValidWrite(t *testing.T) {
	srv := newTestServer(t)
	payload, _ := sig.EncodeLabel([]byte("hi"))
	name := sig.RendezvousName("example.com", "room", sig.Offer, 0, 1, payload)

	resp := serveQuery(t, srv, name)
	if resp.Rcode != dns.RcodeSuccess {
		t.Fatalf("rcode = %d, want Success", resp.Rcode)
	}
}

func TestServerHandleMalformedPayload(t *testing.T) {
	srv := newTestServer(t)
	name := "room.0-1.@@@@@@.offer.sig.example.com."

	resp := serveQuery(t, srv, name)
	if resp.Rcode != dns.RcodeFormatError {
		t.Fatalf("rcode = %d, want FormatError", resp.Rcode)
	}
}

func TestServerHandlePollEmpty(t *testing.T) {
	srv := newTestServer(t)
	resp := serveQuery(t, srv, "room.offer.sig.example.com")
	if resp.Rcode != dns.RcodeNameError {
		t.Fatalf("rcode = %d, want NXDOMAIN", resp.Rcode)
	}
}

func TestServerHandlePollInvalidName(t *testing.T) {
	srv := newTestServer(t)
	resp := serveQuery(t, srv, "garbage.name")
	if resp.Rcode != dns.RcodeNameError {
		t.Fatalf("rcode = %d, want NXDOMAIN", resp.Rcode)
	}
}

func TestServerHandlePollInvalidKind(t *testing.T) {
	srv := newTestServer(t)
	resp := serveQuery(t, srv, "room.badopt.sig.example.com")
	if resp.Rcode != dns.RcodeNameError {
		t.Fatalf("rcode = %d, want NXDOMAIN", resp.Rcode)
	}
}

func TestServerPollReturnsStoredFragments(t *testing.T) {
	srv := newTestServer(t)
	payload, _ := sig.EncodeLabel([]byte("hi"))
	writeName := sig.RendezvousName("example.com", "room", sig.Offer, 0, 1, payload)
	if resp := serveQuery(t, srv, writeName); resp.Rcode != dns.RcodeSuccess {
		t.Fatalf("write rcode = %d, want Success", resp.Rcode)
	}

	resp := serveQuery(t, srv, "room.offer.sig.example.com")
	if resp.Rcode != dns.RcodeSuccess || len(resp.Answer) != 1 {
		t.Fatalf("poll rcode=%d answers=%d", resp.Rcode, len(resp.Answer))
	}
	txt, ok := resp.Answer[0].(*dns.TXT)
	if !ok {
		t.Fatalf("answer[0] is %T, want *dns.TXT", resp.Answer[0])
	}
	if len(txt.Txt) != 1 {
		t.Fatalf("txt has %d strings, want 1", len(txt.Txt))
	}
	parsed, err := sig.ParseRendezvousName(txt.Txt[0])
	if err != nil {
		t.Fatalf("parse fragment name: %v", err)
	}
	if parsed.Seq != 0 || parsed.Total != 1 {
		t.Fatalf("seq/total = %d/%d, want 0/1", parsed.Seq, parsed.Total)
	}
}

func TestServerConcurrentWrites(t *testing.T) {
	srv := newTestServer(t)
	for i := 0; i < 50; i++ {
		payload, _ := sig.EncodeLabel([]byte("x"))
		name := sig.RendezvousName("example.com", "room", sig.Offer, uint16(i), 50, payload)
		if resp := serveQuery(t, srv, name); resp.Rcode != dns.RcodeSuccess {
			t.Fatalf("write %d rcode = %d", i, resp.Rcode)
		}
	}
	resp := serveQuery(t, srv, "room.offer.sig.example.com")
	if len(resp.Answer) != 50 {
		t.Fatalf("got %d fragments, want 50", len(resp.Answer))
	}
}
