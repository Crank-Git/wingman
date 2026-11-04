package dns

import (
	"bytes"
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Crank-Git/wingman/internal/sig"
	mdns "github.com/miekg/dns"
)

func testTSIGKey() *TSIGKey {
	return &TSIGKey{Name: "wingman.example.", Algorithm: mdns.HmacSHA256, Secret: "c2VjcmV0"}
}

func TestLoadTSIGKey(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		mode       os.FileMode
		valid      bool
	}{
		{"valid", `{"name":"wingman.example.","algorithm":"hmac-sha256.","secret":"c2VjcmV0"}`, 0600, true},
		{"conventional algorithm", `{"name":"wingman.example.","algorithm":"hmac-sha256","secret":"c2VjcmV0"}`, 0600, true},
		{"read only", `{"name":"wingman.example.","algorithm":"hmac-sha256.","secret":"c2VjcmV0"}`, 0400, true},
		{"public", `{}`, 0644, false}, {"unreadable", `{}`, 0200, false},
		{"malformed", `secret-value`, 0600, false},
		{"extra", `{"extra":"secret-value"}`, 0600, false},
		{"missing", `{}`, 0600, false},
		{"name", `{"name":"Wingman.example","algorithm":"hmac-sha256.","secret":"c2VjcmV0"}`, 0600, false},
		{"algorithm", `{"name":"wingman.example.","algorithm":"hmac-sha1.","secret":"c2VjcmV0"}`, 0600, false},
		{"secret", `{"name":"wingman.example.","algorithm":"hmac-sha256.","secret":"secret-value"}`, 0600, false},
		{"trailing", `{} {}`, 0600, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "key.json")
			if err := os.WriteFile(path, []byte(tc.body), tc.mode); err != nil {
				t.Fatal(err)
			}
			key, err := LoadTSIGKey(path)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v error=%v", tc.valid, err)
			}
			if err != nil && strings.Contains(err.Error(), "secret-value") {
				t.Fatal("error exposes secret")
			}
			if err == nil && key.Algorithm != mdns.HmacSHA256 {
				t.Fatal("algorithm does not use its wire name")
			}
		})
	}
	for _, path := range []string{filepath.Join(t.TempDir(), "absent"), t.TempDir()} {
		if _, err := LoadTSIGKey(path); err == nil {
			t.Fatal("invalid file accepted")
		}
	}
}

func TestTSIGRoundTrip(t *testing.T) {
	for _, network := range []string{"udp", "tcp"} {
		t.Run(network, func(t *testing.T) {
			srv := NewServer()
			key := testTSIGKey()
			key.Name = strings.Repeat("a", 63) + "." + strings.Repeat("b", 63) + "." + strings.Repeat("c", 63) + "." + strings.Repeat("d", 61) + "."
			srv.TSIGKey = key
			if err := srv.Start(context.Background()); err != nil {
				t.Fatal(err)
			}
			defer srv.Stop()
			tr := &WriteOverQuery{Resolver: &Dialer{Addr: srv.Addr(), Net: network, TSIGKey: key}, Zone: "example.com", PollRetries: 1}
			payload := bytes.Repeat([]byte("a"), messageLimit(storageKey{zone: "example.com", room: "signed", kind: sig.Offer}, defaultStorageLimits()))
			if err := tr.Publish(context.Background(), "signed", sig.Offer, payload); err != nil {
				t.Fatal(err)
			}
			m := queryMsg(sig.PollName("example.com", "signed", sig.Offer))
			m.SetTsig(key.Name, mdns.HmacSHA256, 300, time.Now().Unix())
			wire, requestMAC, err := mdns.TsigGenerate(m, key.Secret, "", false)
			if err != nil {
				t.Fatal(err)
			}
			conn, err := mdns.Dial(network, srv.Addr())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := conn.Close(); err != nil {
					t.Error(err)
				}
			})
			if err := conn.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
				t.Fatal(err)
			}
			if _, err := conn.Write(wire); err != nil {
				t.Fatal(err)
			}
			conn.UDPSize = edns0UDPSize
			wire, err = conn.ReadMsgHeader(nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := mdns.TsigVerify(bytes.Clone(wire), key.Secret, requestMAC, false); err != nil {
				t.Fatal(err)
			}
			resp := new(mdns.Msg)
			if err := resp.Unpack(wire); err != nil {
				t.Fatal(err)
			}
			if err := key.validateReply(resp); err != nil {
				t.Fatal(err)
			}
			budget := mdns.MaxMsgSize
			if network == "udp" {
				budget = edns0UDPSize
			}
			if len(wire) > budget || resp.Truncated != (network == "udp") {
				t.Fatalf("signed response size=%d budget=%d truncated=%v", len(wire), budget, resp.Truncated)
			}
			got, err := tr.Poll(context.Background(), "signed", sig.Offer)
			if err != nil || !bytes.Equal(got, payload) {
				t.Fatalf("roundtrip: %d bytes, %v", len(got), err)
			}
		})
	}
}

