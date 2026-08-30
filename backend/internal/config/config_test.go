package config

import (
	"log/slog"
	"strings"
	"testing"
)

func discard() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}

func TestValidateMissingSecretFails(t *testing.T) {
	t.Setenv(DevModeEnv, "")
	t.Setenv("ACS_TEST_SECRET", "")
	err := Validate(discard(), Secret{Env: "ACS_TEST_SECRET", MinBytes: 32, Purpose: "signs operator JWTs"})
	if err == nil {
		t.Fatal("Validate() = nil for a missing required secret, want error")
	}
	if !strings.Contains(err.Error(), "ACS_TEST_SECRET") {
		t.Errorf("error %q does not name the missing variable", err)
	}
}

func TestValidatePlaceholderFails(t *testing.T) {
	t.Setenv(DevModeEnv, "")
	for _, v := range []string{"change-me", "CHANGE_ME", "changeme", "password", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"} {
		t.Setenv("ACS_TEST_SECRET", v)
		err := Validate(discard(), Secret{Env: "ACS_TEST_SECRET", MinBytes: 8, Purpose: "test"})
		if err == nil {
			t.Errorf("Validate() = nil for placeholder %q, want error", v)
		}
	}
}

func TestValidateShortSecretFails(t *testing.T) {
	t.Setenv(DevModeEnv, "")
	t.Setenv("ACS_TEST_SECRET", "Kx9$q2Lp")
	err := Validate(discard(), Secret{Env: "ACS_TEST_SECRET", MinBytes: 32, Purpose: "test"})
	if err == nil {
		t.Fatal("Validate() = nil for an 8-byte secret with MinBytes 32, want error")
	}
}

func TestValidateGoodSecretPasses(t *testing.T) {
	t.Setenv(DevModeEnv, "")
	t.Setenv("ACS_TEST_SECRET", "fj2K9x!mQ4vLp8Rt3Yw6Zb1Nc5Gd7He0")
	if err := Validate(discard(), Secret{Env: "ACS_TEST_SECRET", MinBytes: 32, Purpose: "test"}); err != nil {
		t.Fatalf("Validate() = %v for a strong secret, want nil", err)
	}
}

func TestValidateOptionalAbsentPasses(t *testing.T) {
	t.Setenv(DevModeEnv, "")
	t.Setenv("ACS_TEST_SECRET", "")
	if err := Validate(discard(), Secret{Env: "ACS_TEST_SECRET", MinBytes: 32, Purpose: "test", Optional: true}); err != nil {
		t.Fatalf("Validate() = %v for an absent optional secret, want nil", err)
	}
}

func TestValidateOptionalPresentIsStillChecked(t *testing.T) {
	t.Setenv(DevModeEnv, "")
	t.Setenv("ACS_TEST_SECRET", "change-me")
	if err := Validate(discard(), Secret{Env: "ACS_TEST_SECRET", MinBytes: 8, Purpose: "test", Optional: true}); err == nil {
		t.Fatal("Validate() = nil for a placeholder optional secret, want error")
	}
}

func TestValidateAggregatesAllProblems(t *testing.T) {
	t.Setenv(DevModeEnv, "")
	t.Setenv("ACS_TEST_A", "")
	t.Setenv("ACS_TEST_B", "change-me")
	err := Validate(discard(),
		Secret{Env: "ACS_TEST_A", MinBytes: 32, Purpose: "a"},
		Secret{Env: "ACS_TEST_B", MinBytes: 8, Purpose: "b"},
	)
	if err == nil {
		t.Fatal("Validate() = nil, want error naming both problems")
	}
	if !strings.Contains(err.Error(), "ACS_TEST_A") || !strings.Contains(err.Error(), "ACS_TEST_B") {
		t.Errorf("error %q should name both failing variables", err)
	}
}

func TestDevModeBypassesValidation(t *testing.T) {
	t.Setenv(DevModeEnv, "true")
	t.Setenv("ACS_TEST_SECRET", "")
	if err := Validate(discard(), Secret{Env: "ACS_TEST_SECRET", MinBytes: 32, Purpose: "test"}); err != nil {
		t.Fatalf("Validate() = %v in dev mode, want nil", err)
	}
}

func TestDevModeOnlyAcceptsLiteralTrue(t *testing.T) {
	for _, v := range []string{"1", "yes", "TRUE", "True"} {
		t.Setenv(DevModeEnv, v)
		if InsecureDevMode() {
			t.Errorf("InsecureDevMode() = true for %q, want false (only literal \"true\")", v)
		}
	}
}

func TestRequireOneOfNoneSetFails(t *testing.T) {
	t.Setenv(DevModeEnv, "")
	t.Setenv("ACS_TEST_A", "")
	t.Setenv("ACS_TEST_B", "")
	err := RequireOneOf(discard(), "authenticates BSS callers",
		Secret{Env: "ACS_TEST_A", MinBytes: 32, Purpose: "a"},
		Secret{Env: "ACS_TEST_B", MinBytes: 32, Purpose: "b"},
	)
	if err == nil {
		t.Fatal("RequireOneOf() = nil with neither alternative set, want error")
	}
}

func TestRequireOneOfOneGoodPasses(t *testing.T) {
	t.Setenv(DevModeEnv, "")
	t.Setenv("ACS_TEST_A", "fj2K9x!mQ4vLp8Rt3Yw6Zb1Nc5Gd7He0")
	t.Setenv("ACS_TEST_B", "")
	err := RequireOneOf(discard(), "test",
		Secret{Env: "ACS_TEST_A", MinBytes: 32, Purpose: "a"},
		Secret{Env: "ACS_TEST_B", MinBytes: 32, Purpose: "b"},
	)
	if err != nil {
		t.Fatalf("RequireOneOf() = %v with one strong alternative set, want nil", err)
	}
}

func TestRequireOneOfWeakPresentValueFails(t *testing.T) {
	t.Setenv(DevModeEnv, "")
	t.Setenv("ACS_TEST_A", "change-me")
	t.Setenv("ACS_TEST_B", "")
	err := RequireOneOf(discard(), "test",
		Secret{Env: "ACS_TEST_A", MinBytes: 8, Purpose: "a"},
		Secret{Env: "ACS_TEST_B", MinBytes: 8, Purpose: "b"},
	)
	if err == nil {
		t.Fatal("RequireOneOf() = nil for a placeholder value, want error")
	}
}
