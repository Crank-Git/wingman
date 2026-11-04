// Package turn parses and validates TURN/STUN server configuration and turns
// it into the pion ICE server slice that the peer package wires into a
// PeerConnection. It performs no network I/O itself: contacting the servers is
// the job of the WebRTC engine once the ICE servers are configured.
package turn

import (
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/pion/webrtc/v4"
)

// defaultPort is the conventional port for STUN and TURN when a URI omits one.
const defaultPort = 3478

// validSchemes are the URI schemes this package understands. stun and turn are
// UDP/TCP by default; turns requires TLS.
var validSchemes = map[string]bool{
	"stun":  true,
	"turn":  true,
	"turns": true,
}

// Server is a single TURN/STUN server configuration: one normalized URI plus
// the optional long-term credentials used by TURN.
type Server struct {
	// URIs holds the normalized server URI(s), e.g. "turn:example.com:3478".
	URIs []string
	// Username is the TURN username (empty for STUN-only servers).
	Username string
	// Password is the TURN password, used as the ICE credential.
	Password string
}

// Parse parses and validates a single turn/turns/stun URI, normalizing it so
// it always carries an explicit port. The returned Server is ready to be added
// to a Config or passed to ICEservers.
func Parse(uri, username, password string) (*Server, error) {
	uri = strings.TrimSpace(uri)
	if uri == "" {
		return nil, fmt.Errorf("turn: empty URI")
	}
	scheme, rest, ok := strings.Cut(uri, ":")
	if !ok {
		return nil, fmt.Errorf("turn: %q: missing scheme (want scheme:host[:port])", uri)
	}
	scheme = strings.ToLower(scheme)
	if !validSchemes[scheme] {
		return nil, fmt.Errorf("turn: unsupported scheme %q in %q", scheme, uri)
	}

	host, portStr, hasPort := splitHostPort(rest)
	if host == "" {
		return nil, fmt.Errorf("turn: %q: missing host", uri)
	}
	if !validHost(host) {
		return nil, fmt.Errorf("turn: invalid host %q in %q", host, uri)
	}
	p := defaultPort
	if hasPort {
		var perr error
		if p, perr = strconv.Atoi(portStr); perr != nil || p < 1 || p > 65535 {
			return nil, fmt.Errorf("turn: %q: invalid port %q (want 1-65535)", uri, portStr)
		}
	}

	return &Server{
		URIs:     []string{fmt.Sprintf("%s:%s:%d", scheme, host, p)},
		Username: username,
		Password: password,
	}, nil
}

// Validate reports whether s is usable: it must expose at least one URI.
func (s *Server) Validate() error {
	if len(s.URIs) == 0 {
		return fmt.Errorf("turn: server has no URI")
	}
	for _, u := range s.URIs {
		if _, err := Parse(u, s.Username, s.Password); err != nil {
			return err
		}
	}
	return nil
}

// ICEServers builds the pion ICE server slice for this server. An empty server
// yields a nil slice so callers can append unconditionally.
func (s *Server) ICEServers() []webrtc.ICEServer {
	if len(s.URIs) == 0 {
		return nil
	}
	return []webrtc.ICEServer{{
		URLs:           s.URIs,
		Username:       s.Username,
		Credential:     s.Password,
		CredentialType: webrtc.ICECredentialTypePassword,
	}}
}

// Config is a collection of TURN/STUN servers.
type Config struct {
	Servers []Server
}

// Validate reports whether every server in the config is usable.
func (c *Config) Validate() error {
	if len(c.Servers) == 0 {
		return fmt.Errorf("turn: config has no servers")
	}
	for i := range c.Servers {
		if err := c.Servers[i].Validate(); err != nil {
			return fmt.Errorf("turn: server %d: %w", i, err)
		}
	}
	return nil
}

// ICEServers flattens every server into a single pion ICE server slice.
func (c *Config) ICEServers() []webrtc.ICEServer {
	var out []webrtc.ICEServer
	for i := range c.Servers {
		out = append(out, c.Servers[i].ICEServers()...)
	}
	return out
}

// ParseServers parses and validates a batch of raw URIs, one per Server. It is
// the entry point used by the CLI, where each --turn flag is a separate server.
func ParseServers(uris []string, username, password string) ([]Server, error) {
	out := make([]Server, 0, len(uris))
	for i, u := range uris {
		s, err := Parse(u, username, password)
		if err != nil {
			return nil, fmt.Errorf("turn: server %d: %w", i, err)
		}
		out = append(out, *s)
	}
	return out, nil
}

// splitHostPort splits "host:port" into its parts. hasPort reports whether a
// port was present at all (an omitted port falls back to defaultPort). IPv6
// hosts are wrapped in brackets.
func splitHostPort(rest string) (host, portStr string, hasPort bool) {
	if h, p, err := net.SplitHostPort(rest); err == nil {
		return h, p, true
	}
	return rest, "", false
}

// validHost reports whether h is a valid IPv4/IPv6 address or RFC 1123
// hostname.
func validHost(h string) bool {
	if h == "" || len(h) > 253 {
		return false
	}
	if net.ParseIP(h) != nil {
		return true
	}
	for _, label := range strings.Split(h, ".") {
		if len(label) == 0 || len(label) > 63 {
			return false
		}
		for i := 0; i < len(label); i++ {
			c := label[i]
			switch {
			case c >= 'a' && c <= 'z':
			case c >= '0' && c <= '9':
			case c == '-':
				if i == 0 || i == len(label)-1 {
					return false
				}
			default:
				return false
			}
		}
	}
	return true
}
