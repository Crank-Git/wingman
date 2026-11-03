package sig

import "errors"

// ErrNotFound is returned by a Transport's Poll when the requested message has
// not been published yet. Callers typically retry after a short delay.
var ErrNotFound = errors.New("sig: message not found")
