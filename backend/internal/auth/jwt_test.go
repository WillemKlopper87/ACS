package auth

import (
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
