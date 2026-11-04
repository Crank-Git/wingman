package dns

import (
	"context"
	"strings"
	"testing"

	"github.com/Crank-Git/wingman/internal/sig"
	mdns "github.com/miekg/dns"
)

func TestDynUpdateRejectsInvalidConfig(t *testing.T) {
	key := &TSIGKey{Name: "wingman.example.", Algorithm: mdns.HmacSHA256, Secret: "c2VjcmV0"}
	for _, addr := range []string{"", " ", "localhost", ":53", "localhost:0", "localhost:65536", "localhost:bad", " local:53"} {
		if _, err := NewDynUpdate(addr, "example.com", key); err == nil {
			t.Errorf("accepted address %q", addr)
		}
	}
	for _, zone := range []string{"", " ", ".", "bad..example", "bad\\name"} {
		if _, err := NewDynUpdate("localhost:53", zone, key); err == nil {
			t.Errorf("accepted zone %q", zone)
		}
	}
	for _, key := range []*TSIGKey{nil, {Name: "bad", Secret: "private-value"}, {Name: "wingman.example.", Algorithm: mdns.HmacSHA256, Secret: "private-value"}} {
		_, err := NewDynUpdate("localhost:53", "example.com", key)
		if err == nil || strings.Contains(err.Error(), "private-value") {
			t.Fatalf("unsafe key result: %v", err)
		}
	}
}

func TestDynUpdateConstructsAuthenticatedTCPResolver(t *testing.T) {
	key := &TSIGKey{Name: "wingman.example.", Algorithm: mdns.HmacSHA256, Secret: "c2VjcmV0"}
	tr, err := NewDynUpdate("[::1]:5353", "EXAMPLE.com.", key)
	if err != nil {
		t.Fatal(err)
	}
	var _ sig.Transport = tr
	d := tr.Resolver.(*Dialer)
	if tr.Zone != "example.com" || d.Net != "tcp" || d.Addr != "[::1]:5353" || d.Timeout <= 0 || *d.TSIGKey != *key {
		t.Fatalf("invalid transport configuration: zone=%q network=%q", tr.Zone, d.Net)
	}
	key.Name = "changed.example."
	if d.TSIGKey.Name != "wingman.example." || tr.key.Name != d.TSIGKey.Name {
		t.Fatal("caller mutation changes the retained key")
	}
	if _, err := tr.Poll(context.Background(), "room", sig.Answer); err == nil {
		t.Fatal("Poll reports success before issue #43")
	}
}

func TestDynUpdateAcceptsHostsWithoutResolution(t *testing.T) {
	key := &TSIGKey{Name: "wingman.example.", Algorithm: mdns.HmacSHA256, Secret: "c2VjcmV0"}
	for _, addr := range []string{"ns1.example.invalid:53", "127.0.0.1:5353", "[::1]:53"} {
		tr, err := NewDynUpdate(addr, "example.com", key)
		if err != nil || tr.Resolver.(*Dialer).Addr != addr {
			t.Fatalf("address %q: %v", addr, err)
		}
	}
}
