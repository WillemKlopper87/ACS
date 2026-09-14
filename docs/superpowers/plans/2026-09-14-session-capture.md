# Session Capture Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** On-demand, application-level capture of real CWMP (`cmd/acs`) and
USP (`cmd/uspc`) session content, viewable and exportable from the operator
console, so a protocol-level bug can be diagnosed from what a device
actually said rather than what `acs.log` happened to print.

**Architecture:** A new `internal/captures` package owns the schema,
repository and redaction logic. `cmd/acs` and `cmd/uspc` each get two hook
points (one inbound, one outbound) that check for an active capture and
write a redacted event — reusing the DB touch each process already makes
per Inform/message, no new inter-process channel. `cmd/api` exposes REST
endpoints over the same repository; the console gets a new top-level
screen plus a "Start capture" action on the existing device detail page.

**Tech Stack:** Go (backend, Postgres via `database/sql`), TypeScript/React
19 (frontend), the existing `internal/retention` sweep for expiry cleanup.

**Spec:** `docs/superpowers/specs/2026-09-14-session-capture-design.md`

## Global Constraints

- **Never persist a live credential value**, even to an authenticated
  operator (spec §6): any parameter whose name matches `password`,
  `passphrase`, `secret`, `psk`, `presharedkey`, `privatekey`
  (case-insensitive substring) has its value replaced with
  `"***REDACTED***"` before it is ever written to `capture_events`. A
  `Basic` `Authorization` header is never captured verbatim — only
  `"Basic auth, username=<user>"`. `Digest` headers ARE captured as-is
  (the `response` field is a one-way hash, not the password).
- **No new inter-process channel.** `cmd/acs`/`cmd/uspc` write to
  Postgres; `cmd/api` reads/writes the same tables. No gRPC/SSE/websocket
  between backend processes.
- **On-demand only.** A capture session is always explicitly started by
  an operator and hard-capped in duration (`ACS_CAPTURE_MAX_DURATION`,
  default `30m`). No always-on/rolling-buffer capture.
- **Three trigger modes**: `device`, `identity`, `remote_ip` (spec §4).
  `device` and `identity` both match on the device's `oui_serial` natural
  key (`cwmp.DeviceID.NaturalKey()` shape) — the SAME representation, so
  the per-event check is one query shape, not two. `remote_ip` matches a
  single exact IP address string, never a CIDR range.
- **At most one `ACTIVE` capture per `(match_type, match_value)`** — a
  database guarantee via a partial unique index, not application
  discipline.
- **Retention defaults**: `ACS_RETENTION_CAPTURE_SESSIONS_DAYS=1`
  (matches this codebase's existing `internal/retention` days-only
  convention — the spec's own `ACS_CAPTURE_RETENTION_HOURS` sketch is
  superseded by this, since 24h backfills to exactly 1 day and this way
  the new rule fits the established mechanism with zero new code shape).
- **Every new `cmd/api` route needs a matching `backend/openapi.yaml`
  entry** — CI enforces no drift between it and the frontend's generated
  types.

---

### Task 1: `internal/captures` — schema, types, repository

**Files:**
- Create: `backend/internal/store/migrations/00NN_capture_sessions.sql`
  (NN = next free migration number at implementation time — read
  `backend/internal/store/migrations/` first to confirm it)
- Create: `backend/internal/captures/captures.go`
- Create: `backend/internal/captures/captures_test.go`

**Interfaces:**
- Consumes: nothing new (uses `database/sql`, the existing migration
  runner).
- Produces: `captures.Session{ID, DeviceID *string, MatchType,
  MatchValue, Protocol, Status, StartedBy string, StartedAt time.Time,
  StoppedAt *time.Time, ExpiresAt time.Time}`; `captures.Event{ID,
  SessionID string, Seq int, Direction, Kind string, OccurredAt
  time.Time, Summary string, Body *string}`; `captures.ErrAlreadyActive`
  (returned on the partial-unique-index conflict);
  `captures.Repository{Start, Stop, ActiveMatch, RecordEvent, List, Get,
  ListEvents}` — consumed by exact name/signature in every later task.

- [ ] **Step 1: Write the failing tests**

Create `backend/internal/captures/captures_test.go`:

```go
package captures

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	"acs/internal/store"
)

func newTestDB(t *testing.T) (context.Context, *sql.DB) {
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
	return ctx, db
}

func TestStartAndGet(t *testing.T) {
	ctx, db := newTestDB(t)
	r := NewRepository(db)

	s, err := r.Start(ctx, StartParams{
		MatchType: MatchDevice, MatchValue: "001349+S1", Protocol: "CWMP",
		StartedBy: "operator@example.com", MaxDuration: 30 * time.Minute,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if s.Status != StatusActive {
		t.Errorf("Status = %q, want ACTIVE", s.Status)
	}

	got, err := r.Get(ctx, s.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.MatchValue != "001349+S1" || got.Protocol != "CWMP" {
		t.Errorf("Get() = %+v, want the started session", got)
	}
}

func TestStartRejectsDuplicateActiveMatch(t *testing.T) {
	ctx, db := newTestDB(t)
	r := NewRepository(db)

	params := StartParams{MatchType: MatchIdentity, MatchValue: "001349+S2", Protocol: "CWMP", StartedBy: "op", MaxDuration: time.Minute}
	if _, err := r.Start(ctx, params); err != nil {
		t.Fatalf("first Start: %v", err)
	}
	if _, err := r.Start(ctx, params); err != ErrAlreadyActive {
		t.Errorf("second Start on the same (match_type, match_value) = %v, want ErrAlreadyActive", err)
	}
}

func TestStartAllowsSameMatchValueAfterStop(t *testing.T) {
	ctx, db := newTestDB(t)
	r := NewRepository(db)

	params := StartParams{MatchType: MatchRemoteIP, MatchValue: "10.0.0.5", Protocol: "USP", StartedBy: "op", MaxDuration: time.Minute}
	s1, err := r.Start(ctx, params)
	if err != nil {
		t.Fatalf("first Start: %v", err)
	}
	if err := r.Stop(ctx, s1.ID); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if _, err := r.Start(ctx, params); err != nil {
		t.Errorf("Start after Stop = %v, want success (the unique index is scoped to status=ACTIVE)", err)
	}
}

func TestActiveMatchFindsRunningSessionOnly(t *testing.T) {
	ctx, db := newTestDB(t)
	r := NewRepository(db)

	s, err := r.Start(ctx, StartParams{MatchType: MatchIdentity, MatchValue: "001349+S3", Protocol: "CWMP", StartedBy: "op", MaxDuration: time.Minute})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	found, err := r.ActiveMatch(ctx, "CWMP", "001349+S3", "")
	if err != nil {
		t.Fatalf("ActiveMatch: %v", err)
	}
	if len(found) != 1 || found[0].ID != s.ID {
		t.Fatalf("ActiveMatch = %+v, want exactly the started session", found)
	}

	if err := r.Stop(ctx, s.ID); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	found, err = r.ActiveMatch(ctx, "CWMP", "001349+S3", "")
	if err != nil {
		t.Fatalf("ActiveMatch after stop: %v", err)
	}
	if len(found) != 0 {
		t.Errorf("ActiveMatch after Stop = %+v, want none", found)
	}
}

func TestActiveMatchExcludesExpired(t *testing.T) {
	ctx, db := newTestDB(t)
	r := NewRepository(db)

	s, err := r.Start(ctx, StartParams{MatchType: MatchIdentity, MatchValue: "001349+S4", Protocol: "CWMP", StartedBy: "op", MaxDuration: -time.Minute})
	if err != nil {
		t.Fatalf("Start with already-past expiry: %v", err)
	}
	found, err := r.ActiveMatch(ctx, "CWMP", "001349+S4", "")
	if err != nil {
		t.Fatalf("ActiveMatch: %v", err)
	}
	if len(found) != 0 {
		t.Errorf("ActiveMatch for an expired session = %+v, want none", found)
	}
	_ = s
}

func TestActiveMatchByRemoteIP(t *testing.T) {
	ctx, db := newTestDB(t)
	r := NewRepository(db)

	if _, err := r.Start(ctx, StartParams{MatchType: MatchRemoteIP, MatchValue: "192.168.1.50", Protocol: "USP", StartedBy: "op", MaxDuration: time.Minute}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	found, err := r.ActiveMatch(ctx, "USP", "", "192.168.1.50")
	if err != nil {
		t.Fatalf("ActiveMatch: %v", err)
	}
	if len(found) != 1 {
		t.Fatalf("ActiveMatch by remote IP = %+v, want exactly one", found)
	}
	found, err = r.ActiveMatch(ctx, "USP", "", "10.10.10.10")
	if err != nil {
		t.Fatalf("ActiveMatch: %v", err)
	}
	if len(found) != 0 {
		t.Errorf("ActiveMatch for a non-matching IP = %+v, want none", found)
	}
}

func TestRecordEventAndListEventsOrdered(t *testing.T) {
	ctx, db := newTestDB(t)
	r := NewRepository(db)

	s, err := r.Start(ctx, StartParams{MatchType: MatchDevice, MatchValue: "001349+S5", Protocol: "CWMP", StartedBy: "op", MaxDuration: time.Minute})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	body1 := "<Inform>...</Inform>"
	if err := r.RecordEvent(ctx, s.ID, "inbound", "Inform", "Inform received", &body1); err != nil {
		t.Fatalf("RecordEvent 1: %v", err)
	}
	if err := r.RecordEvent(ctx, s.ID, "outbound", "SetParameterValues", "dispatched SetParameterValues", nil); err != nil {
		t.Fatalf("RecordEvent 2: %v", err)
	}

	events, err := r.ListEvents(ctx, s.ID)
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("ListEvents = %d events, want 2", len(events))
	}
	if events[0].Seq != 1 || events[0].Kind != "Inform" || events[1].Seq != 2 || events[1].Kind != "SetParameterValues" {
		t.Errorf("events out of order or wrong: %+v", events)
	}
	if events[0].Body == nil || *events[0].Body != body1 {
		t.Errorf("events[0].Body = %v, want %q", events[0].Body, body1)
	}
	if events[1].Body != nil {
		t.Errorf("events[1].Body = %v, want nil", events[1].Body)
	}
}

func TestListReturnsNewestFirst(t *testing.T) {
	ctx, db := newTestDB(t)
	r := NewRepository(db)

	s1, err := r.Start(ctx, StartParams{MatchType: MatchDevice, MatchValue: "001349+S6", Protocol: "CWMP", StartedBy: "op", MaxDuration: time.Minute})
	if err != nil {
		t.Fatalf("Start s1: %v", err)
	}
	s2, err := r.Start(ctx, StartParams{MatchType: MatchDevice, MatchValue: "001349+S7", Protocol: "CWMP", StartedBy: "op", MaxDuration: time.Minute})
	if err != nil {
		t.Fatalf("Start s2: %v", err)
	}

	list, err := r.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 2 || list[0].ID != s2.ID || list[1].ID != s1.ID {
		t.Fatalf("List() = %+v, want [s2, s1] (newest first)", list)
	}
}
```

