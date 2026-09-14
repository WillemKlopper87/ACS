// Package connreq implements the ACS-to-CPE Connection Request HTTP GET
// (design doc v3 §5.6 pseudocode / §12, build plan §4 Phase 3): a plain
// GET to the device's ConnectionRequestURL, retried once with HTTP Digest
// or Basic credentials if the CPE challenges with 401. A successful 2xx
// here only means the CPE accepted the wake-up request — it does not mean
// a new CWMP session has opened yet, so the caller still has to wait for
// a subsequent Inform.
package connreq

import (
	"context"
	"crypto/md5"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// Outcome categories mirror design doc v3 §12.3's status vocabulary.
const (
	// OutcomeHTTP200 is retained as the stable historical success value.
	// For CPE interoperability Attempt normalizes every successful 2xx
	// Connection Request response (including common 204 No Content) to
	// this value; callers should treat it as HTTP_SUCCESS, not literally
	// "the device sent status 200".
	OutcomeHTTP200     = "HTTP_200"
	OutcomeHTTP401     = "HTTP_401"
	OutcomeHTTP404     = "HTTP_404"
	OutcomeHTTPOther   = "HTTP_OTHER"
	OutcomeTimeout     = "TIMEOUT"
	OutcomeDNSFailure  = "DNS_FAILURE"
	OutcomeTCPFailure  = "TCP_FAILURE"
	OutcomeTLSFailure  = "TLS_FAILURE"
	OutcomeUnavailable = "UNAVAILABLE"
	// OutcomeBlockedByPolicy means ConnectionRequestURL resolved outside
	// the configured device network policy (audit H-2/P1.2) — distinct
	// from a real network failure so operators can tell "misconfigured
	// policy" apart from "device unreachable" in the fleet view. Set by
	// the caller (cmd/api/connreq_worker.go), not by Attempt itself —
	// see Attempt's doc comment for why the policy check lives there.
	OutcomeBlockedByPolicy = "BLOCKED_BY_POLICY"

	// Annex G UDP outcomes (annexg.go).
	OutcomeUDPSendFailed     = "UDP_SEND_FAILED"
	OutcomeUDPInformReceived = "UDP_SENT_INFORM_RECEIVED"
	OutcomeUDPNoInform       = "UDP_SENT_NO_INFORM"
)

// Attempt performs the Connection Request GET, retrying once with the
// strongest supported authentication challenge. Digest is preferred;
// Basic is accepted as an interoperability fallback for older CPEs.
// username may be empty (no credentials configured); in that case a 401
// is reported as-is rather than retried.
//
// client lets the caller enforce an outbound network policy (audit H-2/
// P1.2): ConnectionRequestURL is CPE-controlled, so a malicious or
// compromised device can point it at an internal service or the cloud
// metadata endpoint and use the ACS as a port scanner and an offline-
// crackable-digest oracle for the shared Connection Request credential.
// This package stays policy-agnostic on purpose (it has no concept of
// tenancy/device networks, and its own tests exercise real httptest
// servers on loopback, which a real device-network policy would always
// reject) — cmd/api/connreq_worker.go is what builds a client whose
// Transport enforces netguard.Policy.DialControl and whose CheckRedirect
// refuses to follow, the same pattern webgui_handlers.go's proxy uses.
// A nil client falls back to a plain one with no policy enforcement —
// callers that skip this argument are opting out, not getting it for
// free.
func Attempt(ctx context.Context, targetURL, username, password string, timeout time.Duration, client *http.Client) string {
	if targetURL == "" {
		return OutcomeUnavailable
	}
	if client == nil {
		client = &http.Client{Timeout: timeout}
	}

	resp, err := doGet(ctx, client, targetURL, "")
	if err != nil {
		return categorizeError(err)
	}
	outcome := drainAndCategorize(resp)

	if outcome != OutcomeHTTP401 || username == "" {
		return outcome
	}

	authHeader, ok := buildAuthorization(resp.Header.Values("WWW-Authenticate"), username, password, http.MethodGet, targetURL)
	if !ok {
		return OutcomeHTTP401
	}

	resp2, err := doGet(ctx, client, targetURL, authHeader)
	if err != nil {
		return categorizeError(err)
	}
	return drainAndCategorize(resp2)
}

func doGet(ctx context.Context, client *http.Client, targetURL, authHeader string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		return nil, err
	}
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	return client.Do(req)
}

