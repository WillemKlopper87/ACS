package cwmp

import (
	"strings"
	"testing"
)

func TestParseGetParameterValuesResponse(t *testing.T) {
	env, err := ParseEnvelope(readFixture(t, "get_parameter_values_response.xml"))
	if err != nil {
		t.Fatalf("ParseEnvelope: %v", err)
	}
	if env.Body.GetParameterValuesResponse == nil {
		t.Fatal("expected GetParameterValuesResponse, got nil")
	}
	params := env.Body.GetParameterValuesResponse.ParameterList
	if len(params) != 1 {
		t.Fatalf("ParameterList len = %d, want 1", len(params))
	}
	if got, want := params[0].Name, "Device.WiFi.SSID.1.SSID"; got != want {
		t.Errorf("Name = %q, want %q", got, want)
	}
	if got, want := params[0].Value, "CorpWiFi"; got != want {
		t.Errorf("Value = %q, want %q", got, want)
	}
}

func TestParseSetParameterValuesResponse(t *testing.T) {
	env, err := ParseEnvelope(readFixture(t, "set_parameter_values_response.xml"))
	if err != nil {
		t.Fatalf("ParseEnvelope: %v", err)
	}
	if env.Body.SetParameterValuesResponse == nil {
		t.Fatal("expected SetParameterValuesResponse, got nil")
	}
	if got, want := env.Body.SetParameterValuesResponse.Status, 0; got != want {
		t.Errorf("Status = %d, want %d", got, want)
	}
}

func TestRenderGetParameterValues(t *testing.T) {
	raw := RenderGetParameterValues("acs-test-id", []string{"Device.DeviceInfo.SoftwareVersion", "Device.WiFi.SSID.1.SSID"})
	mustWellFormed(t, raw)
	if !strings.Contains(string(raw), `arrayType="xsd:string[2]"`) {
		t.Errorf("expected arrayType count of 2, got: %s", raw)
	}
	if !strings.Contains(string(raw), "<string>Device.WiFi.SSID.1.SSID</string>") {
		t.Errorf("expected parameter name to be rendered, got: %s", raw)
	}
}

func TestRenderSetParameterValues(t *testing.T) {
	raw := RenderSetParameterValues("acs-test-id", []ParameterValueStruct{
		{Name: "Device.WiFi.SSID.1.SSID", Value: "CorpWiFi"},
	}, "setparam_20260804_0001")
	mustWellFormed(t, raw)
	if !strings.Contains(string(raw), "<Name>Device.WiFi.SSID.1.SSID</Name>") {
		t.Errorf("expected parameter name, got: %s", raw)
	}
	if !strings.Contains(string(raw), `<Value xsi:type="xsd:string">CorpWiFi</Value>`) {
		t.Errorf("expected parameter value, got: %s", raw)
	}
	if !strings.Contains(string(raw), "<ParameterKey>setparam_20260804_0001</ParameterKey>") {
		t.Errorf("expected ParameterKey, got: %s", raw)
	}
}

func TestRenderSetParameterValuesEscapesSpecialChars(t *testing.T) {
	raw := RenderSetParameterValues("id", []ParameterValueStruct{
		{Name: "Device.Test", Value: `<script>&"evil"</script>`},
	}, "")
	mustWellFormed(t, raw)
	if strings.Contains(string(raw), "<script>") {
		t.Errorf("expected value to be escaped, got: %s", raw)
	}
}
