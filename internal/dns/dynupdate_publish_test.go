package dns

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Crank-Git/wingman/internal/sig"
	mdns "github.com/miekg/dns"
)

type updateCapture struct {
	calls   []*mdns.Msg
	records []mdns.RR
}

func (r *updateCapture) Exchange(_ context.Context, m *mdns.Msg) (*mdns.Msg, error) {
	r.calls = append(r.calls, m.Copy())
	r.records = nil
	for _, rr := range m.Ns {
		if rr.Header().Class == mdns.ClassINET {
			r.records = append(r.records, rr)
		}
	}
	reply := new(mdns.Msg)
	reply.SetReply(m)
	return reply, nil
}

type updateFailure struct {
	err      error
	rcode    int
	calls    int
	nilReply bool
}

func (r *updateFailure) Exchange(_ context.Context, m *mdns.Msg) (*mdns.Msg, error) {
	r.calls++
	if r.err != nil || r.nilReply {
		return nil, r.err
	}
	reply := new(mdns.Msg)
	reply.SetReply(m)
	reply.Rcode = r.rcode
	return reply, nil
}

func TestDynUpdatePublishRejectsBeforeExchange(t *testing.T) {
	longZone := strings.Repeat("a", 63) + "." + strings.Repeat("b", 63) + "." + strings.Repeat("c", 63)
	for _, tc := range []struct {
		name, room, zone string
		kind             sig.Kind
		msg              []byte
	}{
		{"decoded limit", "room", "example.com", sig.Offer, make([]byte, maxMessageBytes+1)},
		{"wire limit", "room", longZone, sig.Offer, make([]byte, maxMessageBytes)},
		{"empty message", "room", "example.com", sig.Offer, nil},
		{"invalid room", "bad.room", "example.com", sig.Offer, []byte("safe")},
		{"invalid kind", "room", "example.com", sig.Kind(20), []byte("safe")},
		{"long owner", strings.Repeat("r", 63), longZone, sig.Offer, []byte("safe")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr, err := NewDynUpdate("localhost:53", tc.zone, testTSIGKey())
			if err != nil {
				t.Fatal(err)
			}
			r := new(updateFailure)
			tr.Resolver = r
			err = tr.Publish(context.Background(), tc.room, tc.kind, tc.msg)
			if err == nil || r.calls != 0 {
				t.Fatalf("unsafe result: calls=%d err=%v", r.calls, err)
			}
			if strings.Contains(tc.name, "limit") {
				var size *DynUpdateSizeError
				if !errors.As(err, &size) {
					t.Fatalf("untyped size error: %v", err)
				}
			}
		})
	}
	var zero DynUpdate
	if err := zero.Publish(context.Background(), "room", sig.Offer, []byte("safe")); err == nil {
		t.Fatal("zero configuration accepted")
	}
	tr := newPublishTransport(t, "localhost:53")
	tr.Zone = "bad..zone"
	if err := tr.Publish(context.Background(), "room", sig.Offer, []byte("safe")); err == nil {
		t.Fatal("invalid zone accepted")
	}
}

func TestDynUpdatePublishPreservesFailures(t *testing.T) {
	for _, code := range []int{mdns.RcodeRefused, mdns.RcodeNotZone, mdns.RcodeNotAuth} {
		tr := newPublishTransport(t, "localhost:53")
		tr.Resolver = &updateFailure{rcode: code}
		var rejection *DynUpdateRcodeError
		if err := tr.Publish(context.Background(), "room", sig.Answer, []byte("private-sdp")); !errors.As(err, &rejection) || rejection.Rcode != code || strings.Contains(err.Error(), "private-sdp") {
			t.Fatalf("rcode result: %v", err)
		}
	}
	cause := errors.New("route unavailable")
	for _, r := range []*updateFailure{{err: cause}, {nilReply: true}} {
		tr := newPublishTransport(t, "localhost:53")
		tr.Resolver = r
		err := tr.Publish(context.Background(), "room", sig.Offer, []byte("safe"))
		if err == nil || (r.err != nil && !errors.Is(err, cause)) {
			t.Fatalf("lost error: %v", err)
		}
	}
	tr := newPublishTransport(t, "localhost:53")
	r := new(updateFailure)
	tr.Resolver = r
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := tr.Publish(ctx, "room", sig.Offer, []byte("safe")); !errors.Is(err, context.Canceled) || r.calls != 0 {
		t.Fatalf("cancellation result: %v", err)
	}
}

