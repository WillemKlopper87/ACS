package bss

import (
	"errors"
	"reflect"
	"testing"
)

func TestNormalizeOAuthPolicy(t *testing.T) {
	tests := []struct {
		name    string
		in      OAuthPolicy
		want    OAuthPolicy
		wantErr bool
	}{
		{
			name: "account scoped and normalized",
			in: OAuthPolicy{
				Scopes:     []string{ScopeTMFWrite, " " + ScopeTMFRead + " ", ScopeTMFWrite},
				AccountIDs: []string{" acct-b ", "acct-a", "acct-a"},
			},
			want: OAuthPolicy{Scopes: []string{ScopeTMFRead, ScopeTMFWrite}, AccountIDs: []string{"acct-a", "acct-b"}},
		},
		{
			name: "global scoped",
			in:   OAuthPolicy{Scopes: []string{ScopeTMFRead}, GlobalAccess: true},
			want: OAuthPolicy{Scopes: []string{ScopeTMFRead}, GlobalAccess: true},
		},
		{
			name: "empty policy allowed and fail closed",
			in:   OAuthPolicy{},
			want: OAuthPolicy{},
		},
		{
			name:    "unsupported scope",
			in:      OAuthPolicy{Scopes: []string{"tmf:admin"}, GlobalAccess: true},
			wantErr: true,
		},
		{
			name:    "global cannot also list accounts",
			in:      OAuthPolicy{Scopes: []string{ScopeTMFRead}, AccountIDs: []string{"acct-a"}, GlobalAccess: true},
			wantErr: true,
		},
		{
			name:    "permissions require entitlement",
			in:      OAuthPolicy{Scopes: []string{ScopeTMFRead}},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NormalizeOAuthPolicy(tt.in)
			if tt.wantErr {
				if !errors.Is(err, ErrInvalidOAuthPolicy) {
					t.Fatalf("NormalizeOAuthPolicy() error = %v, want ErrInvalidOAuthPolicy", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("NormalizeOAuthPolicy() error = %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("NormalizeOAuthPolicy() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestAllTMFScopesReturnsCopy(t *testing.T) {
	a := AllTMFScopes()
	a[0] = "mutated"
	b := AllTMFScopes()
	if b[0] == "mutated" {
		t.Fatal("AllTMFScopes returned mutable package state")
	}
}
