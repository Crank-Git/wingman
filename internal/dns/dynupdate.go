package dns

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/Crank-Git/wingman/internal/sig"
)

// DynUpdate holds the authenticated RFC 2136 transport configuration.
// Set its fields before concurrent use. Poll requires issue #43.
type DynUpdate struct {
	Resolver Resolver
	Zone     string
	key      TSIGKey
}

// NewDynUpdate returns a transport with a signed TCP resolver and a canonical zone.
// addr requires a host and numeric port. key requires a validated TSIG identity.
// Invalid configuration returns an error before any network request.
func NewDynUpdate(addr, zone string, key *TSIGKey) (*DynUpdate, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("DynUpdate address: %w", err)
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 || strings.TrimSpace(host) == "" || strings.TrimSpace(host) != host {
		return nil, errors.New("DynUpdate address requires a host and port from 1 to 65535")
	}
	zone = canonicalReplyName(zone)
	if !validName(zone) || strings.ContainsAny(zone, " \t\r\n") {
		return nil, errors.New("DynUpdate zone: invalid domain name")
	}
	if key == nil {
		return nil, errors.New("DynUpdate requires a TSIG key")
	}
	if err := key.validate(); err != nil {
		return nil, fmt.Errorf("DynUpdate authentication: %w", err)
	}
	transport := &DynUpdate{Zone: zone, key: *key}
	transport.Resolver = &Dialer{Addr: addr, Net: "tcp", Timeout: 5 * time.Second, TSIGKey: &transport.key}
	return transport, nil
}

// Poll returns an error until issue #43 supplies TXT reconstruction.
func (t *DynUpdate) Poll(context.Context, string, sig.Kind) ([]byte, error) {
	return nil, errors.New("DynUpdate Poll requires issue #43")
}
