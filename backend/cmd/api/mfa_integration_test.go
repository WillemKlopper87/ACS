package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"acs/internal/mfa"
	"acs/internal/operators"
	"acs/internal/ratelimit"
)

// mfaEnv is a test env with MFA available and a clock the test advances,
// so each check falls in a fresh 30-second step.
func mfaEnv(t *testing.T) (*testEnv, func() time.Time) {
	e := newTestEnv(t)
	// These tests sign in many times in a second; the limits get their own
	// test below.
	savedLogin, savedMFA := loginLimiter, mfaLimiter
	loginLimiter, mfaLimiter = ratelimit.New(1000, 1000, time.Minute), ratelimit.New(1000, 1000, time.Minute)
	t.Cleanup(func() { loginLimiter, mfaLimiter = savedLogin, savedMFA })
	now := time.Unix(1_800_000_000, 0)
	clock := func() time.Time { now = now.Add(2 * time.Minute); return now }
	repo, err := mfa.NewRepository(e.db, "integration-test-encryption-key")
	if err != nil {
		t.Fatal(err)
	}
	// The repository reads the clock once per check; peek returns the
	// time the next check will see.
	e.h.mfa = repo.WithClock(func() time.Time { return now })
	return e, clock
}

func decode(t *testing.T, r resp, v any) {
	t.Helper()
	if err := json.Unmarshal([]byte(r.body), v); err != nil {
		t.Fatalf("decode %q: %v", r.body, err)
	}
}

type loginOut struct {
	Token         string   `json:"token"`
	MFA           string   `json:"mfa"`
	MFAToken      string   `json:"mfa_token"`
	RecoveryCodes []string `json:"recovery_codes"`
}

func (e *testEnv) password(t *testing.T, username string) loginOut {
	t.Helper()
	r := e.call("", "POST", "/api/v1/auth/login", map[string]string{"username": username, "password": "pw-" + username})
	if r.code != 200 {
		t.Fatalf("login %s → %d %s", username, r.code, r.body)
	}
	var out loginOut
	decode(t, r, &out)
	return out
}

