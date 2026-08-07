package vpn

import "fmt"

// ConcentratorConfig is the ACS-side half of the tunnel — its own
// keypair, the address a peer dials to reach it, and the overlay subnet
// peers are allocated from. Sourced from env vars in cmd/api's main()
// (ACS_VPN_SERVER_PUBLIC_KEY / ACS_VPN_SERVER_ENDPOINT /
// ACS_VPN_OVERLAY_SUBNET), the same "off unless configured" shape as
// every other infra credential in this codebase — there is deliberately
// no server *private* key here, since this process never runs the
// interface itself (see this package's doc comment); the private key
// belongs to whatever concentrator host actually applies these peers,
// not to cmd/api.
type ConcentratorConfig struct {
	ServerPublicKey string `json:"server_public_key"`
	Endpoint        string `json:"endpoint"`                  // host:port a peer's [Peer] section dials
	OverlaySubnet   string `json:"overlay_subnet"`            // CIDR, e.g. 10.99.0.0/16
	ConcentratorIP  string `json:"concentrator_ip,omitempty"` // .1 in the subnet, reserved by AllocateOverlayIP
}

func (c ConcentratorConfig) Configured() bool {
	return c.ServerPublicKey != "" && c.Endpoint != ""
}

// RenderClientConfig produces a standard wg-quick [Interface]/[Peer]
// config file for one enrolled peer — the artifact an operator would
// actually paste onto a device (or into whatever vendor-specific
// mechanism gets it there; TR-069 has no push RPC for this). Format is
// exactly wg-quick(8)'s, not a custom shape, so it's usable with a stock
// WireGuard client without translation.
func RenderClientConfig(peer *Peer, concentrator ConcentratorConfig) string {
	return fmt.Sprintf(`[Interface]
PrivateKey = %s
Address = %s/32

[Peer]
PublicKey = %s
Endpoint = %s
AllowedIPs = %s
PersistentKeepalive = 25
`, peer.PrivateKey, peer.OverlayIP, concentrator.ServerPublicKey, concentrator.Endpoint, concentrator.OverlaySubnet)
}
