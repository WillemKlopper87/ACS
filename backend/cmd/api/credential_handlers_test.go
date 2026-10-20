package main

import (
	"testing"
	"time"

	"acs/internal/credentials"
)

func TestCredentialMayBeManuallyActivated(t *testing.T) {
	tests := []struct {
		name string
		cred *credentials.Credential
		want bool
	}{
		{name: "connection request", cred: &credentials.Credential{CredentialType: credentials.TypeConnectionRequest}, want: true},
		{name: "cwmp digest", cred: &credentials.Credential{CredentialType: credentials.TypeCWMPDigest}, want: false},
		{name: "unknown", cred: &credentials.Credential{CredentialType: "UNKNOWN"}, want: false},
		{name: "nil", cred: nil, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := credentialMayBeManuallyActivated(tt.cred); got != tt.want {
				t.Fatalf("credentialMayBeManuallyActivated(%v) = %v, want %v", tt.cred, got, tt.want)
			}
		})
	}
}


func TestCredentialResponsePreservesDirection(t *testing.T) {
	created := time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC)
	for _, credentialType := range []string{credentials.TypeConnectionRequest, credentials.TypeCWMPDigest} {
		t.Run(credentialType, func(t *testing.T) {
			resp := toCredentialResponse(&credentials.Credential{
				ID: "cred-1", DeviceID: "dev-1", CredentialType: credentialType,
				Version: 2, Username: "user", Status: credentials.StatusPending, CreatedAt: created,
			})
			if resp.CredentialType != credentialType {
				t.Fatalf("CredentialType = %q, want %q", resp.CredentialType, credentialType)
			}
		})
	}
}