// TestIntegration_MFASelfEnrolAndLogin: an operator turns MFA on from
// their session; from then on a password alone yields no session.
func TestIntegration_MFASelfEnrolAndLogin(t *testing.T) {
	e, tick := mfaEnv(t)
	e.operator("root", operators.RoleSuperAdmin)
	e.operator("alice", operators.RoleNOC)

	// Before MFA: password gives a session, as it always did.
	if out := e.password(t, "alice"); out.Token == "" || out.MFA != "" {
		t.Fatalf("login without MFA: %+v", out)
	}

	var enrol struct {
		Secret string `json:"secret"`
		URI    string `json:"otpauth_uri"`
	}
	r := e.call("alice", "POST", "/api/v1/auth/mfa/enroll", nil)
	if r.code != 200 {
		t.Fatalf("enroll → %d %s", r.code, r.body)
	}
	decode(t, r, &enrol)
	if enrol.Secret == "" || !strings.HasPrefix(enrol.URI, "otpauth://totp/ACS:alice?") {
		t.Fatalf("enroll response: %+v", enrol)
	}

	// An unconfirmed enrolment gates nothing.
	if out := e.password(t, "alice"); out.Token == "" {
		t.Fatalf("pending enrolment blocked login: %+v", out)
	}
	if r := e.call("alice", "POST", "/api/v1/auth/mfa/confirm", map[string]string{"code": "000000"}); r.code != 403 {
		t.Errorf("wrong confirm code → %d, want 403", r.code)
	}
	code, _ := mfa.Code(enrol.Secret, tick())
	var confirmed struct {
		RecoveryCodes []string `json:"recovery_codes"`
	}
	r = e.call("alice", "POST", "/api/v1/auth/mfa/confirm", map[string]string{"code": code})
	if r.code != 200 {
		t.Fatalf("confirm → %d %s", r.code, r.body)
	}
	decode(t, r, &confirmed)
	if len(confirmed.RecoveryCodes) != 10 {
		t.Fatalf("got %d recovery codes, want 10", len(confirmed.RecoveryCodes))
	}

	t.Run("password alone no longer yields a session", func(t *testing.T) {
		out := e.password(t, "alice")
		if out.Token != "" || out.MFA != "verify" || out.MFAToken == "" {
			t.Fatalf("login with MFA on: %+v", out)
		}
		// The MFA token is not a session.
		if r := e.call(out.MFAToken, "GET", "/api/v1/devices", nil); r.code != 401 {
			t.Errorf("MFA token used as a session → %d, want 401", r.code)
		}
		if r := e.call("", "POST", "/api/v1/auth/login/mfa", map[string]string{"mfa_token": out.MFAToken, "code": "000000"}); r.code != 401 {
			t.Errorf("wrong code → %d, want 401", r.code)
		}
		good, _ := mfa.Code(enrol.Secret, tick())
		r := e.call("", "POST", "/api/v1/auth/login/mfa", map[string]string{"mfa_token": out.MFAToken, "code": good})
		if r.code != 200 {
			t.Fatalf("right code → %d %s", r.code, r.body)
		}
		var session loginOut
		decode(t, r, &session)
		if r := e.call(session.Token, "GET", "/api/v1/auth/mfa", nil); r.code != 200 || !strings.Contains(r.body, `"enabled":true`) {
			t.Errorf("session after MFA → %d %s", r.code, r.body)
		}
		// The same code again is refused: a code works once.
		again := e.password(t, "alice")
		if r := e.call("", "POST", "/api/v1/auth/login/mfa", map[string]string{"mfa_token": again.MFAToken, "code": good}); r.code != 401 {
			t.Errorf("replayed code → %d, want 401", r.code)
		}
	})

	t.Run("a recovery code works once", func(t *testing.T) {
		rc := confirmed.RecoveryCodes[0]
		out := e.password(t, "alice")
		if r := e.call("", "POST", "/api/v1/auth/login/mfa", map[string]string{"mfa_token": out.MFAToken, "code": strings.ToLower(rc)}); r.code != 200 {
			t.Fatalf("recovery code → %d %s", r.code, r.body)
		}
		out = e.password(t, "alice")
		if r := e.call("", "POST", "/api/v1/auth/login/mfa", map[string]string{"mfa_token": out.MFAToken, "code": rc}); r.code != 401 {
			t.Errorf("reused recovery code → %d, want 401", r.code)
		}
		if r := e.call("alice", "GET", "/api/v1/auth/mfa", nil); !strings.Contains(r.body, `"recovery_codes_left":9`) {
			t.Errorf("status: %s", r.body)
		}
	})

	t.Run("the token of one step is not accepted by another", func(t *testing.T) {
		out := e.password(t, "alice")
		if r := e.call("", "POST", "/api/v1/auth/login/mfa/enroll", map[string]string{"mfa_token": out.MFAToken}); r.code != 401 {
			t.Errorf("verify token on the enrol route → %d, want 401", r.code)
		}
		if r := e.call("", "POST", "/api/v1/auth/login/mfa", map[string]string{"mfa_token": e.tokens["alice"], "code": "123456"}); r.code != 401 {
			t.Errorf("session token as an MFA token → %d, want 401", r.code)
		}
	})

	t.Run("turning it off needs a current code", func(t *testing.T) {
		if r := e.call("alice", "POST", "/api/v1/auth/mfa/disable", map[string]string{"code": "000000"}); r.code != 403 {
			t.Errorf("disable with a wrong code → %d, want 403 (the session is still good)", r.code)
		}
		if r := e.call("alice", "POST", "/api/v1/auth/mfa/enroll", nil); r.code != 409 {
			t.Errorf("enrol while enabled → %d, want 409", r.code)
		}
	})

	t.Run("the operator list shows who has MFA, and an admin can reset it", func(t *testing.T) {
		r := e.call("root", "GET", "/api/v1/auth/operators", nil)
		var list struct {
			Items []struct {
				ID, Username string
				MFAEnabled   bool `json:"mfa_enabled"`
			} `json:"items"`
		}
		decode(t, r, &list)
		var aliceID string
		for _, op := range list.Items {
			if op.Username == "alice" {
				aliceID = op.ID
				if !op.MFAEnabled {
					t.Error("alice not listed with MFA on")
				}
			} else if op.MFAEnabled {
				t.Errorf("%s listed with MFA on", op.Username)
			}
		}
		if r := e.call("alice", "DELETE", "/api/v1/auth/operators/"+aliceID+"/mfa", nil); r.code != 403 {
			t.Errorf("non-admin reset → %d, want 403", r.code)
		}
		if r := e.call("root", "DELETE", "/api/v1/auth/operators/"+aliceID+"/mfa", nil); r.code != 204 {
			t.Fatalf("admin reset → %d %s", r.code, r.body)
		}
		// Her old session is gone, and a password is enough again.
		if r := e.call("alice", "GET", "/api/v1/auth/mfa", nil); r.code != 401 {
			t.Errorf("session after reset → %d, want 401", r.code)
		}
		if out := e.password(t, "alice"); out.Token == "" {
			t.Errorf("login after reset: %+v", out)
		}
	})

	t.Run("recovery codes never reach the audit log", func(t *testing.T) {
		var n int
		if err := e.db.QueryRowContext(e.ctx, `SELECT count(*) FROM audit_log WHERE details::text LIKE '%' || $1 || '%'`, confirmed.RecoveryCodes[1]).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Errorf("a recovery code is in %d audit rows", n)
		}
	})
}

