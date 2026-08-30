package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrInvalidToken covers every way a JWT can fail to verify: bad
// signature, malformed structure, or expiry — deliberately one error, not
// several, so callers can't be tempted to give a caller more information
// about *why* their token was rejected than an attacker should get.
var ErrInvalidToken = errors.New("invalid or expired token")

// Claims is the JWT payload for operator sessions (design doc v3 §11.3
// credential class 4: REST/API operator, "OIDC/JWT rotation"). There is
// no external identity provider in this lab, so cmd/api is its own
// minimal token issuer — this is that token's shape.
type Claims struct {
	Subject   string
	Role      string
	IssuedAt  time.Time
	ExpiresAt time.Time
	// Audience separates token kinds that must not be interchangeable
	// (audit P1.4): "" is an ordinary operator session presented in the
	// Authorization header; AudienceBrowserTicket is a short-lived
	// ticket that may travel in a URL for the one WebSocket/iframe
	// handshake a browser cannot attach a header to.
	Audience string
	// Version is the operator's token_version at issue time; a token is
	// revoked once the stored version moves past it (password change,
	// logout). Zero for tokens that predate revocation support.
	Version int
}

// AudienceBrowserTicket marks a ticket minted by POST /auth/ticket.
const AudienceBrowserTicket = "browser-ticket"

type jwtPayload struct {
	Subject   string `json:"sub"`
	Role      string `json:"role"`
	IssuedAt  int64  `json:"iat"`
	ExpiresAt int64  `json:"exp"`
	Audience  string `json:"aud,omitempty"`
	Version   int    `json:"ver,omitempty"`
}

const jwtHeader = `{"alg":"HS256","typ":"JWT"}`

// SignJWT issues an HS256-signed JWT. Hand-rolled against crypto/hmac
// rather than pulling in a JWT library — HS256 is a direct fit for
// stdlib, unlike Digest's nonce/qop rules or the CWMP SOAP layer, both of
// which stayed hand-rolled for the same reason: the interesting
// complexity here is small enough that a dependency would hide more than
// it saves. Password *hashing* is different (bcrypt, via golang.org/x/crypto)
// — that's not something to hand-roll.
func SignJWT(secret []byte, claims Claims) (string, error) {
	payload, err := json.Marshal(jwtPayload{
		Subject:   claims.Subject,
		Role:      claims.Role,
		IssuedAt:  claims.IssuedAt.Unix(),
		ExpiresAt: claims.ExpiresAt.Unix(),
		Audience:  claims.Audience,
		Version:   claims.Version,
	})
	if err != nil {
		return "", fmt.Errorf("marshal claims: %w", err)
	}
	signingInput := b64(([]byte)(jwtHeader)) + "." + b64(payload)
	return signingInput + "." + b64(sign(secret, signingInput)), nil
}

// VerifyJWT checks the signature (constant-time) and expiry, returning
// the parsed claims only if both hold.
func VerifyJWT(secret []byte, token string) (*Claims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, ErrInvalidToken
	}

	expected := sign(secret, parts[0]+"."+parts[1])
	got, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || subtle.ConstantTimeCompare(expected, got) != 1 {
		return nil, ErrInvalidToken
	}

	payloadRaw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, ErrInvalidToken
	}
	var p jwtPayload
	if err := json.Unmarshal(payloadRaw, &p); err != nil {
		return nil, ErrInvalidToken
	}

	claims := &Claims{
		Subject:   p.Subject,
		Role:      p.Role,
		IssuedAt:  time.Unix(p.IssuedAt, 0).UTC(),
		ExpiresAt: time.Unix(p.ExpiresAt, 0).UTC(),
		Audience:  p.Audience,
		Version:   p.Version,
	}
	if time.Now().After(claims.ExpiresAt) {
		return nil, ErrInvalidToken
	}
	return claims, nil
}

func sign(secret []byte, signingInput string) []byte {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(signingInput))
	return mac.Sum(nil)
}

func b64(b []byte) string {
	return base64.RawURLEncoding.EncodeToString(b)
}
