package retention

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

// TestPruneEvents: events are removed per customer as a prefix of the
// sequence, never past an event that is still recent, and purged_through
// records where each stream now starts.
func TestPruneEvents(t *testing.T) {
	ctx, db := newMigratedDB(t)
	var custA string
	if err := db.QueryRowContext(ctx, `INSERT INTO customers (id, name) VALUES (gen_random_uuid(), 'isp-a') RETURNING id`).Scan(&custA); err != nil {
		t.Fatal(err)
	}
	old, recent := time.Now().UTC().Add(-100*24*time.Hour), time.Now().UTC().Add(-time.Hour)
	insert := func(customer any, at time.Time) {
		t.Helper()
		if _, err := db.ExecContext(ctx, `INSERT INTO tmf_events (id, source_key, customer_id, event_type, event_time) VALUES ($1, $2, $3, 'DeviceSeen', $4)`,
			uuid.New(), uuid.NewString(), customer, at); err != nil {
			t.Fatal(err)
		}
	}
	// Customer A: seq 1-2 old, 3 recent, 4 old (a late arrival), 5 recent.
	for _, at := range []time.Time{old, old, recent, old, recent} {
		insert(custA, at)
	}
	// No customer: everything old.
	for range 3 {
		insert(nil, old)
	}

	res, err := Run(ctx, db, Policy{TMFEventsDays: 90})
	if err != nil {
		t.Fatal(err)
	}
	if res["tmf_events"] != 5 {
		t.Fatalf("pruned %d events, want 5", res["tmf_events"])
	}
	seqs := func(scope string) (left []int64, purged int64) {
		rows, err := db.QueryContext(ctx, `SELECT seq FROM tmf_events WHERE COALESCE(customer_id::text, '') = $1 ORDER BY seq`, scope)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		for rows.Next() {
			var s int64
			_ = rows.Scan(&s)
			left = append(left, s)
		}
		_ = db.QueryRowContext(ctx, `SELECT purged_through FROM tmf_event_sequences WHERE scope = $1`, scope).Scan(&purged)
		return left, purged
	}
	if left, purged := seqs(custA); len(left) != 3 || left[0] != 3 || purged != 2 {
		t.Fatalf("customer A: left %v, purged_through %d; want [3 4 5] and 2", left, purged)
	}
	if left, purged := seqs(""); len(left) != 0 || purged != 3 {
		t.Fatalf("no customer: left %v, purged_through %d; want none and 3", left, purged)
	}

	// A second pass removes nothing more; disabled, nothing at all.
	if res, _ := Run(ctx, db, Policy{TMFEventsDays: 90}); res["tmf_events"] != 0 {
		t.Fatalf("second pass pruned %d", res["tmf_events"])
	}
	if res, _ := Run(ctx, db, Policy{}); res["tmf_events"] != 0 {
		t.Fatalf("disabled policy pruned %d", res["tmf_events"])
	}
	// New events keep numbering after the purged range.
	insert(nil, recent)
	if left, purged := seqs(""); len(left) != 1 || left[0] != 4 || purged != 3 {
		t.Fatalf("after a new event: left %v, purged_through %d", left, purged)
	}
}
