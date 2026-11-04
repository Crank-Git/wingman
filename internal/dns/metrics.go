package dns

import (
	"strings"

	"github.com/Crank-Git/wingman/internal/metrics"
	"github.com/Crank-Git/wingman/internal/sig"
	"github.com/miekg/dns"
)

type metricsWriter struct {
	dns.ResponseWriter
	code int
}

func (w *metricsWriter) WriteMsg(m *dns.Msg) error {
	w.code = m.Rcode
	return w.ResponseWriter.WriteMsg(m)
}

func (s *Server) recordQuery(req *dns.Msg, code int) {
	operation := queryOperation(req)
	result := metrics.DNSRejected
	if code == dns.RcodeSuccess || (operation == metrics.DNSPoll && code == dns.RcodeNameError) {
		result = metrics.DNSSuccess
	}
	s.Metrics.RecordDNSRequest(operation, result)
}

func queryOperation(req *dns.Msg) string {
	if len(req.Question) != 1 || req.Question[0].Qtype != dns.TypeTXT || req.Question[0].Qclass != dns.ClassINET {
		return metrics.DNSRequest
	}
	name := strings.ToLower(strings.TrimSuffix(req.Question[0].Name, "."))
	if !validName(name) {
		return metrics.DNSRequest
	}
	if _, err := sig.ParseRendezvousName(name); err == nil {
		return metrics.DNSWrite
	}
	fields := strings.Split(name, ".")
	if len(fields) >= 4 && fields[2] == sig.SigLabel {
		if _, err := sig.ParseKind(fields[1]); err == nil {
			return metrics.DNSPoll
		}
	}
	return metrics.DNSRequest
}

// updateRetainedMetrics requires mu. Metrics methods never acquire Server locks.
func (s *Server) updateRetainedMetrics() {
	if s.Metrics != nil {
		s.Metrics.SetDNSRetainedPayloadBytes(int64(s.retainedBytes))
	}
}
