// Package auth implements authentication for both CWMP and REST traffic.
// This file: CPE-to-ACS Digest auth for the CWMP endpoint (v3 §2.5/§11.2
// credential class 1 — mTLS is the eventual target, Digest is the
// fallback, unverified per-vendor until prerequisite P3 is resolved).
// jwt.go: operator JWT auth for cmd/api (v3 §11.3 credential class 4),
// added Phase 6.
package auth

import (
	"crypto/hmac"
	"crypto/md5"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const realm = "acs"

// nonceTTL bounds how long an issued Digest nonce verifies (audit P0.5).
// A CPE session comfortably completes within it; a captured
// Authorization header goes stale shortly after. Expiry is answered
// with stale=true so a well-behaved CPE silently re-authenticates.
const nonceTTL = 10 * time.Minute

// nonceState tracks replay for one issued nonce.
type nonceState struct {
	expires time.Time
	lastNC  uint64 // highest nonce-count seen (qop=auth); 0 = unused
	used    bool   // non-qop responses are strictly single-use
}

// nonceCache is the bounded replay cache (audit P0.5): (nonce, nc)
// pairs must be strictly increasing per nonce, and a non-qop response
// verifies at most once. Entries expire with their nonce.
var nonceCache = struct {
	sync.Mutex
	m map[string]*nonceState
}{m: map[string]*nonceState{}}

// nonceCacheMax caps the cache so an attacker hammering the endpoint
// with fresh challenges can't grow it without bound; expired entries
// are purged first, then verification is refused until space frees up
// (fail closed, never fail open).
const nonceCacheMax = 100_000

func purgeExpiredNoncesLocked(now time.Time) {
	for n, st := range nonceCache.m {
		if now.After(st.expires) {
			delete(nonceCache.m, n)
		}
	}
}

// DigestAuthenticator validates HTTP Digest (RFC 2617) credentials against
// a single configured username/password. Phase 0/1 have no per-device
// credential store yet (that lands with device_credentials in Phase 6,
// v3 §7.2) — every CPE in the lab fleet authenticates with one shared
// operator-configured credential.
type DigestAuthenticator struct {
	Username string
	Password string

	// AllowBasic additionally accepts HTTP Basic credentials (same
	// username/password). Some CPE firmwares only implement Basic for the
	// ACS connection, or default to it until reconfigured — enable this
	// (ACS_AUTH_ALLOW_BASIC=1) when onboarding such devices, ideally only
	// together with TLS since Basic sends the password in cleartext.
	AllowBasic bool

	// Lookup, when set, resolves a presented username that is not the
	// shared Username to its per-device password and the device it is
	// bound to (audit P0.5: unique per-device credentials; audit C-1:
	// deviceID lets the caller verify the Inform's claimed identity
	// matches the credential that authenticated it). deviceID is
	// whatever opaque identifier the caller's device store uses; empty
	// deviceID with ok=true is treated the same as the shared credential
	// — no per-device binding to enforce. Returning ok=false rejects the
	// request.
	Lookup func(username string) (password, deviceID string, ok bool)
	// OnAuthenticated is invoked after a per-device credential verifies —
	// the hook that auto-activates a PENDING rotation.
	OnAuthenticated func(username string)
	// NonceSecret keys the nonce HMAC. When empty the shared Password is
	// used, which is fine for the common single-credential fleet; set it
	// explicitly when the shared credential is absent (per-device only)
	// so nonces are still unforgeable.
	NonceSecret []byte
}

// Enabled reports whether a credential has been configured. When it
// hasn't, the caller should let requests through unauthenticated but log
// that fact loudly — this is a lab harness, not production (v3 §11.1:
// "No plaintext CWMP in production").
func (d DigestAuthenticator) Enabled() bool {
	return (d.Username != "" && d.Password != "") || d.Lookup != nil
}

// passwordFor resolves the password to verify a presented username
// against: the shared credential, or a per-device one via Lookup —
// returning that credential's bound device identifier (audit C-1), if
// any, so the caller can enforce it against the identity the request
// actually claims.
func (d DigestAuthenticator) passwordFor(username string) (password, deviceID string, perDevice bool, ok bool) {
	if d.Username != "" && username == d.Username {
		return d.Password, "", false, true
	}
	if d.Lookup != nil && username != "" {
		if pw, devID, found := d.Lookup(username); found {
			return pw, devID, true, true
		}
	}
	return "", "", false, false
}

// Challenge sends a 401 with a fresh Digest challenge (and, when
// AllowBasic is set, a Basic challenge as well — the CPE picks whichever
// scheme it implements). stale=true tells a CPE whose credentials were
// right but whose nonce had expired to simply retry with the new nonce
// (RFC 2617 §3.2.1) instead of treating it as an auth failure.
func (d DigestAuthenticator) Challenge(w http.ResponseWriter) {
	d.challenge(w, false)
}

// ChallengeStale is Challenge with stale=true — see Verify.
func (d DigestAuthenticator) ChallengeStale(w http.ResponseWriter) {
	d.challenge(w, true)
}

func (d DigestAuthenticator) challenge(w http.ResponseWriter, stale bool) {
	nonce := d.newNonce(time.Now())
	hdr := fmt.Sprintf(`Digest realm="%s", qop="auth", nonce="%s", algorithm=MD5`, realm, nonce)
	if stale {
		hdr += `, stale=true`
	}
	w.Header().Set("WWW-Authenticate", hdr)
	if d.AllowBasic {
		w.Header().Add("WWW-Authenticate", fmt.Sprintf(`Basic realm="%s"`, realm))
	}
	w.WriteHeader(http.StatusUnauthorized)
}

// Verify checks the Authorization header of an incoming CWMP request. It
// returns ok=false with no side effects if the header is missing or
// malformed — callers should respond with Challenge in that case.
// Stale reports the one case where the credentials were right but the
// nonce had expired; callers should answer that with ChallengeStale.
// Identity is what a successful Verify learned about the credential that
// authenticated the request (audit C-1). BoundDeviceID is empty for the
// shared fleet credential — no per-device binding to enforce, matching
// the pre-existing shared-credential compatibility tradeoff — and
// non-empty for a per-device credential, in which case the caller must
// verify the request's claimed device identity equals BoundDeviceID
// before trusting it.
type Identity struct {
	Username      string
	BoundDeviceID string
}

func (d DigestAuthenticator) Verify(r *http.Request) (ok bool, stale bool, identity Identity) {
	header := r.Header.Get("Authorization")
	switch {
	case strings.HasPrefix(header, "Digest "):
		return d.verifyDigest(r, header[len("Digest "):], time.Now())
	case d.AllowBasic && strings.HasPrefix(header, "Basic "):
		ok, identity := d.verifyBasic(header[len("Basic "):])
		return ok, false, identity
	default:
		return false, false, Identity{}
	}
}

// verifyDigest validates a Digest response (audit P0.5). Beyond the
// RFC 2617 hash check it requires: realm (when sent) matching ours; uri
// matching the request actually made; a nonce this ACS issued (HMAC over
// its timestamp, keyed from the configured password) that hasn't
// expired; and no replay — nc strictly increasing per nonce for
// qop=auth, single-use for legacy non-qop responses.
func (d DigestAuthenticator) verifyDigest(r *http.Request, rest string, now time.Time) (ok bool, stale bool, identity Identity) {
	params := parseDigestParams(rest)
	username := params["username"]
	password, deviceID, perDevice, found := d.passwordFor(username)
	if !found {
		return false, false, Identity{}
	}
	if rl, present := params["realm"]; present && rl != realm {
		return false, false, Identity{}
	}
	if params["uri"] != r.URL.RequestURI() {
		return false, false, Identity{}
	}

	nonce := params["nonce"]
	issued, valid := d.parseNonce(nonce)
	if !valid {
		return false, false, Identity{}
	}

	ha1 := md5Hex(username + ":" + realm + ":" + password)
	ha2 := md5Hex(r.Method + ":" + params["uri"])

	var expected string
	qop := params["qop"]
	if qop != "" {
		if qop != "auth" {
			return false, false, Identity{}
		}
		expected = md5Hex(strings.Join([]string{
			ha1, nonce, params["nc"], params["cnonce"], qop, ha2,
		}, ":"))
	} else {
		expected = md5Hex(ha1 + ":" + nonce + ":" + ha2)
	}
	if subtle.ConstantTimeCompare([]byte(expected), []byte(params["response"])) != 1 {
		return false, false, Identity{}
	}

	// Credentials are right. Expiry is decided only now so a wrong
	// password never learns whether its nonce was still fresh.
	if now.After(issued.Add(nonceTTL)) {
		return false, true, Identity{}
	}
	if !d.checkReplay(nonce, qop, params["nc"], issued.Add(nonceTTL), now) {
		return false, false, Identity{}
	}
	if perDevice && d.OnAuthenticated != nil {
		d.OnAuthenticated(username)
	}
	return true, false, Identity{Username: username, BoundDeviceID: deviceID}
}

// checkReplay records this (nonce, nc) use and reports whether it is
// new. Fails closed when the cache is full of unexpired entries.
func (d DigestAuthenticator) checkReplay(nonce, qop, ncHex string, expires, now time.Time) bool {
	nonceCache.Lock()
	defer nonceCache.Unlock()

	st, seen := nonceCache.m[nonce]
	if !seen {
		if len(nonceCache.m) >= nonceCacheMax {
			purgeExpiredNoncesLocked(now)
			if len(nonceCache.m) >= nonceCacheMax {
				return false
			}
		}
		st = &nonceState{expires: expires}
		nonceCache.m[nonce] = st
	}

	if qop == "" {
		if st.used {
			return false
		}
		st.used = true
		return true
	}
	nc, err := strconv.ParseUint(ncHex, 16, 64)
	if err != nil || nc == 0 || nc <= st.lastNC {
		return false
	}
	st.lastNC = nc
	return true
}

// nonceKey derives the nonce-signing key from the configured password
// so every ACS replica sharing the credential also accepts each other's
// nonces, with nothing extra to configure.
func (d DigestAuthenticator) nonceKey() []byte {
	secret := d.NonceSecret
	if len(secret) == 0 {
		secret = []byte(d.Password)
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte("acs-digest-nonce-v1"))
	return mac.Sum(nil)
}

func (d DigestAuthenticator) nonceMAC(ts, random string) string {
	mac := hmac.New(sha256.New, d.nonceKey())
	mac.Write([]byte(ts + "." + random))
	return hex.EncodeToString(mac.Sum(nil)[:16])
}

// newNonce issues "<unix-ts>.<random>.<hmac>": the random part keeps
// nonces unique, the HMAC proves this ACS issued it and that the
// timestamp is untampered.
func (d DigestAuthenticator) newNonce(now time.Time) string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	ts := strconv.FormatInt(now.Unix(), 10)
	random := hex.EncodeToString(b)
	return ts + "." + random + "." + d.nonceMAC(ts, random)
}

