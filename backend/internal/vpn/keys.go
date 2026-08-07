// Package vpn is the peer registry behind the fleet-wide VPN/tunnel
// concentrator (admin-platform backlog, deliberately sequenced last —
// see migration 0036's header comment for the full scoping rationale).
// It owns keypair generation, overlay IP allocation, and the peer table;
// it does not manage an OS-level WireGuard interface.
package vpn

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"

	"golang.org/x/crypto/curve25519"
)

// KeyPair is a WireGuard-compatible Curve25519 keypair, base64-encoded
// the same way `wg genkey`/`wg pubkey` render them — this is the
// well-documented, standardized half of the VPN feature (unlike Annex
// G's HMAC scheme, there is no ambiguity to guess at here: RFC 7748
// Curve25519 plus WireGuard's own published clamping rule).
type KeyPair struct {
	PrivateKey string
	PublicKey  string
}

// GenerateKeyPair produces one real Curve25519 keypair per WireGuard's
// key format (Noise_IK over Curve25519 — see the WireGuard whitepaper
// §3): 32 random bytes, clamped per RFC 7748 §5, base64-encoded. The
// public key is derived via scalar multiplication against the Curve25519
// base point, matching exactly what `wg pubkey` computes from a private
// key — a real operator could paste either half into a stock `wg`
// command and get the same result.
func GenerateKeyPair() (KeyPair, error) {
	var priv [32]byte
	if _, err := rand.Read(priv[:]); err != nil {
		return KeyPair{}, fmt.Errorf("generate private key: %w", err)
	}
	// WireGuard's clamping rule (same as X25519 key clamping generally):
	// clear the low 3 bits of the first byte, clear the high bit and set
	// the second-highest bit of the last byte.
	priv[0] &= 248
	priv[31] &= 127
	priv[31] |= 64

	pub, err := curve25519.X25519(priv[:], curve25519.Basepoint)
	if err != nil {
		return KeyPair{}, fmt.Errorf("derive public key: %w", err)
	}

	return KeyPair{
		PrivateKey: base64.StdEncoding.EncodeToString(priv[:]),
		PublicKey:  base64.StdEncoding.EncodeToString(pub),
	}, nil
}