- [ ] **Step 2: Run to verify they fail**

Run: `cd backend && go test ./internal/captures/... -v`
Expected: FAIL to compile — package `captures` doesn't exist yet.

- [ ] **Step 3: Write the migration**

Read `backend/internal/store/migrations/` first to find the actual next
free number (this plan was written when `0056` was the latest on `main`;
confirm rather than assume). Create
`backend/internal/store/migrations/00NN_capture_sessions.sql`:

```sql
-- On-demand session capture (design docs/superpowers/specs/
-- 2026-09-14-session-capture-design.md). match_value is always
-- expressed in its mode's own terms: 'device' and 'identity' both use
-- the device's oui_serial natural key (cwmp.DeviceID.NaturalKey()
-- shape), deliberately the same representation for both so cmd/acs's
-- per-event check is one query shape; 'remote_ip' uses a single exact
-- IP address string, never a CIDR range. device_id is a denormalized
-- resolution column, never the match key itself even for
-- match_type='device'.
CREATE TABLE capture_sessions (
    id           UUID PRIMARY KEY,
    device_id    UUID REFERENCES devices(id),
    match_type   TEXT NOT NULL CHECK (match_type IN ('device','identity','remote_ip')),
    match_value  TEXT NOT NULL,
    protocol     TEXT NOT NULL CHECK (protocol IN ('CWMP','USP')),
    status       TEXT NOT NULL DEFAULT 'ACTIVE' CHECK (status IN ('ACTIVE','STOPPED','EXPIRED')),
    started_by   TEXT NOT NULL,
    started_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    stopped_at   TIMESTAMPTZ,
    expires_at   TIMESTAMPTZ NOT NULL
);
CREATE UNIQUE INDEX capture_sessions_active_match_idx
    ON capture_sessions (match_type, match_value) WHERE status = 'ACTIVE';
CREATE INDEX capture_sessions_device_idx ON capture_sessions (device_id) WHERE device_id IS NOT NULL;

CREATE TABLE capture_events (
    id          UUID PRIMARY KEY,
    session_id  UUID NOT NULL REFERENCES capture_sessions(id) ON DELETE CASCADE,
    seq         INTEGER NOT NULL,
    direction   TEXT NOT NULL CHECK (direction IN ('inbound','outbound')),
    kind        TEXT NOT NULL,
    occurred_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    summary     TEXT NOT NULL,
    body        TEXT,
    UNIQUE (session_id, seq)
);
CREATE INDEX capture_events_session_idx ON capture_events (session_id, seq);
```

- [ ] **Step 4: Write `captures.go`**

Create `backend/internal/captures/captures.go`:

```go
// Package captures implements on-demand session capture (design
// docs/superpowers/specs/2026-09-14-session-capture-design.md): a
// device's, an expected identity's, or a remote address's CWMP/USP
// session traffic, redacted, recorded to Postgres for operator
// troubleshooting. No HTTP handler here and no direct dependency on
// cmd/acs/cmd/uspc/cmd/api -- this is the shared repository all three
// call into.
package captures

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

const (
	MatchDevice   = "device"
	MatchIdentity = "identity"
	MatchRemoteIP = "remote_ip"

	StatusActive  = "ACTIVE"
	StatusStopped = "STOPPED"
	StatusExpired = "EXPIRED"
)

// ErrAlreadyActive is returned by Start when an ACTIVE session already
// exists for the same (match_type, match_value) -- the partial unique
// index turning that into a database guarantee, not application
// discipline.
var ErrAlreadyActive = errors.New("an active capture already exists for this target")

// ErrNotFound is returned by Get/Stop for an unknown session id.
var ErrNotFound = errors.New("no capture session with that id")

type Session struct {
	ID         string
	DeviceID   *string
	MatchType  string
	MatchValue string
	Protocol   string
	Status     string
	StartedBy  string
	StartedAt  time.Time
	StoppedAt  *time.Time
	ExpiresAt  time.Time
}

// EffectiveStatus reports what the console should display: an ACTIVE
// row whose expiry has already passed reads as EXPIRED even though no
// background job has flipped its stored status yet (design §7 -- the
// per-event check already treats it as inert via expires_at, so no
// sweep is needed for correctness, only for this display).
func (s Session) EffectiveStatus(now time.Time) string {
	if s.Status == StatusActive && now.After(s.ExpiresAt) {
		return StatusExpired
	}
	return s.Status
}

type Event struct {
	ID         string
	SessionID  string
	Seq        int
	Direction  string
	Kind       string
	OccurredAt time.Time
	Summary    string
	Body       *string
}

type StartParams struct {
	DeviceID    *string // set only for MatchDevice, where it's already known
	MatchType   string
	MatchValue  string
	Protocol    string
	StartedBy   string
	MaxDuration time.Duration
}

type Repository struct {
	db *sql.DB
}

func NewRepository(db *sql.DB) *Repository {
	return &Repository{db: db}
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

const sessionColumns = `id, device_id, match_type, match_value, protocol, status, started_by, started_at, stopped_at, expires_at`

type scanner interface {
	Scan(dest ...any) error
}

func scanSession(s scanner) (*Session, error) {
	var sess Session
	var deviceID sql.NullString
	var stoppedAt sql.NullTime
	if err := s.Scan(&sess.ID, &deviceID, &sess.MatchType, &sess.MatchValue, &sess.Protocol,
		&sess.Status, &sess.StartedBy, &sess.StartedAt, &stoppedAt, &sess.ExpiresAt); err != nil {
		return nil, fmt.Errorf("scan capture session: %w", err)
	}
	if deviceID.Valid {
		sess.DeviceID = &deviceID.String
	}
	if stoppedAt.Valid {
		sess.StoppedAt = &stoppedAt.Time
	}
	return &sess, nil
}

// Start creates a new ACTIVE capture session. Returns ErrAlreadyActive
// if one already exists for the same (match_type, match_value).
func (r *Repository) Start(ctx context.Context, p StartParams) (*Session, error) {
	id := uuid.New().String()
	now := time.Now().UTC()
	expiresAt := now.Add(p.MaxDuration)
	row := r.db.QueryRowContext(ctx, `
		INSERT INTO capture_sessions (id, device_id, match_type, match_value, protocol, status, started_by, started_at, expires_at)
		VALUES ($1, $2, $3, $4, $5, 'ACTIVE', $6, $7, $8)
		RETURNING `+sessionColumns,
		id, p.DeviceID, p.MatchType, p.MatchValue, p.Protocol, p.StartedBy, now, expiresAt)
	sess, err := scanSession(row)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, ErrAlreadyActive
		}
		return nil, err
	}
	return sess, nil
}

// Stop marks a session STOPPED. A no-op (not an error) if it is already
// non-ACTIVE.
func (r *Repository) Stop(ctx context.Context, id string) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE capture_sessions SET status = 'STOPPED', stopped_at = now() WHERE id = $1 AND status = 'ACTIVE'`, id)
	return err
}

// Get looks up one session by id.
func (r *Repository) Get(ctx context.Context, id string) (*Session, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+sessionColumns+` FROM capture_sessions WHERE id = $1`, id)
	sess, err := scanSession(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return sess, err
}

// List returns every capture session, newest first.
func (r *Repository) List(ctx context.Context) ([]Session, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+sessionColumns+` FROM capture_sessions ORDER BY started_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("list capture sessions: %w", err)
	}
	defer rows.Close()
	var out []Session
	for rows.Next() {
		s, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *s)
	}
	return out, rows.Err()
}

// ActiveMatch returns every ACTIVE, unexpired session matching this
// request: MatchDevice/MatchIdentity sessions whose match_value equals
// naturalKey (deliberately the same lookup for both modes -- see the
// migration's own comment), plus MatchRemoteIP sessions whose
// match_value equals remoteIP. Either naturalKey or remoteIP may be
// empty (a caller with no resolved identity yet passes "" for
// naturalKey; USP's outbound dispatch, always device-scoped, has no
// remote address to check and passes "" for remoteIP).
func (r *Repository) ActiveMatch(ctx context.Context, protocol, naturalKey, remoteIP string) ([]Session, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT `+sessionColumns+` FROM capture_sessions
		WHERE protocol = $1 AND status = 'ACTIVE' AND expires_at > now()
		AND (
			(match_type IN ('device','identity') AND match_value = $2 AND $2 <> '')
			OR (match_type = 'remote_ip' AND match_value = $3 AND $3 <> '')
		)`, protocol, naturalKey, remoteIP)
	if err != nil {
		return nil, fmt.Errorf("active capture match: %w", err)
	}
	defer rows.Close()
	var out []Session
	for rows.Next() {
		s, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *s)
	}
	return out, rows.Err()
}

// ResolveDeviceID backfills device_id on an 'identity' (or 'remote_ip')
// session once the device it belongs to has actually authenticated
// (design §4) -- a no-op if device_id is already set.
func (r *Repository) ResolveDeviceID(ctx context.Context, sessionID, deviceID string) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE capture_sessions SET device_id = $2 WHERE id = $1 AND device_id IS NULL`, sessionID, deviceID)
	return err
}

// RecordEvent appends one event to a session, seq auto-assigned as
// max(seq)+1 for that session (starting at 1). body is nil for an event
// with nothing worth attaching (e.g. a bare dispatch trigger); callers
// are responsible for having already redacted it.
func (r *Repository) RecordEvent(ctx context.Context, sessionID, direction, kind, summary string, body *string) error {
	id := uuid.New().String()
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO capture_events (id, session_id, seq, direction, kind, summary, body)
		VALUES ($1, $2, COALESCE((SELECT MAX(seq) FROM capture_events WHERE session_id = $2), 0) + 1, $3, $4, $5, $6)`,
		id, sessionID, direction, kind, summary, body)
	return err
}

