package credentials

import "testing"

func TestEncryptDecryptRoundTrip(t *testing.T) {
	r, err := NewRepository(nil, "test-passphrase")
	if err != nil {
		t.Fatalf("NewRepository: %v", err)
	}
	if !r.Encrypted() {
		t.Fatal("Encrypted() = false, want true when a key seed is configured")
	}

	stored, err := r.encrypt("super-secret-password")
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if stored == "super-secret-password" {
		t.Fatal("encrypt returned plaintext unchanged — not actually encrypting")
	}

	plain, err := r.decrypt(stored)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if plain != "super-secret-password" {
		t.Fatalf("decrypt() = %q, want original plaintext", plain)
	}
}

func TestEncryptDisabledByDefault(t *testing.T) {
	r, err := NewRepository(nil, "")
	if err != nil {
		t.Fatalf("NewRepository: %v", err)
	}
	if r.Encrypted() {
		t.Fatal("Encrypted() = true with no key seed configured")
	}

	stored, err := r.encrypt("plain-password")
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if stored != "plain-password" {
		t.Fatalf("encrypt() = %q with encryption disabled, want unchanged plaintext", stored)
	}
}

// A row written before ACS_CREDENTIAL_ENCRYPTION_KEY was ever configured
// (no encPrefix) must still decrypt cleanly once encryption is turned on
// — no backfill migration required.
func TestDecryptLegacyPlaintextRow(t *testing.T) {
	r, err := NewRepository(nil, "a-key-turned-on-later")
	if err != nil {
		t.Fatalf("NewRepository: %v", err)
	}

	plain, err := r.decrypt("legacy-unencrypted-value")
	if err != nil {
		t.Fatalf("decrypt legacy row: %v", err)
	}
	if plain != "legacy-unencrypted-value" {
		t.Fatalf("decrypt() = %q, want legacy plaintext returned as-is", plain)
	}
}

func TestDecryptWrongKeyFails(t *testing.T) {
	r1, err := NewRepository(nil, "key-one")
	if err != nil {
		t.Fatalf("NewRepository: %v", err)
	}
	stored, err := r1.encrypt("secret")
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}

	r2, err := NewRepository(nil, "key-two")
	if err != nil {
		t.Fatalf("NewRepository: %v", err)
	}
	if _, err := r2.decrypt(stored); err == nil {
		t.Fatal("decrypt with the wrong key succeeded — GCM should reject a tampered/mismatched key")
	}
}
