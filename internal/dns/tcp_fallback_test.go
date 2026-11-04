package dns

import (
	"context"
	"testing"
	"time"

	"github.com/miekg/dns"

	"github.com/Crank-Git/wingman/internal/sig"
)

// TestLargePollFallsBackToTCP verifies that a poll returning more fragments
// than fit in a single UDP response rides over TCP. The server truncates the
// UDP response (setting TC); the Dialer retries over TCP and returns every
// fragment.
func TestLargePollFallsBackToTCP(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	srv := newTestServer(t)
	tr := newClient(srv)

	// ~10 KB of SDP expands to a few hundred fragments once base32-encoded,
	// far more than the 4096-byte UDP budget can carry in one response, so the
	// poll must fall back to TCP. The result still fits under DNS's 65535-byte
	// message cap, so a single poll returns the whole message.
	line := []byte("a=candidate:usr 1 udp 1 192.168.1.1 8000 typ host end-of-candidates\n")
	offer := make([]byte, 0, len(line)*150)
	for i := 0; i < 150; i++ {
		offer = append(offer, line...)
	}
	if err := tr.Publish(ctx, "bigroom", sig.Offer, offer); err != nil {
		t.Fatalf("publish: %v", err)
	}

	// Poll directly and confirm every fragment comes back.
	d := &Dialer{Addr: srv.Addr()}
	m := new(dns.Msg)
	m.SetQuestion(dns.Fqdn(sig.PollName("example.com", "bigroom", sig.Offer)), dns.TypeTXT)
	m.SetEdns0(edns0UDPSize, false)
	resp, err := d.Exchange(ctx, m)
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}
	if resp.Rcode != dns.RcodeSuccess {
		t.Fatalf("rcode = %d, want success", resp.Rcode)
	}

	got, err := tr.Poll(ctx, "bigroom", sig.Offer)
	if err != nil {
		t.Fatalf("poll: %v", err)
	}
	if len(got) != len(offer) {
		t.Fatalf("poll returned %d bytes, want %d", len(got), len(offer))
	}
}
