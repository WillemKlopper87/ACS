// Package mailer sends operator-facing transactional email (currently just
// password-reset links) via any SMTP server — the company's own, or a
// public one like Gmail's, per the user's own framing of the requirement.
// Stdlib net/smtp only, no new dependency for something this small.
//
// "Off unless configured, loud warning when it isn't" — this project's
// existing convention (credential encryption, Digest auth, mTLS, walled
// garden) applies here too: with no SMTP host configured, Send refuses
// delivery. Transactional message bodies can contain bearer reset links and
// must never be written to application logs.
package mailer

import (
	"errors"
	"fmt"
	"log/slog"
	"net/smtp"
)

var ErrNotConfigured = errors.New("SMTP is not configured")

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

// Send delivers a plain-text email. It deliberately refuses an unconfigured
// mailer rather than logging the body, which can contain a password-reset
// bearer token.
func (m *Mailer) Send(to, subject, body string) error {
	if !m.cfg.Configured() {
		return ErrNotConfigured
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
