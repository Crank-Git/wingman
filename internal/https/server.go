package https

import (
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"
	"unicode"

	"github.com/Crank-Git/wingman/internal/credentials"
)

// ServerConfig supplies the optional TLS listener and its credentials.
type ServerConfig struct {
	ListenAddr string
	CertFile   string
	KeyFile    string
	SecretFile string
	LocalDemo  bool
}

// LoadSecret returns a token from one source without exposing its value in errors.
// A file uses the shared permission and size restrictions.
func LoadSecret(direct, path string) (string, error) {
	if direct != "" && path != "" {
		return "", errors.New("https: conflicting signaling secret sources")
	}
	if path == "" {
		return direct, nil
	}
	data, err := credentials.ReadRestrictedFile(path)
	if err != nil {
		return "", fmt.Errorf("https: load signaling secret: %w", err)
	}
	token := strings.TrimSpace(string(data))
	if token == "" || strings.ContainsFunc(token, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
		return "", errors.New("https: signaling secret file requires one nonempty token without control characters")
	}
	return token, nil
}

// BindTLS returns a configured server and a bound listener without starting service.
// An empty listen address disables TLS. The caller owns both returned resources.
func BindTLS(cfg ServerConfig) (*http.Server, net.Listener, error) {
	if cfg.ListenAddr == "" {
		return nil, nil, nil
	}
	if cfg.CertFile == "" || cfg.KeyFile == "" {
		return nil, nil, errors.New("https: --tls-cert and --tls-key are required")
	}
	secret, err := LoadSecret("", cfg.SecretFile)
	if err != nil {
		return nil, nil, err
	}
	if cfg.LocalDemo {
		host, _, err := net.SplitHostPort(cfg.ListenAddr)
		if err != nil {
			return nil, nil, fmt.Errorf("https: local demo listen address: %w", err)
		}
		if !net.ParseIP(host).IsLoopback() {
			return nil, nil, errors.New("https: local demo requires a loopback IP listen address")
		}
	} else if secret == "" {
		return nil, nil, errors.New("https: requires --signaling-secret-file or explicit --local-demo")
	}
	certificate, err := tls.LoadX509KeyPair(cfg.CertFile, cfg.KeyFile)
	if err != nil {
		return nil, nil, fmt.Errorf("https: load TLS certificate: %w", err)
	}
	listener, err := net.Listen("tcp", cfg.ListenAddr)
	if err != nil {
		return nil, nil, fmt.Errorf("https: bind TLS listener: %w", err)
	}
	handler := NewHandler()
	handler.Secret = secret
	return &http.Server{
		Handler:           handler,
		TLSConfig:         &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12},
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second,
		WriteTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second,
	}, listener, nil
}
