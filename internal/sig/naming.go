package sig

import (
	"fmt"
	"strconv"
	"strings"
)

// Kind identifies the direction of an SDP exchange.
type Kind int

const (
	// Offer is a session offer published by peer A.
	Offer Kind = iota
	// Answer is a session answer published by peer B.
	Answer
)

// String returns the DNS label used for k ("offer" or "answer").
func (k Kind) String() string {
	switch k {
	case Offer:
		return "offer"
	case Answer:
		return "answer"
	}
	return ""
}

// ParseKind parses the DNS label for a Kind, returned by String.
func ParseKind(s string) (Kind, error) {
	switch s {
	case "offer":
		return Offer, nil
	case "answer":
		return Answer, nil
	default:
		return Offer, fmt.Errorf("sig: unknown kind %q", s)
	}
}

// sigLabel marks the signaling section of a rendezvous name. It sits between
// the kind and the zone, e.g. "...offer.sig.example".
const sigLabel = "sig"

// SigLabel is the exported signaling-section marker used in rendezvous names.
const SigLabel = sigLabel

// RendezvousName builds the canonical DNS name for a fragment:
//
//	<room>.<seq>-<total>.<payload>.<kind>.sig.<zone>
//
// payload must already be a single DNS-safe label (see EncodeLabel).
func RendezvousName(zone, room string, k Kind, seq, total uint16, payload string) string {
	return strings.Join([]string{
		room,
		strconv.Itoa(int(seq)) + "-" + strconv.Itoa(int(total)),
		payload,
		k.String(),
		sigLabel,
		zone,
	}, ".")
}

// PollName builds the stable name peers query to retrieve the current set of
// fragments for a room. It has fewer labels than a fragment-write name because
// it omits the seq/total and payload labels:
//
//	<room>.<kind>.sig.<zone>
func PollName(zone, room string, k Kind) string {
	return strings.Join([]string{room, k.String(), sigLabel, zone}, ".")
}

// ParsedName is the decoded form of a rendezvous name.
type ParsedName struct {
	Zone    string
	Room    string
	Kind    Kind
	Seq     uint16
	Total   uint16
	Payload string
}

// ParseRendezvousName parses a name produced by RendezvousName.
func ParseRendezvousName(name string) (ParsedName, error) {
	name = strings.TrimSuffix(strings.TrimSpace(name), ".")
	fields := strings.Split(name, ".")
	if len(fields) < 6 {
		return ParsedName{}, fmt.Errorf("sig: malformed rendezvous name %q", name)
	}

	room := fields[0]
	seqTotal := fields[1]
	payload := fields[2]
	kindStr := fields[3]
	if fields[4] != sigLabel {
		return ParsedName{}, fmt.Errorf("sig: %q is not a signaling name (missing %q label)", name, sigLabel)
	}
	zone := strings.Join(fields[5:], ".")

	kind, err := ParseKind(kindStr)
	if err != nil {
		return ParsedName{}, err
	}

	st := strings.SplitN(seqTotal, "-", 2)
	if len(st) != 2 {
		return ParsedName{}, fmt.Errorf("sig: malformed sequence label %q", seqTotal)
	}
	seq, err := strconv.ParseUint(st[0], 10, 16)
	if err != nil {
		return ParsedName{}, fmt.Errorf("sig: bad sequence in %q: %w", seqTotal, err)
	}
	total, err := strconv.ParseUint(st[1], 10, 16)
	if err != nil {
		return ParsedName{}, fmt.Errorf("sig: bad total in %q: %w", seqTotal, err)
	}
	if seq >= total {
		return ParsedName{}, fmt.Errorf("sig: sequence %d not less than total %d", seq, total)
	}

	return ParsedName{
		Zone:    zone,
		Room:    room,
		Kind:    kind,
		Seq:     uint16(seq),
		Total:   uint16(total),
		Payload: payload,
	}, nil
}
