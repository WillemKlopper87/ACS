// Package auth implements authentication for both CWMP and REST traffic.
// This file: CPE-to-ACS Digest auth for the CWMP endpoint (v3 §2.5/§11.2
// credential class 1 — mTLS is the eventual target, Digest is the
// fallback, unverified per-vendor until prerequisite P3 is resolved).
// jwt.go: operator JWT auth for cmd/api (v3 §11.3 credential class 4),
// added Phase 6.
package auth

import (
	"context"
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

// Nonce field sizes. See newNonce for why they are this small.
const (
	nonceRandomBytes = 6 // 12 hex characters
	nonceMACBytes    = 8 // 16 hex characters
)

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
	// OnAuthenticated is a caller-owned lifecycle hook for a verified
	// per-device credential. Verify deliberately does NOT invoke it: HTTP
	// proof alone is insufficient to activate a PENDING device credential.
	// cmd/acs invokes the hook only after the Inform's claimed natural
	// identity resolves to the same BoundDeviceID returned by Verify.
	OnAuthenticated func(username string)
	// Algorithms lists the Digest algorithms to challenge for, in the
	// order offered (one WWW-Authenticate line each). Empty means the
	// default, {"MD5", "SHA-256"} — MD5 first so an RFC 2617 CPE takes
	// the first line it understands.
	//
	// It exists because some embedded CPE HTTP stacks parse the 401
	// poorly: a Huawei N5368X (HW_ATP_HTTP) re-POSTs its Inform
	// unauthenticated rather than answering a challenge it cannot make
	// sense of, and a multi-line challenge is one of the things that can
	// provoke that. ACS_DIGEST_ALGORITHMS=MD5 narrows the 401 to a
	// single, minimal line for such a device without giving up SHA-256
	// for the rest of the fleet.
	Algorithms []string

	// NonceSecret keys the nonce HMAC. When empty the shared Password is
	// used, which is fine for the common single-credential fleet; set it
	// explicitly when the shared credential is absent (per-device only)
	// so nonces are still unforgeable.
	NonceSecret []byte

	// ReplayStore, when set, records nonce/nc use in shared storage
	// instead of this process's own memory (audit P1.6). nil (the
	// default) is correct and sufficient for exactly one cmd/acs
	// replica; behind a load balancer or across a restart, a captured
	// Authorization header would otherwise replay cleanly against any
	// replica that hadn't seen it yet. See ReplayStore's doc comment for
	// the chosen architecture.
	ReplayStore ReplayStore
}

