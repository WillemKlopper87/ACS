package cwmp

import (
	"encoding/xml"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "test", "fixtures", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return b
}

func TestParseInform(t *testing.T) {
	env, err := ParseEnvelope(readFixture(t, "inform_bootstrap.xml"))
	if err != nil {
		t.Fatalf("ParseEnvelope: %v", err)
	}
	if env.Body.Inform == nil {
		t.Fatal("expected Inform to be parsed, got nil")
	}

	inform := env.Body.Inform
	if got, want := inform.DeviceId.Manufacturer, "Zyxel"; got != want {
		t.Errorf("Manufacturer = %q, want %q", got, want)
	}
	if got, want := inform.DeviceId.OUI, "001349"; got != want {
		t.Errorf("OUI = %q, want %q", got, want)
	}
	if got, want := inform.DeviceId.SerialNumber, "S230Q12345678"; got != want {
		t.Errorf("SerialNumber = %q, want %q", got, want)
	}
	if got, want := inform.DeviceId.NaturalKey(), "001349+NR5103+S230Q12345678"; got != want {
		t.Errorf("NaturalKey() = %q, want %q", got, want)
	}

	wantEvents := []string{"0 BOOTSTRAP", "1 BOOT"}
	events := inform.EventCodes()
	if len(events) != len(wantEvents) {
		t.Fatalf("EventCodes() = %v, want %v", events, wantEvents)
	}
	for i, e := range wantEvents {
		if events[i] != e {
			t.Errorf("EventCodes()[%d] = %q, want %q", i, events[i], e)
		}
	}
	if !inform.HasEventCode("0") {
		t.Error("HasEventCode(\"0\") = false, want true")
	}
	if inform.HasEventCode("6") {
		t.Error("HasEventCode(\"6\") = true, want false")
	}

	if len(inform.ParameterList) != 2 {
		t.Fatalf("ParameterList len = %d, want 2", len(inform.ParameterList))
	}
	if got, want := inform.ParameterList[0].Name, "Device.DeviceInfo.SoftwareVersion"; got != want {
		t.Errorf("ParameterList[0].Name = %q, want %q", got, want)
	}
}

func TestParseGetRPCMethodsResponse(t *testing.T) {
	env, err := ParseEnvelope(readFixture(t, "get_rpc_methods_response.xml"))
	if err != nil {
		t.Fatalf("ParseEnvelope: %v", err)
	}
	if env.Body.GetRPCMethodsResponse == nil {
		t.Fatal("expected GetRPCMethodsResponse, got nil")
	}
	methods := env.Body.GetRPCMethodsResponse.MethodList
	if len(methods) != 6 {
		t.Fatalf("MethodList len = %d, want 6", len(methods))
	}
	found := false
	for _, m := range methods {
		if m == "Download" {
			found = true
		}
	}
	if !found {
		t.Errorf("MethodList = %v, want to contain %q", methods, "Download")
	}
}

func TestParseGetParameterNamesResponse(t *testing.T) {
	env, err := ParseEnvelope(readFixture(t, "get_parameter_names_response.xml"))
	if err != nil {
		t.Fatalf("ParseEnvelope: %v", err)
	}
	if env.Body.GetParameterNamesResponse == nil {
		t.Fatal("expected GetParameterNamesResponse, got nil")
	}
	params := env.Body.GetParameterNamesResponse.ParameterList
	if len(params) != 2 {
		t.Fatalf("ParameterList len = %d, want 2", len(params))
	}
	if got, want := params[1].Name, "Device.WiFi.SSID.1.SSID"; got != want {
		t.Errorf("ParameterList[1].Name = %q, want %q", got, want)
	}
	if got, want := params[1].Writable, "1"; got != want {
		t.Errorf("ParameterList[1].Writable = %q, want %q", got, want)
	}
}

func TestParseFault(t *testing.T) {
	env, err := ParseEnvelope(readFixture(t, "fault.xml"))
	if err != nil {
		t.Fatalf("ParseEnvelope: %v", err)
	}
	if env.Body.Fault == nil {
		t.Fatal("expected Fault, got nil")
	}
	if got, want := env.Body.Fault.CWMPCode(), "9005"; got != want {
		t.Errorf("CWMPCode() = %q, want %q", got, want)
	}
	if got, want := env.Body.Fault.CWMPMessage(), "Invalid parameter name"; got != want {
		t.Errorf("CWMPMessage() = %q, want %q", got, want)
	}
}

func TestParseEmptyBody(t *testing.T) {
	env, err := ParseEnvelope(nil)
	if err != nil {
		t.Fatalf("ParseEnvelope(nil): %v", err)
	}
	if !env.Body.IsEmpty() {
		t.Error("expected IsEmpty() true for empty POST")
	}
}

// mustWellFormed parses raw as generic XML to confirm the ACS's own
// rendered output is well-formed — this is the round-trip check for
// outbound messages, since InformResponse/GetRPCMethods/GetParameterNames
// requests are never parsed by the ACS itself (only rendered).
func mustWellFormed(t *testing.T, raw []byte) {
	t.Helper()
	var generic struct {
		XMLName xml.Name
	}
	if err := xml.Unmarshal(raw, &generic); err != nil {
		t.Fatalf("rendered output is not well-formed XML: %v\n%s", err, raw)
	}
}

func TestRenderInformResponse(t *testing.T) {
	raw := RenderInformResponse("acs-test-id")
	mustWellFormed(t, raw)
	if !strings.Contains(string(raw), "acs-test-id") {
		t.Error("expected rendered InformResponse to contain the cwmp:ID")
	}
	if !strings.Contains(string(raw), "InformResponse") {
		t.Error("expected rendered output to contain InformResponse element")
	}
}

func TestRenderGetRPCMethods(t *testing.T) {
	raw := RenderGetRPCMethods("acs-test-id")
	mustWellFormed(t, raw)
	if !strings.Contains(string(raw), "GetRPCMethods") {
		t.Error("expected rendered output to contain GetRPCMethods element")
	}
}

func TestRenderGetParameterNames(t *testing.T) {
	raw := RenderGetParameterNames("acs-test-id", "InternetGatewayDevice.", false)
	mustWellFormed(t, raw)
	if !strings.Contains(string(raw), "InternetGatewayDevice.") {
		t.Error("expected rendered output to contain the requested ParameterPath")
	}
	if !strings.Contains(string(raw), "<NextLevel>0</NextLevel>") {
		t.Error("expected NextLevel to render as 0 for nextLevel=false")
	}
}

func TestEscapeXML(t *testing.T) {
	got := escapeXML(`Device.<X>&"Y"`)
	want := `Device.&lt;X&gt;&amp;&quot;Y&quot;`
	if got != want {
		t.Errorf("escapeXML() = %q, want %q", got, want)
	}
}