func drainAndCategorize(resp *http.Response) string {
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return categorizeStatus(resp.StatusCode)
}

func categorizeStatus(code int) string {
	switch {
	case code >= 200 && code < 300:
		// TR-069 implementations in the field use both 200 and 204 for
		// accepted Connection Requests. Normalize all 2xx responses so a
		// harmless status-code variation does not force Annex G fallback.
		return OutcomeHTTP200
	case code == http.StatusUnauthorized:
		return OutcomeHTTP401
	case code == http.StatusNotFound:
		return OutcomeHTTP404
	default:
		return OutcomeHTTPOther
	}
}

func categorizeError(err error) string {
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return OutcomeTimeout
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return OutcomeDNSFailure
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		if opErr.Op == "dial" {
			return OutcomeTCPFailure
		}
	}
	lower := strings.ToLower(err.Error())
	if strings.Contains(lower, "tls") || strings.Contains(lower, "certificate") || strings.Contains(lower, "x509") {
		return OutcomeTLSFailure
	}
	return OutcomeTCPFailure
}

var challengeParamRE = regexp.MustCompile(`(\w+)=("([^"]*)"|[^,\s]*)`)

func parseChallengeParams(s string) map[string]string {
	out := map[string]string{}
	for _, m := range challengeParamRE.FindAllStringSubmatch(s, -1) {
		key := strings.ToLower(m[1])
		val := m[2]
		if strings.HasPrefix(val, `"`) {
			val = m[3]
		}
		out[key] = strings.TrimSpace(val)
	}
	return out
}

func authSchemeAndRest(challenge string) (scheme, rest string, ok bool) {
	challenge = strings.TrimSpace(challenge)
	idx := strings.IndexByte(challenge, ' ')
	if idx <= 0 {
		return "", "", false
	}
	return strings.ToLower(challenge[:idx]), strings.TrimSpace(challenge[idx+1:]), true
}

// buildAuthorization chooses the strongest challenge this client can
// answer. Multiple WWW-Authenticate header fields are common; Digest is
// preferred over Basic regardless of header order, and among Digest
// challenges SHA-256 is preferred over MD5.
//
// RFC 7616 §3.7 has the client take the first algorithm it supports from
// the server's own preference order. We deliberately pick the strongest
// instead: a CPE that offers both has told us it can verify either, so
// there is no interop risk in the choice, and MD5 is only still here for
// devices that offer nothing else.
func buildAuthorization(challenges []string, username, password, method, targetURL string) (string, bool) {
	for _, family := range []string{"sha-256", "md5"} {
		for _, challenge := range challenges {
			scheme, rest, ok := authSchemeAndRest(challenge)
			if !ok || scheme != "digest" {
				continue
			}
			if algorithmFamily(parseChallengeParams(rest)["algorithm"]) != family {
				continue
			}
			if header, ok := buildDigestAuthorization(challenge, username, password, method, targetURL); ok {
				return header, true
			}
		}
	}
	for _, challenge := range challenges {
		if scheme, _, ok := authSchemeAndRest(challenge); ok && scheme == "basic" {
			return "Basic " + base64.StdEncoding.EncodeToString([]byte(username+":"+password)), true
		}
	}
	return "", false
}

