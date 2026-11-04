package https

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Crank-Git/wingman/internal/sig"
)

func testCertificate(t *testing.T) (string, string, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "localhost"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	private, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certFile, keyFile := filepath.Join(t.TempDir(), "cert.pem"), filepath.Join(t.TempDir(), "key.pem")
	cert := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err := os.WriteFile(certFile, cert, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: private}), 0600); err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(cert)
	return certFile, keyFile, roots
}

func TestTLSListenerRoundtrip(t *testing.T) {
	cert, key, roots := testCertificate(t)
	secret := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(secret, []byte(" token\n"), 0600); err != nil {
		t.Fatal(err)
	}
	server, listener, err := BindTLS(ServerConfig{ListenAddr: "127.0.0.1:0", CertFile: cert, KeyFile: key, SecretFile: secret})
	if err != nil {
		t.Fatal(err)
	}
	if server.ReadHeaderTimeout != 5*time.Second || server.ReadTimeout != 15*time.Second || server.WriteTimeout != 15*time.Second || server.IdleTimeout != time.Minute || server.TLSConfig.MinVersion != tls.VersionTLS12 {
		t.Fatal("incorrect TLS server bounds")
	}
	done := make(chan error, 1)
	go func() { done <- server.ServeTLS(listener, "", "") }()
	defer func() {
		if err := server.Close(); err != nil {
			t.Error(err)
		}
		<-done
	}()
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots}}
	defer transport.CloseIdleConnections()
	client := &Transport{Client: &http.Client{Transport: transport, Timeout: time.Second}, BaseURL: "https://" + listener.Addr().String(), Secret: "token"}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for _, secret := range []string{"", "wrong"} {
		client.Secret = secret
		if err := client.Publish(ctx, "room", sig.Offer, []byte("offer")); err == nil {
			t.Fatal("unauthorized publish succeeds")
		}
		if _, err := client.Poll(ctx, "room", sig.Offer); err == nil {
			t.Fatal("unauthorized poll succeeds")
		}
	}
	client.Secret = "token"
	if err := client.Publish(ctx, "room", sig.Offer, []byte("offer")); err != nil {
		t.Fatal(err)
	}
	got, err := client.Poll(ctx, "room", sig.Offer)
	if err != nil || string(got) != "offer" {
		t.Fatalf("roundtrip: %q %v", got, err)
	}
}

func TestTLSListenerRejectsInvalidConfiguration(t *testing.T) {
	cert, key, _ := testCertificate(t)
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	for _, cfg := range []ServerConfig{
		{ListenAddr: "127.0.0.1:0"},
		{ListenAddr: "127.0.0.1:0", CertFile: cert},
		{ListenAddr: "127.0.0.1:0", KeyFile: key},
		{ListenAddr: "127.0.0.1:0", CertFile: key, KeyFile: cert, LocalDemo: true},
		{ListenAddr: "127.0.0.1:0", CertFile: cert, KeyFile: key},
		{ListenAddr: "0.0.0.0:0", CertFile: cert, KeyFile: key, LocalDemo: true},
		{ListenAddr: "localhost:0", CertFile: cert, KeyFile: key, LocalDemo: true},
		{ListenAddr: "bad", CertFile: cert, KeyFile: key, LocalDemo: true},
		{ListenAddr: occupied.Addr().String(), CertFile: cert, KeyFile: key, LocalDemo: true},
		{ListenAddr: "127.0.0.1:0", CertFile: cert, KeyFile: key, SecretFile: "missing"},
	} {
		if _, listener, err := BindTLS(cfg); err == nil {
			listener.Close()
			t.Fatalf("accepted %+v", cfg)
		}
	}
	server, listener, err := BindTLS(ServerConfig{CertFile: "unused"})
	if err != nil || server != nil || listener != nil {
		t.Fatal("disabled TLS binds")
	}
	server, listener, err = BindTLS(ServerConfig{ListenAddr: "127.0.0.1:0", CertFile: cert, KeyFile: key, LocalDemo: true})
	if err != nil {
		t.Fatal(err)
	}
	listener.Close()
	server.Close()
}

func TestLoadSecretValidatesRestrictedToken(t *testing.T) {
	for _, value := range []string{"", " \n", "two tokens", "a\nb", "a\x00b", "a\x7fb"} {
		path := filepath.Join(t.TempDir(), "secret")
		if err := os.WriteFile(path, []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadSecret("", path); err == nil || strings.Contains(err.Error(), "a\x00b") {
			t.Fatalf("invalid token: %v", err)
		}
	}
	if _, err := LoadSecret("direct", "file"); err == nil {
		t.Fatal("conflicting secrets succeed")
	}
	if got, err := LoadSecret("direct", ""); err != nil || got != "direct" {
		t.Fatalf("compatibility: %q %v", got, err)
	}
}