// ListEvents returns a session's events in seq order.
func (r *Repository) ListEvents(ctx context.Context, sessionID string) ([]Event, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, session_id, seq, direction, kind, occurred_at, summary, body
		FROM capture_events WHERE session_id = $1 ORDER BY seq ASC`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("list capture events: %w", err)
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var e Event
		var body sql.NullString
		if err := rows.Scan(&e.ID, &e.SessionID, &e.Seq, &e.Direction, &e.Kind, &e.OccurredAt, &e.Summary, &body); err != nil {
			return nil, fmt.Errorf("scan capture event: %w", err)
		}
		if body.Valid {
			e.Body = &body.String
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
```

- [ ] **Step 5: Run to verify they pass**

Run: `cd backend && export ACS_TEST_POSTGRES_DSN=<your test DSN> && go test ./internal/captures/... -v`
Expected: PASS, all 8 tests.

- [ ] **Step 6: `gofmt`, `vet`, build**

Run: `cd backend && go build ./... && go vet ./internal/captures/... && gofmt -l internal/captures/ internal/store/migrations/`
Expected: clean.

- [ ] **Step 7: Commit**

```bash
git add backend/internal/store/migrations/*_capture_sessions.sql backend/internal/captures/captures.go backend/internal/captures/captures_test.go
git commit -m "$(cat <<'EOF'
feat(captures): schema and repository for on-demand session capture

Sub-project: session capture (design S5). capture_sessions/
capture_events tables, plus internal/captures.Repository (Start/Stop/
Get/List/ActiveMatch/RecordEvent/ListEvents). ActiveMatch is the
per-event checkpoint cmd/acs/cmd/uspc will call: device and identity
modes share one lookup (both match on the device's oui_serial natural
key), remote_ip is a separate exact-string check. The partial unique
index on (match_type, match_value) WHERE status='ACTIVE' makes
"at most one active capture per target" a database guarantee.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 2: Redaction

**Files:**
- Create: `backend/internal/captures/redact.go`
- Create: `backend/internal/captures/redact_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `captures.RedactParamValue(name, value string) string`;
  `captures.RedactAuthHeader(header string) string` — both consumed by
  exact name in Tasks 3 and 4.

- [ ] **Step 1: Write the failing tests**

Create `backend/internal/captures/redact_test.go`:

```go
package captures

import "testing"

func TestRedactParamValue(t *testing.T) {
	sensitive := []string{
		"Device.WiFi.AccessPoint.1.Security.KeyPassphrase",
		"Device.WiFi.AccessPoint.1.Security.PreSharedKey.1.KeyPassphrase",
		"X_HUAWEI_AdminPassword",
		"Device.ManagementServer.ConnectionRequestPassword",
		"some.PSK.value",
		"Secret",
	}
	for _, name := range sensitive {
		if got := RedactParamValue(name, "letmein123"); got != "***REDACTED***" {
			t.Errorf("RedactParamValue(%q, ...) = %q, want ***REDACTED***", name, got)
		}
	}

	notSensitive := []string{"Device.WiFi.SSID.1.SSID", "Device.DeviceInfo.SoftwareVersion", "Device.WiFi.AccessPoint.1.Enable"}
	for _, name := range notSensitive {
		if got := RedactParamValue(name, "MyNetwork"); got != "MyNetwork" {
			t.Errorf("RedactParamValue(%q, ...) = %q, want the original value unredacted", name, got)
		}
	}
}

func TestRedactAuthHeader(t *testing.T) {
	digest := `Digest username="dev1", realm="acs", nonce="abc", uri="/cwmp", response="deadbeef"`
	if got := RedactAuthHeader(digest); got != digest {
		t.Errorf("RedactAuthHeader(Digest) = %q, want it captured verbatim (response is a one-way hash)", got)
	}

	basic := "Basic ZGV2MTpzM2NyZXQ="
	got := RedactAuthHeader(basic)
	if got == basic {
		t.Error("RedactAuthHeader(Basic) returned the header verbatim, want it redacted")
	}
	if got != "Basic auth, username=dev1" {
		t.Errorf("RedactAuthHeader(Basic) = %q, want %q", got, "Basic auth, username=dev1")
	}

	if got := RedactAuthHeader(""); got != "" {
		t.Errorf("RedactAuthHeader(\"\") = %q, want empty", got)
	}
}
```

- [ ] **Step 2: Run to verify they fail**

Run: `cd backend && go test ./internal/captures/... -run TestRedact -v`
Expected: FAIL to compile — `RedactParamValue`/`RedactAuthHeader` don't
exist yet.

- [ ] **Step 3: Write `redact.go`**

Create `backend/internal/captures/redact.go`:

```go
package captures

import (
	"encoding/base64"
	"strings"
)

// sensitiveNamePatterns is deliberately a pattern match, not an exact
// allowlist tied to this codebase's known canonical parameters (design
// §6) -- CWMP/USP can target arbitrary vendor-specific paths, and a
// pattern catches those too. Over-redacting something that merely
// contains "password" in its name but isn't actually secret is a cheap
// false positive; under-redacting a real secret is not acceptable.
var sensitiveNamePatterns = []string{
	"password", "passphrase", "secret", "psk", "presharedkey", "privatekey",
}

const redactedMarker = "***REDACTED***"

// RedactParamValue returns value unchanged unless name looks like a
// secret parameter (case-insensitive substring match against
// sensitiveNamePatterns), in which case it returns the fixed marker.
// The name itself is never touched -- an operator can still see which
// parameter was being set, only the value is masked.
func RedactParamValue(name, value string) string {
	lower := strings.ToLower(name)
	for _, pattern := range sensitiveNamePatterns {
		if strings.Contains(lower, pattern) {
			return redactedMarker
		}
	}
	return value
}

// RedactAuthHeader returns a Digest Authorization header verbatim (its
// response field is a one-way hash, not the password, and seeing it is
// the actual diagnostic payload this feature exists to show) but never
// returns a Basic header's credential -- Basic is a trivially-reversible
// base64 encoding of the real credential, so only the username is kept.
func RedactAuthHeader(header string) string {
	if header == "" {
		return ""
	}
	if rest, ok := strings.CutPrefix(header, "Basic "); ok {
		decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(rest))
		if err != nil {
			return "Basic auth (undecodable)"
		}
		user, _, _ := strings.Cut(string(decoded), ":")
		return "Basic auth, username=" + user
	}
	return header
}
```

- [ ] **Step 4: Run to verify they pass**

Run: `cd backend && go test ./internal/captures/... -run TestRedact -v`
Expected: PASS, both tests.

- [ ] **Step 5: `gofmt`, `vet`, build**

Run: `cd backend && go build ./... && go vet ./internal/captures/... && gofmt -l internal/captures/`
Expected: clean.

- [ ] **Step 6: Commit**

```bash
git add backend/internal/captures/redact.go backend/internal/captures/redact_test.go
git commit -m "$(cat <<'EOF'
feat(captures): redaction (design S6)

RedactParamValue masks a parameter's value (never its name) when the
name pattern-matches a sensitivity list -- password/passphrase/secret/
psk/presharedkey/privatekey, case-insensitive substring, deliberately
broader than this codebase's own named canonical parameters so a
vendor-specific secret (X_HUAWEI_AdminPassword) is caught too.
RedactAuthHeader keeps a Digest header verbatim (its response is a
one-way hash) but strips a Basic header down to just the username,
since Basic is trivially reversible.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 3: `cmd/acs` capture hooks

**Files:**
- Modify: `backend/cmd/acs/session.go`
- Modify: `backend/cmd/acs/main.go` (wire `captures.Repository` into the handler struct)
- Test: `backend/cmd/acs/capture_test.go`

**Interfaces:**
- Consumes: `captures.Repository{ActiveMatch, RecordEvent, ResolveDeviceID}`
  (Task 1), `captures.RedactParamValue`/`RedactAuthHeader` (Task 2).
- Produces: `handler.captureInbound(ctx, naturalKey, remoteIP,
  deviceID, kind, summary string, redactedBody *string)` and
  `handler.captureOutbound(ctx, deviceID, kind, summary string,
  redactedBody *string)` helper methods — consumed by exact name by
  Task 6 if it needs to test through the handler; internal to this task
  otherwise.

- [ ] **Step 1: Read `session.go` in full first**

Read `backend/cmd/acs/session.go` completely before editing — confirm
`handleInform` and `dispatch` still match what this plan assumes
(verbatim bodies were captured for this plan; re-verify line numbers and
surrounding code before inserting, since this file may have shifted).

- [ ] **Step 2: Add the capture repository to `handler` and wire it in `main.go`**

In `backend/cmd/acs/main.go`, find where other repositories (`h.devices`,
`h.sessions`, `h.jobs`, etc.) are constructed and added to the `handler`
struct literal. Add:

```go
	captureRepo := captures.NewRepository(db)
```

and add `captures *captures.Repository` as a new field on the `handler`
struct (wherever `devices *devices.Repository` etc. are declared), then
add `captures: captureRepo,` to the struct literal where `h := &handler{...}`
is built. Add `"acs/internal/captures"` to `main.go`'s imports.

Also read `ACS_CAPTURE_MAX_DURATION`'s env var here, matching the
existing `envDays`-style pattern this codebase uses elsewhere (e.g.
`internal/retention.envDays`), but for a `time.Duration`:

```go
	captureMaxDuration := 30 * time.Minute
	if v := os.Getenv("ACS_CAPTURE_MAX_DURATION"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			captureMaxDuration = d
		}
	}
```

Store it as `h.captureMaxDuration` (a `time.Duration` field on `handler`)
— Task 6 (`cmd/api`) needs the SAME default when it starts a session, so
note this value in your Task 6 dispatch rather than re-deriving it
independently; for now it's read here only because `cmd/acs`'s `main.go`
is where every other `ACS_*` env var in this process is already read.

- [ ] **Step 3: Add the inbound capture hook to `handleInform`**

In `backend/cmd/acs/session.go`, add this new method near `handleInform`:

```go
// captureInbound writes a redacted capture_events row for every ACTIVE
// session matching this request (design §4/§5) -- naturalKey covers
// 'device'/'identity' modes, remoteIP covers 'remote_ip'. deviceID,
// once known, backfills any 'identity'-mode session's device_id
// (design §4: "once the device does authenticate for real, the
// session's device_id is backfilled"). No-op (not an error) if nothing
// is capturing right now -- the common case, so this must stay a cheap
// single indexed query.
func (h *handler) captureInbound(ctx context.Context, naturalKey, remoteIP, deviceID, kind, summary string, redactedBody *string) {
	sessions, err := h.captures.ActiveMatch(ctx, "CWMP", naturalKey, remoteIP)
	if err != nil {
		h.logger.Error("failed to check active captures", "err", err, "natural_key", naturalKey)
		return
	}
	for _, s := range sessions {
		if s.DeviceID == nil && deviceID != "" {
			if err := h.captures.ResolveDeviceID(ctx, s.ID, deviceID); err != nil {
				h.logger.Error("failed to backfill capture session device_id", "err", err, "session_id", s.ID)
			}
		}
		if err := h.captures.RecordEvent(ctx, s.ID, "inbound", kind, summary, redactedBody); err != nil {
			h.logger.Error("failed to record capture event", "err", err, "session_id", s.ID)
		}
	}
}
```

Then in `handleInform`, right after the `device, err := h.devices.UpsertFromInform(...)`
block succeeds (after its own error-handling `if err != nil { ...; return }`,
before the `h.devices.UpdateAuthMode(...)` call), insert:

```go
	h.captureInbound(ctx, naturalKey, remoteIP(r), device.ID, "Inform",
		fmt.Sprintf("Inform (events: %v)", events), captureInformBody(inform, r.Header.Get("Authorization")))
