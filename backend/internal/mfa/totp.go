// Package mfa is the operator console's second login step: time-based
// one-time codes (TOTP, RFC 6238) from an authenticator app, with one-time
// recovery codes for a lost phone.
package mfa

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1" //nolint:gosec // RFC 6238's default, and what every authenticator app implements
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"net/url"
	"strings"
	"time"
)

const (
	digits     = 6
	period     = 30 * time.Second
	secretSize = 20 // bytes; RFC 4226 recommends 160 bits
	// skew is how many steps either side of now are accepted, for a phone
	// clock that runs a little off.
	skew = 1
)

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// NewSecret returns a random shared secret in the base32 form
// authenticator apps take.
func NewSecret() (string, error) {
	buf := make([]byte, secretSize)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate MFA secret: %w", err)
	}
	return b32.EncodeToString(buf), nil
}

// step is the 30-second counter for t.
func step(t time.Time) int64 { return t.Unix() / int64(period/time.Second) }

// codeAt computes the code for one step (RFC 4226 dynamic truncation).
func codeAt(secret string, counter int64) (string, error) {
	key, err := b32.DecodeString(strings.ToUpper(strings.TrimSpace(secret)))
	if err != nil {
		return "", fmt.Errorf("decode MFA secret: %w", err)
	}
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(counter))
	mac := hmac.New(sha1.New, key)
	mac.Write(msg[:])
	sum := mac.Sum(nil)
	offset := sum[len(sum)-1] & 0x0f
	value := binary.BigEndian.Uint32(sum[offset:offset+4]) & 0x7fffffff
	return fmt.Sprintf("%0*d", digits, value%1_000_000), nil
}

// Code returns the code an authenticator shows at t.
func Code(secret string, t time.Time) (string, error) { return codeAt(secret, step(t)) }

// Verify reports the step whose code matches, looking one step either side
// of t, and only at steps after lastUsed so an accepted code cannot be
// replayed. ok is false when nothing matches.
func Verify(secret, code string, t time.Time, lastUsed int64) (matched int64, ok bool) {
	code = NormalizeCode(code)
	if len(code) != digits {
		return 0, false
	}
	now := step(t)
	for s := now - skew; s <= now+skew; s++ {
		if s <= lastUsed {
			continue
		}
		want, err := codeAt(secret, s)
		if err != nil {
			return 0, false
		}
		if subtle.ConstantTimeCompare([]byte(want), []byte(code)) == 1 {
			return s, true
		}
	}
	return 0, false
}

// NormalizeCode strips the spaces and dashes people type or paste.
func NormalizeCode(code string) string {
	return strings.Map(func(r rune) rune {
		if r == ' ' || r == '-' {
			return -1
		}
		return r
	}, strings.TrimSpace(code))
}

// URI is the otpauth:// link an authenticator app reads from a QR code.
func URI(issuer, account, secret string) string {
	label := url.PathEscape(issuer + ":" + account)
	q := url.Values{}
	q.Set("secret", secret)
	q.Set("issuer", issuer)
	q.Set("algorithm", "SHA1")
	q.Set("digits", fmt.Sprint(digits))
	q.Set("period", fmt.Sprint(int(period/time.Second)))
	return "otpauth://totp/" + label + "?" + q.Encode()
}
