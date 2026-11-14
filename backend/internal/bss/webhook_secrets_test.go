package bss

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"
)

const whTestSecret = "a-signing-secret-of-thirty-two-chars-or-more"

func storedSecret(t *testing.T, db *sql.DB, id string) (string, string) {
	t.Helper()
	var secret string
	var previous sql.NullString
	if err := db.QueryRowContext(context.Background(), `SELECT secret, previous_secret FROM webhook_subscriptions WHERE id = $1`, id).Scan(&secret, &previous); err != nil {
		t.Fatal(err)
	}
	return secret, previous.String
}

// With a key, a signing secret is stored encrypted and still signs
// deliveries; without one, nothing changes (finding O8).
func TestWebhookSecretsAreSealedAtRest(t *testing.T) {
	ctx, repo := newMappingTestRepo(t)
	db := repo.db
	plain := NewWebhookRepository(db)
	sealed, err := NewWebhookRepository(db).WithSecretKey("a-key-seed-for-the-test-0123456789")
	if err != nil || !sealed.SealsSecrets() || plain.SealsSecrets() {
		t.Fatalf("sealing: %v", err)
	}

	// Created without a key: stored as given, as before.
	old, err := plain.CreateSubscription(ctx, nil, "https://example.com/old", whTestSecret, []string{"JOB_COMPLETED"})
	if err != nil {
		t.Fatal(err)
	}
	if stored, _ := storedSecret(t, db, old.ID); stored != whTestSecret {
		t.Fatalf("without a key the secret is stored as %q", stored)
	}

	// Created with a key: not in the table in the clear, returned to the
	// creator, absent from listings.
	sub, err := sealed.CreateSubscription(ctx, nil, "https://example.com/new", whTestSecret, []string{"JOB_COMPLETED"})
	if err != nil || sub.Secret != whTestSecret {
		t.Fatalf("create: %+v %v", sub, err)
	}
	stored, _ := storedSecret(t, db, sub.ID)
	if strings.Contains(stored, whTestSecret) || !strings.HasPrefix(stored, sealedPrefix) {
		t.Fatalf("stored secret = %q, want it sealed", stored)
	}
	listed, err := sealed.ListSubscriptions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range listed {
		if s.ID == sub.ID && s.Secret != "" {
			t.Fatalf("a listing carries the sealed subscription's secret: %q", s.Secret)
		}
	}

	// Deliveries are signed with the real secret, for old and new rows.
	for _, id := range []string{old.ID, sub.ID} {
		if err := sealed.EnqueueDelivery(ctx, id, "JOB_COMPLETED", map[string]string{"k": "v"}); err != nil {
			t.Fatal(err)
		}
	}
	due, err := sealed.DueDeliveries(ctx, 10)
	if err != nil || len(due) != 2 {
		t.Fatalf("due = %d %v", len(due), err)
	}
	for _, d := range due {
		if d.Secret != whTestSecret || d.SecretError != "" {
			t.Fatalf("delivery for %s would be signed with %q (%s)", d.SubscriptionID, d.Secret, d.SecretError)
		}
	}
	// A process without the key cannot sign a sealed subscription's
	// delivery, and says so instead of signing with the ciphertext.
	due, _ = plain.DueDeliveries(ctx, 10)
	for _, d := range due {
		if d.SubscriptionID == sub.ID && (d.SecretError == "" || d.Secret != "") {
			t.Fatalf("without the key: secret %q error %q", d.Secret, d.SecretError)
		}
	}

	// Rotation: the new secret is sealed, the previous one still opens.
	const next = "the-next-signing-secret-also-thirty-two-plus"
	if _, err := sealed.RotateSecret(ctx, sub.ID, next, time.Hour); err != nil {
		t.Fatal(err)
	}
	cur, prev := storedSecret(t, db, sub.ID)
	if !strings.HasPrefix(cur, sealedPrefix) || !strings.HasPrefix(prev, sealedPrefix) || strings.Contains(cur+prev, next) {
		t.Fatalf("after rotation: current %q previous %q", cur, prev)
	}
	due, _ = sealed.DueDeliveries(ctx, 10)
	for _, d := range due {
		if d.SubscriptionID == sub.ID && (d.Secret != next || d.PreviousSecret != whTestSecret) {
			t.Fatalf("after rotation a delivery has secret %q previous %q", d.Secret, d.PreviousSecret)
		}
	}

	// The row written before the key existed is sealed in place.
	n, err := sealed.SealStoredSecrets(ctx)
	if err != nil || n != 1 {
		t.Fatalf("sealed %d stored secrets (%v), want the 1 old row", n, err)
	}
	if stored, _ := storedSecret(t, db, old.ID); !strings.HasPrefix(stored, sealedPrefix) {
		t.Fatalf("old row after sealing = %q", stored)
	}
	if n, _ := sealed.SealStoredSecrets(ctx); n != 0 {
		t.Fatalf("a second pass sealed %d more", n)
	}

	// A sealed value moved onto another subscription's row does not open.
	if _, err := db.ExecContext(ctx, `UPDATE webhook_subscriptions SET secret = (SELECT secret FROM webhook_subscriptions WHERE id = $1) WHERE id = $2`, sub.ID, old.ID); err != nil {
		t.Fatal(err)
	}
	due, _ = sealed.DueDeliveries(ctx, 10)
	for _, d := range due {
		if d.SubscriptionID == old.ID && d.SecretError == "" {
			t.Fatal("a secret copied from another subscription opened")
		}
	}
}

func TestWebhookSecretAndSchemeRules(t *testing.T) {
	if err := CheckWebhookSecret("short"); err == nil {
		t.Error("a short signing secret was accepted")
	}
	if err := CheckWebhookSecret(whTestSecret); err != nil {
		t.Errorf("a long secret was refused: %v", err)
	}
	for _, c := range []struct {
		scheme string
		strict bool
		ok     bool
	}{{"https", true, true}, {"https", false, true}, {"http", false, true}, {"http", true, false}, {"ftp", false, false}, {"", true, false}} {
		if err := CheckWebhookScheme(c.scheme, c.strict); (err == nil) != c.ok {
			t.Errorf("scheme %q, https required %v: %v", c.scheme, c.strict, err)
		}
	}
}
