package ice

import (
	"context"
	"net"
	"sort"
	"strings"
)

// ifaceAddr pairs a network interface with its assigned addresses. It exists so
// the parsing in GatherHostCandidates can be exercised against a fixed set of
// interfaces rather than the host's live network configuration.
type ifaceAddr struct {
	iface net.Interface
	addrs []net.Addr
}

// enumerateInterfaces returns the local interfaces paired with their addresses.
// It is a package variable so tests can substitute a fixed set and exercise the
// parsing branches (down interfaces, unspecified addresses, both address types,
// enumeration errors) deterministically instead of depending on the host's
// actual network configuration.
var enumerateInterfaces = func() ([]ifaceAddr, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	out := make([]ifaceAddr, 0, len(ifaces))
	for _, iface := range ifaces {
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		out = append(out, ifaceAddr{iface: iface, addrs: addrs})
	}
	return out, nil
}

// GatherHostCandidates returns the host candidates available on the local
// machine. A host candidate is a directly-assigned (network, address, port)
// tuple; here we enumerate every usable IP address on every active interface for
// each requested transport ("udp", "tcp"). An empty networks slice means both.
//
// Host gathering needs no network access: loopback interfaces always yield at
// least the 127.0.0.1 and ::1 candidates, so this works in sandboxes and CI.
//
// localPreference breaks ties among same-type candidates; it is assigned
// deterministically from the address so priorities stay distinct and orderable.
func GatherHostCandidates(ctx context.Context, networks []string, localPreference uint16) ([]Candidate, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	wantUDP, wantTCP := true, true
	if len(networks) > 0 {
		wantUDP, wantTCP = false, false
		for _, n := range networks {
			switch strings.ToLower(strings.TrimSpace(n)) {
			case "udp":
				wantUDP = true
			case "tcp":
				wantTCP = true
			}
		}
	}

	var out []Candidate
	seen := make(map[string]struct{})

	add := func(network string, addr net.IP) {
		if addr == nil || addr.IsUnspecified() {
			// Skip "all interfaces" addresses (0.0.0.0 / ::); loopback (127.0.0.1
			// / ::1) is a legitimate direct host path and is kept.
			return
		}
		// Port zero records address inventory. Pion allocates negotiation candidates separately.
		c := NewHost(addr, 0, localPreference)
		c.Network = network
		if _, ok := seen[c.Key()]; ok {
			return
		}
		seen[c.Key()] = struct{}{}
		out = append(out, c)
	}

	ifaces, err := enumerateInterfaces()
	if err != nil {
		return nil, err
	}

	for _, ia := range ifaces {
		if ia.iface.Flags&net.FlagUp == 0 {
			continue
		}
		if wantUDP {
			out = append(out, gatherInterfaceAddresses("udp", ia.addrs, add)...)
		}
		if wantTCP {
			out = append(out, gatherInterfaceAddresses("tcp", ia.addrs, add)...)
		}
	}

	sort.SliceStable(out, func(i, j int) bool { return out[i].Key() < out[j].Key() })
	return out, nil
}

// gatherInterfaceAddresses parses each address on one interface for its IP and
// hands the usable host addresses to add.
func gatherInterfaceAddresses(network string, addrs []net.Addr, add func(string, net.IP)) []Candidate {
	var out []Candidate
	for _, a := range addrs {
		var ip net.IP
		switch v := a.(type) {
		case *net.IPAddr:
			ip = v.IP
		case *net.IPNet:
			ip = v.IP
		}
		if ip != nil {
			add(network, ip)
		}
	}
	return out
}
