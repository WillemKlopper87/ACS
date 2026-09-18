package diagnostics

import "testing"

func TestValidateTR143Target(t *testing.T) {
	if err := ValidateTR143Target("https://diag.example.test/file.bin", []string{"diag.example.test"}); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{
		"http://diag.example.test/file.bin",
		"https://evil.example.test/file.bin",
		"https://user:pass@diag.example.test/file.bin",
		"https://127.0.0.1/file.bin",
	} {
		if err := ValidateTR143Target(raw, []string{"diag.example.test", "127.0.0.1"}); err == nil {
			t.Errorf("ValidateTR143Target(%q) accepted unsafe target", raw)
		}
	}
}
