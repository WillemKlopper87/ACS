# USP Subscriptions and Notify Routing Implementation Plan (B-3c)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** An operator-desired set of USP subscriptions (which notifications a device should send, and for what) actually exists on the device, is restored automatically after a reboot or factory reset, and every notification type the device can send — not just `OnBoardRequest` and `OperationComplete` — lands somewhere useful: the parameter cache, a new device event log, or triggers identity/job-dispatch work already built.

**Architecture:** A new `usp_subscriptions` table holds desired state, one row per (device, notification type, reference list). `internal/subscriptions` is a small, protocol-agnostic diff engine (`Reconcile(desired, actual) (toAdd, toRemove)`) — the first generic reconciler in this codebase, built once so sub-project C can reuse it for provisioning. All the USP-specific glue — encoding the `Add`/`Delete` that convergence requires, decoding the device's actual `Subscription` instances, and routing the four remaining Notify types — lives in `cmd/uspc`, matching how `internal/usp`'s domain-free boundary and `cmd/uspc`'s wiring role have been kept separate throughout this programme.

**Tech Stack:** Go 1.26; existing `internal/usp`/`internal/parameters`/`internal/devices`/`internal/store` stack. No new dependency.

**Spec:** [`docs/superpowers/specs/2026-09-09-usp-controller-design.md`](../specs/2026-09-09-usp-controller-design.md) §7 (Subscriptions and Notify) in full, and the `Recipient`/`ID` correction to §7.1 recorded below. §5 (identity) and §6 (dispatch) are complete (B-3a, B-3b) and are only extended here at their existing hook points.

## Programme context

