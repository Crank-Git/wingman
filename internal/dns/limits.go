package dns

import (
	"strings"

	"github.com/Crank-Git/wingman/internal/sig"
	"github.com/miekg/dns"
)

const (
	maxMessageBytes   = 16 * 1024
	fragmentDataBytes = 35
	messageChunkBytes = 31
	messageCRCBytes   = 4
)

// Set limits before Start or requests. Server.mu guards quota accounting.
type storageLimits struct {
	messageBytes  int
	keys          int
	retainedBytes int
	responseBytes int
}

func defaultStorageLimits() storageLimits {
	return storageLimits{maxMessageBytes, 1024, 16 * 1024 * 1024, 60 * 1024}
}

// pollResponseSize bounds an uncompressed response with maximum payloads and sequence widths.
func pollResponseSize(key storageKey, total uint16) int {
	poll := dns.Fqdn(sig.PollName(key.zone, key.room, key.kind))
	req := new(dns.Msg)
	req.SetQuestion(poll, dns.TypeTXT)
	resp := new(dns.Msg)
	resp.SetReply(req)
	resp.SetEdns0(edns0UDPSize, false)
	name := sig.RendezvousName(key.zone, key.room, key.kind, total-1, total, strings.Repeat("a", 56))
	rr := &dns.TXT{Hdr: dns.RR_Header{Name: poll, Rrtype: dns.TypeTXT, Class: dns.ClassINET, Ttl: 1}, Txt: []string{name}}
	return resp.Len() + int(total)*dns.Len(rr)
}

func fragmentLimit(limits storageLimits) int {
	return (limits.messageBytes + messageChunkBytes - 1) / messageChunkBytes
}

func fitsResponse(key storageKey, total uint16, limits storageLimits) bool {
	name := sig.RendezvousName(key.zone, key.room, key.kind, total-1, total, strings.Repeat("a", 56))
	return validName(name) && pollResponseSize(key, total) <= limits.responseBytes
}

func messageLimit(key storageKey, limits storageLimits) int {
	total := fragmentLimit(limits)
	for total > 0 && !fitsResponse(key, uint16(total), limits) {
		total--
	}
	limit := total * messageChunkBytes
	if limit > limits.messageBytes {
		return limits.messageBytes
	}
	return limit
}
