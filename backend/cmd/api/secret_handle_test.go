package main

import "testing"

func TestBuildSetParameterPayloadSecretHandle(t *testing.T) {
	// A TR-181 passphrase shorter than 8 characters fails validation as a
	// value; a handle carries no value to validate.
	payload, err := buildSetParameterPayload([]parameterInput{{Name: "Device.WiFi.AccessPoint.1.Security.KeyPassphrase", Type: "xsd:string", SecretHandle: "sh_abc"}})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if p := payload.Parameters[0]; p.SecretHandle != "sh_abc" || p.Value != "" {
		t.Fatalf("write = %+v", p)
	}
	if _, err := buildSetParameterPayload([]parameterInput{{Name: "Device.WiFi.AccessPoint.1.Security.KeyPassphrase", Value: "x", SecretHandle: "sh_abc"}}); err == nil {
		t.Fatal("a value and a handle were accepted")
	}
	if !hasSecretHandle([]parameterInput{{Name: "a"}, {Name: "b", SecretHandle: "sh_x"}}) || hasSecretHandle([]parameterInput{{Name: "a"}}) {
		t.Fatal("hasSecretHandle")
	}
}
