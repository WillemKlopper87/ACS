package xmpp

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

// TestBuildConnectionRequestMatchesAnnexK pins the wire shape TR-069
// Annex K specifies: an iq of type "get" addressed to the CPE's full JID,
// carrying connectionRequest in the xmppConnReq-1-0 namespace with the
// username and password as child elements.
func TestBuildConnectionRequestMatchesAnnexK(t *testing.T) {
	raw, err := BuildConnectionRequest("cr1", "xmpp-abc@xmpp.acs.lab/cwmp", "cr-user", "s3cret")
	if err != nil {
		t.Fatal(err)
	}
	want := `<iq id="cr1" type="get" to="xmpp-abc@xmpp.acs.lab/cwmp">` +
		`<connectionRequest xmlns="urn:broadband-forum-org:cwmp:xmppConnReq-1-0">` +
		`<username>cr-user</username><password>s3cret</password></connectionRequest></iq>`
	if string(raw) != want {
		t.Fatalf("stanza =\n%s\nwant\n%s", raw, want)
	}
}

// TestBuildConnectionRequestEscapesCredentials proves credentials are XML
// escaped, never spliced: a password containing markup must not be able
// to inject elements into the stanza.
func TestBuildConnectionRequestEscapesCredentials(t *testing.T) {
	raw, err := BuildConnectionRequest("cr2", "a@b/c", "u<x>", `p&"</password><evil/>`)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "<evil/>") || strings.Contains(string(raw), "<x>") {
		t.Fatalf("credentials were not escaped: %s", raw)
	}
	var iq IQ
	if err := xmlUnmarshal(raw, &iq); err != nil {
		t.Fatal(err)
	}
	user, pass, ok := ParseConnectionRequest(iq.Payload)
	if !ok || user != "u<x>" || pass != `p&"</password><evil/>` {
		t.Fatalf("round trip = %q %q %v", user, pass, ok)
	}
}

func TestParseConnectionRequestRejectsOtherPayloads(t *testing.T) {
	if _, _, ok := ParseConnectionRequest([]byte(`<ping xmlns="urn:xmpp:ping"/>`)); ok {
		t.Fatal("a ping must not parse as a connection request")
	}
	if _, _, ok := ParseConnectionRequest([]byte(`<connectionRequest xmlns="urn:other"><username>u</username></connectionRequest>`)); ok {
		t.Fatal("wrong namespace must not parse as a connection request")
	}
}

// TestParseStanzaError covers the replies a CPE (or the server on its
// behalf) gives a rejected Connection Request.
func TestParseStanzaError(t *testing.T) {
	cases := []struct {
		payload, typ, cond, text string
	}{
		{`<connectionRequest xmlns="urn:broadband-forum-org:cwmp:xmppConnReq-1-0"><username>u</username><password>p</password></connectionRequest>` +
			`<error type="cancel"><service-unavailable xmlns="urn:ietf:params:xml:ns:xmpp-stanzas"/></error>`, "cancel", "service-unavailable", ""},
		{`<error type="auth"><not-authorized xmlns="urn:ietf:params:xml:ns:xmpp-stanzas"/><text xmlns="urn:ietf:params:xml:ns:xmpp-stanzas">bad credentials</text></error>`, "auth", "not-authorized", "bad credentials"},
		{`garbage`, "", "undefined-condition", ""},
	}
	for _, c := range cases {
		got := ParseStanzaError([]byte(c.payload))
		if got.Type != c.typ || got.Condition != c.cond || got.Text != c.text {
			t.Errorf("ParseStanzaError(%q) = %+v, want type=%q cond=%q text=%q", c.payload, got, c.typ, c.cond, c.text)
		}
	}
}

func TestValidateFullJID(t *testing.T) {
	good := []string{"xmpp-1@xmpp.acs.lab/cwmp", "a@b/c/d"}
	bad := []string{"", "xmpp.acs.lab", "user@xmpp.acs.lab", "@b/c", "a@/c", "a@b/", "a b@c/d", "a@b/<c>", "/res"}
	for _, j := range good {
		if err := ValidateFullJID(j); err != nil {
			t.Errorf("ValidateFullJID(%q) = %v, want ok", j, err)
		}
	}
	for _, j := range bad {
		if err := ValidateFullJID(j); !errors.Is(err, ErrInvalidJID) {
			t.Errorf("ValidateFullJID(%q) = %v, want ErrInvalidJID", j, err)
		}
	}
	if Bare("a@b/c") != "a@b" || Bare("a@b") != "a@b" {
		t.Fatal("Bare")
	}
}

