// Package transfer signs and verifies the expiring, purpose-bound
// tokens that protect the two public file-transfer endpoints (audit
// P0.3): firmware downloads fetched by a CPE's Download RPC, and upload
// receipts PUT back by a CPE's Upload RPC. Neither caller can present
// an operator JWT, so the URL itself must carry a credential — but a
// bare persistent UUID is a permanent bearer secret. These tokens are
// HMAC-signed over (purpose, resource id, expiry), so a leaked URL goes
// stale, a firmware token cannot be replayed against an upload slot,
// and nothing secret is stored server-side.
package transfer

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

var (
	ErrInvalid = errors.New("transfer token invalid")
	ErrExpired = errors.New("transfer token expired")
)

// DeriveKey derives the transfer-URL signing key from a parent secret,
// so no extra environment variable is needed and a leaked transfer key
// cannot be used where the parent secret is expected.
func DeriveKey(parent []byte) []byte {
	mac := hmac.New(sha256.New, parent)
	mac.Write([]byte("acs-transfer-url-v1"))
	return mac.Sum(nil)
}

func digest(key []byte, purpose, id string, expUnix int64) []byte {
	mac := hmac.New(sha256.New, key)
	fmt.Fprintf(mac, "%s\x00%s\x00%d", purpose, id, expUnix)
	return mac.Sum(nil)
}

// Sign returns a token bound to purpose+id that verifies until expires.
// Format: "<unix expiry, decimal>.<hex hmac-sha256>".
func Sign(key []byte, purpose, id string, expires time.Time) string {
	exp := expires.Unix()
	return strconv.FormatInt(exp, 10) + "." + hex.EncodeToString(digest(key, purpose, id, exp))
}

// Verify checks token against purpose+id at time now. It returns
// ErrExpired only for a token whose signature is valid but stale, and
// ErrInvalid for everything else — a tampered expiry fails the
// signature check, not the expiry check.
func Verify(key []byte, purpose, id, token string, now time.Time) error {
	expStr, macHex, ok := strings.Cut(token, ".")
	if !ok {
		return ErrInvalid
	}
	exp, err := strconv.ParseInt(expStr, 10, 64)
	if err != nil {
		return ErrInvalid
	}
	got, err := hex.DecodeString(macHex)
	if err != nil {
		return ErrInvalid
	}
	if !hmac.Equal(got, digest(key, purpose, id, exp)) {
		return ErrInvalid
	}
	if now.Unix() > exp {
		return ErrExpired
	}
	return nil
}
