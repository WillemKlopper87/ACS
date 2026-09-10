# Fleet Data Model Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make device assignment addressable by role and temporal, so BSS orders target a specific device deterministically and device swaps preserve history.

**Architecture:** `account_device_mappings` is evolved in place — four new columns, the pair-unique constraint replaced by two partial unique indexes over active rows. `internal/bss` gains role-aware read and write operations and loses the arbitrary-tiebreak `PrimaryDeviceForAccount`. The two callers (BSS adapter, admin API) are rewired, and CI is extended so the DB-backed constraint tests actually run.

**Tech Stack:** Go 1.26+, PostgreSQL 18 (partial unique indexes), `database/sql` over pgx stdlib, `github.com/jackc/pgx/v5/pgconn` for error-code inspection (already a dependency).

**Spec:** [`docs/superpowers/specs/2026-09-09-fleet-data-model-design.md`](../specs/2026-09-09-fleet-data-model-design.md) — the plan argues from it; read both.

## Global Constraints

- Go module is `acs`; Go directive `go 1.26.6`. Do not raise it.
- No new third-party dependencies. `pgconn` is part of the existing `pgx/v5` module.
- Migrations are forward-only, embedded via `//go:embed all:migrations`, and checksum-verified at startup. **Never edit a migration file after it is committed.**
- Before every commit, from `backend/`: `gofmt -l .` prints nothing; `go vet ./...` and `go test ./...` pass. DB-backed tests skip without `ACS_TEST_POSTGRES_DSN` and must be run with it set at least once per task that adds one.
- DB-backed tests reset the schema (`DROP SCHEMA public CASCADE`), so run them with `-p 1`.
- Role values: `gateway`, `ont`, `extender`, `stb`, `ata`, `other`. Unassign reasons: `rma`, `upgrade`, `return`, `moved`, `corrected`. Both are DB `CHECK` constraints and Go constants; keep them in sync.
- Nothing may read `status` to decide whether a device currently serves an account. `unassigned_at IS NULL` is that answer (spec §5.4).
- Commit message style: `type(scope): summary`, imperative mood. End each with `Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>`.

**Running the DB-backed tests locally:**
```bash
# from repo root; both Grafana vars only satisfy compose's whole-file interpolation
GRAFANA_ADMIN_PASSWORD=unused-postgres-only ACS_GRAFANA_DB_PASSWORD=unused-postgres-only \
  docker compose -f infra/docker-compose.yml up -d postgres
# from backend/
ACS_TEST_POSTGRES_DSN="postgres://acs:acs@127.0.0.1:5432/acs?sslmode=disable" go test -count=1 -p 1 ./internal/bss/
```

---

### Task 1: Migration — role, temporal columns, partial unique indexes

**Ruling on spec §12.1 (conflicting data):** the migration **fails loudly**. Creating `account_device_mappings_active_role_idx` errors if any account already has two active rows (both backfilled to `gateway`). Pre-UAT no such data is expected; if the migration fails, an operator resolves the duplicate deliberately rather than the migration guessing which device to demote. No pre-migration "mark all but newest as corrected" step.

**Files:**
- Create: `backend/internal/store/migrations/0052_device_assignment_roles.sql`
- Test: `backend/internal/store/migration_0052_test.go` (create)

**Interfaces:**
- Consumes: nothing.
- Produces: columns `role TEXT NOT NULL DEFAULT 'gateway'`, `assigned_at TIMESTAMPTZ NOT NULL DEFAULT now()`, `unassigned_at TIMESTAMPTZ`, `unassign_reason TEXT`; indexes `account_device_mappings_active_idx`, `account_device_mappings_active_role_idx`, `account_device_mappings_device_active_idx`. The old constraint `account_device_mappings_account_id_device_id_key` no longer exists.

- [ ] **Step 1: Write the failing test**

Create `backend/internal/store/migration_0052_test.go`:

```go
package store

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"
)

// newMigratedDB returns a clean, fully migrated database. Open already
// returns *sql.DB, which has everything the assertions need.
func newMigratedDB(t *testing.T) (context.Context, *sql.DB) {
	t.Helper()
	dsn := os.Getenv("ACS_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("ACS_TEST_POSTGRES_DSN not set — skipping DB-backed migration test")
	}
	ctx := context.Background()
	db, err := Open(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.ExecContext(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public`); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	return ctx, db
}