| Plan | Deliverable | Depends on |
|---|---|---|
| 0, A, B-1, B-2, B-3a, B-3b | Defect fixes, fleet data model, protocol core, transports, agent identity, job dispatch — all complete. | — |
| **B-3c** (this) | Subscription reconciliation; `ValueChange`/`ObjectCreation`/`ObjectDeletion`/`Event` Notify routing. | B-3a (identity), B-3b (the `msg_id`-pending pattern this plan's `Add`/`Delete` correlation reuses) |
| C | BSS improvements | A; may reuse `internal/subscriptions`' diff engine for provisioning |

After this plan, every Notify variant in USP 1.3 is handled and the controller half of §5–§7 is complete. The agent allowlist (§8) remains the one open item from the whole programme.

## Facts and decisions established during research (binding — do not re-derive)

**`cmd/uspc`'s current `OnRecord` chain** (`backend/cmd/uspc/handler.go:130-191`): `usp.DecodeRecord` → `usp.DecodeMsg` → `usp.DecodeOnBoardRequest` → `usp.DecodeOperationComplete` → `probe.handle` → `dispatcher.handleResponse`. Each `Decode*` call is narrow and mutually exclusive by construction (wrong oneof shape → a distinct sentinel error), so this plan's four new Notify checks insert between `DecodeOperationComplete`'s return and `probe.handle`, in the same "tried first" block the existing comment at `handler.go:167-172` describes — nothing downstream needs to change.

**Generated protobuf type names — verified against `backend/internal/usp/uspproto/usp-msg-1-3.pb.go`, do not guess:**
- `Notify_ValueChange{ParamPath, ParamValue string}` (oneof wrapper field `ValueChange`).
- `Notify_ObjectCreation{ObjPath string, UniqueKeys map[string]string}` (oneof wrapper field `ObjCreation` — **note the wrapper field is `ObjCreation` but the payload type is `Notify_ObjectCreation`**, unabbreviated; the same asymmetry holds for `ObjDeletion`/`Notify_ObjectDeletion`).
- `Notify_ObjectDeletion{ObjPath string}` (oneof wrapper field `ObjDeletion`).
- `Notify_Event{ObjPath, EventName string, Params map[string]string}` (oneof wrapper field `Event`).
- The top-level `Notify` struct carries `SubscriptionId string`, `SendResp bool` regardless of variant — read via `notify.GetSubscriptionId()`/`GetSendResp()`, exactly as `DecodeOnBoardRequest`/`DecodeOperationComplete` already do.
- `Header.GetMsgId() string` exists on every `Msg`, including a `Notify` — this plan's dedup key uses it.

**No subscription-specific USP message exists.** `uspproto.Header_MsgType`'s full enum has no `SUBSCRIBE`/`UNSUBSCRIBE`. Subscriptions are ordinary `Device.LocalAgent.Subscription.{i}.` object-instance CRUD, managed with the `EncodeAdd`/`EncodeDelete`/`EncodeGetInstances`/`EncodeGet` functions B-1 already built — no new encoder is needed.

**`Device.LocalAgent.Subscription.{i}.` parameters, verified against the real TR-181 2.18.1 XML — two corrections to the spec's own §7.1 wording:**
| Parameter | Access | Use in this plan |
|---|---|---|
| `ID` | readWrite | **Correction 1: this *is* the subscription id carried on every Notify from this subscription — there is no separate `SubscriptionID` parameter.** The controller sets it at creation time. |
| `Enable` | readWrite | Always `"true"` — this plan creates only enabled subscriptions. |
| `NotifType` | readWrite | **Correction 2: the real name is `NotifType`, not `NotificationType`.** Enum: `ValueChange`/`ObjectCreation`/`ObjectDeletion`/`OperationComplete`/`Event`. |
| `ReferenceList` | readWrite | Comma-separated list of paths. **Immutable once created** — TR-181 gives no in-place update; changing it means delete-then-recreate, which shapes how the reconciler diffs (see Decisions below). |
| `Persistent` | readWrite | Survives an agent restart if `true`. |
| `Recipient` | **readOnly** | Agent-populated. A controller `Add` must never attempt to set this. |
| `TriggerAction`, `TriggerConfigSettings` (v2.16+), `NotifRetry`, `NotifExpiration` | readWrite | Out of scope for this plan (spec §7.1 only models notification type, reference path, persistence, and creating operator) — never set, left at agent defaults. |

**Reusable pattern for `ValueChange`**: `internal/parameters.Repository.Upsert(ctx, deviceID string, values map[string]CachedValue) error` (`internal/parameters/cache.go:51`) already does exactly the diff-and-insert-on-change work a `ValueChange` Notify needs — one call with a one-entry map, `Source` set to a new constant distinguishing it from `SourceInform`/`SourceGetValues`. It is also naturally idempotent for at-least-once delivery: writing the same value twice inserts no duplicate `parameter_history` row.

**No cache-invalidation method exists yet.** `internal/parameters.Repository` has `Upsert` and `History` only — `ObjectCreation`/`ObjectDeletion` need a new `InvalidateSubtree(ctx, deviceID, objPath string) error` that removes every cached key with that prefix from `device_parameter_cache.parameters`, under the same row-lock discipline `Upsert` already uses (read `cache.go`'s transaction shape before writing this).

**No device event store exists.** Nothing in `backend/` stores a general per-device event stream; `audit_log` is actor/action-centric (operator/session actions), not device-event-shaped. This plan adds `device_events`, modeled directly on `parameter_history`'s insert-only shape, as a new file on the existing `devices.Repository` (the package that already owns device-scoped historical concerns) rather than a new one-file package.

**No generic reconciliation engine exists.** Every "reconcil" hit in the codebase today is B-3a's identity reconciliation (agent↔`devices`-row matching) — a different meaning of the word. This plan's `internal/subscriptions` diff engine is genuinely new architecture, not a reuse.

**`usp_agents` has no subscription-tracking column** (`internal/store/migrations/0053_usp_agents.sql`) — desired *and* believed-actual subscription state are both new for this plan, living in `usp_subscriptions`.

**Decision — the correlation key between a desired-state row and the wire.** Rather than reading back an agent-assigned id, the controller assigns `Device.LocalAgent.Subscription.{i}.ID` to the exact string form of `usp_subscriptions.id` (a UUID) at creation time. One canonical identifier, both sides, chosen once and never re-derived — the alternative (matching by content, or reading the agent's own numbering) is unnecessary complexity this plan doesn't need.

**Decision — reconciliation trigger.** One hook: after `handler.resolveAndMarkReconciled` succeeds (the same extension point B-3b used for `dispatcher.tryDispatch`), call `subscriptionReconciler.reconcile(ctx, deviceID, conn)`. No periodic sweep for subscriptions in this plan — unlike job dispatch, a subscription drift is only introduced by an agent-side event (reboot, factory reset) that this plan's own on-connect hook already catches at the moment it matters; a sweep would only add complexity for a case that doesn't occur between connects.

**Decision — diverging `ReferenceList`/`NotifType` triggers delete-then-recreate, not `Set`.** Since `ReferenceList` is immutable per TR-181, the diff engine treats a desired row whose `(NotifType, ReferenceList)` doesn't match any actual instance as **both** "remove the stale actual instance if one exists for that id" and "add the desired one" — never a `Set`. `Persistent`/`Enable` changes, if ever needed, are out of scope for this plan (every subscription this plan creates is `Enable=true`, `Persistent` fixed at creation — no in-place mutation path is built).

**Decision — unknown `subscription_id` triggers a reconcile pass, not an error** (spec §7.3, previously unimplemented — nothing validated `subscription_id` before this plan). Every Notify handler that needs identity resolves the reporting connection's `device_id` (`handler.reconciledDeviceID`, the same call B-3b's `OperationComplete` handling already uses) and checks the Notify's `subscription_id` against `usp_subscriptions` for that device; no match → log at Info and call the same on-connect reconciler, don't fail the Notify.

**Decision — idempotency for `ObjectCreation`/`ObjectDeletion`/`Event`.** `ValueChange` is naturally idempotent via `Upsert`. The other three aren't (each call would insert a new `device_events` row for an at-least-once redelivery) — `device_events` gets a `UNIQUE (device_id, msg_id)` constraint, and the insert uses `ON CONFLICT (device_id, msg_id) DO NOTHING`, keyed on the Notify's own `Header.MsgId` (present on every `Msg`, unique per send — an agent redelivering the identical Notify after a missed `NotifyResp` uses the same `msg_id`, but note this is a per-delivery-attempt key, not a per-fact key; document this precisely rather than overclaiming stronger dedup).

## Global Constraints

- Go module `acs`; directive `go 1.26.6`. Do not raise it.
- No new dependencies.
- `internal/usp/...` still may not import any domain package — B-1's walk-based boundary test enforces this over the whole tree and is untouched by this plan. The four new `Decode*` functions are pure protocol code, exactly like `DecodeOnBoardRequest`.
- `internal/subscriptions` (new) must not import `internal/usp` or any USP-specific type — it is the generic diff engine sub-project C is meant to reuse; USP-specific encoding/decoding stays in `cmd/uspc`.
- `cmd/uspc`'s boundary test currently forbids nothing (`forbiddenPrefixes = []string{}`, B-3b) — this plan adds `internal/subscriptions` and `internal/parameters` as new imports; no boundary change needed, but the boundary test's self-check (`TestForbiddenPrefixesIsCurrentlyEmpty`) must still pass unchanged.
- Every migration is forward-only and checksum-verified at boot — never edit a committed migration.
- Before every commit, from `backend/`: `gofmt -l .` prints nothing, `go vet ./...` and `go test ./...` pass.
- DB-backed tests use `ACS_TEST_POSTGRES_DSN`, matching every existing DB-backed test in this codebase; `ACS_TEST_POSTGRES_DSN=postgres://acs:acs@127.0.0.1:5432/acs?sslmode=disable` reaches this session's local Postgres.
- `./cmd/uspc/` is already on CI's DB-backed test package list (B-3b's final fix) — new DB-backed tests in that package run in CI automatically; this plan's `internal/subscriptions` and `internal/devices`/`internal/parameters` additions need the same check sub-project A's plan established: confirm each new DB-backed test file's package is on that list, and if `internal/subscriptions` is new, add it.
- Commit message style: `type(scope): summary`, imperative mood. End each commit message with:
  `Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>`

## File structure

| File | Responsibility |
|---|---|
| `backend/internal/store/migrations/0055_usp_subscriptions.sql` | `usp_subscriptions` (desired state) and `device_events` (append-only). |
| `backend/internal/usp/message.go` (modify) | `DecodeValueChange`, `DecodeObjectCreation`, `DecodeObjectDeletion`, `DecodeEvent`. |
| `backend/internal/parameters/cache.go` (modify) | `InvalidateSubtree`; a new `SourceUSPNotify` constant. |
| `backend/internal/devices/events.go` | `RecordEvent`, `Events` on the existing `devices.Repository`. |
| `backend/internal/subscriptions/reconcile.go` | The generic `Reconcile(desired, actual []Item) (toAdd, toRemove []Item)` diff engine — pure, no I/O. |
| `backend/internal/subscriptions/repository.go` | `usp_subscriptions` CRUD: `Repository`, `Create`, `ByDevice`, `Delete`. |
| `backend/cmd/uspc/subscriptions.go` | USP-specific glue: builds `Item`s from `usp_subscriptions` rows and from a decoded `Device.LocalAgent.Subscription.` `GetResp`; encodes the `Add`/`Delete` convergence; the reconciler's pending-response tracking (mirrors `dispatcher.go`'s `pending` map). |
| `backend/cmd/uspc/handler.go` (modify) | Four new `OnRecord` branches; the reconcile-on-connect hook. |
| `backend/cmd/uspc/main.go` (modify) | Wires `subscriptions.Repository`, `parameters.Repository`, the subscription reconciler into `run`. |
| `ci/usp/` (modify) | Prove subscription reconciliation and at least one routed Notify against real obuspa. |