// buildDigestAuthorization computes an HTTP Digest Authorization header
// from a WWW-Authenticate challenge. It accepts legacy RFC-2069 style
// challenges without qop, qop lists containing auth, and the MD5,
// MD5-sess, SHA-256 and SHA-256-sess algorithms. auth-int is deliberately
// not selected because Connection Request is a GET with no entity and
// many embedded HTTP stacks implement only qop=auth correctly.
//
// SHA-256 (RFC 7616) is not optional in practice: a Huawei N5368X with
// "Connection request Authentication: Digest-SHA256" challenges with
// nothing else, so an MD5-only client could never wake it — every
// ACS-initiated job would sit QUEUED until the device's next periodic
// Inform. SHA-512-256 is not implemented: no observed CPE offers it, and
// guessing at an untested algorithm is worse than falling through to the
// Basic fallback. The two -sess variants are supported here, unlike in
// the CWMP server (internal/auth), because this side computes HA1 itself
// and so can key it per-session without any stored state.
func buildDigestAuthorization(challenge, username, password, method, targetURL string) (header string, ok bool) {
	scheme, rest, ok := authSchemeAndRest(challenge)
	if !ok || scheme != "digest" {
		return "", false
	}
	params := parseChallengeParams(rest)
	realm, nonce := params["realm"], params["nonce"]
	if realm == "" || nonce == "" {
		return "", false
	}

	u, err := url.Parse(targetURL)
	if err != nil {
		return "", false
	}
	uri := u.RequestURI()

	qop := ""
	if offered := params["qop"]; offered != "" {
		for _, candidate := range strings.Split(offered, ",") {
			if strings.EqualFold(strings.TrimSpace(candidate), "auth") {
				qop = "auth"
				break
			}
		}
		if qop == "" {
			return "", false
		}
	}

	hashHex, sess, supported := hashForAlgorithm(params["algorithm"])
	if !supported {
		return "", false
	}

	nc := "00000001"
	cnonce := newCnonce()
	ha1 := hashHex(username + ":" + realm + ":" + password)
	if sess {
		ha1 = hashHex(ha1 + ":" + nonce + ":" + cnonce)
	}
	ha2 := hashHex(method + ":" + uri)

	var response string
	if qop != "" {
		response = hashHex(strings.Join([]string{ha1, nonce, nc, cnonce, qop, ha2}, ":"))
	} else {
		response = hashHex(ha1 + ":" + nonce + ":" + ha2)
	}

	var sb strings.Builder
	sb.WriteString(`Digest username="` + escapeDigestValue(username) + `"`)
	sb.WriteString(`, realm="` + escapeDigestValue(realm) + `"`)
	sb.WriteString(`, nonce="` + escapeDigestValue(nonce) + `"`)
	sb.WriteString(`, uri="` + escapeDigestValue(uri) + `"`)
	sb.WriteString(`, response="` + response + `"`)
	if qop != "" {
		sb.WriteString(`, qop=` + qop)
		sb.WriteString(`, nc=` + nc)
		sb.WriteString(`, cnonce="` + cnonce + `"`)
	} else if sess {
		// The -sess variants need cnonce for HA1 even when the server uses
		// legacy no-qop digest semantics.
		sb.WriteString(`, cnonce="` + cnonce + `"`)
	}
	if alg := params["algorithm"]; alg != "" {
		sb.WriteString(`, algorithm=` + alg)
	}
	if opaque := params["opaque"]; opaque != "" {
		sb.WriteString(`, opaque="` + escapeDigestValue(opaque) + `"`)
	}
	return sb.String(), true
}

func escapeDigestValue(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	return strings.ReplaceAll(s, `"`, `\"`)
}

// algorithmFamily reduces a challenge's algorithm token to the hash it
// selects, ignoring the -sess suffix (which changes how HA1 is derived,
// not which hash does the deriving). An absent algorithm means MD5
// (RFC 2617). Unsupported tokens return themselves, so they simply match
// none of the families buildAuthorization asks for.
func algorithmFamily(algorithm string) string {
	normalized := strings.ToLower(strings.TrimSpace(algorithm))
	if normalized == "" {
		return "md5"
	}
	return strings.TrimSuffix(normalized, "-sess")
}

// hashForAlgorithm maps a challenge's algorithm token to the hash it
// asks for and whether it is a -sess variant. ok=false for anything this
// client cannot answer, so the caller falls through to another challenge
// (or to Basic) rather than sending a response computed with the wrong
// hash, which a CPE would reject as a bad password.
func hashForAlgorithm(algorithm string) (fn func(string) string, sess, ok bool) {
	normalized := strings.ToLower(strings.TrimSpace(algorithm))
	sess = strings.HasSuffix(normalized, "-sess")
	switch algorithmFamily(normalized) {
	case "md5":
		return md5Hex, sess, true
	case "sha-256":
		return sha256Hex, sess, true
	default:
		return nil, false, false
	}
}

func md5Hex(s string) string {
	sum := md5.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func newCnonce() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
