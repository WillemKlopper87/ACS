# USP Agent Identity & Registry Implementation Plan (B-3a)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give `cmd/uspc` real device identity: a connecting USP agent is matched to (or creates) the same `devices` row CWMP would use, `cmd/uspc` persists which agents are live, and the liveness reaper stops mislabelling connected USP agents `UNREACHABLE`.

**Architecture:** A new `usp_agents` table linked to `devices` by `device_id` (never a new identity key — OUI+ProductClass+SerialNumber stays the one identity, per sub-project A and the design spec). Reconciliation runs in priority order — `Notify.OnBoardRequest` (primary), endpoint-ID parse (optimisation), `Get` fallback (last resort, reusing the interop probe's existing round trip) — inside `cmd/uspc`, which gains its first two domain imports (`internal/devices`, `internal/store`) under a narrowed boundary test. `internal/jobs` stays out of reach; dispatch is B-3b.

**Tech Stack:** Go 1.26; existing `database/sql`/`pgx` stack via `internal/store`; no new dependencies.

**Spec:** [`docs/superpowers/specs/2026-09-09-usp-controller-design.md`](../specs/2026-09-09-usp-controller-design.md) — this plan implements §5 (device identity and registry) and §5.4's liveness-reaper fix. §6 (dispatch) and §7 (subscriptions/Notify routing beyond OnBoardRequest) are later plans (B-3b, B-3c) that build on the `device_id`↔`endpoint_id` link this plan establishes.

## Programme context

| Plan | Deliverable | Depends on |
|---|---|---|
| 0 | Defect fixes — complete. | — |
| A | Fleet data model — complete. | — |
| B-1 | USP protocol core — complete. | — |
| B-2 | USP transports, `cmd/uspc`, obuspa interop in CI — complete. | B-1 |
| **B-3a** (this) | Agent identity, `usp_agents`, liveness reaper fix. | A (device schema), B-2 (`cmd/uspc`) |
| B-3b | Job dispatch over USP (`internal/jobs` wired in, push-on-insert). | B-3a |
| B-3c | Subscriptions and Notify routing beyond OnBoardRequest. | B-3a, B-3b |

## Facts established during research (binding — do not re-derive, do not contradict)

- `devices` (migration `0001_devices.sql`) already has `oui`, `product_class`, `serial_number`, and a `UNIQUE` natural key `oui_serial`. It has **no** `management_protocols` column — this plan adds one.
- `internal/devices.Repository.UpsertFromInform(ctx, id cwmp.DeviceID, eventCodes []string) (*Device, error)` (`internal/devices/repository.go:35`) is the existing CWMP match-or-create: `INSERT ... ON CONFLICT (oui_serial) DO UPDATE`, keyed on `id.NaturalKey()`. USP reconciliation must land on the *same* `oui_serial` row for a dual-stack device — reuse this exact key, do not invent a second one.
- `internal/devices.Repository.GetByOUIserial(ctx, ouiSerial string) (*Device, error)` (`repository.go:94`) is the lookup counterpart.
- The tenancy pattern established by sub-project A's `account_device_mappings` and used by `internal/jobs` (`job.go:271`, `:311`) is: **no `customer_id` column on the child table**, scope by joining through `device_id` to `devices.customer_id`. `usp_agents` follows this exactly.
- The liveness reaper is `internal/devices.Repository.RefreshLiveness(ctx, onlineThreshold, unreachableThreshold time.Duration)` (`repository.go:102`), called every minute from `cmd/acs/workers.go:73` (`runLivenessReaper`). It has **zero** per-protocol branching today — a connected USP agent that never Informs is marked `UNREACHABLE` by this exact SQL. This plan adds the branch.
- `cmd/uspc/boundary_test.go`'s `TestUSPCImportsNoDomainPackages` currently forbids `internal/devices`, `internal/jobs`, `internal/store`. This plan **narrows** that list to `internal/jobs` only — `internal/devices` and `internal/store` become permitted imports for `cmd/uspc`, `internal/jobs` stays forbidden until B-3b.
- `cmd/uspc` currently has **no database connection at all** — it is a pure wiring service (no `ACS_POSTGRES_DSN`, no `store.Open`). This plan adds one, following `cmd/bssadapter/main.go`'s pattern exactly: `dsn := os.Getenv("ACS_POSTGRES_DSN")`, `store.Open(ctx, dsn)` (`internal/store/postgres.go:57`), then `store.Migrate` is **not** called by `cmd/uspc` itself — migrations are applied once by `cmd/migrate`, the same convention every other service follows.
- The generated protobuf already has everything needed to decode an `OnBoardRequest` and build a `NotifyResp` — B-1 built encoders for controller-initiated requests only, never touched `Notify`:
  - `uspproto.Notify` (oneof `Notification`, one variant `*Notify_OnBoardReq{OnBoardReq *Notify_OnBoardRequest}`), fields `SubscriptionId string`, `SendResp bool`.
  - `uspproto.Notify_OnBoardRequest{Oui, ProductClass, SerialNumber, AgentSupportedProtocolVersions string}`.
  - `uspproto.NotifyResp{SubscriptionId string}`.
  - `uspproto.Header_NOTIFY Header_MsgType = 3`.
  - A `Notify` arrives as `Msg.Body.MsgBody = &Body_Request{Request: &Request{ReqType: &Request_Notify{Notify: ...}}}` — same `Body`/`Request` envelope `EncodeGet` etc. already use; check the generated `Request` oneof for the exact field name (`Request_Notify`) before writing code.
  - A `NotifyResp` reply is a `Response`, not a `Request`: `Msg.Body.MsgBody = &Body_Response{Response: &Response{RespType: &Response_NotifyResp{NotifyResp: ...}}}`, header `Header_NOTIFY_RESP`. Confirm the exact response oneof variant name in the generated code (grep `Response_NotifyResp` in `uspproto`) before implementing — do not guess it.
- `internal/usp/message.go`'s existing pattern for a request encoder is `encodeRequest(msgID, msgType, req *uspproto.Request)`; there is no equivalent `encodeResponse` helper yet — Task 2 adds the minimal one needed for `NotifyResp`, matching the existing function's shape (marshal error wrapping, msgID/msgType placement).
- The `usp-interop` CI job (added in B-2) runs `cmd/uspc` with **no** database — it is not the `migrations` job and has no `postgres` service container. Once `cmd/uspc` requires a DSN, that job must gain one, or `usp-interop` breaks on its very next run. This plan's Task 6 fixes the CI job in the same commit that adds the DSN requirement — do not ship one without the other.
- The `migrations` job's existing DB-backed test step (`.github/workflows/ci.yml`, "DB-backed repository tests...") already runs `./internal/store/ ./internal/bss/ ./internal/auth/ ./internal/jobs/ ./cmd/acs/` with `-p 1` (shared database, must not run concurrently). Sub-project A's plan established the rule: a DB-backed test placed in a package not on this list silently never runs in CI. This plan's new DB-backed tests land in `internal/devices` and `internal/usp` (identity reconciliation) — `internal/devices` is **not currently on the list** and must be added.

## Global Constraints

- Go module `acs`; directive `go 1.26.6`. Do not raise it.
- No new dependencies.
- `internal/usp/...` still may not import `acs/internal/devices`, `acs/internal/jobs`, `acs/internal/store`, `acs/internal/sessions`, `acs/internal/cwmp`, or `acs/cmd/...` — B-1's walk-based boundary test enforces this over the whole tree and is untouched by this plan. Identity reconciliation logic that needs both USP types and domain types lives in `cmd/uspc` or a new package under `internal/devices` (see Task 3), never inside `internal/usp`.
- `cmd/uspc` may now import `acs/internal/devices` and `acs/internal/store`. It still may **not** import `acs/internal/jobs` — Task 5's rewritten boundary test enforces exactly this narrower rule.
- One `oui_serial` per physical device is the only identity key. A device record must never be created from an Endpoint ID alone (spec §5.3) — a USP-only agent whose OnBoardRequest never arrives and whose Endpoint ID doesn't parse as `os::<OUI>-<Serial>` falls through to the `Get` fallback; if even that fails to yield OUI/ProductClass/Serial, no device record is created and the connection is logged, not silently dropped.
- Every migration is forward-only and checksum-verified at boot (`internal/store/postgres.go`, `//go:embed all:migrations`) — never edit a migration once committed.
- Before every commit, from `backend/`: `gofmt -l .` prints nothing, `go vet ./...` and `go test ./...` pass.
- DB-backed tests use `ACS_TEST_POSTGRES_DSN`, matching every existing DB-backed test package (grep any file under `internal/store/*_test.go` for the exact skip-if-unset pattern before writing new tests).
- Commit message style: `type(scope): summary`, imperative mood. End each commit message with:
  `Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>`

## Decisions settled here

**Agent allowlisting is still not built by this plan.** §8's controller-trust allowlist (rejecting an agent whose endpoint id isn't provisioned) is a connection-time gate; this plan is about what happens *after* a connection is accepted, and does not change `cmd/uspc`'s current posture. The startup warning added in B-2 stays as-is and is not weakened — an unrecognised agent can still open a connection and even get a `devices` row created for it via the `Get` fallback. Allowlisting is deferred to a plan that names it explicitly, not silently implied by "identity" work landing.

