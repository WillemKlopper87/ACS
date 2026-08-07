// Package mailer sends operator-facing transactional email (currently just
// password-reset links) via any SMTP server — the company's own, or a
// public one like Gmail's, per the user's own framing of the requirement.
// Stdlib net/smtp only, no new dependency for something this small.
//
// "Off unless configured, loud warning when it isn't" — this project's
// existing convention (credential encryption, Digest auth, mTLS, walled
// garden) applies here too: with no SMTP host configured, Send logs the
// message instead of emailing it, which is also just genuinely convenient
// in development (the reset link is right there in the server log, no
// mail server needed to test the flow end to end).
package mailer

import (
	"fmt"
	"log/slog"
	"net/smtp"
)

type Config struct {
	Host     string
	Port     string // e.g. "587"
	Username string
	Password string
	From     string
}

func (c Config) Configured() bool {
	return c.Host != "" && c.From != ""
}

type Mailer struct {
	cfg    Config
	logger *slog.Logger
}

func New(cfg Config, logger *slog.Logger) *Mailer {
	return &Mailer{cfg: cfg, logger: logger}
}

// Configured reports whether a real SMTP host is set — cmd/api's startup
// log uses this to decide whether to print a warning, same pattern as
// credentials.Repository.Encrypted().
func (m *Mailer) Configured() bool {
	return m.cfg.Configured()
}

// Send delivers a plain-text email, or logs it at Info level if no SMTP
// host is configured — see the package doc comment for why that's the
// deliberate fallback, not an error.
func (m *Mailer) Send(to, subject, body string) error {
	if !m.cfg.Configured() {
		m.logger.Info("SMTP not configured — logging email instead of sending", "to", to, "subject", subject, "body", body)
		return nil
	}

	addr := m.cfg.Host + ":" + m.cfg.Port
	msg := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\n\r\n%s\r\n", m.cfg.From, to, subject, body)

	var auth smtp.Auth
	if m.cfg.Username != "" {
		auth = smtp.PlainAuth("", m.cfg.Username, m.cfg.Password, m.cfg.Host)
	}

	if err := smtp.SendMail(addr, auth, m.cfg.From, []string{to}, []byte(msg)); err != nil {
		return fmt.Errorf("send email via %s: %w", addr, err)
	}
	return nil
}
