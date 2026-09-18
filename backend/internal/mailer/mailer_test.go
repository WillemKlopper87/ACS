package mailer

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

func TestConfigConfigured(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
		want bool
	}{
		{"empty", Config{}, false},
		{"host only", Config{Host: "smtp.example.com"}, false},
		{"from only", Config{From: "acs@example.com"}, false},
		{"host and from", Config{Host: "smtp.example.com", From: "acs@example.com"}, true},
	}
	for _, c := range cases {
		if got := c.cfg.Configured(); got != c.want {
			t.Errorf("%s: Configured() = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestSendUnconfiguredRefusesWithoutLoggingSecrets(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	m := New(Config{}, logger)

	if m.Configured() {
		t.Fatal("an empty Config must report Configured() == false")
	}

	err := m.Send("operator@example.com", "Password reset", "reset link: https://example.com/reset?token=abc")
	if !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("Send with no SMTP configured error = %v, want ErrNotConfigured", err)
	}

	logged := buf.String()
	if strings.Contains(logged, "reset link") || strings.Contains(logged, "token=abc") {
		t.Errorf("unconfigured Send logged sensitive content: %s", logged)
	}
}

func TestSendConfiguredAttemptsRealDelivery(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	// A host that will refuse the connection immediately (port 1 is never
	// a real SMTP listener) — this test only asserts Send() actually tries
	// the network path instead of silently falling back to logging once
	// configured, not that delivery succeeds.
	m := New(Config{Host: "127.0.0.1", Port: "1", From: "acs@example.com"}, logger)

	if !m.Configured() {
		t.Fatal("a Config with Host and From must report Configured() == true")
	}

	err := m.Send("operator@example.com", "subject", "body")
	if err == nil {
		t.Fatal("Send against an unreachable SMTP host should return an error, not silently succeed")
	}
	if buf.Len() != 0 {
		t.Errorf("a configured Mailer should not fall back to logging the message, got log output: %s", buf.String())
	}
}
