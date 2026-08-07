package vpn

import (
	"errors"
	"fmt"
	"net"
)

// ErrOverlaySubnetExhausted means every address in the configured
// overlay subnet is already assigned to an ENROLLED peer.
var ErrOverlaySubnetExhausted = errors.New("overlay subnet exhausted")

// AllocateOverlayIP returns the lowest free host address in subnet,
// skipping the network address, the broadcast address, and .1 (reserved
// for the concentrator's own interface — the design doc's recommended
// address for the WireGuard server side of the tunnel). used holds every
// address already assigned to a non-revoked peer.
func AllocateOverlayIP(subnet *net.IPNet, used map[string]bool) (string, error) {
	ip := subnet.IP.Mask(subnet.Mask).To4()
	if ip == nil {
		return "", fmt.Errorf("overlay subnet must be IPv4: %s", subnet)
	}

	for candidate := nextIP(ip); subnet.Contains(candidate); candidate = nextIP(candidate) {
		if candidate[3] == 1 {
			continue // reserved for the concentrator itself
		}
		if candidate.Equal(broadcastAddr(subnet)) {
			continue
		}
		s := candidate.String()
		if !used[s] {
			return s, nil
		}
	}
	return "", ErrOverlaySubnetExhausted
}

func nextIP(ip net.IP) net.IP {
	out := make(net.IP, len(ip))
	copy(out, ip)
	for i := len(out) - 1; i >= 0; i-- {
		out[i]++
		if out[i] != 0 {
			break
		}
	}
	return out
}

func broadcastAddr(subnet *net.IPNet) net.IP {
	ip := subnet.IP.To4()
	mask := subnet.Mask
	out := make(net.IP, 4)
	for i := range out {
		out[i] = ip[i] | ^mask[i]
	}
	return out
}
