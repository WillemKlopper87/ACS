// Package xmpp is a deliberately small XMPP (RFC 6120) client for one job:
// the ACS side of TR-069 Annex K "XMPP Connection Request". A CPE behind
// carrier CGNAT (the Nokia FastMile 5G Gateway 2 on the 2026-10-30 field
// test advertised ConnectionRequestURL http://10.10.178.14:7547/ and has no
// STUN) can never be dialled, but it can hold an outbound XMPP session
// open; the ACS then sends it an iq carrying the Connection Request
// credentials and the CPE opens a CWMP session with "6 CONNECTION REQUEST".
//
// Only the subset that job needs is implemented: STARTTLS (TLS is
// mandatory), SASL SCRAM-SHA-256/SCRAM-SHA-1/PLAIN, resource binding,
// XEP-0199 keepalive pings, iq request/response matching, and two XEP-0133
// admin commands so the ACS can create per-device accounts on its own
// server. There is no roster, presence handling, message support or
// server-to-server code. Hand-written rather than a third-party library on
// purpose: see docs/XMPP-CONNECTION-REQUEST.md ("Design choices").
package xmpp

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strings"
)

// Namespaces used on the wire.
const (
	nsStream   = "http://etherx.jabber.org/streams"
	nsTLS      = "urn:ietf:params:xml:ns:xmpp-tls"
	nsSASL     = "urn:ietf:params:xml:ns:xmpp-sasl"
	nsBind     = "urn:ietf:params:xml:ns:xmpp-bind"
	nsSession  = "urn:ietf:params:xml:ns:xmpp-session"
	nsStanzas  = "urn:ietf:params:xml:ns:xmpp-stanzas"
	nsPing     = "urn:xmpp:ping"
	nsCommands = "http://jabber.org/protocol/commands"
	nsData     = "jabber:x:data"

	// NSConnectionRequest is the TR-069 Annex K payload namespace.
	NSConnectionRequest = "urn:broadband-forum-org:cwmp:xmppConnReq-1-0"
)

// IQ is an iq stanza. Payload is the raw inner XML, both inbound (as
// received) and outbound (written verbatim, so it must already be
// well-formed — build it with xml.Marshal, never by string concatenation of
// untrusted values).
type IQ struct {
	XMLName xml.Name `xml:"iq"`
	ID      string   `xml:"id,attr"`
	Type    string   `xml:"type,attr"`
	From    string   `xml:"from,attr,omitempty"`
	To      string   `xml:"to,attr,omitempty"`
	Payload []byte   `xml:",innerxml"`
}

// Marshal renders the stanza.
func (iq *IQ) Marshal() ([]byte, error) { return xml.Marshal(iq) }

// StanzaError is an iq of type "error" (RFC 6120 §8.3). The CPE answers a
// rejected Connection Request this way (Annex K: not-authorized for bad
// credentials, service-unavailable when it is rate-limiting or the feature
// is off), and the server answers on the CPE's behalf when the addressed
// resource is not online (service-unavailable).
type StanzaError struct {
	Type      string // cancel, auth, modify, wait, continue
	Condition string // e.g. service-unavailable, not-authorized
	Text      string
}

func (e *StanzaError) Error() string {
	msg := "xmpp stanza error: " + e.Condition
	if e.Type != "" {
		msg += " (" + e.Type + ")"
	}
	if e.Text != "" {
		msg += ": " + e.Text
	}
	return msg
}

// ParseStanzaError extracts the <error/> child of an error iq's payload.
// It never fails: an unparseable or missing error element yields
// condition "undefined-condition" so callers always get a usable value.
func ParseStanzaError(payload []byte) *StanzaError {
	out := &StanzaError{Condition: "undefined-condition"}
	dec := xml.NewDecoder(bytes.NewReader(payload))
	inError, inText := false, false
	for {
		tok, err := dec.Token()
		if err != nil {
			return out
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch {
			case !inError && t.Name.Local == "error":
				inError = true
				for _, a := range t.Attr {
					if a.Name.Local == "type" {
						out.Type = a.Value
					}
				}
			case inError && t.Name.Local == "text":
				inText = true
			case inError && t.Name.Space == nsStanzas && out.Condition == "undefined-condition":
				out.Condition = t.Name.Local
			}
		case xml.EndElement:
			if t.Name.Local == "text" {
				inText = false
			}
			if t.Name.Local == "error" && inError {
				return out
			}
		case xml.CharData:
			if inText {
				out.Text += strings.TrimSpace(string(t))
			}
		}
	}
}

// connectionRequest is the Annex K payload. Username and Password are the
// device's Connection Request credentials (ManagementServer.
// ConnectionRequestUsername/Password), exactly as the HTTP Digest path
// would use them; the CPE validates them before opening a session.
type connectionRequest struct {
	XMLName  xml.Name `xml:"urn:broadband-forum-org:cwmp:xmppConnReq-1-0 connectionRequest"`
	Username string   `xml:"username"`
	Password string   `xml:"password"`
}

