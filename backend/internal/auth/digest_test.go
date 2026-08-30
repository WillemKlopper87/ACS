package auth

import (
	"crypto/md5"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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

// buildLegacyAuthHeader is the non-qop (RFC 2069 style) form some older
// CPE stacks still send.
func buildLegacyAuthHeader(username, password, method, uri, nonce string) string {
	ha1 := md5hex(username + ":" + realm + ":" + password)
	ha2 := md5hex(method + ":" + uri)
	response := md5hex(ha1 + ":" + nonce + ":" + ha2)
	return `Digest username="` + username + `", realm="` + realm +
		`", nonce="` + nonce + `", uri="` + uri + `", response="` + response + `"`
}

var testAuthr = DigestAuthenticator{Username: "cpe-device", Password: "s3cret"}

// issuedNonce returns a nonce exactly as Challenge would have issued it.
func issuedNonce(t *testing.T, d DigestAuthenticator, at time.Time) string {
	t.Helper()
	return d.newNonce(at)
}

func digestRequest(nonce, nc, password string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/cwmp", nil)
	req.Header.Set("Authorization", buildAuthHeader(
		"cpe-device", password, http.MethodPost, "/cwmp", nonce, nc, "testcnonce"))
	return req
}

func TestDigestAuthenticator_ValidCredentials(t *testing.T) {
	nonce := issuedNonce(t, testAuthr, time.Now())
	if ok, stale := testAuthr.Verify(digestRequest(nonce, "00000001", "s3cret")); !ok || stale {
		t.Errorf("Verify() = (%v, %v) for correctly computed Digest credentials, want (true, false)", ok, stale)
	}
}

func TestDigestAuthenticator_WrongPassword(t *testing.T) {
	nonce := issuedNonce(t, testAuthr, time.Now())
	if ok, _ := testAuthr.Verify(digestRequest(nonce, "00000001", "wrong-password")); ok {
		t.Error("Verify() = true for wrong password, want false")
	}
}

func TestDigestAuthenticator_MissingHeader(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/cwmp", nil)
	if ok, _ := testAuthr.Verify(req); ok {
		t.Error("Verify() = true with no Authorization header, want false")
	}
}

// --- audit P0.5: nonce authenticity, expiry, replay, uri ---------------

func TestDigest_ForeignNonceRejected(t *testing.T) {
	// A nonce not issued by this ACS (or issued under a different
	// credential) must fail even with the right password.
	for _, nonce := range []string{"testnonce", "1700000000.abcd.0000", issuedNonce(t, DigestAuthenticator{Username: "cpe-device", Password: "other"}, time.Now())} {
		if ok, _ := testAuthr.Verify(digestRequest(nonce, "00000001", "s3cret")); ok {
			t.Errorf("Verify() = true for foreign nonce %q, want false", nonce)
		}
	}
}

func TestDigest_TamperedNonceTimestampRejected(t *testing.T) {
	nonce := issuedNonce(t, testAuthr, time.Now().Add(-time.Hour))
	parts := strings.SplitN(nonce, ".", 3)
	forged := "9999999999." + parts[1] + "." + parts[2]
	if ok, stale := testAuthr.Verify(digestRequest(forged, "00000001", "s3cret")); ok || stale {
		t.Errorf("Verify() = (%v, %v) for forged timestamp, want (false, false)", ok, stale)
	}
}

func TestDigest_ExpiredNonceIsStaleNotFailed(t *testing.T) {
	nonce := issuedNonce(t, testAuthr, time.Now().Add(-nonceTTL-time.Minute))
	ok, stale := testAuthr.Verify(digestRequest(nonce, "00000001", "s3cret"))
	if ok || !stale {
		t.Errorf("Verify() = (%v, %v) for expired nonce with right password, want (false, true)", ok, stale)
	}
	// Wrong password on an expired nonce must NOT reveal staleness.
	ok, stale = testAuthr.Verify(digestRequest(nonce, "00000001", "wrong"))
	if ok || stale {
		t.Errorf("Verify() = (%v, %v) for expired nonce with wrong password, want (false, false)", ok, stale)
	}
}

func TestDigest_ReplayRejected(t *testing.T) {
	nonce := issuedNonce(t, testAuthr, time.Now())
	if ok, _ := testAuthr.Verify(digestRequest(nonce, "00000001", "s3cret")); !ok {
		t.Fatal("first use rejected")
	}
	// Exact replay of the same (nonce, nc).
	if ok, _ := testAuthr.Verify(digestRequest(nonce, "00000001", "s3cret")); ok {
		t.Error("Verify() = true for replayed (nonce, nc), want false")
	}
	// nc must strictly increase — going backwards is a replay too.
	if ok, _ := testAuthr.Verify(digestRequest(nonce, "00000003", "s3cret")); !ok {
		t.Error("nc=3 after nc=1 rejected, want accepted")
	}
	if ok, _ := testAuthr.Verify(digestRequest(nonce, "00000002", "s3cret")); ok {
		t.Error("Verify() = true for nc=2 after nc=3, want false")
	}
}

func TestDigest_LegacyNonQopIsSingleUse(t *testing.T) {
	nonce := issuedNonce(t, testAuthr, time.Now())
	req := httptest.NewRequest(http.MethodPost, "/cwmp", nil)
	req.Header.Set("Authorization", buildLegacyAuthHeader("cpe-device", "s3cret", http.MethodPost, "/cwmp", nonce))
	if ok, _ := testAuthr.Verify(req); !ok {
		t.Fatal("legacy non-qop response rejected on first use, want accepted")
	}
	if ok, _ := testAuthr.Verify(req); ok {
		t.Error("Verify() = true for replayed legacy non-qop response, want false")
	}
}

func TestDigest_URIMismatchRejected(t *testing.T) {
	nonce := issuedNonce(t, testAuthr, time.Now())
	req := httptest.NewRequest(http.MethodPost, "/cwmp", nil)
	// Signed over a different uri than the request actually targets.
	req.Header.Set("Authorization", buildAuthHeader("cpe-device", "s3cret", http.MethodPost, "/other", nonce, "00000001", "c"))
	if ok, _ := testAuthr.Verify(req); ok {
		t.Error("Verify() = true for uri mismatch, want false")
	}
}

func TestDigest_WrongRealmRejected(t *testing.T) {
	nonce := issuedNonce(t, testAuthr, time.Now())
	req := httptest.NewRequest(http.MethodPost, "/cwmp", nil)
	hdr := buildAuthHeader("cpe-device", "s3cret", http.MethodPost, "/cwmp", nonce, "00000001", "c")
	req.Header.Set("Authorization", strings.Replace(hdr, `realm="acs"`, `realm="evil"`, 1))
	if ok, _ := testAuthr.Verify(req); ok {
		t.Error("Verify() = true for wrong realm, want false")
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
	rec := httptest.NewRecorder()
	testAuthr.Challenge(rec)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("Challenge() status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
	hdr := rec.Header().Get("WWW-Authenticate")
	if hdr == "" {
		t.Fatal("Challenge() did not set WWW-Authenticate header")
	}
	if strings.Contains(hdr, "stale=true") {
		t.Error("plain Challenge() must not set stale=true")
	}
	// The issued nonce must verify as ours.
	params := parseDigestParams(strings.TrimPrefix(hdr, "Digest "))
	if _, valid := testAuthr.parseNonce(params["nonce"]); !valid {
		t.Errorf("Challenge() issued nonce %q that parseNonce rejects", params["nonce"])
	}

	rec = httptest.NewRecorder()
	testAuthr.ChallengeStale(rec)
	if !strings.Contains(rec.Header().Get("WWW-Authenticate"), "stale=true") {
		t.Error("ChallengeStale() did not set stale=true")
	}
}
