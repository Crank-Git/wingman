package dns

import (
	"fmt"
	"net"
	"strconv"
	"strings"
)

func (s *Server) validateConfig() error {
	if s.Zone != "" && !validName(strings.ToLower(strings.TrimSuffix(s.Zone, "."))) {
		return fmt.Errorf("DNS zone: invalid domain name")
	}
	if s.ListenAddr == "" {
		return nil
	}
	host, port, err := net.SplitHostPort(s.ListenAddr)
	if err != nil {
		return fmt.Errorf("DNS listen address: %w", err)
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("DNS listen address: use a literal IP address")
	}
	n, err := strconv.Atoi(port)
	if err != nil {
		return fmt.Errorf("DNS listen port: %w", err)
	}
	if n < 0 || n > 65535 {
		return fmt.Errorf("DNS listen address: invalid port")
	}
	if !ip.IsLoopback() && s.TSIGKey == nil {
		return fmt.Errorf("DNS listen address: remote binds require TSIG authentication")
	}
	return nil
}

func (s *Server) servesZone(zone string) bool {
	return s.Zone == "" || zone == strings.ToLower(strings.TrimSuffix(s.Zone, "."))
}