// BuildConnectionRequest renders the TR-069 Annex K iq:
//
//	<iq type="get" id="…" to="cpe@domain/resource">
//	  <connectionRequest xmlns="urn:broadband-forum-org:cwmp:xmppConnReq-1-0">
//	    <username>…</username><password>…</password>
//	  </connectionRequest>
//	</iq>
//
// The from attribute is left to the server to stamp (RFC 6120 §8.1.2.1),
// which is also what the CPE checks against ConnReqAllowedJabberIDs.
func BuildConnectionRequest(id, to, username, password string) ([]byte, error) {
	payload, err := xml.Marshal(connectionRequest{Username: username, Password: password})
	if err != nil {
		return nil, err
	}
	return (&IQ{ID: id, Type: "get", To: to, Payload: payload}).Marshal()
}

// ParseConnectionRequest is the CPE-side inverse of BuildConnectionRequest
// (used by the in-process fake CPE in tests, and handy for anyone
// debugging captured stanzas). ok is false when payload is not an Annex K
// connectionRequest.
func ParseConnectionRequest(payload []byte) (username, password string, ok bool) {
	var req connectionRequest
	if err := xml.Unmarshal(payload, &req); err != nil {
		return "", "", false
	}
	if req.XMLName.Space != NSConnectionRequest || req.XMLName.Local != "connectionRequest" {
		return "", "", false
	}
	return req.Username, req.Password, true
}

// firstChild reports the name of the first element in an iq payload — how
// inbound get/set requests are routed (ping vs. anything else).
func firstChild(payload []byte) (xml.Name, bool) {
	dec := xml.NewDecoder(bytes.NewReader(payload))
	for {
		tok, err := dec.Token()
		if err != nil {
			return xml.Name{}, false
		}
		if se, ok := tok.(xml.StartElement); ok {
			return se.Name, true
		}
	}
}

// errorReply builds the RFC 6120 §8.4 answer to an iq get/set this client
// does not implement. Every get/set must be answered, or the sender waits
// for its own timeout.
func errorReply(req *IQ, errType, condition string) ([]byte, error) {
	type cond struct {
		XMLName xml.Name
	}
	type stanzaErr struct {
		XMLName xml.Name `xml:"error"`
		Type    string   `xml:"type,attr"`
		Cond    cond
	}
	payload, err := xml.Marshal(stanzaErr{Type: errType, Cond: cond{XMLName: xml.Name{Space: nsStanzas, Local: condition}}})
	if err != nil {
		return nil, err
	}
	return (&IQ{ID: req.ID, Type: "error", To: req.From, Payload: payload}).Marshal()
}

// Bare strips the resource from a JID ("a@b/c" -> "a@b").
func Bare(jid string) string {
	if i := strings.IndexByte(jid, '/'); i >= 0 {
		return jid[:i]
	}
	return jid
}

// ErrInvalidJID is returned for a JID this client will not address.
var ErrInvalidJID = errors.New("invalid jid")

// ValidateFullJID checks the shape of a CPE's ConnReqJabberID before the
// ACS addresses a Connection Request to it. Annex K requires a full JID
// (localpart@domain/resource): a bare JID would be delivered to whichever
// resource the server picks — or to none. The value comes from the CPE,
// so it is also bounded and checked for characters that have no place in
// a JID (whitespace, XML metacharacters are escaped anyway by xml.Marshal).
func ValidateFullJID(jid string) error {
	if len(jid) == 0 || len(jid) > 3071 {
		return fmt.Errorf("%w: length", ErrInvalidJID)
	}
	if strings.ContainsAny(jid, " \t\r\n\"'<>&") {
		return fmt.Errorf("%w: contains forbidden characters", ErrInvalidJID)
	}
	slash := strings.IndexByte(jid, '/')
	if slash <= 0 || slash == len(jid)-1 {
		return fmt.Errorf("%w: not a full JID (missing resource)", ErrInvalidJID)
	}
	at := strings.IndexByte(jid[:slash], '@')
	if at <= 0 || at == slash-1 {
		return fmt.Errorf("%w: missing localpart or domain", ErrInvalidJID)
	}
	return nil
}

// readStreamError extracts the condition of a <stream:error/>.
func readStreamError(dec *xml.Decoder, start *xml.StartElement) error {
	var se struct {
		Inner []byte `xml:",innerxml"`
	}
	if err := dec.DecodeElement(&se, start); err != nil {
		return fmt.Errorf("xmpp stream error (unparseable): %w", err)
	}
	name, ok := firstChild(se.Inner)
	if !ok {
		return errors.New("xmpp stream error")
	}
	return fmt.Errorf("xmpp stream error: %s", name.Local)
}

// nextElement returns the next top-level element start inside an open
// stream, skipping whitespace keepalives. The closing </stream:stream>
// is reported as io.EOF.
func nextElement(dec *xml.Decoder) (xml.StartElement, error) {
	for {
		tok, err := dec.Token()
		if err != nil {
			return xml.StartElement{}, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			return t, nil
		case xml.EndElement:
			if t.Name.Space == nsStream && t.Name.Local == "stream" {
				return xml.StartElement{}, io.EOF
			}
		}
	}
}
