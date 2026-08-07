package vpn

import (
	"encoding/base64"
	"net"
	"strings"
	"testing"

	"golang.org/x/crypto/curve25519"
)

func TestGenerateKeyPair(t *testing.T) {
	a, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair: %v", err)
	}
	priv, err := base64.StdEncoding.DecodeString(a.PrivateKey)
	if err != nil || len(priv) != 32 {
		t.Fatalf("private key = %d bytes, err %v, want 32 bytes base64", len(priv), err)
	}
	pub, err := base64.StdEncoding.DecodeString(a.PublicKey)
	if err != nil || len(pub) != 32 {
		t.Fatalf("public key = %d bytes, err %v, want 32 bytes base64", len(pub), err)
	}

	// The public key must be exactly what a stock WireGuard `wg pubkey`
	// would compute from this private key — recompute independently and
	// compare, rather than trusting our own derivation.
	wantPub, err := curve25519.X25519(priv, curve25519.Basepoint)
	if err != nil {
		t.Fatalf("recompute public key: %v", err)
	}
	if !strings.EqualFold(base64.StdEncoding.EncodeToString(wantPub), a.PublicKey) {
		t.Errorf("public key does not match independent X25519 derivation")
	}

	b, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair (second): %v", err)
	}
	if a.PrivateKey == b.PrivateKey {
		t.Error("two calls produced the same private key — rand.Read not actually random?")
	}
}

func TestAllocateOverlayIP(t *testing.T) {
	_, subnet, err := net.ParseCIDR("10.99.0.0/29") // 10.99.0.0-7, tiny subnet to exercise exhaustion
	if err != nil {
		t.Fatalf("ParseCIDR: %v", err)
	}

	// .1 is reserved for the concentrator even though nothing has used it yet.
	ip, err := AllocateOverlayIP(subnet, map[string]bool{})
	if err != nil {
		t.Fatalf("AllocateOverlayIP: %v", err)
	}
	if ip != "10.99.0.2" {
		t.Errorf("first allocation = %q, want 10.99.0.2 (10.99.0.1 reserved)", ip)
	}

	ip, err = AllocateOverlayIP(subnet, map[string]bool{"10.99.0.2": true})
	if err != nil {
		t.Fatalf("AllocateOverlayIP: %v", err)
	}
	if ip != "10.99.0.3" {
		t.Errorf("second allocation = %q, want 10.99.0.3", ip)
	}

	// /29 has 10.99.0.0-7: .0 network, .1 reserved, .7 broadcast — so
	// .2 through .6 (5 addresses) are the only allocatable range.
	used := map[string]bool{"10.99.0.2": true, "10.99.0.3": true, "10.99.0.4": true, "10.99.0.5": true, "10.99.0.6": true}
	if _, err := AllocateOverlayIP(subnet, used); err != ErrOverlaySubnetExhausted {
		t.Errorf("err = %v, want ErrOverlaySubnetExhausted", err)
	}
}

func TestRenderClientConfig(t *testing.T) {
	peer := &Peer{PrivateKey: "priv-key-b64", OverlayIP: "10.99.0.2"}
	cfg := ConcentratorConfig{ServerPublicKey: "server-pub-b64", Endpoint: "vpn.example.com:51820", OverlaySubnet: "10.99.0.0/16"}
	out := RenderClientConfig(peer, cfg)

	for _, want := range []string{
		"PrivateKey = priv-key-b64",
		"Address = 10.99.0.2/32",
		"PublicKey = server-pub-b64",
		"Endpoint = vpn.example.com:51820",
		"AllowedIPs = 10.99.0.0/16",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered config missing %q\ngot:\n%s", want, out)
		}
	}
}
