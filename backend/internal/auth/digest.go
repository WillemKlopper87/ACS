// Package auth implements authentication for both CWMP and REST traffic.
// This file: CPE-to-ACS Digest auth for the CWMP endpoint (v3 §2.5/§11.2
// credential class 1 — mTLS is the eventual target, Digest is the
// fallback, unverified per-vendor until prerequisite P3 is resolved).
// jwt.go: operator JWT auth for cmd/api (v3 §11.3 credential class 4),
// added Phase 6.
package auth

import (
	"crypto/md5"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"regexp"
	"strings"
)

const realm = "acs"

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
}

// Enabled reports whether a credential has been configured. When it
// hasn't, the caller should let requests through unauthenticated but log
// that fact loudly — this is a lab harness, not production (v3 §11.1:
// "No plaintext CWMP in production").
func (d DigestAuthenticator) Enabled() bool {
	return d.Username != "" && d.Password != ""
}

// Challenge sends a 401 with a fresh Digest challenge (and, when
// AllowBasic is set, a Basic challenge as well — the CPE picks whichever
// scheme it implements).
func (d DigestAuthenticator) Challenge(w http.ResponseWriter) {
	nonce := newNonce()
	w.Header().Set("WWW-Authenticate", fmt.Sprintf(
		`Digest realm="%s", qop="auth", nonce="%s", algorithm=MD5`, realm, nonce))
	if d.AllowBasic {
		w.Header().Add("WWW-Authenticate", fmt.Sprintf(`Basic realm="%s"`, realm))
	}
	w.WriteHeader(http.StatusUnauthorized)
}

// Verify checks the Authorization header of an incoming CWMP request. It
// returns ok=false with no side effects if the header is missing or
// malformed — callers should respond with Challenge in that case.
func (d DigestAuthenticator) Verify(r *http.Request) bool {
	header := r.Header.Get("Authorization")
	switch {
	case strings.HasPrefix(header, "Digest "):
		return d.verifyDigest(r, header[len("Digest "):])
	case d.AllowBasic && strings.HasPrefix(header, "Basic "):
		return d.verifyBasic(header[len("Basic "):])
	default:
		return false
	}
}

func (d DigestAuthenticator) verifyDigest(r *http.Request, rest string) bool {
	params := parseDigestParams(rest)
	if params["username"] != d.Username {
		return false
	}

	ha1 := md5Hex(d.Username + ":" + realm + ":" + d.Password)
	ha2 := md5Hex(r.Method + ":" + params["uri"])

	var expected string
	if qop := params["qop"]; qop != "" {
		expected = md5Hex(strings.Join([]string{
			ha1, params["nonce"], params["nc"], params["cnonce"], qop, ha2,
		}, ":"))
	} else {
		expected = md5Hex(ha1 + ":" + params["nonce"] + ":" + ha2)
	}

	return subtle.ConstantTimeCompare([]byte(expected), []byte(params["response"])) == 1
}

func (d DigestAuthenticator) verifyBasic(encoded string) bool {
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encoded))
	if err != nil {
		return false
	}
	user, pass, ok := strings.Cut(string(decoded), ":")
	if !ok {
		return false
	}
	userOK := subtle.ConstantTimeCompare([]byte(user), []byte(d.Username)) == 1
	passOK := subtle.ConstantTimeCompare([]byte(pass), []byte(d.Password)) == 1
	return userOK && passOK
}

func md5Hex(s string) string {
	sum := md5.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

func newNonce() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
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
