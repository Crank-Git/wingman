package dns

import (
	"errors"
	"testing"

	"github.com/Crank-Git/wingman/internal/metrics"
	mdns "github.com/miekg/dns"
)

type metricsBarrierWriter struct {
	mdns.ResponseWriter
	entered    chan struct{}
	release    chan struct{}
	status     error
	writeError error
}

func (w *metricsBarrierWriter) TsigStatus() error { return w.status }

func (w *metricsBarrierWriter) WriteMsg(*mdns.Msg) error {
	// The client can receive a response before the writer returns to the handler.
	close(w.entered)
	<-w.release
	return w.writeError
}

func TestDNSMetricsPrecedeResponseDelivery(t *testing.T) {
	for _, tc := range []struct {
		name                                                   string
		query                                                  *mdns.Msg
		authenticated, signed, badSignature, quota, writeFails bool
		want                                                   [3][2]int64
	}{
		{name: "accepted write", query: fragmentQuery("one.test", "abc", 0, 1), want: [3][2]int64{{1, 0}, {0, 0}, {0, 0}}},
		{name: "empty poll", query: queryMsg("room.offer.sig.one.test"), want: [3][2]int64{{0, 0}, {1, 0}, {0, 0}}},
		{name: "unsigned poll", query: queryMsg("room.offer.sig.one.test"), authenticated: true, want: [3][2]int64{{0, 0}, {0, 1}, {0, 0}}},
		{name: "wrong key poll", query: queryMsg("room.offer.sig.one.test"), authenticated: true, signed: true, badSignature: true, want: [3][2]int64{{0, 0}, {0, 1}, {0, 0}}},
		{name: "signed write", query: fragmentQuery("one.test", "abc", 0, 1), authenticated: true, signed: true, want: [3][2]int64{{1, 0}, {0, 0}, {0, 0}}},
		{name: "malformed request", query: queryMsg("bad.one.test"), want: [3][2]int64{{0, 0}, {0, 0}, {0, 1}}},
		{name: "quota rejection", query: fragmentQuery("one.test", "abc", 0, 1), quota: true, want: [3][2]int64{{0, 1}, {0, 0}, {0, 0}}},
		{name: "failed response write", query: fragmentQuery("one.test", "abc", 0, 1), writeFails: true, want: [3][2]int64{{1, 0}, {0, 0}, {0, 0}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := NewServer()
			s.Metrics = metrics.New()
			w := &metricsBarrierWriter{entered: make(chan struct{}), release: make(chan struct{})}
			if tc.authenticated {
				s.TSIGKey = testTSIGKey()
			}
			if tc.signed {
				tc.query.SetTsig(s.TSIGKey.Name, s.TSIGKey.Algorithm, 300, 1)
			}
			if tc.badSignature {
				w.status = errors.New("bad signature")
			}
			if tc.writeFails {
				w.writeError = errors.New("response write fails")
			}
			if tc.quota {
				s.limits.retainedBytes = 1
			}
			done := make(chan struct{})
			go func() { defer close(done); s.makeHandler(0)(w, tc.query) }()
			<-w.entered
			atDelivery := s.Metrics.Snapshot().DNSRequests
			close(w.release)
			<-done
			if atDelivery != tc.want {
				t.Fatalf("counter at response delivery: got %v, want %v", atDelivery, tc.want)
			}
			if got := s.Metrics.Snapshot().DNSRequests; got != tc.want {
				t.Fatalf("counter after handler completion: got %v, want %v", got, tc.want)
			}
		})
	}
}