// parseNonce checks a nonce's authenticity and returns its issue time.
func (d DigestAuthenticator) parseNonce(nonce string) (issued time.Time, valid bool) {
	parts := strings.SplitN(nonce, ".", 3)
	if len(parts) != 3 {
		return time.Time{}, false
	}
	if !hmac.Equal([]byte(parts[2]), []byte(d.nonceMAC(parts[0], parts[1]))) {
		return time.Time{}, false
	}
	sec, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return time.Time{}, false
	}
	return time.Unix(sec, 0), true
}

func (d DigestAuthenticator) verifyBasic(encoded string) (bool, Identity) {
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encoded))
	if err != nil {
		return false, Identity{}
	}
	user, pass, ok := strings.Cut(string(decoded), ":")
	if !ok {
		return false, Identity{}
	}
	expected, deviceID, perDevice, found := d.passwordFor(user)
	if !found {
		return false, Identity{}
	}
	if subtle.ConstantTimeCompare([]byte(pass), []byte(expected)) != 1 {
		return false, Identity{}
	}
	if perDevice && d.OnAuthenticated != nil {
		d.OnAuthenticated(user)
	}
	return true, Identity{Username: user, BoundDeviceID: deviceID}
}

func md5Hex(s string) string {
	sum := md5.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

var digestParamRE = regexp.MustCompile(`(\w+)=("([^"]*)"|[^,]*)`)

func parseDigestParams(s string) map[string]string {
	out := map[string]string{}
	for _, m := range digestParamRE.FindAllStringSubmatch(s, -1) {
		key := m[1]
		val := m[2]
		if m[3] != "" || strings.HasPrefix(val, `"`) {
			val = m[3]
		}
		out[key] = strings.TrimSpace(val)
	}
	return out
}
