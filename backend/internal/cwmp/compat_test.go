package cwmp

import (
	"bytes"
	"testing"
)

func TestParseEnvelopeWhitespaceOnlyIsEmpty(t *testing.T) {
	for _, raw := range [][]byte{[]byte("   \r\n\t"), []byte("\n"), []byte("\r\n")} {
		env, err := ParseEnvelope(raw)
		if err != nil {
			t.Fatalf("ParseEnvelope(%q): %v", raw, err)
		}
		if !env.Body.IsEmpty() {
			t.Errorf("ParseEnvelope(%q): Body.IsEmpty() = false, want true", raw)
		}
	}
}

func TestDetectCWMPNamespace(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"cwmp12", `<Envelope xmlns:cwmp="urn:dslforum-org:cwmp-1-2"><Header><cwmp:ID>1</cwmp:ID></Header></Envelope>`, "urn:dslforum-org:cwmp-1-2"},
		{"cwmp14", `<SOAP-ENV:Envelope xmlns:cwmp="urn:dslforum-org:cwmp-1-4"></SOAP-ENV:Envelope>`, "urn:dslforum-org:cwmp-1-4"},
		{"future-compatible", `<Envelope xmlns:cwmp="urn:dslforum-org:cwmp-1-5"></Envelope>`, "urn:dslforum-org:cwmp-1-5"},
		{"absent", `<Envelope><Body/></Envelope>`, DefaultCWMPNamespace},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := DetectCWMPNamespace([]byte(tc.raw)); got != tc.want {
				t.Errorf("DetectCWMPNamespace() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestInformResponseEchoesIDAndNamespace(t *testing.T) {
	got := RenderInformResponseNS(`cpe-&-id`, "urn:dslforum-org:cwmp-1-2")
	if !bytes.Contains(got, []byte(`xmlns:cwmp="urn:dslforum-org:cwmp-1-2"`)) {
		t.Errorf("response does not use negotiated namespace:\n%s", got)
	}
	if !bytes.Contains(got, []byte(`>cpe-&amp;-id</cwmp:ID>`)) {
		t.Errorf("response does not XML-escape/echo CPE cwmp:ID:\n%s", got)
	}
}

func TestTransferCompleteResponseEchoesNamespace(t *testing.T) {
	got := RenderTransferCompleteResponseNS("tc-1", "urn:dslforum-org:cwmp-1-4")
	if !bytes.Contains(got, []byte(`xmlns:cwmp="urn:dslforum-org:cwmp-1-4"`)) {
		t.Errorf("TransferCompleteResponse does not use negotiated namespace:\n%s", got)
	}
}

func TestRewriteCWMPNamespaceForACSInitiatedRPC(t *testing.T) {
	original := RenderGetRPCMethods("id-1")
	got := RewriteCWMPNamespace(original, "urn:dslforum-org:cwmp-1-4")
	if !bytes.Contains(got, []byte(`xmlns:cwmp="urn:dslforum-org:cwmp-1-4"`)) {
		t.Fatalf("outbound RPC did not use session namespace:\n%s", got)
	}
	if bytes.Contains(got, []byte(`xmlns:cwmp="urn:dslforum-org:cwmp-1-0"`)) {
		t.Fatalf("outbound RPC retained default namespace:\n%s", got)
	}
}

func TestRewriteCWMPNamespaceRejectsInjection(t *testing.T) {
	original := RenderGetRPCMethods("id-1")
	got := RewriteCWMPNamespace(original, `urn:dslforum-org:cwmp-1-4" bad="1`)
	if !bytes.Equal(got, original) {
		t.Fatal("invalid namespace should leave rendered envelope unchanged")
	}
}