// TestIntegration_MFARequiredRole: a role that must use MFA cannot get a
// session until it has enrolled, and cannot turn MFA off afterwards.
func TestIntegration_MFARequiredRole(t *testing.T) {
	e, tick := mfaEnv(t)
	e.h.mfaRequired = mfaRequiredRoles("superadmin")
	e.operator("root", operators.RoleSuperAdmin)
	e.operator("alice", operators.RoleNOC)

	if out := e.password(t, "alice"); out.Token == "" {
		t.Fatalf("a role not required to use MFA was challenged: %+v", out)
	}

	out := e.password(t, "root")
	if out.Token != "" || out.MFA != "enroll" || out.MFAToken == "" {
		t.Fatalf("required role without MFA: %+v", out)
	}
	// The enrolment token cannot be used to verify.
	if r := e.call("", "POST", "/api/v1/auth/login/mfa", map[string]string{"mfa_token": out.MFAToken, "code": "123456"}); r.code != 401 {
		t.Errorf("enrol token on the verify route → %d, want 401", r.code)
	}
	var enrol struct {
		Secret string `json:"secret"`
	}
	r := e.call("", "POST", "/api/v1/auth/login/mfa/enroll", map[string]string{"mfa_token": out.MFAToken})
	if r.code != 200 {
		t.Fatalf("enroll at login → %d %s", r.code, r.body)
	}
	decode(t, r, &enrol)
	code, _ := mfa.Code(enrol.Secret, tick())
	r = e.call("", "POST", "/api/v1/auth/login/mfa/confirm", map[string]string{"mfa_token": out.MFAToken, "code": code})
	if r.code != 200 {
		t.Fatalf("confirm at login → %d %s", r.code, r.body)
	}
	var session loginOut
	decode(t, r, &session)
	if session.Token == "" || len(session.RecoveryCodes) != 10 {
		t.Fatalf("session after enrolment: %+v", session)
	}

	if out := e.password(t, "root"); out.MFA != "verify" {
		t.Errorf("next login: %+v, want a code prompt", out)
	}
	off, _ := mfa.Code(enrol.Secret, tick())
	if r := e.call(session.Token, "POST", "/api/v1/auth/mfa/disable", map[string]string{"code": off}); r.code != 409 {
		t.Errorf("required role turning MFA off → %d, want 409", r.code)
	}
}

func TestMFARequiredRolesParsing(t *testing.T) {
	if got := mfaRequiredRoles(" superadmin, Manager ,,"); !got["superadmin"] || !got["manager"] || len(got) != 2 {
		t.Errorf("list: %v", got)
	}
	if got := mfaRequiredRoles("all"); len(got) != 4 {
		t.Errorf("all: %v", got)
	}
	if got := mfaRequiredRoles(""); len(got) != 0 {
		t.Errorf("empty: %v", got)
	}
}

// TestIntegration_MFAGuessLimit: a six-digit code is only safe if it
// cannot be tried many times.
func TestIntegration_MFAGuessLimit(t *testing.T) {
	e, tick := mfaEnv(t)
	mfaLimiter = ratelimit.New(1.0/12, 5, 30*time.Minute) // the production limit
	e.operator("alice", operators.RoleNOC)
	var enrol struct {
		Secret string `json:"secret"`
	}
	decode(t, e.call("alice", "POST", "/api/v1/auth/mfa/enroll", nil), &enrol)
	code, _ := mfa.Code(enrol.Secret, tick())
	if r := e.call("alice", "POST", "/api/v1/auth/mfa/confirm", map[string]string{"code": code}); r.code != 200 {
		t.Fatalf("confirm → %d %s", r.code, r.body)
	}
	out := e.password(t, "alice")
	last := 0
	for i := 0; i < 8; i++ {
		last = e.call("", "POST", "/api/v1/auth/login/mfa", map[string]string{"mfa_token": out.MFAToken, "code": "000000"}).code
	}
	if last != 429 {
		t.Fatalf("eighth wrong code → %d, want 429", last)
	}
	// Even the right code is refused while locked out.
	good, _ := mfa.Code(enrol.Secret, tick())
	if r := e.call("", "POST", "/api/v1/auth/login/mfa", map[string]string{"mfa_token": out.MFAToken, "code": good}); r.code != 429 {
		t.Errorf("right code during lockout → %d, want 429", r.code)
	}
}