**Reconciliation happens once per connection, on the first thing that yields identity — not on every record.** `handler.OnConnect` starts the existing interop probe (B-2, unchanged) and also starts an identity timer; `handler.OnRecord` short-circuits reconciliation the moment an `OnBoardRequest` Notify or a matching endpoint-ID-parse succeeds, whichever comes first, and falls back to the probe's own `GetResp` (already round-tripping `Device.DeviceInfo.`, which includes `ManufacturerOUI`, `ProductClass`, `SerialNumber` as standard TR-181 parameters — no second Get is needed for the fallback path).

**`usp_agents` rows are per-connection state, not per-device history.** One row per `device_id` (`UNIQUE`), overwritten on every reconnect — this mirrors `mtp.Registry`'s "one live connection per endpoint" model, not sub-project A's "history preserved" model. There is no `usp_agents` history table; `devices.last_updated_at` and the existing audit log remain the record of what changed and when.

## File structure

| File | Responsibility |
|---|---|
| `backend/internal/store/migrations/0053_usp_agents.sql` | `devices.management_protocols`, `usp_agents` table. |
| `backend/internal/usp/message.go` (modify) | Adds `DecodeNotify`/notify-shape helpers and `EncodeNotifyResp`. |
| `backend/internal/usp/message_test.go` (modify) | Tests for the above. |
| `backend/internal/devices/usp.go` | `Repository` methods: `UpsertFromOnBoard`, USP-specific upsert reusing the `oui_serial` key. |
| `backend/internal/devices/usp_test.go` | DB-backed tests. |
| `backend/internal/devices/usp_agents.go` | New `UspAgentRepository` (or extend `devices.Repository` — see Task 3's contract): link/lookup/mark-connected/mark-disconnected on `usp_agents`. |
| `backend/internal/devices/usp_agents_test.go` | DB-backed tests. |
| `backend/internal/devices/repository.go` (modify) | `RefreshLiveness` gains the USP-aware branch. |
| `backend/internal/devices/repository_test.go` (modify) | Regression test for the liveness fix. |
| `backend/cmd/uspc/identity.go` | `reconciler` type: owns the OnConnect/OnRecord identity logic described in *Decisions settled here*. |
| `backend/cmd/uspc/identity_test.go` | Tests using fake `devices`/`usp_agents` repositories (interfaces, not a live DB — keep `cmd/uspc`'s own tests fast; DB-backed correctness is proven in `internal/devices`'s tests). |
| `backend/cmd/uspc/handler.go` (modify) | Wires the reconciler into `OnConnect`/`OnRecord`/`OnDisconnect`. |
| `backend/cmd/uspc/main.go` (modify) | Adds `ACS_USP_POSTGRES_DSN`-driven `store.Open`, passes repositories into the handler, closes the DB on shutdown. |
| `backend/cmd/uspc/config.go` (modify) | New required-with-existing-fail-closed-pattern DSN setting. |
| `backend/cmd/uspc/boundary_test.go` (modify) | Narrows `forbiddenPrefixes` to `internal/jobs` only. |
| `.github/workflows/ci.yml` (modify) | `usp-interop` job gains a `postgres` service and DSN env, migrates before starting `uspc`; `migrations` job's DB-backed repository-tests step gains `./internal/devices/`. |

---

### Task 1: Migration — `management_protocols` and `usp_agents`

**Files:**
- Create: `backend/internal/store/migrations/0053_usp_agents.sql`
- Test: `backend/internal/store/migration_0053_test.go`

**Interfaces:**
- Consumes: the migration harness pattern from `backend/internal/store/migration_0052_test.go` (read it for the exact `Migrate(ctx, db)` call and DSN-skip pattern before writing this task's test).
- Produces: `devices.management_protocols TEXT[]`, table `usp_agents`.

**Contract:**

```sql
ALTER TABLE devices
  ADD COLUMN management_protocols TEXT[] NOT NULL DEFAULT '{}';

CREATE TABLE usp_agents (
    device_id                     UUID PRIMARY KEY REFERENCES devices(id),
    endpoint_id                   TEXT NOT NULL UNIQUE,
    mtp_kind                      TEXT NOT NULL CHECK (mtp_kind IN ('WebSocket', 'MQTT')),
    connected                     BOOLEAN NOT NULL DEFAULT true,
    last_connected_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at                  TIMESTAMPTZ NOT NULL DEFAULT now(),
    supported_protocol_versions   TEXT[] NOT NULL DEFAULT '{}',
    controller_role               TEXT
);

CREATE INDEX usp_agents_connected_idx ON usp_agents (connected);
```

- `mtp_kind`'s two values are `mtp.KindWebSocket`/`mtp.KindMQTT`'s exact string values (`"WebSocket"`, `"MQTT"`) — verify against `backend/internal/usp/mtp/transport.go` before writing the `CHECK`, do not assume.
- `endpoint_id UNIQUE` (not `device_id, endpoint_id` composite) because at most one live agent uses a given endpoint id at a time — the `mtp.Registry` already enforces this per-process; the database enforces it durably.
- No `customer_id` column (*Decisions settled here* and the tenancy pattern above).
- No `ON DELETE CASCADE` on `device_id` — device deletion is out of scope for this plan and CWMP has no precedent for it either; leave the FK to fail loudly if that ever changes.

**Checklist:**
| Requirement | Test |
|---|---|
| Migration applies to a clean DB | `TestMigration0053AppliesCleanly` |
| `management_protocols` defaults to `'{}'` for existing rows | same test, asserted after apply |
| `usp_agents.endpoint_id` unique constraint holds | `TestMigration0053EndpointIDUnique` |
| `usp_agents.mtp_kind` CHECK rejects a bad value | `TestMigration0053MTPKindCheck` |

- [ ] **Step 1: Write the failing tests**

Write `backend/internal/store/migration_0053_test.go` following `migration_0052_test.go`'s exact structure (DSN from `ACS_TEST_POSTGRES_DSN`, skip if unset, fresh schema per test via that file's existing setup helper — read it, do not reinvent). Assert:
- After `Migrate`, `information_schema.columns` shows `devices.management_protocols` as `ARRAY` type with `NOT NULL`.
- Inserting two `usp_agents` rows with the same `endpoint_id` (different `device_id`s, both referencing real `devices` rows created via a minimal insert) fails with a unique-violation (`pgconn.PgError` code `23505`, same assertion style as sub-project A's `SwapDevice` tests — check `internal/bss/mapping_test.go` for the exact `errors.As` pattern).
- Inserting a `usp_agents` row with `mtp_kind = 'STOMP'` fails a CHECK violation (code `23514`).

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/store/ -run TestMigration0053 -v` (with `ACS_TEST_POSTGRES_DSN` set to a local/CI Postgres). Expected: FAIL — migration file does not exist, `Migrate` succeeds without creating the table, subsequent assertions fail.

- [ ] **Step 3: Write the migration**

Create `0053_usp_agents.sql` exactly as specified in the Contract above.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/store/ -run TestMigration0053 -v`. Expected: PASS.

- [ ] **Step 5: Full backend checks, then commit**

Run: `gofmt -l . && go vet ./... && go test ./...` — clean (skip DB-backed tests silently if no DSN is set, matching every existing test's own skip behaviour).

```bash
git add internal/store/migrations/0053_usp_agents.sql internal/store/migration_0053_test.go
git commit -F - <<'EOF'
feat(store): add devices.management_protocols and usp_agents

The one identity a device has is OUI+ProductClass+SerialNumber
(sub-project A); usp_agents links that same devices row to a live USP
endpoint id and MTP, never introducing a second identity key. One row
per device, overwritten on reconnect -- this is live connection state,
not history.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
```

---

### Task 2: `internal/usp` — decode `Notify`, encode `NotifyResp`

**Files:**
- Modify: `backend/internal/usp/message.go`
- Modify: `backend/internal/usp/message_test.go`

**Interfaces:**
- Consumes: `uspproto.Notify`, `uspproto.Notify_OnBoardReq`, `uspproto.Notify_OnBoardRequest`, `uspproto.NotifyResp`, `uspproto.Header_NOTIFY`, `uspproto.Header_NOTIFY_RESP` (confirm this constant's exact name in the generated code before use), `uspproto.Request_Notify` and `uspproto.Response_NotifyResp` (confirm exact oneof wrapper type names by grepping `uspproto/usp-msg-1-3.pb.go` — do not guess).
- Produces:
  - `type OnBoardRequest struct { SubscriptionID string; SendResp bool; OUI, ProductClass, SerialNumber, AgentSupportedProtocolVersions string }`.
  - `func DecodeOnBoardRequest(msg *uspproto.Msg) (*OnBoardRequest, error)` — returns a sentinel `ErrNotOnBoardRequest = errors.New("USP message is not an OnBoardRequest Notify")` if `msg` is not a `NOTIFY` whose `Notification` oneof is `*Notify_OnBoardReq`. This is deliberately narrow (only the one Notify variant B-3a needs); B-3c's plan adds decode for the other Notify variants (`ValueChange`, `ObjCreation`, `ObjDeletion`, `OperComplete`, `Event`) when it needs them — do not build those here.
  - `func EncodeNotifyResp(msgID, subscriptionID string) ([]byte, error)` — mirrors `encodeRequest`'s shape but builds a `Body_Response`/`Response_NotifyResp` instead, with header `Header_NOTIFY_RESP`.

**Contract:**
- `DecodeOnBoardRequest` must reject (with `ErrNotOnBoardRequest`) a `Notify` whose oneof is any other variant, and any non-`NOTIFY`-typed message — a `GetResp` accidentally passed in must not panic or silently return a zero-value struct.
- `EncodeNotifyResp` follows `encodeRequest`'s pattern for marshal-error wrapping (`fmt.Errorf("marshal USP %v: %w", msgType, err)`).

**Checklist:**
| Requirement | Test |
|---|---|
| Decodes a well-formed OnBoardRequest Notify, all four fields | `TestDecodeOnBoardRequest` |
| Rejects a non-OnBoardRequest Notify variant (e.g. a ValueChange) distinctly | `TestDecodeOnBoardRequestWrongVariant` |
| Rejects a non-Notify message (e.g. a GetResp) without panicking | `TestDecodeOnBoardRequestWrongMsgType` |
| `EncodeNotifyResp` round-trips through `DecodeMsg` with the right header/subscription_id | `TestEncodeNotifyRespRoundTrip` |

- [ ] **Step 1: Write the failing tests**

Add to `message_test.go` — build a `Notify`-carrying `Msg` directly via `uspproto` types (same style B-1's tests use to build a hand-crafted `Msg` for `DecodeMsg` tests; read one such existing test for the exact construction pattern), covering all four checklist rows above. `TestEncodeNotifyRespRoundTrip` calls `EncodeNotifyResp`, feeds the bytes to the existing `DecodeMsg`, and asserts `Header.MsgType == Header_NOTIFY_RESP` (confirm exact constant name) and the response's `SubscriptionId` matches what was passed in.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/usp/ -run 'TestDecodeOnBoardRequest|TestEncodeNotifyResp' -v`. Expected: compile FAIL — functions undefined.

- [ ] **Step 3: Implement**

Add `OnBoardRequest`, `ErrNotOnBoardRequest`, `DecodeOnBoardRequest`, `EncodeNotifyResp` to `message.go`, following the file's existing doc-comment voice (explain *why*, as the rest of `internal/usp` does).

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/usp/... -v`. Expected: all PASS, full B-1 suite unaffected.

- [ ] **Step 5: Full backend checks, then commit**

```bash
git add internal/usp/message.go internal/usp/message_test.go
git commit -F - <<'EOF'
feat(usp): decode OnBoardRequest Notify, encode NotifyResp

B-1 built encoders only for controller-initiated requests; an agent's
unsolicited OnBoardRequest -- the primary identity-reconciliation path
(design spec S5.3) -- was never decodable. Narrow on purpose: only the
one Notify variant this plan's reconciliation needs. The other five
variants are a later plan's concern.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
```

---

### Task 3: `internal/devices` — USP identity upsert and `usp_agents` repository

**Files:**
- Create: `backend/internal/devices/usp.go`, `backend/internal/devices/usp_test.go`
- Create: `backend/internal/devices/usp_agents.go`, `backend/internal/devices/usp_agents_test.go`

**Interfaces:**
- Consumes: `internal/cwmp.DeviceID` and its `NaturalKey()` method (`internal/cwmp/types.go:21,39`) — reused as-is so a USP-onboarded device lands on the identical `oui_serial` key a CWMP Inform would produce for the same physical unit. `Repository.UpsertFromInform`'s SQL shape (`repository.go:35`) as the pattern to follow, not to call directly (that function is CWMP-specific in its event-code handling).
- Produces (package `devices`, extending the existing `Repository`):
  - `func (r *Repository) UpsertFromOnBoard(ctx context.Context, oui, productClass, serialNumber string) (*Device, error)` — builds a `cwmp.DeviceID{OUI: oui, ProductClass: productClass, SerialNumber: serialNumber}`, computes `NaturalKey()`, and performs the same `INSERT ... ON CONFLICT (oui_serial) DO UPDATE` shape as `UpsertFromInform`, but additionally appends `'USP'` to `management_protocols` (via `array_append` with a duplicate-guard, e.g. `management_protocols = CASE WHEN 'USP' = ANY(management_protocols) THEN management_protocols ELSE array_append(management_protocols, 'USP') END`) — do not clobber an existing CWMP entry in that array.
  - `type UspAgent struct { DeviceID string; EndpointID string; MTPKind string; Connected bool; LastConnectedAt, LastSeenAt time.Time; SupportedProtocolVersions []string; ControllerRole string }`
  - `func (r *Repository) LinkUspAgent(ctx context.Context, deviceID, endpointID, mtpKind string) error` — `INSERT INTO usp_agents (...) VALUES (...) ON CONFLICT (device_id) DO UPDATE SET endpoint_id = EXCLUDED.endpoint_id, mtp_kind = EXCLUDED.mtp_kind, connected = true, last_connected_at = now(), last_seen_at = now()`. Because `endpoint_id` is independently `UNIQUE`, a conflict there (a different device already claiming this endpoint id — should not happen if the caller's identity reconciliation is correct, but the database must not silently corrupt state) must surface as a distinct, named error: `var ErrEndpointIDInUse = errors.New("usp endpoint id already linked to a different device")`, detected via the `pgconn.PgError` code `23505` on the `usp_agents_endpoint_id_key` constraint specifically (not the `device_id` primary key conflict, which is the normal reconnect path and must NOT error).
  - `func (r *Repository) MarkUspAgentDisconnected(ctx context.Context, deviceID string) error` — `UPDATE usp_agents SET connected = false, last_seen_at = now() WHERE device_id = $1`. No-op (not an error) if no row exists yet for that device.
  - `func (r *Repository) GetUspAgentByEndpointID(ctx context.Context, endpointID string) (*UspAgent, error)` — the endpoint-ID-parse fast path's lookup; `sql.ErrNoRows` bubbles as a typed `ErrUspAgentNotFound = errors.New("no usp_agents row for endpoint id")`.

**Checklist:**
| Requirement | Test |
|---|---|
| `UpsertFromOnBoard` creates a new device on first contact | `TestUpsertFromOnBoardCreates` |
| `UpsertFromOnBoard` matches an existing CWMP device by the same `oui_serial`, appends `'USP'` without disturbing `'CWMP'` | `TestUpsertFromOnBoardMatchesExistingCWMPDevice` |
| Calling `UpsertFromOnBoard` twice for the same device is idempotent on `management_protocols` (no duplicate `'USP'` entries) | `TestUpsertFromOnBoardIdempotent` |
| `LinkUspAgent` creates a row, then updates it in place on reconnect (same `device_id`) | `TestLinkUspAgentReconnect` |
| `LinkUspAgent` on an endpoint id already claimed by a different device returns `ErrEndpointIDInUse`, not a raw pg error | `TestLinkUspAgentEndpointCollision` |
| `MarkUspAgentDisconnected` sets `connected = false`; no-op for an unknown device | `TestMarkUspAgentDisconnected`, `TestMarkUspAgentDisconnectedUnknownDevice` |
| `GetUspAgentByEndpointID` finds a linked agent; returns `ErrUspAgentNotFound` otherwise | `TestGetUspAgentByEndpointID`, `TestGetUspAgentByEndpointIDNotFound` |

- [ ] **Step 1: Write the failing tests**

Both test files are DB-backed, following the skip-if-`ACS_TEST_POSTGRES_DSN`-unset pattern used throughout `internal/devices`'s existing tests (read `repository_test.go` for the exact setup/teardown helper and reuse it — do not write a second one). Write every case from the Checklist as a literal test. For `TestUpsertFromOnBoardMatchesExistingCWMPDevice`: first call `r.UpsertFromInform(ctx, cwmp.DeviceID{OUI: "001122", ProductClass: "Router", SerialNumber: "ABC123"}, nil)`, then `r.UpsertFromOnBoard(ctx, "001122", "Router", "ABC123")`, and assert both calls return the same `Device.ID`, and the final row's `management_protocols` contains both `"CWMP"` and `"USP"` (note: confirm `UpsertFromInform` actually writes `'CWMP'` into this new column — if it currently doesn't, since the column didn't exist before Task 1, this test will need `UpsertFromInform` itself extended in this same task to also set `management_protocols` on its own upsert path; check `repository.go:35`'s SQL and, if `management_protocols` isn't referenced there yet, add the equivalent `'CWMP'`-append clause to `UpsertFromInform`'s `ON CONFLICT DO UPDATE` as part of this task — this is a real, necessary addition the Contract section above did not spell out because it depends on what Task 1's migration default leaves existing rows with).

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/devices/ -run 'TestUpsertFromOnBoard|TestLinkUspAgent|TestMarkUspAgent|TestGetUspAgent' -v`. Expected: compile FAIL — functions undefined.

- [ ] **Step 3: Implement**

`usp.go` and `usp_agents.go` per the **Produces** contracts above, plus the `UpsertFromInform` extension identified in Step 1 if needed. Use the same `pgconn.PgError`-unwrapping pattern sub-project A's `internal/bss/mapping.go` uses for its own named sentinel errors (`ErrDeviceAlreadyAssigned` etc.) — read that file's error-mapping code for the exact idiom before writing `ErrEndpointIDInUse`'s detection.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/devices/... -v`. Expected: all PASS.

- [ ] **Step 5: Full backend checks, then commit**

```bash
git add internal/devices/usp.go internal/devices/usp_test.go internal/devices/usp_agents.go internal/devices/usp_agents_test.go internal/devices/repository.go
git commit -F - <<'EOF'
feat(devices): USP identity upsert and usp_agents repository

UpsertFromOnBoard lands a USP OnBoardRequest on the exact same
oui_serial key UpsertFromInform uses for CWMP, so a dual-stack device
is one devices row, not two. usp_agents links that row to a live
endpoint id; LinkUspAgent is idempotent on reconnect and refuses (by
name, not raw pg error) an endpoint id already claimed elsewhere.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
```

---

### Task 4: Liveness reaper — skip connected USP agents

**Files:**
- Modify: `backend/internal/devices/repository.go`
- Modify: `backend/internal/devices/repository_test.go`

**Interfaces:**
- Consumes: `usp_agents.connected` (Task 3's schema).
- Produces: `RefreshLiveness`'s existing signature, unchanged — only its SQL and behaviour change.

**Contract:**
- A device with a `usp_agents` row where `connected = true` must never be moved to `UNREACHABLE` or `OFFLINE` by this reaper, regardless of `last_inform_at` — MTP connection state is authoritative and immediate for USP (spec §5.4), the Inform-based inference is simply wrong for these devices.
- A device whose USP agent has disconnected (`usp_agents.connected = false`, or no `usp_agents` row at all) is unaffected by this change — it falls back to exactly today's `last_inform_at`-based logic, since a disconnected USP agent's liveness is genuinely unknown the same way a CWMP device's is.
- Implementation approach: add `AND (management_protocols IS NULL OR NOT ('USP' = ANY(management_protocols)) OR NOT EXISTS (SELECT 1 FROM usp_agents ua WHERE ua.device_id = devices.id AND ua.connected))` to the existing `UPDATE ... WHERE` clause (or an equivalent `LEFT JOIN`/`NOT IN` — pick whichever reads more clearly against the existing query's style, but do not change the query's shape more than this one exclusion needs).

**Checklist:**
| Requirement | Test |
|---|---|
| A device with `usp_agents.connected = true` and old `last_inform_at` stays `ONLINE`/whatever it already was, not `UNREACHABLE` | `TestRefreshLivenessSkipsConnectedUSPAgent` |
| A device with `usp_agents.connected = false` and old `last_inform_at` is still marked `UNREACHABLE` exactly as before | `TestRefreshLivenessMarksDisconnectedUSPAgentUnreachable` |
| A pure-CWMP device (no `usp_agents` row) is completely unaffected — existing `TestRepository_RefreshLivenessTransitions` still passes unchanged | run the existing test |

- [ ] **Step 1: Write the failing tests**

Add the two new tests to `repository_test.go`, following the existing `TestRepository_RefreshLivenessTransitions`'s setup style (read it first). Each inserts a device with an old `last_inform_at`, links (or doesn't) a `usp_agents` row via Task 3's `LinkUspAgent`/`MarkUspAgentDisconnected`, calls `RefreshLiveness`, and asserts the resulting `online_status`.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/devices/ -run TestRefreshLiveness -v`. Expected: the two new tests FAIL (device incorrectly moved to `UNREACHABLE`); the existing test still passes (confirming the starting point is exactly today's behaviour).

- [ ] **Step 3: Implement**

Modify `RefreshLiveness`'s SQL per the Contract.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/devices/... -v`. Expected: all PASS, including the pre-existing `TestRepository_RefreshLivenessTransitions` unchanged.

- [ ] **Step 5: Full backend checks, then commit**

```bash
git add internal/devices/repository.go internal/devices/repository_test.go
git commit -F - <<'EOF'
fix(devices): liveness reaper skips connected USP agents

MTP connection state is authoritative and immediate for USP; inferring
liveness from missed Informs is simply the wrong signal for a device
that never Informs in the first place. A device stops being exempt the
moment its USP agent disconnects, at which point today's
last_inform_at logic already applies correctly.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
```

---

### Task 5: `cmd/uspc` — narrow the boundary, add the identity reconciler

**Files:**
- Create: `backend/cmd/uspc/identity.go`, `backend/cmd/uspc/identity_test.go`
- Modify: `backend/cmd/uspc/handler.go`, `backend/cmd/uspc/main.go`, `backend/cmd/uspc/config.go`, `backend/cmd/uspc/boundary_test.go`

**Interfaces:**
- Consumes: `devices.Repository`'s new methods (Task 3), `usp.DecodeOnBoardRequest`/`usp.EncodeNotifyResp` (Task 2), `internal/config`'s fail-closed pattern (already used elsewhere in `cmd/uspc/config.go`), `internal/store.Open` (`internal/store/postgres.go:57`).
- Produces:
  - `type identityStore interface { UpsertFromOnBoard(ctx context.Context, oui, productClass, serialNumber string) (*devices.Device, error); LinkUspAgent(ctx context.Context, deviceID, endpointID, mtpKind string) error; MarkUspAgentDisconnected(ctx context.Context, deviceID string) error; GetUspAgentByEndpointID(ctx context.Context, endpointID string) (*devices.UspAgent, error) }` — a narrow interface (not the concrete `*devices.Repository`) so `identity_test.go` can use a fake, matching the plan's stated intent to keep `cmd/uspc`'s own tests fast and DB-free.
  - `type reconciler struct { store identityStore; log *slog.Logger }`, `func newReconciler(store identityStore, log *slog.Logger) *reconciler`.
  - `func (r *reconciler) onBoard(ctx context.Context, c mtp.Conn, ob *usp.OnBoardRequest) error` — calls `UpsertFromOnBoard` then `LinkUspAgent(ctx, device.ID, string(c.Endpoint()), string(c.Kind()))`.
  - `func (r *reconciler) fromProbeFallback(ctx context.Context, c mtp.Conn, oui, productClass, serialNumber string) error` — same shape, used when the `Get` fallback (not an `OnBoardRequest`) is what yields identity; logs at Info that this device onboarded via fallback rather than the primary path, since that is diagnostically useful and the design spec calls it out as "last resort."
  - `func (r *reconciler) disconnect(ctx context.Context, deviceID string)` — calls `MarkUspAgentDisconnected`, logging (not failing) on error, since a disconnect-path failure must never block the transport's own cleanup.

**Contract:**
- `handler.OnRecord` (existing): after the current `usp.DecodeMsg` succeeds and before/alongside handing the message to the probe, attempt `usp.DecodeOnBoardRequest(msg)`; on success, call `reconciler.onBoard` and, if `ob.SendResp`, send an `EncodeNotifyResp(usp.NewMsgID(), ob.SubscriptionID)`-wrapped Record back via `c.Send`. On `usp.ErrNotOnBoardRequest`, proceed exactly as today (the probe-response path is unaffected — an OnBoardRequest and a GetResp are mutually exclusive message shapes, so this check never intercepts probe traffic).
- The probe's existing `GetResp` handling (`probe.handle`) already parses `Device.DeviceInfo.` parameters; Task 5 does **not** change `probe.go`. Instead, `handler`'s probe-response branch, after today's existing logging, additionally checks whether the resolved parameters include `ManufacturerOUI`/`ProductClass`/`SerialNumber` and, if so and if no `OnBoardRequest`-driven reconciliation has already happened for this connection (track this with a `sync.Once` or a boolean on the per-connection state — your choice, but it must be per-`mtp.Conn`, not global), calls `reconciler.fromProbeFallback`. Exact TR-181 parameter names to check: `Device.DeviceInfo.ManufacturerOUI`, `Device.DeviceInfo.ProductClass`, `Device.DeviceInfo.SerialNumber` — confirm these are the standard names (they are; TR-181 Issue 2 `Device.DeviceInfo.` object) rather than guessing a different casing.
- `handler.OnDisconnect` (existing): after today's registry/probe cleanup, if this connection's identity reconciliation ever succeeded (track the resolved `device_id`, if any, on the per-connection state), call `reconciler.disconnect`.
- `main.go`: add `ACS_USP_POSTGRES_DSN` to `config.go`'s fail-closed loader (required, no placeholder default — reuse whatever helper `bssadapter`'s DSN loading uses, or `cmd/uspc`'s own existing `envOrDefault`/validation helpers if a DSN has no sensible default, which it doesn't). Open the DB with `store.Open(ctx, dsn)` before constructing the transports; pass `*devices.Repository` (built from the `*sql.DB`) into `newReconciler`; close the DB in `shutdown` alongside the existing transport/HTTP shutdown, in the same ordered-shutdown style already used there.
- `boundary_test.go`: change `forbiddenPrefixes` to `[]string{"acs/internal/jobs"}` only, and update the test's doc comment to say why (`internal/devices`/`internal/store` are now permitted for identity; `internal/jobs` stays out until dispatch).

**Checklist:**
| Requirement | Test |
|---|---|
| An OnBoardRequest triggers `onBoard` with the right OUI/ProductClass/Serial | `TestHandlerOnBoardRequestReconciles` |
| `SendResp: true` on the OnBoardRequest produces a `NotifyResp` sent back on the same conn | `TestHandlerOnBoardRequestSendsResp` |
| `SendResp: false` sends nothing extra | `TestHandlerOnBoardRequestNoRespWhenNotRequested` |
| A GetResp carrying DeviceInfo params triggers `fromProbeFallback` exactly once per connection, even across multiple GetResps | `TestHandlerProbeFallbackReconcilesOnce` |
| OnBoardRequest reconciliation, once it succeeds, suppresses the probe-fallback path for the same connection | `TestHandlerOnBoardRequestSuppressesProbeFallback` |
| Disconnect calls `MarkUspAgentDisconnected` only for a connection that was reconciled | `TestHandlerDisconnectMarksUspAgent`, `TestHandlerDisconnectSkipsUnreconciledConnection` |
| `cmd/uspc` no longer forbids `internal/devices`/`internal/store`, still forbids `internal/jobs` | `TestUSPCImportsNoDomainPackages` (rewritten) |

- [ ] **Step 1: Write the failing tests**

`identity_test.go`: a fake `identityStore` (struct recording calls) tests `reconciler` in isolation — literal test code covering `onBoard`, `fromProbeFallback`, `disconnect`.

`handler_test.go` (extend the existing one if `cmd/uspc` has one from B-2, else create it): use the same `recordingHandler`-adjacent fake-`mtp.Conn` pattern B-2's transport tests established (check `backend/internal/usp/mtp/websocket_test.go`'s `fakeConn` for the shape, or `cmd/uspc/probe_test.go`'s `captureConn` — reuse whichever is closer) to drive `handler.OnRecord` with a hand-built OnBoardRequest Record, then a GetResp Record, and assert against a fake `identityStore`'s recorded calls per the Checklist rows.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./cmd/uspc/ -run 'TestHandlerOnBoard|TestHandlerProbeFallback|TestHandlerDisconnect' -v`. Expected: compile FAIL.

- [ ] **Step 3: Implement**

`identity.go` per the **Produces** contract; wire into `handler.go`, `main.go`, `config.go` per the **Contract**; rewrite `boundary_test.go`.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./cmd/uspc/... -v`. Expected: all PASS, including the rewritten boundary test.

- [ ] **Step 5: Boot locally against a real database, if one is reachable**

If a local Postgres is available (check `docker compose up -d postgres` from `infra/` or skip with a note if not, matching B-2's own honest-about-local-limits pattern):

```bash
/tmp/migrate  # or however this repo's migrate tool is invoked locally
ACS_USP_POSTGRES_DSN=... ACS_USP_CONTROLLER_ID=... ACS_USP_ALLOW_PLAINTEXT=true \
ACS_USP_WS_ADDR=127.0.0.1:19877 ACS_USP_MQTT_ADDR=127.0.0.1:11883 ACS_USP_HTTP_ADDR=127.0.0.1:18092 \
go run ./cmd/uspc &
sleep 3
curl -s -w " [HTTP %{http_code}]\n" http://127.0.0.1:18092/readyz
kill %1
```

Expected: `ready [HTTP 200]`, confirming the added DB dependency doesn't break startup. Record the output in the report; if Postgres isn't reachable locally, say so plainly and rely on the CI job (Task 6) as first real verification, exactly as B-2's plan allowed for Docker.

- [ ] **Step 6: Full backend checks, then commit**

```bash
git add cmd/uspc/identity.go cmd/uspc/identity_test.go cmd/uspc/handler.go cmd/uspc/handler_test.go cmd/uspc/main.go cmd/uspc/config.go cmd/uspc/boundary_test.go
git commit -F - <<'EOF'
feat(uspc): reconcile agent identity on OnBoardRequest and probe fallback

cmd/uspc gains its first two domain imports -- internal/devices and
internal/store -- under a narrowed boundary test that still forbids
internal/jobs until dispatch lands. OnBoardRequest is the primary
identity path; a GetResp's own DeviceInfo parameters are the
last-resort fallback the design spec calls for, and the two are
mutually exclusive per connection so neither double-onboards a device.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
```

---

### Task 6: CI — give `usp-interop` a database, extend the DB-backed test list

**Files:**
- Modify: `.github/workflows/ci.yml`

**Interfaces:**
- Consumes: the `migrations` job's existing `postgres:18-alpine` service block (copy its shape, do not invent a new one) and DSN env var name (`ACS_USP_POSTGRES_DSN`, from Task 5).

**Contract:**
- `usp-interop` job gains a `postgres` service identical in shape to the `migrations` job's (`postgres:18-alpine`, `POSTGRES_USER/PASSWORD/DB: acs`, health check).
- Before building/running `cmd/uspc` in each of the three interop steps, the job runs the migrate tool once against that database (`go build -o /tmp/migrate ./cmd/migrate && /tmp/migrate`), and sets `ACS_USP_POSTGRES_DSN` alongside the existing `ACS_USP_*` env vars in each step (or once at the job level if that's cleaner given the existing structure — check how the three steps currently share/repeat env vars and follow that convention).
- `migrations` job's DB-backed repository-tests step gains `./internal/devices/` in its package list: `go test -race -count=1 -p 1 ./internal/store/ ./internal/bss/ ./internal/auth/ ./internal/jobs/ ./internal/devices/ ./cmd/acs/`. This is the exact rule sub-project A's plan established (§9 of the fleet-data-model spec): a DB-backed test in a package not on this list never runs in CI.

**Checklist:**
| Requirement | Evidence |
|---|---|
| `usp-interop` provisions its own database, doesn't reuse `migrations`' (separate jobs, separate service containers) | workflow YAML |
| Migration applied before `cmd/uspc` starts in each of the three steps | workflow YAML, ordering |
| `internal/devices`'s new DB-backed tests actually run in CI | `migrations` job's step list |
| YAML still parses | `python3 -c "import yaml;yaml.safe_load(open('.github/workflows/ci.yml'));print('YAML OK')"` |

- [ ] **Step 1: Modify the workflow**

Add the `postgres` service block and migration step to `usp-interop`; add `ACS_USP_POSTGRES_DSN` to its env; append `./internal/devices/` to the `migrations` job's DB-backed repository-tests step.

- [ ] **Step 2: Validate**

Run: `python3 -c "import yaml;yaml.safe_load(open('.github/workflows/ci.yml'));print('YAML OK')"` and `bash -n` any modified inline shell blocks.

- [ ] **Step 3: Commit**

```bash
git add .github/workflows/ci.yml
git commit -F - <<'EOF'
ci: give usp-interop a database, run internal/devices' DB-backed tests

cmd/uspc now opens a database connection (identity reconciliation);
usp-interop must provision and migrate one or every run fails at
startup. internal/devices' new usp_agents-backed tests were not on the
DB-backed repository-tests step's package list and would otherwise
never run in CI -- the exact rule sub-project A's plan established.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
```

---

## Self-Review

**1. Spec coverage.**

| Spec section | Task |
|---|---|
| §5.1 identity principle (OUI+ProductClass+Serial, endpoint id as routing address only) | 3 (reuses `cwmp.DeviceID.NaturalKey()`) |
| §5.2 schema | 1 |
| §5.3 reconciliation priority order (OnBoardRequest → endpoint-ID parse → Get fallback) | 5 for OnBoardRequest and Get-fallback; endpoint-ID-parse fast path is `GetUspAgentByEndpointID` (Task 3) — **note:** this plan wires OnBoardRequest and the Get fallback into `handler.go`, but the endpoint-ID-parse optimisation (checking a reconnecting agent's endpoint id against an existing `usp_agents` row before doing any protocol work) is *not* wired into `OnConnect` by this plan — flagging this as a real gap rather than silently completing it: add a `TestHandlerReconnectSkipsReconciliation`-style Task 5b if this matters before B-3b, or accept that every reconnect currently re-runs full reconciliation (correct, just not optimised). Given the plan's own honesty principle, this is recorded here rather than glossed over — it does not break correctness, only wastes one round-trip on every reconnect. |
| §5.3 "must never create from Endpoint ID alone" | Enforced by construction: `UpsertFromOnBoard` and `fromProbeFallback` both require OUI+ProductClass+Serial; there is no code path that creates a device from `c.Endpoint()` alone. |
| §5.4 liveness reaper fix | 4 |
| §5.4 tenancy (no `customer_id` on `usp_agents`) | 1, 3 |
| §8 controller EndpointID / TLS / allowlist | Unchanged — explicitly deferred, see *Decisions settled here* |

Deliberately not here: §6 dispatch, §7 subscriptions/Notify beyond OnBoardRequest — B-3b, B-3c.

**2. Placeholder scan.** No `TBD`/`TODO`/"implement later". Task 3's note about `UpsertFromInform` possibly needing extension is not a placeholder — it's an explicit, load-bearing instruction to the implementer about a real dependency the Contract section can't resolve without seeing the column's actual default behaviour, with a concrete test that will catch it either way.

**3. Type consistency.** `identityStore` (Task 5) is defined against `devices.Repository`'s exact method signatures from Task 3 — `UpsertFromOnBoard`, `LinkUspAgent`, `MarkUspAgentDisconnected`, `GetUspAgentByEndpointID` all match by name and signature. `usp.OnBoardRequest`/`usp.DecodeOnBoardRequest`/`usp.EncodeNotifyResp` (Task 2) are consumed by name in Task 5's Contract. `RefreshLiveness` (Task 4) consumes `usp_agents.connected` from Task 1's schema, no other new column.

**Ordering.** 1 → 2 can run in parallel with 1 (no dependency between migration and pure-protocol-codec work) but both must land before 3. 3 → 4 (liveness reaper needs the table). 3, 2 → 5. 5 → 6 (CI needs the DSN requirement to exist before it can be fixed for it). Sequential 1→ 2 → 3 → 4 → 5 → 6 is safe and simplest; a controller running this via subagent-driven-development may dispatch 1 and 2 in either order but should not start 3 before both are committed.
