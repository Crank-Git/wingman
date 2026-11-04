//go:build namespace

package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

func main() {
	if len(os.Args) < 2 {
		fail("usage: readiness cert|status|roundtrip ...")
	}
	switch os.Args[1] {
	case "cert":
		if len(os.Args) != 4 {
			fail("usage: readiness cert CERT KEY")
		}
		key, err := rsa.GenerateKey(rand.Reader, 2048)
		check(err)
		now := time.Now()
		template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "namespace fixture"}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IPAddresses: []net.IP{net.ParseIP("10.203.0.1")}}
		der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
		check(err)
		cert, err := os.Create(os.Args[2])
		check(err)
		check(pem.Encode(cert, &pem.Block{Type: "CERTIFICATE", Bytes: der}))
		check(cert.Close())
		private, err := os.Create(os.Args[3])
		check(err)
		check(pem.Encode(private, &pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
		check(private.Close())
	case "status":
		if len(os.Args) != 6 {
			fail("usage: readiness status URL CERT PATH CODE")
		}
		client := client(os.Args[3])
		response, err := client.Get(os.Args[2] + os.Args[4])
		check(err)
		defer response.Body.Close()
		if response.StatusCode != mustInt(os.Args[5]) {
			fail(fmt.Sprintf("%s returned %d", os.Args[4], response.StatusCode))
		}
	case "roundtrip":
		if len(os.Args) != 5 {
			fail("usage: readiness roundtrip URL CERT SECRET")
		}
		client := client(os.Args[3])
		endpoint := os.Args[2] + "/signaling?room=namespace&kind=offer"
		request, err := http.NewRequest(http.MethodPost, endpoint, strings.NewReader("namespace-message"))
		check(err)
		request.Header.Set("X-Wingman-Secret", os.Args[4])
		response, err := client.Do(request)
		check(err)
		response.Body.Close()
		if response.StatusCode != http.StatusOK {
			fail(fmt.Sprintf("signaling write returned %d", response.StatusCode))
		}
		request, err = http.NewRequest(http.MethodGet, endpoint, nil)
		check(err)
		request.Header.Set("X-Wingman-Secret", os.Args[4])
		response, err = client.Do(request)
		check(err)
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		check(err)
		if response.StatusCode != http.StatusOK || string(body) != "namespace-message" {
			fail(fmt.Sprintf("signaling read returned %d and %q", response.StatusCode, body))
		}
	default:
		fail("unknown readiness command")
	}
}

func client(path string) *http.Client {
	data, err := os.ReadFile(path)
	check(err)
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(data) {
		fail("certificate trust setup failed")
	}
	return &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}, Timeout: 2 * time.Second}
}
func mustInt(value string) int { var n int; _, err := fmt.Sscan(value, &n); check(err); return n }
func check(err error) {
	if err != nil {
		fail(err.Error())
	}
}
func fail(message string) { fmt.Fprintln(os.Stderr, message); os.Exit(1) }
