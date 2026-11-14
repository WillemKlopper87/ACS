package mfa

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

const recoveryCodeCount = 10

var (
	// ErrUnavailable: no encryption key is configured, so a secret cannot
	// be stored safely and MFA cannot be enrolled.
	ErrUnavailable = errors.New("MFA needs ACS_CREDENTIAL_ENCRYPTION_KEY to be set")
	// ErrNotEnrolled: the operator has no enrolment (or none in progress).
	ErrNotEnrolled = errors.New("MFA is not set up for this operator")
	// ErrAlreadyEnabled: MFA is on; turn it off before enrolling again.
	ErrAlreadyEnabled = errors.New("MFA is already enabled")
	// ErrInvalidCode: the code is wrong, expired or already used.
	ErrInvalidCode = errors.New("invalid or already used code")
)

// Status is what the console shows about an operator's MFA.
type Status struct {
	Enabled           bool
	RecoveryCodesLeft int
}

// Repository stores sealed secrets and recovery-code hashes.
type Repository struct {
	db   *sql.DB
	aead cipher.AEAD // nil when no key is configured
	now  func() time.Time
}

// NewRepository derives the sealing key from keySeed (the value of
// ACS_CREDENTIAL_ENCRYPTION_KEY). An empty seed gives a repository that
// can read status but refuses enrolment.
func NewRepository(db *sql.DB, keySeed string) (*Repository, error) {
	r := &Repository{db: db, now: time.Now}
	if keySeed == "" {
		return r, nil
	}
	// A purpose label keeps this key distinct from other uses of the seed.
	key := sha256.Sum256([]byte("acs-operator-mfa-v1|" + keySeed))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, fmt.Errorf("init MFA cipher: %w", err)
	}
	if r.aead, err = cipher.NewGCM(block); err != nil {
		return nil, fmt.Errorf("init MFA cipher: %w", err)
	}
	return r, nil
}

// Available reports whether enrolment is possible.
func (r *Repository) Available() bool { return r.aead != nil }

func (r *Repository) seal(operatorID, secret string) (string, error) {
	nonce := make([]byte, r.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("seal MFA secret: %w", err)
	}
	return base64.StdEncoding.EncodeToString(r.aead.Seal(nonce, nonce, []byte(secret), []byte(operatorID))), nil
}

func (r *Repository) open(operatorID, sealed string) (string, error) {
	if r.aead == nil {
		return "", ErrUnavailable
	}
	raw, err := base64.StdEncoding.DecodeString(sealed)
	if err != nil || len(raw) < r.aead.NonceSize() {
		return "", errors.New("stored MFA secret is malformed")
	}
	plain, err := r.aead.Open(nil, raw[:r.aead.NonceSize()], raw[r.aead.NonceSize():], []byte(operatorID))
	if err != nil {
		return "", errors.New("stored MFA secret cannot be opened with the configured key")
	}
	return string(plain), nil
}

// Status reports whether MFA is enabled and how many recovery codes are left.
func (r *Repository) Status(ctx context.Context, operatorID string) (Status, error) {
	var s Status
	err := r.db.QueryRowContext(ctx, `
		SELECT EXISTS (SELECT 1 FROM operator_mfa WHERE operator_id = $1 AND confirmed_at IS NOT NULL),
		       (SELECT COUNT(*) FROM operator_mfa_recovery_codes WHERE operator_id = $1 AND used_at IS NULL)`,
		operatorID).Scan(&s.Enabled, &s.RecoveryCodesLeft)
	if err != nil {
		return s, fmt.Errorf("read MFA status: %w", err)
	}
	return s, nil
}

