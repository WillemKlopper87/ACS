package subscribers

import (
	"acs/internal/accounts/accountstest"
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"testing"

	"acs/internal/store"
)

func newDB(t *testing.T) (context.Context, *sql.DB) {
	t.Helper()
	dsn := os.Getenv("ACS_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("ACS_TEST_POSTGRES_DSN not set — skipping DB-backed test")
	}
	ctx := context.Background()
	db, err := store.OpenUTC(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.ExecContext(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public`); err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	return ctx, db
}

// seed creates a customer and, per row, a device, its site and account.
func seed(t *testing.T, ctx context.Context, db *sql.DB, cust, serial, account, suburb string) string {
	t.Helper()
	var site, dev string
	if err := db.QueryRowContext(ctx, `INSERT INTO sites (id, customer_id, name, street, suburb, city)
		VALUES (gen_random_uuid(), $1, 'Home', '12 Oak Street', $2, 'Johannesburg') RETURNING id`, cust, suburb).Scan(&site); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `INSERT INTO devices (id, oui_serial, oui, product_class, serial_number, manufacturer, customer_id, site_id)
		VALUES (gen_random_uuid(), $1, 'A4FC77', 'FastMile', $2, 'Nokia', $3, $4) RETURNING id`, "A4FC77-FastMile-"+serial, serial, cust, site).Scan(&dev); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, accountstest.EnsureCTE("$1")+`INSERT INTO account_device_mappings (id, account_id, account_key, device_id, oui_serial, role, assigned_at)
		VALUES (gen_random_uuid(), $1, (SELECT id FROM acct), $2, $3, 'gateway', now())`, account, dev, "A4FC77-FastMile-"+serial); err != nil {
		t.Fatal(err)
	}
	return dev
}

func TestLookupAndDisplayNames(t *testing.T) {
	ctx, db := newDB(t)
	r := NewRepository(db)
	var custA, custB string
	for _, c := range []*string{&custA, &custB} {
		if err := db.QueryRowContext(ctx, `INSERT INTO customers (id, name) VALUES (gen_random_uuid(), 'isp') RETURNING id`).Scan(c); err != nil {
			t.Fatal(err)
		}
	}
	smith1 := seed(t, ctx, db, custA, "ALCL0000A1B2", "ACC-1001", "Sandton")
	smith2 := seed(t, ctx, db, custA, "ALCL0000C3D4", "ACC-1002", "Soweto")
	other := seed(t, ctx, db, custB, "ALCL0000E5F6", "ACC-2001", "Sandton")
	name := func(s string) *string { return &s }
	vip := true
	for acc, n := range map[string]string{"ACC-1001": "Jane Smith", "ACC-1002": "John Smith", "ACC-2001": "Jane Smith"} {
		if _, err := r.Set(ctx, acc, SourceBSS, "bss", name(n), nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := r.Set(ctx, "ACC-1002", SourceOperator, "op", nil, &vip); err != nil {
		t.Fatal(err)
	}

	sums, err := r.Summaries(ctx, []string{smith1})
	if err != nil {
		t.Fatal(err)
	}
	if got := sums[smith1].DisplayName; got != "Jane Smith · ACC-1001 · Sandton · FastMile …A1B2" {
		t.Fatalf("display name = %q", got)
	}

	find := func(q string, vipOnly bool, scope ...string) []Hit {
		t.Helper()
		hits, err := r.Lookup(ctx, LookupParams{Query: q, VIPOnly: vipOnly, CustomerIDs: scope, Scoped: len(scope) > 0})
		if err != nil {
			t.Fatal(err)
		}
		return hits
	}
	ids := func(hits []Hit) string {
		var out []string
		for _, h := range hits {
			out = append(out, h.DeviceID)
		}
		return strings.Join(out, ",")
	}

	// Two Smiths in scope, told apart by account and suburb.
	if hits := find("smith", false, custA); len(hits) != 2 || !PersonalMatch(hits) {
		t.Fatalf("smith in A = %+v", hits)
	}
	// Name plus suburb narrows to one; scope hides the other customer.
	if hits := find("jane sandton", false, custA); ids(hits) != smith1 {
		t.Fatalf("jane sandton in A = %s", ids(hits))
	}
	if hits := find("jane sandton", false); len(hits) != 2 {
		t.Fatalf("jane sandton unscoped = %d hits", len(hits))
	}
	// Last serial digits and account number rank first and are not personal.
	if hits := find("c3d4", false, custA); ids(hits) != smith2 || hits[0].Matched[0] != MatchSerial || PersonalMatch(hits) {
		t.Fatalf("serial digits = %+v", hits)
	}
	if hits := find("ACC-1001", false, custA, custB); ids(hits) != smith1 {
		t.Fatalf("account = %s", ids(hits))
	}
	// Address words.
	if hits := find("oak street soweto", false, custA); ids(hits) != smith2 {
		t.Fatalf("address = %s", ids(hits))
	}
	// VIP list.
	if hits := find("", true, custA, custB); ids(hits) != smith2 || !hits[0].VIP {
		t.Fatalf("vip list = %+v", hits)
	}
	// LIKE wildcards in the query are literal.
	if hits := find("%", false, custA); len(hits) != 0 {
		t.Fatalf("%% matched %d", len(hits))
	}
	_ = other

	// The BSS name wins: an operator cannot overwrite or clear it.
	if _, err := r.Set(ctx, "ACC-1001", SourceOperator, "op", name("Someone Else"), nil); !errors.Is(err, ErrNameFromBSS) {
		t.Fatalf("operator over BSS name: %v", err)
	}
	if _, err := r.Set(ctx, "ACC-3000", SourceOperator, "op", name("Filled In"), nil); err != nil {
		t.Fatalf("operator fills a missing name: %v", err)
	}
	if p, _ := r.Set(ctx, "ACC-3000", SourceBSS, "bss", name("From Billing"), nil); p == nil || *p.DisplayName != "From Billing" || p.NameSource != SourceBSS {
		t.Fatalf("BSS replaces operator name: %+v", p)
	}
}
