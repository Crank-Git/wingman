package dns

import (
	"strings"
	"testing"

	"github.com/Crank-Git/wingman/internal/sig"
	"github.com/miekg/dns"
)

func directQuery(s *Server, req *dns.Msg) *dns.Msg {
	w := &probeWriter{}
	s.handle(w, req, 0)
	return w.wrote
}

func fragmentQuery(zone, data string, seq, total uint16) *dns.Msg {
	payload, _ := sig.EncodeLabel([]byte(data))
	return queryMsg(sig.RendezvousName(zone, "room", sig.Offer, seq, total, payload))
}

func TestIdentityIsolatesZones(t *testing.T) {
	s := newTestServer(t)
	serveQuery(t, s, fragmentQuery("one.test", "first", 0, 1).Question[0].Name)
	if got := serveQuery(t, s, "room.offer.sig.two.test"); got.Rcode != dns.RcodeNameError {
		t.Fatal("another zone reads the fragment")
	}
	serveQuery(t, s, fragmentQuery("two.test", "second", 0, 1).Question[0].Name)
	got := serveQuery(t, s, "room.offer.sig.one.test")
	want := fragmentQuery("one.test", "first", 0, 1).Question[0].Name
	if len(got.Answer) != 1 || got.Answer[0].(*dns.TXT).Txt[0]+"." != want {
		t.Fatalf("another zone overwrites the fragment: %v", got)
	}
}

func TestIdentityAcceptsNameBoundaries(t *testing.T) {
	room := strings.Repeat("r", 63)
	prefix := room + ".offer.sig."
	zone := strings.Repeat("z", 63) + "." + strings.Repeat("z", 63) + "."
	name := prefix + zone + strings.Repeat("z", 253-len(prefix)-len(zone))
	s := NewServer()
	if got := directQuery(s, queryMsg(name)); got.Rcode != dns.RcodeNameError {
		t.Fatalf("255-octet wire name: %v", got)
	}
	if got := directQuery(s, queryMsg(name+"z")); got.Rcode != dns.RcodeFormatError {
		t.Fatalf("256-octet wire name: %v", got)
	}
}

func TestIdentityRejectsUnsupportedQuestions(t *testing.T) {
	for _, shape := range []string{"type", "class", "multiple"} {
		t.Run(shape, func(t *testing.T) {
			s := NewServer()
			req := fragmentQuery("one.test", "first", 0, 1)
			switch shape {
			case "type":
				req.Question[0].Qtype = dns.TypeA
			case "class":
				req.Question[0].Qclass = dns.ClassCHAOS
			case "multiple":
				req.Question = append(req.Question, req.Question[0])
			}
			if got := directQuery(s, req); got.Rcode != dns.RcodeFormatError || len(s.frags) != 0 {
				t.Fatalf("unsupported question changes the store: %v", got)
			}
		})
	}
}

func TestIdentityNormalizesCase(t *testing.T) {
	s := NewServer()
	req := fragmentQuery("one.test", "first", 0, 1)
	req.Question[0].Name = strings.ToUpper(req.Question[0].Name)
	if got := directQuery(s, req); got.Rcode != dns.RcodeSuccess {
		t.Fatalf("write: %v", got)
	}
	if got := directQuery(s, queryMsg("ROOM.OFFER.SIG.ONE.TEST")); len(got.Answer) != 1 {
		t.Fatalf("poll: %v", got)
	}
}

func TestIdentityRejectsMalformedNames(t *testing.T) {
	for _, name := range []string{
		"room.bad.sig.one.test", "room.0-0.abc.offer.sig.one.test", "room.0-1.abc.bad.sig.one.test",
		strings.Repeat("r", 64) + ".offer.sig.one.test", "room.offer.sig..test",
		"room.offer.sig." + strings.Repeat("a.", 120) + "test",
		" room.0-1.abc.offer.sig.one.test", "room.0-1.abc.offer.sig.one.test ",
		"room\\.other.offer.sig.one.test",
	} {
		t.Run(name, func(t *testing.T) {
			s := NewServer()
			if got := directQuery(s, queryMsg(name)); got.Rcode != dns.RcodeFormatError || len(s.frags) != 0 {
				t.Fatalf("malformed name: %v", got)
			}
		})
	}
}

func TestIdentityRejectsFragmentConflicts(t *testing.T) {
	for _, conflict := range []struct {
		data       string
		seq, total uint16
	}{{"other", 0, 2}, {"first", 1, 3}} {
		t.Run(conflict.data, func(t *testing.T) {
			s := NewServer()
			req := fragmentQuery("one.test", "first", 0, 2)
			directQuery(s, req)
			if got := directQuery(s, fragmentQuery("one.test", conflict.data, conflict.seq, conflict.total)); got.Rcode != dns.RcodeFormatError {
				t.Fatalf("conflict: %v", got)
			}
			got := directQuery(s, queryMsg("room.offer.sig.one.test"))
			if len(got.Answer) != 1 || got.Answer[0].(*dns.TXT).Txt[0]+"." != req.Question[0].Name {
				t.Fatalf("conflict changes the store: %v", got)
			}
		})
	}
}

func TestIdentityKeepsIdenticalDuplicates(t *testing.T) {
	s := NewServer()
	req := fragmentQuery("one.test", "first", 0, 1)
	for range 2 {
		if got := directQuery(s, req); got.Rcode != dns.RcodeSuccess {
			t.Fatalf("write: %v", got)
		}
	}
	if got := directQuery(s, queryMsg("room.offer.sig.one.test")); len(got.Answer) != 1 {
		t.Fatalf("duplicate: %v", got)
	}
}
