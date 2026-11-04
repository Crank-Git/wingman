package metrics

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestCountersIncrement(t *testing.T) {
	m := New()
	m.ConnectionAttempted()
	m.ConnectionAttempted()
	m.ConnectionSucceeded()
	m.ConnectionFailed()

	if got := m.ConnectionsAttempted(); got != 2 {
		t.Errorf("ConnectionsAttempted = %d, want 2", got)
	}
	if got := m.ConnectionsSucceeded(); got != 1 {
		t.Errorf("ConnectionsSucceeded = %d, want 1", got)
	}
	if got := m.ConnectionsFailed(); got != 1 {
		t.Errorf("ConnectionsFailed = %d, want 1", got)
	}
}

func TestCandidateDistribution(t *testing.T) {
	m := New()
	for i := 0; i < 3; i++ {
		m.RecordCandidate("host")
	}
	m.RecordCandidate("srflx")
	m.RecordCandidate("relay")

	got := m.Candidates()
	if got["host"] != 3 || got["srflx"] != 1 || got["relay"] != 1 {
		t.Errorf("Candidates = %v, want host:3 srflx:1 relay:1", got)
	}
}

func TestHandshakeLatencyAggregates(t *testing.T) {
	m := New()
	m.RecordHandshakeLatency(100 * time.Millisecond)
	m.RecordHandshakeLatency(300 * time.Millisecond)
	m.RecordHandshakeLatency(200 * time.Millisecond)

	if got := m.HandshakeCount(); got != 3 {
		t.Errorf("HandshakeCount = %d, want 3", got)
	}
	if got := m.HandshakeLatencyTotal(); got != 600*time.Millisecond {
		t.Errorf("HandshakeLatencyTotal = %v, want 600ms", got)
	}
	if got := m.HandshakeLatencyMax(); got != 300*time.Millisecond {
		t.Errorf("HandshakeLatencyMax = %v, want 300ms", got)
	}
	// Average is exposed so callers need not recompute it.
	if got := m.HandshakeLatencyAvg(); got != 200*time.Millisecond {
		t.Errorf("HandshakeLatencyAvg = %v, want 200ms", got)
	}
}

func TestHandshakeLatencyEmpty(t *testing.T) {
	m := New()
	if got := m.HandshakeLatencyAvg(); got != 0 {
		t.Errorf("avg on empty metrics = %v, want 0", got)
	}
	if got := m.HandshakeLatencyMax(); got != 0 {
		t.Errorf("max on empty metrics = %v, want 0", got)
	}
}

func TestFailureCounter(t *testing.T) {
	m := New()
	m.Failure()
	m.Failure()
	if got := m.Failures(); got != 2 {
		t.Errorf("Failures = %d, want 2", got)
	}
}

func TestSnapshotIsImmutableCopy(t *testing.T) {
	m := New()
	m.ConnectionAttempted()
	m.RecordCandidate("host")
	snap := m.Snapshot()

	m.ConnectionAttempted()
	m.RecordCandidate("host")

	if got := snap.ConnectionsAttempted; got != 1 {
		t.Errorf("snapshot ConnectionsAttempted = %d, want 1 (snapshot must be a copy)", got)
	}
	if got := snap.Candidates["host"]; got != 1 {
		t.Errorf("snapshot Candidates[host] = %d, want 1", got)
	}
}

// TestConcurrentAccess guards against data races under -race.
func TestConcurrentAccess(t *testing.T) {
	m := New()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m.ConnectionAttempted()
			m.RecordCandidate("srflx")
			m.RecordHandshakeLatency(time.Millisecond)
			_ = m.Text()
		}()
	}
	wg.Wait()
	if got := m.ConnectionsAttempted(); got != 50 {
		t.Errorf("ConnectionsAttempted = %d, want 50", got)
	}
}

func TestTextRendered(t *testing.T) {
	m := New()
	m.ConnectionAttempted()
	m.ConnectionSucceeded()
	m.RecordCandidate("host")
	m.RecordHandshakeLatency(12 * time.Millisecond)

	out := m.Text()
	if !strings.Contains(out, "wingman_connections_attempted 1") {
		t.Errorf("Text missing attempted counter:\n%s", out)
	}
	if !strings.Contains(out, "wingman_connections_succeeded 1") {
		t.Errorf("Text missing succeeded counter:\n%s", out)
	}
	if !strings.Contains(out, "wingman_candidates_host 1") {
		t.Errorf("Text missing candidate counter:\n%s", out)
	}
	// The HELP/TYPE annotations must be present for Prometheus scraping.
	if !strings.Contains(out, "# HELP") || !strings.Contains(out, "# TYPE") {
		t.Errorf("Text missing Prometheus annotations:\n%s", out)
	}
}

// TestHandshakeLatencyTotalIsCounter ensures the cumulative handshake latency is
// typed as a counter, not a gauge. The Prometheus data model reserves the
// _total suffix for counters, so emitting it as a gauge violates the format.
func TestHandshakeLatencyTotalIsCounter(t *testing.T) {
	m := New()
	m.RecordHandshakeLatency(12 * time.Millisecond)

	out := m.Text()
	if !strings.Contains(out, "# TYPE wingman_handshake_latency_total_seconds counter") {
		t.Fatalf("handshake latency total must be a counter:\n%s", out)
	}
	if strings.Contains(out, "# TYPE wingman_handshake_latency_total_seconds gauge") {
		t.Fatalf("handshake latency total must not be a gauge:\n%s", out)
	}
}

// TestTextFormatIsValid exercises the numeric rendering path.
func TestTextFormatIsValid(t *testing.T) {
	m := New()
	m.ConnectionAttempted()
	out := m.Text()
	// A well-formed Prometheus line is: name value
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 {
			t.Errorf("malformed metric line %q", line)
			continue
		}
		if _, err := strconv.ParseFloat(fields[1], 64); err != nil {
			t.Errorf("metric value %q not numeric: %v", fields[1], err)
		}
	}
}

// TestMetricsHandlerSweep verifies the /metrics scrape endpoint renders the
// Prometheus text format, sets the correct content type, rejects non-GET, and
// 404s off-path.
func TestMetricsHandlerSweep(t *testing.T) {
	m := New()
	m.ConnectionAttempted()
	m.ConnectionSucceeded()
	m.RecordCandidate("host")

	srv := httptest.NewServer(m.MetricsHandler())
	defer srv.Close()

	// GET /metrics: 200, correct content type, expected lines present.
	resp, err := http.Get(srv.URL + "/metrics")
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /metrics status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != PrometheusContentType {
		t.Errorf("Content-Type = %q, want %q", ct, PrometheusContentType)
	}
	body, _ := io.ReadAll(resp.Body)
	for _, want := range []string{
		"# TYPE wingman_connections_attempted counter",
		"wingman_connections_attempted 1",
		"wingman_candidates_host 1",
	} {
		if !strings.Contains(string(body), want) {
			t.Errorf("metrics body missing %q\n%s", want, body)
		}
	}

	// POST /metrics: 405.
	postResp, err := http.Post(srv.URL+"/metrics", "text/plain", nil)
	if err != nil {
		t.Fatalf("POST /metrics: %v", err)
	}
	postResp.Body.Close()
	if postResp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("POST /metrics status = %d, want 405", postResp.StatusCode)
	}

	// Off-path: 404.
	other, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	other.Body.Close()
	if other.StatusCode != http.StatusNotFound {
		t.Errorf("GET / status = %d, want 404", other.StatusCode)
	}
}
