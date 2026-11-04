package dns

import (
	"bytes"
	"context"
	"net"
	"strings"
	"testing"

	"github.com/miekg/dns"

	"github.com/Crank-Git/wingman/internal/sig"
)

func TestPollRejectsForeignIdentity(t *testing.T) {
	for _, tc := range []struct {
		name, zone, room, owner string
		kind                    sig.Kind
	}{
		{"room", "example.com", "other", "room.offer.sig.example.com.", sig.Offer},
		{"kind", "example.com", "room", "room.offer.sig.example.com.", sig.Answer},
		{"zone", "other.example.com", "room", "room.offer.sig.example.com.", sig.Offer},
		{"zone-suffix", "example.com.evil", "room", "room.offer.sig.example.com.", sig.Offer},
		{"owner", "example.com", "room", "other.offer.sig.example.com.", sig.Offer},
		{"invalid-owner", "example.com", "room", " room.offer.sig.example.com.", sig.Offer},
	} {
		t.Run(tc.name, func(t *testing.T) {
			names := replyFragmentNames(t, tc.zone, tc.room, tc.kind)
			resp := new(dns.Msg)
			for _, name := range names {
				resp.Answer = append(resp.Answer, &dns.TXT{Hdr: dns.RR_Header{Name: tc.owner}, Txt: []string{name}})
			}
			tr := &WriteOverQuery{Resolver: &fixedResolver{resp: resp}, Zone: "example.com"}
			if _, err := tr.Poll(context.Background(), "room", sig.Offer); err == nil || !strings.Contains(err.Error(), "identity") {
				t.Fatalf("Poll error = %v, want identity error", err)
			}
		})
	}
}

func TestPollAcceptsCanonicalIdentity(t *testing.T) {
	names := replyFragmentNames(t, "example.com", "room", sig.Offer)
	resp := new(dns.Msg)
	for i := len(names) - 1; i >= 0; i-- {
		resp.Answer = append(resp.Answer, &dns.TXT{Hdr: dns.RR_Header{Name: "RoOm.OfFeR.SiG.ExAmPlE.CoM"}, Txt: []string{strings.ToUpper(names[i]) + "."}})
	}
	tr := &WriteOverQuery{Resolver: &fixedResolver{resp: resp}, Zone: "ExAmPlE.CoM."}
	got, err := tr.Poll(context.Background(), "RoOm", sig.Offer)
	if err != nil || !bytes.Equal(got, []byte("reply identity")) {
		t.Fatalf("Poll = %q, %v", got, err)
	}
}

func TestResolverNilResponseReturnsError(t *testing.T) {
	for _, operation := range []string{"poll", "publish"} {
		t.Run(operation, func(t *testing.T) {
			tr := &WriteOverQuery{Resolver: &fixedResolver{}, Zone: "example.com"}
			var err error
			if operation == "poll" {
				_, err = tr.Poll(context.Background(), "room", sig.Offer)
			} else {
				err = tr.Publish(context.Background(), "room", sig.Offer, []byte("reply"))
			}
			if err == nil || !strings.Contains(err.Error(), "nil resolver response") {
				t.Fatalf("error = %v, want nil resolver response error", err)
			}
		})
	}
}

func TestPollRejectsInjectedDNSReply(t *testing.T) {
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	names := replyFragmentNames(t, "other.example.com", "room", sig.Offer)
	server := &dns.Server{PacketConn: conn, Handler: dns.HandlerFunc(func(w dns.ResponseWriter, req *dns.Msg) {
		resp := new(dns.Msg).SetReply(req)
		for _, name := range names {
			resp.Answer = append(resp.Answer, &dns.TXT{
				Hdr: dns.RR_Header{Name: req.Question[0].Name, Rrtype: dns.TypeTXT, Class: dns.ClassINET},
				Txt: []string{name},
			})
		}
		if err := w.WriteMsg(resp); err != nil {
			t.Error(err)
		}
	})}
	started := make(chan struct{})
	server.NotifyStartedFunc = func() { close(started) }
	done := make(chan error, 1)
	go func() { done <- server.ActivateAndServe() }()
	<-started
	t.Cleanup(func() {
		if err := server.Shutdown(); err != nil {
			t.Error(err)
		}
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	tr := &WriteOverQuery{Resolver: &Dialer{Addr: conn.LocalAddr().String()}, Zone: "example.com"}
	if _, err := tr.Poll(context.Background(), "room", sig.Offer); err == nil || !strings.Contains(err.Error(), "identity") {
		t.Fatalf("Poll error = %v, want identity error", err)
	}
}

func replyFragmentNames(t *testing.T, zone, room string, kind sig.Kind) []string {
	t.Helper()
	frags, err := sig.Fragments([]byte("reply identity"), 8)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(frags))
	for _, frag := range frags {
		payload, err := sig.EncodeLabel(frag.Data)
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, sig.RendezvousName(zone, room, kind, frag.Seq, frag.Total, payload))
	}
	return names
}
