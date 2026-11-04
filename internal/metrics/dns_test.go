package metrics

import (
	"strings"
	"sync"
	"testing"
)

func TestDNSMetricsBoundLabels(t *testing.T) {
	var m Metrics
	for _, op := range []string{DNSWrite, DNSPoll, DNSRequest} {
		for _, result := range []string{DNSSuccess, DNSRejected} {
			m.RecordDNSRequest(op, result)
		}
	}
	m.RecordDNSRequest("private-room", "private-error")
	m.SetDNSRetainedPayloadBytes(17)
	text := m.Text()
	if strings.Contains(text, "private") {
		t.Fatal(text)
	}
	if strings.Count(text, "wingman_dns_requests_total{operation=") != 6 {
		t.Fatal(text)
	}
	if !strings.Contains(text, `wingman_dns_requests_total{operation="request",result="rejected"} 2`) || !strings.Contains(text, "wingman_dns_retained_payload_bytes 17\n") {
		t.Fatal(text)
	}
	if text != m.Text() {
		t.Fatal("exposition changes without an event")
	}
	snap := m.Snapshot()
	snap.DNSRequests[0][0] = 999
	if m.Snapshot().DNSRequests[0][0] != 1 {
		t.Fatal("snapshot changes the collector")
	}
}

func TestDNSMetricsAcceptConcurrentEvents(t *testing.T) {
	var m Metrics
	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() {
			for range 100 {
				m.RecordDNSRequest(DNSWrite, DNSSuccess)
				m.SetDNSRetainedPayloadBytes(9)
				_ = m.Text()
			}
		})
	}
	wg.Wait()
	if got := m.Snapshot(); got.DNSRequests[0][0] != 1200 || got.DNSRetainedPayloadBytes != 9 {
		t.Fatal(got)
	}
}
