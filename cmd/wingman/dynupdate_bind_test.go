//go:build bindintegration

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Crank-Git/wingman/internal/dns"
)

func TestBINDCLIExplainsDynUpdateAuthenticationFailures(t *testing.T) {
	keyPath := os.Getenv("WINGMAN_BIND_KEY")
	key, err := dns.LoadTSIGKey(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	wrongKey := filepath.Join(t.TempDir(), "wrong.json")
	const wrongSecret = "d3Jvbmc="
	if err := os.WriteFile(wrongKey, []byte(`{"name":"`+key.Name+`","algorithm":"`+key.Algorithm+`","secret":"`+wrongSecret+`"}`), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, zone, key string }{
		{"wrong zone", "wrong.test", keyPath},
		{"wrong key", "example.test", wrongKey},
	} {
		t.Run(tc.name, func(t *testing.T) {
			command := exec.Command(os.Getenv("WINGMAN_BIND_CLI"), "offer", "--signaling", "dynupdate", "--addr", os.Getenv("WINGMAN_BIND_ADDR"), "--zone", tc.zone, "--dns-tsig-key-file", tc.key, "--room", "authguidance", "--handshake-timeout", "2s")
			output, err := command.CombinedOutput()
			if err == nil {
				t.Fatal("invalid configuration succeeds")
			}
			text := string(output)
			for _, check := range []string{"DynUpdate publish exchange:", "zone", "authoritative endpoint", "key identity/file", "update policy"} {
				if !strings.Contains(text, check) {
					t.Errorf("missing %q: %s", check, text)
				}
			}
			for _, private := range []string{key.Secret, wrongSecret, "v=0", "a=ice-ufrag", "DNS rcode", "NOTAUTH"} {
				if strings.Contains(text, private) {
					t.Errorf("output contains private data or an untrusted cause: %s", text)
				}
			}
		})
	}
}
