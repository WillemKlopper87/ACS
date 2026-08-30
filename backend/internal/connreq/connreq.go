// Package connreq implements the ACS-to-CPE Connection Request HTTP GET
// (design doc v3 §5.6 pseudocode / §12, build plan §4 Phase 3): a plain
// GET to the device's ConnectionRequestURL, retried once with HTTP Digest
// credentials if the CPE challenges with 401. A 200 here only means the
// CPE accepted the wake-up request — it does not mean a new CWMP session
// has opened yet (v3 §5.6: "does not necessarily mean the CPE session has
// opened"), so the caller still has to wait for a subsequent Inform.
package connreq

import (
	"context"
	"crypto/md5"
	"crypto/rand"
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
	OutcomeHTTP200     = "HTTP_200"
	OutcomeHTTP401     = "HTTP_401"
	OutcomeHTTP404     = "HTTP_404"
	OutcomeHTTPOther   = "HTTP_OTHER"
	OutcomeTimeout     = "TIMEOUT"
	OutcomeDNSFailure  = "DNS_FAILURE"
	OutcomeTCPFailure  = "TCP_FAILURE"
	OutcomeTLSFailure  = "TLS_FAILURE"
	OutcomeUnavailable = "UNAVAILABLE"

	// Annex G UDP outcomes (annexg.go).
	OutcomeUDPSendFailed     = "UDP_SEND_FAILED"
	OutcomeUDPInformReceived = "UDP_SENT_INFORM_RECEIVED"
	OutcomeUDPNoInform       = "UDP_SENT_NO_INFORM"
)

// Attempt performs the Connection Request GET, retrying once with Digest
// auth on a 401 challenge. username may be empty (no credentials
// configured); in that case a 401 is reported as-is rather than retried.
func Attempt(ctx context.Context, targetURL, username, password string, timeout time.Duration) string {
	if targetURL == "" {
		return OutcomeUnavailable
	}

	client := &http.Client{Timeout: timeout}

	resp, err := doGet(ctx, client, targetURL, "")
	if err != nil {
		return categorizeError(err)
	}
	outcome := drainAndCategorize(resp)

	if outcome != OutcomeHTTP401 || username == "" {
		return outcome
	}

	challenge := resp.Header.Get("WWW-Authenticate")
	authHeader, ok := buildDigestAuthorization(challenge, username, password, http.MethodGet, targetURL)
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
	switch code {
	case http.StatusOK:
		return OutcomeHTTP200
	case http.StatusUnauthorized:
		return OutcomeHTTP401
	case http.StatusNotFound:
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
	if strings.Contains(err.Error(), "tls") || strings.Contains(err.Error(), "certificate") || strings.Contains(err.Error(), "x509") {
		return OutcomeTLSFailure
	}
	return OutcomeTCPFailure
}

var challengeParamRE = regexp.MustCompile(`(\w+)=("([^"]*)"|[^,\s]*)`)

func parseChallengeParams(s string) map[string]string {
	out := map[string]string{}
	for _, m := range challengeParamRE.FindAllStringSubmatch(s, -1) {
		key := m[1]
		val := m[2]
		if strings.HasPrefix(val, `"`) {
			val = m[3]
		}
		out[key] = strings.TrimSpace(val)
	}
	return out
}

// buildDigestAuthorization computes an RFC 2617 Digest Authorization
// header value from a WWW-Authenticate challenge. Only qop=auth is
// supported (matches internal/auth's server-side implementation); ok is
// false if the challenge isn't a Digest challenge this can answer.
func buildDigestAuthorization(challenge, username, password, method, targetURL string) (header string, ok bool) {
	if !strings.HasPrefix(challenge, "Digest ") {
		return "", false
	}
	params := parseChallengeParams(challenge[len("Digest "):])
	realm, nonce := params["realm"], params["nonce"]
	if realm == "" || nonce == "" {
		return "", false
	}

	u, err := url.Parse(targetURL)
	if err != nil {
		return "", false
	}
	uri := u.RequestURI()

	ha1 := md5Hex(username + ":" + realm + ":" + password)
	ha2 := md5Hex(method + ":" + uri)

	qop := params["qop"]
	nc := "00000001"
	cnonce := newCnonce()

	var response string
	if qop != "" {
		response = md5Hex(strings.Join([]string{ha1, nonce, nc, cnonce, qop, ha2}, ":"))
	} else {
		response = md5Hex(ha1 + ":" + nonce + ":" + ha2)
	}

	var sb strings.Builder
	sb.WriteString(`Digest username="` + username + `"`)
	sb.WriteString(`, realm="` + realm + `"`)
	sb.WriteString(`, nonce="` + nonce + `"`)
	sb.WriteString(`, uri="` + uri + `"`)
	sb.WriteString(`, response="` + response + `"`)
	if qop != "" {
		sb.WriteString(`, qop=` + qop)
		sb.WriteString(`, nc=` + nc)
		sb.WriteString(`, cnonce="` + cnonce + `"`)
	}
	if alg := params["algorithm"]; alg != "" {
		sb.WriteString(`, algorithm=` + alg)
	}
	return sb.String(), true
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
