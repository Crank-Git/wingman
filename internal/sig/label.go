package sig

import (
	"encoding/base32"
	"errors"
	"strings"
)

// dnsLabelMaxLen is the maximum length of a single DNS label in octets.
const dnsLabelMaxLen = 63

// lowercase is a DNS-safe base32 encoding using the lowercase alphabet
// (A-Z / 2-7). DNS labels are case-insensitive and restrict characters to
// letters, digits, and hyphens, so this alphabet is always valid inside a
// label.
var lowercase = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)

// EncodeLabel base32-encodes data so it can be carried inside a single DNS
// label. It returns an error if data is too large to fit in dnsLabelMaxLen
// characters.
func EncodeLabel(data []byte) (string, error) {
	if len(data) > maxBytesForLabel(dnsLabelMaxLen) {
		return "", errors.New("sig: fragment too large for a single DNS label")
	}
	return lowercase.EncodeToString(data), nil
}

// DecodeLabel base32-decodes a DNS-safe label produced by EncodeLabel.
func DecodeLabel(label string) ([]byte, error) {
	label = strings.TrimSpace(label)
	if label == "" {
		return nil, errors.New("sig: empty label")
	}
	out, err := lowercase.DecodeString(label)
	if err != nil {
		return nil, err
	}
	return out, nil
}
