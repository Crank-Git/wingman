package dns

import (
	"bytes"
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Crank-Git/wingman/internal/sig"
	mdns "github.com/miekg/dns"
)

func TestDynUpdateCleanupScopesOneUpdate(t *testing.T) {
	tr := newPublishTransport(t, "localhost:53")
	r := new(updateCapture)
	tr.Resolver = r
	if err := tr.Cleanup(context.Background(), "RoOm"); err != nil {
		t.Fatal(err)
	}
	if len(r.calls) != 1 {
		t.Fatalf("UPDATE count: %d", len(r.calls))
	}
	m := r.calls[0]
	if m.Opcode != mdns.OpcodeUpdate || len(m.Question) != 1 || m.Question[0].Name != "example.com." || m.Question[0].Qtype != mdns.TypeSOA || len(m.Ns) != 2 || len(m.Answer) != 0 || len(m.Extra) != 0 {
		t.Fatalf("invalid cleanup request: %v", m)
	}
	for i, kind := range []string{"offer", "answer"} {
		h := m.Ns[i].Header()
		if h.Name != "room."+kind+".sig.example.com." || h.Rrtype != mdns.TypeTXT || h.Class != mdns.ClassANY || h.Ttl != 0 || h.Rdlength != 0 {
			t.Fatalf("unsafe deletion: %v", m.Ns[i])
		}
	}
}

func TestDynUpdateCleanupRejectsBeforeExchange(t *testing.T) {
	for _, room := range []string{"", "bad.room", "bad room", strings.Repeat("r", 64)} {
		tr := newPublishTransport(t, "localhost:53")
		r := new(updateFailure)
		tr.Resolver = r
		if err := tr.Cleanup(context.Background(), room); err == nil || r.calls != 0 {
			t.Fatalf("validation: %v calls=%d", err, r.calls)
		}
	}
	tr := newPublishTransport(t, "localhost:53")
	r := new(updateFailure)
	tr.Resolver = r
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := tr.Cleanup(ctx, "room"); !errors.Is(err, context.Canceled) || r.calls != 0 {
		t.Fatalf("cancellation: %v", err)
	}
	var zero DynUpdate
	if err := zero.Cleanup(context.Background(), "room"); err == nil {
		t.Fatal("zero configuration accepted")
	}
	tr.key.Secret = "private-value"
	if err := tr.Cleanup(context.Background(), "room"); err == nil || strings.Contains(err.Error(), "private-value") || r.calls != 0 {
		t.Fatalf("authentication: %v", err)
	}
}

func TestDynUpdateCleanupPreservesFailures(t *testing.T) {
	cause := errors.New("route unavailable")
	for _, r := range []*updateFailure{{err: cause}, {nilReply: true}, {rcode: mdns.RcodeRefused}, {rcode: mdns.RcodeNotAuth}} {
		tr := newPublishTransport(t, "localhost:53")
		tr.Resolver = r
		err := tr.Cleanup(context.Background(), "room")
		if err == nil || r.err != nil && !errors.Is(err, cause) {
			t.Fatalf("failure cause: %v", err)
		}
		var rejected *DynUpdateRcodeError
		if r.rcode != 0 && (!errors.As(err, &rejected) || rejected.Rcode != r.rcode || rejected.Operation != "cleanup") {
			t.Fatalf("typed rejection: %v", err)
		}
	}
}

func TestDynUpdateCleanupPreservesTCPRouteCause(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	tr := newPublishTransport(t, addr)
	var route *net.OpError
	if err := tr.Cleanup(context.Background(), "room"); !errors.As(err, &route) {
		t.Fatalf("lost network cause: %v", err)
	}
}

func TestDynUpdatePollReportsCanceledCleanupAfterValidation(t *testing.T) {
	tr := newPublishTransport(t, "localhost:53")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := &pollReply{reply: pollAnswer(t, []byte("safe")), cancel: cancel}
	tr.Resolver = r
	calls := 0
	tr.OnCleanupFailure = func() { calls++ }
	got, err := tr.Poll(ctx, "room", sig.Offer)
	if err != nil || string(got) != "safe" || calls != 1 || r.cleanups != 0 || r.calls != 1 {
		t.Fatalf("canceled cleanup: %v callbacks=%d queries=%d updates=%d", err, calls, r.calls, r.cleanups)
	}
}

type cleanupPollReply struct {
	reply            *mdns.Msg
	err              error
	queries, updates int
	owner            string
}

func (r *cleanupPollReply) Exchange(_ context.Context, m *mdns.Msg) (*mdns.Msg, error) {
	if m.Opcode != mdns.OpcodeUpdate {
		r.queries++
		return r.reply, nil
	}
	r.updates++
	if len(m.Ns) != 1 || m.Ns[0].Header().Name != r.owner || m.Ns[0].Header().Rrtype != mdns.TypeTXT || m.Ns[0].Header().Class != mdns.ClassANY {
		panic("cleanup removes an unconsumed RRset")
	}
	return new(mdns.Msg), r.err
}

