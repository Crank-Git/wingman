// Package ice provides the ICE (RFC 8445) candidate model, local host-candidate
// gathering, and connection-state tracking used by the Wingman peer. It is a
// pure-Go layer: it does not perform connectivity checks itself (that is the job
// of the WebRTC engine wired in by the peer package), but it models candidates
// and ranks them consistently with that engine so diagnostics and local
// selection agree with ICE.
package ice

import (
	"fmt"
	"net"
	"strconv"
	"strings"
)

// CandidateType identifies how an ICE candidate was obtained.
type CandidateType byte

// CandidateType enum. The numeric order is not significant; Preference() carries
// the ordering weight.
const (
	CandidateTypeUnspecified CandidateType = iota
	CandidateTypeHost
	CandidateTypePeerReflexive
	CandidateTypeServerReflexive
	CandidateTypeRelay
)

// Preference returns the ICE type-preference weight for t. Higher values are
// more preferred, so host candidates (direct, no relay) win over server
// reflexive over peer reflexive over relayed candidates.
//
// The weights match pion/ice so that candidates ranked here order identically
// to the ones the WebRTC engine selects. See RFC 8445 §4.1.2.2.
func (t CandidateType) Preference() uint16 {
	switch t {
	case CandidateTypeHost:
		return 126
	case CandidateTypePeerReflexive:
		return 110
	case CandidateTypeServerReflexive:
		return 100
	default:
		// relay and unspecified both carry the lowest preference weight.
		return 0
	}
}

// String returns the canonical lowercase name for t (e.g. "host").
func (t CandidateType) String() string {
	switch t {
	case CandidateTypeHost:
		return "host"
	case CandidateTypePeerReflexive:
		return "prflx"
	case CandidateTypeServerReflexive:
		return "srflx"
	case CandidateTypeRelay:
		return "relay"
	default:
		return "unspecified"
	}
}

// ParseCandidateType parses the canonical name produced by String.
func ParseCandidateType(s string) (CandidateType, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "host":
		return CandidateTypeHost, nil
	case "prflx", "peer-reflexive", "peerreflexive":
		return CandidateTypePeerReflexive, nil
	case "srflx", "server-reflexive", "serverreflexive":
		return CandidateTypeServerReflexive, nil
	case "relay", "turn":
		return CandidateTypeRelay, nil
	case "unspecified":
		return CandidateTypeUnspecified, nil
	default:
		return CandidateTypeUnspecified, fmt.Errorf("ice: unknown candidate type %q", s)
	}
}

// Candidate is a single ICE candidate: a (type, network, address, port) tuple
// with its computed priority.
type Candidate struct {
	// Type is how the candidate was obtained (host, srflx, relay, ...).
	Type CandidateType
	// Network is the transport protocol, e.g. "udp" or "tcp".
	Network string
	// Address is the candidate IP address.
	Address string
	// Port is the candidate port.
	Port uint16
	// Component is the ICE component (1 for RTP/RTCP in the common case).
	Component uint16
	// Priority is the connection priority from CandidatePriority. Higher wins.
	Priority uint32
}

// CandidatePriority computes the ICE connection priority for a candidate.
//
//	priority = (2^24)*typePreference + (2^8)*localPreference + (256 - component)
//
// The formula matches pion/ice: higher values are more preferred. typePreference
// comes from Preference(); localPreference is an agent-chosen weight in
// [0, 65535] that breaks ties among same-type candidates (higher is better and
// SHOULD be unique per type/component); component is normally 1.
func CandidatePriority(typePreference, localPreference, component uint16) uint32 {
	return (1<<24)*uint32(typePreference) +
		(1<<8)*uint32(localPreference) +
		(256 - uint32(component))
}

// NewHost builds a host candidate for addr:port and computes its priority.
// localPreference breaks ties among same-type candidates.
func NewHost(addr net.IP, port uint16, localPreference uint16) Candidate {
	return Candidate{
		Type:      CandidateTypeHost,
		Network:   "udp",
		Address:   addr.String(),
		Port:      port,
		Component: 1,
		Priority:  CandidatePriority(CandidateTypeHost.Preference(), localPreference, 1),
	}
}

// Network returns the candidate's network lower-cased ("udp"/"tcp").
func (c Candidate) NetworkLower() string {
	return strings.ToLower(c.Network)
}

// Key is a stable identity for a candidate: "network:address:port".
func (c Candidate) Key() string {
	return c.NetworkLower() + ":" + c.Address + ":" + strconv.Itoa(int(c.Port))
}

// Equal reports whether a and b identify the same candidate (same network,
// address, and port).
func (a Candidate) Equal(b Candidate) bool {
	return a.Key() == b.Key()
}