// EnabledSet returns the IDs of the operators that have MFA on.
func (r *Repository) EnabledSet(ctx context.Context) (map[string]bool, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT operator_id FROM operator_mfa WHERE confirmed_at IS NOT NULL`)
	if err != nil {
		return nil, fmt.Errorf("list MFA operators: %w", err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// Begin starts (or restarts) an enrolment and returns the new secret. The
// secret gates nothing until Confirm proves the app shows the right code.
func (r *Repository) Begin(ctx context.Context, operatorID string) (string, error) {
	if r.aead == nil {
		return "", ErrUnavailable
	}
	secret, err := NewSecret()
	if err != nil {
		return "", err
	}
	sealed, err := r.seal(operatorID, secret)
	if err != nil {
		return "", err
	}
	res, err := r.db.ExecContext(ctx, `
		INSERT INTO operator_mfa (operator_id, secret_enc) VALUES ($1, $2)
		ON CONFLICT (operator_id) DO UPDATE SET secret_enc = EXCLUDED.secret_enc, last_used_step = 0, created_at = now()
		WHERE operator_mfa.confirmed_at IS NULL`, operatorID, sealed)
	if err != nil {
		return "", fmt.Errorf("store MFA enrolment: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return "", ErrAlreadyEnabled
	}
	return secret, nil
}

// Confirm turns MFA on once code matches the pending secret, and returns
// a fresh set of recovery codes. They are shown once; only hashes are kept.
func (r *Repository) Confirm(ctx context.Context, operatorID, code string) ([]string, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin MFA confirm: %w", err)
	}
	defer tx.Rollback()

	var sealed string
	var confirmed sql.NullTime
	err = tx.QueryRowContext(ctx, `SELECT secret_enc, confirmed_at FROM operator_mfa WHERE operator_id = $1 FOR UPDATE`, operatorID).Scan(&sealed, &confirmed)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotEnrolled
	}
	if err != nil {
		return nil, fmt.Errorf("read MFA enrolment: %w", err)
	}
	if confirmed.Valid {
		return nil, ErrAlreadyEnabled
	}
	secret, err := r.open(operatorID, sealed)
	if err != nil {
		return nil, err
	}
	matched, ok := Verify(secret, code, r.now(), 0)
	if !ok {
		return nil, ErrInvalidCode
	}
	if _, err := tx.ExecContext(ctx, `UPDATE operator_mfa SET confirmed_at = now(), last_used_step = $2 WHERE operator_id = $1`, operatorID, matched); err != nil {
		return nil, fmt.Errorf("enable MFA: %w", err)
	}
	codes, err := replaceRecoveryCodes(ctx, tx, operatorID)
	if err != nil {
		return nil, err
	}
	return codes, tx.Commit()
}

// Check verifies a login code: a six-digit app code, or a recovery code.
// Either is accepted once only. usedRecovery says which it was.
func (r *Repository) Check(ctx context.Context, operatorID, code string) (usedRecovery bool, err error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin MFA check: %w", err)
	}
	defer tx.Rollback()

	var sealed string
	var lastUsed int64
	err = tx.QueryRowContext(ctx, `
		SELECT secret_enc, last_used_step FROM operator_mfa
		WHERE operator_id = $1 AND confirmed_at IS NOT NULL FOR UPDATE`, operatorID).Scan(&sealed, &lastUsed)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrNotEnrolled
	}
	if err != nil {
		return false, fmt.Errorf("read MFA secret: %w", err)
	}

	normalized := NormalizeCode(code)
	if len(normalized) == digits {
		secret, err := r.open(operatorID, sealed)
		if err != nil {
			return false, err
		}
		matched, ok := Verify(secret, normalized, r.now(), lastUsed)
		if !ok {
			return false, ErrInvalidCode
		}
		if _, err := tx.ExecContext(ctx, `UPDATE operator_mfa SET last_used_step = $2 WHERE operator_id = $1`, operatorID, matched); err != nil {
			return false, fmt.Errorf("record MFA use: %w", err)
		}
		return false, tx.Commit()
	}

	res, err := tx.ExecContext(ctx, `
		UPDATE operator_mfa_recovery_codes SET used_at = now()
		WHERE operator_id = $1 AND code_hash = $2 AND used_at IS NULL`, operatorID, hashRecoveryCode(normalized))
	if err != nil {
		return false, fmt.Errorf("use recovery code: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return false, ErrInvalidCode
	}
	return true, tx.Commit()
}

// Disable removes an operator's MFA and recovery codes.
func (r *Repository) Disable(ctx context.Context, operatorID string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin MFA disable: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM operator_mfa_recovery_codes WHERE operator_id = $1`, operatorID); err != nil {
		return fmt.Errorf("delete recovery codes: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM operator_mfa WHERE operator_id = $1`, operatorID); err != nil {
		return fmt.Errorf("delete MFA: %w", err)
	}
	return tx.Commit()
}

// RegenerateRecoveryCodes replaces the operator's recovery codes.
func (r *Repository) RegenerateRecoveryCodes(ctx context.Context, operatorID string) ([]string, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin recovery code regeneration: %w", err)
	}
	defer tx.Rollback()
	var enabled bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM operator_mfa WHERE operator_id = $1 AND confirmed_at IS NOT NULL)`, operatorID).Scan(&enabled); err != nil {
		return nil, fmt.Errorf("read MFA status: %w", err)
	}
	if !enabled {
		return nil, ErrNotEnrolled
	}
	codes, err := replaceRecoveryCodes(ctx, tx, operatorID)
	if err != nil {
		return nil, err
	}
	return codes, tx.Commit()
}

func replaceRecoveryCodes(ctx context.Context, tx *sql.Tx, operatorID string) ([]string, error) {
	if _, err := tx.ExecContext(ctx, `DELETE FROM operator_mfa_recovery_codes WHERE operator_id = $1`, operatorID); err != nil {
		return nil, fmt.Errorf("clear recovery codes: %w", err)
	}
	codes := make([]string, 0, recoveryCodeCount)
	for len(codes) < recoveryCodeCount {
		code, err := newRecoveryCode()
		if err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO operator_mfa_recovery_codes (operator_id, code_hash) VALUES ($1, $2) ON CONFLICT DO NOTHING`,
			operatorID, hashRecoveryCode(NormalizeCode(code))); err != nil {
			return nil, fmt.Errorf("store recovery code: %w", err)
		}
		codes = append(codes, code)
	}
	return codes, nil
}

// newRecoveryCode returns 50 random bits as "XXXXX-XXXXX". A recovery code
// is never six characters, so it cannot be mistaken for an app code.
func newRecoveryCode() (string, error) {
	buf := make([]byte, 7)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate recovery code: %w", err)
	}
	s := b32.EncodeToString(buf)[:10]
	return s[:5] + "-" + s[5:], nil
}

func hashRecoveryCode(normalized string) string {
	sum := sha256.Sum256([]byte(strings.ToUpper(normalized)))
	return hex.EncodeToString(sum[:])
}

// WithClock replaces the repository's time source. Tests use it to move
// between 30-second steps without waiting.
func (r *Repository) WithClock(now func() time.Time) *Repository {
	r.now = now
	return r
}
