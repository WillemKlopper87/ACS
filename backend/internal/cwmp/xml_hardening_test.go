package cwmp

import "testing"

// These prove (not assume) build plan §4 Phase 6's "SOAP/XML hardening"
// item for this codebase's specific parser: encoding/xml.Unmarshal, no
// custom xml.Decoder, no Entity map set. Go's stdlib XML decoder has no
// external-entity-fetching capability at all (unlike libxml2-based
// parsers elsewhere) and does not expand general entities in character
// data unless the caller explicitly supplies a Decoder.Entity map — this
// codebase never does. So both classic attack shapes are structurally
// unavailable here, not just mitigated by configuration that could drift.

func TestParseEnvelope_RejectsExternalEntity(t *testing.T) {
	// Classic XXE: a DOCTYPE declaring a SYSTEM entity pointing at a local
	// file, referenced in a text node an attacker hopes gets echoed back.
	payload := []byte(`<?xml version="1.0"?>
<!DOCTYPE foo [<!ENTITY xxe SYSTEM "file:///etc/passwd">]>
<soap-env:Envelope xmlns:soap-env="http://schemas.xmlsoap.org/soap/envelope/" xmlns:cwmp="urn:dslforum-org:cwmp-1-0">
  <soap-env:Body>
    <soap-env:Fault><faultcode>Client</faultcode><faultstring>&xxe;</faultstring></soap-env:Fault>
  </soap-env:Body>
</soap-env:Envelope>`)

	_, err := ParseEnvelope(payload)
	if err == nil {
		t.Fatal("ParseEnvelope accepted a payload referencing an undefined external entity — want a parse error, since this parser has no entity map and no external-fetch capability")
	}
}

func TestParseEnvelope_RejectsEntityExpansionBomb(t *testing.T) {
	// "Billion laughs" shape: nested entity definitions that would expand
	// exponentially if the parser resolved them.
	payload := []byte(`<?xml version="1.0"?>
<!DOCTYPE lolz [
  <!ENTITY lol0 "lol">
  <!ENTITY lol1 "&lol0;&lol0;&lol0;&lol0;&lol0;&lol0;&lol0;&lol0;&lol0;&lol0;">
  <!ENTITY lol2 "&lol1;&lol1;&lol1;&lol1;&lol1;&lol1;&lol1;&lol1;&lol1;&lol1;">
]>
<soap-env:Envelope xmlns:soap-env="http://schemas.xmlsoap.org/soap/envelope/" xmlns:cwmp="urn:dslforum-org:cwmp-1-0">
  <soap-env:Body>
    <soap-env:Fault><faultcode>Client</faultcode><faultstring>&lol2;</faultstring></soap-env:Fault>
  </soap-env:Body>
</soap-env:Envelope>`)

	_, err := ParseEnvelope(payload)
	if err == nil {
		t.Fatal("ParseEnvelope accepted a payload with nested internal entity definitions — want a parse error, since general entities are never expanded here")
	}
}

func TestParseEnvelope_PredefinedEntitiesStillWork(t *testing.T) {
	// The hardening above must not have broken the 5 predefined XML
	// entities every real CPE payload legitimately uses (e.g. an SSID or
	// FaultString containing "&amp;" or "&lt;").
	payload := []byte(`<?xml version="1.0"?>
<soap-env:Envelope xmlns:soap-env="http://schemas.xmlsoap.org/soap/envelope/" xmlns:cwmp="urn:dslforum-org:cwmp-1-0">
  <soap-env:Body>
    <soap-env:Fault>
      <faultcode>Client</faultcode>
      <faultstring>Tom &amp; Jerry &lt;test&gt;</faultstring>
    </soap-env:Fault>
  </soap-env:Body>
</soap-env:Envelope>`)

	env, err := ParseEnvelope(payload)
	if err != nil {
		t.Fatalf("ParseEnvelope rejected predefined entities: %v", err)
	}
	if env.Body.Fault == nil {
		t.Fatal("expected a parsed Fault")
	}
	if got, want := env.Body.Fault.FaultString, "Tom & Jerry <test>"; got != want {
		t.Errorf("FaultString = %q, want %q", got, want)
	}
}
