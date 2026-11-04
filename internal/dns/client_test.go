package dns

import (
	"context"
	"testing"
	"time"

	"github.com/miekg/dns"

	"github.com/Crank-Git/wingman/internal/sig"
)

func TestDialerDefaults(t *testing.T) {
	d := (&Dialer{}).withDefaults()
	if d.Addr != "127.0.0.1:53" {
		t.Errorf("Addr = %q, want 127.0.0.1:53", d.Addr)
	}
	if d.Net != "udp" {
		t.Errorf("Net = %q, want udp", d.Net)
	}
	if d.Timeout != 2*time.Second {
		t.Errorf("Timeout = %v, want 2s", d.Timeout)
	}
}

func TestDialerRespectsSettings(t *testing.T) {
	d := (&Dialer{Addr: "127.0.0.1:5300", Net: "tcp", Timeout: 5 * time.Second}).withDefaults()
	if d.Addr != "127.0.0.1:5300" || d.Net != "tcp" || d.Timeout != 5*time.Second {
		t.Fatalf("defaults overrode explicit settings: %+v", d)
	}
}

func TestDialerExchangeRoundTrip(t *testing.T) {
	ctx := context.Background()
	srv := newTestServer(t)
	d := &Dialer{Addr: srv.Addr()}

	// Poll an empty room: expect NXDOMAIN.
	resp, err := d.Exchange(ctx, queryMsg("room.offer.sig.example.com"))
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	if resp.Rcode != dns.RcodeNameError {
		t.Fatalf("rcode = %d, want NXDOMAIN", resp.Rcode)
	}

	// Publish a fragment, then poll again: expect the fragment back.
	payload, _ := sig.EncodeLabel([]byte("hi"))
	writeName := sig.RendezvousName("example.com", "room", sig.Offer, 0, 1, payload)
	if _, err := d.Exchange(ctx, queryMsg(writeName)); err != nil {
		t.Fatalf("publish Exchange: %v", err)
	}
	resp, err = d.Exchange(ctx, queryMsg("room.offer.sig.example.com"))
	if err != nil {
		t.Fatalf("poll Exchange: %v", err)
	}
	if resp.Rcode != dns.RcodeSuccess || len(resp.Answer) != 1 {
		t.Fatalf("poll rcode=%d answers=%d", resp.Rcode, len(resp.Answer))
	}
}
