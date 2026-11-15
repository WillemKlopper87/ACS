package auth

import (
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/crypto/bcrypt"
)

// MinPasswordLength is the shortest operator password accepted when one
// is set or changed. Passwords set before this rule keep working.
const MinPasswordLength = 12

// maxPasswordBytes is bcrypt's limit: it reads no further.
const maxPasswordBytes = 72

// commonBases are words a password must not simply be dressed up from:
// "Password2026!" and "Qwerty123456" are among the first things tried.
var commonBases = []string{
	"password", "passw0rd", "qwerty", "qwertyuiop", "admin", "administrator", "welcome", "letmein",
	"changeme", "iloveyou", "operator", "superadmin", "default", "secret", "master", "login",
	"abc", "abcdef", "asdf", "asdfgh", "zxcvbn", "monkey", "dragon", "football", "baseball", "sunshine", "princess",
}

// CheckPassword says why a new operator password is not acceptable, or
// nil. It is a floor, not a strength meter: length, not the username, not
// one repeated character or a keyboard or counting run, and not a common
// word with digits and punctuation around it. It does not consult a list
// of breached passwords; two-step sign-in is the control for a password
// that has leaked.
func CheckPassword(password, username string) error {
	if utf8.RuneCountInString(password) < MinPasswordLength {
		return errors.New("the password must be at least 12 characters")
	}
	if len(password) > maxPasswordBytes {
		return errors.New("the password must be at most 72 bytes")
	}
	lower := strings.ToLower(password)
	if u := strings.ToLower(strings.TrimSpace(username)); len(u) >= 3 && strings.Contains(lower, u) {
		return errors.New("the password must not contain the username")
	}
	if isRun(lower) {
		return errors.New("the password is a repeated character or a simple run; choose something less predictable")
	}
	// The word in the middle: digits and punctuation trimmed from both
	// ends, then the usual letter substitutions undone.
	trimmed := strings.TrimFunc(lower, func(r rune) bool { return !unicode.IsLetter(r) })
	core := strings.Map(func(r rune) rune {
		switch r {
		case '0':
			return 'o'
		case '1', '!':
			return 'i'
		case '3':
			return 'e'
		case '4', '@':
			return 'a'
		case '5', '$':
			return 's'
		case '7':
			return 't'
		}
		if unicode.IsLetter(r) {
			return r
		}
		return -1
	}, trimmed)
	letters := strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) {
			return r
		}
		return -1
	}, lower)
	for _, candidate := range []string{core, letters} {
		for _, base := range commonBases {
			if candidate == base || candidate == base+base {
				return errors.New("the password is a common word with digits or symbols added; choose something less predictable")
			}
		}
	}
	return nil
}

// isRun reports a password made of one character, or of characters that
// each follow the previous by the same step (123456789012, abcdefghijkl,
// 987654321098 wraps and is caught as two runs).
func isRun(s string) bool {
	r := []rune(s)
	same, steps := true, 0
	for i := 1; i < len(r); i++ {
		if r[i] != r[0] {
			same = false
		}
		d := r[i] - r[i-1]
		if d == 1 || d == -1 || d == -9 || d == 9 { // counting up or down, digits wrapping at 0
			steps++
		}
	}
	return same || steps >= len(r)-2
}

// dummyHash is compared against when there is no account to compare with,
// so that an unknown name costs the same time as a wrong password and the
// response time does not tell the two apart.
var dummyHash, _ = bcrypt.GenerateFromPassword([]byte("no such account"), bcrypt.DefaultCost)

// BurnPasswordCheck spends the time a password comparison takes. Call it
// on the path where the account (or client) does not exist.
func BurnPasswordCheck(password string) {
	_ = bcrypt.CompareHashAndPassword(dummyHash, []byte(password))
}
