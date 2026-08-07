package cwmp

import (
	"strings"
	"testing"
)

// CPEs in the wild send a bare CRLF (or a few spaces) as their empty
// "any work for me?" POST — that must parse as an empty envelope, not a
// 400 "malformed XML" that kills the session right after InformResponse.
func TestParseEnvelope_WhitespaceOnlyBodyIsEmpty(t *testing.T) {
	for _, body := range []string{"", "\r\n", "  \n\t "} {
		env, err := ParseEnvelope([]byte(body))
		if err != nil {
			t.Fatalf("ParseEnvelope(%q): %v", body, err)
		}
		if !env.Body.IsEmpty() {
			t.Fatalf("ParseEnvelope(%q): expected empty body", body)
		}
	}
}

func TestDetectCWMPNamespace(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		{`<soapenv:Envelope xmlns:cwmp="urn:dslforum-org:cwmp-1-2">`, "urn:dslforum-org:cwmp-1-2"},
		{`<SOAP-ENV:Envelope xmlns:cwmp="urn:dslforum-org:cwmp-1-4">`, "urn:dslforum-org:cwmp-1-4"},
		{``, DefaultCWMPNamespace},
		{`<x>no namespace here</x>`, DefaultCWMPNamespace},
	}
	for _, c := range cases {
		if got := DetectCWMPNamespace([]byte(c.raw)); got != c.want {
			t.Errorf("DetectCWMPNamespace(%q) = %q, want %q", c.raw, got, c.want)
		}
	}
}

// The InformResponse must carry the CPE's own cwmp:ID and namespace
// version back — TR-069 §3.4.1.1; strict CPE stacks abort otherwise.
func TestRenderInformResponseNS_EchoesIDAndNamespace(t *testing.T) {
	out := string(RenderInformResponseNS("device-id-42", "urn:dslforum-org:cwmp-1-2"))
	if !strings.Contains(out, ">device-id-42</cwmp:ID>") {
		t.Errorf("InformResponse does not echo the request ID: %s", out)
	}
	if !strings.Contains(out, `xmlns:cwmp="urn:dslforum-org:cwmp-1-2"`) {
		t.Errorf("InformResponse does not echo the request namespace: %s", out)
	}
	if !strings.Contains(out, "<MaxEnvelopes>1</MaxEnvelopes>") {
		t.Errorf("InformResponse missing MaxEnvelopes: %s", out)
	}
}

func TestRenderTransferCompleteResponseNS_EchoesIDAndNamespace(t *testing.T) {
	out := string(RenderTransferCompleteResponseNS("tc-7", "urn:dslforum-org:cwmp-1-1"))
	if !strings.Contains(out, ">tc-7</cwmp:ID>") {
		t.Errorf("TransferCompleteResponse does not echo the request ID: %s", out)
	}
	if !strings.Contains(out, `xmlns:cwmp="urn:dslforum-org:cwmp-1-1"`) {
		t.Errorf("TransferCompleteResponse does not echo the request namespace: %s", out)
	}
}
