# USP Job Dispatch Implementation Plan (B-3b)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** An operator-queued job (Set/Get a parameter, Add/Delete an object, Reboot, Factory Reset, run diagnostics, discover the data model) dispatches to a USP agent the moment it can — on insert if the agent is already connected, on connect if it queued first — and resolves through the same `jobs` table CWMP already uses, so every feature built on top of the job queue (rollouts, policies, the operator console, audit) works over USP with zero changes above `cmd/uspc`.

**Architecture:** `cmd/uspc` gains its first `internal/jobs` import. A Postgres `LISTEN/NOTIFY` channel (fired by a trigger on `jobs` INSERT) plus a periodic sweep drive dispatch; both funnel into the same `dispatch(deviceID)` call, which leases a job restricted to the eight job types this plan maps, encodes it as a USP message, and sends it on the device's live `mtp.Conn`. Two independent completion paths resolve the job — a synchronous response (`*Resp`) matched by `msg_id`, or an asynchronous `Notify.OperationComplete` matched by `command_key` — whichever the agent actually uses, since USP lets an agent answer either way regardless of what `GetSupportedDM` would have predicted.

**Tech Stack:** Go 1.26; existing `internal/jobs`/`internal/devices`/`internal/store` stack; the pgx stdlib driver already in use, unwrapped via `sql.Conn.Raw()` to reach native `LISTEN/NOTIFY` — no new dependency.

**Spec:** [`docs/superpowers/specs/2026-09-09-usp-controller-design.md`](../specs/2026-09-09-usp-controller-design.md) — this plan implements §6 (dispatch) in full and the async half of §6.3/§6.4 (error mapping). §7 (subscriptions and Notify routing beyond `OnBoardRequest`) is a later plan, B-3c.

## Programme context

| Plan | Deliverable | Depends on |
|---|---|---|
| 0, A, B-1, B-2, B-3a | Defect fixes, fleet data model, protocol core, transports, agent identity — all complete. | — |
| **B-3b** (this) | Job dispatch over USP: LISTEN/NOTIFY trigger, job-type mapping, sync+async correlation, error mapping. | B-3a |
| B-3c | Subscriptions and full Notify routing. | B-3a, B-3b |

## Facts and decisions established during research (binding — do not re-derive)

**cmd/uspc today** (`backend/cmd/uspc/`): `handler.go` wires transport lifecycle to identity reconciliation (B-3a) — `OnConnect` registers the conn and starts the interop probe; `OnRecord` tries `usp.DecodeOnBoardRequest` first, falls through to the probe's `GetResp` handling; `OnDisconnect` clears identity state. `boundary_test.go`'s `forbiddenPrefixes` is `[]string{"acs/internal/jobs"}` only — `internal/devices` and `internal/store` are already permitted (B-3a). This plan's whole point is removing that last entry.

**`internal/jobs`'s real type list is larger than the design spec's §6.2 table.** Confirmed 15 types: the 8 the spec maps (`TypeSetParameter`, `TypeGetParameter`, `TypeAddObject`, `TypeDeleteObject`, `TypeReboot`, `TypeFactoryReset`, `TypeFirmwareDownload`, `TypeDiagnosticsPing`, `TypeDiagnosticsTraceroute`, `TypeParameterDiscovery` — that's actually 10, the table groups some), plus `TypeConnectionRequest`, `TypeScheduleInform`, `TypeSetParameterAttributes`, `TypeGetParameterAttributes`, `TypeUpload`.

**Decision — job type scope.** USP dispatch handles exactly the ten types the spec's §6.2 table names. The other five stay CWMP-only:
- `TypeConnectionRequest` — meaningless over USP; a USP agent is already connected, there is nothing to wake up.
- `TypeScheduleInform` — a CWMP concept (schedule a future Inform); USP has no equivalent RPC.
- `TypeSetParameterAttributes`/`TypeGetParameterAttributes` — CWMP's notification-configuration RPCs. USP's equivalent is subscription management, which is B-3c's scope, not this plan's.
- `TypeUpload` — CWMP's log-upload RPC. USP's base message set has no standard command for this; a vendor-specific `Operate` command would be guessing. Out of scope.

A job of one of these five types queued for a USP-only device (no CWMP session) is simply never leased by USP dispatch — it sits `QUEUED` forever, exactly as it would today for a CWMP device that never Informs. No special rejection path; `LeaseForTypes` (Task 1) filtering it out is sufficient.

**Decision — no `GetSupportedDM` sync/async cache.** The design spec's §6.3 describes caching each command's `CMD_SYNC`/`CMD_ASYNC` classification per device. No schema or code for this exists anywhere, and building one adds a whole caching subsystem for a distinction the wire already answers directly: an `OperateResp` that carries populated `output_args` **is** the sync completion; a later `Notify.OperationComplete` carrying the same `command_key` **is** the async completion. This plan reacts to whichever arrives — first one wins, race-guarded the same way `probe.go`'s pending map already guards against a duplicate/late reply (delete-on-match). `GetSupportedDM` itself is still sent for `TypeParameterDiscovery` jobs (the spec's own mapping), its result is the job's `result_detail`; it is simply never consulted to *decide* which completion path to expect.