// ReplayStore is the pluggable backend for cross-replica Digest replay
// protection (audit P1.6). The architecture chosen here is shared
// storage (a Postgres table, internal/auth's PostgresReplayStore) rather
// than a cryptographic/stateless scheme or sticky routing: this
// codebase already treats Postgres as the source of truth for every
// other piece of shared CWMP state (sessions, job leases, migrations),
// cmd/acs already holds a live *sql.DB, and unlike sticky routing it
// keeps working correctly through a rolling deploy or an unplanned
// replica restart.
type ReplayStore interface {
	// CheckAndRecord atomically records this (nonce, nc) pair's use and
	// reports whether it is new (ok=true, not yet seen anywhere) or a
	// replay (ok=false). ncOrdinal is the parsed qop=auth nc value, or 1
	// for a legacy non-qop response (single-use — modeled as "nc must
	// strictly increase past 1", which a second use of the same
	// zero-nc response satisfies as still <= 1 and is correctly
	// rejected the same way a repeated qop=auth nc would be).
	// Implementations must fail closed: an error is treated as ok=false.
	CheckAndRecord(ctx context.Context, nonce string, ncOrdinal uint64, expires time.Time) (ok bool, err error)
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

// defaultChallengeAlgorithms is what a CPE is offered when Algorithms is
// unset: MD5 first (RFC 2617 CPEs take the first line they understand),
// then SHA-256.
var defaultChallengeAlgorithms = []string{"MD5", "SHA-256"}

// challengeAlgorithms is Algorithms filtered to what verifyDigest can
// actually verify — a challenge we could not answer ourselves would just
// waste a CPE's retry — falling back to the default when that leaves
// nothing.
func (d DigestAuthenticator) challengeAlgorithms() []string {
	if len(d.Algorithms) == 0 {
		return defaultChallengeAlgorithms
	}
	out := make([]string, 0, len(d.Algorithms))
	for _, algorithm := range d.Algorithms {
		if _, ok := hashFuncFor(algorithm); ok && algorithm != "" {
			out = append(out, algorithm)
		}
	}
	if len(out) == 0 {
		return defaultChallengeAlgorithms
	}
	return out
}

func (d DigestAuthenticator) challenge(w http.ResponseWriter, stale bool) {
	// Same nonce is safe to reuse across both challenge lines: nonceMAC
	// is independent of the algorithm the CPE eventually picks, and
	// verifyDigest re-derives the algorithm from the Authorization
	// header it gets back, not from which challenge line matched.
	nonce := d.newNonce(time.Now())
	staleSuffix := ""
	if stale {
		staleSuffix = `, stale=true`
	}
	// One Digest challenge line per algorithm (RFC 7616 §3.3): a CPE that
	// only implements SHA-256 (observed: Huawei cwmpmng logs "Digest
	// challenge can't find all element" against an MD5-only challenge and
	// silently gives up rather than falling back) picks the line it
	// understands; MD5-only CPEs keep working unchanged. See Algorithms
	// for narrowing this to one line for a CPE that cannot cope.
	w.Header().Del("WWW-Authenticate")
	for _, algorithm := range d.challengeAlgorithms() {
		w.Header().Add("WWW-Authenticate", fmt.Sprintf(`Digest realm="%s", qop="auth", nonce="%s", algorithm=%s%s`, realm, nonce, algorithm, staleSuffix))
	}
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
	password, deviceID, _, found := d.passwordFor(username)
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

	// RFC 7616: algorithm defaults to MD5 when absent (RFC 2617 CPEs
	// never send it). "-sess" variants aren't supported — no observed
	// CPE here has needed them, and they'd need ha1 keyed per-session.
	hashHex, algOK := hashFuncFor(params["algorithm"])
	if !algOK {
		return false, false, Identity{}
	}

	ha1 := hashHex(username + ":" + realm + ":" + password)
	ha2 := hashHex(r.Method + ":" + params["uri"])

	var expected string
	qop := params["qop"]
	if qop != "" {
		if qop != "auth" {
			return false, false, Identity{}
		}
		expected = hashHex(strings.Join([]string{
			ha1, nonce, params["nc"], params["cnonce"], qop, ha2,
		}, ":"))
	} else {
		expected = hashHex(ha1 + ":" + nonce + ":" + ha2)
	}
	if subtle.ConstantTimeCompare([]byte(expected), []byte(params["response"])) != 1 {
		return false, false, Identity{}
	}

	// Credentials are right. Expiry is decided only now so a wrong
	// password never learns whether its nonce was still fresh.
	if now.After(issued.Add(nonceTTL)) {
		return false, true, Identity{}
	}
	if !d.checkReplay(r.Context(), nonce, qop, params["nc"], issued.Add(nonceTTL), now) {
		return false, false, Identity{}
	}
	return true, false, Identity{Username: username, BoundDeviceID: deviceID}
}

// checkReplay records this (nonce, nc) use and reports whether it is
// new. Delegates to ReplayStore when set (audit P1.6, cross-replica);
// otherwise falls back to this process's own in-memory cache, fine for
// exactly one cmd/acs replica. Both paths fail closed.
func (d DigestAuthenticator) checkReplay(ctx context.Context, nonce, qop, ncHex string, expires, now time.Time) bool {
	ncOrdinal := uint64(1) // legacy non-qop: single-use, modeled as "nc must exceed 1"
	if qop != "" {
		nc, err := strconv.ParseUint(ncHex, 16, 64)
		if err != nil || nc == 0 {
			return false
		}
		ncOrdinal = nc
	}

	if d.ReplayStore != nil {
		ok, err := d.ReplayStore.CheckAndRecord(ctx, nonce, ncOrdinal, expires)
		if err != nil {
			return false
		}
		return ok
	}

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
	if ncOrdinal <= st.lastNC {
		return false
	}
	st.lastNC = ncOrdinal
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
	return hex.EncodeToString(mac.Sum(nil)[:nonceMACBytes])
}

// newNonce issues "<hex-unix-ts>.<random>.<hmac>": the random part keeps
// nonces unique, the HMAC proves this ACS issued it and that the
// timestamp is untampered.
//
// Every field is kept as short as it can be while still doing its job,
// because CPE HTTP stacks are where this string has to survive. The
// earlier decimal-timestamp/12-byte-random/16-byte-MAC form was 68
// characters, past the 64-byte nonce buffer several embedded clients
// allocate, and a client that cannot store the nonce cannot answer the
// challenge at all — it just retries unauthenticated, which is exactly
// what a Huawei N5368X was observed doing. 38 characters clears that
// comfortably. What is given up is margin, not security: 48 bits of
// randomness inside a 10-minute window with a 64-bit MAC binding the
// timestamp still leaves forgery and collision far out of reach, and the
// MAC is what actually makes a nonce unforgeable.
//
// Nonces issued by an older build stop verifying across this change, so
// any challenge outstanding at the moment of a restart is answered with
// stale=true and the CPE simply retries. That is the same path an
// expired nonce already takes.
func (d DigestAuthenticator) newNonce(now time.Time) string {
	b := make([]byte, nonceRandomBytes)
	_, _ = rand.Read(b)
	ts := strconv.FormatInt(now.Unix(), 16)
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
	sec, err := strconv.ParseInt(parts[0], 16, 64)
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
	expected, deviceID, _, found := d.passwordFor(user)
	if !found {
		return false, Identity{}
	}
	if subtle.ConstantTimeCompare([]byte(pass), []byte(expected)) != 1 {
		return false, Identity{}
	}
	return true, Identity{Username: user, BoundDeviceID: deviceID}
}

func md5Hex(s string) string {
	sum := md5.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// hashFuncFor maps a Digest response's algorithm field to the hash it
// asserts was used, matching one of the two challenge lines challenge()
// sends. Empty (RFC 2617, no field sent) and explicit "MD5" both mean
// MD5. ok=false for anything else — including "-sess" variants — so the
// caller rejects rather than silently verifying against the wrong hash.
func hashFuncFor(algorithm string) (fn func(string) string, ok bool) {
	switch strings.ToUpper(algorithm) {
	case "", "MD5":
		return md5Hex, true
	case "SHA-256":
		return sha256Hex, true
	default:
		return nil, false
	}
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
