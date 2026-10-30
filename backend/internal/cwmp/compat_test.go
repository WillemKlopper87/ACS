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

func cpeRequestEnvelope(ns, id, body string) []byte {
	return []byte(`<soap-env:Envelope xmlns:soap-env="http://schemas.xmlsoap.org/soap/envelope/" xmlns:cwmp="` + ns + `">` +
		`<soap-env:Header><cwmp:ID soap-env:mustUnderstand="1">` + id + `</cwmp:ID></soap-env:Header>` +
		`<soap-env:Body>` + body + `</soap-env:Body></soap-env:Envelope>`)
}

func TestParseEnvelopeCPEInitiatedGetRPCMethodsIsNotEmpty(t *testing.T) {
	env, err := ParseEnvelope(cpeRequestEnvelope("urn:dslforum-org:cwmp-1-2", "rpc-1", `<cwmp:GetRPCMethods></cwmp:GetRPCMethods>`))
	if err != nil {
		t.Fatalf("ParseEnvelope: %v", err)
	}
	if env.Body.GetRPCMethods == nil {
		t.Fatal("GetRPCMethods request not recognised")
	}
	if env.Body.IsEmpty() {
		t.Fatal("CPE-initiated GetRPCMethods must not be treated as an empty POST")
	}
	if got := env.Body.UnsupportedRequest(); got != "" {
		t.Fatalf("UnsupportedRequest() = %q, want empty", got)
	}
}

func TestParseEnvelopeUnsupportedCPERequest(t *testing.T) {
	env, err := ParseEnvelope(cpeRequestEnvelope("urn:dslforum-org:cwmp-1-0", "rpc-2",
		`<cwmp:AutonomousTransferComplete><CommandKey></CommandKey></cwmp:AutonomousTransferComplete>`))
	if err != nil {
		t.Fatalf("ParseEnvelope: %v", err)
	}
	if got := env.Body.UnsupportedRequest(); got != "AutonomousTransferComplete" {
		t.Fatalf("UnsupportedRequest() = %q, want AutonomousTransferComplete", got)
	}
	if env.Body.IsEmpty() {
		t.Fatal("unsupported CPE request must not be treated as an empty POST")
	}
}

func TestParseEnvelopeUnknownResponseIsNotAnUnsupportedRequest(t *testing.T) {
	env, err := ParseEnvelope(cpeRequestEnvelope("urn:dslforum-org:cwmp-1-0", "rpc-3", `<cwmp:SomeVendorRPCResponse/>`))
	if err != nil {
		t.Fatalf("ParseEnvelope: %v", err)
	}
	if got := env.Body.UnsupportedRequest(); got != "" {
		t.Fatalf("UnsupportedRequest() = %q, want empty for a *Response element", got)
	}
}

func TestGetRPCMethodsResponseEchoesIDAndNamespace(t *testing.T) {
	out := RenderGetRPCMethodsResponseNS("cpe-<id>", "urn:dslforum-org:cwmp-1-2")
	for _, want := range []string{
		`xmlns:cwmp="urn:dslforum-org:cwmp-1-2"`,
		`>cpe-&lt;id&gt;</cwmp:ID>`,
		`<cwmp:GetRPCMethodsResponse><MethodList soap-enc:arrayType="xsd:string[3]">`,
		`<string>Inform</string><string>GetRPCMethods</string><string>TransferComplete</string>`,
	} {
		if !bytes.Contains(out, []byte(want)) {
			t.Errorf("GetRPCMethodsResponse missing %q:\n%s", want, out)
		}
	}
	if _, err := ParseEnvelope(out); err != nil {
		t.Fatalf("rendered GetRPCMethodsResponse is not well-formed: %v", err)
	}
}

func TestMethodNotSupportedFaultIsCWMP8000(t *testing.T) {
	out := RenderMethodNotSupportedFaultNS("rpc-2", "urn:dslforum-org:cwmp-1-0")
	env, err := ParseEnvelope(out)
	if err != nil {
		t.Fatalf("rendered fault is not well-formed: %v", err)
	}
	if env.Body.Fault == nil || env.Body.Fault.FaultCode != "Server" || env.Body.Fault.CWMPCode() != "8000" {
		t.Fatalf("fault = %+v, want Server / 8000", env.Body.Fault)
	}
	if env.Header.ID != "rpc-2" {
		t.Fatalf("fault cwmp:ID = %q, want rpc-2", env.Header.ID)
	}
}

func TestSetParameterValuesPrefixesBareTypes(t *testing.T) {
	out := RenderSetParameterValues("id-1", []ParameterValueStruct{{Name: "Device.WiFi.SSID.1.Enable", Value: "1", Type: "boolean"}}, "k")
	if !bytes.Contains(out, []byte(`xsi:type="xsd:boolean"`)) {
		t.Fatalf("bare boolean type not rendered as xsd:boolean: %s", out)
	}
}
