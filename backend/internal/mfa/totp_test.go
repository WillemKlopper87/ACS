package mfa

import (
	"strings"
	"testing"
	"time"
)

// RFC 6238 Appendix B, SHA-1 rows, truncated to six digits.
func TestCodeMatchesRFC6238Vectors(t *testing.T) {
	secret := b32.EncodeToString([]byte("12345678901234567890"))
	for unix, want := range map[int64]string{59: "287082", 1111111109: "081804", 1234567890: "005924", 2000000000: "279037"} {
		got, err := Code(secret, time.Unix(unix, 0))
		if err != nil || got != want {
			t.Errorf("Code at %d = %q, %v; want %q", unix, got, err, want)
		}
	}
}

func TestVerifyWindowAndReplay(t *testing.T) {
	secret, err := NewSecret()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_800_000_000, 0)
	code, _ := Code(secret, now)

	matched, ok := Verify(secret, code, now, 0)
	if !ok || matched != step(now) {
		t.Fatalf("current code refused")
	}
	// Spaces and a dash, as people paste them.
	if _, ok := Verify(secret, code[:3]+" "+code[3:], now, 0); !ok {
		t.Error("code with a space refused")
	}
	// One step of clock drift either way is accepted; two is not.
	if _, ok := Verify(secret, code, now.Add(30*time.Second), 0); !ok {
		t.Error("code one step old refused")
	}
	if _, ok := Verify(secret, code, now.Add(-30*time.Second), 0); !ok {
		t.Error("code one step ahead refused")
	}
	if _, ok := Verify(secret, code, now.Add(90*time.Second), 0); ok {
		t.Error("code three steps old accepted")
	}
	// A code is good once: the same code after it was accepted is refused.
	if _, ok := Verify(secret, code, now, matched); ok {
		t.Error("replayed code accepted")
	}
	for _, bad := range []string{"", "12345", "1234567", "abcdef"} {
		if _, ok := Verify(secret, bad, now, 0); ok {
			t.Errorf("code %q accepted", bad)
		}
	}
}

func TestURI(t *testing.T) {
	u := URI("ACS", "alice", "JBSWY3DPEHPK3PXP")
	for _, want := range []string{"otpauth://totp/ACS:alice?", "secret=JBSWY3DPEHPK3PXP", "issuer=ACS", "digits=6", "period=30"} {
		if !strings.Contains(u, want) {
			t.Errorf("URI %q lacks %q", u, want)
		}
	}
}

func TestRecoveryCodeShape(t *testing.T) {
	code, err := newRecoveryCode()
	if err != nil || len(code) != 11 || code[5] != '-' {
		t.Fatalf("recovery code %q, %v", code, err)
	}
	if len(NormalizeCode(code)) == digits {
		t.Error("a recovery code must not look like an app code")
	}
	if hashRecoveryCode(NormalizeCode(code)) != hashRecoveryCode(strings.ToLower(NormalizeCode(code))) {
		t.Error("recovery codes should not be case-sensitive")
	}
}