func TestDynUpdatePublishAuthenticatesTCPReplacement(t *testing.T) {
	for _, mode := range []string{"valid", "unsigned", "bad signature", "refused", "wrong zone"} {
		t.Run(mode, func(t *testing.T) {
			key := testTSIGKey()
			server := &mdns.Server{TsigSecret: key.secrets(), MsgAcceptFunc: func(mdns.Header) mdns.MsgAcceptAction { return mdns.MsgAccept }}
			f := &updateFixture{t: t, mode: mode, key: key}
			server.Handler = f
			if mode == "bad signature" {
				server.TsigSecret = map[string]string{key.Name: "b3RoZXI="}
			}
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			server.Listener = ln
			if err := activateListener(server); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := server.Shutdown(); err != nil {
					t.Error(err)
				}
			})
			tr, err := NewDynUpdate(ln.Addr().String(), "example.com", key)
			if err != nil {
				t.Fatal(err)
			}
			for _, msg := range [][]byte{bytes.Repeat([]byte("long"), 100), []byte("short")} {
				err := tr.Publish(context.Background(), "room", sig.Offer, msg)
				if mode != "valid" {
					if err == nil {
						t.Fatal("rejected reply accepted")
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			query := new(mdns.Msg)
			query.SetQuestion("room.offer.sig.example.com.", mdns.TypeTXT)
			answer, err := tr.Resolver.Exchange(context.Background(), query)
			if err != nil || len(answer.Answer) != 1 {
				t.Fatalf("stale records remain: %v", err)
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.updates != 2 {
				t.Fatalf("got %d UPDATEs", f.updates)
			}
		})
	}
}

func TestDynUpdatePublishReturnsTCPRouteCause(t *testing.T) {
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
	if err := tr.Publish(context.Background(), "room", sig.Offer, []byte("safe")); !errors.As(err, &route) {
		t.Fatalf("lost network cause: %v", err)
	}
}

func TestDynUpdateWireSizeIncludesExactSignature(t *testing.T) {
	tr := newPublishTransport(t, "localhost:53")
	for _, operation := range []string{"UPDATE", "RRset response"} {
		for _, target := range []int{60 * 1024, 60*1024 + 1, 65535, 65536} {
			m, wire := sizedSignedMessage(t, tr, operation, target)
			if len(wire) != target {
				t.Fatalf("wire fixture: %d != %d", len(wire), target)
			}
			err := tr.checkWireSize(operation, m)
			if target == 60*1024 {
				if err != nil {
					t.Fatal(err)
				}
				continue
			}
			var bounded *DynUpdateSizeError
			if !errors.As(err, &bounded) || bounded.Size != target {
				t.Fatalf("wire bound: %v", err)
			}
			wantLimit := 60 * 1024
			if target > 65535 {
				wantLimit = 65535
			}
			if bounded.Limit != wantLimit {
				t.Fatalf("limit %d != %d", bounded.Limit, wantLimit)
			}
		}
	}
}

func TestDynUpdatePublishReplacesCompleteRRset(t *testing.T) {
	tr, err := NewDynUpdate("localhost:53", "EXAMPLE.com", testTSIGKey())
	if err != nil {
		t.Fatal(err)
	}
	r := new(updateCapture)
	tr.Resolver = r
	for _, msg := range [][]byte{bytes.Repeat([]byte("long"), 100), []byte("short")} {
		if err := tr.Publish(context.Background(), "Room", sig.Offer, msg); err != nil {
			t.Fatal(err)
		}
		m := r.calls[len(r.calls)-1]
		if m.Opcode != mdns.OpcodeUpdate || m.Question[0].Name != "example.com." || m.Question[0].Qtype != mdns.TypeSOA {
			t.Fatal("invalid UPDATE zone")
		}
		deleted := m.Ns[0].Header()
		if deleted.Name != "room.offer.sig.example.com." || deleted.Rrtype != mdns.TypeTXT || deleted.Class != mdns.ClassANY || deleted.Ttl != 0 {
			t.Fatal("missing RRset deletion")
		}
		var fragments []sig.Fragment
		for _, rr := range r.records {
			txt := rr.(*mdns.TXT)
			if txt.Hdr.Ttl != 1 || txt.Hdr.Name != deleted.Name {
				t.Fatal("invalid replacement record")
			}
			var seq, total uint16
			var payload string
			if _, err := fmt.Sscanf(txt.Txt[0], "%d-%d:%s", &seq, &total, &payload); err != nil {
				t.Fatal(err)
			}
			data, err := sig.DecodeLabel(payload)
			if err != nil {
				t.Fatal(err)
			}
			fragments = append(fragments, sig.Fragment{Seq: seq, Total: total, Data: data})
		}
		got, err := sig.Reassemble(fragments)
		if err != nil || !bytes.Equal(got, msg) {
			t.Fatalf("replacement mismatch: %v", err)
		}
	}
	if len(r.calls) != 2 || len(r.records) != 1 {
		t.Fatal("replacement retains stale records")
	}
}

// mu guards records and counts shared by handlers and assertions.
type updateFixture struct {
	mu      sync.Mutex
	stored  []mdns.RR
	updates int
	t       *testing.T
	mode    string
	key     *TSIGKey
}

func (f *updateFixture) ServeDNS(w mdns.ResponseWriter, req *mdns.Msg) {
	if req.IsTsig() == nil || (w.TsigStatus() != nil && f.mode != "bad signature") {
		f.t.Error("request authentication failed")
	}
	reply := new(mdns.Msg)
	reply.SetReply(req)
	f.mu.Lock()
	if req.Opcode == mdns.OpcodeUpdate {
		f.updates++
		if len(req.Ns) < 2 || req.Ns[0].Header().Class != mdns.ClassANY {
			f.t.Error("UPDATE lacks atomic replacement")
		}
		f.stored = append([]mdns.RR(nil), req.Ns[1:]...)
		for _, rr := range f.stored {
			if rr.Header().Ttl != 1 {
				f.t.Error("TTL differs from one")
			}
		}
	} else {
		reply.Answer = append([]mdns.RR(nil), f.stored...)
	}
	f.mu.Unlock()
	if f.mode == "refused" {
		reply.Rcode = mdns.RcodeRefused
	}
	if f.mode == "wrong zone" {
		reply.Rcode = mdns.RcodeNotZone
	}
	if f.mode != "unsigned" {
		reply.SetTsig(f.key.Name, f.key.Algorithm, 300, time.Now().Unix())
	}
	if err := w.WriteMsg(reply); err != nil {
		f.t.Error(err)
	}

}

func newPublishTransport(t *testing.T, addr string) *DynUpdate {
	t.Helper()
	tr, err := NewDynUpdate(addr, "example.com", testTSIGKey())
	if err != nil {
		t.Fatal(err)
	}
	return tr
}

func sizedSignedMessage(t *testing.T, tr *DynUpdate, operation string, target int) (*mdns.Msg, []byte) {
	t.Helper()
	m := new(mdns.Msg)
	m.SetQuestion("room.offer.sig.example.com.", mdns.TypeTXT)
	if operation == "UPDATE" {
		m.SetUpdate("example.com.")
	}
	for i := 0; i < 240; i++ {
		m.Answer = append(m.Answer, &mdns.TXT{Hdr: mdns.RR_Header{Name: "room.offer.sig.example.com.", Rrtype: mdns.TypeTXT, Class: mdns.ClassINET}, Txt: []string{strings.Repeat("a", 200)}})
	}
	// Padding keeps each TXT string below its one-byte length limit.
	unsigned, err := m.Pack()
	if err != nil {
		t.Fatal(err)
	}
	for len(unsigned) > target-1000 {
		m.Answer = m.Answer[:len(m.Answer)-1]
		unsigned, err = m.Pack()
		if err != nil {
			t.Fatal(err)
		}
	}
	signed := m.Copy()
	signed.SetTsig(tr.key.Name, tr.key.Algorithm, 300, time.Now().Unix())
	wire, _, err := mdns.TsigGenerate(signed, tr.key.Secret, "", false)
	if err != nil {
		t.Fatal(err)
	}
	remaining := target - len(wire)
	for remaining > 0 {
		n := remaining - 12
		if n > 200 {
			n = 200
		}
		if n < 0 {
			t.Fatal("invalid size fixture")
		}
		m.Extra = append(m.Extra, &mdns.TXT{Hdr: mdns.RR_Header{Name: ".", Rrtype: mdns.TypeTXT, Class: mdns.ClassINET}, Txt: []string{strings.Repeat("a", n)}})
		remaining -= n + 12
		if remaining > 0 && remaining < 12 {
			last := m.Extra[len(m.Extra)-1].(*mdns.TXT)
			last.Txt[0] += strings.Repeat("a", remaining)
			remaining = 0
		}
	}
	signed = m.Copy()
	signed.SetTsig(tr.key.Name, tr.key.Algorithm, 300, time.Now().Unix())
	wire, _, err = mdns.TsigGenerate(signed, tr.key.Secret, "", false)
	if err != nil {
		t.Fatal(err)
	}
	return m, wire
}
