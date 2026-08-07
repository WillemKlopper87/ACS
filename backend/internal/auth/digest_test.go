package auth

import (
	"crypto/md5"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"testing"
)

func md5hex(s string) string {
	sum := md5.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

// buildAuthHeader computes a valid RFC 2617 Digest Authorization header
// for the given credentials, method, and URI — standing in for what a
// real CPE's HTTP client would send.
func buildAuthHeader(username, password, method, uri, nonce, nc, cnonce string) string {
	ha1 := md5hex(username + ":" + realm + ":" + password)
	ha2 := md5hex(method + ":" + uri)
	response := md5hex(ha1 + ":" + nonce + ":" + nc + ":" + cnonce + ":auth:" + ha2)

	return `Digest username="` + username + `", realm="` + realm +
		`", nonce="` + nonce + `", uri="` + uri +
		`", qop=auth, nc=` + nc + `, cnonce="` + cnonce +
		`", response="` + response + `", algorithm=MD5`
}

func TestDigestAuthenticator_ValidCredentials(t *testing.T) {
	authr := DigestAuthenticator{Username: "cpe-device", Password: "s3cret"}

	req := httptest.NewRequest(http.MethodPost, "/cwmp", nil)
	req.Header.Set("Authorization", buildAuthHeader(
		"cpe-device", "s3cret", http.MethodPost, "/cwmp", "testnonce", "00000001", "testcnonce"))

	if !authr.Verify(req) {
		t.Error("Verify() = false for correctly computed Digest credentials, want true")
	}
}

func TestDigestAuthenticator_WrongPassword(t *testing.T) {
	authr := DigestAuthenticator{Username: "cpe-device", Password: "s3cret"}

	req := httptest.NewRequest(http.MethodPost, "/cwmp", nil)
	req.Header.Set("Authorization", buildAuthHeader(
		"cpe-device", "wrong-password", http.MethodPost, "/cwmp", "testnonce", "00000001", "testcnonce"))

	if authr.Verify(req) {
		t.Error("Verify() = true for wrong password, want false")
	}
}

func TestDigestAuthenticator_MissingHeader(t *testing.T) {
	authr := DigestAuthenticator{Username: "cpe-device", Password: "s3cret"}
	req := httptest.NewRequest(http.MethodPost, "/cwmp", nil)

	if authr.Verify(req) {
		t.Error("Verify() = true with no Authorization header, want false")
	}
}

func TestDigestAuthenticator_Enabled(t *testing.T) {
	if (DigestAuthenticator{}).Enabled() {
		t.Error("Enabled() = true for zero-value authenticator, want false")
	}
	if !(DigestAuthenticator{Username: "u", Password: "p"}).Enabled() {
		t.Error("Enabled() = false with username+password set, want true")
	}
}

func TestDigestAuthenticator_Challenge(t *testing.T) {
	authr := DigestAuthenticator{Username: "cpe-device", Password: "s3cret"}
	rec := httptest.NewRecorder()
	authr.Challenge(rec)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("Challenge() status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
	if rec.Header().Get("WWW-Authenticate") == "" {
		t.Error("Challenge() did not set WWW-Authenticate header")
	}
}
