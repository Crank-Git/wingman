package dns

import (
	"context"
	"errors"
	"net"
	"strings"
	"syscall"
	"testing"

	"github.com/Crank-Git/wingman/internal/sig"
	"github.com/miekg/dns"
)

func TestServerEnforcesConfiguredZone(t *testing.T) {
	s := NewServer()
	s.Zone = "EXAMPLE.COM."
	payload, err := sig.EncodeLabel([]byte("payload"))
	if err != nil {
		t.Fatal(err)
	}
	for _, zone := range []string{"example.com", "other.test", "notexample.com", "sub.example.com"} {
		name := sig.RendezvousName(zone, "room", sig.Offer, 0, 1, payload)
		req := new(dns.Msg)
		req.SetQuestion(dns.Fqdn(name), dns.TypeTXT)
		w := &probeWriter{}
		s.handle(w, req, 4096)
		if zone == "example.com" {
			if w.wrote.Rcode != dns.RcodeSuccess {
				t.Fatal(w.wrote)
			}
		} else if w.wrote.Rcode != dns.RcodeRefused {
			t.Fatalf("zone %s: %v", zone, w.wrote)
		}
		req.SetQuestion(dns.Fqdn(sig.PollName(zone, "room", sig.Offer)), dns.TypeTXT)
		s.handle(w, req, 4096)
		if zone != "example.com" && (w.wrote.Rcode != dns.RcodeRefused || len(w.wrote.Answer) != 0) {
			t.Fatal(w.wrote)
		}
	}
	if len(s.frags) != 1 || s.retainedBytes != len("payload") {
		t.Fatalf("storage: %d keys, %d bytes", len(s.frags), s.retainedBytes)
	}
}

func TestServerBindsConfiguredAddress(t *testing.T) {
	for _, address := range []string{"127.0.0.1:0", "[::1]:0"} {
		t.Run(address, func(t *testing.T) {
			s := NewServer()
			s.ListenAddr = address
			s.Zone = "Example.COM."
			if err := s.Start(context.Background()); err != nil {
				t.Fatal(err)
			}
			defer s.Stop()
			if s.dns.PacketConn.LocalAddr().String() != s.tcp.Listener.Addr().String() || strings.HasSuffix(s.Addr(), ":0") {
				t.Fatal(s.Addr())
			}
		})
	}
}

func TestServerRejectsInvalidConfiguration(t *testing.T) {
	for _, tc := range []struct{ address, zone string }{{"0.0.0.0:0", "example.com"}, {"localhost:0", "example.com"}, {"bad", "example.com"}, {"127.0.0.1:no", "example.com"}, {"127.0.0.1:65536", "example.com"}, {"127.0.0.1:-1", "example.com"}, {"127.0.0.1:0", "bad..zone"}} {
		s := NewServer()
		s.ListenAddr, s.Zone = tc.address, tc.zone
		if err := s.Start(context.Background()); err == nil {
			s.Stop()
			t.Fatalf("accepted %+v", tc)
		}
	}
}

func TestServerAuthenticatesRemoteBind(t *testing.T) {
	s := NewServer()
	s.ListenAddr, s.Zone, s.TSIGKey = "0.0.0.0:0", "example.com", testTSIGKey()
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	s.Stop()
	s.TSIGKey.Secret = "invalid"
	if err := s.Start(context.Background()); err == nil {
		s.Stop()
		t.Fatal("invalid key permits remote bind")
	}
}

func TestServerClosesUDPAfterFixedTCPConflict(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	s := NewServer()
	s.ListenAddr = ln.Addr().String()
	attempts := 0
	var pc net.PacketConn
	s.listenPacket = func(network, address string) (net.PacketConn, error) {
		attempts++
		var err error
		pc, err = net.ListenPacket(network, address)
		return pc, err
	}
	if err := s.Start(context.Background()); !errors.Is(err, syscall.EADDRINUSE) {
		t.Fatal(err)
	}
	if attempts != 1 {
		t.Fatalf("bind attempts: %d", attempts)
	}
	assertPacketClosed(t, pc)
}

func TestServerRejectsMalformedNamesWithinZone(t *testing.T) {
	s := NewServer()
	s.Zone = "example.com"
	for _, name := range []string{"room..offer.sig.example.com.", "room.offer.sig.example.com..", "room.offer.sig.example\\.com.", ".", "example.com."} {
		q := new(dns.Msg)
		q.SetQuestion(name, dns.TypeTXT)
		w := &probeWriter{}
		s.handle(w, q, 4096)
		if w.wrote.Rcode == dns.RcodeSuccess || len(w.wrote.Answer) != 0 {
			t.Fatal(w.wrote)
		}
	}
	if len(s.frags) != 0 || s.retainedBytes != 0 {
		t.Fatal("malformed query changes storage")
	}
}
