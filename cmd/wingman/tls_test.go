package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Crank-Git/wingman/internal/https"
	"github.com/Crank-Git/wingman/internal/sig"
)

func cliCertificate(t *testing.T) (string, string, *x509.CertPool) {
	t.Helper()
	fixture := httptest.NewTLSServer(http.NotFoundHandler())
	defer fixture.Close()
	certificate := fixture.TLS.Certificates[0]
	private, err := x509.MarshalPKCS8PrivateKey(certificate.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	cert, key := filepath.Join(t.TempDir(), "cert.pem"), filepath.Join(t.TempDir(), "key.pem")
	certBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate.Certificate[0]})
	if err := os.WriteFile(cert, certBytes, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(key, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: private}), 0600); err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(certBytes)
	return cert, key, roots
}

func TestServeTLSRoundtrip(t *testing.T) {
	cert, key, roots := cliCertificate(t)
	secretFile := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(secretFile, []byte("private-token\n"), 0600); err != nil {
		t.Fatal(err)
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	old := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = old }()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- runServe(ctx, []string{"--local-demo", "--https-listen", "127.0.0.1:0", "--tls-cert", cert, "--tls-key", key, "--signaling-secret-file", secretFile})
		w.Close()
	}()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	scanner := bufio.NewScanner(r)
	var base string
	for scanner.Scan() {
		line := scanner.Text()
		if strings.Contains(line, "private-token") {
			t.Fatal("secret appears in output")
		}
		if strings.HasPrefix(line, "HTTPS listening on ") {
			base = strings.TrimPrefix(line, "HTTPS listening on ")
			break
		}
	}
	if base == "" || strings.HasSuffix(base, ":0") {
		t.Fatalf("bound address: %q", base)
	}
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots}}
	defer transport.CloseIdleConnections()
	tr := &https.Transport{BaseURL: base, Client: &http.Client{Transport: transport, Timeout: time.Second}, Secret: "private-token"}
	if err := tr.Publish(ctx, "cli", sig.Offer, []byte("offer")); err != nil {
		t.Fatal(err)
	}
	got, err := tr.Poll(ctx, "cli", sig.Offer)
	if err != nil || string(got) != "offer" {
		t.Fatalf("roundtrip: %q %v", got, err)
	}
}

func TestServeTLSRejectsCertificateFailures(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, args := range [][]string{
		{"--https-listen", "127.0.0.1:0"},
		{"--https-listen", "127.0.0.1:0", "--tls-cert", "missing", "--tls-key", "missing"},
	} {
		if err := runServe(ctx, append([]string{"--local-demo"}, args...)); err == nil {
			t.Fatal("invalid TLS succeeds")
		}
	}
	// An absent listen flag keeps TLS disabled.
	if err := runServe(ctx, []string{"--local-demo", "--tls-cert", "unused"}); err != nil {
		t.Fatal(err)
	}
}

func TestPeerLoadsSignalingSecretFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(path, []byte(" token\n"), 0600); err != nil {
		t.Fatal(err)
	}
	opts, err := parsePeerFlags("offer", []string{"--room", "room", "--signaling", "https", "--signaling-url", "https://example.com", "--signaling-secret-file", path})
	if err != nil {
		t.Fatal(err)
	}
	tr, err := buildTransport(opts)
	if err != nil || tr.(*https.Transport).Secret != "token" {
		t.Fatalf("file token: %v", err)
	}
	opts.signalingSecret = "sensitive"
	if _, err := buildTransport(opts); err == nil || err.Error() != "https: conflicting signaling secret sources" {
		t.Fatalf("conflict: %v", err)
	}
	opts.signalingSecret = ""
	opts.signalingSecretFile = path + "missing"
	if _, err := buildTransport(opts); err == nil {
		t.Fatal("missing file succeeds")
	}
}