```

Add this helper (near `captureInbound`) to build the redacted body text —
a simple, readable summary rather than re-serializing to XML (the spec's
§6 redaction rule operates on parsed data; this renders that redacted
form as plain text for the console to display, not as re-emitted CWMP
XML the console has no reason to parse back):

```go
// captureInformBody renders a redacted, human-readable summary of an
// Inform for capture storage -- the Authorization header (redacted per
// RedactAuthHeader) plus every parameter the Inform carried, with any
// sensitive value masked via RedactParamValue.
func captureInformBody(inform *cwmp.Inform, authHeader string) *string {
	var b strings.Builder
	fmt.Fprintf(&b, "Authorization: %s\n", captures.RedactAuthHeader(authHeader))
	fmt.Fprintf(&b, "DeviceId: OUI=%s ProductClass=%s SerialNumber=%s\n",
		inform.DeviceId.OUI, inform.DeviceId.ProductClass, inform.DeviceId.SerialNumber)
	for _, p := range inform.ParameterList {
		fmt.Fprintf(&b, "%s = %s\n", p.Name, captures.RedactParamValue(p.Name, p.Value))
	}
	s := b.String()
	return &s
}
```

Add `"acs/internal/captures"` and `"strings"` to `session.go`'s imports
if not already present (check first).

- [ ] **Step 4: Add the outbound capture hook to `dispatch`**

In `dispatch`, right before `_, _ = w.Write(requestBody)` at the end of
the function, insert:

```go
	h.captureOutbound(ctx, session.DeviceID, job.Type, fmt.Sprintf("dispatching %s (command_key=%s)", job.Type, job.CommandKey), captureDispatchBody(requestBody))
```

Add the corresponding helper and the outbound capture method:

```go
// captureOutbound is captureInbound's counterpart for a dispatch this
// process itself is sending, always device-scoped (no remote-address
// ambiguity for an outbound RPC) -- so it only ever needs the device's
// own natural key resolved via deviceID, not a separate remote-IP path.
func (h *handler) captureOutbound(ctx context.Context, deviceID, kind, summary string, redactedBody *string) {
	device, err := h.devices.Get(ctx, deviceID)
	if err != nil {
		h.logger.Error("failed to resolve device for outbound capture check", "err", err, "device_id", deviceID)
		return
	}
	sessions, err := h.captures.ActiveMatch(ctx, "CWMP", device.OUISerial, "")
	if err != nil {
		h.logger.Error("failed to check active captures", "err", err, "device_id", deviceID)
		return
	}
	for _, s := range sessions {
		if err := h.captures.RecordEvent(ctx, s.ID, "outbound", kind, summary, redactedBody); err != nil {
			h.logger.Error("failed to record capture event", "err", err, "session_id", s.ID)
		}
	}
}

// captureDispatchBody redacts an outbound CWMP RPC's raw XML body for
// capture storage by re-parsing the parameter writes it carries (only
// SetParameterValues has redaction-relevant content; other RPC types
// pass through as-is since they carry no parameter values). This does a
// second, cheap parse of requestBody specifically for capture purposes
// -- read internal/cwmp's SetParameterValues request shape first to
// confirm the right parse entry point (it renders requests but may not
// yet expose a matching *parse*; if none exists, a simple regex-free
// substring scan for "<Name>...</Name><Value...>...</Value>" pairs is
// an acceptable, narrowly-scoped fallback here specifically because
// this is capture-only tooling, not a security boundary parse -- the
// real dispatch path never uses this function's output for anything
// but display).
func captureDispatchBody(requestBody []byte) *string {
	s := string(requestBody)
	// Redact every <Value ...>...</Value> whose preceding <Name>...</Name>
	// matches a sensitive pattern. Implementer: write this against a real
	// captured SetParameterValues fixture (Task 1's test data or
	// internal/cwmp's own rpc_test.go has one) rather than guessing the
	// exact whitespace/attribute shape RenderSetParameterValues produces.
	return &s
}
```

**Note on Step 4's last function**: the plan intentionally leaves
`captureDispatchBody`'s redaction body as a real gap for you to close
with the exact XML shape `internal/cwmp/rpc.go`'s `RenderSetParameterValues`
actually emits (read it — this plan's earlier research already quoted it
in full) rather than guessing at parsing logic here. Implement it by
scanning the rendered XML for `<Name>NAME</Name><Value xsi:type="...">VALUE</Value>`
pairs (a simple state-machine scan or `encoding/xml` decode into a
throwaway struct mirroring `cwmp.ParameterValueStruct`) and rebuilding
the string with `captures.RedactParamValue`-masked values — do not ship
this function returning the unredacted body verbatim; write a test
proving a `KeyPassphrase`-named parameter's value does not appear in its
output before moving on.

- [ ] **Step 5: Write the test**

Create `backend/cmd/acs/capture_test.go` — read `cmd/acs/session_integration_test.go`
first to copy its real test-harness setup (mock CPE session driver, DB
connection pattern) rather than inventing a new one. Write at minimum:

```go
package main

import (
	"strings"
	"testing"
)

// TestCaptureDispatchBodyRedactsKeyPassphrase is the one hard
// requirement Step 4 calls out explicitly: whatever captureDispatchBody
// implementation you write, a KeyPassphrase value must never appear in
// its output.
func TestCaptureDispatchBodyRedactsKeyPassphrase(t *testing.T) {
	// Build requestBody from cwmp.RenderSetParameterValues with a real
	// KeyPassphrase-named parameter (read rpc.go's real signature and
	// call it here rather than hand-writing XML), then:
	//
	//   got := captureDispatchBody(requestBody)
	//   if strings.Contains(*got, "the-real-secret-value") { t.Fatal(...) }
	//   if !strings.Contains(*got, "***REDACTED***") { t.Fatal(...) }
	_ = strings.Contains // placeholder import use; remove once the real test body is written
}
```

Also write an integration-style test (matching
`session_integration_test.go`'s pattern) proving: starting an
`identity`-mode capture for a not-yet-onboarded device's natural key,
then driving a real Inform through `handleInform` for that identity,
produces exactly one `capture_events` row with `kind="Inform"` and the
session's `device_id` backfilled.

- [ ] **Step 6: Run to verify they pass**

Run: `cd backend && export ACS_TEST_POSTGRES_DSN=<your test DSN> && go test ./cmd/acs/... -run TestCapture -v`
Expected: PASS.

- [ ] **Step 7: Run the full `cmd/acs` suite, `gofmt`, `vet`**

Run: `cd backend && go test ./cmd/acs/... -v 2>&1 | tail -150 && gofmt -l cmd/acs/ && go vet ./cmd/acs/...`
Expected: all PASS (every existing `cmd/acs` test, unaffected — the added
checks are cheap no-ops when nothing is capturing), clean.

- [ ] **Step 8: Commit**

```bash
git add backend/cmd/acs/session.go backend/cmd/acs/main.go backend/cmd/acs/capture_test.go
git commit -m "$(cat <<'EOF'
feat(acs): wire session capture into Inform/dispatch (design S5)

captureInbound (handleInform) and captureOutbound (dispatch) each
check captures.Repository.ActiveMatch and write a redacted
capture_events row when a session is watching -- a cheap no-op when
nothing is capturing, the common case. Inbound also backfills an
'identity'-mode session's device_id once the device actually
authenticates.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 4: `cmd/uspc` capture hooks

**Files:**
- Modify: `backend/cmd/uspc/handler.go`
- Modify: `backend/cmd/uspc/dispatcher.go`
- Modify: `backend/cmd/uspc/main.go` (wire `captures.Repository` into the handler/dispatcher)
- Test: `backend/cmd/uspc/capture_test.go`

**Interfaces:**
- Consumes: `captures.Repository{ActiveMatch, RecordEvent, ResolveDeviceID}`
  (Task 1), `captures.RedactParamValue`/`RedactAuthHeader` (Task 2).
- Produces: nothing consumed elsewhere in this plan.

- [ ] **Step 1: Read `handler.go` and `dispatcher.go` in full first**

Read `backend/cmd/uspc/handler.go`'s `OnRecord` and
`backend/cmd/uspc/dispatcher.go`'s `tryDispatch` completely before editing
— confirm they still match what this plan assumes (verbatim bodies were
captured for this plan; re-verify before inserting).

- [ ] **Step 2: Add the capture repository to `handler` and `dispatcher`, wire in `main.go`**

Mirror Task 3 Step 2's pattern exactly: add a `captures *captures.Repository`
field to both the `handler` struct (`handler.go`) and the `dispatcher`
struct (`dispatcher.go`), construct one `captures.NewRepository(db)` in
`main.go` and pass it to both constructors. Add `"acs/internal/captures"`
to both files' imports.

- [ ] **Step 3: Add the inbound capture hook to `OnRecord`**

In `backend/cmd/uspc/handler.go`, add this method:

```go
// captureInbound is OnRecord's capture checkpoint -- one hook covering
// every inbound USP message kind (Notify variants, GetResp, SetResp,
// ...), called once per decoded message before it's dispatched to any
// type-specific handler. deviceID is empty for a not-yet-reconciled
// connection; naturalKey, when the caller has one (e.g. from an
// OnBoardRequest's own claimed identity), covers 'identity'-mode
// capture the same way cmd/acs's does.
func (h *handler) captureInbound(ctx context.Context, c mtp.Conn, naturalKey, deviceID, kind, summary string, redactedBody *string) {
	sessions, err := h.captures.ActiveMatch(ctx, "USP", naturalKey, c.RemoteAddr())
	if err != nil {
		h.log.Error("uspc: failed to check active captures", "err", err, "natural_key", naturalKey)
		return
	}
	for _, s := range sessions {
		if s.DeviceID == nil && deviceID != "" {
			if err := h.captures.ResolveDeviceID(ctx, s.ID, deviceID); err != nil {
				h.log.Error("uspc: failed to backfill capture session device_id", "err", err, "session_id", s.ID)
			}
		}
		if err := h.captures.RecordEvent(ctx, s.ID, "inbound", kind, summary, redactedBody); err != nil {
			h.log.Error("uspc: failed to record capture event", "err", err, "session_id", s.ID)
		}
	}
}
```

Then in `OnRecord`, right after `h.metrics.records.WithLabelValues(kind, "in", "ok").Inc()`
(the "ok" case is confirmed — `msg` is decoded), insert:

```go
	if deviceID, ok := h.reconciledDeviceID(in.Conn); ok {
		h.captureInbound(context.Background(), in.Conn, "", deviceID, "USP message", uspMessageSummary(msg), nil)
	} else {
		h.captureInbound(context.Background(), in.Conn, "", "", "USP message", uspMessageSummary(msg), nil)
	}
```

(`context.Background()` because `OnRecord`'s own signature — confirm by
reading it — takes no `context.Context` parameter; every other
`captures.Repository` call in this plan has a real request-scoped
context available, this is the one exception, matching how this
codebase's other fire-and-forget background writes are already handled
elsewhere in `cmd/uspc`.)

Add a small helper for the summary (read `usp.DecodeMsg`'s return type
first to write this against the real `*uspproto.Msg` accessors — a
`Msg` carries a `Header` with `MsgType`/`MsgId`):

```go
func uspMessageSummary(msg *uspproto.Msg) string {
	return fmt.Sprintf("%s (msg_id=%s)", msg.GetHeader().GetMsgType(), msg.GetHeader().GetMsgId())
}
```

