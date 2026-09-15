package auth

import (
	"reflect"
	"testing"
	"time"
)

func TestSignAndVerifyJWT_RoundTrip(t *testing.T) {
	secret := []byte("test-signing-secret")
	now := time.Now().UTC().Truncate(time.Second)
	claims := Claims{Subject: "alice", Role: "admin", IssuedAt: now, ExpiresAt: now.Add(time.Hour)}

	token, err := SignJWT(secret, claims)
	if err != nil {
		t.Fatalf("SignJWT: %v", err)
	}

	got, err := VerifyJWT(secret, token)
	if err != nil {
		t.Fatalf("VerifyJWT: %v", err)
	}
	if got.Subject != claims.Subject || got.Role != claims.Role {
		t.Errorf("VerifyJWT roundtrip = %+v, want subject=%q role=%q", got, claims.Subject, claims.Role)
	}
}

func TestSignAndVerifyJWT_MachinePolicyRoundTrip(t *testing.T) {
	secret := []byte("test-signing-secret")
	now := time.Now().UTC().Truncate(time.Second)
	claims := Claims{
		Subject: "bss-client:client-1", Role: "bss_client", IssuedAt: now, ExpiresAt: now.Add(time.Hour),
		Scopes: []string{"tmf:read", "tmf:execute"}, AccountIDs: []string{"acct-a", "acct-b"},
	}

	token, err := SignJWT(secret, claims)
	if err != nil {
		t.Fatalf("SignJWT: %v", err)
	}
	got, err := VerifyJWT(secret, token)
	if err != nil {
		t.Fatalf("VerifyJWT: %v", err)
	}
	if !reflect.DeepEqual(got.Scopes, claims.Scopes) || !reflect.DeepEqual(got.AccountIDs, claims.AccountIDs) || got.GlobalAccess {
		t.Fatalf("machine policy roundtrip = %+v, want scopes=%v accounts=%v global=false", got, claims.Scopes, claims.AccountIDs)
	}

	claims.GlobalAccess = true
	claims.AccountIDs = nil
	token, err = SignJWT(secret, claims)
	if err != nil {
		t.Fatalf("SignJWT global: %v", err)
	}
	got, err = VerifyJWT(secret, token)
	if err != nil {
		t.Fatalf("VerifyJWT global: %v", err)
	}
	if !got.GlobalAccess || len(got.AccountIDs) != 0 {
		t.Fatalf("global machine policy roundtrip = %+v", got)
	}
}

func TestVerifyJWT_WrongSecretRejected(t *testing.T) {
	now := time.Now().UTC()
	token, err := SignJWT([]byte("secret-a"), Claims{Subject: "alice", Role: "admin", IssuedAt: now, ExpiresAt: now.Add(time.Hour)})
	if err != nil {
		t.Fatalf("SignJWT: %v", err)
	}

	if _, err := VerifyJWT([]byte("secret-b"), token); err != ErrInvalidToken {
		t.Errorf("VerifyJWT with wrong secret = %v, want ErrInvalidToken", err)
	}
}

func TestVerifyJWT_ExpiredRejected(t *testing.T) {
	secret := []byte("test-signing-secret")
	now := time.Now().UTC()
	// Already-expired token — issued an hour ago, expired 30 minutes ago.
	token, err := SignJWT(secret, Claims{Subject: "alice", Role: "readonly", IssuedAt: now.Add(-time.Hour), ExpiresAt: now.Add(-30 * time.Minute)})
	if err != nil {
		t.Fatalf("SignJWT: %v", err)
	}

	if _, err := VerifyJWT(secret, token); err != ErrInvalidToken {
		t.Errorf("VerifyJWT on expired token = %v, want ErrInvalidToken", err)
	}
}

func TestVerifyJWT_MalformedRejected(t *testing.T) {
	if _, err := VerifyJWT([]byte("secret"), "not-a-jwt"); err != ErrInvalidToken {
		t.Errorf("VerifyJWT on malformed token = %v, want ErrInvalidToken", err)
	}
}
