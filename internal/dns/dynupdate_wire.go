package dns

import (
	"errors"
	"fmt"
	"strings"

	"github.com/Crank-Git/wingman/internal/sig"
	"github.com/miekg/dns"
)

func (t *DynUpdate) owner(room string, kind sig.Kind) (string, error) {
	if t == nil || t.Resolver == nil {
		return "", errors.New("DynUpdate: missing resolver configuration")
	}
	if err := t.key.validate(); err != nil {
		return "", fmt.Errorf("DynUpdate authentication: %w", err)
	}
	zone := canonicalReplyName(t.Zone)
	room = strings.ToLower(room)
	if !validName(zone) || strings.ContainsAny(t.Zone, " \t\r\n") || !validName(room) || strings.ContainsAny(room, ". \t\r\n") || kind.String() == "" {
		return "", errors.New("DynUpdate: invalid room, kind, or zone")
	}
	owner := sig.PollName(zone, room, kind)
	if !validName(owner) {
		return "", errors.New("DynUpdate: owner exceeds DNS name limit")
	}
	return dns.Fqdn(owner), nil
}

func (t *DynUpdate) checkWireSize(operation string, m *dns.Msg) error {
	copy := m.Copy()
	copy.SetTsig(t.key.Name, t.key.Algorithm, 300, 0)
	// HMAC-SHA256 always adds 32 MAC bytes, independent of the secret and timestamp.
	copy.IsTsig().MAC = strings.Repeat("00", 32)
	copy.IsTsig().MACSize = 32
	wire, err := copy.Pack()
	if err != nil {
		return fmt.Errorf("DynUpdate pack %s: %w", operation, err)
	}
	limit := defaultStorageLimits().responseBytes
	if len(wire) > dns.MaxMsgSize {
		return &DynUpdateSizeError{Operation: operation, Size: len(wire), Limit: dns.MaxMsgSize}
	}
	if len(wire) > limit {
		return &DynUpdateSizeError{Operation: operation, Size: len(wire), Limit: limit}
	}
	return nil
}