Redacting the body of a specific inbound USP message type (Notify's
`ValueChange`/parameter-carrying variants) is deliberately left as `nil`
for this task's first cut — the summary line (message type + msg_id) is
the acceptance bar this task must clear; a follow-up increment can add
full redacted-body capture per message type once this ships and the
console UI (Task 8) is real to validate against. Note this scope
decision in your task report so the controller can log it as a ledger
ruling rather than silently shipping less than the spec implies.

- [ ] **Step 4: Add the outbound capture hook to `dispatcher.tryDispatch`**

In `backend/cmd/uspc/dispatcher.go`, add:

```go
func (d *dispatcher) captureOutbound(ctx context.Context, deviceID, job *jobs.Job) {
	// Read jobsRepo/devicesRepo's real accessor for a device's
	// oui_serial before writing this -- ActiveMatch needs the natural
	// key, not deviceID directly (mirrors cmd/acs's captureOutbound).
}
```

Then, right before `record, err := usp.EncodeRecord(...)` in
`tryDispatch`, insert a call to it, passing `deviceID` (the function's
own parameter) and `job`. Implement the body following
`cmd/acs/session.go`'s `captureOutbound` exactly (resolve the device's
natural key, call `h.captures.ActiveMatch(ctx, "USP", naturalKey, "")`,
record an event per matching session with `kind = string(job.Type)` and
a summary mentioning the job's command key) — the two should read as
near-identical implementations of the same pattern, one per protocol.

- [ ] **Step 5: Write the test**

Create `backend/cmd/uspc/capture_test.go` mirroring Task 3 Step 5's
structure: a device-mode capture session started for an already-known
device, driving a real (mock) USP message through `OnRecord`, asserting
exactly one `capture_events` row with the expected `kind`/`summary`.
Read `cmd/uspc`'s existing test harness (its own mock MTP/probe setup,
used by its current test suite) first and reuse it rather than building
a new one.

- [ ] **Step 6: Run to verify they pass**

Run: `cd backend && export ACS_TEST_POSTGRES_DSN=<your test DSN> && go test ./cmd/uspc/... -run TestCapture -v`
Expected: PASS.

- [ ] **Step 7: Run the full `cmd/uspc` suite, `gofmt`, `vet`**

Run: `cd backend && go test ./cmd/uspc/... -v 2>&1 | tail -200 && gofmt -l cmd/uspc/ && go vet ./cmd/uspc/...`
Expected: all PASS, clean.

- [ ] **Step 8: Commit**

```bash
git add backend/cmd/uspc/handler.go backend/cmd/uspc/dispatcher.go backend/cmd/uspc/main.go backend/cmd/uspc/capture_test.go
git commit -m "$(cat <<'EOF'
feat(uspc): wire session capture into OnRecord/dispatch (design S5)

captureInbound (OnRecord, one hook covering every inbound USP message
kind) and captureOutbound (dispatcher.tryDispatch) mirror cmd/acs's
Task 3 shape for the USP side. Inbound per-message-type body redaction
is intentionally deferred past this task's first cut (summary-only for
now) -- a real, disclosed scope decision, not a silent gap.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 5: Retention

**Files:**
- Modify: `backend/internal/retention/retention.go`

**Interfaces:**
- Consumes: nothing new.
- Produces: nothing consumed elsewhere in this plan (this task closes
  the loop on its own).

- [ ] **Step 1: Add the policy field**

In `backend/internal/retention/retention.go`, add to the `Policy` struct:

```go
	CaptureSessionsDays   int
```

Add to `DefaultPolicy`:

```go
	CaptureSessionsDays:   1,
```

Add to `PolicyFromEnv`:

```go
		CaptureSessionsDays:   envDays("ACS_RETENTION_CAPTURE_SESSIONS_DAYS", d.CaptureSessionsDays),
```

- [ ] **Step 2: Add the prune rule**

Add to the `rules` slice:

```go
	{"capture_sessions", func(p Policy) int { return p.CaptureSessionsDays },
		"status <> 'ACTIVE' AND COALESCE(stopped_at, expires_at) < $1", "id"},
```

`capture_events` needs no separate rule — it cascade-deletes via its
`session_id` foreign key (`ON DELETE CASCADE`, Task 1's migration) when
its parent `capture_sessions` row is pruned.

- [ ] **Step 3: Write the test**

Add to `backend/internal/retention/retention_test.go` (read the existing
file first to match its exact test-setup pattern for the other rules —
likely a shared DB fixture inserting an old row, calling `Run`, and
asserting the row is gone):

```go
func TestRun_PrunesOldStoppedCaptureSessions(t *testing.T) {
	// Mirror an existing rule's test exactly (e.g. whichever table's test
	// is simplest to copy) with these specifics:
	// - insert a capture_sessions row with status='STOPPED',
	//   stopped_at = now() - 2 days
	// - insert a capture_sessions row with status='ACTIVE',
	//   expires_at = now() - 2 days (must NOT be pruned -- ACTIVE rows
	//   are never pruned regardless of age; only their own eventual
	//   Stop/expiry-then-age makes them eligible)
	// - Run(ctx, db, Policy{CaptureSessionsDays: 1})
	// - assert the STOPPED row is gone, the ACTIVE row remains
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `cd backend && export ACS_TEST_POSTGRES_DSN=<your test DSN> && go test ./internal/retention/... -run TestRun_PrunesOldStoppedCaptureSessions -v`
Expected: PASS.

- [ ] **Step 5: Run the full `internal/retention` suite, `gofmt`, `vet`**

Run: `cd backend && go test ./internal/retention/... -v 2>&1 | tail -60 && gofmt -l internal/retention/ && go vet ./internal/retention/...`
Expected: all PASS, clean.

- [ ] **Step 6: Commit**

```bash
git add backend/internal/retention/retention.go backend/internal/retention/retention_test.go
git commit -m "$(cat <<'EOF'
feat(retention): prune old capture sessions (design S7)

One more rule on the existing hourly sweep -- ACS_RETENTION_CAPTURE_SESSIONS_DAYS
(default 1), matching this package's existing days-only convention
rather than the spec's own hours-based sketch (24h backfills to exactly
1 day, so this fits the established mechanism with zero new shape).
Only non-ACTIVE sessions age out; capture_events cascade-delete via
their FK, no separate rule needed.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 6: `cmd/api` REST endpoints

**Files:**
- Create: `backend/cmd/api/capture_handlers.go`
- Create: `backend/cmd/api/capture_handlers_test.go`
- Modify: `backend/cmd/api/routes.go`
- Modify: `backend/cmd/api/main.go` (wire `captures.Repository`)
- Modify: `backend/openapi.yaml`

**Interfaces:**
- Consumes: `captures.Repository` (Task 1), `captures.Session.EffectiveStatus`
  (Task 1).
- Produces: HTTP routes `POST /api/v1/devices/{id}/captures`,
  `POST /api/v1/captures`, `POST /api/v1/captures/{id}/stop`,
  `GET /api/v1/captures`, `GET /api/v1/captures/{id}/events`,
  `GET /api/v1/captures/{id}/export` — consumed by exact path in Task 7
  (frontend API client).

- [ ] **Step 1: Read `routes.go`, `auth_handlers.go`, and `diagnostics_handlers.go` in full first**

Read all three completely — confirm `route`/`routePerm`,
`requireRole`/`requirePermission`, `getScopedDevice`, `writeJSON`, and
`createDiagnosticsPing` still match what this plan assumes (verbatim
code was captured for this plan; re-verify before writing new handlers
against it).

- [ ] **Step 2: Wire the repository in `main.go`**

Add `captureRepo := captures.NewRepository(db)` alongside this process's
other repository construction, pass it into the `handler` struct (add a
`captures *captures.Repository` field), and read
`ACS_CAPTURE_MAX_DURATION` the same way Task 3 Step 2 does in `cmd/acs`
(same env var name, same 30-minute default — this is the process that
actually starts a session, so this is where the value is load-bearing).
Add `"acs/internal/captures"` to `main.go`'s imports.

- [ ] **Step 3: Write the failing tests**

Create `backend/cmd/api/capture_handlers_test.go`. Read an existing
`cmd/api` handler test file first (e.g. whatever tests
`createDiagnosticsPing` or `getParameters`) to copy its exact test-harness
setup (`httptest.NewRecorder`, how `h.captures`/`h.devices` are
constructed against a real test DB, how `r.PathValue`/`r.SetPathValue` is
used to simulate routing). Write tests for:

- `POST /api/v1/devices/{id}/captures` on a real, existing device
  returns `202` with the session id, `match_type: "device"`.
- `POST /api/v1/captures` with `{"match_type":"identity","match_value":"001349+S1","protocol":"CWMP"}`
  returns `202`.
- `POST /api/v1/captures` with an invalid `match_type` returns `400`.
- `POST /api/v1/captures/{id}/stop` on an active session returns `200`
  and the session reads `STOPPED` afterward.
- `POST /api/v1/captures/{id}/stop` on an unknown id returns `404`.
- `GET /api/v1/captures` returns every session, newest first, each with
  an `effective_status` field (via `Session.EffectiveStatus(time.Now())`)
  distinct from its raw `status`.
- `GET /api/v1/captures/{id}/events` returns a session's events in order.
- `POST /api/v1/captures` twice with the same `match_type`/`match_value`
  returns `409` on the second call (Task 1's `ErrAlreadyActive`).

- [ ] **Step 4: Run to verify they fail**

Run: `cd backend && go test ./cmd/api/... -run TestCapture -v`
Expected: FAIL to compile — the handlers don't exist yet.

- [ ] **Step 5: Write `capture_handlers.go`**

Create `backend/cmd/api/capture_handlers.go`:

```go
package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"acs/internal/captures"
)

type startCaptureRequest struct {
	MatchType  string `json:"match_type"`
	MatchValue string `json:"match_value"`
	Protocol   string `json:"protocol"`
}

type captureSessionResponse struct {
	ID              string  `json:"id"`
	DeviceID        *string `json:"device_id,omitempty"`
	MatchType       string  `json:"match_type"`
	MatchValue      string  `json:"match_value"`
	Protocol        string  `json:"protocol"`
	Status          string  `json:"status"`
	EffectiveStatus string  `json:"effective_status"`
	StartedBy       string  `json:"started_by"`
	StartedAt       string  `json:"started_at"`
	StoppedAt       *string `json:"stopped_at,omitempty"`
	ExpiresAt       string  `json:"expires_at"`
}

func toCaptureSessionResponse(s captures.Session) captureSessionResponse {
	resp := captureSessionResponse{
		ID: s.ID, DeviceID: s.DeviceID, MatchType: s.MatchType, MatchValue: s.MatchValue,
		Protocol: s.Protocol, Status: s.Status, EffectiveStatus: s.EffectiveStatus(time.Now()),
		StartedBy: s.StartedBy, StartedAt: s.StartedAt.Format(time.RFC3339), ExpiresAt: s.ExpiresAt.Format(time.RFC3339),
	}
	if s.StoppedAt != nil {
		t := s.StoppedAt.Format(time.RFC3339)
		resp.StoppedAt = &t
	}
	return resp
}

