package dns

import (
	"errors"
	"fmt"

	"github.com/miekg/dns"
)

func dynUpdateExchangeError(operation string, err error) error {
	if errors.Is(err, dns.ErrAuth) {
		// ErrAuth can precede signature verification, so it does not identify the remote cause.
		return fmt.Errorf("DynUpdate %s exchange: %w; check the configured zone, authoritative endpoint, key identity/file, and server update policy", operation, err)
	}
	return fmt.Errorf("DynUpdate %s exchange: %w", operation, err)
}

// DynUpdateRcodeError identifies a rejected authenticated DNS operation.
type DynUpdateRcodeError struct {
	Operation string
	Rcode     int
}

func (e *DynUpdateRcodeError) Error() string {
	return fmt.Sprintf("DynUpdate %s: DNS rcode %d", e.Operation, e.Rcode)
}

// DynUpdateSizeError identifies a message that exceeds a byte limit.
type DynUpdateSizeError struct {
	Operation   string
	Size, Limit int
}

func (e *DynUpdateSizeError) Error() string {
	return fmt.Sprintf("DynUpdate %s: %d bytes exceed limit %d", e.Operation, e.Size, e.Limit)
}