**Decision — LISTEN/NOTIFY mechanism.** `internal/store.Open` returns a pooled `*sql.DB` via the `pgx/v5/stdlib` driver — `database/sql` gives no direct `WaitForNotification`. Reachable via `db.Conn(ctx)` (one dedicated connection, the same primitive `internal/store/postgres.go`'s `Migrate` already uses for its advisory lock) then `(*sql.Conn).Raw(func(driverConn any) error {...})` to unwrap to the underlying `*stdlib.Conn`, whose `.Conn()` method returns the native `*pgx.Conn` — that has `WaitForNotification(ctx) (*pgconn.Notification, error)`. This connection is held for the listener's entire life (long-lived, unlike `Migrate`'s one-shot use of the same primitive), with `LISTEN <channel>` issued as a raw `Exec` first, and reconnects with backoff on any error, since holding one pooled connection out of `database/sql`'s pool forever is the whole point (a lost connection must be replaced, not treated as fatal).

**Decision — dispatch is triggered three ways, converging on one function.** (1) a Postgres `NOTIFY` fired by a trigger on `jobs` INSERT (device already connected — dispatch immediately); (2) after a connection's identity reconciliation succeeds (job queued before the device connected — drain it the moment identity is known, extending B-3a's `resolveAndMarkReconciled`); (3) a periodic sweep (safety net for a missed notification, e.g. during a listener reconnect gap — spec §6.1 calls for exactly this). All three call the same `dispatcher.tryDispatch(ctx, deviceID)`.

**Decision — identity binding for async resolution.** CWMP's `TransferComplete` handler checks the reporting session's credential/mTLS identity against `job.DeviceID` before trusting a `command_key` resolution (`cmd/acs/session.go`'s audit C-1/M-2 checks) — USP has no per-request credential, only the endpoint id a connection reconciled to at connect time. The equivalent check for `Notify.OperationComplete`: the connection it arrived on must have a reconciled `device_id` (B-3a's per-connection identity state) equal to `job.DeviceID`. An unreconciled connection, or a device-id mismatch, refuses the resolution (logged, not silently ignored) rather than trusting the claim.

**Payload shape gaps, resolved:**
- `AddObjectPayload{ObjectPath string}` has no field for initial parameter values, which USP's `Add` message supports and CWMP's `AddObject` RPC does not. Add an optional `Parameters []ParameterWrite` field (reusing the existing `ParameterWrite` type) — empty for every existing CWMP caller, additive and backward compatible.
- `DeleteObjectPayload.ObjectPath` is singular; `usp.EncodeDelete` takes `objPaths []string`. Wrap in a one-element slice at the call site — no payload change.
- `ParameterWrite.Type` has no home in `usp.EncodeSet`'s `map[string]map[string]string` value shape (object path → param name → **string** value, no type). USP dispatch ignores `Type` — B-1's `EncodeSet` was deliberately built this way (see its doc comment), and USP has no wire concept of a CWMP-style `xsd:` type annotation on a `Set`.
- `SetParameterPayload.Parameters` is a flat list of full dotted paths (`ParameterWrite.Name`, e.g. `"Device.WiFi.SSID.1.SSID"`); `EncodeSet` wants them grouped by object path (`"Device.WiFi.SSID.1."` → `{"SSID": "value"}`). Group by splitting `Name` at its last `.` — the object path is everything up to and including that dot, the leaf name is everything after.

**Firmware and diagnostics command names — verify before implementing, do not trust this plan's guess as ground truth.** `Device.Reboot()` and `Device.FactoryReset()` are standard, unparameterized top-level TR-181 commands with essentially no ambiguity. `Device.IP.Diagnostics.IPPing()`/`Device.IP.Diagnostics.TraceRoute()` (ping/traceroute) and `Device.FirmwareImage.{i}.Download()` (firmware) are also standard TR-181 Device:2 commands, but their exact `InputArguments` names and the firmware command's instance-addressing (`{i}`) carry real risk of being subtly wrong from memory alone — this project's own standing practice (see B-2's obuspa commit correction) is to verify protocol facts against a primary source before shipping them, not assert from training data. Task 3's implementer must check the actual TR-181 Device:2 data model (or, cheaper and already available, `obuspa`'s own supported-command list — the same repository B-2's CI job already clones at a pinned commit, `ci/usp/` or the obuspa source itself likely enumerates supported `Operate` commands and their `InputArguments`) before finalizing these three command names and argument sets. If a command's exact shape cannot be confirmed with reasonable confidence, that job type ships as **not yet supported over USP** for this plan (job stays queued, logged, no guessed RPC sent) rather than risk sending a malformed command to real firmware — this is a legitimate, explicit scope reduction if verification comes up short, not silent scope creep either way it lands.

## Global Constraints

