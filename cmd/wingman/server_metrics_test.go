package main

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Crank-Git/wingman/internal/dns"
	"github.com/Crank-Git/wingman/internal/sig"
	mdns "github.com/miekg/dns"
)

func TestServeMetricsReportSignedDNSActivity(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	key := &dns.TSIGKey{Name: "wingman.example.", Algorithm: mdns.HmacSHA256, Secret: "c2VjcmV0"}
	out := &readinessOutput{lines: make(chan string, 8), release: make(chan struct{})}
	done := make(chan error, 1)
	go func() {
		done <- serveListeners(ctx, serveConfig{listen: "127.0.0.1:0", zone: "one.test", key: key, metricsAddr: "127.0.0.1:0"}, out)
	}()
	defer func() {
		cancel()
		close(out.release)
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	var address, base string
	for base == "" {
		select {
		case line := <-out.lines:
			if strings.HasPrefix(line, "wingman server listening on ") {
				address = strings.Fields(line)[4]
			}
			if strings.HasPrefix(line, "metrics listening on ") {
				base = "http://" + strings.TrimSuffix(strings.TrimSpace(strings.TrimPrefix(line, "metrics listening on ")), "/metrics")
			}
		case <-ctx.Done():
			t.Fatal("listener reports time out")
		}
	}
	client := &http.Client{Timeout: time.Second}
	scrape := func(path string) string {
		t.Helper()
		r, err := client.Get(base + path)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Body.Close()
		b, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		if r.StatusCode != 200 {
			t.Fatalf("%s: %d", path, r.StatusCode)
		}
		return string(b)
	}
	before := scrape("/metrics")
	d := &dns.Dialer{Addr: address, TSIGKey: key}
	payload, err := sig.EncodeLabel([]byte("abc"))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{sig.RendezvousName("one.test", "room", sig.Offer, 0, 1, payload), sig.PollName("one.test", "room", sig.Offer)} {
		q := new(mdns.Msg)
		q.SetQuestion(mdns.Fqdn(name), mdns.TypeTXT)
		if _, err := d.Exchange(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	after := scrape("/metrics")
	for _, line := range []string{`wingman_dns_requests_total{operation="write",result="success"} 1`, `wingman_dns_requests_total{operation="poll",result="success"} 1`, "wingman_dns_retained_payload_bytes 3"} {
		if strings.Contains(before, line) || !strings.Contains(after, line) {
			t.Fatalf("missing change %s: %s", line, after)
		}
	}
	if !strings.Contains(after, "wingman_connections_attempted 0") {
		t.Fatal(after)
	}
	scrape("/healthz")
	scrape("/readyz")
}
