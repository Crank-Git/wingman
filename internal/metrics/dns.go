package metrics

// DNS operations and results bound the labels in wingman_dns_requests_total.
const (
	DNSWrite    = "write"
	DNSPoll     = "poll"
	DNSRequest  = "request"
	DNSSuccess  = "success"
	DNSRejected = "rejected"
)

// RecordDNSRequest counts one query. Unknown values count as request or rejected.
func (m *Metrics) RecordDNSRequest(operation, result string) {
	i, j := 2, 1
	switch operation {
	case DNSWrite:
		i = 0
	case DNSPoll:
		i = 1
	}
	if result == DNSSuccess {
		j = 0
	}
	m.mu.Lock()
	m.dnsRequests[i][j]++
	m.mu.Unlock()
}

// SetDNSRetainedPayloadBytes sets wingman_dns_retained_payload_bytes.
// The caller holds its storage lock to preserve mutation order.
func (m *Metrics) SetDNSRetainedPayloadBytes(bytes int64) {
	m.mu.Lock()
	m.dnsRetainedPayloadBytes = bytes
	m.mu.Unlock()
}