---

### Task 1: Migration — `usp_subscriptions` and `device_events`

**Files:**
- Create: `backend/internal/store/migrations/0055_usp_subscriptions.sql`
- Create: `backend/internal/store/migration_0055_test.go`

**Interfaces:**
- Consumes: the migration harness pattern from `backend/internal/store/migration_0053_test.go`/`0054_test.go` — read one for the exact DSN-skip/setup idiom before writing this task's test.
- Produces: tables `usp_subscriptions`, `device_events`.

**Contract:**

```sql
CREATE TABLE usp_subscriptions (
    id              UUID PRIMARY KEY,
    device_id       UUID NOT NULL REFERENCES devices(id),
    notif_type      TEXT NOT NULL CHECK (notif_type IN ('ValueChange','ObjectCreation','ObjectDeletion','OperationComplete','Event')),
    reference_list  TEXT[] NOT NULL,
    persistent      BOOLEAN NOT NULL DEFAULT false,
    created_by      TEXT NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX usp_subscriptions_device_idx ON usp_subscriptions (device_id);

CREATE TABLE device_events (
    id           BIGSERIAL PRIMARY KEY,
    device_id    UUID NOT NULL REFERENCES devices(id),
    msg_id       TEXT NOT NULL,
    obj_path     TEXT NOT NULL,
    event_name   TEXT NOT NULL,
    params       JSONB NOT NULL DEFAULT '{}',
    recorded_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (device_id, msg_id)
);

CREATE INDEX device_events_device_idx ON device_events (device_id, recorded_at DESC);
```

- `usp_subscriptions.id` is the UUID the controller writes verbatim into `Device.LocalAgent.Subscription.{i}.ID` — no separate wire-id column.
- `reference_list TEXT[]` (not a comma-joined string) — join at USP-encode time (Task 4), so SQL queries/tests can inspect individual paths.
- No `unassigned_at`/history semantics — this table is desired state only, matching §7.1; there is no requirement in this plan to preserve a subscription's own history, only whether it currently should exist.
- `device_events.msg_id` + the `UNIQUE (device_id, msg_id)` constraint is the idempotency mechanism the Decisions section specifies — document this exact rationale in the migration's own comment (a future reader must know this is a per-delivery-attempt dedup, not a per-fact dedup).
- `ObjectCreation`/`ObjectDeletion` and `Event` all land in `device_events` — `obj_path`/`event_name` cover all three (`ObjectCreation`/`ObjectDeletion` set `event_name` to a fixed literal, e.g. `"ObjectCreation"`/`"ObjectDeletion"`, with `params` empty or carrying `UniqueKeys` for creation; `Event` sets `event_name` to the Notify's own `EventName` and `params` to its `Params` map — this is Task 5's job to populate correctly, Task 1 only needs the schema to support it).

**Checklist:**
| Requirement | Test |
|---|---|
| Migration applies to a clean DB | `TestMigration0055AppliesCleanly` |
| `usp_subscriptions.notif_type` CHECK rejects a bad value | `TestMigration0055NotifTypeCheck` |
| `device_events` `UNIQUE (device_id, msg_id)` — a duplicate insert with `ON CONFLICT DO NOTHING` is a no-op, not an error | `TestMigration0055DeviceEventsDedup` |

- [ ] **Step 1: Write the failing tests**

Follow `migration_0053_test.go`'s exact structure. `TestMigration0055DeviceEventsDedup`: insert a device, insert one `device_events` row, then `INSERT ... ON CONFLICT (device_id, msg_id) DO NOTHING` the identical `(device_id, msg_id)` with different other columns, assert exactly one row exists and it still has the *first* insert's values (proving the conflict really was a no-op, not an overwrite).

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/store/ -run TestMigration0055 -v`. Expected: FAIL — migration doesn't exist.

- [ ] **Step 3: Write the migration**

Exactly per the Contract.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/store/ -run TestMigration0055 -v`. Expected: PASS.

- [ ] **Step 5: Full backend checks, then commit**

```bash
git add internal/store/migrations/0055_usp_subscriptions.sql internal/store/migration_0055_test.go
git commit -F - <<'EOF'
feat(store): add usp_subscriptions and device_events

usp_subscriptions is desired state only (S7.1) -- the controller's own
UUID is written verbatim into the agent's Subscription.{i}.ID, so
there is no separate wire-id to correlate. device_events is the
per-device event stream nothing in this codebase has today; its
UNIQUE(device_id, msg_id) is a per-delivery-attempt dedup for USP's
at-least-once Notify semantics, not a per-fact dedup.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
```

---

### Task 2: `internal/usp` — decode the four remaining Notify variants

**Files:**
- Modify: `backend/internal/usp/message.go`
- Modify: `backend/internal/usp/message_test.go`