func TestDynUpdatePollKeepsSuccessAfterCleanupFailure(t *testing.T) {
	for _, kind := range []sig.Kind{sig.Offer, sig.Answer} {
		for _, fail := range []bool{false, true} {
			for _, callback := range []bool{false, true} {
				tr := newPublishTransport(t, "localhost:53")
				owner, err := tr.owner("room", kind)
				if err != nil {
					t.Fatal(err)
				}
				msg := []byte("private-sdp")
				rrs, err := dynUpdateRecords(owner, msg)
				if err != nil {
					t.Fatal(err)
				}
				r := &cleanupPollReply{reply: &mdns.Msg{Answer: rrs}, owner: owner}
				if fail {
					r.err = errors.New("private-server-error")
				}
				tr.Resolver = r
				calls := 0
				if callback {
					tr.OnCleanupFailure = func() { calls++ }
				}
				got, err := tr.Poll(context.Background(), "room", kind)
				want := 0
				if fail && callback {
					want = 1
				}
				if err != nil || !bytes.Equal(got, msg) || r.queries != 1 || r.updates != 1 || calls != want {
					t.Fatalf("consumption: %v queries=%d updates=%d callbacks=%d", err, r.queries, r.updates, calls)
				}
			}
		}
	}
}

type scopedCleanupFixture struct {
	t       *testing.T
	key     *TSIGKey
	mode    string
	mu      sync.Mutex
	records []mdns.RR
	updates int
}

func (f *scopedCleanupFixture) ServeDNS(w mdns.ResponseWriter, req *mdns.Msg) {
	if (w.TsigStatus() != nil && f.mode != "bad signature") || req.IsTsig() == nil || req.IsEdns0() != nil {
		f.t.Error("request lacks valid TCP authentication")
	}
	reply := new(mdns.Msg)
	reply.SetReply(req)
	f.mu.Lock()
	if req.Opcode == mdns.OpcodeUpdate {
		f.updates++
		for _, del := range req.Ns {
			h := del.Header()
			if h.Class != mdns.ClassANY || h.Rrtype != mdns.TypeTXT || h.Ttl != 0 {
				f.t.Error("unsafe delete")
			}
			kept := f.records[:0]
			for _, rr := range f.records {
				if rr.Header().Name != h.Name || rr.Header().Rrtype != h.Rrtype {
					kept = append(kept, rr)
				}
			}
			f.records = kept
		}
	} else {
		for _, rr := range f.records {
			if rr.Header().Name == req.Question[0].Name && rr.Header().Rrtype == req.Question[0].Qtype {
				reply.Answer = append(reply.Answer, rr)
			}
		}
	}
	f.mu.Unlock()
	if f.mode == "refused" {
		reply.Rcode = mdns.RcodeRefused
	}
	if f.mode != "unsigned" {
		reply.SetTsig(f.key.Name, f.key.Algorithm, 300, time.Now().Unix())
	}
	if err := w.WriteMsg(reply); err != nil {
		f.t.Error(err)
	}
}

func TestDynUpdateCleanupAuthenticatesScopedTCPDeletion(t *testing.T) {
	for _, mode := range []string{"valid", "unsigned", "bad signature", "refused"} {
		t.Run(mode, func(t *testing.T) {
			key := testTSIGKey()
			f := &scopedCleanupFixture{t: t, key: key, mode: mode}
			for _, owner := range []string{"room.offer.sig.example.com.", "room.answer.sig.example.com.", "other.offer.sig.example.com.", "other.answer.sig.example.com.", "room.extra.sig.example.com."} {
				f.records = append(f.records, &mdns.TXT{Hdr: mdns.RR_Header{Name: owner, Rrtype: mdns.TypeTXT, Class: mdns.ClassINET, Ttl: 1}, Txt: []string{"retained"}})
			}
			f.records = append(f.records, &mdns.A{Hdr: mdns.RR_Header{Name: "room.offer.sig.example.com.", Rrtype: mdns.TypeA, Class: mdns.ClassINET}, A: net.IPv4(127, 0, 0, 1)})
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			server := &mdns.Server{Listener: ln, Handler: f, TsigSecret: key.secrets(), MsgAcceptFunc: func(mdns.Header) mdns.MsgAcceptAction { return mdns.MsgAccept }}
			if mode == "bad signature" {
				server.TsigSecret = map[string]string{key.Name: "b3RoZXI="}
			}
			if err := activateListener(server); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := server.Shutdown(); err != nil {
					t.Error(err)
				}
			})
			tr := newPublishTransport(t, ln.Addr().String())
			err = tr.Cleanup(context.Background(), "room")
			if mode != "valid" {
				if err == nil {
					t.Fatal("invalid reply accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			for _, tc := range []struct {
				owner string
				kind  uint16
				count int
			}{
				{"room.offer.sig.example.com.", mdns.TypeTXT, 0}, {"room.answer.sig.example.com.", mdns.TypeTXT, 0},
				{"other.offer.sig.example.com.", mdns.TypeTXT, 1}, {"other.answer.sig.example.com.", mdns.TypeTXT, 1},
				{"room.extra.sig.example.com.", mdns.TypeTXT, 1}, {"room.offer.sig.example.com.", mdns.TypeA, 1},
			} {
				q := new(mdns.Msg)
				q.SetQuestion(tc.owner, tc.kind)
				reply, err := tr.Resolver.Exchange(context.Background(), q)
				if err != nil || len(reply.Answer) != tc.count {
					t.Fatalf("readback %s: %v", tc.owner, err)
				}
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.updates != 1 {
				t.Fatalf("UPDATE count: %d", f.updates)
			}
		})
	}
}
