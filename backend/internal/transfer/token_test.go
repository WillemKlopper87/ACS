package transfer

import (
	"errors"
	"strings"
	"testing"
	"time"
)

var key = DeriveKey([]byte("test-parent-secret"))

func TestRoundTrip(t *testing.T) {
	now := time.Now()
	tok := Sign(key, "firmware", "img-1", now.Add(time.Hour))
	if err := Verify(key, "firmware", "img-1", tok, now); err != nil {
		t.Fatalf("Verify() = %v, want nil", err)
	}
}

func TestExpired(t *testing.T) {
	now := time.Now()
	tok := Sign(key, "firmware", "img-1", now.Add(-time.Minute))
	if err := Verify(key, "firmware", "img-1", tok, now); !errors.Is(err, ErrExpired) {
		t.Fatalf("Verify() = %v, want ErrExpired", err)
	}
}

func TestWrongPurposeRejected(t *testing.T) {
	now := time.Now()
	tok := Sign(key, "firmware", "id-1", now.Add(time.Hour))
	if err := Verify(key, "upload", "id-1", tok, now); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Verify() with wrong purpose = %v, want ErrInvalid", err)
	}
}

func TestWrongIDRejected(t *testing.T) {
	now := time.Now()
	tok := Sign(key, "upload", "slot-1", now.Add(time.Hour))
	if err := Verify(key, "upload", "slot-2", tok, now); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Verify() with wrong id = %v, want ErrInvalid", err)
	}
}

func TestTamperedExpiryRejected(t *testing.T) {
	now := time.Now()
	tok := Sign(key, "upload", "slot-1", now.Add(-time.Minute))
	// Push the expiry forward without re-signing — must fail as invalid,
	// not merely expired.
	_, mac, _ := strings.Cut(tok, ".")
	forged := "9999999999." + mac
	if err := Verify(key, "upload", "slot-1", forged, now); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Verify() of forged expiry = %v, want ErrInvalid", err)
	}
}

func TestGarbageRejected(t *testing.T) {
	for _, tok := range []string{"", ".", "abc", "123.zzzz", "123."} {
		if err := Verify(key, "upload", "id", tok, time.Now()); !errors.Is(err, ErrInvalid) {
			t.Errorf("Verify(%q) = %v, want ErrInvalid", tok, err)
		}
	}
}

func TestWrongKeyRejected(t *testing.T) {
	now := time.Now()
	tok := Sign(key, "upload", "id", now.Add(time.Hour))
	other := DeriveKey([]byte("other-secret"))
	if err := Verify(other, "upload", "id", tok, now); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Verify() with wrong key = %v, want ErrInvalid", err)
	}
}
