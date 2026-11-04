package ice

import (
	"context"
	"net"
	"sort"
	"strings"
)

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
		c := Candidate{
			Type:      CandidateTypeHost,
			Network:   network,
			Address:   addr.String(),
			Port:      0,
			Component: 1,
			Priority:  CandidatePriority(CandidateTypeHost.Preference(), localPreference, 1),
		}
		if _, ok := seen[c.Key()]; ok {
			return
		}
		seen[c.Key()] = struct{}{}
		out = append(out, c)
	}

	if wantUDP {
		out = append(out, gatherInterfaceAddresses("udp", add)...)
	}
	if wantTCP {
		out = append(out, gatherInterfaceAddresses("tcp", add)...)
	}

	sort.SliceStable(out, func(i, j int) bool { return out[i].Key() < out[j].Key() })
	return out, nil
}

// gatherInterfaceAddresses returns host candidates of network across all active
// interfaces, parsing each interface address for its IP.
func gatherInterfaceAddresses(network string, add func(string, net.IP)) []Candidate {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []Candidate
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
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
	}
	return out
}
