package bss

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

// Webhook signing secrets at rest (docs/SECURITY-ASSESSMENT-2026-11-14.md,
// O8). A signing secret has to be recoverable, because the adapter signs
// every delivery with it, so it cannot be hashed. It was stored as it was
// given: anyone who could read the table could forge events to an ISP's
// receiver. With a key configured (ACS_BSS_SECRET_KEY, the key the sealed
// Wi-Fi handles already use) it is stored encrypted, bound to its
// subscription, and opened only to sign.
//
// Without a key nothing changes: secrets are stored and read as before.
// Rows written before the key was set are read as they are and sealed by
// SealStoredSecrets.

// MinWebhookSecretLength is the shortest signing secret accepted for a
// new subscription or a rotation. Existing shorter ones keep working.
const MinWebhookSecretLength = 32

const sealedPrefix = "sealed:v1:"

// ErrWebhookSecretSealed: a stored secret is encrypted and this process
// has no key to open it with.
var ErrWebhookSecretSealed = errors.New("the webhook signing secret is sealed and no key is configured (ACS_BSS_SECRET_KEY)")

// WithSecretKey makes the repository seal signing secrets under a key
// derived from keySeed. An empty seed leaves sealing off.
func (r *WebhookRepository) WithSecretKey(keySeed string) (*WebhookRepository, error) {
	if keySeed == "" {
		return r, nil
	}
	sum := sha256.Sum256([]byte("acs webhook signing secrets v1\x00" + keySeed))
	block, err := aes.NewCipher(sum[:])
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	r.aead = aead
	return r, nil
}

// SealsSecrets reports whether a key is configured.
func (r *WebhookRepository) SealsSecrets() bool { return r.aead != nil }

// sealSecret encrypts a secret for one subscription. Without a key it is
// returned as it is.
func (r *WebhookRepository) sealSecret(subscriptionID, secret string) (string, error) {
	if r.aead == nil || secret == "" || strings.HasPrefix(secret, sealedPrefix) {
		return secret, nil
	}
	nonce := make([]byte, r.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	// The subscription ID is authenticated with the secret: a sealed
	// value copied onto another subscription's row does not open.
	sealed := r.aead.Seal(nonce, nonce, []byte(secret), []byte(subscriptionID))
	return sealedPrefix + base64.RawStdEncoding.EncodeToString(sealed), nil
}

// openSecret returns the usable secret from a stored value.
func (r *WebhookRepository) openSecret(subscriptionID, stored string) (string, error) {
	encoded, ok := strings.CutPrefix(stored, sealedPrefix)
	if !ok {
		return stored, nil // stored before sealing was on
	}
	if r.aead == nil {
		return "", ErrWebhookSecretSealed
	}
	raw, err := base64.RawStdEncoding.DecodeString(encoded)
	if err != nil || len(raw) < r.aead.NonceSize() {
		return "", errors.New("the stored webhook signing secret is damaged")
	}
	plain, err := r.aead.Open(nil, raw[:r.aead.NonceSize()], raw[r.aead.NonceSize():], []byte(subscriptionID))
	if err != nil {
		return "", errors.New("the stored webhook signing secret does not open with the configured key")
	}
	return string(plain), nil
}

// SealStoredSecrets encrypts the signing secrets that are still stored in
// the clear, current and previous. It returns how many subscriptions it
// changed; with no key it does nothing.
func (r *WebhookRepository) SealStoredSecrets(ctx context.Context) (int, error) {
	if r.aead == nil {
		return 0, nil
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT id::text, secret, COALESCE(previous_secret, '') FROM webhook_subscriptions
		 WHERE secret NOT LIKE $1 OR (previous_secret IS NOT NULL AND previous_secret <> '' AND previous_secret NOT LIKE $1)`, sealedPrefix+"%")
	if err != nil {
		return 0, fmt.Errorf("list unsealed webhook secrets: %w", err)
	}
	type row struct{ id, secret, previous string }
	var todo []row
	for rows.Next() {
		var x row
		if err := rows.Scan(&x.id, &x.secret, &x.previous); err != nil {
			rows.Close()
			return 0, err
		}
		todo = append(todo, x)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	for _, x := range todo {
		secret, err := r.sealSecret(x.id, x.secret)
		if err != nil {
			return 0, err
		}
		previous, err := r.sealSecret(x.id, x.previous)
		if err != nil {
			return 0, err
		}
		// Only if nobody rotated it meanwhile.
		if _, err := r.db.ExecContext(ctx, `
			UPDATE webhook_subscriptions SET secret = $2, previous_secret = NULLIF($3, '')
			 WHERE id = $1 AND secret = $4`, x.id, secret, previous, x.secret); err != nil {
			return 0, fmt.Errorf("seal webhook secret: %w", err)
		}
	}
	return len(todo), nil
}

// CheckWebhookSecret says why a signing secret is not acceptable for a
// new subscription, or nil.
func CheckWebhookSecret(secret string) error {
	if len(secret) < MinWebhookSecretLength {
		return fmt.Errorf("secret must be at least %d characters: it is the only thing that lets the receiver tell a real event from a forged one", MinWebhookSecretLength)
	}
	return nil
}

// CheckWebhookScheme says why a target URL's scheme is not acceptable, or
// nil. Events describe customers' devices and subscribers; outside the
// lab they must not leave unencrypted.
func CheckWebhookScheme(scheme string, requireHTTPS bool) error {
	switch {
	case scheme == "https":
		return nil
	case scheme == "http" && !requireHTTPS:
		return nil
	case scheme == "http":
		return errors.New("target_url must be https: this deployment does not send events over plain http")
	}
	return errors.New("target_url must be a valid http/https URL")
}
