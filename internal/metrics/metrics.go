// Package metrics collects the runtime statistics Wingman reports about its
// connection attempts, candidate distribution, handshake latency, and failure
// rates. All methods are safe for concurrent use.
package metrics

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// Metrics holds Wingman's runtime counters. Every field is guarded by mu so the
// zero value is ready to use.
type Metrics struct {
	mu sync.RWMutex

	connectionsAttempted int64
	connectionsSucceeded int64
	connectionsFailed    int64
	failures             int64

	candidates map[string]int64

	handshakeLatencyTotal time.Duration
	handshakeLatencyMax   time.Duration
	handshakeCount        int64
}

// New returns an empty, ready-to-use Metrics.
func New() *Metrics {
	return &Metrics{candidates: make(map[string]int64)}
}

// ConnectionAttempted records that a connection handshake was started.
func (m *Metrics) ConnectionAttempted() {
	m.mu.Lock()
	m.connectionsAttempted++
	m.mu.Unlock()
}

// ConnectionSucceeded records that a handshake completed successfully.
func (m *Metrics) ConnectionSucceeded() {
	m.mu.Lock()
	m.connectionsSucceeded++
	m.mu.Unlock()
}

// ConnectionFailed records that a handshake ended in failure.
func (m *Metrics) ConnectionFailed() {
	m.mu.Lock()
	m.connectionsFailed++
	m.mu.Unlock()
}

// Failure records a generic failure (signaling, gathering, or ICE).
func (m *Metrics) Failure() {
	m.mu.Lock()
	m.failures++
	m.mu.Unlock()
}

// RecordCandidate counts host, srflx, prflx, and relay candidates.
// Other values count as unknown to keep metric names valid and bounded.
func (m *Metrics) RecordCandidate(kind string) {
	switch kind {
	case "host", "srflx", "prflx", "relay":
	default:
		kind = "unknown"
	}
	m.mu.Lock()
	if m.candidates == nil {
		m.candidates = make(map[string]int64)
	}
	m.candidates[kind]++
	m.mu.Unlock()
}

// RecordHandshakeLatency accumulates a completed handshake's duration, tracking
// the running total, maximum, and sample count.
func (m *Metrics) RecordHandshakeLatency(d time.Duration) {
	m.mu.Lock()
	m.handshakeLatencyTotal += d
	if d > m.handshakeLatencyMax {
		m.handshakeLatencyMax = d
	}
	m.handshakeCount++
	m.mu.Unlock()
}

// ConnectionsAttempted returns the total number of handshake attempts.
func (m *Metrics) ConnectionsAttempted() int64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.connectionsAttempted
}

// ConnectionsSucceeded returns the total number of successful handshakes.
func (m *Metrics) ConnectionsSucceeded() int64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.connectionsSucceeded
}

// ConnectionsFailed returns the total number of failed handshakes.
func (m *Metrics) ConnectionsFailed() int64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.connectionsFailed
}

// Failures returns the total number of recorded failures.
func (m *Metrics) Failures() int64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.failures
}

// Candidates returns a copy of the candidate-type distribution.
func (m *Metrics) Candidates() map[string]int64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make(map[string]int64, len(m.candidates))
	for k, v := range m.candidates {
		out[k] = v
	}
	return out
}

// HandshakeCount returns the number of handshakes whose latency was recorded.
func (m *Metrics) HandshakeCount() int64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.handshakeCount
}

// HandshakeLatencyTotal returns the cumulative recorded handshake duration.
func (m *Metrics) HandshakeLatencyTotal() time.Duration {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.handshakeLatencyTotal
}

// HandshakeLatencyMax returns the longest recorded handshake duration.
func (m *Metrics) HandshakeLatencyMax() time.Duration {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.handshakeLatencyMax
}

// HandshakeLatencyAvg returns the mean recorded handshake duration, or zero
// when no handshake has been recorded.
func (m *Metrics) HandshakeLatencyAvg() time.Duration {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.handshakeCount == 0 {
		return 0
	}
	return m.handshakeLatencyTotal / time.Duration(m.handshakeCount)
}

