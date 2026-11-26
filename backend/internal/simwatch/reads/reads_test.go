package reads_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"
	"time"

	"acs/internal/cwmp"
	"acs/internal/devices"
	"acs/internal/simwatch"
	"acs/internal/simwatch/reads"
	"acs/internal/simwatch/recorder"
	"acs/internal/store"
)

func setup(t *testing.T) (context.Context, *sql.DB, *reads.Repository, *recorder.Repository, string) {
	t.Helper()
	dsn := os.Getenv("ACS_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("ACS_TEST_POSTGRES_DSN not set — skipping DB-backed integration test")
	}
	ctx := context.Background()
	db, err := store.Open(ctx, dsn)
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
	key := cwmp.DeviceID{OUI: "001349", ProductClass: "NR7101", SerialNumber: "CPE-RD-01"}.NaturalKey()
	d, err := devices.NewRepository(db).PreRegister(ctx, key, "", "001349", "NR7101", "CPE-RD-01", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return ctx, db, reads.New(db), recorder.New(db), d.ID
}

var (
	idA = simwatch.Identity{ICCID: "8927000000000000011", IMSI: "655010000000011"}
	idB = simwatch.Identity{ICCID: "8927000000000000022", IMSI: "655010000000022"}
)

func TestGetIsNilWithoutARow(t *testing.T) {
	ctx, _, r, _, id := setup(t)
	if s, err := r.Get(ctx, id); err != nil || s != nil {
		t.Fatalf("Get = %+v, %v; want nil, nil", s, err)
	}
}

func TestApproveReturnsNoticeAndAlertToOK(t *testing.T) {
	for _, kind := range []simwatch.Kind{simwatch.KindNotice, simwatch.KindAlert} {
		t.Run(string(kind), func(t *testing.T) {
			ctx, _, r, rec, id := setup(t)
			if err := rec.Record(ctx, id, simwatch.Verdict{Kind: kind, Network: "65501"}, idB, idA, time.Now().UTC()); err != nil {
				t.Fatal(err)
			}
			s, err := r.Get(ctx, id)
			if err != nil || s == nil || s.State != string(kind) || s.Network != "65501" || s.PreviousICCIDHint == "" {
				t.Fatalf("Get = %+v, %v", s, err)
			}
			at := time.Now().UTC().Truncate(time.Second)
			if err := r.Approve(ctx, id, "alice", "Customer swapped the SIM on site", *s.DetectedAt, at); err != nil {
				t.Fatal(err)
			}
			s, _ = r.Get(ctx, id)
			if s.State != "ok" || s.ApprovedBy != "alice" || s.ApprovalReason != "Customer swapped the SIM on site" || s.ApprovedAt == nil || !s.ApprovedAt.Equal(at) {
				t.Fatalf("after approval: %+v", s)
			}
		})
	}
}

func TestApproveWithNothingOpen(t *testing.T) {
	ctx, _, r, rec, id := setup(t)
	if err := r.Approve(ctx, id, "alice", "no row at all here", time.Now(), time.Now()); !errors.Is(err, reads.ErrNothingToApprove) {
		t.Fatalf("no row: %v", err)
	}
	if err := rec.Record(ctx, id, simwatch.Verdict{Kind: simwatch.KindBaseline, Network: "65501"}, idA, simwatch.Identity{}, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := r.Approve(ctx, id, "alice", "already ok on this one", time.Now(), time.Now()); !errors.Is(err, reads.ErrNothingToApprove) {
		t.Fatalf("ok row: %v", err)
	}
}

func TestTheApprovedSIMSeenAgainStaysOK(t *testing.T) {
	ctx, _, r, rec, id := setup(t)
	if err := rec.Record(ctx, id, simwatch.Verdict{Kind: simwatch.KindNotice, Network: "65501"}, idB, idA, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	seen, _ := r.Get(ctx, id)
	if err := r.Approve(ctx, id, "alice", "Customer swapped the SIM on site", *seen.DetectedAt, time.Now()); err != nil {
		t.Fatal(err)
	}
	// The judge sees the same SIM as the cache again: None, which only
	// refreshes the check time.
	if err := rec.Record(ctx, id, simwatch.Verdict{Kind: simwatch.KindNone, Network: "65501"}, idB, idB, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if s, _ := r.Get(ctx, id); s.State != "ok" || s.ApprovedBy != "alice" {
		t.Fatalf("approval lost: %+v", s)
	}
}

// The operator approves the verdict they saw. If the SIM changed again in the
// meantime (a new verdict, a new detection time) the approval is refused.
func TestApproveIsBoundToTheVerdictThatWasSeen(t *testing.T) {
	ctx, _, r, rec, id := setup(t)
	if err := rec.Record(ctx, id, simwatch.Verdict{Kind: simwatch.KindNotice, Network: "65501"}, idB, idA, time.Now().UTC().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	seen, _ := r.Get(ctx, id)
	idC := simwatch.Identity{ICCID: "8927000000000000033", IMSI: "999990000000033"}
	if err := rec.Record(ctx, id, simwatch.Verdict{Kind: simwatch.KindAlert, Network: "99999"}, idC, idB, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := r.Approve(ctx, id, "alice", "Customer swapped the SIM on site", *seen.DetectedAt, time.Now()); !errors.Is(err, reads.ErrNothingToApprove) {
		t.Fatalf("approving a verdict that was replaced: %v, want ErrNothingToApprove", err)
	}
	if s, _ := r.Get(ctx, id); s.State != "alert" {
		t.Fatalf("state = %q, the newer alert must stay open", s.State)
	}
}

// Approving clears what the event claim remembers: a new swap soon after an
// approval is a new verdict and must be announced, not coalesced into the old
// one.
func TestApprovalResetsTheEventClaim(t *testing.T) {
	ctx, _, r, rec, id := setup(t)
	now := time.Now().UTC().Truncate(time.Second)
	if err := rec.Record(ctx, id, simwatch.Verdict{Kind: simwatch.KindNotice, Network: "65501"}, idB, idA, now); err != nil {
		t.Fatal(err)
	}
	if ok, err := rec.ClaimEvent(ctx, id, now, 10*time.Minute); err != nil || !ok {
		t.Fatalf("first claim: %v %v", ok, err)
	}
	seen, _ := r.Get(ctx, id)
	if err := r.Approve(ctx, id, "alice", "Customer swapped the SIM on site", *seen.DetectedAt, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	idC := simwatch.Identity{ICCID: "8927000000000000033", IMSI: "655010000000033"}
	if err := rec.Record(ctx, id, simwatch.Verdict{Kind: simwatch.KindNotice, Network: "65501"}, idC, idB, now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if ok, err := rec.ClaimEvent(ctx, id, now.Add(2*time.Minute), 10*time.Minute); err != nil || !ok {
		t.Fatalf("a new swap after an approval must be announced: %v %v", ok, err)
	}
}