**Interfaces:**
- Consumes: `uspproto.Notify_ValueChange{ParamPath, ParamValue string}`, `uspproto.Notify_ObjectCreation{ObjPath string, UniqueKeys map[string]string}`, `uspproto.Notify_ObjectDeletion{ObjPath string}`, `uspproto.Notify_Event{ObjPath, EventName string, Params map[string]string}`, and their oneof wrapper types `uspproto.Notify_ValueChange_{ValueChange *Notify_ValueChange}` / `Notify_ObjCreation{ObjCreation *Notify_ObjectCreation}` / `Notify_ObjDeletion{ObjDeletion *Notify_ObjectDeletion}` / `Notify_Event_{Event *Notify_Event}` — confirm every one of these exact names against `uspproto/usp-msg-1-3.pb.go` before writing code; the wrapper-vs-payload naming asymmetry noted in Decisions above is real and easy to get wrong.
- Produces:
  - `type ValueChange struct { SubscriptionID string; SendResp bool; ParamPath, ParamValue string }`, `var ErrNotValueChange = errors.New(...)`, `func DecodeValueChange(msg *uspproto.Msg) (*ValueChange, error)`.
  - `type ObjectCreation struct { SubscriptionID string; SendResp bool; ObjPath string; UniqueKeys map[string]string }`, `var ErrNotObjectCreation = errors.New(...)`, `func DecodeObjectCreation(msg *uspproto.Msg) (*ObjectCreation, error)`.
  - `type ObjectDeletion struct { SubscriptionID string; SendResp bool; ObjPath string }`, `var ErrNotObjectDeletion = errors.New(...)`, `func DecodeObjectDeletion(msg *uspproto.Msg) (*ObjectDeletion, error)`.
  - `type Event struct { SubscriptionID string; SendResp bool; ObjPath, EventName string; Params map[string]string }`, `var ErrNotEvent = errors.New(...)`, `func DecodeEvent(msg *uspproto.Msg) (*Event, error)`.

