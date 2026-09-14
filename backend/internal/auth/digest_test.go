package auth

import (
	"crypto/md5"
	"crypto/sha256"
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
	if ok, stale, _ := testAuthr.Verify(digestRequest(nonce, "00000001", "s3cret")); !ok || stale {
		t.Errorf("Verify() = (%v, %v) for correctly computed Digest credentials, want (true, false)", ok, stale)
	}
}

func TestDigestAuthenticator_WrongPassword(t *testing.T) {
	nonce := issuedNonce(t, testAuthr, time.Now())
	if ok, _, _ := testAuthr.Verify(digestRequest(nonce, "00000001", "wrong-password")); ok {
		t.Error("Verify() = true for wrong password, want false")
	}
}

func TestDigestAuthenticator_MissingHeader(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/cwmp", nil)
	if ok, _, _ := testAuthr.Verify(req); ok {
		t.Error("Verify() = true with no Authorization header, want false")
	}
}

// --- audit P0.5: nonce authenticity, expiry, replay, uri ---------------

func TestDigest_ForeignNonceRejected(t *testing.T) {
	// A nonce not issued by this ACS (or issued under a different
	// credential) must fail even with the right password.
	for _, nonce := range []string{"testnonce", "1700000000.abcd.0000", issuedNonce(t, DigestAuthenticator{Username: "cpe-device", Password: "other"}, time.Now())} {
		if ok, _, _ := testAuthr.Verify(digestRequest(nonce, "00000001", "s3cret")); ok {
			t.Errorf("Verify() = true for foreign nonce %q, want false", nonce)
		}
	}
}

func TestDigest_TamperedNonceTimestampRejected(t *testing.T) {
	nonce := issuedNonce(t, testAuthr, time.Now().Add(-time.Hour))
	parts := strings.SplitN(nonce, ".", 3)
	forged := "9999999999." + parts[1] + "." + parts[2]
	if ok, stale, _ := testAuthr.Verify(digestRequest(forged, "00000001", "s3cret")); ok || stale {
		t.Errorf("Verify() = (%v, %v) for forged timestamp, want (false, false)", ok, stale)
	}
}

func TestDigest_ExpiredNonceIsStaleNotFailed(t *testing.T) {
	nonce := issuedNonce(t, testAuthr, time.Now().Add(-nonceTTL-time.Minute))
	ok, stale, _ := testAuthr.Verify(digestRequest(nonce, "00000001", "s3cret"))
	if ok || !stale {
		t.Errorf("Verify() = (%v, %v) for expired nonce with right password, want (false, true)", ok, stale)
	}
	// Wrong password on an expired nonce must NOT reveal staleness.
	ok, stale, _ = testAuthr.Verify(digestRequest(nonce, "00000001", "wrong"))
	if ok || stale {
		t.Errorf("Verify() = (%v, %v) for expired nonce with wrong password, want (false, false)", ok, stale)
	}
}

func TestDigest_ReplayRejected(t *testing.T) {
	nonce := issuedNonce(t, testAuthr, time.Now())
	if ok, _, _ := testAuthr.Verify(digestRequest(nonce, "00000001", "s3cret")); !ok {
		t.Fatal("first use rejected")
	}
	// Exact replay of the same (nonce, nc).
	if ok, _, _ := testAuthr.Verify(digestRequest(nonce, "00000001", "s3cret")); ok {
		t.Error("Verify() = true for replayed (nonce, nc), want false")
	}
	// nc must strictly increase — going backwards is a replay too.
	if ok, _, _ := testAuthr.Verify(digestRequest(nonce, "00000003", "s3cret")); !ok {
		t.Error("nc=3 after nc=1 rejected, want accepted")
	}
	if ok, _, _ := testAuthr.Verify(digestRequest(nonce, "00000002", "s3cret")); ok {
		t.Error("Verify() = true for nc=2 after nc=3, want false")
	}
}

func TestDigest_LegacyNonQopIsSingleUse(t *testing.T) {
	nonce := issuedNonce(t, testAuthr, time.Now())
	req := httptest.NewRequest(http.MethodPost, "/cwmp", nil)
	req.Header.Set("Authorization", buildLegacyAuthHeader("cpe-device", "s3cret", http.MethodPost, "/cwmp", nonce))
	if ok, _, _ := testAuthr.Verify(req); !ok {
		t.Fatal("legacy non-qop response rejected on first use, want accepted")
	}
	if ok, _, _ := testAuthr.Verify(req); ok {
		t.Error("Verify() = true for replayed legacy non-qop response, want false")
	}
}

func TestDigest_URIMismatchRejected(t *testing.T) {
	nonce := issuedNonce(t, testAuthr, time.Now())
	req := httptest.NewRequest(http.MethodPost, "/cwmp", nil)
	// Signed over a different uri than the request actually targets.
	req.Header.Set("Authorization", buildAuthHeader("cpe-device", "s3cret", http.MethodPost, "/other", nonce, "00000001", "c"))
	if ok, _, _ := testAuthr.Verify(req); ok {
		t.Error("Verify() = true for uri mismatch, want false")
	}
}

