package auth

import (
	"testing"
	"time"
)

func TestCheckPassword(t *testing.T) {
	good := []string{"correct horse battery", "Tr0ub4dor&3-extra", "vX9#mQ2$kL7@", "blue-Kettle-49-rain", "naïve café über 12"}
	for _, p := range good {
		if err := CheckPassword(p, "alice"); err != nil {
			t.Errorf("%q refused: %v", p, err)
		}
	}
	bad := map[string]string{
		"short1!":           "at least 12",
		"elevenchars":       "at least 12",
		"alice-is-the-best": "username",
		"xALICEx123456":     "username",
		"aaaaaaaaaaaa":      "repeated",
		"123456789012":      "repeated",
		"abcdefghijkl":      "repeated",
		"Password2026!":     "common word",
		"P@ssw0rd1234":      "common word",
		"Qwerty123456":      "common word",
		"Admin!!!12345":     "common word",
		"welcome-2026-":     "common word",
		"passwordpassword":  "common word",
		"ChangeMe12345":     "common word",
	}
	for p, want := range bad {
		err := CheckPassword(p, "alice")
		if err == nil || !contains(err.Error(), want) {
			t.Errorf("%q: got %v, want an error mentioning %q", p, err, want)
		}
	}
	if err := CheckPassword(string(make([]byte, 80)), ""); err == nil {
		t.Error("a password beyond bcrypt's 72 bytes was accepted")
	}
	// A two-letter username is too short to forbid as a substring.
	if err := CheckPassword("an-okay-long-phrase", "an"); err != nil {
		t.Errorf("short username: %v", err)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// The check for a missing account costs real hashing time, like a wrong
// password does.
func TestBurnPasswordCheckTakesHashingTime(t *testing.T) {
	start := time.Now()
	BurnPasswordCheck("whatever")
	if d := time.Since(start); d < 5*time.Millisecond {
		t.Fatalf("burn took %v: it is not doing a bcrypt comparison", d)
	}
}
