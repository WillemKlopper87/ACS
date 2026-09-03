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
// preferred over Basic regardless of header order.
func buildAuthorization(challenges []string, username, password, method, targetURL string) (string, bool) {
	for _, challenge := range challenges {
		if scheme, _, ok := authSchemeAndRest(challenge); ok && scheme == "digest" {
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
// challenges without qop, qop lists containing auth, and both MD5 and
// MD5-sess. auth-int is deliberately not selected because Connection
// Request is a GET with no entity and many embedded HTTP stacks implement
// only qop=auth correctly.
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

	algorithm := strings.ToLower(strings.TrimSpace(params["algorithm"]))
	if algorithm == "" {
		algorithm = "md5"
	}
	if algorithm != "md5" && algorithm != "md5-sess" {
		return "", false
	}

	nc := "00000001"
	cnonce := newCnonce()
	ha1 := md5Hex(username + ":" + realm + ":" + password)
	if algorithm == "md5-sess" {
		ha1 = md5Hex(ha1 + ":" + nonce + ":" + cnonce)
	}
	ha2 := md5Hex(method + ":" + uri)

	var response string
	if qop != "" {
		response = md5Hex(strings.Join([]string{ha1, nonce, nc, cnonce, qop, ha2}, ":"))
	} else {
		response = md5Hex(ha1 + ":" + nonce + ":" + ha2)
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
	} else if algorithm == "md5-sess" {
		// MD5-sess needs cnonce for HA1 even when the server uses legacy
		// no-qop digest semantics.
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

func md5Hex(s string) string {
	sum := md5.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

func newCnonce() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
