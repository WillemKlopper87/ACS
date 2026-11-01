package xmpp

import (
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"hash"
	"strconv"
	"strings"
)

// SASL mechanisms in preference order. PLAIN is only ever offered by this
// client inside TLS (the session refuses to authenticate on a plaintext
// stream at all), and is kept because some embedded servers and older
// Prosody storage backends offer nothing else.
const (
	mechSCRAMSHA256 = "SCRAM-SHA-256"
	mechSCRAMSHA1   = "SCRAM-SHA-1"
	mechPLAIN       = "PLAIN"
)

// chooseMechanism picks the strongest mechanism the server offers that this
// client implements, or "" when there is none.
func chooseMechanism(offered []string) string {
	have := map[string]bool{}
	for _, m := range offered {
		have[strings.ToUpper(strings.TrimSpace(m))] = true
	}
	for _, m := range []string{mechSCRAMSHA256, mechSCRAMSHA1, mechPLAIN} {
		if have[m] {
			return m
		}
	}
	return ""
}

// plainInitial renders the RFC 4616 PLAIN initial response (no authzid).
func plainInitial(username, password string) []byte {
	return []byte("\x00" + username + "\x00" + password)
}

// scramClient is one RFC 5802 SCRAM exchange (no channel binding: "n,,").
type scramClient struct {
	newHash         func() hash.Hash
	username        string
	password        string
	clientNonce     string
	clientFirstBare string
	serverSignature []byte
}

func newSCRAM(mech, username, password string) (*scramClient, error) {
	var h func() hash.Hash
	switch mech {
	case mechSCRAMSHA256:
		h = sha256.New
	case mechSCRAMSHA1:
		h = sha1.New
	default:
		return nil, fmt.Errorf("unsupported SCRAM mechanism %q", mech)
	}
	nonce := make([]byte, 18)
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return &scramClient{newHash: h, username: username, password: password, clientNonce: base64.RawStdEncoding.EncodeToString(nonce)}, nil
}

// scramEscape applies RFC 5802 §5.1 saslname escaping.
func scramEscape(s string) string {
	return strings.NewReplacer("=", "=3D", ",", "=2C").Replace(s)
}

// first returns client-first-message.
func (s *scramClient) first() []byte {
	s.clientFirstBare = "n=" + scramEscape(s.username) + ",r=" + s.clientNonce
	return []byte("n,," + s.clientFirstBare)
}

// scramIterationLimits bound what a server may demand: fewer than 1000
// iterations would be a downgrade RFC 5802 warns about, and an absurd
// count would let a hostile server burn the ACS's CPU on every reconnect.
const (
	scramMinIterations = 1000
	scramMaxIterations = 1 << 20
)

// final consumes server-first-message and returns client-final-message.
func (s *scramClient) final(serverFirst []byte) ([]byte, error) {
	attrs := parseSCRAMAttrs(string(serverFirst))
	nonce, saltB64, iterStr := attrs["r"], attrs["s"], attrs["i"]
	if nonce == "" || saltB64 == "" || iterStr == "" {
		return nil, errors.New("scram: malformed server-first-message")
	}
	if !strings.HasPrefix(nonce, s.clientNonce) || len(nonce) == len(s.clientNonce) {
		return nil, errors.New("scram: server nonce does not extend the client nonce")
	}
	salt, err := base64.StdEncoding.DecodeString(saltB64)
	if err != nil {
		return nil, fmt.Errorf("scram: bad salt: %w", err)
	}
	iterations, err := strconv.Atoi(iterStr)
	if err != nil || iterations < scramMinIterations || iterations > scramMaxIterations {
		return nil, fmt.Errorf("scram: unacceptable iteration count %q", iterStr)
	}

	keyLen := s.newHash().Size()
	salted, err := pbkdf2.Key(s.newHash, s.password, salt, iterations, keyLen)
	if err != nil {
		return nil, fmt.Errorf("scram: derive key: %w", err)
	}
	clientKey := s.hmac(salted, "Client Key")
	storedKey := s.sum(clientKey)
	withoutProof := "c=biws,r=" + nonce // biws = base64("n,,")
	authMessage := s.clientFirstBare + "," + string(serverFirst) + "," + withoutProof
	clientSignature := s.hmac(storedKey, authMessage)
	proof := make([]byte, len(clientKey))
	subtle.XORBytes(proof, clientKey, clientSignature)
	serverKey := s.hmac(salted, "Server Key")
	s.serverSignature = s.hmac(serverKey, authMessage)
	return []byte(withoutProof + ",p=" + base64.StdEncoding.EncodeToString(proof)), nil
}

// verify checks server-final-message — mutual authentication: a server
// that does not know the password cannot produce the signature, so a
// man-in-the-middle that got past TLS still cannot impersonate it.
func (s *scramClient) verify(serverFinal []byte) error {
	attrs := parseSCRAMAttrs(string(serverFinal))
	if e := attrs["e"]; e != "" {
		return fmt.Errorf("scram: server error %q", e)
	}
	got, err := base64.StdEncoding.DecodeString(attrs["v"])
	if err != nil || len(s.serverSignature) == 0 || !hmac.Equal(got, s.serverSignature) {
		return errors.New("scram: server signature mismatch")
	}
	return nil
}

func (s *scramClient) hmac(key []byte, msg string) []byte {
	m := hmac.New(s.newHash, key)
	m.Write([]byte(msg))
	return m.Sum(nil)
}

func (s *scramClient) sum(b []byte) []byte {
	h := s.newHash()
	h.Write(b)
	return h.Sum(nil)
}

func parseSCRAMAttrs(msg string) map[string]string {
	out := map[string]string{}
	for _, part := range strings.Split(msg, ",") {
		if len(part) >= 2 && part[1] == '=' {
			out[part[:1]] = part[2:]
		}
	}
	return out
}