// Snapshot is an immutable point-in-time copy of a Metrics.
type Snapshot struct {
	ConnectionsAttempted  int64
	ConnectionsSucceeded  int64
	ConnectionsFailed     int64
	Failures              int64
	Candidates            map[string]int64
	HandshakeCount        int64
	HandshakeLatencyTotal time.Duration
	HandshakeLatencyMax   time.Duration
	HandshakeLatencyAvg   time.Duration
}

// Snapshot returns a copy of the current metrics.
func (m *Metrics) Snapshot() Snapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()
	cands := make(map[string]int64, len(m.candidates))
	for k, v := range m.candidates {
		cands[k] = v
	}
	avg := m.handshakeLatencyTotal
	if m.handshakeCount > 0 {
		avg = m.handshakeLatencyTotal / time.Duration(m.handshakeCount)
	}
	return Snapshot{
		ConnectionsAttempted:  m.connectionsAttempted,
		ConnectionsSucceeded:  m.connectionsSucceeded,
		ConnectionsFailed:     m.connectionsFailed,
		Failures:              m.failures,
		Candidates:            cands,
		HandshakeCount:        m.handshakeCount,
		HandshakeLatencyTotal: m.handshakeLatencyTotal,
		HandshakeLatencyMax:   m.handshakeLatencyMax,
		HandshakeLatencyAvg:   avg,
	}
}

// Text renders the metrics in Prometheus text exposition format so they can be
// scraped by a monitoring backend.
func (m *Metrics) Text() string {
	snap := m.Snapshot()
	var b strings.Builder

	counter := func(name, help string, v float64) {
		fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s counter\n%s %v\n", name, help, name, name, v)
	}
	gauge := func(name, help string, v float64) {
		fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s gauge\n%s %v\n", name, help, name, name, v)
	}

	counter("wingman_connections_attempted", "Total connection handshake attempts.", float64(snap.ConnectionsAttempted))
	counter("wingman_connections_succeeded", "Total successful connection handshakes.", float64(snap.ConnectionsSucceeded))
	counter("wingman_connections_failed", "Total failed connection handshakes.", float64(snap.ConnectionsFailed))
	counter("wingman_failures", "Total recorded failures.", float64(snap.Failures))

	for _, kind := range sortedKeys(snap.Candidates) {
		counter(fmt.Sprintf("wingman_candidates_%s", kind),
			fmt.Sprintf("Number of %s ICE candidates gathered.", kind), float64(snap.Candidates[kind]))
	}

	if snap.HandshakeCount > 0 {
		// A cumulative latency is a counter by the Prometheus data model, so it
		// must be typed counter (the _total suffix is reserved for counters),
		// not gauge.
		counter("wingman_handshake_latency_total_seconds", "Cumulative handshake latency in seconds.",
			nanosToSeconds(snap.HandshakeLatencyTotal))
		gauge("wingman_handshake_latency_max_seconds", "Longest handshake latency in seconds.",
			nanosToSeconds(snap.HandshakeLatencyMax))
		gauge("wingman_handshake_latency_avg_seconds", "Average handshake latency in seconds.",
			nanosToSeconds(snap.HandshakeLatencyAvg))
		counter("wingman_handshake_count", "Number of handshakes whose latency was recorded.", float64(snap.HandshakeCount))
	}

	return b.String()
}

// PrometheusContentType is the media type Prometheus expects for the text
// exposition format. See the Prometheus documentation for the exact value.
const PrometheusContentType = "text/plain; version=0.0.4; charset=utf-8"

// Handler returns an http.Handler that renders the current metrics in the
// Prometheus text exposition format at the /metrics path. It is safe for
// concurrent use; each request takes a read lock via Snapshot.
type metricsHandler struct {
	m *Metrics
}

// MetricsHandler returns an http.Handler exposing m at /metrics. Non-GET
// requests receive 405 so the endpoint cannot be used to mutate state.
func (m *Metrics) MetricsHandler() http.Handler {
	return metricsHandler{m: m}
}

func (h metricsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/metrics" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", PrometheusContentType)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(h.m.Text()))
}

// sortedKeys returns the map keys in ascending order for stable output.
func sortedKeys(m map[string]int64) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// nanosToSeconds renders a duration in seconds with microsecond precision.
func nanosToSeconds(d time.Duration) float64 {
	return float64(d.Nanoseconds()) / float64(time.Second)
}
