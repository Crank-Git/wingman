package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func checkAuthenticatedOperationsCLI(t *testing.T, bin string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	key := cleanupCLIKey(t)
	server := exec.CommandContext(ctx, bin, "serve", "--listen", "127.0.0.1:0", "--zone", "example.test", "--dns-tsig-key-file", key, "--metrics-addr", "127.0.0.1:0")
	stdout, err := server.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	server.Stderr = &stderr
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := server.Process.Signal(os.Interrupt); err != nil {
			t.Error(err)
		}
		if err := server.Wait(); err != nil {
			t.Errorf("server exit: %v; %s", err, &stderr)
		}
	}()
	lines := make(chan string, 8)
	go func() {
		defer close(lines)
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			lines <- scanner.Text()
		}
	}()
	addr, base := operationsCLIAddresses(t, ctx, lines)
	client := &http.Client{Timeout: time.Second}
	defer client.CloseIdleConnections()
	scrape := func(path string) string {
		t.Helper()
		response, err := client.Get(base + path)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != http.StatusOK {
			t.Fatalf("%s: HTTP %d", path, response.StatusCode)
		}
		return string(body)
	}
	for _, path := range []string{"/healthz", "/readyz"} {
		if body := scrape(path); body != "" {
			t.Fatalf("%s: unexpected body %q", path, body)
		}
	}
	before := scrape("/metrics")
	probe := exec.CommandContext(ctx, bin, "doctor", "--signaling", "dns", "--addr", addr, "--zone", "example.test", "--dns-tsig-key-file", key, "--json")
	output, err := probe.Output()
	if err != nil {
		t.Fatalf("doctor: %v", err)
	}
	checkOperationsDoctorReport(t, output)
	after := scrape("/metrics")
	for _, operation := range []string{"write", "poll"} {
		prefix := `wingman_dns_requests_total{operation="` + operation + `",result="success"} `
		if !strings.Contains(before, prefix+"0\n") || !strings.Contains(after, prefix+"1\n") {
			t.Fatalf("%s counter does not change from 0 to 1: %s", operation, after)
		}
	}
}

func operationsCLIAddresses(t *testing.T, ctx context.Context, lines <-chan string) (string, string) {
	t.Helper()
	var addr, base string
	for addr == "" || base == "" {
		select {
		case line, ok := <-lines:
			if !ok {
				t.Fatal("server closes output before it reports both listeners")
			}
			if strings.HasPrefix(line, "wingman server listening on ") {
				addr = strings.Fields(line)[4]
			}
			if strings.HasPrefix(line, "metrics listening on ") {
				base = "http://" + strings.TrimSuffix(strings.TrimPrefix(line, "metrics listening on "), "/metrics")
			}
		case <-ctx.Done():
			t.Fatal("server does not report both listeners before the deadline")
		}
	}
	return addr, base
}

func checkOperationsDoctorReport(t *testing.T, output []byte) {
	t.Helper()
	var report struct {
		SchemaVersion int `json:"schema_version"`
		Transport     string
		Success       bool
		Checks        []doctorCheck
	}
	if err := json.Unmarshal(output, &report); err != nil {
		t.Fatal(err)
	}
	if report.SchemaVersion != 1 || report.Transport != "dns" || !report.Success || len(report.Checks) != 5 {
		t.Fatalf("doctor report: %s", output)
	}
	for i, name := range []string{"dns_udp", "dns_tcp", "auth", "publication", "retrieval"} {
		check := report.Checks[i]
		if check.Name != name || !check.Success || check.ErrorCode != "" {
			t.Fatalf("doctor check: %+v", check)
		}
	}
}