// TestMigration0052_AssignmentSchema pins the shape spec §5 requires: the
// pair-unique constraint is gone, the two partial unique indexes over
// active rows exist, and role backfills to gateway.
func TestMigration0052_AssignmentSchema(t *testing.T) {
	ctx, db := newMigratedDB(t)

	// The old pair-unique constraint must be gone.
	var n int
	if err := db.QueryRowContext(ctx, `
		SELECT count(*) FROM pg_constraint
		WHERE conname = 'account_device_mappings_account_id_device_id_key'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("old constraint account_device_mappings_account_id_device_id_key still exists")
	}

	// Both partial unique indexes must exist and be partial.
	for _, idx := range []string{"account_device_mappings_active_idx", "account_device_mappings_active_role_idx"} {
		var def string
		if err := db.QueryRowContext(ctx, `SELECT indexdef FROM pg_indexes WHERE indexname = $1`, idx).Scan(&def); err != nil {
			t.Fatalf("index %s: %v", idx, err)
		}
		if !strings.Contains(def, "UNIQUE") || !strings.Contains(def, "WHERE (unassigned_at IS NULL)") {
			t.Errorf("index %s is not a partial unique index over active rows: %s", idx, def)
		}
	}

	// A device and a mapping inserted without a role must backfill to gateway.
	if _, err := db.ExecContext(ctx, `INSERT INTO devices (id, oui_serial) VALUES ('11111111-1111-1111-1111-111111111111', '001349-NR7101-A')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO account_device_mappings (id, account_id, device_id, oui_serial, status)
		VALUES ('22222222-2222-2222-2222-222222222222', 'acct-1', '11111111-1111-1111-1111-111111111111', '001349-NR7101-A', 'ACTIVE')`); err != nil {
		t.Fatal(err)
	}
	var role string
	var unassigned interface{}
	if err := db.QueryRowContext(ctx, `SELECT role, unassigned_at FROM account_device_mappings WHERE account_id = 'acct-1'`).Scan(&role, &unassigned); err != nil {
		t.Fatal(err)
	}
	if role != "gateway" {
		t.Errorf("role backfilled to %q, want gateway", role)
	}
	if unassigned != nil {
		t.Errorf("new row has unassigned_at set; want NULL (active)")
	}
}

// TestMigration0052_RoleUniqueRejectsSecondGateway is the constraint that
// makes addressing safe: one active device per role per account.
func TestMigration0052_RoleUniqueRejectsSecondGateway(t *testing.T) {
	ctx, db := newMigratedDB(t)
	for i, serial := range []string{"001349-NR7101-B", "001349-NR7101-C"} {
		id := "3333333" + string(rune('0'+i)) + "-3333-3333-3333-333333333333"
		if _, err := db.ExecContext(ctx, `INSERT INTO devices (id, oui_serial) VALUES ($1, $2)`, id, serial); err != nil {
			t.Fatal(err)
		}
	}
	first := `INSERT INTO account_device_mappings (id, account_id, device_id, oui_serial, status, role)
	          VALUES ('44444444-4444-4444-4444-444444444444', 'acct-2', '33333330-3333-3333-3333-333333333333', '001349-NR7101-B', 'ACTIVE', 'gateway')`
	if _, err := db.ExecContext(ctx, first); err != nil {
		t.Fatal(err)
	}
	second := `INSERT INTO account_device_mappings (id, account_id, device_id, oui_serial, status, role)
	           VALUES ('55555555-5555-5555-5555-555555555555', 'acct-2', '33333331-3333-3333-3333-333333333333', '001349-NR7101-C', 'ACTIVE', 'gateway')`
	if _, err := db.ExecContext(ctx, second); err == nil {
		t.Fatal("second active gateway for the same account was accepted; the role-unique index is not enforcing")
	}
	// Releasing the first must allow the second.
	if _, err := db.ExecContext(ctx, `UPDATE account_device_mappings SET unassigned_at = now(), unassign_reason = 'rma' WHERE id = '44444444-4444-4444-4444-444444444444'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, second); err != nil {
		t.Fatalf("second gateway rejected after the first was released: %v", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run, from `backend/`, with the DSN set:
`ACS_TEST_POSTGRES_DSN="postgres://acs:acs@127.0.0.1:5432/acs?sslmode=disable" go test -count=1 -p 1 -run TestMigration0052 -v ./internal/store/`

Expected: `TestMigration0052_AssignmentSchema` FAILS — the old constraint still exists (count 1) and `pg_indexes` has no row for either new index. `TestMigration0052_RoleUniqueRejectsSecondGateway` FAILS at the `INSERT ... role` because the `role` column does not exist.

- [ ] **Step 3: Write the migration**

Create `backend/internal/store/migrations/0052_device_assignment_roles.sql`:

```sql
-- 0052: device assignment becomes addressable by role and temporal.
--
-- Spec: docs/superpowers/specs/2026-09-09-fleet-data-model-design.md §5.
--
-- Order dispatch used to pick "the most recently updated ACTIVE mapping",
-- so on a multi-device account an order silently targeted whichever device
-- was touched last. Role makes "the gateway for account X" resolve to one
-- row or none. unassigned_at makes assignment temporal: history is the
-- rows with it set, current state the rows with it NULL, one table, one
-- source of truth.
--
-- Backfill: every existing row becomes role=gateway (so single-device
-- accounts keep working with no BSS change) and assigned_at=now(). That
-- assigned_at is a LOWER BOUND on a fact never previously recorded, not
-- real history -- the operator console labels backfilled rows.
--
-- If any account already has two active rows, the role-unique index
-- creation below FAILS. That is deliberate: an operator resolves the
-- duplicate, rather than this migration guessing which device to demote.

ALTER TABLE account_device_mappings
    ADD COLUMN role TEXT NOT NULL DEFAULT 'gateway'
        CHECK (role IN ('gateway', 'ont', 'extender', 'stb', 'ata', 'other')),
    ADD COLUMN assigned_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    ADD COLUMN unassigned_at TIMESTAMPTZ,
    ADD COLUMN unassign_reason TEXT
        CHECK (unassign_reason IN ('rma', 'upgrade', 'return', 'moved', 'corrected'));

-- Postgres's auto-generated name for the unnamed inline
-- UNIQUE (account_id, device_id) in 0007; no later migration altered it.
ALTER TABLE account_device_mappings
    DROP CONSTRAINT account_device_mappings_account_id_device_id_key;

-- A device is assigned to an account at most once *currently*; it may be
-- assigned, released and reassigned over time.
CREATE UNIQUE INDEX account_device_mappings_active_idx
    ON account_device_mappings (account_id, device_id)
    WHERE unassigned_at IS NULL;

-- One active device per role per account. This is the constraint that
-- makes addressing safe, and it forces close-before-open on swap: a
-- replacement gateway cannot be inserted while the old one is active.
CREATE UNIQUE INDEX account_device_mappings_active_role_idx
    ON account_device_mappings (account_id, role)
    WHERE unassigned_at IS NULL;

CREATE INDEX account_device_mappings_device_active_idx
    ON account_device_mappings (device_id)
    WHERE unassigned_at IS NULL;
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `ACS_TEST_POSTGRES_DSN="postgres://acs:acs@127.0.0.1:5432/acs?sslmode=disable" go test -count=1 -p 1 -run TestMigration0052 -v ./internal/store/`

Expected: both PASS.

- [ ] **Step 5: Verify idempotent re-run and the checksum gate still work**

Run: `ACS_TEST_POSTGRES_DSN="postgres://acs:acs@127.0.0.1:5432/acs?sslmode=disable" go test -count=1 -p 1 ./internal/store/`

Expected: all `ok`. The existing migration tests re-apply the full set and verify checksums; a new file must not break them.

- [ ] **Step 6: Full backend checks**

Run: `gofmt -l . && go vet ./... && go test ./...`

Expected: `gofmt -l .` prints nothing; vet silent; all `ok` (DB-backed tests skip without a DSN).

- [ ] **Step 7: Commit**

```bash
git add internal/store/migrations/0052_device_assignment_roles.sql internal/store/migration_0052_test.go
git commit -F - <<'EOF'
feat(store): make device assignment role-addressable and temporal

Adds role, assigned_at, unassigned_at and unassign_reason to
account_device_mappings, drops the pair-unique constraint, and adds
partial unique indexes over active rows: one per (account, device) and
one per (account, role). The role index is what makes "the gateway for
account X" resolve to exactly one row or none, and it forces
close-before-open on swap at the database rather than by application
discipline.

Existing rows backfill to role=gateway. assigned_at backfills to now(),
which is a lower bound on a fact never previously recorded, not history.
If any account already has two active rows the index creation fails
deliberately -- an operator resolves it, the migration does not guess.

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>
EOF
```

---

### Task 2: Repository read side — model, active-only filters, role lookup, history

**Files:**
- Modify: `backend/internal/bss/mapping.go`
- Modify: `backend/internal/bss/stats.go:26`
- Test: `backend/internal/bss/mapping_test.go` (create)

**Interfaces:**
- Consumes: Task 1's schema.
- Produces:
  - `AccountDeviceMapping` gains `Role string`, `AssignedAt time.Time`, `UnassignedAt *time.Time`, `UnassignReason string` (JSON `role`, `assigned_at`, `unassigned_at,omitempty`, `unassign_reason,omitempty`).
  - Constants `RoleGateway = "gateway"`, `RoleONT = "ont"`, `RoleExtender = "extender"`, `RoleSTB = "stb"`, `RoleATA = "ata"`, `RoleOther = "other"`; `ReasonRMA = "rma"`, `ReasonUpgrade = "upgrade"`, `ReasonReturn = "return"`, `ReasonMoved = "moved"`, `ReasonCorrected = "corrected"`.
  - `var ErrNoDeviceForRole = errors.New("no active device assigned in that role")`.
  - `func (r *Repository) ActiveDeviceForAccount(ctx context.Context, accountID, role string) (*AccountDeviceMapping, error)` — returns `ErrNoDeviceForRole` (wrapped) when none.
  - `func (r *Repository) AssignmentHistory(ctx context.Context, accountID string) ([]AccountDeviceMapping, error)` — all rows for the account, `ORDER BY assigned_at ASC, id ASC`.
  - `ListByAccount` and `ListAll` now return active rows only. `Stats.MappingsByStatus` counts active rows only.
  - `PrimaryDeviceForAccount` still exists after this task (removed in Task 4 with its caller).

- [ ] **Step 1: Write the failing tests**

Create `backend/internal/bss/mapping_test.go`:

```go
package bss

import (
	"context"
	"errors"
	"os"
	"testing"

	"acs/internal/store"
)

// newMappingTestRepo mirrors newOAuthTestRepo: a clean, fully migrated
// schema per test.
func newMappingTestRepo(t *testing.T) (context.Context, *Repository) {
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
	return ctx, NewRepository(db)
}

// seedDevice inserts the minimum a devices row needs so a mapping can
// reference it. oui_serial is the only NOT NULL column without a default.
func seedDevice(t *testing.T, ctx context.Context, r *Repository, id, ouiSerial string) {
	t.Helper()
	if _, err := r.db.ExecContext(ctx, `INSERT INTO devices (id, oui_serial) VALUES ($1, $2)`, id, ouiSerial); err != nil {
		t.Fatalf("seed device %s: %v", ouiSerial, err)
	}
}

// rawAssign writes an assignment row directly, bypassing the repository's
// write path, so the read-side tests do not depend on Task 3.
func rawAssign(t *testing.T, ctx context.Context, r *Repository, id, accountID, deviceID, ouiSerial, role string, released bool) {
	t.Helper()
	q := `INSERT INTO account_device_mappings (id, account_id, device_id, oui_serial, status, role) VALUES ($1, $2, $3, $4, 'ACTIVE', $5)`
	if _, err := r.db.ExecContext(ctx, q, id, accountID, deviceID, ouiSerial, role); err != nil {
		t.Fatalf("raw assign: %v", err)
	}
	if released {
		if _, err := r.db.ExecContext(ctx, `UPDATE account_device_mappings SET unassigned_at = now(), unassign_reason = 'rma' WHERE id = $1`, id); err != nil {
			t.Fatal(err)
		}
	}
}

const (
	devA = "aaaaaaaa-0000-0000-0000-000000000001"
	devB = "aaaaaaaa-0000-0000-0000-000000000002"
	devC = "aaaaaaaa-0000-0000-0000-000000000003"
	mapA = "bbbbbbbb-0000-0000-0000-000000000001"
	mapB = "bbbbbbbb-0000-0000-0000-000000000002"
	mapC = "bbbbbbbb-0000-0000-0000-000000000003"
)

func TestActiveDeviceForAccount_ResolvesByRole(t *testing.T) {
	ctx, r := newMappingTestRepo(t)
	seedDevice(t, ctx, r, devA, "S-A")
	seedDevice(t, ctx, r, devB, "S-B")
	rawAssign(t, ctx, r, mapA, "acct", devA, "S-A", RoleGateway, false)
	rawAssign(t, ctx, r, mapB, "acct", devB, "S-B", RoleONT, false)

	got, err := r.ActiveDeviceForAccount(ctx, "acct", RoleGateway)
	if err != nil {
		t.Fatal(err)
	}
	if got.DeviceID != devA || got.Role != RoleGateway {
		t.Errorf("gateway resolved to %s/%s, want %s/gateway", got.DeviceID, got.Role, devA)
	}
	got, err = r.ActiveDeviceForAccount(ctx, "acct", RoleONT)
	if err != nil {
		t.Fatal(err)
	}
	if got.DeviceID != devB {
		t.Errorf("ont resolved to %s, want %s", got.DeviceID, devB)
	}
}

func TestActiveDeviceForAccount_UnfilledRoleIsTypedError(t *testing.T) {
	ctx, r := newMappingTestRepo(t)
	seedDevice(t, ctx, r, devA, "S-A")
	rawAssign(t, ctx, r, mapA, "acct", devA, "S-A", RoleGateway, false)

	_, err := r.ActiveDeviceForAccount(ctx, "acct", RoleExtender)
	if !errors.Is(err, ErrNoDeviceForRole) {
		t.Errorf("unfilled role returned %v, want ErrNoDeviceForRole", err)
	}
}

// A released assignment must be invisible to every current-state read but
// present in history. This is the property ListAll and Stats would silently
// lose without the predicate.
func TestReleasedAssignmentsAreHistoryNotCurrent(t *testing.T) {
	ctx, r := newMappingTestRepo(t)
	seedDevice(t, ctx, r, devA, "S-A")
	seedDevice(t, ctx, r, devB, "S-B")
	rawAssign(t, ctx, r, mapA, "acct", devA, "S-A", RoleGateway, true)  // released
	rawAssign(t, ctx, r, mapB, "acct", devB, "S-B", RoleGateway, false) // current

	if _, err := r.ActiveDeviceForAccount(ctx, "acct", RoleGateway); err != nil {
		t.Fatalf("current gateway not resolvable: %v", err)
	}

	byAcct, err := r.ListByAccount(ctx, "acct")
	if err != nil {
		t.Fatal(err)
	}
	if len(byAcct) != 1 || byAcct[0].DeviceID != devB {
		t.Errorf("ListByAccount = %+v, want only the current device %s", byAcct, devB)
	}

	all, err := r.ListAll(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 {
		t.Errorf("ListAll returned %d rows, want 1 (released row must be excluded)", len(all))
	}

	stats, err := r.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stats.MappingsByStatus["ACTIVE"] != 1 {
		t.Errorf("Stats counted %d ACTIVE mappings, want 1 (released row must be excluded)", stats.MappingsByStatus["ACTIVE"])
	}

	hist, err := r.AssignmentHistory(ctx, "acct")
	if err != nil {
		t.Fatal(err)
	}
	if len(hist) != 2 {
		t.Fatalf("AssignmentHistory returned %d rows, want 2", len(hist))
	}
	if hist[0].UnassignedAt == nil || hist[0].UnassignReason != ReasonRMA {
		t.Errorf("first history row should be the released one with reason rma, got %+v", hist[0])
	}
	if hist[1].UnassignedAt != nil {
		t.Errorf("second history row should be current (UnassignedAt nil), got %+v", hist[1])
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `ACS_TEST_POSTGRES_DSN="postgres://acs:acs@127.0.0.1:5432/acs?sslmode=disable" go test -count=1 -p 1 -run 'TestActiveDevice|TestReleased' -v ./internal/bss/`

Expected: compile FAIL — `undefined: RoleGateway`, `undefined: ErrNoDeviceForRole`, `r.ActiveDeviceForAccount undefined`, `r.AssignmentHistory undefined`, `got.Role undefined`.

- [ ] **Step 3: Add constants, the error, and the struct fields**

In `backend/internal/bss/mapping.go`, add `"time"` to the imports, then add after the existing `Status*` constants:

```go
// Roles describe what a device does for an account, not what the device
// is — the same model can be a gateway at one address and an extender at
// another. Kept in sync with the CHECK constraint in migration 0052.
const (
	RoleGateway  = "gateway"
	RoleONT      = "ont"
	RoleExtender = "extender"
	RoleSTB      = "stb"
	RoleATA      = "ata"
	RoleOther    = "other"
)

// Unassign reasons record why an assignment ended. Kept in sync with the
// CHECK constraint in migration 0052.
const (
	ReasonRMA       = "rma"
	ReasonUpgrade   = "upgrade"
	ReasonReturn    = "return"
	ReasonMoved     = "moved"
	ReasonCorrected = "corrected"
)

// ErrNoDeviceForRole is returned when an account has no active device in
// the requested role. It is a typed error rather than sql.ErrNoRows so a
// caller can distinguish "no such device" from any other query failure.
var ErrNoDeviceForRole = errors.New("no active device assigned in that role")
```

Replace the `AccountDeviceMapping` struct with:

```go
// AccountDeviceMapping is a row of account_device_mappings. JSON tags
// matter here (unlike a purely-internal repository type) because the
// admin panel's handlers (cmd/api/bss_admin_handlers.go) encode this
// struct directly rather than mapping it into a local response type the
// way cmd/bssadapter's own handlers do.
//
// An assignment is current while UnassignedAt is nil and historical once
// it is set. Status is retained for API compatibility and is NOT what
// decides whether a device currently serves an account (spec §5.4).
type AccountDeviceMapping struct {
	ID             string     `json:"id"`
	AccountID      string     `json:"account_id"`
	DeviceID       string     `json:"device_id"`
	OUISerial      string     `json:"oui_serial"`
	ServicePlan    string     `json:"service_plan,omitempty"`
	Status         string     `json:"status"`
	Role           string     `json:"role"`
	AssignedAt     time.Time  `json:"assigned_at"`
	UnassignedAt   *time.Time `json:"unassigned_at,omitempty"`
	UnassignReason string     `json:"unassign_reason,omitempty"`
}
```

- [ ] **Step 4: Update the scan and every SELECT column list**

Replace `scanMapping` with:

```go
func scanMapping(s scanner) (*AccountDeviceMapping, error) {
	var m AccountDeviceMapping
	var servicePlan, reason sql.NullString
	var unassigned sql.NullTime
	if err := s.Scan(&m.ID, &m.AccountID, &m.DeviceID, &m.OUISerial, &servicePlan, &m.Status,
		&m.Role, &m.AssignedAt, &unassigned, &reason); err != nil {
		return nil, fmt.Errorf("scan mapping: %w", err)
	}
	if servicePlan.Valid {
		m.ServicePlan = servicePlan.String
	}
	if unassigned.Valid {
		t := unassigned.Time
		m.UnassignedAt = &t
	}
	if reason.Valid {
		m.UnassignReason = reason.String
	}
	return &m, nil
}
```

Add a shared column list so every query selects the same columns in the same order the scan expects:

```go
// mappingColumns is every column scanMapping reads, in scan order. Every
// SELECT against account_device_mappings uses it so the two cannot drift.
const mappingColumns = `id, account_id, device_id, oui_serial, service_plan, status, role, assigned_at, unassigned_at, unassign_reason`
```

Then update each existing query to use it and, where it reads current state, add the active predicate:

- `getByAccountDevice`: `SELECT ` + mappingColumns + ` FROM account_device_mappings WHERE account_id = $1 AND device_id = $2 AND unassigned_at IS NULL`
- `ListByAccount`: `SELECT ` + mappingColumns + ` FROM account_device_mappings WHERE account_id = $1 AND unassigned_at IS NULL ORDER BY assigned_at ASC, id ASC`. Update its doc comment to "returns every device *currently* assigned to an account".
- `PrimaryDeviceForAccount`: update only its column list to `mappingColumns` so it still compiles. Do not change its logic; it is deleted in Task 4.
- `ListAll`: `SELECT ` + mappingColumns + ` FROM account_device_mappings WHERE unassigned_at IS NULL ORDER BY assigned_at DESC LIMIT $1`. Update its doc comment to say it lists current assignments.

Because these queries now use string concatenation, write them as `"SELECT " + mappingColumns + " FROM ..."` rather than backtick literals.

- [ ] **Step 5: Add the two new read methods**

Add to `backend/internal/bss/mapping.go`:

```go
// ActiveDeviceForAccount resolves the one device currently serving an
// account in the given role — the mapping an order dispatches against.
//
// This has exactly one rule and no tiebreak. The partial unique index
// account_device_mappings_active_role_idx guarantees at most one active
// row per (account, role), so there is nothing to order by and no LIMIT 1
// hiding a multiplicity. An unfilled role is ErrNoDeviceForRole.
func (r *Repository) ActiveDeviceForAccount(ctx context.Context, accountID, role string) (*AccountDeviceMapping, error) {
	row := r.db.QueryRowContext(ctx,
		"SELECT "+mappingColumns+" FROM account_device_mappings WHERE account_id = $1 AND role = $2 AND unassigned_at IS NULL",
		accountID, role)
	m, err := scanMapping(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: account %s role %s", ErrNoDeviceForRole, accountID, role)
	}
	return m, err
}

// AssignmentHistory returns every assignment an account has ever had,
// current and released, oldest first — the care-agent view that answers
// "this started after you swapped my router".
func (r *Repository) AssignmentHistory(ctx context.Context, accountID string) ([]AccountDeviceMapping, error) {
	rows, err := r.db.QueryContext(ctx,
		"SELECT "+mappingColumns+" FROM account_device_mappings WHERE account_id = $1 ORDER BY assigned_at ASC, id ASC",
		accountID)
	if err != nil {
		return nil, fmt.Errorf("assignment history: %w", err)
	}
	defer rows.Close()

	var out []AccountDeviceMapping
	for rows.Next() {
		m, err := scanMapping(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *m)
	}
	return out, rows.Err()
}
```

Note `scanMapping` wraps `sql.ErrNoRows` with `%w`, so `errors.Is` above works through the wrap.

- [ ] **Step 6: Make Stats count current assignments only**

In `backend/internal/bss/stats.go:26`, change the mapping-count query to:

```go
	rows, err := r.db.QueryContext(ctx, `SELECT status, count(*) FROM account_device_mappings WHERE unassigned_at IS NULL GROUP BY status`)
```

and add above the `Stats` type's doc comment a sentence: "Mapping counts cover current assignments only; released assignments are history, and counting them would make the figure drift upward forever."

- [ ] **Step 7: Run tests to verify they pass**

Run: `ACS_TEST_POSTGRES_DSN="postgres://acs:acs@127.0.0.1:5432/acs?sslmode=disable" go test -count=1 -p 1 ./internal/bss/`

Expected: all PASS, including the pre-existing `template_test.go`, `acsclient_test.go` and `oauth_revocation_test.go`.

- [ ] **Step 8: Full backend checks**

Run: `gofmt -l . && go vet ./... && go test ./...`

Expected: clean. `cmd/bssadapter` and `cmd/api` still compile because no method they call has changed signature yet.

- [ ] **Step 9: Commit**

```bash
git add internal/bss/mapping.go internal/bss/stats.go internal/bss/mapping_test.go
git commit -F - <<'EOF'
feat(bss): resolve devices by role and expose assignment history

ActiveDeviceForAccount has one rule and no tiebreak: the partial unique
index guarantees at most one active row per (account, role), so an
unfilled role is a typed ErrNoDeviceForRole rather than sql.ErrNoRows.
AssignmentHistory returns current and released rows oldest first.

ListByAccount, ListAll and Stats now read current assignments only.
Without the predicate ListAll would show released devices as current and
Stats would count history, drifting upward indefinitely while looking
plausible.

PrimaryDeviceForAccount is untouched here and removed with its caller.

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>
EOF
```

---

### Task 3: Repository write side — assign, unassign, swap

**Files:**
- Modify: `backend/internal/bss/mapping.go`
- Test: `backend/internal/bss/mapping_test.go`

**Interfaces:**
- Consumes: Task 2's constants, `ErrNoDeviceForRole`, `mappingColumns`, `scanMapping`, `getByAccountDevice`.
- Produces:
  - `var ErrRoleAlreadyAssigned = errors.New("account already has an active device in that role")`.
  - `func (r *Repository) AssignDevice(ctx context.Context, accountID, ouiSerial, role, servicePlan string) (*AccountDeviceMapping, error)` — plain insert; `ErrDeviceNotFound` for an unknown serial, `ErrRoleAlreadyAssigned` (wrapped) on the role index, and also on the pair index (same device already active for this account).
  - `func (r *Repository) UnassignDevice(ctx context.Context, accountID, role, reason string) error` — `ErrNoDeviceForRole` if nothing to release.
  - `func (r *Repository) SwapDevice(ctx context.Context, accountID, role, newOUISerial, reason string) (*AccountDeviceMapping, error)` — close-then-open in one transaction.
  - `CreateMapping` still exists after this task (removed in Task 4 with its callers).

- [ ] **Step 1: Write the failing tests**

Append to `backend/internal/bss/mapping_test.go`:

```go
func TestAssignDevice_RejectsSecondDeviceInSameRole(t *testing.T) {
	ctx, r := newMappingTestRepo(t)
	seedDevice(t, ctx, r, devA, "S-A")
	seedDevice(t, ctx, r, devB, "S-B")

	if _, err := r.AssignDevice(ctx, "acct", "S-A", RoleGateway, "plan-1"); err != nil {
		t.Fatalf("first assign: %v", err)
	}
	_, err := r.AssignDevice(ctx, "acct", "S-B", RoleGateway, "plan-1")
	if !errors.Is(err, ErrRoleAlreadyAssigned) {
		t.Errorf("second gateway returned %v, want ErrRoleAlreadyAssigned", err)
	}
	// A different role is fine.
	if _, err := r.AssignDevice(ctx, "acct", "S-B", RoleONT, "plan-1"); err != nil {
		t.Errorf("assigning S-B as ont failed: %v", err)
	}
}

func TestAssignDevice_UnknownSerialIsDeviceNotFound(t *testing.T) {
	ctx, r := newMappingTestRepo(t)
	_, err := r.AssignDevice(ctx, "acct", "NO-SUCH", RoleGateway, "")
	if !errors.Is(err, ErrDeviceNotFound) {
		t.Errorf("got %v, want ErrDeviceNotFound", err)
	}
}

func TestUnassignThenReassignSameDevice(t *testing.T) {
	ctx, r := newMappingTestRepo(t)
	seedDevice(t, ctx, r, devA, "S-A")

	if _, err := r.AssignDevice(ctx, "acct", "S-A", RoleGateway, ""); err != nil {
		t.Fatal(err)
	}
	if err := r.UnassignDevice(ctx, "acct", RoleGateway, ReasonReturn); err != nil {
		t.Fatalf("unassign: %v", err)
	}
	if _, err := r.ActiveDeviceForAccount(ctx, "acct", RoleGateway); !errors.Is(err, ErrNoDeviceForRole) {
		t.Errorf("after unassign, gateway resolved: %v", err)
	}
	// The released device can come back — to the same account, even.
	if _, err := r.AssignDevice(ctx, "acct", "S-A", RoleGateway, ""); err != nil {
		t.Fatalf("reassign after release: %v", err)
	}
	hist, err := r.AssignmentHistory(ctx, "acct")
	if err != nil {
		t.Fatal(err)
	}
	if len(hist) != 2 || hist[0].UnassignReason != ReasonReturn || hist[1].UnassignedAt != nil {
		t.Errorf("history = %+v, want [released(return), current]", hist)
	}
}

func TestUnassignDevice_NothingToReleaseIsTypedError(t *testing.T) {
	ctx, r := newMappingTestRepo(t)
	err := r.UnassignDevice(ctx, "acct", RoleGateway, ReasonRMA)
	if !errors.Is(err, ErrNoDeviceForRole) {
		t.Errorf("got %v, want ErrNoDeviceForRole", err)
	}
}

// SwapDevice is the reason the temporal model exists. Close-then-open in
// one transaction: after it, exactly one gateway is active, it is the new
// device, and the old one is in history with the reason recorded.
func TestSwapDevice_ClosesOldOpensNewAtomically(t *testing.T) {
	ctx, r := newMappingTestRepo(t)
	seedDevice(t, ctx, r, devA, "S-A")
	seedDevice(t, ctx, r, devB, "S-B")
	if _, err := r.AssignDevice(ctx, "acct", "S-A", RoleGateway, "plan-1"); err != nil {
		t.Fatal(err)
	}

	got, err := r.SwapDevice(ctx, "acct", RoleGateway, "S-B", ReasonRMA)
	if err != nil {
		t.Fatalf("swap: %v", err)
	}
	if got.DeviceID != devB || got.UnassignedAt != nil {
		t.Errorf("swap returned %+v, want current assignment of %s", got, devB)
	}
	cur, err := r.ActiveDeviceForAccount(ctx, "acct", RoleGateway)
	if err != nil || cur.DeviceID != devB {
		t.Errorf("after swap, active gateway = %+v (err %v), want %s", cur, err, devB)
	}
	hist, err := r.AssignmentHistory(ctx, "acct")
	if err != nil {
		t.Fatal(err)
	}
	if len(hist) != 2 || hist[0].DeviceID != devA || hist[0].UnassignReason != ReasonRMA {
		t.Errorf("history = %+v, want old device released with reason rma first", hist)
	}
	// The service plan carries over to the replacement.
	if got.ServicePlan != "plan-1" {
		t.Errorf("service_plan after swap = %q, want plan-1 carried over", got.ServicePlan)
	}
}

// If the replacement cannot be inserted, the release must roll back —
// otherwise the account is left with NO gateway, worse than the defect
// being fixed. An unknown serial is the cheapest way to force that path.
func TestSwapDevice_RollsBackReleaseWhenInsertFails(t *testing.T) {
	ctx, r := newMappingTestRepo(t)
	seedDevice(t, ctx, r, devA, "S-A")
	if _, err := r.AssignDevice(ctx, "acct", "S-A", RoleGateway, ""); err != nil {
		t.Fatal(err)
	}

	_, err := r.SwapDevice(ctx, "acct", RoleGateway, "NO-SUCH", ReasonRMA)
	if !errors.Is(err, ErrDeviceNotFound) {
		t.Fatalf("swap to unknown serial returned %v, want ErrDeviceNotFound", err)
	}
	cur, err := r.ActiveDeviceForAccount(ctx, "acct", RoleGateway)
	if err != nil || cur.DeviceID != devA {
		t.Errorf("after failed swap, active gateway = %+v (err %v); the release was not rolled back", cur, err)
	}
}

func TestSwapDevice_NothingToSwapIsTypedError(t *testing.T) {
	ctx, r := newMappingTestRepo(t)
	seedDevice(t, ctx, r, devB, "S-B")
	_, err := r.SwapDevice(ctx, "acct", RoleGateway, "S-B", ReasonRMA)
	if !errors.Is(err, ErrNoDeviceForRole) {
		t.Errorf("got %v, want ErrNoDeviceForRole", err)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `ACS_TEST_POSTGRES_DSN="postgres://acs:acs@127.0.0.1:5432/acs?sslmode=disable" go test -count=1 -p 1 -run 'TestAssignDevice|TestUnassign|TestSwapDevice' -v ./internal/bss/`

Expected: compile FAIL — `r.AssignDevice undefined`, `r.UnassignDevice undefined`, `r.SwapDevice undefined`, `undefined: ErrRoleAlreadyAssigned`.

- [ ] **Step 3: Add the error and the unique-violation helper**

In `backend/internal/bss/mapping.go`, add `"github.com/jackc/pgx/v5/pgconn"` to the imports (it is already a transitive dependency of `pgx/v5`, which `go.mod` requires; run `go mod tidy` if it complains, and confirm `go.mod` gains no new module line). Then add:

```go
// ErrRoleAlreadyAssigned is returned when an account already has an active
// device in the requested role, or the same device is already active for
// the account. Both are unique-index violations; the caller must
// UnassignDevice or SwapDevice first.
var ErrRoleAlreadyAssigned = errors.New("account already has an active device in that role")

// isUniqueViolation reports whether err is Postgres 23505, the code both
// partial unique indexes raise. Same shape as internal/operators.
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
```

- [ ] **Step 4: Add the three write methods**

Add to `backend/internal/bss/mapping.go`:

```go
// resolveDeviceID turns an oui_serial into a devices.id, or ErrDeviceNotFound.
// q is either the pool or a transaction so AssignDevice and SwapDevice share it.
func resolveDeviceID(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, ouiSerial string) (string, error) {
	var deviceID string
	err := q.QueryRowContext(ctx, `SELECT id FROM devices WHERE oui_serial = $1`, ouiSerial).Scan(&deviceID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("%w: %s", ErrDeviceNotFound, ouiSerial)
	}
	if err != nil {
		return "", fmt.Errorf("resolve device: %w", err)
	}
	return deviceID, nil
}

// insertAssignment writes one active assignment row. It is a plain INSERT,
// not an upsert: the partial unique indexes decide whether it is allowed,
// and a violation surfaces as ErrRoleAlreadyAssigned.
func insertAssignment(ctx context.Context, q interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, id, accountID, deviceID, ouiSerial, role string, servicePlan any) error {
	_, err := q.ExecContext(ctx, `
		INSERT INTO account_device_mappings (id, account_id, device_id, oui_serial, service_plan, status, role)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
	`, id, accountID, deviceID, ouiSerial, servicePlan, StatusActive, role)
	if isUniqueViolation(err) {
		return fmt.Errorf("%w: account %s role %s", ErrRoleAlreadyAssigned, accountID, role)
	}
	if err != nil {
		return fmt.Errorf("insert assignment: %w", err)
	}
	return nil
}

// AssignDevice resolves oui_serial against the devices table and records
// that the device now serves the account in the given role.
func (r *Repository) AssignDevice(ctx context.Context, accountID, ouiSerial, role, servicePlan string) (*AccountDeviceMapping, error) {
	deviceID, err := resolveDeviceID(ctx, r.db, ouiSerial)
	if err != nil {
		return nil, err
	}
	if err := insertAssignment(ctx, r.db, uuid.New().String(), accountID, deviceID, ouiSerial, role, nullIfEmpty(servicePlan)); err != nil {
		return nil, err
	}
	return r.getByAccountDevice(ctx, accountID, deviceID)
}

// UnassignDevice ends the account's current assignment in the given role,
// recording why. The row stays as history.
func (r *Repository) UnassignDevice(ctx context.Context, accountID, role, reason string) error {
	res, err := r.db.ExecContext(ctx, `
		UPDATE account_device_mappings
		   SET unassigned_at = now(), unassign_reason = $3, updated_at = now()
		 WHERE account_id = $1 AND role = $2 AND unassigned_at IS NULL
	`, accountID, role, reason)
	if err != nil {
		return fmt.Errorf("unassign device: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("%w: account %s role %s", ErrNoDeviceForRole, accountID, role)
	}
	return nil
}

// SwapDevice replaces the device serving an account in a role: close the
// old assignment, then open the new one, in one transaction.
//
// The order and the transaction are both load-bearing. The role-unique
// index rejects the insert while the old row is active, so close must
// come first; and if the insert then fails, the close must roll back or
// the account is left with no device in that role at all -- worse than
// the addressing defect this model exists to fix. The service plan
// carries over to the replacement.
func (r *Repository) SwapDevice(ctx context.Context, accountID, role, newOUISerial, reason string) (*AccountDeviceMapping, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin swap: %w", err)
	}
	defer tx.Rollback()

	var servicePlan sql.NullString
	err = tx.QueryRowContext(ctx, `
		UPDATE account_device_mappings
		   SET unassigned_at = now(), unassign_reason = $3, updated_at = now()
		 WHERE account_id = $1 AND role = $2 AND unassigned_at IS NULL
		 RETURNING service_plan
	`, accountID, role, reason).Scan(&servicePlan)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: account %s role %s", ErrNoDeviceForRole, accountID, role)
	}
	if err != nil {
		return nil, fmt.Errorf("release old assignment: %w", err)
	}

	newDeviceID, err := resolveDeviceID(ctx, tx, newOUISerial)
	if err != nil {
		return nil, err // deferred Rollback restores the old assignment
	}
	var plan any
	if servicePlan.Valid {
		plan = servicePlan.String
	}
	if err := insertAssignment(ctx, tx, uuid.New().String(), accountID, newDeviceID, newOUISerial, role, plan); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit swap: %w", err)
	}
	return r.getByAccountDevice(ctx, accountID, newDeviceID)
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `ACS_TEST_POSTGRES_DSN="postgres://acs:acs@127.0.0.1:5432/acs?sslmode=disable" go test -count=1 -p 1 ./internal/bss/`

Expected: all PASS.

- [ ] **Step 6: Full backend checks**

Run: `gofmt -l . && go vet ./... && go test ./...`

Expected: clean. `go.mod` must show no new `require` line — `pgconn` lives inside the `pgx/v5` module already required.

- [ ] **Step 7: Commit**

```bash
git add internal/bss/mapping.go internal/bss/mapping_test.go
git commit -F - <<'EOF'
feat(bss): assign, unassign and swap devices by role

AssignDevice is a plain insert: the partial unique indexes decide
whether it is allowed, and a violation is a typed ErrRoleAlreadyAssigned.
UnassignDevice ends the current assignment in a role and keeps the row as
history.

SwapDevice closes the old assignment and opens the new one in a single
transaction. The order is forced by the role-unique index; the
transaction is what stops a failed insert leaving the account with no
device in that role. The service plan carries over.

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>
EOF
```

---

### Task 4: Rewire the callers, delete the tiebreak, update the API contract

**Files:**
- Modify: `backend/cmd/bssadapter/main.go:457-470` (structs), `:476-495` (createMapping), `:513-525` (listMappings), `:536-542` (createOrderRequest), `:584-592` (order resolution)
- Modify: `backend/cmd/api/bss_admin_handlers.go:107-135`
- Modify: `backend/internal/bss/mapping.go` — delete `CreateMapping` and `PrimaryDeviceForAccount`
- Modify: `backend/openapi-bssadapter.yaml:219-226` (Mapping schema) and the request schemas for `POST /bss/v1/mappings` and `POST /bss/v1/orders`
- Modify: `bss-integration-guide.md` — document `role`
- Test: `backend/cmd/bssadapter/order_role_test.go` (create)

**Interfaces:**
- Consumes: `AssignDevice`, `ActiveDeviceForAccount`, `ErrNoDeviceForRole`, `ErrRoleAlreadyAssigned`, `RoleGateway`, and the `Role` field from Tasks 2–3.
- Produces: `createMappingRequest.Role`, `createOrderRequest.Role`, `mappingResponse.Role`, and a package-level `func roleOrDefault(role string) string` in `cmd/bssadapter` returning `bss.RoleGateway` for `""`. After this task `CreateMapping` and `PrimaryDeviceForAccount` do not exist.

- [ ] **Step 1: Write the failing test for the default-role rule**

Create `backend/cmd/bssadapter/order_role_test.go`:

```go
package main

import (
	"testing"

	"acs/internal/bss"
)

// An order that names no role targets the gateway. This is the contract
// decision that keeps every existing BSS caller working while replacing
// the "most recently updated" tiebreak with a deterministic target.
func TestRoleOrDefault(t *testing.T) {
	if got := roleOrDefault(""); got != bss.RoleGateway {
		t.Errorf("roleOrDefault(\"\") = %q, want %q", got, bss.RoleGateway)
	}
	if got := roleOrDefault(bss.RoleONT); got != bss.RoleONT {
		t.Errorf("roleOrDefault(ont) = %q, want ont", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/bssadapter/ -run TestRoleOrDefault -v`

Expected: compile FAIL — `undefined: roleOrDefault`.

- [ ] **Step 3: Add role to the adapter's request/response types and the helper**

In `backend/cmd/bssadapter/main.go`, change the two structs to:

```go
type createMappingRequest struct {
	AccountID   string `json:"account_id"`
	OUISerial   string `json:"oui_serial"`
	DeviceUUID  string `json:"device_uuid"`
	ServicePlan string `json:"service_plan"`
	Role        string `json:"role"` // optional; defaults to gateway
}

type mappingResponse struct {
	AccountID   string `json:"account_id"`
	DeviceUUID  string `json:"device_uuid"`
	OUISerial   string `json:"oui_serial"`
	ServicePlan string `json:"service_plan,omitempty"`
	Status      string `json:"status"`
	Role        string `json:"role"`
}
```

Add `Role string \`json:"role"\`` to `createOrderRequest` after `Action`, with the same `// optional; defaults to gateway` comment. Then add the helper near the structs:

```go
// roleOrDefault applies the contract rule for callers that name no role:
// they target the gateway. An account with no gateway assigned gets a
// clean ErrNoDeviceForRole rather than an arbitrary device.
func roleOrDefault(role string) string {
	if role == "" {
		return bss.RoleGateway
	}
	return role
}
```

- [ ] **Step 4: Run the helper test**

Run: `go test ./cmd/bssadapter/ -run TestRoleOrDefault -v`

Expected: PASS.

- [ ] **Step 5: Rewire createMapping in the adapter**

In `createMapping`, replace the `h.mappings.CreateMapping(...)` call and its error handling with:

```go
	mapping, err := h.mappings.AssignDevice(r.Context(), req.AccountID, req.OUISerial, roleOrDefault(req.Role), req.ServicePlan)
	if errors.Is(err, bss.ErrDeviceNotFound) {
		writeError(w, http.StatusNotFound, "ErrDeviceNotMapped", err.Error())
		return
	}
	if errors.Is(err, bss.ErrRoleAlreadyAssigned) {
		writeError(w, http.StatusConflict, "ErrRoleAlreadyAssigned", "the account already has an active device in that role; unassign or swap it first")
		return
	}
```

(keep the existing generic `if err != nil` branch after it). Add `"role": mapping.Role` to the audit `Record` map, and `Role: mapping.Role` to the `mappingResponse` literal. In `listMappings`, add `Role: m.Role` to each `mappingResponse` literal.

- [ ] **Step 6: Rewire order dispatch in the adapter**

In `createOrder`, replace the `PrimaryDeviceForAccount` block:

```go
	mapping, err := h.mappings.ActiveDeviceForAccount(r.Context(), req.AccountID, roleOrDefault(req.Role))
	if errors.Is(err, bss.ErrNoDeviceForRole) {
		writeError(w, http.StatusNotFound, "ErrDeviceNotMapped", "no active device is assigned to this account in the requested role")
		return
	}
	if err != nil {
		h.logger.Error("failed to resolve account device", "err", err, "account_id", req.AccountID, "role", roleOrDefault(req.Role))
		writeError(w, http.StatusInternalServerError, "ErrInternal", "internal error")
		return
	}
```

If `database/sql` was imported only for the `sql.ErrNoRows` check you just removed, drop the import.

- [ ] **Step 7: Rewire the admin API**

In `backend/cmd/api/bss_admin_handlers.go`, add `Role string \`json:"role"\`` to `createBSSMappingRequest`, and change the call to:

```go
	role := req.Role
	if role == "" {
		role = bss.RoleGateway
	}
	mapping, err := h.bssMappings.AssignDevice(r.Context(), req.AccountID, req.OUISerial, role, req.ServicePlan)
	if errors.Is(err, bss.ErrDeviceNotFound) {
		http.Error(w, "no device found for that oui_serial — it must have sent at least one Inform first", http.StatusNotFound)
		return
	}
	if errors.Is(err, bss.ErrRoleAlreadyAssigned) {
		http.Error(w, "the account already has an active device in that role", http.StatusConflict)
		return
	}
```

Note the existing code compares `err == bss.ErrDeviceNotFound` with `==`; `AssignDevice` wraps the error, so it must become `errors.Is`. Add `"errors"` to the imports if absent.

- [ ] **Step 8: Delete the superseded repository methods**

In `backend/internal/bss/mapping.go`, delete `CreateMapping` and `PrimaryDeviceForAccount` entirely, including their doc comments. Run `go build ./...` — it must succeed, proving no caller remains.

- [ ] **Step 9: Update the OpenAPI contract**

In `backend/openapi-bssadapter.yaml`, add to the `Mapping` schema:

```yaml
        role: { type: string, enum: [gateway, ont, extender, stb, ata, other], description: "What the device does for this account. One active device per role per account." }
```

Find the request body schemas for `POST /bss/v1/mappings` and `POST /bss/v1/orders` (search for `oui_serial` and `external_order_id` under `requestBody`) and add to each:

```yaml
              role: { type: string, enum: [gateway, ont, extender, stb, ata, other], description: "Optional. Defaults to gateway." }
```

Add a `409` response to `POST /bss/v1/mappings` referencing the existing error schema, described as "the account already has an active device in that role".

Then, in `bss-integration-guide.md`, in the section describing Workflow A (mapping) and Workflow B (orders), add a short paragraph: assignments carry a `role`; an order or mapping with no `role` targets `gateway`; an account may have one active device per role; replacing a device in a role is done by unassigning or swapping, not by creating a second mapping.

- [ ] **Step 10: Run the contract checks and full suite**

Run, from `backend/`: `gofmt -l . && go vet ./... && go test ./...`
Then, from `frontend/`: `npm run generate:api && git status --porcelain frontend/src/api/generated.ts`

Expected: backend clean; `generated.ts` is unchanged (the adapter spec does not feed it — only `openapi.yaml` does, and that file was not touched). If `git status` shows it modified, the wrong spec was edited; revert and re-check.

Also run the DB-backed adapter integration tests if any exist: `ACS_TEST_POSTGRES_DSN=... go test -count=1 -p 1 -run Integration ./cmd/api/`.

- [ ] **Step 11: Commit**

```bash
git add cmd/bssadapter/main.go cmd/bssadapter/order_role_test.go cmd/api/bss_admin_handlers.go internal/bss/mapping.go openapi-bssadapter.yaml ../bss-integration-guide.md
git commit -F - <<'EOF'
feat(bss): address orders by device role, drop the last-touched tiebreak

Order dispatch resolved the account's device with ORDER BY updated_at
DESC LIMIT 1, so on a multi-device account an order silently targeted
whichever device was most recently touched. It now resolves by role, and
an order naming no role targets the gateway -- deterministic, and every
existing caller keeps working.

PrimaryDeviceForAccount and CreateMapping are deleted rather than
wrapped, so the tiebreak is unreachable. Mapping and order requests
accept an optional role; responses carry it; a second active device in a
role is a 409.

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>
EOF
```

---

### Task 5: Run the DB-backed constraint tests in CI

**Why:** CI's DB-backed job runs `go test ... -run Integration ./cmd/api/ ./cmd/acs/`. Nothing in `internal/bss` matches, so every test written in Tasks 1–3 passes locally and **never runs in CI** — already true of `internal/auth`'s replay-store tests, `internal/jobs`'s lease-recovery test, `internal/store`'s dashboard SQL gate and `internal/bss`'s OAuth revocation test. The constraint behaviour is exactly what must not regress silently.

**Files:**
- Modify: `.github/workflows/ci.yml:143-146`

**Interfaces:** none.

- [ ] **Step 1: Confirm the gap**

Run, from `backend/`, exactly what CI runs:
`ACS_TEST_POSTGRES_DSN="postgres://acs:acs@127.0.0.1:5432/acs?sslmode=disable" go test -count=1 -p 1 -run Integration ./cmd/api/ ./cmd/acs/ -v 2>&1 | grep -c "TestMigration0052\|TestSwapDevice"`

Expected: `0` — none of the new tests execute under CI's current invocation.

- [ ] **Step 2: Add a second DB-backed step**

In `.github/workflows/ci.yml`, directly after the step at lines 143–146 (`DB-backed integration tests …`), add:

```yaml
      - name: DB-backed repository tests (assignment constraints, Digest replay, lease recovery, dashboard SQL, OAuth revocation)
        env:
          ACS_TEST_POSTGRES_DSN: postgres://acs:acs@localhost:5432/acs?sslmode=disable
        # These packages' DB-backed tests are not named *Integration*, so the
        # step above never ran them. -p 1: every one resets the same database.
        run: go test -race -count=1 -p 1 ./internal/store/ ./internal/bss/ ./internal/auth/ ./internal/jobs/
```

Keep the existing step exactly as it is. Match the indentation of the surrounding steps.

- [ ] **Step 3: Verify the new step locally**

Run, from `backend/`:
`ACS_TEST_POSTGRES_DSN="postgres://acs:acs@127.0.0.1:5432/acs?sslmode=disable" go test -count=1 -p 1 ./internal/store/ ./internal/bss/ ./internal/auth/ ./internal/jobs/ -v 2>&1 | grep -E "^(=== RUN|--- (PASS|FAIL)|ok|FAIL)" | grep -E "TestMigration0052|TestSwapDevice|TestPostgresReplayStore|TestRecoverExpiredLeases|TestDashboardSQL|TestOAuthRepository" | head -20`

Expected: each of those tests appears with `--- PASS`. If `-race` is unavailable locally (Windows without CGO), run without it; CI has it.

- [ ] **Step 4: Validate the workflow file**

Run, from the repository root: `python3 -c "import yaml,sys; yaml.safe_load(open('.github/workflows/ci.yml'))" && echo "YAML OK"` (or any YAML validator available). Expected: `YAML OK`.

- [ ] **Step 5: Commit**

```bash
git add .github/workflows/ci.yml
git commit -F - <<'EOF'
ci: run the DB-backed repository tests that -run Integration skipped

The DB-backed job filtered on -run Integration across cmd/api and
cmd/acs only, so the assignment-constraint tests, the Digest replay
store, lease recovery, the dashboard SQL gate and OAuth revocation all
passed locally and never executed in CI. A second step runs those four
packages with -p 1, since each resets the shared database.

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>
EOF
```

---

## Self-Review

**1. Spec coverage.**

| Spec section | Task |
|---|---|
| §5 schema, indexes, backfill | 1 |
| §5.4 `status` not authoritative | 2 (predicates), Global Constraints |
| §5.5 backfill documented as lower bound | 1 (migration comment) |
| §6 role semantics, single-rule resolution, delete `PrimaryDeviceForAccount` | 2 (resolution), 4 (deletion) |
| §7 swap transaction, close-then-open | 3 |
| §8 repository API, `ListAll`/`Stats` predicates | 2, 3 |
| §8.1 BSS API surface (`role` on mappings and orders, default gateway) | 4 |
| §8.2 tenancy untouched | no task modifies tenancy — verified by file lists |
| §9 testing, CI extension | 1–3 (tests), 5 (CI) |
| §12.1 conflicting-data behaviour | 1 (ruling: fail loudly) |

Not in this plan: exposing `AssignmentHistory` on `/bss/v1` (spec defers to C); an operator-console history view (spec §5.5 mentions labelling backfilled rows — that is a frontend concern and a C/console follow-up, recorded here so it is not lost).

**2. Placeholder scan.** No `TBD`/`TODO`/"similar to Task N". Every code step carries code. Task 5's YAML step shows the exact block.

**3. Type consistency.** `ActiveDeviceForAccount(ctx, accountID, role string) (*AccountDeviceMapping, error)` — Task 2 defines, Tasks 3 and 4 consume. `AssignDevice(ctx, accountID, ouiSerial, role, servicePlan string)` — Task 3 defines, Task 4 consumes with that argument order. `ErrNoDeviceForRole` (Task 2), `ErrRoleAlreadyAssigned` (Task 3) consumed in Task 4 via `errors.Is`. `mappingColumns` and `scanMapping`'s ten-column order match. `roleOrDefault` defined and tested in Task 4. `resolveDeviceID`/`insertAssignment` take the narrow interfaces both `*sql.DB` and `*sql.Tx` satisfy.

**Ordering.** 1 → 2 → 3 → 4 strictly; 5 can run any time after 1 but is placed last so the tests it enables exist.
