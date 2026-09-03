package cwmp

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

// DefaultCWMPNamespace is the CWMP namespace used for ACS-initiated RPCs
// when no session-negotiated namespace is available. Responses to
// CPE-initiated RPCs should echo the namespace version the CPE used — see
// DetectCWMPNamespace.
const DefaultCWMPNamespace = "urn:dslforum-org:cwmp-1-0"

// soapEnvelopeTemplate wraps a single RPC body element in the standard
// CWMP SOAP envelope. %[1]s is the cwmp:ID header value, %[2]s is the
// already-rendered body element (e.g. <cwmp:InformResponse>...), %[3]s is
// the CWMP namespace URN (cwmp-1-0 .. cwmp-1-4 and compatible later
// dslforum cwmp-1-N revisions).
//
// Outbound envelopes are hand-rendered rather than built via encoding/xml
// because Go's Marshal gives no control over which namespace prefix
// (cwmp:, soap-env:) is used, and CWMP implementations in the wild are
// picky about exactly this — hand-rendering keeps the wire format fixed
// and predictable.
const soapEnvelopeTemplate = `<?xml version="1.0" encoding="UTF-8"?>
<soap-env:Envelope xmlns:soap-env="http://schemas.xmlsoap.org/soap/envelope/" xmlns:soap-enc="http://schemas.xmlsoap.org/soap/encoding/" xmlns:xsd="http://www.w3.org/2001/XMLSchema" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" xmlns:cwmp="%[3]s">
  <soap-env:Header>
    <cwmp:ID soap-env:mustUnderstand="1">%[1]s</cwmp:ID>
  </soap-env:Header>
  <soap-env:Body>
    %[2]s
  </soap-env:Body>
</soap-env:Envelope>`

// NewID generates an ACS-side cwmp:ID for correlating a request/response
// pair in logs. Not a CommandKey — CommandKey is job-level (v3 §7.4), this
// is per-envelope.
func NewID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return "acs-" + hex.EncodeToString(b)
}

// renderEnvelope wraps a body element with the given cwmp:ID, in the
// default (ACS-initiated) namespace version.
func renderEnvelope(id, body string) []byte {
	return renderEnvelopeNS(id, body, DefaultCWMPNamespace)
}

// renderEnvelopeNS wraps a body element with the given cwmp:ID and CWMP
// namespace URN — used for responses to CPE-initiated RPCs, which must
// speak the same namespace version as the request. The id is escaped
// because responses echo the CPE-supplied cwmp:ID verbatim (TR-069
// §3.4.1.1), and a CPE-chosen ID must not be able to break or inject
// into our XML.
func renderEnvelopeNS(id, body, ns string) []byte {
	if !validCWMPNamespace(ns) {
		ns = DefaultCWMPNamespace
	}
	return []byte(fmt.Sprintf(soapEnvelopeTemplate, escapeXML(id), body, ns))
}

// RewriteCWMPNamespace rewrites an ACS-rendered envelope to the namespace
// negotiated by the CPE for the current session. The individual RPC
// renderers intentionally keep their simple historical API and render in
// DefaultCWMPNamespace; cmd/acs applies this function immediately before
// sending the RPC. Only a syntactically valid dslforum CWMP namespace is
// accepted, so a persisted/corrupted string cannot inject XML.
func RewriteCWMPNamespace(envelope []byte, ns string) []byte {
	if len(envelope) == 0 || ns == "" || ns == DefaultCWMPNamespace || !validCWMPNamespace(ns) {
		return envelope
	}
	oldDecl := []byte(`xmlns:cwmp="` + DefaultCWMPNamespace + `"`)
	newDecl := []byte(`xmlns:cwmp="` + ns + `"`)
	return bytes.Replace(envelope, oldDecl, newDecl, 1)
}

func validCWMPNamespace(ns string) bool {
	m := cwmpNamespaceRE.FindString(ns)
	return m != "" && m == ns
}

// RenderEmptyResponse is retained for callers/tests that want the body
// bytes of a session-closing response. The HTTP status (204 by default)
// is owned by cmd/acs; the CWMP payload itself is always empty.
func RenderEmptyResponse() []byte {
	return nil
}
