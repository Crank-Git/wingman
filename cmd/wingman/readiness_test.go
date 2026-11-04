package main

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Crank-Git/wingman/internal/https"
)

type readinessOutput struct {
	lines   chan string
	release chan struct{}
}

func (o *readinessOutput) Write(p []byte) (int, error) {
	line := string(p)
	o.lines <- line
	if strings.HasPrefix(line, "metrics listening on ") {
		// Hold the lifecycle owner so cancellation cannot close the listeners yet.
		<-o.release
	}
	return len(p), nil
}

func TestServeEndpointsObserveCancellationBeforeClosure(t *testing.T) {
	cert, key, roots := cliCertificate(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out := &readinessOutput{lines: make(chan string, 8), release: make(chan struct{})}
	done := make(chan error, 1)
	go func() {
		done <- serveListeners(ctx, serveConfig{
			listen: "127.0.0.1:0", zone: "example.com",
			https:       https.ServerConfig{ListenAddr: "127.0.0.1:0", CertFile: cert, KeyFile: key, LocalDemo: true},
			metricsAddr: "127.0.0.1:0",
		}, out)
	}()
	defer func() {
		cancel()
		close(out.release)
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	var bases []string
	for len(bases) < 2 {
		select {
		case line := <-out.lines:
			if strings.HasPrefix(line, "HTTPS listening on ") {
				bases = append(bases, strings.TrimSpace(strings.TrimPrefix(line, "HTTPS listening on ")))
			}
			if strings.HasPrefix(line, "metrics listening on ") {
				address := strings.TrimSpace(strings.TrimPrefix(line, "metrics listening on "))
				bases = append(bases, "http://"+strings.TrimSuffix(address, "/metrics"))
			}
		case <-ctx.Done():
			t.Fatal("listener reports time out")
		}
	}
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: time.Second}
	check := func(path string, want int) {
		t.Helper()
		for _, base := range bases {
			if strings.HasSuffix(base, ":0") {
				t.Fatalf("unbound address: %s", base)
			}
			response, err := client.Get(base + path)
			if err != nil {
				t.Fatal(err)
			}
			response.Body.Close()
			if response.StatusCode != want {
				t.Fatalf("%s%s: got %d, want %d", base, path, response.StatusCode, want)
			}
		}
	}
	check("/healthz", 200)
	check("/readyz", 200)
	cancel()
	check("/healthz", 200)
	check("/readyz", 503)
}

func TestServeReturnsMetricsBindFailure(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := runServe(ctx, []string{"--local-demo", "--metrics-addr", listener.Addr().String()}); err == nil {
		t.Fatal("metrics bind failure succeeds")
	}
}

func TestReadinessTransitionsOnHTTPAndTLS(t *testing.T) {
	for _, secure := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		state := &readiness{ctx: ctx}
		handler := state.handler(http.NotFoundHandler())
		server := httptest.NewUnstartedServer(handler)
		if secure {
			server.StartTLS()
		} else {
			server.Start()
		}
		client := server.Client()
		if secure {
			client.Transport.(*http.Transport).TLSClientConfig.MinVersion = tls.VersionTLS12
		}
		check := func(path string, want int) {
			t.Helper()
			response, err := client.Get(server.URL + path)
			if err != nil {
				t.Fatal(err)
			}
			response.Body.Close()
			if response.StatusCode != want {
				t.Fatalf("%s: got %d, want %d", path, response.StatusCode, want)
			}
		}
		check("/healthz", 503)
		check("/readyz", 503)
		state.started.Store(true)
		state.ready.Store(true)
		check("/healthz", 200)
		check("/readyz", 200)
		cancel()
		check("/healthz", 200)
		check("/readyz", 503)
		state.ready.Store(false)
		check("/readyz", 503)
		server.Close()
	}
}

func TestServeRollsBackEachBindFailure(t *testing.T) {
	cert, key, _ := cliCertificate(t)
	for _, position := range []string{"dns", "https", "metrics", "turn"} {
		t.Run(position, func(t *testing.T) {
			var blockedAddr string
			// Independent TCP and UDP allocations can select the same port.
			// Reserve only the protocol that must fail at this stage.
			if position == "dns" || position == "turn" {
				blocked, err := net.ListenPacket("udp", "127.0.0.1:0")
				if err != nil {
					t.Fatal(err)
				}
				defer blocked.Close()
				blockedAddr = blocked.LocalAddr().String()
			} else {
				blocked, err := net.Listen("tcp", "127.0.0.1:0")
				if err != nil {
					t.Fatal(err)
				}
				defer blocked.Close()
				blockedAddr = blocked.Addr().String()
			}
			address := func() string {
				t.Helper()
				ln, err := net.Listen("tcp", "127.0.0.1:0")
				if err != nil {
					t.Fatal(err)
				}
				a := ln.Addr().String()
				ln.Close()
				return a
			}
			dnsAddr, tlsAddr, metricsAddr := address(), address(), address()
			args := []string{"--local-demo", "--listen", dnsAddr, "--https-listen", tlsAddr, "--tls-cert", cert, "--tls-key", key, "--metrics-addr", metricsAddr}
			flag := map[string]string{"dns": "--listen", "https": "--https-listen", "metrics": "--metrics-addr", "turn": "--turn-server"}[position]
			args = append(args, flag, blockedAddr)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			reader, writer, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer reader.Close()
			previous := os.Stdout
			os.Stdout = writer
			serveErr := runServe(ctx, args)
			os.Stdout = previous
			writer.Close()
			output, err := io.ReadAll(reader)
			if err != nil || len(output) != 0 {
				t.Fatalf("partial startup reports addresses: %q, %v", output, err)
			}
			if serveErr == nil || !strings.Contains(serveErr.Error(), "address already in use") {
				t.Fatalf("bind cause: %v", serveErr)
			}
			for _, a := range []string{dnsAddr, tlsAddr, metricsAddr} {
				ln, err := net.Listen("tcp", a)
				if err != nil {
					t.Fatalf("rollback %s: %v", a, err)
				}
				ln.Close()
			}
			udp, err := net.ListenPacket("udp", dnsAddr)
			if err != nil {
				t.Fatal(err)
			}
			udp.Close()
		})
	}
}

func TestDrainHTTPPreservesImmediateServeAndEarlierCauses(t *testing.T) {
	serveCause := errors.New("immediate serve failure")
	prior := errors.New("startup failure")
	served := make(chan error, 1)
	served <- serveCause
	err := drainHTTP(&http.Server{}, served, prior)
	if !errors.Is(err, serveCause) || !errors.Is(err, prior) {
		t.Fatalf("serve causes: %v", err)
	}
}
