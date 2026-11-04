package main

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

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
			blocked, err := net.ListenPacket("udp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer blocked.Close()
			tcp, err := net.Listen("tcp", blocked.LocalAddr().String())
			if err != nil {
				t.Fatal(err)
			}
			defer tcp.Close()
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
			args = append(args, flag, tcp.Addr().String())
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if err := runServe(ctx, args); err == nil || !strings.Contains(err.Error(), "address already in use") {
				t.Fatalf("bind cause: %v", err)
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
