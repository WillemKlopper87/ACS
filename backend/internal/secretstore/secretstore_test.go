package secretstore

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"acs/internal/store"
)

const testKey = "test-secret-store-key-0123456789abcdef"

func newTestStore(t *testing.T) (context.Context, *Store, *sql.DB) {
	t.Helper()
	dsn := os.Getenv("ACS_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("ACS_TEST_POSTGRES_DSN not set — skipping DB-backed integration test")
	}
	ctx := context.Background()
	db, err := store.OpenUTC(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.ExecContext(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public`); err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	s, err := New(db, testKey)
	if err != nil {
		t.Fatal(err)
	}
	return ctx, s, db
}

func seedCustomer(t *testing.T, ctx context.Context, db *sql.DB, name string) string {
	t.Helper()
	var id string
	if err := db.QueryRowContext(ctx, `INSERT INTO customers (id, name) VALUES (gen_random_uuid(), $1) RETURNING id`, name).Scan(&id); err != nil {
		t.Fatalf("seed customer: %v", err)
	}
	return id
}

func TestNewKey(t *testing.T) {
	if s, err := New(nil, ""); err != nil || s.Enabled() {
		t.Fatalf("empty key: enabled=%v err=%v, want disabled", s.Enabled(), err)
	}
	if _, err := New(nil, "short"); err == nil {
		t.Fatal("a short key was accepted")
	}
	var disabled *Store
	if disabled.Enabled() {
		t.Fatal("nil store reports enabled")
	}
	s, _ := New(nil, "")
	if _, err := s.Put(context.Background(), "", "x", time.Minute); !errors.Is(err, ErrDisabled) {
		t.Fatalf("Put on a keyless store: %v, want ErrDisabled", err)
	}
}

func TestPutClaimOpenOnce(t *testing.T) {
	ctx, s, db := newTestStore(t)
	cust := seedCustomer(t, ctx, db, "isp-a")

	h, err := s.Put(ctx, cust, "canary-passphrase-1", 10*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h.ID, "sh_") || len(h.ID) != 25 {
		t.Fatalf("handle = %q", h.ID)
	}
	if d := time.Until(h.ExpiresAt); d < 9*time.Minute || d > 11*time.Minute {
		t.Fatalf("expires in %s, want about 10m", d)
	}
	var sealed []byte
	if err := db.QueryRowContext(ctx, `SELECT sealed FROM bss_secrets WHERE id = $1`, h.ID).Scan(&sealed); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(sealed), "canary") {
		t.Fatal("the value is stored in clear")
	}

	if err := s.Check(ctx, h.ID, cust); err != nil {
		t.Fatalf("Check: %v", err)
	}
	if err := s.openErr(ctx, h.ID, "dev-1"); err == nil {
		t.Fatal("an unclaimed handle opened")
	}
	if err := s.Claim(ctx, h.ID, cust, "dev-1"); err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if err := s.Claim(ctx, h.ID, cust, "dev-2"); !errors.Is(err, ErrInvalidHandle) {
		t.Fatalf("second Claim: %v, want ErrInvalidHandle", err)
	}
	if err := s.Check(ctx, h.ID, cust); !errors.Is(err, ErrInvalidHandle) {
		t.Fatalf("Check of a claimed handle: %v, want ErrInvalidHandle", err)
	}
	if _, err := s.Open(ctx, h.ID, "dev-2"); !errors.Is(err, ErrHandleGone) {
		t.Fatalf("Open for another device: %v, want ErrHandleGone", err)
	}
	v, err := s.Open(ctx, h.ID, "dev-1")
	if err != nil || v != "canary-passphrase-1" {
		t.Fatalf("Open = %q, %v", v, err)
	}
	if _, err := s.Open(ctx, h.ID, "dev-1"); !errors.Is(err, ErrHandleGone) {
		t.Fatalf("second Open: %v, want ErrHandleGone", err)
	}
	var n int
	_ = db.QueryRowContext(ctx, `SELECT count(*) FROM bss_secrets`).Scan(&n)
	if n != 0 {
		t.Fatalf("%d rows left after use", n)
	}
}

// openErr is Open for a test that wants only the error.
func (s *Store) openErr(ctx context.Context, id, device string) error {
	_, err := s.Open(ctx, id, device)
	return err
}

func TestOwnerIsolation(t *testing.T) {
	ctx, s, db := newTestStore(t)
	a, b := seedCustomer(t, ctx, db, "isp-a"), seedCustomer(t, ctx, db, "isp-b")

	h, err := s.Put(ctx, a, "value-a", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	for _, owner := range []string{b, ""} {
		if err := s.Check(ctx, h.ID, owner); !errors.Is(err, ErrInvalidHandle) {
			t.Fatalf("owner %q Check: %v, want ErrInvalidHandle", owner, err)
		}
		if err := s.Claim(ctx, h.ID, owner, "dev-1"); !errors.Is(err, ErrInvalidHandle) {
			t.Fatalf("owner %q Claim: %v, want ErrInvalidHandle", owner, err)
		}
		if ok, err := s.Discard(ctx, h.ID, owner); ok || err != nil {
			t.Fatalf("owner %q Discard = %v, %v; want false", owner, ok, err)
		}
	}
	global, err := s.Put(ctx, "", "value-global", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Check(ctx, global.ID, a); !errors.Is(err, ErrInvalidHandle) {
		t.Fatalf("a customer used a global handle: %v", err)
	}
	if err := s.Check(ctx, global.ID, ""); err != nil {
		t.Fatalf("global Check: %v", err)
	}
	if ok, err := s.Discard(ctx, h.ID, a); !ok || err != nil {
		t.Fatalf("owner Discard = %v, %v", ok, err)
	}
}

func TestExpiryAndSweep(t *testing.T) {
	ctx, s, db := newTestStore(t)
	h, err := s.Put(ctx, "", "value", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Claim(ctx, h.ID, "", "dev-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE bss_secrets SET expires_at = now() - interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	if err := s.openErr(ctx, h.ID, "dev-1"); !errors.Is(err, ErrHandleGone) {
		t.Fatalf("Open after expiry: %v, want ErrHandleGone", err)
	}
	other, _ := s.Put(ctx, "", "value", time.Minute)
	if n, err := s.Sweep(ctx); n != 1 || err != nil {
		t.Fatalf("Sweep = %d, %v; want 1", n, err)
	}
	if err := s.Check(ctx, other.ID, ""); err != nil {
		t.Fatalf("an unexpired handle was swept: %v", err)
	}
}

func TestSealedValueBoundToRow(t *testing.T) {
	ctx, s, db := newTestStore(t)
	a, _ := s.Put(ctx, "", "value-a", time.Minute)
	b, _ := s.Put(ctx, "", "value-b", time.Minute)
	// Move a's ciphertext onto b: it must not open as b.
	if _, err := db.ExecContext(ctx, `UPDATE bss_secrets SET sealed = (SELECT sealed FROM bss_secrets WHERE id = $1) WHERE id = $2`, a.ID, b.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Claim(ctx, b.ID, "", "dev-1"); err != nil {
		t.Fatal(err)
	}
	if err := s.openErr(ctx, b.ID, "dev-1"); err == nil || errors.Is(err, ErrHandleGone) {
		t.Fatalf("a moved ciphertext opened (err %v)", err)
	}
	// Another key cannot open a row either.
	c, _ := s.Put(ctx, "", "value-c", time.Minute)
	_ = s.Claim(ctx, c.ID, "", "dev-1")
	other, _ := New(db, "another-secret-store-key-0123456789")
	if _, err := other.Open(ctx, c.ID, "dev-1"); err == nil {
		t.Fatal("a row opened under another key")
	}
}