func TestDigest_WrongRealmRejected(t *testing.T) {
	nonce := issuedNonce(t, testAuthr, time.Now())
	req := httptest.NewRequest(http.MethodPost, "/cwmp", nil)
	hdr := buildAuthHeader("cpe-device", "s3cret", http.MethodPost, "/cwmp", nonce, "00000001", "c")
	req.Header.Set("Authorization", strings.Replace(hdr, `realm="acs"`, `realm="evil"`, 1))
	if ok, _, _ := testAuthr.Verify(req); ok {
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

// --- per-device credentials (audit P0.5 remainder) ---------------------

func TestDigest_PerDeviceLookup(t *testing.T) {
	var activated []string
	d := DigestAuthenticator{
		Username: "cpe-device", Password: "s3cret",
		Lookup: func(u string) (string, string, bool) {
			if u == "cr-device-1" {
				return "device-1-password", "device-1-id", true
			}
			return "", "", false
		},
		OnAuthenticated: func(u string) { activated = append(activated, u) },
	}
	nonce := issuedNonce(t, d, time.Now())
	mk := func(user, pass, nc string) *http.Request {
		req := httptest.NewRequest(http.MethodPost, "/cwmp", nil)
		req.Header.Set("Authorization", buildAuthHeader(user, pass, http.MethodPost, "/cwmp", nonce, nc, "c"))
		return req
	}
	ok, _, identity := d.Verify(mk("cr-device-1", "device-1-password", "00000001"))
	if !ok {
		t.Fatal("per-device credential rejected, want accepted")
	}
	if identity.BoundDeviceID != "device-1-id" {
		t.Errorf("Identity.BoundDeviceID = %q, want %q (audit C-1)", identity.BoundDeviceID, "device-1-id")
	}
	if len(activated) != 1 || activated[0] != "cr-device-1" {
		t.Errorf("OnAuthenticated calls = %v, want [cr-device-1]", activated)
	}
	if ok, _, _ := d.Verify(mk("cr-device-1", "wrong", "00000002")); ok {
		t.Error("per-device credential with wrong password accepted")
	}
	if ok, _, _ := d.Verify(mk("cr-unknown", "device-1-password", "00000003")); ok {
		t.Error("unknown per-device username accepted")
	}
	// The shared credential keeps working alongside, without the hook, and
	// asserts no device binding (audit C-1: shared credential = no identity).
	ok, _, identity = d.Verify(mk("cpe-device", "s3cret", "00000004"))
	if !ok {
		t.Error("shared credential rejected once Lookup is set")
	}
	if identity.BoundDeviceID != "" {
		t.Errorf("shared credential Identity.BoundDeviceID = %q, want empty", identity.BoundDeviceID)
	}
	if len(activated) != 1 {
		t.Errorf("OnAuthenticated fired for the shared credential: %v", activated)
	}
}

func TestDigest_EnabledWithLookupOnly(t *testing.T) {
	d := DigestAuthenticator{Lookup: func(string) (string, string, bool) { return "", "", false }, NonceSecret: []byte("k")}
	if !d.Enabled() {
		t.Error("Enabled() = false with only a per-device Lookup, want true")
	}
}

// --- SHA-256 Digest (RFC 7616) -----------------------------------------

func sha256hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// buildAuthHeaderAlg is buildAuthHeader with the algorithm (and the hash
// the response is actually computed with) chosen by the caller, standing
// in for a CPE that picked the SHA-256 challenge line.
func buildAuthHeaderAlg(username, password, method, uri, nonce, nc, cnonce, algorithm string, h func(string) string) string {
	ha1 := h(username + ":" + realm + ":" + password)
	ha2 := h(method + ":" + uri)
	response := h(ha1 + ":" + nonce + ":" + nc + ":" + cnonce + ":auth:" + ha2)

	return `Digest username="` + username + `", realm="` + realm +
		`", nonce="` + nonce + `", uri="` + uri +
		`", qop=auth, nc=` + nc + `, cnonce="` + cnonce +
		`", response="` + response + `", algorithm=` + algorithm
}

func algRequest(nonce, nc, password, algorithm string, h func(string) string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/cwmp", nil)
	req.Header.Set("Authorization", buildAuthHeaderAlg(
		"cpe-device", password, http.MethodPost, "/cwmp", nonce, nc, "testcnonce", algorithm, h))
	return req
}

func TestDigest_SHA256ResponseVerifies(t *testing.T) {
	nonce := issuedNonce(t, testAuthr, time.Now())
	if ok, stale, _ := testAuthr.Verify(algRequest(nonce, "00000001", "s3cret", "SHA-256", sha256hex)); !ok || stale {
		t.Errorf("Verify() = (%v, %v) for a valid SHA-256 Digest response, want (true, false)", ok, stale)
	}
}

func TestDigest_SHA256WrongPasswordRejected(t *testing.T) {
	nonce := issuedNonce(t, testAuthr, time.Now())
	if ok, _, _ := testAuthr.Verify(algRequest(nonce, "00000001", "wrong", "SHA-256", sha256hex)); ok {
		t.Error("Verify() = true for a SHA-256 response computed with the wrong password, want false")
	}
}

// A response must be verified with the hash it claims, not whichever one
// happens to match — an MD5-labelled response computed with SHA-256 (or
// vice versa) is a forgery attempt, not a fallback.
func TestDigest_AlgorithmMismatchRejected(t *testing.T) {
	nonce := issuedNonce(t, testAuthr, time.Now())
	if ok, _, _ := testAuthr.Verify(algRequest(nonce, "00000001", "s3cret", "MD5", sha256hex)); ok {
		t.Error("Verify() = true for a SHA-256 digest labelled algorithm=MD5, want false")
	}
	if ok, _, _ := testAuthr.Verify(algRequest(nonce, "00000002", "s3cret", "SHA-256", md5hex)); ok {
		t.Error("Verify() = true for an MD5 digest labelled algorithm=SHA-256, want false")
	}
}

// Anything outside the two algorithms challenge() offers is refused
// rather than silently verified against the wrong hash.
func TestDigest_UnsupportedAlgorithmRejected(t *testing.T) {
	for _, alg := range []string{"MD5-sess", "SHA-256-sess", "SHA-512-256", "bogus"} {
		nonce := issuedNonce(t, testAuthr, time.Now())
		if ok, _, _ := testAuthr.Verify(algRequest(nonce, "00000001", "s3cret", alg, md5hex)); ok {
			t.Errorf("Verify() = true for algorithm=%s, want false", alg)
		}
	}
}

// The lower-case spelling some CPE stacks emit means the same thing.
func TestDigest_AlgorithmCaseInsensitive(t *testing.T) {
	nonce := issuedNonce(t, testAuthr, time.Now())
	if ok, _, _ := testAuthr.Verify(algRequest(nonce, "00000001", "s3cret", "sha-256", sha256hex)); !ok {
		t.Error("Verify() = false for algorithm=sha-256 (lower case), want true")
	}
	nonce = issuedNonce(t, testAuthr, time.Now())
	if ok, _, _ := testAuthr.Verify(algRequest(nonce, "00000001", "s3cret", "md5", md5hex)); !ok {
		t.Error("Verify() = false for algorithm=md5 (lower case), want true")
	}
}

// A CPE that only implements SHA-256 needs its own challenge line — an
// MD5-only challenge is what made Huawei's cwmpmng give up entirely.
func TestDigest_ChallengeOffersBothAlgorithms(t *testing.T) {
	rec := httptest.NewRecorder()
	testAuthr.Challenge(rec)

	lines := rec.Header().Values("WWW-Authenticate")
	if len(lines) != 2 {
		t.Fatalf("Challenge() sent %d WWW-Authenticate lines, want 2 (MD5 + SHA-256): %q", len(lines), lines)
	}
	if !strings.Contains(lines[0], "algorithm=MD5") {
		t.Errorf("first challenge line = %q, want algorithm=MD5 first (RFC 2617 CPEs take the first they understand)", lines[0])
	}
	if !strings.Contains(lines[1], "algorithm=SHA-256") {
		t.Errorf("second challenge line = %q, want algorithm=SHA-256", lines[1])
	}

	// Both lines must carry the same, valid nonce: a CPE picking either
	// one gets a nonce this ACS will accept.
	var nonces []string
	for _, line := range lines {
		params := parseDigestParams(strings.TrimPrefix(line, "Digest "))
		if _, valid := testAuthr.parseNonce(params["nonce"]); !valid {
			t.Errorf("challenge line %q issued a nonce parseNonce rejects", line)
		}
		nonces = append(nonces, params["nonce"])
	}
	if nonces[0] != nonces[1] {
		t.Errorf("challenge lines issued different nonces (%q vs %q); a CPE that sees both must not have to guess", nonces[0], nonces[1])
	}

	// And a SHA-256 response against that challenge's nonce verifies.
	if ok, _, _ := testAuthr.Verify(algRequest(nonces[1], "00000001", "s3cret", "SHA-256", sha256hex)); !ok {
		t.Error("SHA-256 response against the challenge's own nonce did not verify")
	}
}

func TestDigest_ChallengeStaleOnBothAlgorithms(t *testing.T) {
	rec := httptest.NewRecorder()
	testAuthr.ChallengeStale(rec)
	for _, line := range rec.Header().Values("WWW-Authenticate") {
		if !strings.Contains(line, "stale=true") {
			t.Errorf("ChallengeStale() line %q is missing stale=true; a SHA-256 CPE would treat expiry as an auth failure", line)
		}
	}
}

func TestDigest_BasicChallengeStillOfferedAlongsideBoth(t *testing.T) {
	d := DigestAuthenticator{Username: "cpe-device", Password: "s3cret", AllowBasic: true}
	rec := httptest.NewRecorder()
	d.Challenge(rec)
	lines := rec.Header().Values("WWW-Authenticate")
	if len(lines) != 3 {
		t.Fatalf("Challenge() with AllowBasic sent %d lines, want 3 (MD5, SHA-256, Basic): %q", len(lines), lines)
	}
	if !strings.HasPrefix(lines[2], "Basic ") {
		t.Errorf("last challenge line = %q, want the Basic challenge", lines[2])
	}
}
