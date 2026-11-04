package sig

import "errors"

// ErrNotFound is returned by a Transport's Poll when the requested message has
// not been published yet. Callers typically retry after a short delay.
var ErrNotFound = errors.New("sig: message not found")

// ErrIncomplete is returned by Reassemble when the received fragments form a
// contiguous prefix of the message but the final fragment -- the one carrying
// the CRC32 trailer -- has not yet arrived. It is a transient state during an
// in-flight publish; callers should keep polling until the message is complete.
var ErrIncomplete = errors.New("sig: incomplete message: final fragment not yet received")
