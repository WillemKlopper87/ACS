package main

import (
	"testing"

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
