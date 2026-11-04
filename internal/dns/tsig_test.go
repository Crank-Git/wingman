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
			_, err := LoadTSIGKey(path)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v error=%v", tc.valid, err)
			}
			if err != nil && strings.Contains(err.Error(), "secret-value") {
				t.Fatal("error exposes secret")
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
			srv.TSIGKey = testTSIGKey()
			if err := srv.Start(context.Background()); err != nil {
				t.Fatal(err)
			}
			defer srv.Stop()
			tr := &WriteOverQuery{Resolver: &Dialer{Addr: srv.Addr(), Net: network, TSIGKey: testTSIGKey()}, Zone: "example.com", PollRetries: 1}
			payload := bytes.Repeat([]byte("a"), 9000)
			if err := tr.Publish(context.Background(), "signed", sig.Offer, payload); err != nil {
				t.Fatal(err)
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
	for _, network := range []string{"udp", "tcp"} {
		for _, kind := range []string{"unsigned", "unknown", "wrong", "expired"} {
			t.Run(network+kind, func(t *testing.T) {
				m := new(mdns.Msg)
				payload, _ := sig.EncodeLabel([]byte("blocked"))
				m.SetQuestion(mdns.Fqdn(sig.RendezvousName("example.com", "room", sig.Offer, 0, 1, payload)), mdns.TypeTXT)
				key := testTSIGKey()
				stamp := time.Now().Unix()
				if kind == "unknown" {
					key.Name = "other.example."
				}
				if kind == "wrong" {
					key.Secret = "b3RoZXI="
				}
				if kind == "expired" {
					stamp -= 3600
				}
				c := &mdns.Client{Net: network, TsigSecret: key.secrets()}
				if kind != "unsigned" {
					m.SetTsig(key.Name, key.Algorithm, 300, stamp)
				}
				resp, _, err := c.Exchange(m, srv.Addr())
				if err == nil && resp.Rcode != mdns.RcodeNotAuth {
					t.Fatalf("unauthenticated response: %v", resp)
				}
				srv.mu.Lock()
				defer srv.mu.Unlock()
				if len(srv.frags) != 0 {
					t.Fatal("unauthenticated write reaches storage")
				}
			})
		}
	}
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
