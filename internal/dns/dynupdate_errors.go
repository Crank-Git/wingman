package dns

import "fmt"

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
