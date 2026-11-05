package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	wdns "github.com/Crank-Git/wingman/internal/dns"
	"github.com/Crank-Git/wingman/internal/sig"
)

func TestTSIGCLITransport(t *testing.T) {
	path := filepath.Join(t.TempDir(), "key.json")
	if err := os.WriteFile(path, []byte(`{"name":"wingman.example.","algorithm":"hmac-sha256.","secret":"c2VjcmV0"}`), 0600); err != nil {
		t.Fatal(err)
	}
	key, err := wdns.LoadTSIGKey(path)
	if err != nil {
		t.Fatal(err)
	}
	srv, err := startServe(key)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Stop()
	opts, err := parsePeerFlags("offer", []string{"--addr", srv.Addr(), "--room", "auth", "--dns-tsig-key-file", path})
	if err != nil {
		t.Fatal(err)
	}
	tr, err := buildTransport(opts)
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte("signed CLI signaling")
	if err := tr.Publish(context.Background(), "auth", sig.Offer, payload); err != nil {
		t.Fatal(err)
	}
	got, err := tr.Poll(context.Background(), "auth", sig.Offer)
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("roundtrip: %q %v", got, err)
	}
	opts.dnsTSIGKeyFile = filepath.Join(t.TempDir(), "absent")
	if _, err := buildTransport(opts); err == nil {
		t.Fatal("invalid peer key accepted")
	}
	if err := runServe(context.Background(), []string{"--dns-tsig-key-file", opts.dnsTSIGKeyFile}); err == nil {
		t.Fatal("invalid server key accepted")
	}
}
