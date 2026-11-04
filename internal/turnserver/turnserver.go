// Package turnserver runs a self-hosted TURN/STUN server so wingman peers can
// obtain srflx (STUN) and relay (TURN) ICE candidates without depending on an
// external server. It wraps pion/turn/v5.
//
// A credentialless server answers STUN binding requests (yielding srflx
// candidates) but runs in STUN-only mode. Supplying a username and password
// enables TURN relay allocations (yielding relay candidates) with short-term
// credentials, matching the credentials peers present via --user/--pass.
package turnserver

import (
	"fmt"
	"net"
	"strings"

	"github.com/pion/turn/v5"
)

// defaultRealm is the TURN realm advertised to peers when none is configured.
const defaultRealm = "wingman"

// Config configures a self-hosted TURN/STUN server.
type Config struct {
	// Addr is the UDP listen address, e.g. "127.0.0.1:3478" or "0.0.0.0:3478".
	Addr string

	// Realm is the TURN realm advertised to peers. Defaults to "wingman".
	Realm string

	// Username and Password enable TURN relay with short-term credentials.
	// Both must be set together; when empty the server runs STUN-only.
	Username string
	Password string

	// ExternalAddr is the IP peers are told to dial for srflx/relay
	// candidates. It overrides the listen host in the advertised address and is
	// required when Addr binds a wildcard interface (0.0.0.0 or ::).
	ExternalAddr string
}

// Server is a running self-hosted TURN/STUN server.
type Server struct {
	srv         *turn.Server
	boundAddr   string // actual bound address (host:port, port may be OS-assigned)
	advertised  string // advertised host peers dial (ExternalAddr or the listen host)
	realm       string
	turnEnabled bool
}

// New creates and starts a TURN/STUN server. The UDP socket is bound
// immediately; on any error the socket is released and the error is returned.
func New(cfg Config) (*Server, error) {
	realm := strings.TrimSpace(cfg.Realm)
	if realm == "" {
		realm = defaultRealm
	}
	if strings.TrimSpace(cfg.Addr) == "" {
		return nil, fmt.Errorf("turnserver: listen address is required")
	}
	if (cfg.Username == "") != (cfg.Password == "") {
		return nil, fmt.Errorf("turnserver: --turn-user and --turn-pass must be set together")
	}
	// Validate ExternalAddr before binding so an invalid value fails fast
	// without touching a socket.
	if ext := strings.TrimSpace(cfg.ExternalAddr); ext != "" && net.ParseIP(ext) == nil {
		return nil, fmt.Errorf("turnserver: --turn-external %q is not an IP address", ext)
	}

	udp, err := net.ListenPacket("udp4", cfg.Addr)
	if err != nil {
		return nil, fmt.Errorf("turnserver: bind %q: %w", cfg.Addr, err)
	}

	boundHost, _, err := net.SplitHostPort(udp.LocalAddr().String())
	if err != nil {
		_ = udp.Close()
		return nil, fmt.Errorf("turnserver: %w", err)
	}

	advertised, err := advertisedHost(cfg, boundHost)
	if err != nil {
		_ = udp.Close()
		return nil, err
	}

	relayGen := &turn.RelayAddressGeneratorStatic{
		// RelayAddress is what peers see; Address is where the relay socket
		// binds. They differ only when the server sits behind a public IP.
		RelayAddress: net.ParseIP(advertised),
		Address:      boundHost,
	}

	var authHandler turn.AuthHandler
	if cfg.Username != "" {
		user, pass := cfg.Username, cfg.Password
		authHandler = func(ra *turn.RequestAttributes) (userID string, key []byte, ok bool) {
			if ra.Username == user {
				return ra.Username, turn.GenerateAuthKey(ra.Username, ra.Realm, pass), true
			}
			return "", nil, false
		}
	}

	srv, err := turn.NewServer(turn.ServerConfig{
		Realm:             realm,
		AuthHandler:       authHandler,
		PacketConnConfigs: []turn.PacketConnConfig{{PacketConn: udp, RelayAddressGenerator: relayGen}},
	})
	if err != nil {
		_ = udp.Close()
		return nil, fmt.Errorf("turnserver: %w", err)
	}

	return &Server{
		srv:         srv,
		boundAddr:   udp.LocalAddr().String(),
		advertised:  advertised,
		realm:       realm,
		turnEnabled: cfg.Username != "",
	}, nil
}

// advertisedHost resolves the address peers should dial. It uses ExternalAddr
// when set, otherwise the concrete listen host. A wildcard listen host without
// an explicit external address is rejected: advertising 0.0.0.0 is useless.
func advertisedHost(cfg Config, boundHost string) (string, error) {
	// ExternalAddr is validated in New before the socket is bound, so a
	// non-empty value here is already a well-formed IP.
	if ext := strings.TrimSpace(cfg.ExternalAddr); ext != "" {
		return ext, nil
	}
	if net.ParseIP(boundHost) == nil || net.ParseIP(boundHost).IsUnspecified() {
		return "", fmt.Errorf("turnserver: listen address binds a wildcard interface; set --turn-external to the public IP")
	}
	return boundHost, nil
}

// Addr returns the actual bound address, e.g. "127.0.0.1:54321". The port may
// differ from Config.Addr when the caller asked for port 0.
func (s *Server) Addr() string { return s.boundAddr }

// port returns the bound port as a string. boundAddr is always a valid
// "host:port" (set from the bound socket's LocalAddr), so SplitHostPort
// cannot fail here.
func (s *Server) port() string {
	_, port, _ := net.SplitHostPort(s.boundAddr)
	return port
}

// hostPort returns "host:port" using the advertised host and bound port.
func (s *Server) hostPort() string {
	return net.JoinHostPort(s.advertised, s.port())
}

// STUNURI returns the STUN URI peers should use, e.g. "stun:127.0.0.1:3478".
func (s *Server) STUNURI() string { return "stun:" + s.hostPort() }

// TURNURI returns the TURN URI peers should use, e.g. "turn:127.0.0.1:3478".
// It is empty when the server runs in STUN-only mode (no credentials).
func (s *Server) TURNURI() string {
	if !s.TURNEnabled() {
		return ""
	}
	return "turn:" + s.hostPort()
}

// TURNEnabled reports whether TURN relay is active (credentials configured).
func (s *Server) TURNEnabled() bool { return s.turnEnabled }

// Realm returns the configured TURN realm.
func (s *Server) Realm() string { return s.realm }

// AllocationCount returns the number of active TURN relay allocations.
func (s *Server) AllocationCount() int { return s.srv.AllocationCount() }

// Close stops the server and releases its socket.
func (s *Server) Close() error {
	return s.srv.Close()
}
