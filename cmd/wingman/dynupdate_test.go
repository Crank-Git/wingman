package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Crank-Git/wingman/internal/dns"
)

func TestDynUpdateRequiresExplicitConfig(t *testing.T) {
	for _, flags := range [][]string{
		{"--zone", "example.com", "--dns-tsig-key-file", "key"},
		{"--addr", " ", "--zone", "example.com", "--dns-tsig-key-file", "key"},
		{"--addr", "localhost:53", "--dns-tsig-key-file", "key"},
		{"--addr", "localhost:53", "--zone", " ", "--dns-tsig-key-file", "key"},
		{"--addr", "localhost:53", "--zone", "example.com"},
		{"--addr", "localhost:53", "--zone", "example.com", "--dns-tsig-key-file", " "},
	} {
		_, err := parsePeerFlags("offer", append([]string{"--signaling", "dynupdate", "--room", "test"}, flags...))
		if err == nil {
			t.Errorf("accepted flags %q", flags)
		}
	}
}

func TestDynUpdateCLIConstructsTransport(t *testing.T) {
	path := filepath.Join(t.TempDir(), "key.json")
	if err := os.WriteFile(path, []byte(`{"name":"wingman.example.","algorithm":"hmac-sha256.","secret":"c2VjcmV0"}`), 0600); err != nil {
		t.Fatal(err)
	}
	opts, err := parsePeerFlags("answer", []string{"--signaling", "dynupdate", "--room", "test", "--addr", "localhost:53", "--zone=example.com", "--dns-tsig-key-file", path})
	if err != nil {
		t.Fatal(err)
	}
	tr, err := buildTransport(opts)
	if err != nil {
		t.Fatal(err)
	}
	d := tr.(*dns.DynUpdate).Resolver.(*dns.Dialer)
	if d.Net != "tcp" || d.TSIGKey == nil || opts.zone != defaultZone {
		t.Fatal("CLI loses authenticated TCP configuration")
	}
	for _, path := range []string{"", filepath.Join(t.TempDir(), "missing")} {
		opts.dnsTSIGKeyFile = path
		if _, err := buildTransport(opts); err == nil {
			t.Fatal("accepted missing key")
		}
	}
}

func TestDynUpdateRejectsBadKeyWithoutSecretOutput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "key.json")
	if err := os.WriteFile(path, []byte(`{"name":"wingman.example.","algorithm":"hmac-sha256.","secret":"private-value"}`), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := buildTransport(peerOpts{signaling: "dynupdate", addr: "localhost:53", zone: "example.com", dnsTSIGKeyFile: path})
	if err == nil || strings.Contains(err.Error(), "private-value") {
		t.Fatalf("unsafe key error: %v", err)
	}
	var help bytes.Buffer
	usage(&help)
	if !strings.Contains(help.String(), "dynupdate") || strings.Contains(help.String(), "private-value") {
		t.Fatal("help omits DynUpdate or exposes a secret")
	}
}

func TestDynUpdatePreservesExistingZoneDefaults(t *testing.T) {
	for _, args := range [][]string{{"--addr", "localhost:53"}, {"--signaling", "https", "--signaling-url", "https://example.com"}} {
		opts, err := parsePeerFlags("offer", append([]string{"--room", "test"}, args...))
		if err != nil || opts.zone != defaultZone {
			t.Fatalf("zone default changes: %q %v", opts.zone, err)
		}
		if _, err := buildTransport(opts); err != nil {
			t.Fatal(err)
		}
	}
}

func checkDynUpdateCLI(t *testing.T, bin string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "key.json")
	if err := os.WriteFile(path, []byte(`{"name":"wingman.example.","algorithm":"hmac-sha256.","secret":"private-value"}`), 0600); err != nil {
		t.Fatal(err)
	}
	for _, role := range []string{"offer", "answer"} {
		for _, flags := range [][]string{
			{},
			{"--addr", "127.0.0.1:1", "--dns-tsig-key-file", path},
			{"--addr", "127.0.0.1:1", "--zone", "example.com"},
			{"--addr", "127.0.0.1:1", "--zone", "example.com", "--dns-tsig-key-file", path},
		} {
			args := append([]string{role, "--signaling", "dynupdate", "--room", "test"}, flags...)
			out, err := exec.Command(bin, args...).CombinedOutput()
			if err == nil || bytes.Contains(out, []byte("private-value")) || bytes.Contains(out, []byte("dial tcp")) {
				t.Fatalf("unsafe CLI failure: %v %s", err, out)
			}
			if !bytes.Contains(out, []byte("requires")) && !bytes.Contains(out, []byte("valid base64")) {
				t.Fatalf("CLI omits configuration error: %s", out)
			}
		}
	}
}