- Go module `acs`; directive `go 1.26.6`. Do not raise it.
- No new dependencies — `LISTEN/NOTIFY` reachability is via the existing `pgx/v5/stdlib` driver's `Raw()` escape hatch, not a new library.
- `cmd/uspc` may now import `acs/internal/jobs` (this plan removes the last entry from `boundary_test.go`'s `forbiddenPrefixes`, which then becomes an empty allowlist — see Task 4 for whether the test itself should be deleted or kept as a documented-empty guard).
- `internal/usp/...` still may not import any domain package — B-1's walk-based boundary test enforces this over the whole tree and is untouched by this plan. Any code that needs both USP types and domain types (`jobs.Job`, `devices.Repository`) lives in `cmd/uspc`, never inside `internal/usp`.
- Every migration is forward-only and checksum-verified at boot — never edit a committed migration.
- Before every commit, from `backend/`: `gofmt -l .` prints nothing, `go vet ./...` and `go test ./...` pass.
- DB-backed tests use `ACS_TEST_POSTGRES_DSN`, matching every existing DB-backed test in this codebase.
- A job dispatched over USP must reach exactly the device it targets — every outbound Record's `to_id` is the device's currently-linked `endpoint_id` (via `mtp.Conn.Endpoint()`), read fresh at dispatch time, never cached from an earlier connection.
- Commit message style: `type(scope): summary`, imperative mood. End each commit message with:
  `Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>`

## File structure

| File | Responsibility |
|---|---|
| `backend/internal/store/migrations/0054_jobs_notify.sql` | Trigger function + trigger: `NOTIFY <channel>` with the device id as payload, on `jobs` INSERT where `status = 'QUEUED'`. |
| `backend/internal/jobs/lease.go` (modify) | `LeaseForTypes(ctx, deviceID, types)`; `Lease` becomes a thin wrapper calling it with `sessionDispatchableTypes`. |
| `backend/internal/jobs/payloads.go` (modify) | `AddObjectPayload` gains `Parameters []ParameterWrite`. |
| `backend/internal/jobs/listen.go` | `QueueListener`: dedicated-connection `LISTEN`, reconnect-with-backoff, `Notifications() <-chan string`. |
| `backend/internal/jobs/listen_test.go` | DB-backed tests. |
| `backend/internal/devices/usp_agents.go` (modify) | `GetUspAgentByDeviceID(ctx, deviceID) (*UspAgent, error)` — the reverse lookup dispatch needs. |
| `backend/internal/usp/message.go` (modify) | `DecodeOperationComplete` — mirrors `DecodeOnBoardRequest`'s pattern for the one other Notify variant this plan needs. |
| `backend/cmd/uspc/dispatch.go` | `buildUSPRequest(msgID string, job *jobs.Job) ([]byte, error)` — the job-type switch, mirrors `cmd/acs/dispatch.go`'s `renderJobRequest`. |
| `backend/cmd/uspc/dispatch_test.go` | Tests for every mapped job type. |
| `backend/cmd/uspc/dispatcher.go` | `dispatcher` type: `tryDispatch(ctx, deviceID)`, the three trigger paths, the pending-response map generalizing `probe.go`'s pattern. |
| `backend/cmd/uspc/dispatcher_test.go` | Tests using fake repositories/conns, matching `identity_test.go`'s existing style. |
| `backend/cmd/uspc/handler.go` (modify) | `OnRecord` routes `*Resp` messages to the dispatcher's sync-completion path; `resolveAndMarkReconciled` triggers `tryDispatch` after a successful reconcile. |
| `backend/cmd/uspc/main.go` (modify) | Wires `jobs.NewRepository`, the `QueueListener`, the periodic sweep goroutine, and the dispatcher into `run`/`shutdown`. |
| `backend/cmd/uspc/boundary_test.go` (modify) | `forbiddenPrefixes` becomes empty — decide in Task 4 whether to delete the test or keep it as a documented no-op guard against a future regression. |
| `ci/usp/` (modify) | Interop job proves a queued job actually dispatches and resolves against real obuspa. |

---

### Task 1: `internal/jobs`/`internal/devices` — the small, safe additions

**Files:**
- Modify: `backend/internal/jobs/lease.go`, `backend/internal/jobs/lease_test.go`
- Modify: `backend/internal/jobs/payloads.go`
- Modify: `backend/internal/devices/usp_agents.go`, `backend/internal/devices/usp_agents_test.go`

**Interfaces:**
- Consumes: existing `Repository.Lease`, `sessionDispatchableTypes` (`internal/jobs/job.go:64-69`), `UspAgent` struct and `uspAgentColumns` (`internal/devices/usp_agents.go`).
- Produces:
  - `func (r *jobs.Repository) LeaseForTypes(ctx context.Context, deviceID string, types []string) (*Job, error)` — identical transaction/locking behavior to today's `Lease`, parameterized on the type filter instead of hardcoding `sessionDispatchableTypes`.
  - `Lease` becomes `func (r *Repository) Lease(ctx context.Context, deviceID string) (*Job, error) { return r.LeaseForTypes(ctx, deviceID, sessionDispatchableTypes) }`.
  - `AddObjectPayload` gains `Parameters []ParameterWrite \`json:"parameters,omitempty"\`` — reuses the existing `ParameterWrite` type, `omitempty` so existing CWMP-created payloads round-trip unchanged.
  - `func (r *devices.Repository) GetUspAgentByDeviceID(ctx context.Context, deviceID string) (*UspAgent, error)` — `WHERE device_id = $1` (the table's primary key, so this is a point lookup), returns `ErrUspAgentNotFound` on no row, matching `GetUspAgentByEndpointID`'s existing error-handling shape exactly.

**Contract:**
- `LeaseForTypes`'s SQL is `Lease`'s current query with `type = ANY($2)` bound to the `types` parameter instead of the hardcoded `sessionDispatchableTypes`; every other clause (`FOR UPDATE SKIP LOCKED`, the `RPC_SENT`/`started_at`/`attempts`/`lease_owner`/`leased_until` update) is unchanged.
- No behavior change for any existing CWMP caller — `Lease`'s callers, tests, and semantics are identical before and after this refactor.

**Checklist:**
| Requirement | Test |
|---|---|
| `Lease`'s existing behavior is unchanged after the refactor | existing `lease_test.go` suite, unmodified, still passes |
| `LeaseForTypes` with a narrower type list only leases matching types | `TestLeaseForTypesFiltersByType` |
| `AddObjectPayload` round-trips through JSON with and without `Parameters` | `TestAddObjectPayloadJSONRoundTrip` |
| `GetUspAgentByDeviceID` finds a linked agent | `TestGetUspAgentByDeviceID` |
| `GetUspAgentByDeviceID` returns `ErrUspAgentNotFound` for an unlinked device | `TestGetUspAgentByDeviceIDNotFound` |

- [ ] **Step 1: Write the failing tests**

`lease_test.go`: `TestLeaseForTypesFiltersByType` — queue two jobs of different types for one device (e.g. one `TypeUpload`, one `TypeGetParameter`), call `LeaseForTypes(ctx, deviceID, []string{jobs.TypeGetParameter})`, assert only the `TypeGetParameter` job is returned and the `TypeUpload` job stays `QUEUED`.

`payloads.go` needs no new test file if one doesn't exist for it — check first; if `payloads_test.go` doesn't exist, add `TestAddObjectPayloadJSONRoundTrip` to whichever existing test file covers `internal/jobs`' payload types, or create one.

`usp_agents_test.go`: `TestGetUspAgentByDeviceID`/`...NotFound`, following the exact DB-backed pattern `TestGetUspAgentByEndpointID`/`...NotFound` already use in the same file.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/jobs/ ./internal/devices/ -run 'TestLeaseForTypes|TestAddObjectPayload|TestGetUspAgentByDeviceID' -v`. Expected: compile FAIL — functions undefined.

- [ ] **Step 3: Implement**

Refactor `Lease`/add `LeaseForTypes` in `lease.go`; add `Parameters` to `AddObjectPayload`; add `GetUspAgentByDeviceID` to `usp_agents.go` mirroring `GetUspAgentByEndpointID`'s exact structure (same `scanUspAgent`, same error wrapping).

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/jobs/... ./internal/devices/... -v`. Expected: all PASS, including the full pre-existing `lease_test.go` suite unchanged.

- [ ] **Step 5: Full backend checks, then commit**

```bash
git add internal/jobs/lease.go internal/jobs/lease_test.go internal/jobs/payloads.go \
        internal/devices/usp_agents.go internal/devices/usp_agents_test.go
git commit -F - <<'EOF'
feat(jobs,devices): type-filtered leasing and the device-to-agent reverse lookup

LeaseForTypes parameterizes Lease's existing type filter so USP
dispatch -- which can only execute a subset of job types -- never
leases a job it cannot build a USP request for. GetUspAgentByDeviceID
is the reverse of the endpoint-id lookup B-3a already has; dispatch
needs device_id -> endpoint_id -> live mtp.Conn, and this is the first
hop.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
```

---

### Task 2: Postgres LISTEN/NOTIFY — trigger and listener

**Files:**
- Create: `backend/internal/store/migrations/0054_jobs_notify.sql`
- Create: `backend/internal/jobs/listen.go`, `backend/internal/jobs/listen_test.go`

**Interfaces:**
- Consumes: `internal/store.Open`'s `*sql.DB` (pgx stdlib driver); `github.com/jackc/pgx/v5/stdlib` for the `Raw()` unwrap (already an indirect dependency via `internal/store`, not a new one).
- Produces:
  - Migration: a `PL/pgSQL` trigger function `notify_jobs_queued()` calling `pg_notify('acs_jobs_queued', NEW.device_id::text)`, and a trigger `jobs_notify_queued AFTER INSERT ON jobs FOR EACH ROW WHEN (NEW.status = 'QUEUED') EXECUTE FUNCTION notify_jobs_queued()`.
  - `type QueueListener struct { ... }`, `func Listen(ctx context.Context, db *sql.DB, channel string, log *slog.Logger) (*QueueListener, error)` — acquires one dedicated `*sql.Conn`, unwraps to the native `*pgx.Conn`, issues `LISTEN <channel>`, starts an internal goroutine calling `WaitForNotification` in a loop.
  - `func (l *QueueListener) Notifications() <-chan string` — the channel of `device_id` payload strings.
  - `func (l *QueueListener) Close() error` — stops the goroutine, releases the dedicated connection back to the pool.

**Contract:**
- The channel name (`"acs_jobs_queued"`) is a constant exported as `jobs.NotifyChannel` so `cmd/uspc` and the migration agree on it without a magic string duplicated in two places — the migration file itself can't reference a Go constant, so the migration's literal string and `jobs.NotifyChannel`'s value must be identical; add a test asserting this (e.g. a DB-backed test that inserts a job and asserts a notification arrives on `jobs.NotifyChannel`, which fails loudly if the two ever drift).
- On any error from `WaitForNotification` (connection dropped, network blip), the listener's goroutine does not exit — it closes the broken connection, waits a short backoff (start at 1s, cap at 30s, no need for anything fancier than a fixed doubling with a cap), re-acquires a fresh `*sql.Conn`, re-issues `LISTEN`, and resumes. Only `Close()` (context cancellation) stops it for good.
- `Notifications()`'s channel is buffered (a reasonable size, e.g. 64) so a burst of inserts doesn't block Postgres's own notify delivery; a full channel drops the oldest-unread notification with a logged warning rather than blocking — the periodic sweep (Task 4) is the safety net for anything dropped this way, so silent loss under load is an accepted, bounded degradation, not data loss (no job is lost, only its "hurry up" signal).
- The trigger fires on INSERT only, not on UPDATE — a job re-queued via `Requeue` (existing method, sets `status` back to `QUEUED` on a retry) does **not** get a fresh NOTIFY in this plan; the periodic sweep (Task 4) covers requeued jobs. Document this explicitly in the migration's comment so a future reader doesn't assume requeue-triggers-notify and is surprised when it doesn't.

**Checklist:**
| Requirement | Test |
|---|---|
| Migration applies cleanly | `TestMigration0054AppliesCleanly` |
| A `jobs` INSERT with `status='QUEUED'` fires a NOTIFY carrying the device id | `TestMigration0054NotifiesOnInsert` |
| `jobs.NotifyChannel`'s Go constant matches the migration's literal channel name | same test, asserted via `Listen(ctx, db, jobs.NotifyChannel, ...)` actually receiving it |
| `QueueListener.Notifications()` delivers a payload end to end | `TestListenReceivesNotification` |
| `Close()` stops the goroutine cleanly (no leak, no panic) | `TestListenCloseStopsCleanly` |

- [ ] **Step 1: Write the failing tests**

`migration_0054_test.go` (or add to the pattern established by `migration_0053_test.go`): after `Migrate`, in one connection call `LISTEN acs_jobs_queued`, in another perform a real `jobs.Repository.Create(...)`-style insert (or the raw SQL a job insert produces — check `internal/jobs/job.go`'s `Create` for the exact INSERT shape), and assert a notification with the right device id arrives via a direct `pgxpool`/raw-connection listen (bypassing `QueueListener` entirely, to test the trigger in isolation from the Go listener code).

`listen_test.go`: `TestListenReceivesNotification` — call `jobs.Listen(ctx, db, jobs.NotifyChannel, logger)`, insert a job via the real repository, assert the device id arrives on `Notifications()` within a short timeout (e.g. 3s). `TestListenCloseStopsCleanly` — start a listener, call `Close()`, assert it returns promptly and the goroutine actually exits (e.g. via a `sync.WaitGroup` the listener exposes for testing, or by asserting `Notifications()` closes — pick whichever is simplest to verify without adding test-only production API surface).

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/store/ ./internal/jobs/ -run 'TestMigration0054|TestListen' -v`. Expected: FAIL — migration/functions don't exist.

- [ ] **Step 3: Implement**

Write the migration exactly per the Contract. Write `listen.go` per the Produces contract, using `db.Conn(ctx)` → `.Raw(func(driverConn any) error { ... })` → type-assert to `*stdlib.Conn` → `.Conn()` to reach the native `*pgx.Conn`. Reference `internal/store/postgres.go`'s `Migrate` function for the exact `db.Conn`/`defer Close` pattern this codebase already uses for a dedicated connection, even though `Migrate`'s use is one-shot and this one is long-lived.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/store/... ./internal/jobs/... -v`. Expected: all PASS.

- [ ] **Step 5: Full backend checks, then commit**

```bash
git add internal/store/migrations/0054_jobs_notify.sql internal/store/migration_0054_test.go \
        internal/jobs/listen.go internal/jobs/listen_test.go
git commit -F - <<'EOF'
feat(jobs): LISTEN/NOTIFY on job insert, with reconnect and a bounded buffer

Push-not-pull dispatch (design S6.1) needs a way for cmd/uspc to learn
about a newly queued job without polling. The trigger fires on INSERT
only -- a requeue does not get a fresh notify, which is fine because
the periodic sweep this plan's dispatcher adds is the stated safety
net for exactly that gap, not just for connection loss.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
```

---

### Task 3: `cmd/uspc/dispatch.go` — job type to USP message mapping

**Files:**
- Create: `backend/cmd/uspc/dispatch.go`, `backend/cmd/uspc/dispatch_test.go`

**Interfaces:**
- Consumes: `jobs.Job`, `jobs.Type*` constants, `jobs.SetParameterPayload`/`GetParameterPayload`/`AddObjectPayload`/`DeleteObjectPayload`/`DiagnosticsPingPayload`/(traceroute and firmware payload types — check `internal/jobs/payloads.go` for their exact names and fields before writing this task's code); `usp.EncodeGet`/`EncodeSet`/`EncodeAdd`/`EncodeDelete`/`EncodeOperate`/`EncodeGetSupportedDM`.
- Produces: `func buildUSPRequest(msgID string, job *jobs.Job) ([]byte, error)` — the payload of a Record still needs encoding via `usp.EncodeRecord` at the call site (Task 4), this function returns the `usp.EncodeXxx`-produced `Msg` bytes only. Also: `func groupParametersByObjectPath(params []jobs.ParameterWrite) map[string]map[string]string` (the Set grouping helper from the Decisions section above) as its own tested unit, since it's the one piece of nontrivial logic in this file.

**Contract — the ten-type switch:**
| `job.Type` | USP call | Notes |
|---|---|---|
| `TypeGetParameter` | `usp.EncodeGet(msgID, payload.Paths, 0)` | `GetParameterPayload{Paths []string}` maps 1:1. `maxDepth 0` = unlimited, matching a CWMP GetParameterValues' unrestricted depth. |
| `TypeSetParameter` | `usp.EncodeSet(msgID, false, groupParametersByObjectPath(payload.Parameters))` | `allowPartial=false` — see the Decisions section's rationale. |
| `TypeAddObject` | `usp.EncodeAdd(msgID, true, payload.ObjectPath, paramMap)` | `paramMap` built from the new `payload.Parameters` field (Task 1) — empty map if `Parameters` is nil/empty, never a nil map (check `EncodeAdd`'s handling of a nil vs empty map and match whichever it expects). |
| `TypeDeleteObject` | `usp.EncodeDelete(msgID, true, []string{payload.ObjectPath})` | Singular-to-slice wrap. |
| `TypeReboot` | `usp.EncodeOperate(msgID, "Device.Reboot()", job.CommandKey, true, nil)` | Standard TR-181 command, no verification needed. |
| `TypeFactoryReset` | `usp.EncodeOperate(msgID, "Device.FactoryReset()", job.CommandKey, true, nil)` | Standard TR-181 command, no verification needed. |
| `TypeFirmwareDownload` | `usp.EncodeOperate(msgID, "<verify>", job.CommandKey, true, inputArgs)` | **Verify the exact command path and `InputArguments` before implementing** — see the Decisions section. If verification doesn't land with reasonable confidence, mark unsupported (see below) rather than guess. |
| `TypeDiagnosticsPing` | `usp.EncodeOperate(msgID, "<verify>", job.CommandKey, true, inputArgs)` | Same verification requirement. |
| `TypeDiagnosticsTraceroute` | `usp.EncodeOperate(msgID, "<verify>", job.CommandKey, true, inputArgs)` | Same verification requirement. |
| `TypeParameterDiscovery` | `usp.EncodeGetSupportedDM(msgID, []string{"Device."}, false, true, true, true)` | Discovers the whole tree; the job's `result_detail` becomes the full `GetSupportedDMResp`, summarized (see Task 6 for how a `*Resp` becomes `result_detail`). |

For any job type whose command shape couldn't be verified with confidence: `buildUSPRequest` returns a distinct sentinel `ErrUnsupportedOverUSP`, and Task 4's dispatcher treats that as "put this job back, do not mark it failed" (a `TypeFirmwareDownload` job for a USP-only device should wait for a human to notice, not be silently marked FAILED as if the device rejected it) — log at Warn, leave `status='QUEUED'`, and **do not** re-lease it in the same sweep (or it would spin) — this needs its own small guard in Task 4, noted there.

**Checklist:**
| Requirement | Test |
|---|---|
| Each of the ten mapped types produces a correctly-shaped `usp.Msg` (decode it back with `usp.DecodeMsg` and assert the fields) | one test per type, e.g. `TestBuildUSPRequestGetParameter`, `...SetParameter`, etc. |
| `groupParametersByObjectPath` groups correctly, including a path with no dot (edge case: a top-level scalar, if that's even valid — check and document) | `TestGroupParametersByObjectPath` |
| `SetParameter` uses `allowPartial=false` | asserted in `TestBuildUSPRequestSetParameter` via the decoded `Set.AllowPartial` field |
| `AddObject` with empty `Parameters` still produces a valid (non-nil-map, if required) `Add` message | `TestBuildUSPRequestAddObjectNoParams` |
| An unsupported type (if verification came up short for firmware/diagnostics) returns `ErrUnsupportedOverUSP`, not a malformed message | `TestBuildUSPRequestUnsupportedType` (only if applicable — if verification succeeds for all three, this test instead proves they succeed, and `ErrUnsupportedOverUSP` is only reachable for a genuinely unmapped type as a defensive default case) |
| An unrecognized `job.Type` (one of the five explicitly out-of-scope types, or a future addition) returns `ErrUnsupportedOverUSP` too, not a panic | `TestBuildUSPRequestUnknownType` |

- [ ] **Step 1: Verify the firmware/diagnostics command shapes**

Before writing any test, check the TR-181 Device:2 data model (or obuspa's own command enumeration — `ci/usp/` from B-2 or the obuspa source at the pinned commit) for `Device.FirmwareImage.{i}.Download()`'s exact `InputArguments` and instance-addressing convention, and `Device.IP.Diagnostics.IPPing()`/`TraceRoute()`'s exact argument names. Record what you found (source, exact strings) in your report before proceeding — this is load-bearing evidence, not a formality.

- [ ] **Step 2: Write the failing tests**

One test per mapped type, each building a `jobs.Job` with a realistic `Payload`, calling `buildUSPRequest`, and decoding the result back through `usp.DecodeMsg` to assert the right message shape and field values — not just "no error." `TestGroupParametersByObjectPath` as a pure unit test with a handful of literal cases (single param, multiple params on one object, params on two different objects, a path with only one segment after the root if that's a real case).

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test ./cmd/uspc/ -run TestBuildUSPRequest -v`. Expected: compile FAIL.

- [ ] **Step 4: Implement**

`dispatch.go` per the Contract table, using whatever Step 1 verified for firmware/diagnostics.

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./cmd/uspc/... -v`. Expected: all PASS.

- [ ] **Step 6: Full backend checks, then commit**

```bash
git add cmd/uspc/dispatch.go cmd/uspc/dispatch_test.go
git commit -F - <<'EOF'
feat(uspc): map job types to USP request messages

Ten of internal/jobs' fifteen types get a USP mapping (design S6.2);
the other five are CWMP-only concepts (wake-up, scheduling, CWMP
notification attributes, log upload) with no USP equivalent and are
simply never leased over this transport. Firmware and diagnostics
command shapes were verified against <source> before encoding, not
assumed from memory.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
```

---

### Task 4: `cmd/uspc/dispatcher.go` — the dispatch loop and sync-completion correlation

**Files:**
- Create: `backend/cmd/uspc/dispatcher.go`, `backend/cmd/uspc/dispatcher_test.go`
- Modify: `backend/cmd/uspc/handler.go`, `backend/cmd/uspc/main.go`, `backend/cmd/uspc/boundary_test.go`

**Interfaces:**
- Consumes: `jobs.Repository.LeaseForTypes`, `jobs.Repository.MarkSuccessWithDetail`/`MarkFailed` (Task 1, existing); `devices.Repository.GetUspAgentByDeviceID` (Task 1); `mtp.Registry.Get(endpointID)`; `usp.EncodeRecord`; `buildUSPRequest` (Task 3); `jobs.QueueListener` (Task 2); the existing `identityStore`/`reconciler` from B-3a for the OnConnect-triggers-dispatch hook.
- Produces:
  - `var uspDispatchableTypes = []string{jobs.TypeGetParameter, jobs.TypeSetParameter, ...}` (the ten types, package-private to `cmd/uspc` — `internal/jobs` stays protocol-agnostic, it does not need to know USP's subset).
  - `type dispatcher struct { jobsRepo *jobs.Repository; devicesRepo identityStore; registry *mtp.Registry; controllerID usp.EndpointID; log *slog.Logger; pending map[string]pendingDispatch; mu sync.Mutex }` (extend `identityStore` if it doesn't already expose `GetUspAgentByDeviceID` — check Task 1's addition landed on the right interface).
  - `type pendingDispatch struct { job *jobs.Job; conn mtp.Conn }` — mirrors `probe.go`'s `pendingProbe`, keyed by `msg_id` the same way.
  - `func newDispatcher(jobsRepo *jobs.Repository, devicesRepo identityStore, registry *mtp.Registry, controllerID usp.EndpointID, log *slog.Logger) *dispatcher`
  - `func (d *dispatcher) tryDispatch(ctx context.Context, deviceID string) error` — looks up the live conn via `GetUspAgentByDeviceID` → `registry.Get(endpointID)` (no live conn → return nil, nothing to do, not an error); leases via `LeaseForTypes(ctx, deviceID, uspDispatchableTypes)` (no leasable job → return nil); builds the request via `buildUSPRequest`; on `ErrUnsupportedOverUSP`, log at Warn and return nil **without** re-attempting that same job in this call (the lease already marked it `RPC_SENT`/consumed an attempt — decide whether `tryDispatch` should `Requeue` it immediately so it doesn't sit falsely `RPC_SENT` forever, since nothing will ever complete that RPC; requeueing immediately after an unsupported-type dispatch failure is the right behavior — do this); otherwise wraps it in a Record (`usp.EncodeRecord(d.controllerID, conn.Endpoint(), msgBytes)`) and sends it (`conn.Send`, bounded by a short timeout matching `probe.go`'s `sendTimeout` constant — reuse or mirror it), records the pending entry keyed by `msg_id`, and returns.
  - `func (d *dispatcher) handleResponse(from usp.EndpointID, msg *uspproto.Msg) (matched bool)` — same shape as `probe.handle`: look up `msg.GetHeader().GetMsgId()`, delete-on-match, and on match either `MarkSuccessWithDetail` (response decoded into a JSON-serializable summary — reuse whatever shape is natural per message type, this doesn't need to match CWMP's `result_detail` shape byte-for-byte, just be useful) or, if the message is an `Error`, map it via `usp.ErrorFromMsg` and `MarkFailed(job.ID, strconv.Itoa(int(uspErr.Code)), uspErr.Message)` (Task 6 refines this mapping; a basic version here is fine, Task 6 tightens it).
  - `func (d *dispatcher) periodicSweep(ctx context.Context, registry *mtp.Registry, interval time.Duration)` — every `interval`, calls `registry.Each(func(c mtp.Conn) { ... })` and, for each live connection whose identity is reconciled, calls `tryDispatch` for its device id. Runs as a goroutine started in `main.go`'s `run`, stopped via context cancellation on shutdown.

**Contract — wiring into `handler.go`:**
- `handler.OnRecord`, after its existing `usp.DecodeOnBoardRequest`/probe-handling fallthrough, adds one more fallthrough: if neither of those matched, try `dispatcher.handleResponse(rec.From, msg)`. Order matters — OnBoardRequest first (it's a `Notify`, structurally distinct), then the probe's own `Get`/`GetResp` round trip, then dispatch responses last, since all three are mutually exclusive message shapes for a properly correlated `msg_id` and the ordering only matters for which one's "not found" log fires if something is badly malformed.
- `resolveAndMarkReconciled` (B-3a, in `handler.go`), on success, calls `dispatcher.tryDispatch(ctx, deviceID)` — the "job queued before device connected" trigger. A failure here (no leasable job, no live conn edge case that shouldn't happen since this *is* the newly-connected conn) is logged, not treated as a reconciliation failure.
- `main.go`'s `run`: constructs `jobs.NewRepository(db)`, the `dispatcher`, starts `jobs.Listen(ctx, db, jobs.NotifyChannel, logger)` and a goroutine draining `Notifications()` into `dispatcher.tryDispatch`, and starts `periodicSweep` as another goroutine. Both goroutines stop on `shutdown`'s context. Pick a sweep interval — 30s is a reasonable default matching the order of magnitude of CWMP's own periodic-inform-adjacent intervals; document why in a comment, don't leave it a bare unexplained number.
- `boundary_test.go`: `forbiddenPrefixes` becomes `[]string{}`. **Decision: keep the test, don't delete it.** An empty-but-present `TestUSPCImportsNoDomainPackages` with a rewritten doc comment ("no domain package is currently forbidden — this test exists as a placeholder a future plan can populate if a new boundary is needed, e.g. keeping USP protocol negotiation free of billing logic") costs nothing and keeps the file's structure ready for the next boundary this programme decides to draw, rather than deleting infrastructure that cost real effort to build correctly (the path-boundary predicate, the `Imports`/`TestImports`/`XTestImports` coverage). If `forbiddenPrefixes` is empty, the test trivially passes — assert that explicitly with a one-line test rather than leaving an empty loop with no assertion at all.

**Checklist:**
| Requirement | Test |
|---|---|
| `tryDispatch` with no live connection for the device is a no-op, not an error | `TestTryDispatchNoLiveConnection` |
| `tryDispatch` with no leasable job is a no-op | `TestTryDispatchNoJob` |
| `tryDispatch` with a leasable job sends the right bytes on the right conn and records a pending entry | `TestTryDispatchSendsAndTracks` |
| `tryDispatch` on an `ErrUnsupportedOverUSP` job requeues it rather than leaving it falsely `RPC_SENT` | `TestTryDispatchRequeuesUnsupportedType` |
| `handleResponse` resolves a pending dispatch on a matching `msg_id`, marks the job success | `TestHandleResponseResolvesSuccess` |
| `handleResponse` on an Error message marks the job failed | `TestHandleResponseResolvesFailure` |
| `handleResponse` on an unknown `msg_id` is a no-op, not a crash (mirrors `probe.handle`) | `TestHandleResponseUnknownMsgID` |
| `resolveAndMarkReconciled` success triggers `tryDispatch` for the newly-known device | `TestReconcileTriggersDispatch` |
| `boundary_test.go` with an empty `forbiddenPrefixes` still asserts something meaningful | `TestUSPCImportsNoDomainPackages` (rewritten) |

- [ ] **Step 1: Write the failing tests**

`dispatcher_test.go`: fake `identityStore` (extend or reuse `identity_test.go`'s `fakeIdentityStore`, adding `GetUspAgentByDeviceID` support), a `fakeConn` (reuse the pattern from `probe_test.go`'s `captureConn` or `registry_test.go`'s `fakeConn` — check both, pick whichever fits), and a fake/minimal `jobs.Repository` — check whether `internal/jobs` already has an interface seam for testing or whether `dispatcher_test.go` needs a real DB-backed `*jobs.Repository` (likely the latter, since `jobs.Repository` is a concrete type wrapping `*sql.DB` throughout the codebase, matching `internal/devices`' own pattern — if so, these tests are DB-backed, not pure-fake, and that's fine, follow the codebase's existing convention rather than inventing an interface seam `internal/jobs` doesn't have).

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./cmd/uspc/ -run 'TestTryDispatch|TestHandleResponse|TestReconcileTriggersDispatch' -v`. Expected: compile FAIL.

- [ ] **Step 3: Implement**

`dispatcher.go` per the Contract; wire into `handler.go`/`main.go`; rewrite `boundary_test.go`.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./cmd/uspc/... -v`. Expected: all PASS.

- [ ] **Step 5: Full backend checks, then commit**

```bash
git add cmd/uspc/dispatcher.go cmd/uspc/dispatcher_test.go cmd/uspc/handler.go \
        cmd/uspc/main.go cmd/uspc/boundary_test.go
git commit -F - <<'EOF'
feat(uspc): dispatch loop -- LISTEN/NOTIFY, reconnect drain, periodic sweep

cmd/uspc gains its last forbidden import, internal/jobs. Three trigger
paths converge on one tryDispatch: a Postgres NOTIFY for an
already-connected device, a successful identity reconcile for a device
that queued a job before it connected, and a periodic sweep as the
safety net design S6.1 calls for. Sync completions correlate by
msg_id, the same pattern probe.go already established for its own
single-purpose Get/GetResp round trip, generalized to every dispatched
job.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
```

---

### Task 5: Async completion — `Notify.OperationComplete` and identity-bound `command_key` resolution

**Files:**
- Modify: `backend/internal/usp/message.go`, `backend/internal/usp/message_test.go`
- Modify: `backend/cmd/uspc/handler.go`, `backend/cmd/uspc/dispatcher.go`, `backend/cmd/uspc/handler_test.go`

**Interfaces:**
- Consumes: the generated `uspproto.Notify_OperComplete`/`uspproto.Notify_OperationComplete` types (confirm exact generated names by grepping `uspproto/usp-msg-1-3.pb.go`, following the same "verify before use" discipline Task 2 of B-3a used for `Notify_OnBoardReq`); `jobs.Repository.ByCommandKey`; B-3a's per-connection identity state (the map `handler.go` already keeps from `mtp.Conn` to its reconciled `device_id` — read the current `handler.go` for its exact name/shape before extending it).
- Produces:
  - `type OperationComplete struct { SubscriptionID string; SendResp bool; CommandKey string; OutputArgs map[string]string; ErrCode uint32; ErrMsg string }`, `var ErrNotOperationComplete = errors.New(...)`, `func DecodeOperationComplete(msg *uspproto.Msg) (*OperationComplete, error)` — mirrors `DecodeOnBoardRequest`'s exact structure (reject non-`NOTIFY`, reject any other `Notification` oneof variant, both via the sentinel).
  - `func (d *dispatcher) handleOperationComplete(ctx context.Context, connDeviceID string, oc *usp.OperationComplete) error` — looks up the job by `oc.CommandKey` via `ByCommandKey`; if not found, logs and returns nil (an unknown command key from a Notify is not necessarily an error — could be a retransmission for a job already resolved and no longer trackable, or an agent-side artifact); **checks `connDeviceID == job.DeviceID`** before resolving anything (the identity-binding decision above) — a mismatch is logged at Warn with both ids and the resolution is refused; on match, resolves success/failure the same way `handleResponse` does for a sync `*Resp`, through the **same underlying resolve-once function** so a job that gets both a sync `OperateResp` and a later `OperationComplete` (a real possibility if `SendResp` was true and the agent also fires the Notify) only resolves once — reuse `handleResponse`'s pending-map delete-on-match discipline, or if the job was already resolved and removed from `pending`, treat the second signal as a no-op, not an error.

**Contract:**
- `handler.OnRecord`'s fallthrough chain gains one more link, before the dispatch-response fallthrough added in Task 4: try `usp.DecodeOperationComplete(msg)`; on success, call `dispatcher.handleOperationComplete` with the current connection's reconciled device id (nil/empty if unreconciled — the identity check inside `handleOperationComplete` handles that case as a mismatch/refusal, it doesn't need special-casing at the call site).
- If `oc.SendResp` is true, send a `NotifyResp` back (reuse `usp.EncodeNotifyResp` from B-3a, same pattern `handleOnBoardRequest` already uses for its own `SendResp` handling).

**Checklist:**
| Requirement | Test |
|---|---|
| Decodes a well-formed OperationComplete, all fields including error case (`ErrCode`/`ErrMsg` populated when the operation itself failed on the agent) | `TestDecodeOperationComplete`, `TestDecodeOperationCompleteWithError` |
| Rejects a non-OperationComplete Notify variant / non-Notify message, mirroring Task 2 of B-3a's tests | `TestDecodeOperationCompleteWrongVariant`, `...WrongMsgType` |
| A resolved job's identity check passes when the connection's device id matches | `TestHandleOperationCompleteResolves` |
| A mismatched device id refuses resolution, logs, does not mark the job | `TestHandleOperationCompleteRefusesIdentityMismatch` |
| An unknown command_key is a no-op, not an error/crash | `TestHandleOperationCompleteUnknownCommandKey` |
| A job that already resolved via sync `OperateResp` is not re-resolved by a later `OperationComplete` for the same job | `TestHandleOperationCompleteAfterSyncResolutionIsNoop` |
| `SendResp: true` produces a `NotifyResp` on the same conn | `TestHandlerOperationCompleteSendsResp` |

- [ ] **Step 1: Write the failing tests**

`message_test.go`: mirror B-3a Task 2's `TestDecodeOnBoardRequest*` tests exactly, substituting `Notify_OperComplete`/`OperationComplete` — hand-build a `Msg` via `uspproto` types, cover the checklist's first two rows including the error-populated case (`OperComplete.ErrCode != 0`).

`handler_test.go`/`dispatcher_test.go`: extend with the identity-mismatch and double-resolution scenarios, using the same fake-conn/fake-repository patterns Task 4 established.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/usp/ ./cmd/uspc/ -run 'TestDecodeOperationComplete|TestHandleOperationComplete|TestHandlerOperationComplete' -v`. Expected: compile FAIL.

- [ ] **Step 3: Implement**

`DecodeOperationComplete` in `message.go`, following `DecodeOnBoardRequest`'s exact shape. `handleOperationComplete` in `dispatcher.go`, wired into `handler.OnRecord`.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/usp/... ./cmd/uspc/... -v`. Expected: all PASS.

- [ ] **Step 5: Full backend checks, then commit**

```bash
git add internal/usp/message.go internal/usp/message_test.go \
        cmd/uspc/handler.go cmd/uspc/dispatcher.go cmd/uspc/handler_test.go cmd/uspc/dispatcher_test.go
git commit -F - <<'EOF'
feat(uspc): resolve async operations via OperationComplete + command_key

The same command_key correlation CWMP's TransferComplete already uses
(design S6.3), with the one thing USP lacks that CWMP has: a
per-request credential. The substitute is the identity B-3a already
establishes per connection -- a command_key resolution is refused, not
trusted, unless the reporting connection's own reconciled device_id
matches the job it claims to complete.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
```

---

### Task 6: Error mapping — USP 7xxx codes into job failure states

**Files:**
- Modify: `backend/cmd/uspc/dispatcher.go`, `backend/cmd/uspc/dispatcher_test.go`

**Interfaces:**
- Consumes: `usp.USPError`, `usp.ErrorFromMsg` (B-1, existing), the four pre-wired sentinels `usp.ErrNotWriteable` (7013), `usp.ErrObjectDoesNotExist` (7016), `usp.ErrPermissionDenied` (7006), `usp.ErrCommandFailure` (7022).

**Contract:**
- Task 4/5's basic `MarkFailed(job.ID, strconv.Itoa(int(uspErr.Code)), uspErr.Message)` becomes the default case of a small switch using `errors.Is` against the four sentinels, per design §6.4:
  - `errors.Is(err, usp.ErrNotWriteable)` → `MarkFailed` with a `FaultString` prefixed to make the class obvious in the operator UI (e.g. `"parameter is not writeable: " + uspErr.Message`) — this is the Huawei-class trap design §6.4 calls out, typed rather than silent, same intent as the CWMP passphrase fix in sub-project 0.
  - `errors.Is(err, usp.ErrObjectDoesNotExist)` → `MarkFailed`, and additionally trigger re-discovery (design §6.4 says so) — re-discovery here means queuing a `TypeParameterDiscovery` job for the same device, or, if that's too heavy a side effect for this plan's scope, logging a distinct message an operator/future automation can act on. **Decide and document which** — queuing a real follow-up job has a real cost (another RPC round trip, possibly on every failed Add/Delete against a stale path) and this plan should make a deliberate choice, not default to the heavier option by accident. Recommendation: log distinctly for now, do not auto-queue — re-discovery-on-demand is a reasonable operator/console feature for a later plan, and auto-queuing from inside error handling risks a retry storm if a device's data model is persistently stale.
  - `errors.Is(err, usp.ErrPermissionDenied)` → `MarkFailed` with wording that flags it as a controller-trust misconfiguration, not a device fault (design §6.4) — distinct log level (Warn, not Info) since this indicates an operational problem with the deployment, not a normal job failure.
  - `errors.Is(err, usp.ErrCommandFailure)` → `MarkFailed` with the agent's own `err_msg` verbatim (design §6.4 — `ErrCommandFailure`'s `uspErr.Message` already carries this via `ErrorFromMsg`, so this case may just be the same as the default, confirm and simplify if so rather than writing a redundant branch).
  - Every other/unmapped code: `MarkFailed` with code and message recorded verbatim (unchanged from Task 4/5's baseline).

**Checklist:**
| Requirement | Test |
|---|---|
| Each of the four special-cased codes produces the documented distinct handling | `TestErrorMappingNotWriteable`, `...ObjectDoesNotExist`, `...PermissionDenied`, `...CommandFailure` |
| An unmapped code falls through to the generic path, verbatim code+message | `TestErrorMappingUnmappedCode` |

- [ ] **Step 1: Write the failing tests**

One test per mapped code, constructing a `uspproto.Error` with the relevant code, running it through the dispatcher's failure path, and asserting the resulting `Job.FaultCode`/`FaultString` (and, for `ErrObjectDoesNotExist`, whatever re-discovery decision Step 3 below settles on — a distinct log line is enough to assert against if that's the chosen approach, via a test logger capturing output, matching how other tests in this codebase assert on log content if that pattern exists — check first).

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./cmd/uspc/ -run TestErrorMapping -v`. Expected: FAIL (generic handling doesn't distinguish the four codes yet).

- [ ] **Step 3: Implement**

The `errors.Is` switch per the Contract, with the `ErrObjectDoesNotExist` re-discovery decision made and documented in a code comment explaining the retry-storm reasoning above (or the opposite decision, if you judge the auto-queue approach is actually right for this codebase's existing retry/backoff discipline — check `internal/jobs`' existing retry semantics, e.g. `MaxAttempts`, `RecoverExpiredLeases`'s `nonRepeatableTypes` handling, before deciding; this plan's recommendation is not the only defensible answer, but *an* answer must be picked and justified).

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./cmd/uspc/... -v`. Expected: all PASS.

- [ ] **Step 5: Full backend checks, then commit**

```bash
git add cmd/uspc/dispatcher.go cmd/uspc/dispatcher_test.go
git commit -F - <<'EOF'
feat(uspc): distinguish the four operationally-significant USP error codes

Design S6.4's four codes get typed handling: a non-writeable parameter
is now diagnosable in the operator UI instead of a silent Huawei-class
failure (the same class of bug sub-project 0 fixed on the CWMP side);
a permission-denied is logged as a controller-trust misconfiguration,
not an ordinary job failure. Object-does-not-exist logs distinctly
rather than auto-queuing a rediscovery job, to avoid a retry storm
against a persistently stale data model.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
```

---

### Task 7: CI — prove a queued job actually dispatches to and resolves against real obuspa

**Files:**
- Modify: `.github/workflows/ci.yml`, `ci/usp/` assets as needed.

**Interfaces:**
- Consumes: B-2's existing `usp-interop` job structure (three MTP-variant steps, each starting `cmd/uspc` and obuspa); B-3a's Task 6 additions (the job already has a database and runs migrations).

**Contract:**
- After the existing probe/GetResp evidence in (at minimum) the WebSocket step — proving identity reconciliation already works, per B-3a — insert a real job for the now-identified device directly via SQL (or, if simpler and more representative of real usage, via `cmd/api`'s REST endpoint if the CI job is willing to also start `cmd/api`; use your judgment on which is more valuable evidence versus added CI complexity — a direct SQL insert against the `jobs` table is almost certainly the lower-risk, more targeted choice for proving *this plan's* dispatch path specifically, since going through `cmd/api` would also be testing unrelated REST-layer code) — a `TypeGetParameter` job targeting `Device.DeviceInfo.SoftwareVersion` is a safe, side-effect-free choice.
- Poll (reusing/extending `assert-getresp.sh`'s pattern, or a new small script) for the job reaching `status='SUCCESS'` in the database within a reasonable timeout, and assert `result_detail` contains the expected parameter.
- This is the concrete proof that LISTEN/NOTIFY, the type mapping, and sync-completion correlation all actually work against a real agent — not just against fakes, mirroring the project's standing rule that "protocol code that has never met a real agent is an intention, not a capability" (spec §9, already the philosophy behind B-2's whole `usp-interop` job).

**Checklist:**
| Requirement | Evidence |
|---|---|
| A job queued after the device is already connected dispatches via LISTEN/NOTIFY and resolves | new CI step assertion |
| `result_detail` contains real data from the real agent | same assertion, checking the actual value |
| YAML still parses | `python3 -c "import yaml;yaml.safe_load(open('.github/workflows/ci.yml'));print('YAML OK')"` |

- [ ] **Step 1: Design and add the job-insert + poll step**

Extend the WebSocket interop step (or add a new one immediately after it, sharing the same running `uspc`/obuspa pair rather than starting a fresh pair — cheaper and still real evidence) with the SQL insert and a poll loop.

- [ ] **Step 2: Validate YAML**

Run: `python3 -c "import yaml;yaml.safe_load(open('.github/workflows/ci.yml'));print('YAML OK')"`.

- [ ] **Step 3: Run locally if Docker is reachable**

Attempt the same sequence locally exactly as B-2's Task 6 did (Docker daemon availability was inconsistent across that plan's sessions — check `docker ps` first; if reachable, actually run it and record the output; if not, say so plainly and rely on CI as the first real execution, consistent with this programme's established, honest pattern for this exact situation).

- [ ] **Step 4: Commit**

```bash
git add .github/workflows/ci.yml ci/usp/
git commit -F - <<'EOF'
ci: prove a queued job dispatches to and resolves against real obuspa

Extends the existing usp-interop evidence (an agent connects and
answers a probe Get) with the thing this whole plan exists to build:
an operator-queued job reaches the agent via LISTEN/NOTIFY and
resolves with a real result, not a fake one.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
```

---

## Self-Review

**1. Spec coverage.**

| Spec section | Task |
|---|---|
| §6.1 push-not-pull, LISTEN/NOTIFY + periodic sweep safety net, connection registry lookup | 2, 4 |
| §6.2 job type mapping | 3, and the explicit scope decision for the five unmapped types |
| §6.3 sync/async, `command_key` correlation, no new correlation concept | 4 (sync), 5 (async) — deliberately without the GetSupportedDM cache the spec describes, per the Decisions section's reasoning |
| §6.4 error mapping, the four special-cased codes | 6 |
| Single-instance posture (implicit in §6.1) | assumed throughout; no multi-instance coordination is built, matching B-2/B-3a's same posture |

Deliberately not here: §7 subscriptions/Notify beyond `OperationComplete` and `OnBoardRequest` — B-3c.

**2. Placeholder scan.** No bare `TODO`/"implement later". The two places this plan explicitly defers a decision to the implementer (firmware/diagnostics command verification in Task 3; the `ErrObjectDoesNotExist` re-discovery choice in Task 6) are not placeholders — each names the decision, the tradeoff, and a recommendation, with an explicit instruction to document whichever way it's resolved. This mirrors how B-2 handled the obuspa pin (verify live, don't guess) and is the same discipline, not a lowering of it.

**3. Type consistency.** `LeaseForTypes`, `AddObjectPayload.Parameters`, `GetUspAgentByDeviceID` (Task 1) are consumed by name in Tasks 3/4. `buildUSPRequest`/`ErrUnsupportedOverUSP` (Task 3) are consumed by Task 4's `tryDispatch`. `dispatcher`/`pendingDispatch`/`handleResponse` (Task 4) are extended, not redefined, by Task 5's `handleOperationComplete` and Task 6's error-mapping switch. `usp.OperationComplete`/`DecodeOperationComplete` (Task 5) match B-3a Task 2's `OnBoardRequest`/`DecodeOnBoardRequest` naming convention exactly, deliberately, for a future reader's pattern-matching.

**Ordering.** 1 → 2 can proceed in parallel (no shared files) but both must land before 3 needs nothing from either and could run earlier, but 4 needs 1, 2, and 3 all landed. 4 → 5 → 6 strictly (5 extends 4's `handleResponse`/pending-map machinery; 6 extends 4/5's failure-handling call site). 7 needs everything. Sequential 1 → 2 → 3 → 4 → 5 → 6 → 7 is safe and simplest for a controller running this via subagent-driven-development.