func TestTSIGRejectsRequests(t *testing.T) {
	srv := NewServer()
	srv.TSIGKey = testTSIGKey()
	if err := srv.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer srv.Stop()
	tr := &WriteOverQuery{Resolver: &Dialer{Addr: srv.Addr(), TSIGKey: testTSIGKey()}, Zone: "example.com", PollRetries: 1}
	payload := []byte("private stored message")
	if err := tr.Publish(context.Background(), "room", sig.Offer, payload); err != nil {
		t.Fatal(err)
	}
	srv.mu.Lock()
	retained := srv.retainedBytes
	srv.mu.Unlock()
	label, err := sig.EncodeLabel([]byte("blocked"))
	if err != nil {
		t.Fatal(err)
	}
	for _, network := range []string{"udp", "tcp"} {
		for _, kind := range []string{"unsigned", "unknown", "wrong", "expired", "corrupt MAC"} {
			for _, name := range []string{
				sig.RendezvousName("example.com", "blocked", sig.Offer, 0, 1, label),
				sig.PollName("example.com", "room", sig.Offer),
			} {
				t.Run(network+"/"+kind+"/"+name, func(t *testing.T) {
					resp := rejectedTSIGRequest(t, srv.Addr(), network, name, kind)
					if resp.Rcode != mdns.RcodeNotAuth || len(resp.Answer) != 0 {
						t.Fatalf("rejected request exposes a response: %v", resp)
					}
					srv.mu.Lock()
					defer srv.mu.Unlock()
					if len(srv.frags) != 1 || srv.retainedBytes != retained {
						t.Fatal("rejected request changes storage")
					}
				})
			}
		}
	}
	got, err := tr.Poll(context.Background(), "room", sig.Offer)
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("rejected polls change stored message: %q, %v", got, err)
	}
}

func rejectedTSIGRequest(t *testing.T, addr, network, name, kind string) *mdns.Msg {
	t.Helper()
	m := queryMsg(name)
	key := testTSIGKey()
	stamp := time.Now().Unix()
	switch kind {
	case "unknown":
		key.Name = "other.example."
	case "wrong":
		key.Secret = "b3RoZXI="
	case "expired":
		stamp -= 3600
	}
	var wire []byte
	var err error
	if kind == "unsigned" {
		wire, err = m.Pack()
	} else {
		m.SetTsig(key.Name, key.Algorithm, 300, stamp)
		wire, _, err = mdns.TsigGenerate(m, key.Secret, "", false)
	}
	if err != nil {
		t.Fatal(err)
	}
	if kind == "corrupt MAC" {
		if err := m.Unpack(wire); err != nil {
			t.Fatal(err)
		}
		mac := m.IsTsig().MAC
		if mac[0] == '0' {
			m.IsTsig().MAC = "1" + mac[1:]
		} else {
			m.IsTsig().MAC = "0" + mac[1:]
		}
		wire, err = m.Pack()
		if err != nil {
			t.Fatal(err)
		}
	}
	conn, err := mdns.Dial(network, addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := conn.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := conn.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write(wire); err != nil {
		t.Fatal(err)
	}
	resp, err := conn.ReadMsg()
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestTSIGRejectsReplies(t *testing.T) {
	for _, network := range []string{"udp", "tcp"} {
		for _, mode := range []string{"unsigned", "wrong"} {
			t.Run(network+mode, func(t *testing.T) {
				server := &mdns.Server{TsigSecret: testTSIGKey().secrets(), Handler: mdns.HandlerFunc(func(w mdns.ResponseWriter, r *mdns.Msg) {
					reply := new(mdns.Msg)
					reply.SetReply(r)
					if mode == "wrong" {
						reply.SetTsig(testTSIGKey().Name, mdns.HmacSHA256, 300, time.Now().Unix())
					}
					if err := w.WriteMsg(reply); err != nil {
						t.Error(err)
					}
				})}
				var addr string
				if network == "udp" {
					pc, err := net.ListenPacket("udp", "127.0.0.1:0")
					if err != nil {
						t.Fatal(err)
					}
					server.PacketConn = pc
					addr = pc.LocalAddr().String()
				} else {
					ln, err := net.Listen("tcp", "127.0.0.1:0")
					if err != nil {
						t.Fatal(err)
					}
					server.Listener = ln
					addr = ln.Addr().String()
				}
				if mode == "wrong" {
					server.TsigSecret = map[string]string{testTSIGKey().Name: "b3RoZXI="}
				}
				if err := activateListener(server); err != nil {
					t.Fatal(err)
				}
				defer server.Shutdown()
				d := &Dialer{Addr: addr, Net: network, TSIGKey: testTSIGKey()}
				if _, err := d.Exchange(context.Background(), queryMsg("room.offer.sig.example.com")); err == nil {
					t.Fatal("unauthenticated reply accepted")
				}
			})
		}
	}
}