func validMatchType(t string) bool {
	return t == captures.MatchDevice || t == captures.MatchIdentity || t == captures.MatchRemoteIP
}

func validProtocol(p string) bool {
	return p == "CWMP" || p == "USP"
}

// createDeviceCapture implements POST /api/v1/devices/{id}/captures --
// the "by device" trigger mode (design §4), where match_value is
// resolved from the device's own oui_serial rather than supplied by the
// caller.
func (h *handler) createDeviceCapture(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	device, ok := h.getScopedDevice(w, r, id)
	if !ok {
		return
	}

	var req struct {
		Protocol string `json:"protocol"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if !validProtocol(req.Protocol) {
		http.Error(w, `protocol must be "CWMP" or "USP"`, http.StatusBadRequest)
		return
	}

	deviceID := device.ID
	s, err := h.captures.Start(r.Context(), captures.StartParams{
		DeviceID: &deviceID, MatchType: captures.MatchDevice, MatchValue: device.OUISerial,
		Protocol: req.Protocol, StartedBy: operatorFromRequest(r), MaxDuration: h.captureMaxDuration,
	})
	if errors.Is(err, captures.ErrAlreadyActive) {
		http.Error(w, "an active capture already exists for this device", http.StatusConflict)
		return
	}
	if err != nil {
		h.logger.Error("failed to start device capture", "err", err, "device_id", id)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusAccepted, toCaptureSessionResponse(*s))
}

// createCapture implements POST /api/v1/captures -- the "by expected
// identity" and "by remote IP" trigger modes (design §4), where the
// operator supplies match_value directly since no devices row is
// correlated yet.
func (h *handler) createCapture(w http.ResponseWriter, r *http.Request) {
	var req startCaptureRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if req.MatchType != captures.MatchIdentity && req.MatchType != captures.MatchRemoteIP {
		http.Error(w, `match_type must be "identity" or "remote_ip" (use POST /api/v1/devices/{id}/captures for "device")`, http.StatusBadRequest)
		return
	}
	if req.MatchValue == "" {
		http.Error(w, "match_value is required", http.StatusBadRequest)
		return
	}
	if !validProtocol(req.Protocol) {
		http.Error(w, `protocol must be "CWMP" or "USP"`, http.StatusBadRequest)
		return
	}

	s, err := h.captures.Start(r.Context(), captures.StartParams{
		MatchType: req.MatchType, MatchValue: req.MatchValue, Protocol: req.Protocol,
		StartedBy: operatorFromRequest(r), MaxDuration: h.captureMaxDuration,
	})
	if errors.Is(err, captures.ErrAlreadyActive) {
		http.Error(w, "an active capture already exists for this target", http.StatusConflict)
		return
	}
	if err != nil {
		h.logger.Error("failed to start capture", "err", err, "match_type", req.MatchType, "match_value", req.MatchValue)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusAccepted, toCaptureSessionResponse(*s))
}

// stopCapture implements POST /api/v1/captures/{id}/stop.
func (h *handler) stopCapture(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := h.captures.Get(r.Context(), id); errors.Is(err, captures.ErrNotFound) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	} else if err != nil {
		h.logger.Error("failed to look up capture session", "err", err, "id", id)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if err := h.captures.Stop(r.Context(), id); err != nil {
		h.logger.Error("failed to stop capture session", "err", err, "id", id)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s, err := h.captures.Get(r.Context(), id)
	if err != nil {
		h.logger.Error("failed to re-read capture session after stop", "err", err, "id", id)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, toCaptureSessionResponse(*s))
}

// listCaptures implements GET /api/v1/captures.
func (h *handler) listCaptures(w http.ResponseWriter, r *http.Request) {
	sessions, err := h.captures.List(r.Context())
	if err != nil {
		h.logger.Error("failed to list capture sessions", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	out := make([]captureSessionResponse, 0, len(sessions))
	for _, s := range sessions {
		out = append(out, toCaptureSessionResponse(s))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

type captureEventResponse struct {
	ID         string  `json:"id"`
	Seq        int     `json:"seq"`
	Direction  string  `json:"direction"`
	Kind       string  `json:"kind"`
	OccurredAt string  `json:"occurred_at"`
	Summary    string  `json:"summary"`
	Body       *string `json:"body,omitempty"`
}

// getCaptureEvents implements GET /api/v1/captures/{id}/events.
func (h *handler) getCaptureEvents(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := h.captures.Get(r.Context(), id); errors.Is(err, captures.ErrNotFound) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	} else if err != nil {
		h.logger.Error("failed to look up capture session", "err", err, "id", id)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	events, err := h.captures.ListEvents(r.Context(), id)
	if err != nil {
		h.logger.Error("failed to list capture events", "err", err, "id", id)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	out := make([]captureEventResponse, 0, len(events))
	for _, e := range events {
		out = append(out, captureEventResponse{
			ID: e.ID, Seq: e.Seq, Direction: e.Direction, Kind: e.Kind,
			OccurredAt: e.OccurredAt.Format(time.RFC3339), Summary: e.Summary, Body: e.Body,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

// exportCapture implements GET /api/v1/captures/{id}/export -- the full
// redacted transcript as a downloadable JSON file (design §7).
func (h *handler) exportCapture(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s, err := h.captures.Get(r.Context(), id)
	if errors.Is(err, captures.ErrNotFound) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if err != nil {
		h.logger.Error("failed to look up capture session", "err", err, "id", id)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	events, err := h.captures.ListEvents(r.Context(), id)
	if err != nil {
		h.logger.Error("failed to list capture events for export", "err", err, "id", id)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	eventsOut := make([]captureEventResponse, 0, len(events))
	for _, e := range events {
		eventsOut = append(eventsOut, captureEventResponse{
			ID: e.ID, Seq: e.Seq, Direction: e.Direction, Kind: e.Kind,
			OccurredAt: e.OccurredAt.Format(time.RFC3339), Summary: e.Summary, Body: e.Body,
		})
	}
	w.Header().Set("Content-Disposition", `attachment; filename="capture-`+id+`.json"`)
	writeJSON(w, http.StatusOK, map[string]any{
		"session": toCaptureSessionResponse(*s),
		"events":  eventsOut,
	})
}
```

- [ ] **Step 6: Register the routes**

In `backend/cmd/api/routes.go`, add (near the other device-scoped and
top-level routes, matching the file's existing grouping style):

```go
	routePerm("POST", "/api/v1/devices/{id}/captures", operators.PermDiagnosticsRun, h.createDeviceCapture)
	routePerm("POST", "/api/v1/captures", operators.PermDiagnosticsRun, h.createCapture)
	routePerm("POST", "/api/v1/captures/{id}/stop", operators.PermDiagnosticsRun, h.stopCapture)
	route("GET", "/api/v1/captures", ro, h.listCaptures)
	route("GET", "/api/v1/captures/{id}/events", ro, h.getCaptureEvents)
	route("GET", "/api/v1/captures/{id}/export", ro, h.exportCapture)
```

- [ ] **Step 7: Add `backend/openapi.yaml` entries**

Read `backend/openapi.yaml`'s existing entry for
`/api/v1/devices/{id}/diagnostics/ping` (quoted in full in this plan's
research) as the template for request/response schema shape and add six
matching path entries for the routes above — `openapi:lint` (part of this
repo's frontend CI step, run `cd frontend && npm run generate:api` after)
must produce zero diff against what you commit, so run that command
yourself before finishing this task and include any resulting
`frontend/src/api/generated.ts` diff in this same commit.

- [ ] **Step 8: Run to verify they pass**

Run: `cd backend && export ACS_TEST_POSTGRES_DSN=<your test DSN> && go test ./cmd/api/... -run TestCapture -v`
Expected: PASS, all 8 tests from Step 3.

- [ ] **Step 9: Run the full `cmd/api` suite, `gofmt`, `vet`**

Run: `cd backend && go test ./cmd/api/... -v 2>&1 | tail -200 && gofmt -l cmd/api/ && go vet ./cmd/api/...`
Expected: all PASS, clean.

- [ ] **Step 10: Commit**

```bash
git add backend/cmd/api/capture_handlers.go backend/cmd/api/capture_handlers_test.go backend/cmd/api/routes.go backend/cmd/api/main.go backend/openapi.yaml frontend/src/api/generated.ts
git commit -m "$(cat <<'EOF'
feat(api): REST endpoints for session capture (design S4/S7)

POST /api/v1/devices/{id}/captures (device mode) and POST
/api/v1/captures (identity/remote_ip modes), POST .../stop, GET
/api/v1/captures (list, with effective_status computed at read time --
no background status-flip job needed), GET .../events, GET .../export
(downloadable JSON transcript). Gated on PermDiagnosticsRun for
starting/stopping (matches ping/traceroute's existing troubleshooting-
action permission), plain readonly for reads. openapi.yaml updated and
frontend types regenerated to match.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 7: Frontend API client

**Files:**
- Modify: `frontend/src/api/client.ts`
- Modify: `frontend/src/api/types.ts` (or wherever hand-written response
  types live — confirm the exact file by reading `client.ts`'s imports
  first)

**Interfaces:**
- Consumes: the six routes from Task 6.
- Produces: `api.startDeviceCapture(deviceId, protocol)`,
  `api.startCapture(matchType, matchValue, protocol)`,
  `api.stopCapture(id)`, `api.listCaptures()`, `api.getCaptureEvents(id)`,
  `api.downloadCaptureExport(id)` — consumed by exact name in Tasks 8-9.

- [ ] **Step 1: Read `client.ts` in full first**

Read `frontend/src/api/client.ts` completely — confirm the `request<T>`/
`download` helpers and the existing GET/POST examples this plan quotes
still match (verbatim code was captured for this plan; re-verify).

- [ ] **Step 2: Write the failing test**

Read whatever test file already covers `client.ts` (if one exists — check
`frontend/src/api/*.test.ts`) to match its harness (likely `msw` or a
fetch mock). Add:

```ts
import { describe, it, expect, vi } from "vitest";
import { api } from "./client";

describe("capture API client", () => {
  it("startDeviceCapture POSTs to the device-scoped route", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response(JSON.stringify({ id: "cap-1", match_type: "device", status: "ACTIVE" }), { status: 202 })
    );
    const res = await api.startDeviceCapture("dev-1", "CWMP");
    expect(res.id).toBe("cap-1");
    expect(fetchMock).toHaveBeenCalledWith(
      expect.stringContaining("/api/v1/devices/dev-1/captures"),
      expect.objectContaining({ method: "POST" })
    );
  });

  it("startCapture POSTs match_type/match_value/protocol", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response(JSON.stringify({ id: "cap-2" }), { status: 202 })
    );
    const res = await api.startCapture("identity", "001349+S1", "CWMP");
    expect(res.id).toBe("cap-2");
  });

  it("listCaptures GETs the list route", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response(JSON.stringify({ items: [] }), { status: 200 })
    );
    const res = await api.listCaptures();
    expect(res.items).toEqual([]);
  });
});
```

(Adjust the mock/import style to match whatever this codebase's real
existing `client.ts`-adjacent test already does — read one first rather
than guessing `vitest`/`msw` conventions blind.)

- [ ] **Step 3: Run to verify it fails**

Run: `cd frontend && npm test -- client` (or this repo's real test
command — confirm from `package.json`)
Expected: FAIL — `api.startDeviceCapture` etc. don't exist yet.

- [ ] **Step 4: Add the types and client methods**

In `frontend/src/api/types.ts` (or the real hand-written types file),
add:

```ts
export type CaptureMatchType = "device" | "identity" | "remote_ip";
export type CaptureStatus = "ACTIVE" | "STOPPED" | "EXPIRED";
export type CaptureProtocol = "CWMP" | "USP";

export interface CaptureSession {
  id: string;
  device_id?: string;
  match_type: CaptureMatchType;
  match_value: string;
  protocol: CaptureProtocol;
  status: CaptureStatus;
  effective_status: CaptureStatus;
  started_by: string;
  started_at: string;
  stopped_at?: string;
  expires_at: string;
}

export interface CaptureEvent {
  id: string;
  seq: number;
  direction: "inbound" | "outbound";
  kind: string;
  occurred_at: string;
  summary: string;
  body?: string;
}
```

In `frontend/src/api/client.ts`, add to the `api` object:

```ts
startDeviceCapture: (deviceId: string, protocol: CaptureProtocol) =>
  request<CaptureSession>(`/api/v1/devices/${deviceId}/captures`, {
    method: "POST",
    body: JSON.stringify({ protocol }),
  }),

startCapture: (matchType: "identity" | "remote_ip", matchValue: string, protocol: CaptureProtocol) =>
  request<CaptureSession>(`/api/v1/captures`, {
    method: "POST",
    body: JSON.stringify({ match_type: matchType, match_value: matchValue, protocol }),
  }),

stopCapture: (id: string) =>
  request<CaptureSession>(`/api/v1/captures/${id}/stop`, { method: "POST" }),

listCaptures: () => request<{ items: CaptureSession[] }>(`/api/v1/captures`),

getCaptureEvents: (id: string) => request<{ items: CaptureEvent[] }>(`/api/v1/captures/${id}/events`),

downloadCaptureExport: (id: string) => download(`/api/v1/captures/${id}/export`, `capture-${id}.json`),
```

Add `import type { CaptureSession, CaptureEvent, CaptureProtocol } from "./types"`
(matching however `client.ts` already imports its other hand-written
types) if not already present.

- [ ] **Step 5: Run to verify it passes**

Run: `cd frontend && npm test -- client`
Expected: PASS, all 3 tests.

- [ ] **Step 6: Typecheck and lint**

Run: `cd frontend && npm run typecheck && npm run lint`
Expected: clean.

- [ ] **Step 7: Commit**

```bash
git add frontend/src/api/client.ts frontend/src/api/types.ts frontend/src/api/client.test.ts
git commit -m "$(cat <<'EOF'
feat(frontend): API client methods for session capture

startDeviceCapture/startCapture/stopCapture/listCaptures/
getCaptureEvents/downloadCaptureExport, following this file's existing
hand-written request<T>/download conventions.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 8: `CaptureSessions.tsx` screen and shared capture-detail view

**Files:**
- Create: `frontend/src/screens/CaptureSessions.tsx`
- Create: `frontend/src/components/CaptureDetail.tsx`
- Modify: `frontend/src/App.tsx`

**Interfaces:**
- Consumes: `api.listCaptures`/`startCapture`/`stopCapture`/
  `getCaptureEvents`/`downloadCaptureExport` (Task 7), `useLive`
  (`frontend/src/lib/useLive.ts`, existing), `DataTable`
  (`frontend/src/components/DataTable.tsx`, existing).
- Produces: `<CaptureDetail sessionId={string} onStopped?={() => void} />`
  — consumed by exact name in Task 9.

- [ ] **Step 1: Read `Jobs.tsx`, `useLive.ts`, `DataTable.tsx`, and `App.tsx` in full first**

Read all four completely — confirm the `load`/`useLive`/`ColumnDef`/
`NAV`/`SCREEN_COMPONENT` shapes this plan assumes (verbatim code was
captured for this plan's research; re-verify before writing new
screens against them, especially `DataTable`'s exact prop names).

- [ ] **Step 2: Write `CaptureDetail.tsx`**

Create `frontend/src/components/CaptureDetail.tsx`:

```tsx
import { useCallback, useEffect, useState } from "react";
import { api } from "../api/client";
import type { CaptureEvent, CaptureSession } from "../api/types";
import { ApiError } from "../api/client";
import { useLive } from "../lib/useLive";
import { useAuth } from "../auth/useAuth";
import { canWrite } from "../auth/roles";

// CaptureDetail is the shared live-updating event view (design §8),
// used by both CaptureSessions.tsx (fleet-wide list) and
// DeviceDetail.tsx's "Start capture" shortcut.
export function CaptureDetail({ sessionId, onStopped }: { sessionId: string; onStopped?: () => void }) {
  const { role } = useAuth();
  const writable = canWrite(role);
  const [session, setSession] = useState<CaptureSession | null>(null);
  const [events, setEvents] = useState<CaptureEvent[]>([]);
  const [expanded, setExpanded] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const load = useCallback(async (background = false) => {
    try {
      const [sessions, eventsRes] = await Promise.all([api.listCaptures(), api.getCaptureEvents(sessionId)]);
      const found = sessions.items.find((s) => s.id === sessionId) ?? null;
      setSession(found);
      setEvents(eventsRes.items);
      setError(null);
    } catch (e) {
      if (!background) setError(e instanceof ApiError ? `${e.status}: ${e.message}` : "Failed to load capture session");
    }
  }, [sessionId]);

  useEffect(() => {
    load();
  }, [load]);

  const [live, setLive] = useLive(() => load(true), 1500, true);

  useEffect(() => {
    if (session && session.effective_status !== "ACTIVE" && live) {
      setLive(false);
    }
  }, [session, live, setLive]);

  const handleStop = async () => {
    setBusy(true);
    try {
      await api.stopCapture(sessionId);
      await load();
      onStopped?.();
    } catch (e) {
      setError(e instanceof ApiError ? `${e.status}: ${e.message}` : "Failed to stop capture");
    } finally {
      setBusy(false);
    }
  };

  const handleDownload = () => api.downloadCaptureExport(sessionId);

  if (error) return <div className="banner error">{error}</div>;
  if (!session) return <p>Loading…</p>;

  return (
    <div className="panel">
      <div className="toolbar">
        <h3>
          Capture {session.id} — <span className="chip">{session.effective_status}</span>
        </h3>
        <div>
          {session.effective_status === "ACTIVE" && (
            <button className="btn danger" onClick={handleStop} disabled={busy || !writable}>
              Stop
            </button>
          )}
          <button className="btn" onClick={handleDownload}>
            Download transcript
          </button>
          {session.effective_status === "ACTIVE" && (
            <button className={`btn live-toggle ${live ? "on" : ""}`} onClick={() => setLive((l) => !l)}>
              {live ? "Live" : "Paused"}
            </button>
          )}
        </div>
      </div>
      <dl className="kv">
        <dt>Match</dt>
        <dd>
          {session.match_type} = {session.match_value}
        </dd>
        <dt>Protocol</dt>
        <dd>{session.protocol}</dd>
        <dt>Started</dt>
        <dd>
          {session.started_at} by {session.started_by}
        </dd>
      </dl>
      <table className="event-list">
        <thead>
          <tr>
            <th>#</th>
            <th>Time</th>
            <th>Direction</th>
            <th>Kind</th>
            <th>Summary</th>
          </tr>
        </thead>
        <tbody>
          {events.map((e) => (
            <>
              <tr key={e.id} onClick={() => setExpanded(expanded === e.id ? null : e.id)} style={{ cursor: e.body ? "pointer" : "default" }}>
                <td>{e.seq}</td>
                <td>{e.occurred_at}</td>
                <td>{e.direction}</td>
                <td>{e.kind}</td>
                <td>{e.summary}</td>
              </tr>
              {expanded === e.id && e.body && (
                <tr key={`${e.id}-body`}>
                  <td colSpan={5}>
                    <pre>{e.body}</pre>
                  </td>
                </tr>
              )}
            </>
          ))}
        </tbody>
      </table>
      {events.length === 0 && <p style={{ color: "var(--ink-faint)" }}>No events recorded yet.</p>}
    </div>
  );
}
```

(If `useAuth`/`canWrite`/`ApiError`'s real import paths differ from what
this step guesses, fix them to match — Step 1 told you to read the real
files first; this is exactly where that matters.)

- [ ] **Step 3: Write `CaptureSessions.tsx`**

Create `frontend/src/screens/CaptureSessions.tsx`:

```tsx
import { useCallback, useEffect, useState } from "react";
import type { ColumnDef } from "@tanstack/react-table";
import { api, ApiError } from "../api/client";
import type { CaptureSession } from "../api/types";
import { DataTable } from "../components/DataTable";
import { CaptureDetail } from "../components/CaptureDetail";
import { useLive } from "../lib/useLive";
import { useAuth } from "../auth/useAuth";
import { canWrite } from "../auth/roles";

const columns: ColumnDef<CaptureSession, any>[] = [
  { accessorKey: "id", header: "ID" },
  { accessorKey: "match_type", header: "Mode" },
  { accessorKey: "match_value", header: "Target" },
  { accessorKey: "protocol", header: "Protocol" },
  { accessorKey: "effective_status", header: "Status" },
  { accessorKey: "started_by", header: "Started by" },
  { accessorKey: "started_at", header: "Started" },
];

export default function CaptureSessions() {
  const { role } = useAuth();
  const writable = canWrite(role);
  const [sessions, setSessions] = useState<CaptureSession[]>([]);
  const [selected, setSelected] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [matchType, setMatchType] = useState<"identity" | "remote_ip">("identity");
  const [matchValue, setMatchValue] = useState("");
  const [protocol, setProtocol] = useState<"CWMP" | "USP">("CWMP");
  const [busy, setBusy] = useState(false);

  const load = useCallback(async (background = false) => {
    try {
      const res = await api.listCaptures();
      setSessions(res.items);
      setError(null);
    } catch (e) {
      if (!background) setError(e instanceof ApiError ? `${e.status}: ${e.message}` : "Failed to reach the API — is cmd/api running?");
    }
  }, []);

  useEffect(() => {
    load();
  }, [load]);
  const [live, setLive] = useLive(() => load(true), 5000);

  const handleStart = async () => {
    if (!matchValue.trim()) return;
    setBusy(true);
    try {
      const s = await api.startCapture(matchType, matchValue.trim(), protocol);
      setMatchValue("");
      await load();
      setSelected(s.id);
    } catch (e) {
      setError(e instanceof ApiError ? `${e.status}: ${e.message}` : "Failed to start capture");
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="split two-col">
      <div>
        <div className="toolbar">
          <h2>Session captures</h2>
          <button className={`btn live-toggle ${live ? "on" : ""}`} onClick={() => setLive((l) => !l)}>
            {live ? "Live" : "Paused"}
          </button>
        </div>
        {error && <div className="banner error">{error}</div>}
        <div className="panel">
          <h3>Start a capture</h3>
          <p style={{ color: "var(--ink-faint)", fontSize: "0.76rem" }}>
            For an already-onboarded device, use the "Start capture" action on its own device page instead — this form is for a
            device that hasn't authenticated yet (by expected identity) or whose network location you know but not its identity
            (by remote IP).
          </p>
          <div className="form-row">
            <select value={matchType} onChange={(e) => setMatchType(e.target.value as "identity" | "remote_ip")} disabled={busy || !writable}>
              <option value="identity">By expected identity (OUI+Serial)</option>
              <option value="remote_ip">By remote IP</option>
            </select>
            <input
              aria-label="Match value"
              placeholder={matchType === "identity" ? "e.g. 001349+S230Q12345678" : "e.g. 192.168.1.50"}
              value={matchValue}
              onChange={(e) => setMatchValue(e.target.value)}
              disabled={busy || !writable}
            />
            <select value={protocol} onChange={(e) => setProtocol(e.target.value as "CWMP" | "USP")} disabled={busy || !writable}>
              <option value="CWMP">CWMP</option>
              <option value="USP">USP</option>
            </select>
            <button className="btn primary" onClick={handleStart} disabled={busy || !writable || !matchValue.trim()}>
              Start
            </button>
          </div>
        </div>
        <DataTable
          data={sessions}
          columns={columns}
          getRowId={(s) => s.id}
          emptyMessage="No capture sessions yet."
          onRowClick={(s) => setSelected(s.id)}
        />
      </div>
      <div>{selected && <CaptureDetail sessionId={selected} onStopped={() => load()} />}</div>
    </div>
  );
}
```

(`DataTable`'s real prop names — especially whether it supports
`onRowClick` at all — must be confirmed against the file Step 1 told you
to read; adjust if it differs.)

- [ ] **Step 4: Wire the new screen into `App.tsx`**

Following the pattern this plan's research already confirmed exactly
(three edits):

1. Add `"captures"` to the `Screen` union type.
2. Add `{ group: "Records", id: "captures", label: "Captures" }` to the
   `NAV` array.
3. Add `captures: screen(() => import("./screens/CaptureSessions"), "CaptureSessions"),`
   to `SCREEN_COMPONENT`.

- [ ] **Step 5: Manual verification**

Run: `cd frontend && npm run dev` (or this repo's real dev-server command
— check `package.json` / the `run` skill if available), navigate to the
new "Captures" nav item, confirm the screen renders with no console
errors, start a capture against a test device's identity, confirm it
appears in the list and the detail view polls. This is a real
verification step, not optional — per this project's own standing
instructions, a frontend change is not done until exercised in a real
browser.

- [ ] **Step 6: Typecheck and lint**

Run: `cd frontend && npm run typecheck && npm run lint`
Expected: clean.

- [ ] **Step 7: Commit**

```bash
git add frontend/src/screens/CaptureSessions.tsx frontend/src/components/CaptureDetail.tsx frontend/src/App.tsx
git commit -m "$(cat <<'EOF'
feat(frontend): CaptureSessions screen and shared capture-detail view (design S8)

New top-level "Captures" nav screen for the identity/remote_ip trigger
modes (no device page to launch from) plus the fleet-wide session list.
CaptureDetail is the shared live-polling event view (1.5s while ACTIVE,
auto-pauses once a session ends) both this screen and DeviceDetail's
upcoming "Start capture" shortcut use.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 9: `DeviceDetail.tsx` — "Start capture" action

**Files:**
- Modify: `frontend/src/screens/DeviceDetail.tsx`

**Interfaces:**
- Consumes: `api.startDeviceCapture` (Task 7), `<CaptureDetail>` (Task 8).
- Produces: nothing consumed elsewhere in this plan (final task).

- [ ] **Step 1: Read `DeviceDetail.tsx` in full first**

Read the file completely — confirm the `withBusy`/`handleReboot`-style
pattern and the Diagnostics panel's exact JSX this plan quotes still
match (verbatim code was captured for this plan's research; re-verify,
especially the `useNavigate` import — confirm whether it's already
imported in this file or needs adding).

- [ ] **Step 2: Add the "Start capture" panel**

Immediately after the existing `<div className="panel"><h3>Diagnostics</h3>...</div>`
block, add a new sibling panel:

```tsx
<div className="panel">
  <h3>Capture</h3>
  <div className="form-row">
    <select value={captureProtocol} onChange={(e) => setCaptureProtocol(e.target.value as "CWMP" | "USP")} disabled={busy || !writable}>
      <option value="CWMP">CWMP</option>
      <option value="USP">USP</option>
    </select>
    <button className="btn" onClick={handleStartCapture} disabled={busy || !writable}>
      Start capture
    </button>
  </div>
  <p style={{ color: "var(--ink-faint)", fontSize: "0.76rem", marginTop: "0.6rem", marginBottom: 0 }}>
    Records this device's real session traffic for troubleshooting — redacted, capped at 30 minutes, viewable from Captures.
  </p>
</div>
```

Add the corresponding state and handler near the other `handleX`
functions (matching `handleReboot`'s exact `withBusy` shape):

```ts
const [captureProtocol, setCaptureProtocol] = useState<"CWMP" | "USP">("CWMP");
const navigate = useNavigate();

const handleStartCapture = () =>
  withBusy(async () => {
    const s = await api.startDeviceCapture(id, captureProtocol);
    navigate(`/captures?open=${s.id}`);
    return `Capture started: ${s.id}`;
  });
```

Add `import { useNavigate } from "react-router-dom";` if not already
present, and `import type { CaptureSession } from "../api/types";` if
needed for typing (likely not, since the handler only reads `s.id`).

`navigate('/captures?open=${s.id}')` assumes `CaptureSessions.tsx`
(Task 8) reads an `?open=` query param on mount to auto-select that
session in its detail pane — add that small addition to
`CaptureSessions.tsx` now if Task 8 didn't already include it (check
before assuming): read `useSearchParams()` from `react-router-dom` and
call `setSelected` from it in a `useEffect` on mount.

- [ ] **Step 3: Manual verification**

Run the frontend dev server, open a known device's detail page, click
"Start capture", confirm navigation to the Captures screen with that
session pre-selected and its detail view live-polling.

- [ ] **Step 4: Typecheck and lint**

Run: `cd frontend && npm run typecheck && npm run lint`
Expected: clean.

- [ ] **Step 5: Commit**

```bash
git add frontend/src/screens/DeviceDetail.tsx frontend/src/screens/CaptureSessions.tsx
git commit -m "$(cat <<'EOF'
feat(frontend): "Start capture" action on DeviceDetail (design S8)

The device-keyed shortcut: one click starts a match_type='device'
session (device_id/match_value already known, no form to fill) and
navigates straight to its live view.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

## Self-Review

**1. Spec coverage.**

| Design spec section | Task |
|---|---|
| §2 application-level, not network-level | 3, 4 (the hook points themselves — no TLS/pcap code anywhere) |
| §4 three trigger modes | 1 (schema/`ActiveMatch`), 6 (the two `POST` endpoints split by mode) |
| §5 schema, no new inter-process channel | 1, 3, 4 |
| §6 redaction | 2, 3 (CWMP body/header redaction), 4 (USP — summary-only for messages, disclosed scope decision) |
| §7 lifecycle/retention/export | 1 (`Start`/`Stop`/`EffectiveStatus`), 5 (retention rule), 6 (`stopCapture`/`exportCapture`) |
| §8 frontend UX | 7, 8, 9 |
| §9 testing and acceptance | every task's own test steps; Task 3's integration test is the concrete "identity-mode capture on a not-yet-onboarded device catches its real Inform" acceptance bar §9 names directly |
| §10 out of scope | confirmed absent from every task (no pcap synthesis, no always-on buffer, no CIDR matching, no new streaming channel) |

**2. Placeholder scan.** Two intentional, disclosed exceptions to "no
placeholders," both called out explicitly in their own task text as
scope decisions rather than left silent: Task 3's `captureDispatchBody`
(the exact XML redaction parsing is left for the implementer to write
against the real `RenderSetParameterValues` output, with an explicit
test requirement — not a vague "handle it," a concrete pass/fail bar)
and Task 4's per-message-type USP body redaction (explicitly deferred
past this plan's first cut, summary-only, flagged for the controller to
ledger as a ruling rather than silently shipped short). Every other step
shows complete, real code.

**3. Type consistency.** `captures.Repository{Start, Stop, Get, List,
ActiveMatch, RecordEvent, ListEvents, ResolveDeviceID}`,
`captures.Session`/`Event`/`StartParams`, `captures.MatchDevice/Identity/
RemoteIP`, `captures.RedactParamValue`/`RedactAuthHeader` (Tasks 1-2) are
consumed by exact name/signature in Tasks 3, 4, 6. The six REST routes
(Task 6) are consumed by exact path in Task 7's client methods, which are
consumed by exact name in Tasks 8-9. `<CaptureDetail sessionId
onStopped?>` (Task 8) is consumed by exact prop shape in Task 9's
navigation target.

**4. Ruling — permission gate.** No dedicated `PermCaptureManage`
constant exists in `internal/operators`; this plan reuses the existing
`PermDiagnosticsRun` ("diagnostics.run") for start/stop, matching its
already-established semantic scope (ping/traceroute/refresh-cellular/
parameter-discovery — troubleshooting-triggered actions). Cost if wrong:
an operator with diagnostics permission but who a deployer specifically
wanted excluded from session capture would have access anyway; a real
but narrow gap, fixable later by introducing a dedicated permission
constant without any schema change.

**5. Ruling — retention granularity.** The spec sketched
`ACS_CAPTURE_RETENTION_HOURS`; this plan uses
`ACS_RETENTION_CAPTURE_SESSIONS_DAYS` instead, matching
`internal/retention`'s actual, established days-only mechanism (confirmed
by reading the real file before writing Task 5) rather than introducing
a new granularity shape into a package that has never had one. The
spec's 24h default backfills exactly to 1 day, so no default behavior
changes — only the env var name and unit.

**6. Ruling — no background status-flip worker.** The spec described
retention "flipping ACTIVE→EXPIRED." This plan computes
`effective_status` at read time instead (`Session.EffectiveStatus`,
Task 1; surfaced by `GET /api/v1/captures`, Task 6) — functionally
identical from the console's perspective (an expired-but-still-`ACTIVE`
row always displays as `EXPIRED`), simpler (no new retention-rule shape
needed for an UPDATE, since `internal/retention`'s mechanism is
DELETE-only), and strictly more correct (no lag between actual expiry
and displayed status). Retention (Task 5) only ever deletes, never
updates.