**Contract:**
- All four follow `DecodeOnBoardRequest`'s exact structure (`message.go:212-240`, read it in full and copy the shape precisely): reject non-`NOTIFY` header type and reject any other `Notification` oneof variant, both via the function's own sentinel error; on success, populate `SubscriptionID`/`SendResp` from the outer `Notify` message (`notify.GetSubscriptionId()`/`GetSendResp()`) plus the variant-specific fields via nil-safe generated getters.
- Each must not panic on a nil inner payload (e.g. `Notify_ValueChange_{ValueChange: nil}`) — treat as the zero value for that variant's fields, matching `DecodeOperationComplete`'s nil-safety discipline for its own nested oneof (B-3a's Task 2 precedent).

**Checklist:**
| Requirement | Test |
|---|---|
| Each decoder decodes a well-formed Notify of its own type correctly | `TestDecodeValueChange`, `TestDecodeObjectCreation`, `TestDecodeObjectDeletion`, `TestDecodeEvent` |
| Each rejects every other Notify variant (including the other three new ones and the two existing ones) distinctly via its own sentinel | one subtest per decoder iterating the other five variants |
| Each rejects a non-`NOTIFY` message type without panicking | one subtest per decoder |
| Nil inner payload doesn't panic | one subtest per decoder |

- [ ] **Step 1: Write the failing tests**

Mirror `TestDecodeOnBoardRequest`'s hand-built-`Msg` construction pattern for all four, including the "reject every other variant" loop (five other cases available now — `OnBoardReq`, `OperComplete`, and the three siblings among the four new ones).

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/usp/ -run 'TestDecodeValueChange|TestDecodeObjectCreation|TestDecodeObjectDeletion|TestDecodeEvent' -v`. Expected: compile FAIL.

- [ ] **Step 3: Implement**

Four functions in `message.go`, each ~15 lines, following `DecodeOnBoardRequest`'s exact shape.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/usp/... -v`. Expected: all PASS, full existing suite unaffected.

- [ ] **Step 5: Full backend checks, then commit**

```bash
git add internal/usp/message.go internal/usp/message_test.go
git commit -F - <<'EOF'
feat(usp): decode ValueChange, ObjectCreation, ObjectDeletion, Event Notifies

The last four of USP's six Notify variants (OnBoardRequest and
OperationComplete were B-3a/B-3b). Same narrow, mutually-exclusive
shape as the existing two -- a caller tries each in turn and only one
can ever match a given Notify.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
```

---

### Task 3: `internal/subscriptions` — the generic diff engine and the desired-state repository

**Files:**
- Create: `backend/internal/subscriptions/reconcile.go`, `backend/internal/subscriptions/reconcile_test.go`
- Create: `backend/internal/subscriptions/repository.go`, `backend/internal/subscriptions/repository_test.go`

**Interfaces:**
- Consumes: nothing outside the standard library and `internal/store` (for `Repository`'s `*sql.DB`) — this package must not import `internal/usp` or any USP type (Global Constraint).
- Produces:
  - `type Item struct { Key string; Fingerprint string }` — `Key` identifies *what* (e.g. a subscription's `ID`), `Fingerprint` identifies *the desired/actual shape* (e.g. `NotifType + "|" + strings.Join(ReferenceList, ",")`) so the engine can tell "same key, different content" apart from "same key, same content" without knowing what a subscription or any other domain concept is.
  - `func Reconcile(desired, actual []Item) (toAdd, toRemove []Item)` — pure function, no I/O. `toAdd` = every desired item whose key is missing from actual, or present with a different fingerprint (paired with removing the stale actual one — see below). `toRemove` = every actual item whose key is missing from desired, or present with a different fingerprint than desired.
  - `type Subscription struct { ID string; DeviceID string; NotifType string; ReferenceList []string; Persistent bool; CreatedBy string; CreatedAt time.Time }`
  - `type Repository struct { ... }`, `func NewRepository(db *sql.DB) *Repository`
  - `func (r *Repository) Create(ctx context.Context, sub Subscription) error`
  - `func (r *Repository) ByDevice(ctx context.Context, deviceID string) ([]Subscription, error)`
  - `func (r *Repository) Delete(ctx context.Context, id string) error`

**Contract:**
- `Reconcile`'s "different fingerprint" case returns the item in **both** `toRemove` (the stale actual) and `toAdd` (the desired replacement) — this is what Decisions' "delete-then-recreate" choice requires; the caller (Task 4) is responsible for sequencing remove-before-add when it actually executes these against a device, `Reconcile` itself doesn't order operations, it only classifies.
- A `desired` item with no matching `actual` key is `toAdd` only. An `actual` item with no matching `desired` key is `toRemove` only.
- Two `desired`/`actual` items with the same key and the same fingerprint appear in neither list — already converged.
- `Reconcile` makes no assumption about what `Key`/`Fingerprint` mean — this is what keeps it reusable outside USP subscriptions.
- `Repository.Create`/`ByDevice`/`Delete` are a thin, ordinary CRUD layer over `usp_subscriptions` — no business logic; the diff/decision logic is entirely in `Reconcile` and Task 4's USP-specific glue, not here.

**Checklist:**
| Requirement | Test |
|---|---|
| Desired-only item → `toAdd`, not `toRemove` | `TestReconcileAddsMissingDesired` |
| Actual-only item → `toRemove`, not `toAdd` | `TestReconcileRemovesUndesiredActual` |
| Same key, same fingerprint → neither list | `TestReconcileConvergedItemUntouched` |
| Same key, different fingerprint → in both `toAdd` and `toRemove` | `TestReconcileFingerprintMismatchRecreates` |
| Empty desired + empty actual → both nil/empty | `TestReconcileBothEmpty` |
| `Repository.Create`/`ByDevice`/`Delete` round-trip against a live DB | `TestRepositoryCreateByDeviceDelete` |

- [ ] **Step 1: Write the failing tests**

`reconcile_test.go`: pure unit tests, table-driven, covering every Checklist row with literal `Item` slices — no DB needed for this file.

`repository_test.go`: DB-backed, following the established `newDevicesTestRepo`-style harness pattern from `internal/devices/repository_test.go` — seed a device, `Create` two subscriptions, `ByDevice` returns both, `Delete` one, `ByDevice` returns one.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/subscriptions/ -v`. Expected: compile FAIL — package doesn't exist.

- [ ] **Step 3: Implement**

`reconcile.go` (pure), `repository.go` (DB-backed, mirroring `internal/devices/usp_agents.go`'s query style — parameterized SQL, no ORM).

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/subscriptions/... -v`. Expected: all PASS.

- [ ] **Step 5: Confirm the boundary constraint**

`go list -deps ./internal/subscriptions/ | grep '^acs/'` must show only `acs/internal/store` (and stdlib-adjacent) — no `acs/internal/usp`. Record the output in your report.

- [ ] **Step 6: Full backend checks, then commit**

```bash
git add internal/subscriptions/
git commit -F - <<'EOF'
feat(subscriptions): generic desired-vs-actual diff engine

Reconcile knows nothing about USP or subscriptions -- Key/Fingerprint
are opaque strings, so the same function can back sub-project C's
provisioning reconciliation later. A fingerprint mismatch classifies
as both toRemove and toAdd; sequencing (remove before add) is the
caller's job since ReferenceList is immutable on the wire and a
diverged subscription must be recreated, not updated in place.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
```

---

### Task 4: `internal/parameters`/`internal/devices` — invalidation and the event log

**Files:**
- Modify: `backend/internal/parameters/cache.go`, `backend/internal/parameters/cache_test.go` (or the existing test file for this package — check its actual name)
- Create: `backend/internal/devices/events.go`, `backend/internal/devices/events_test.go`

**Interfaces:**
- Consumes: `internal/parameters.Repository`'s existing transaction pattern (`cache.go:56-104` — `FOR UPDATE`, JSONB read-modify-write) as the model for `InvalidateSubtree`'s own transaction.
- Produces:
  - `const SourceUSPNotify = "USP_NOTIFY_VALUE_CHANGE"` (or whatever exact constant name/value fits the existing `SourceInform`/`SourceGetValues` naming convention in `cache.go` — match it).
  - `func (r *parameters.Repository) InvalidateSubtree(ctx context.Context, deviceID, objPath string) error` — removes every key in `device_parameter_cache.parameters` whose name has `objPath` as a prefix, under the same row lock `Upsert` uses. Does **not** touch `parameter_history` (history is a record of what was true, not a live cache — nothing to invalidate there).
  - `func (r *devices.Repository) RecordEvent(ctx context.Context, deviceID, msgID, objPath, eventName string, params map[string]string) error` — `INSERT INTO device_events (...) VALUES (...) ON CONFLICT (device_id, msg_id) DO NOTHING`.
  - `func (r *devices.Repository) Events(ctx context.Context, deviceID string, limit int) ([]DeviceEvent, error)` — `DeviceEvent{ID int64; DeviceID, MsgID, ObjPath, EventName string; Params map[string]string; RecordedAt time.Time}`, ordered `recorded_at DESC`, matching `parameters.Repository.History`'s own ordering/limit convention.

**Contract:**
- `InvalidateSubtree` with no matching keys is a no-op, not an error.
- `RecordEvent`'s `ON CONFLICT DO NOTHING` must be provably a no-op on a duplicate `(device_id, msg_id)` — same requirement as Task 1's migration test, now exercised through the Go method.

**Checklist:**
| Requirement | Test |
|---|---|
| `InvalidateSubtree` removes only keys under the given prefix, leaves siblings | `TestInvalidateSubtreeRemovesOnlyMatchingKeys` |
| `InvalidateSubtree` on an empty cache is a no-op | `TestInvalidateSubtreeEmptyCacheNoop` |
| `RecordEvent` + `Events` round-trip, ordering | `TestRecordEventAndEvents` |
| `RecordEvent` on a duplicate `(device_id, msg_id)` is a no-op, first insert's values survive | `TestRecordEventDedup` |

- [ ] **Step 1: Write the failing tests**

Both DB-backed, following each package's existing test harness exactly.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/parameters/ ./internal/devices/ -run 'TestInvalidateSubtree|TestRecordEvent' -v`. Expected: compile FAIL.

- [ ] **Step 3: Implement**

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/parameters/... ./internal/devices/... -v`. Expected: all PASS.

- [ ] **Step 5: Full backend checks, then commit**

```bash
git add internal/parameters/cache.go internal/parameters/cache_test.go internal/devices/events.go internal/devices/events_test.go
git commit -F - <<'EOF'
feat(parameters,devices): subtree cache invalidation and the device event log

InvalidateSubtree is what an ObjectCreation/ObjectDeletion Notify
needs -- the cache is stale under that path, not globally. RecordEvent
gives ObjectCreation/ObjectDeletion/Event somewhere to land; its
ON CONFLICT DO NOTHING is device_events' per-delivery-attempt dedup
(migration 0055), not a per-fact one.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
```

---

### Task 5: `cmd/uspc/subscriptions.go` — the USP-specific reconciler

**Files:**
- Create: `backend/cmd/uspc/subscriptions.go`, `backend/cmd/uspc/subscriptions_test.go`

**Interfaces:**
- Consumes: `subscriptions.Item`/`Reconcile`/`Repository`/`Subscription` (Task 3); `usp.EncodeAdd`/`EncodeDelete`/`EncodeGetInstances`/`EncodeGet`/`DecodeMsg` (B-1); `mtp.Conn`; `usp.NewMsgID`; the `dbCallTimeout`/`sendTimeout` constants (`handler.go`/`probe.go`).
- Produces:
  - `type subscriptionReconciler struct { repo *subscriptions.Repository; controllerID usp.EndpointID; log *slog.Logger; pending map[string]pendingSubscribe; mu sync.Mutex }` (mirrors `dispatcher`'s/`probe`'s established `pending`-map-plus-mutex shape).
  - `func newSubscriptionReconciler(repo *subscriptions.Repository, controllerID usp.EndpointID, log *slog.Logger) *subscriptionReconciler`
  - `func (s *subscriptionReconciler) reconcile(ctx context.Context, deviceID string, conn mtp.Conn) error` — the on-connect entry point: `repo.ByDevice(ctx, deviceID)` → build `desired []subscriptions.Item` (one per row, `Key = row.ID`, `Fingerprint = row.NotifType + "|" + strings.Join(row.ReferenceList, ",")`); send `usp.EncodeGetInstances(msgID, []string{"Device.LocalAgent.Subscription."}, false)` to read actual instances (or `EncodeGet` if that's simpler given what's already available — your call which primitive is cleaner, but it must actually retrieve each instance's `ID`, `NotifType`, `ReferenceList` to build `actual []subscriptions.Item`); on the response (handled via this reconciler's own `pending` map, keyed by `msgID`, matching `dispatcher.go`'s pattern precisely), call `subscriptions.Reconcile(desired, actual)`, then for each `toRemove` send `usp.EncodeDelete(msgID, true, []string{"Device.LocalAgent.Subscription." + item.Key + "."})`, and for each `toAdd` send `usp.EncodeAdd(msgID, true, "Device.LocalAgent.Subscription.", map[string]string{"ID": item.Key, "Enable": "true", "NotifType": <notif_type from the row>, "ReferenceList": <comma-joined>})` — each of these follow-up sends also registers its own `pending` entry so their `AddResp`/`DeleteResp` can be logged (success/failure), but **do not** need to chain further reconciliation on their own completion — one reconcile pass per connect is this plan's whole contract, not a retry loop.
  - `func (s *subscriptionReconciler) handleResponse(from usp.EndpointID, msg *uspproto.Msg) (matched bool) error` — the `msg_id`-correlated response handler `handler.OnRecord`'s fallthrough chain calls, mirroring `dispatcher.handleResponse`'s shape (delete-on-match, log the outcome, no job/dispatch semantics since this isn't job-queue-backed).

**Contract:**
- This is a **separate** pending map from `dispatcher.go`'s — subscription reconciliation isn't a job, it has no `command_key`, no `jobs.Job`, no `MarkSuccess`/`MarkFailed`. Do not try to route it through the existing `dispatcher` type; a small dedicated type is correct here (the brief's Produces section above already reflects this).
- `reconcile`'s outbound sends are bounded by `sendTimeout`; the whole `reconcile` call (matching B-3b's `resolveAndMarkReconciled`→`tryDispatch` precedent) is invoked with a single `dbCallTimeout`-sized budget from its caller (Task 6), not chained unbounded calls — read `resolveAndMarkReconciled`'s existing pattern in `handler.go` before wiring this.
- Building the `ReferenceList` comma-join and parsing it back on the actual-instance-read side must be exact inverses — write one small tested helper both directions use, don't duplicate the join/split logic.

**Checklist:**
| Requirement | Test |
|---|---|
| `reconcile` with no desired subscriptions and no actual ones sends nothing beyond the read | `TestReconcileNothingToConverge` |
| `reconcile` with a desired subscription missing from actual sends the right `Add` | `TestReconcileSendsAddForMissingDesired` |
| `reconcile` with an actual subscription not in desired sends the right `Delete` | `TestReconcileSendsDeleteForUndesiredActual` |
| A fingerprint mismatch sends both `Delete` (old) and `Add` (new), delete ordered before add | `TestReconcileRecreatesOnFingerprintMismatch` |
| `handleResponse` on an unknown `msg_id` is a no-op | `TestSubscriptionHandleResponseUnknownMsgID` |

- [ ] **Step 1: Write the failing tests**

Use a fake `mtp.Conn` (reuse the established `captureConn`/`fakeConn` pattern from `probe_test.go`/`dispatcher_test.go`) and a real DB-backed `subscriptions.Repository` (matching Task 3's own test convention) seeded with known rows; assert on the bytes actually sent (`usp.DecodeMsg` them back and check fields), not just "no error".

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./cmd/uspc/ -run TestReconcile -v`. Expected: compile FAIL.

- [ ] **Step 3: Implement**

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./cmd/uspc/... -v`. Expected: all PASS.

- [ ] **Step 5: Full backend checks, then commit**

```bash
git add cmd/uspc/subscriptions.go cmd/uspc/subscriptions_test.go
git commit -F - <<'EOF'
feat(uspc): subscription reconciliation on connect

Reads the agent's actual Subscription instances, diffs against
usp_subscriptions via the generic Reconcile engine, and issues
Delete-then-Add to converge -- ReferenceList is immutable on the wire,
so a changed subscription is always recreated, never Set. A separate
pending map from dispatcher.go's: this isn't a job, it has no
command_key and nothing to mark success/failed.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
```

---

### Task 6: `cmd/uspc/handler.go`/`main.go` — wire the four Notify routes and the reconcile-on-connect hook

**Files:**
- Modify: `backend/cmd/uspc/handler.go`, `backend/cmd/uspc/handler_test.go`
- Modify: `backend/cmd/uspc/main.go`

**Interfaces:**
- Consumes: `usp.DecodeValueChange`/`DecodeObjectCreation`/`DecodeObjectDeletion`/`DecodeEvent` (Task 2); `parameters.Repository.Upsert`/`InvalidateSubtree` (Task 4); `devices.Repository.RecordEvent` (Task 4); `subscriptionReconciler.reconcile`/`handleResponse` (Task 5); `handler.reconciledDeviceID` (existing, B-3a); `handler.resolveAndMarkReconciled` (existing, extend exactly as B-3b extended it for `dispatcher.tryDispatch`).

**Contract:**
- `OnRecord`'s fallthrough chain gains four more links, in the same "tried first" block, after `DecodeOperationComplete`'s return and before `probe.handle`:
  ```go
  if vc, err := usp.DecodeValueChange(msg); err == nil {
      h.handleValueChange(in.Conn, vc)
      return
  }
  if oc, err := usp.DecodeObjectCreation(msg); err == nil {
      h.handleObjectCreation(in.Conn, oc)
      return
  }
  if od, err := usp.DecodeObjectDeletion(msg); err == nil {
      h.handleObjectDeletion(in.Conn, od)
      return
  }
  if ev, err := usp.DecodeEvent(msg); err == nil {
      h.handleEvent(in.Conn, ev)
      return
  }
  ```
  (Order among these four doesn't matter — they're mutually exclusive by construction, same as the existing two.)
- Each new handler: resolve `deviceID, ok := h.reconciledDeviceID(in.Conn)` — an unreconciled connection sending any of these is logged at Warn and dropped (a Notify from a connection whose identity was never established has nowhere to attribute the data). Validate `subscription_id` against `usp_subscriptions` for that device (Decisions' "unknown subscription_id" rule) — no match → log at Info, call `subscriptionReconciler.reconcile` for this device (fire-and-forget the same way, one `dbCallTimeout`-bounded call), and still process the Notify's content (an unknown subscription id doesn't mean the data is wrong, just that ACS's bookkeeping drifted — the design spec says log-and-reconcile, not discard).
- `handleValueChange`: `parameters.Repository.Upsert(ctx, deviceID, map[string]parameters.CachedValue{vc.ParamPath: {Value: vc.ParamValue, Source: parameters.SourceUSPNotify, UpdatedAt: time.Now()}})`.
- `handleObjectCreation`/`handleObjectDeletion`: `parameters.Repository.InvalidateSubtree(ctx, deviceID, oc.ObjPath)` (or `od.ObjPath`), then `devices.Repository.RecordEvent(ctx, deviceID, msg.GetHeader().GetMsgId(), objPath, "ObjectCreation" or "ObjectDeletion", <UniqueKeys for creation, nil for deletion>)`.
- `handleEvent`: `devices.Repository.RecordEvent(ctx, deviceID, msg.GetHeader().GetMsgId(), ev.ObjPath, ev.EventName, ev.Params)`.
- Every handler that has `SendResp == true` sends a `NotifyResp` exactly as `handleOnBoardRequest`/`handleOperationComplete` already do — reuse that shape verbatim (`usp.EncodeNotifyResp`/`usp.EncodeRecord`/`sendTimeout`-bounded send), don't reimplement it a third time; factor it into one small shared helper if it isn't already (check whether B-3a/B-3b left this inline twice — if so, extracting a `sendNotifyResp(ctx, conn, msgID, subscriptionID string) error` helper as part of this task is in scope and reduces this task's own duplication from four more call sites to one).
- `resolveAndMarkReconciled` (existing): after its current `dispatcher.tryDispatch` call (B-3b), add `subscriptionReconciler.reconcile(ctx, deviceID, c)` — same fresh-context-per-call discipline B-3b established (do not let one reconciler's budget starve the other's).
- `main.go`: construct `parameters.NewRepository(db)`, `subscriptions.NewRepository(db)`, `newSubscriptionReconciler(...)`, wire into the `handler` struct literal and into the `resolveAndMarkReconciled` call site.

**Checklist:**
| Requirement | Test |
|---|---|
| A `ValueChange` Notify updates the parameter cache | `TestHandlerValueChangeUpdatesCache` |
| An `ObjectCreation` Notify invalidates the subtree and records an event | `TestHandlerObjectCreationInvalidatesAndRecords` |
| An `ObjectDeletion` Notify does the same | `TestHandlerObjectDeletionInvalidatesAndRecords` |
| An `Event` Notify records an event with the right name/params | `TestHandlerEventRecordsEvent` |
| An unreconciled connection sending any of the four is dropped, logged, no writes | `TestHandlerNotifyDroppedWhenUnreconciled` (one subtest per type, or a parametrized test — your call) |
| An unknown `subscription_id` triggers `reconcile` but still processes the Notify's data | `TestHandlerUnknownSubscriptionIDTriggersReconcile` |
| `SendResp: true` produces a `NotifyResp` for each of the four types | one test per type, or parametrized |
| `resolveAndMarkReconciled` success triggers subscription reconciliation alongside job dispatch | `TestReconcileTriggersSubscriptionReconcile` |

- [ ] **Step 1: Write the failing tests**

Extend `handler_test.go` following its existing fakes/patterns.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./cmd/uspc/ -run 'TestHandlerValueChange|TestHandlerObjectCreation|TestHandlerObjectDeletion|TestHandlerEvent|TestHandlerNotifyDropped|TestHandlerUnknownSubscriptionID|TestReconcileTriggersSubscriptionReconcile' -v`. Expected: compile FAIL.

- [ ] **Step 3: Implement**

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./cmd/uspc/... -v`. Expected: all PASS.

- [ ] **Step 5: Full backend checks, then commit**

```bash
git add cmd/uspc/handler.go cmd/uspc/handler_test.go cmd/uspc/main.go
git commit -F - <<'EOF'
feat(uspc): route ValueChange, ObjectCreation, ObjectDeletion, Event

The last four Notify variants land somewhere real: ValueChange in the
parameter cache, Object{Creation,Deletion} invalidate the affected
subtree and log an event, Event logs directly. An unknown
subscription_id doesn't drop the Notify -- it's logged and triggers
the same reconcile pass a fresh connect would run (design S7.3),
since a bookkeeping drift is not evidence the data itself is wrong.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
```

---

### Task 7: CI — prove subscription reconciliation and at least one routed Notify against real obuspa

**Files:**
- Modify: `ci/usp/obuspa-websocket.txt` (or create a dedicated factory-reset variant if adding a `Subscription.1.*` block to the existing shared file risks interfering with the existing probe/job-dispatch assertions already run in that same step — your judgment, but state the reasoning).
- Modify: `.github/workflows/ci.yml`
- Create: `ci/usp/assert-subscription.sh` (mirroring `assert-getresp.sh`/`assert-job-dispatch.sh`'s structure).

**Interfaces:**
- Consumes: B-2's existing `usp-interop` job structure (WebSocket step already reused for `assert-job-dispatch.sh`, per B-3b).

**Contract:**
- The simplest, lowest-risk proof: rather than pre-declaring a subscription in obuspa's factory-reset file (which would test obuspa's own subscription-creation path, not this plan's controller-driven reconciliation), let `cmd/uspc` create a `usp_subscriptions` row for the connected test device (via a direct SQL insert in the CI step, matching `assert-job-dispatch.sh`'s established pattern of a raw `psql` insert after the device id is known) with `notif_type = 'ValueChange'`, `reference_list = '{Device.DeviceInfo.SoftwareVersion}'`, then either (a) wait for the next connect to trigger reconciliation (requires a second connection cycle — more CI complexity), or (b) — **preferred, simpler** — have `cmd/uspc` also expose the reconcile trigger idempotently enough that inserting the row and then forcing a reconnect (kill and restart the obuspa container, or just re-run the WebSocket step's existing connect sequence a second time) exercises it. Pick whichever is actually simplest to implement correctly against the real `reconcile`-on-`resolveAndMarkReconciled` hook (Task 6) — do not invent a new manual-trigger code path in production code purely to make CI easier; if the on-connect hook is the only trigger, CI must genuinely reconnect, not pretend to.
- After reconciliation, assert (via a poll, mirroring `assert-job-dispatch.sh`'s shape) that `usp_agents`-linked device's `Device.LocalAgent.Subscription.` instance count is at least 1 (a `GetInstances` sent by the CI script itself against obuspa — or, simpler, read `SELECT id FROM usp_subscriptions WHERE device_id = ...` succeeded is necessary but not sufficient; the real proof is that the *agent* now has the instance, which means asserting against obuspa's own state — check whether obuspa's `-c dump` CLI tool, mentioned in the design spec's testing section, is available in the CI container and use it if so, since that's the actual oracle for "does the agent believe it").
- Then, change `Device.DeviceInfo.SoftwareVersion` if obuspa's CLI/data model allows a controller-side test to force a value change on a read-only-from-the-controller's-perspective path (SoftwareVersion is agent-owned, not controller-writable — **use a different, controller-writable parameter for this specific assertion**, e.g. something in `Device.LocalAgent.` itself that this plan's own subscription Add already touched, or pick a genuinely writable TR-181 parameter obuspa exposes) — trigger a `ValueChange`, and assert (via `assert-subscription.sh` polling `parameter_history` or `device_parameter_cache` directly with `psql`, matching `assert-job-dispatch.sh`'s DB-polling style) that the new value landed with `source = 'USP_NOTIFY_VALUE_CHANGE'` (or whatever Task 4's exact constant value is — check it, don't guess).
- If any part of this proves impractical against real obuspa's actual behavior (e.g. obuspa doesn't expose a convenient controller-writable parameter to change, or its subscription semantics differ from what this plan assumed), it is acceptable to scale down to a weaker but still real assertion (e.g. just proving the `Add` was sent and obuspa's `-c dump` shows the instance, without also proving a `ValueChange` fires) — document explicitly what was proven versus what was assumed, following this programme's established honesty standard, rather than forcing a fragile end-to-end assertion that might flake for reasons unrelated to the code under test.

**Checklist:**
| Requirement | Evidence |
|---|---|
| A `usp_subscriptions` row converges onto the real agent (obuspa) as an actual `Subscription` instance | CI step assertion |
| YAML still parses; new script passes `bash -n` and has the executable bit | validation commands |

- [ ] **Step 1: Design and implement the CI proof**

Per the Contract's guidance — use your judgment on the exact sequencing, documenting any place you scaled down the assertion.

- [ ] **Step 2: Validate**

`python3 -c "import yaml;yaml.safe_load(open('.github/workflows/ci.yml'));print('YAML OK')"`, `bash -n ci/usp/assert-subscription.sh`, `git update-index --chmod=+x ci/usp/assert-subscription.sh` if you created it, confirm `100755` via `git ls-files -s`.

- [ ] **Step 3: Run locally if Docker is reachable**

Check `docker ps`; if reachable, actually run it and record output; if not (Windows Docker lacking `--network host`, the established limitation for this whole programme's CI work), say so plainly.

- [ ] **Step 4: Commit**

```bash
git add ci/usp/ .github/workflows/ci.yml
git commit -F - <<'EOF'
ci: prove subscription reconciliation against real obuspa

Extends usp-interop with the last unproven piece of the USP
controller programme's subscription/Notify work: a desired
subscription actually converges onto a real agent, not just a fake
one.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
```

---

## Self-Review

**1. Spec coverage.**

| Spec section | Task |
|---|---|
| §7.1 desired state (`usp_subscriptions`) | 1, 3 |
| §7.2 reconciliation on every connect, built once generically | 3, 5, 6 |
| §7.3 Notify routing table (all five rows: ValueChange, ObjectCreation/Deletion, OperationComplete — done B-3b, Event, OnBoardRequest — done B-3a) | 2, 4, 6 |
| §7.3 at-least-once, dedup on subscription_id + content | Task 1's `device_events` unique constraint + Task 4/6's `ON CONFLICT DO NOTHING`; `ValueChange`'s natural idempotency via `Upsert` |
| §7.3 unknown subscription_id → log + reconcile, not error | 6 |
| §9 testing/acceptance — subscription reconciliation across a restart, real-agent evidence | 7 |

Deliberately not here: `TriggerAction`/`Config`-type subscriptions (v2.16, out of scope per spec's own §7.1 framing), any UI/API surface for operators to create subscriptions (this plan builds the repository and reconciler; an operator-facing endpoint to populate `usp_subscriptions` is not named by the spec and is left for whoever needs it first — most likely sub-project C or a dedicated console feature).

**2. Placeholder scan.** No bare `TODO`. Task 7's explicit permission to "scale down... document what was proven versus assumed" is not a placeholder — it's the same honesty discipline every prior real-agent CI task in this programme has used when a specific detail couldn't be verified without running it, stated as an explicit instruction rather than silently guessed.

**3. Type consistency.** `subscriptions.Item`/`Reconcile`/`Repository`/`Subscription` (Task 3) are consumed by name in Task 5's `subscriptionReconciler`. `usp.ValueChange`/`ObjectCreation`/`ObjectDeletion`/`Event` and their `Decode*` functions (Task 2) are consumed by name in Task 6's `handler.go` additions. `parameters.InvalidateSubtree`/`SourceUSPNotify` and `devices.RecordEvent`/`Events` (Task 4) are consumed by name in Task 6. `subscriptionReconciler.reconcile`/`handleResponse` (Task 5) are consumed by name in Task 6's `OnRecord` fallthrough and `resolveAndMarkReconciled` extension.

**Ordering.** 1 → 2 can run in parallel (no shared files). 3 needs 1 (schema) only, not 2. 4 needs 1 (schema) only. 5 needs 2 and 3. 6 needs 2, 4, and 5. 7 needs everything. Sequential 1 → 2 → 3 → 4 → 5 → 6 → 7 is safe and simplest for a controller running this via subagent-driven-development.
