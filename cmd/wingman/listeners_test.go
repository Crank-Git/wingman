package main

import (
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestServeRequiresExplicitDemo(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, args := range [][]string{nil, {"--listen", "0.0.0.0:0"}, {"--local-demo", "--listen", "0.0.0.0:0"}, {"--local-demo", "--listen", "localhost:0"}, {"--local-demo", "--listen", "bad"}} {
		if err := runServe(ctx, args); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
	if err := runServe(ctx, []string{"--local-demo"}); err != nil {
		t.Fatal(err)
	}
}

func TestServeReportsOnlyBoundAddresses(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	for _, address := range []string{"127.0.0.1:0", ln.Addr().String()} {
		t.Run(address, func(t *testing.T) {
			r, w, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close()
			defer w.Close()
			old := os.Stdout
			os.Stdout = w
			defer func() { os.Stdout = old }()
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			err = runServe(ctx, []string{"--local-demo", "--listen", address})
			if closeErr := w.Close(); closeErr != nil {
				t.Fatal(closeErr)
			}
			out, readErr := io.ReadAll(r)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if address == ln.Addr().String() {
				if err == nil || len(out) != 0 {
					t.Fatalf("bind conflict: %v %s", err, out)
				}
			} else if err != nil || !strings.Contains(string(out), "listening on 127.0.0.1:") || strings.Contains(string(out), ":0 ") {
				t.Fatalf("actual address: %v %s", err, out)
			}
		})
	}
}

func TestServeAcceptsAuthenticatedRemoteListener(t *testing.T) {
	path := filepath.Join(t.TempDir(), "key.json")
	if err := os.WriteFile(path, []byte(`{"name":"test.example.","algorithm":"hmac-sha256.","secret":"c2VjcmV0"}`), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := runServe(ctx, []string{"--listen", "0.0.0.0:0", "--zone", "EXAMPLE.COM.", "--dns-tsig-key-file", path}); err != nil {
		t.Fatal(err)
	}
	if err := runServe(ctx, []string{"--listen", "0.0.0.0:0", "--dns-tsig-key-file", path + "missing"}); err == nil {
		t.Fatal("missing key permits remote listener")
	}
}

func TestServeRejectsInvalidZone(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := runServe(ctx, []string{"--local-demo", "--zone", "bad..zone"})
	if err == nil || !strings.Contains(err.Error(), "zone") {
		t.Fatalf("zone error: %v", err)
	}
}

func TestServeRejectsEmptyZone(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := runServe(ctx, []string{"--local-demo", "--zone", ""}); err == nil {
		t.Fatal("CLI accepts unrestricted zone")
	}
}