func TestFromMatches(t *testing.T) {
	own := "acs@d/acs"
	if !fromMatches("cpe@d/cwmp", "cpe@d/cwmp", "d", own) {
		t.Error("exact CPE reply must match")
	}
	if fromMatches("cpe@d/cwmp", "other@d/cwmp", "d", own) || fromMatches("cpe@d/cwmp", "", "d", own) {
		t.Error("a reply to a CPE request from anyone else must not match")
	}
	for _, from := range []string{"", "d", "acs@d", own} {
		if !fromMatches("d", from, "d", own) {
			t.Errorf("server reply from %q must match", from)
		}
	}
}

func TestChooseMechanismPrefersSCRAM(t *testing.T) {
	if got := chooseMechanism([]string{"PLAIN", "SCRAM-SHA-1", "SCRAM-SHA-256"}); got != mechSCRAMSHA256 {
		t.Fatalf("got %s", got)
	}
	if got := chooseMechanism([]string{"PLAIN", "SCRAM-SHA-1"}); got != mechSCRAMSHA1 {
		t.Fatalf("got %s", got)
	}
	if got := chooseMechanism([]string{"DIGEST-MD5", "PLAIN"}); got != mechPLAIN {
		t.Fatalf("got %s", got)
	}
	if got := chooseMechanism([]string{"DIGEST-MD5", "ANONYMOUS"}); got != "" {
		t.Fatalf("got %s", got)
	}
}

// TestSCRAMVectors checks both SCRAM variants against the published test
// vectors (RFC 5802 §5 for SHA-1, RFC 7677 §3 for SHA-256).
func TestSCRAMVectors(t *testing.T) {
	cases := []struct {
		mech, cnonce, serverFirst, clientFinal, serverFinal string
	}{
		{mechSCRAMSHA1, "fyko+d2lbbFgONRv9qkxdawL",
			"r=fyko+d2lbbFgONRv9qkxdawL3rfcNHYJY1ZVvWVs7j,s=QSXCR+Q6sek8bf92,i=4096",
			"c=biws,r=fyko+d2lbbFgONRv9qkxdawL3rfcNHYJY1ZVvWVs7j,p=v0X8v3Bz2T0CJGbJQyF0X+HI4Ts=",
			"v=rmF9pqV8S7suAoZWja4dJRkFsKQ="},
		{mechSCRAMSHA256, "rOprNGfwEbeRWgbNEkqO",
			"r=rOprNGfwEbeRWgbNEkqO%hvYDpWUa2RaTCAfuxFIlj)hNlF$k0,s=W22ZaJ0SNY7soEsUEjb6gQ==,i=4096",
			"c=biws,r=rOprNGfwEbeRWgbNEkqO%hvYDpWUa2RaTCAfuxFIlj)hNlF$k0,p=dHzbZapWIk4jUhN+Ute9ytag9zjfMHgsqmmiz7AndVQ=",
			"v=6rriTRBi23WpRR/wtup+mMhUZUn/dB5nLTJRsjl95G4="},
	}
	for _, c := range cases {
		s, err := newSCRAM(c.mech, "user", "pencil")
		if err != nil {
			t.Fatal(err)
		}
		s.clientNonce = c.cnonce
		if got := string(s.first()); got != "n,,n=user,r="+c.cnonce {
			t.Fatalf("%s client-first = %q", c.mech, got)
		}
		final, err := s.final([]byte(c.serverFirst))
		if err != nil {
			t.Fatal(err)
		}
		if string(final) != c.clientFinal {
			t.Fatalf("%s client-final = %q, want %q", c.mech, final, c.clientFinal)
		}
		if err := s.verify([]byte(c.serverFinal)); err != nil {
			t.Fatalf("%s server signature: %v", c.mech, err)
		}
		if err := s.verify([]byte("v=" + base64.StdEncoding.EncodeToString([]byte("forged-signature-0000")))); err == nil {
			t.Fatalf("%s accepted a forged server signature", c.mech)
		}
	}
}

func TestSCRAMRejectsDowngradesAndForeignNonces(t *testing.T) {
	s, _ := newSCRAM(mechSCRAMSHA1, "user", "pencil")
	s.clientNonce = "abc"
	s.first()
	if _, err := s.final([]byte("r=abcdef,s=QSXCR+Q6sek8bf92,i=1")); err == nil {
		t.Error("iteration count 1 must be refused")
	}
	if _, err := s.final([]byte("r=zzzdef,s=QSXCR+Q6sek8bf92,i=4096")); err == nil {
		t.Error("a server nonce not extending ours must be refused")
	}
}
