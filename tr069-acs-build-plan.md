# TR-069 ACS Platform — Build Plan

Status: Draft  
Date: 2026-08-04 (§§1-8 below); **updated 2026-08-11 to add §9 and §10 — see those for everything built since**  
Source of truth: `tr069-acs-application-design-v3.md`  
Scope: How to actually build the platform described in v3 — repo layout, UI design direction, phase-by-phase deliverables, and the decisions that need to be made before code starts.

This document does not repeat protocol/architecture rationale already covered in v3. It translates v3 into buildable work.

**Reading this document today**: §§1-8 are the original Phase 0-8 build narrative (CWMP core through BSS/CRM integration) and are still accurate as history, but they stop at migration `0026_config_templates.sql`. A further "admin platform backlog" — RBAC tiers, multi-tenancy, per-user dashboards, device CLI/VPN/web-GUI remote access, BSS OAuth2, Excel reporting — was built after this doc was last touched (migrations `0027`-`0039`) and was undocumented here until this pass. §9 covers it. §10 is the corrected, current outstanding-items list — read that instead of §8's "Immediate next actions" (historical, Phase-0-era, effectively all done) and instead of chasing "flagged as an open item" callouts scattered through §§1-8, some of which §9 has since closed.

---

## 1. Decisions needed before Phase 0 starts

v3 deliberately leaves these open. They block repo scaffolding, so they should be resolved first (or defaulted, with the default stated here).

| Decision | v3 recommendation | Default if not decided | Impacts |
|---|---|---|---|
| Backend language | Go (production) or Python/FastAPI (prototype) | **Go** — matches "production-oriented stack" in v3 §3 | Repo layout, CWMP XML/SOAP libraries, deployment |
| Frontend framework | Not specified in v3 | **React + TypeScript** | UI section below |
| Table/grid library | Not specified in v3 | **TanStack Table** (headless, virtualized, sorting/filtering built for dense fleet data) | UI performance at 18k+ devices |
| Infra target (v1) | S3-compatible + Postgres + Redis + Kafka/RabbitMQ | **Docker Compose locally, cloud-agnostic** (Postgres, Redis, MinIO, RabbitMQ) | Phase 0–1 dev environment |
| Monorepo vs split repos | Not specified | **Monorepo**: `/backend`, `/frontend`, `/docs` under this folder | Repo layout below |
| BSS adapter topology | Not in v3 (new scope, added via `Design.txt`/`BSS integration guide.md`) | **Separate `cmd/bssadapter` process**, calling the existing internal ACS REST API over HTTP — matches the provided architecture diagram (BSS Integration Adapter/Gateway as its own box, talking to "ACS Core & Job Manager" over the internal REST API) and the reference `internal_bss_adapter.go` draft | Phase 8 repo layout, auth boundary (BSS-facing vs internal) |

If any of these should go the other way, flag it before Phase 0 scaffolding — changing backend language later means redoing the CWMP XML/SOAP layer.

---

## 2. Repository Layout

Expands v3 §18 (Go-oriented layout) and adds the frontend, which v3 does not cover.

```text
conceptual_alternatives/ACS/
├── tr069-acs-application-design.md        (v1, superseded)
├── tr069-acs-application-design-v3.md     (source of truth)
├── tr069-acs-build-plan.md                (this file)
│
├── backend/
│   ├── cmd/
│   │   ├── acs/         (CWMP gateway process)
│   │   ├── api/         (REST API process)
│   │   ├── worker/      (job dispatch worker)
│   │   └── bssadapter/  (Phase 8: BSS-facing gateway — /bss/v1/* routes)
│   ├── internal/
│   │   ├── cwmp/        (soap.go, inform.go, rpc.go, fault.go, xml.go, session.go, timeout.go)
│   │   ├── auth/        (mtls.go, digest.go, jwt.go, credentials.go)
│   │   ├── devices/     (device.go, repository.go, adapter.go, adapters/{tr181,tr098,huawei,nokia,teltonika,zyxel}.go)
│   │   ├── jobs/        (job.go, queue.go, worker.go, retry.go, lease.go)
│   │   ├── firmware/    (image.go, repository.go, download.go)
│   │   ├── api/         (devices.go, jobs.go, firmware.go, diagnostics.go, middleware.go)
│   │   ├── bss/         (Phase 8: mapping.go, order.go, template.go, webhook.go — see §5)
│   │   ├── observability/ (metrics.go, tracing.go, audit.go)
│   │   ├── store/       (postgres.go, redis.go)
│   │   └── config/
│   ├── migrations/      (SQL from v3 §7, one file per table)
│   └── test/
│       ├── fixtures/    (golden XML: Inform, InformResponse, GetParameterValues, Download, TransferComplete, Fault)
│       └── mockcpe/     (mock CPE emulator, v3 §15.3)
│
├── frontend/             (Vite + React 19 + TypeScript — built, see §6)
│   ├── src/
│   │   ├── screens/      (DeviceFleet, FleetControl, Jobs, DeviceDetail)
│   │   ├── components/   (DataTable — virtualized + selectable, StatusBadge)
│   │   ├── api/          (types.ts + client.ts, typed fetch wrapper over cmd/api)
│   │   └── lib/          (format.ts — timeAgo/fmtTime/durationSeconds)
│   └── package.json      (no mock/ — backend existed before frontend work started)
│
└── infra/
    └── docker-compose.yml   (Postgres, Redis, MinIO, RabbitMQ — Phase 0/1 dev stack)
```

---

## 3. UI Design Direction — Data-Dense / Tabular

### 3.1 Why this style fits

The operators of this console are network engineers managing a fleet that v3's own example puts at **18,432 devices** (§8.1 example response). They need to scan, sort, filter, and compare — not browse cards. A dashboard-of-widgets style (KPI tiles, charts-first) actively works against that task. The UI should read like a well-built ops tool (think: a NOC console, not a marketing dashboard): grids first, chrome minimal, every screen answers "which devices/jobs need my attention right now."

### 3.2 Core patterns

```text
Table-first layout — every primary screen is a grid, not a card list.
Dense rows — compact row height, no card padding, information over whitespace.
Sortable + filterable columns on every list screen (status, vendor, model,
  data_model_root, firmware version, last_inform_at).
Persistent filter bar above the grid, not buried in a modal.
Monospace font for identifiers: device UUIDs, OUI+Serial, CommandKey,
  parameter paths (Device.WiFi.SSID.1.SSID).
Status via compact colored badges inline in the row, not separate icons/cards.
Expandable rows instead of navigation-away for secondary detail
  (e.g. expand a job row to see its rpc_messages without leaving the jobs table).
Sticky header row + sticky first column (device identity) on scroll.
Virtualized rendering — the devices table must stay responsive at fleet scale;
  render only visible rows (TanStack Table + virtualizer, not pagination-only).
Keyboard navigation: row up/down, Enter to expand, `/` to focus filter bar.
No decorative charts on primary screens. Trend/health charts (Prometheus data,
  v3 §16.1) belong on a separate fleet-health screen, not mixed into the device grid.
```

### 3.3 Screen inventory

Maps directly to the REST API surface in v3 §8, so each screen has a real backend contract already defined — no UI screen exists without a corresponding endpoint.

| Screen | Backend source (v3 §) | Primary grid columns |
|---|---|---|
| **Device Fleet** | 8.1 `GET /devices` | oui_serial, manufacturer, model, data_model_root, online_status, connection_request_mode, software_version, last_inform_at |
| **Device Detail** | 8.2, 8.3 | Parameter cache table (path, value, source, as_of) + expandable session/job history |
| **Jobs** | 8.4–8.7, 7.4 | command_key, device, type, status, attempts, created_at, updated_at — expandable to rpc_messages (7.5) |
| **Firmware & Rollouts** | 9.1–9.5 | firmware_images table + rollout state grid (PENDING/ELIGIBLE/QUEUED/DOWNLOADING/SUCCESS/FAILED/SKIPPED/BLOCKED) with canary_percentage, maximum_failure_rate controls |
| **Sessions / RPC Log** | 5.1, 7.3, 7.5 | session state, device, opened_at, in-flight RPC, direction (ACS_TO_CPE/CPE_TO_ACS), fault_json — this is the debugging screen for protocol issues |
| **Diagnostics** | 8.7, §10 | job-driven ping/traceroute results, WiFi associated-device grid |
| **Credentials** | §11.3–11.6, 7.2 | credential_type, version, status (PENDING/ACTIVE/GRACE/REVOKED/EXPIRED), rotation controls |
| **Audit Log** | §11.8 | append-only grid: operator_id, action, device, command_key, before/after diff — filterable by actor and action type |
| **Fleet Health** | §16.1 (Prometheus metrics) | the one screen where charts are appropriate — inform rate, RPC fault rate, connection request success rate, device online/offline/unreachable counts |

### 3.4 Component notes

- `DataTable`: single shared component wrapping TanStack Table + row virtualization; every screen above is an instance of it with different column defs.
- `StatusBadge`: one component covering all enum-like fields (`online_status`, job `status`, credential `status`, rollout state) — color mapping centralized so SUCCESS/FAILED/TIMEOUT etc. are visually consistent across every screen.
- `CommandKeyCell`: renders `command_key` as a link that expands the job's RPC history inline — this is the thread that ties REST request → job → RPC → TransferComplete together (the correlation v3 §16.3 calls out as important for OTA jobs).
- `ParamDiffView`: renders the audit log's before/after JSON (§11.8) as a compact diff, not raw JSON.
- Frontend ships against `frontend/src/mock/` fleet data until the backend REST API exists (Phase 2+), so the UI can be built and reviewed in parallel with backend work instead of blocking on it.

---

## 4. Phased Build Plan

Directly follows v3 §14 phase goals and acceptance criteria; adds concrete file/module targets and where the frontend enters.

### Phase 0 — Lab Harness and Device Probes (backend only)

No UI. Goal per v3 §14: learn real fleet behavior before building anything else.

Build: `backend/cmd/acs` minimal listener + `internal/cwmp/{soap,inform,fault}.go` + probe scripts for `GetRPCMethods` / `GetParameterNames`.

Deliverable: device compatibility matrix (v3 §14 Phase 0 table) — this directly resolves prerequisites P1/P2 from v3 §13 and should be committed to this repo as `docs/device-compatibility-matrix.md`.

**Gate: do not start Phase 1 until at least one real CPE has been probed.** Everything downstream (data model adapters, RPC support assumptions) depends on this.

### Phase 1 — Minimal CWMP ACS

Backend: `internal/cwmp/session.go`, device registry upsert, `migrations/001_devices.sql` (v3 §7.1).  
Frontend: none yet, or a read-only stub of the Device Fleet screen against `mock/` data to validate the table design early.

### Phase 2 — Read/Write Parameters

Backend: serial RPC dispatch (v3 §5.4 pseudocode — one in-flight RPC per session, this is the corrected model from v2→v3, must not regress to the parallel-dispatch bug), `jobs` + `rpc_messages` tables (§7.4, §7.5), REST endpoints §8.3.  
Frontend: **Done.** Device Fleet, Device Detail, and Jobs screens are a real React + TypeScript + Vite app (`frontend/`) wired to the live API — not the `mock/` stub originally planned; the backend existed by the time frontend work started, so it skipped straight to real data. `mock/` was never created. See §6 for what actually got built, including a scope addition (Fleet Control) that wasn't in the original plan.

### Phase 3 — Connection Request

Backend: reachability mode tracking (§12.2), connection_request fields on `devices` (§7.1).  
Frontend: connection_request_mode badge on Device Fleet/Detail, Queue Connection Request action — done, part of the same frontend build as Phase 2 (§6).

### Phase 4 — Firmware OTA

Backend: **Done, MVP scope.** Firmware image repository (`internal/firmware`), Download/TransferComplete protocol handling (§9.2/§9.3), `AWAITING_TRANSFER_COMPLETE` job state. **Not built**: rollout tables (§9.5 — `firmware_rollout`/`firmware_rollout_device`, canary %, maintenance windows). v3's own MVP definition (§17) puts the rollout/canary engine in "Later," not "Must have" — this phase covers single-device Download/TransferComplete only, matching that scope explicitly rather than silently skipping it.  
Frontend: not built this pass (Firmware & Rollouts screen still ahead).

**Firmware storage substitution**: v3 §9.4 specifies S3/MinIO/CDN. This build uses local disk (`internal/firmware.Storage`) instead — a deliberate lab-scope stand-in, not an oversight. The interesting, protocol-correctness part of this phase is Download/TransferComplete and the job-state distinction, not which object store sits behind the URL; a real deployment swaps `Storage`'s implementation for an S3 client without anything above it (repository, job dispatch, REST handlers) needing to change, since they only ever see a URL.

**Two things this phase's own architecture forced, worth keeping in mind for Phase 5+ if any future RPC has the same shape**:
1. `TransferComplete` correlates to a job by `CommandKey` embedded in the message, not by session state — the CPE may not send it until a session well after the one that dispatched `Download` (fetch/flash/reboot can take minutes). `cmd/acs` checks for it *before* falling into the normal session-dispatch path for exactly this reason. Verified live: queued a `Download`, dispatched it, then sent `TransferComplete` in a completely separate `Inform` session and confirmed the original job still resolved correctly by `CommandKey`.
2. Per TR-069, `TransferComplete`'s `FaultStruct` element is present on *every* message, success included — `FaultCode` "0" means no fault, not "no `FaultStruct`." Checking `FaultStruct != nil` alone would have silently treated every successful firmware update as a failure. Caught while writing the golden fixture, not in review — `TransferComplete.IsFault()` is the correct check, not a nil check.

### Phase 5 — Diagnostics

Backend: **Done — Ping and Traceroute.** `DIAGNOSTICS_PING`/`DIAGNOSTICS_TRACEROUTE` job types + `POST /api/v1/devices/{id}/diagnostics/{ping,traceroute}` (§10.1). WiFi associated-device convenience endpoint also done. See "Phase 4/5/8 firm-up" below.  
Frontend: **Done** — a Diagnostics panel in Device Detail, see below.

**Architecturally different from every prior job type**: TR-069 diagnostics aren't a request/response RPC at all — the ACS writes the diagnostic's input parameters (`Host`, `NumberOfRepetitions`, `Timeout`, `DataBlockSize`, `DSCP`) plus `DiagnosticsState=Requested` via an ordinary `SetParameterValues`, then has to poll `GetParameterValues` on `DiagnosticsState` (and the result parameters) until it leaves `"Requested"`. One job/`CommandKey` has to survive trigger → poll → poll → ... → terminal without spawning children, which none of Phase 2–4's job types needed.

**Design**: `job.Attempts` (already incremented by `Lease` every pickup) is reused as the phase discriminator — attempt 1 renders the trigger `SetParameterValues`, attempts ≥ 2 render the poll `GetParameterValues`. A new `Repository.Requeue` cycles a job back to `QUEUED` (without touching `attempts`) instead of finalizing it, so `dispatch`'s existing "complete current job, then immediately lease the next one" loop naturally re-dispatches the poll within the *same* HTTP response when the CPE and ACS stay in the same session. `max_attempts` is overridden per-job (`Repository.CreateWithMaxAttempts`, 15 for pings — the jobs table's default of 3 is far too low for a poll loop) so a device whose `DiagnosticsState` never leaves `"Requested"` times out instead of polling forever, rather than being open-ended.

**Verified live**, both paths, via a hand-built CWMP session simulation (real HTTP POSTs to `cmd/acs` with session cookies, not unit tests):
- Happy path: queued a ping job, dispatched the trigger (confirmed the rendered `SetParameterValues` carried all five input params plus `DiagnosticsState=Requested` and `ParameterKey=CommandKey`), simulated the CPE completing it, confirmed the *same response* immediately carried the first poll `GetParameterValues`, simulated `DiagnosticsState=Requested` (still running) and confirmed a second poll was dispatched in-response, then simulated `DiagnosticsState=Complete` with `SuccessCount`/`FailureCount`/response-time results — job resolved `SUCCESS`, all six `Device.IP.Diagnostics.IPPing.*` result parameters landed in `device_parameter_cache`, and `GET /api/v1/jobs/{command_key}` reported the same.
- Cap path: fast-forwarded a job to `attempts=14`, confirmed one more poll still went out (`attempts=15`, under the cap), then confirmed the *next* still-`Requested` response finalized the job as `TIMEOUT` with no further RPC dispatched — the cap actually stops the loop rather than just existing in code.
- Confirmed `404` for an unknown device and `400` for a missing `host` on the REST endpoint.

### Phase 6 — Security Hardening

Backend: **Done.** Operator auth (JWT + RBAC), mTLS, credential versioning/rotation (§11.3–11.6), rate limiting (§7 below), and SOAP/XML hardening are all built and live-verified — see below for each.  
Frontend: **Login done** (below). Credentials screen, Audit Log screen not built this pass (audit logging itself already exists from Phase 1 per v3's "basic audit logging" acceptance criterion — these would be the *management UI* for it).

**Operator auth (JWT + RBAC), done**: closes the biggest concrete gap flagged in the implementation-status open items — `cmd/api` had no authentication at all. v3 §11.3 calls this credential class 4 ("REST/API operator ... OIDC/JWT rotation"), but there's no external identity provider in this lab, so `cmd/api` is its own minimal issuer:

- New `operators` table (migration 0012): username, bcrypt password hash, one of three roles (`readonly` < `operator` < `admin`).
- `POST /api/v1/auth/login` exchanges username/password for an 8-hour JWT. `POST /api/v1/auth/operators` (admin-only) creates further operators. The very first admin is created from `ACS_BOOTSTRAP_ADMIN_USERNAME`/`ACS_BOOTSTRAP_ADMIN_PASSWORD` at startup, since creating an operator normally requires already being an admin.
- JWT signing (`internal/auth/jwt.go`) is hand-rolled against `crypto/hmac`/`crypto/subtle` rather than pulling in a library — HS256 is a direct fit for stdlib, the same call already made for Digest auth and the CWMP SOAP layer. Password hashing is **not** hand-rolled — `golang.org/x/crypto/bcrypt` is a new dependency for exactly that.
- Every route now has a minimum role (`GET`s need `readonly`, writes need `operator`, operator management needs `admin`), enforced by a `requireRole` wrapper — except the firmware file-serve route, which stays public because it's fetched by CPEs via their `Download` RPC URL, not by an operator.
- **Auth is opt-in via `ACS_JWT_SIGNING_SECRET`**, matching the exact "off unless configured, loud warning when it is" convention `internal/auth.DigestAuthenticator` (CPE Digest) and `cmd/bssadapter`'s bearer token already use. Unset, `cmd/api` behaves exactly like Phases 1–5 — this was a deliberate choice so the already-built frontend (which has no login UI yet) keeps working without changes; enabling real enforcement is one env var away, not a breaking change.
- `operatorFromRequest` (used for `jobs.created_by` and audit actors since Phase 2) now reports the real authenticated username instead of the hardcoded `"operator"` placeholder it used since Phase 2, when auth is enabled.

**Verified live**, both with auth off and on: confirmed unauthenticated requests still succeed when `ACS_JWT_SIGNING_SECRET` is unset (back-compat); with it set, confirmed `401` with no token, `401` on a wrong password, `200` + valid JWT on correct login; confirmed an admin can create a `readonly` operator, that operator can read (`200`) but is `403`'d on both a write endpoint and the admin-only operator-creation route; confirmed `OperatorLogin`/`OperatorCreated` audit records show the real actor; confirmed a job queued by an authenticated admin records `created_by = "admin"`, not the old placeholder.

### Frontend login — done

The frontend had no way to use operator auth at all until now — every screen called the API unauthenticated. New `src/auth/tokenStore.ts` (a module-level store outside React, since `api/client.ts`'s plain `request()` helper needs to read the current token on every call without being a component) + `AuthContext.tsx` (a thin React wrapper) + a `Login` screen. `client.ts` attaches `Authorization: Bearer <token>` when present and clears it on any `401` other than the login call itself (a login's own `401` is "wrong password," not "you need to log in").

The login gate is learned, not assumed: an `authRequired` flag starts `false` and flips `true` the first time a request 401s *or* a login succeeds — so a backend running with `ACS_JWT_SIGNING_SECRET` unset (still the default) never shows a login screen at all, and every existing screen keeps working with zero configuration changes. **Caught during Playwright verification, not by review**: `authRequired` initially wasn't persisted, reasoned as "each page load should re-derive it from real traffic." That broke sign-out after a page reload — a *valid* persisted token never triggers a `401`, so `authRequired` stayed `false` for that whole session, and clearing the token on logout didn't re-show the login screen because the gate's other half never went true. Fixed by persisting `authRequired` alongside the token; once a client has learned auth is required, it stays learned across reloads.

**Verified live** with a real Playwright browser session against a real auth-enabled backend: unauthenticated load shows the login screen; wrong password shows the real server-side error message inline; correct password logs in and shows the topbar's `username (role)` indicator plus real fleet data; a page reload keeps the session (persisted token); sign-out correctly returns to the login screen (the bug above, caught in this exact verification step, not before it). Also found and fixed a stale `frontend/.env.local` (a leftover from earlier port-conflict testing) silently overriding the correct `VITE_API_BASE_URL` from `.env` — `.env.local` takes precedence in Vite and had pointed the whole app at a dead port; removed.

**Not built this pass**: role-aware UI (a `readonly` operator still sees write-action buttons that will 403 on click — the backend is the real enforcement boundary, but the UI doesn't yet gray them out preemptively), and the Credentials/Audit Log screens.

### Frontend screens for Phase 7's new surfaces — done

Four new screens, each following the same established shape (a `panel` create-form above a `DataTable` list, reusing the exact same components/CSS every prior screen uses — no new design-system surface area): **Groups** (create, click a row to open a members panel, add/remove members by device ID, delete), **Scheduled Jobs** (create against a device or group target with a job-type-appropriate payload field, enable/disable, delete, shows live `next_run_at`/`last_run_at`), **Policies** (create a model-filter + parameter + desired-value rule, enable/disable, delete), **Rollouts** (create against a firmware image picker, click a row for live per-device state and failure-rate, Start/Advance buttons gated on rollout status matching the backend's own state machine). `StatusBadge`'s tone map extended with the rollout vocabulary (`ELIGIBLE`, `DOWNLOADING`, `BLOCKED`, etc.) rather than adding a second badge component.

**Verified live** with Playwright against a real running backend: created a group and confirmed it listed with the right member count; clicked into an existing 2-member group and confirmed the real device IDs rendered with working remove controls; created a policy, confirmed it listed, disabled it and confirmed the status pill flipped; loaded the Rollouts screen and confirmed the create form and firmware-image picker rendered. Zero console/page errors across all of it. Test data (groups/policies created purely for this verification pass) cleaned up from the database afterward rather than left behind.

### Audit Log screen, Credentials UI, role-aware UI, tags editor — done

Closes out the remaining frontend gaps from the previous pass:

- **Audit Log screen** (new): the append-only `audit_log` table has existed since Phase 1 and every write-shaped action has recorded to it the whole way through — this is the first REST read path (`GET /api/v1/audit-log`, `internal/observability.Auditor.List`) and the first UI onto it. Filterable by actor/device/details text and by action, virtualized (an audit log is exactly the "grows without bound" case build plan §3.2 calls for virtualization on).
- **Credentials UI** (new, in Device Detail rather than a standalone screen — credentials are inherently per-device, so this fits the existing drill-down panel better than a device-ID-input screen): Rotate/Activate/Revoke wired to the REST endpoints Phase 6 already built, same state machine, same masked-password guarantee (the frontend never receives or displays a password, matching what the backend already never sends).
- **Role-aware UI** (new): a `canWrite(role)` helper mirrors `internal/operators`' `readonly < operator < admin` hierarchy client-side. Every write button across every screen (Fleet Control's bulk-action apply, Device Detail's actions, Groups/Scheduled Jobs/Policies/Rollouts' create and mutate buttons) is now `disabled` for a `readonly` operator — pure UX, the backend `requireRole` check remains the actual enforcement boundary and was never bypassable from the UI even before this.
- **Tags editor** (new, in Device Detail): a comma-separated input wired to `PUT /devices/{id}/tags`, closing the one gap explicitly flagged after the Phase 7 frontend push (groups had a UI, the lighter-weight per-device `tags` column didn't).

**Verified live** with Playwright against a real auth-enabled backend: logged in as admin, created a `readonly` operator via the API (no UI for operator management exists yet — noted as the one remaining gap), confirmed the Audit Log screen renders real entries (300 real rows, spanning the actual session history — logins, scheduled-job fires, policy/group creations). Opened a real device's detail panel and confirmed both new panels render; rotated a credential and confirmed it appeared `PENDING`; saved tags and confirmed the save confirmation and persisted value. Signed out, logged back in as the `readonly` operator, and confirmed — with real form data filled in, not just an empty form — that both the Policies and Groups create buttons stayed disabled. Zero console/page errors. All test data (the readonly operator, leftover test groups, a stray recurring test schedule from an earlier verification pass that was still firing every interval) cleaned up from the database afterward.

### Phase 7 — Fleet Operations

Backend: **device groups/tags, scheduled jobs, canary firmware rollouts, and a policy engine — all done**, detailed below. Bulk operations already exist via Fleet Control (Phase 2's scope addition).  
Frontend: bulk action support in Device Fleet grid — **done** (Fleet Control). Fleet Health screen (§3.3 above) — **done**, see "Gap-filling pass" below. **Dashboards/alerting (Grafana/Prometheus wiring, §16.1) — done**. **Groups, Scheduled Jobs, Rollouts, and Policies screens — done**, detailed below.

### Scheduled jobs — done

Fixed-interval recurring dispatch (migration 0016), not a full cron expression engine — a deliberate lab-scope simplification: every real need here ("refresh WiFi client stats hourly") is expressible as an interval, and a cron parser is a dependency this doesn't need yet. A `scheduled_jobs` row names a job type, a target (one device or a `device_groups` group — resolved fresh at *dispatch* time, not frozen at schedule-creation time), a JSON payload, and an interval (minimum 60s, guarding against an accidental flood generator). `cmd/api`'s new `scheduleWorker` polls every 10s for due, enabled schedules (`LeaseDue`, same `FOR UPDATE SKIP LOCKED` shape `jobs.Repository.Lease` already uses), fans out to one real `jobs.Repository.Create` call per resolved device, and pushes `next_run_at` forward by the schedule's own interval in the same transaction.

**Verified live**: rejected a sub-60s interval (400). Created a device-targeted schedule, confirmed the worker fired within one 10s tick, created a real `GET_PARAMETER` job with `created_by = "scheduler:<name>"`, and advanced `next_run_at`/`last_run_at` correctly. Disabled it and confirmed it stayed at exactly one job total even after its next scheduled fire time passed. Created a group-targeted schedule against a 2-member group and confirmed it fanned out to both members in one fire.

### Firmware canary rollouts (§9.5) — done

The rollout tables explicitly deferred from Phase 4's MVP scope. New `firmware_rollout`/`firmware_rollout_device` tables (migration 0017) implement v3's full control vocabulary: `canary_percentage`, `maximum_failure_rate`, `model_filter`, `current_version_filter`, a UTC maintenance window, and a `rollback_firmware_image_id` field. Eligibility (`model_filter`/`current_version_filter` against `devices` + the cached `Device.DeviceInfo.SoftwareVersion`) is computed once at rollout creation, a snapshot — the target set doesn't shift under a running rollout. Per-device state is deliberately *not* a stored, driftable column: `ELIGIBLE` means `job_id IS NULL`; everything past that (`QUEUED`/`DOWNLOADING`/`SUCCESS`/`FAILED`) is derived live from the linked `FIRMWARE_DOWNLOAD` job's own `jobs.status` — the single source of truth Phase 4 already built correctly (Download vs. TransferComplete), not duplicated here.

Two dispatch endpoints implement the actual rollout mechanics: `POST .../start` queues `canary_percentage`% of eligible devices (minimum 1 — a percentage that rounds to zero on a small fleet still gets a real canary), refusing outside the configured maintenance window. `POST .../advance` computes the failure rate among *terminal* dispatched jobs only (still-downloading devices don't count against the rate before they've resolved either way) and either dispatches every remaining eligible device, or — if the rate exceeds `maximum_failure_rate` — flips the rollout to `BLOCKED` and refuses, a real gate, not a stored-but-unenforced number.

**Not built as of this section** *(closed — see "Phase 4/5/8 firm-up" below)*: automatic rollback execution (the `rollback_firmware_image_id` field is stored but nothing auto-fires a downgrade on `BLOCKED`), and multi-wave progression beyond one canary batch + one "advance to everyone else" step.

**Verified live**: created a rollout filtered to a real 2-device subset of the seeded fleet, started it (canary batch of 1, the min-1 floor kicking in on a tiny eligible set), confirmed the rendered state derivation. Manually resolved the canary job `FAILED` and confirmed `advance` computed a 100% failure rate, correctly refused (409) rather than propagating a bad build, and flipped the rollout to `BLOCKED`. Ran a second rollout end-to-end on the success path: canary `SUCCESS` → `advance` correctly dispatched the remaining device. Confirmed the maintenance-window gate refuses `start` when the current UTC time falls outside a configured window.

### Policy engine — done, deliberately scoped

v3's Phase 7 goal list gives no detail beyond the words "Policy engine" — this pass picked a concrete, defensible interpretation rather than something open-ended: **continuous compliance enforcement**. A `policies` row (migration 0018) says "devices matching this filter should report this parameter as this value." `cmd/acs` checks every Inform's *actually-reported* parameters against matching enabled policies (`cmd/acs/enforce.go`) and queues a correcting `SET_PARAMETER` the moment one has drifted — distinct from both scheduled jobs (time-triggered) and rollouts (one-time push): this re-evaluates on every single check-in, indefinitely, so a config reset or factory-default reflash gets corrected automatically the next time that device phones home, no operator action required.

Deliberately only acts on parameters the Inform actually reported — a policy for a parameter this particular check-in didn't include is neither confirmed-compliant nor confirmed-drifted, so nothing fires for it that cycle. No explicit dedup-against-in-flight-jobs query: periodic Informs are minutes apart and a queued `SET_PARAMETER` plus its Phase 2 auto-confirm `GET_PARAMETER` normally resolves within one session, so the cache already reflects the correction by the next Inform — a stated tradeoff, not an unexamined gap.

**Verified live**: created a policy (`DiagTestVendor` devices must report a specific WiFi SSID), sent a real Inform reporting a drifted value from a matching manufacturer, and confirmed a real `SET_PARAMETER` job was auto-queued with `created_by = "policy:<name>"` and a masked-nothing (no secrets involved) audit record showing both the reported and desired values. Confirmed two negative cases produce *zero* action: an Inform reporting the already-compliant value, and an Inform from a non-matching manufacturer reporting a drifted value — the enforcement job count stayed at exactly one throughout both.

**Device groups & tags, done**: two deliberately separate mechanisms, not one. `device_groups`/`device_group_members` (migration 0013) are a curated, named set an operator builds up over time — the actual payoff is `POST /api/v1/devices/bulk-actions` now accepting `group_id` as an alternative to `device_ids`, resolved server-side via `devices.GroupRepository.MemberDeviceIDs` before the existing per-device fan-out logic (untouched — groups only change how the target list is built, not how bulk dispatch works). `devices.tags` is a plain `TEXT[]` column with a GIN index, replace-the-whole-set on write via `PUT /devices/{id}/tags` — freeform labels don't need a join table's curated-membership semantics the way groups do.

REST surface: `POST/GET /api/v1/device-groups`, `GET/DELETE /api/v1/device-groups/{id}`, `POST /api/v1/device-groups/{id}/members` (idempotent — re-adding an existing member is a no-op via `ON CONFLICT DO NOTHING`, not an error), `DELETE /api/v1/device-groups/{id}/members/{device_id}`. Group deletion cascades to membership rows (`ON DELETE CASCADE`) but never touches the devices themselves.

**Verified live**: created a group, hit the duplicate-name conflict (409), added two real devices as members, ran a `CONNECTION_REQUEST` bulk action by `group_id` and confirmed via direct Postgres query that jobs were created for exactly those two devices and no others. Set tags on a device and confirmed they round-tripped through `GET /devices/{id}`. Removed one member (204, `member_count` dropped to 1), deleted the group (204, then 404 on re-fetch), and confirmed a bulk-action call against the now-nonexistent `group_id` correctly falls through to the same "nothing to target" 400 as an empty `device_ids` list, rather than silently doing nothing or erroring differently.

**Dashboards & alerting, done**: v3's Phase 7 goal list includes "Dashboards" and "Alerting" alongside the fleet-management items — this pass built that slice, deliberately choosing it over device groups/canary rollouts/a policy engine (each of those is itself another multi-decision phase; this one was self-contained and, unusually for this build, pulls in infrastructure beyond Go+Postgres).

- New `internal/observability/metrics.go`: a per-process Prometheus registry (`prometheus/client_golang`), one per service (`cmd/acs`, `cmd/api`, `cmd/bssadapter`) rather than the package's global default registerer. Each service exposes `GET /metrics` — deliberately unauthenticated wherever auth exists elsewhere (a scraper has no operator JWT or bearer token to present), the same treatment as the firmware file-serve route.
- `cmd/acs`: domain counters, not just HTTP counters, since it has exactly one HTTP route (`/cwmp`) — Informs, sessions opened, jobs created/completed by type+status (centralized in `markJobSuccess`/`auditFailure`/`requeueDiagnostic`'s timeout branch, so every job-completion path is covered without hunting down each call site), and an `acs_devices_online` gauge refreshed every 15s from a new `devices.Repository.CountByOnlineStatus` — a periodic poll rather than updating on every Inform, so a Prometheus scrape never costs the hot CWMP request path anything.
- `cmd/api` and `cmd/bssadapter`: per-route HTTP request count + duration, wired at every route registration (a `route(method, pattern, minRole, fn)` helper in `cmd/api` composes metrics instrumentation with the existing `requireRole` wrapper so neither can be registered without the other). `cmd/bssadapter`'s rate limiter now increments `acs_rate_limit_rejected_total` on every 429.
- `infra/docker-compose.yml` gained `prometheus` and `grafana` services. Prometheus scrapes the three Go services via `host.docker.internal` (they run on the host via `go run`, not in the compose network). Grafana is provisioned as code — a datasource (`infra/grafana/provisioning/datasources/`, pinned `uid: prometheus-ds` so dashboard JSON can reference it deterministically) and one dashboard (`infra/grafana/provisioning/dashboards/json/fleet-health.json`): devices online/offline, CWMP Inform/session rate, job completions by type+status, HTTP requests by service+route, rate-limit rejections.
- `infra/alert_rules.yml`: three Prometheus alert rules (`NoDevicesOnline`, `HighJobFailureRate`, `RateLimitRejectionsFiring`), evaluated by Prometheus itself. **No Alertmanager or notification channel wired up** — that would mean sending real alerts to Slack/email/PagerDuty without being asked to configure a destination. This is the buildable, honestly-scoped slice of "alerting" for a lab build with no real on-call: the rule engine runs, thresholds evaluate against real metrics, and a firing alert is visible under Prometheus's own `/alerts`.

**Verified live, the whole pipeline, not just the pieces**: all three `/metrics` endpoints return real data (confirmed `acs_devices_online` showing the true live fleet count from the periodic poll). Prometheus's own target list shows all three scrape jobs `up`. Generated real traffic against `cmd/api` and `cmd/bssadapter` and confirmed the exact counts showed up in a live Prometheus query. Deliberately drove `cmd/bssadapter`'s rate limiter past its burst and confirmed `acs_rate_limit_rejected_total` incremented *and* the `RateLimitRejectionsFiring` alert transitioned to `state: firing` in Prometheus's `/api/v1/alerts` — not just that the rule file parsed. Confirmed Grafana's provisioned datasource and dashboard exist via its API (6 panels, correct titles), then queried live data *through Grafana's own datasource proxy* (not just directly against Prometheus) to confirm Grafana can actually reach and query Prometheus, not merely that both happened to start.

**Caught while verifying, not by code review**: `Stop-Process` on the `go run` wrapper process doesn't kill the compiled child binary it spawns — the child keeps holding the port. A rate-limit config change appeared to do nothing (still no 429s at `burst=2`) because traffic was silently still hitting the old orphaned process the whole time. Found by checking the startup log's own echoed config against what was actually running, not by re-reading the code — the code was already correct.

### Phase 8 — BSS/CRM Integration

Goal: let a BSS/CRM (Salesforce Comm Cloud, Amdocs, Netcracker, custom operator CRM) provision, link, and manage the accounts and devices on this platform, without the ACS ever needing to know who a customer is (design principle from `BSS integration guide.md` §1: "Decoupled Identity" — the ACS stays keyed on `Device UUID`/`OUI+SerialNumber`; the BSS owns the account relationship).

This phase has no v3 section to defer to — it's new scope, grounded in the three reference documents provided (`Design.txt`, `BSS integration guide.md`, `internal_bss_adapter.go`). Full design in §5 below. Depends on Phase 2 (job queue/parameter writes — done) and benefits from Phase 3 (Connection Request) for low-latency order fulfillment, though it works without it via periodic-Inform fallback.

`cmd/bssadapter` request bodies are now capped at 1 MiB (`withMaxBody` middleware, matching `cmd/acs`'s 4 MiB CWMP cap) — closes the "unbounded request bodies" gap this doc previously flagged. Verified live: a 2 MiB body against the cap is rejected with 400 before reaching mapping/order logic; a normal small request still passes through to real handler logic untouched.

---

## 5. BSS/CRM Integration (Phase 8) — Detailed Design

### 5.1 Architecture

Per `Design.txt`'s diagram, the BSS adapter is a distinct service, not folded into `cmd/api`:

```text
BSS/CRM (Salesforce, Amdocs, Netcracker, custom)
  │  TM Forum / standard REST JSON (TMF640 Service Activation, TMF638 Service Inventory)
  ▼
cmd/bssadapter  — Account-Device Mapping · Service Templates · Webhook Engine
  │  Internal ACS REST API (202 Accepted) — the same PUT/GET .../parameters
  │  and GET /jobs/{command_key} endpoints Phase 2 already built
  ▼
cmd/api / cmd/acs — ACS Core & Job Manager
  │  CWMP HTTPS/SOAP, serial RPCs
  ▼
CPE Fleet
```

Why a separate process rather than routes on `cmd/api`: the BSS-facing surface has a different trust boundary (external B2B system-to-system auth, §5.5), a different API contract (TM Forum shapes, not the internal device/job model), and different scaling/deployment concerns (a BSS integration outage or a slow webhook target shouldn't be able to affect the CWMP gateway's ability to talk to CPEs). This mirrors the credential-class separation the design doc already anticipates in v3 §11.3 item 5 ("internal service-to-service credentials").

### 5.2 Durable account-device mapping (Workflow A)

The reference `internal_bss_adapter.go` stores mappings in an in-memory map (its own comment flags this: "Production should use PostgreSQL"). Phase 8 makes that real:

```sql
CREATE TABLE account_device_mappings (
    id UUID PRIMARY KEY,
    account_id TEXT NOT NULL,
    device_id UUID NOT NULL REFERENCES devices(id),
    oui_serial TEXT NOT NULL,
    service_plan TEXT,
    status TEXT NOT NULL DEFAULT 'ACTIVE'
        CHECK (status IN ('PENDING_ACTIVE', 'ACTIVE', 'SUSPENDED', 'TERMINATED')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (account_id, device_id)
);
CREATE INDEX account_device_mappings_account_idx ON account_device_mappings (account_id);
```

Endpoint: `POST /bss/v1/mappings` (Workflow A). Upsert on `(account_id, device_id)`, resolving `device_id` from the given `oui_serial` against the existing `devices` table — this is the one place the BSS adapter needs to know about ACS device identity, exactly as the decoupling principle intends.

### 5.3 Order dispatch and service templates (Workflow B)

`POST /bss/v1/orders` takes a TM Forum-shaped order, resolves the account→device mapping, translates the business `action` into canonical parameter writes, and queues a job.

**Service templates**, not a hardcoded switch statement: the draft's `translateServiceToACSParameters` is a `switch` over 3 actions. Phase 8 should make this a small registry (`internal/bss/template.go`) mapping `action → []CanonicalParameter`, reusing the canonical-parameter concept v3 §6.2 already defines for vendor path resolution — the same indirection (canonical name → adapter → actual CPE path) applies here one layer up (business action → canonical name → adapter → actual CPE path). This is what makes `MODIFY_WIFI` work identically whether the CPE is Device:2 or IGD:1, using the resolver already built.

**Idempotency gap in the draft**: `HandleBSSOrder` has no dedupe on `external_order_id`. A BSS will retry an order on timeout; without dedupe, a retried `MODIFY_WIFI` order creates a second `SET_PARAMETER` job with a new `command_key`, and the BSS's original `order_tracking_id` now maps to two jobs. Add an `orders` table keyed on `external_order_id`, returning the existing `command_key` on a duplicate submission instead of creating a new job.

```sql
CREATE TABLE bss_orders (
    external_order_id TEXT PRIMARY KEY,
    account_id TEXT NOT NULL,
    action TEXT NOT NULL,
    job_id UUID NOT NULL REFERENCES jobs(id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

**Correctness concern in the draft's `SUSPEND` template**: it sets `Device.IP.Interface.1.Enable = false`. That's the same WAN interface the CPE uses to reach the ACS at all — disabling it would likely cut off the CWMP session before `SetParameterValuesResponse` even arrives reliably, and could strand the device unreachable for the *reactivate* order too (v3 §19.5's whole point: don't rely on a single reachability path). A walled-garden approach (redirect via a firewall/NAT rule object, or a captive-portal parameter if the vendor exposes one) preserves CWMP reachability while still restricting service. This needs a real per-vendor answer during Phase 8, not a placeholder — flag it as an open question before building SUSPEND/ACTIVATE templates.

### 5.4 Job status and webhooks (Workflow C + §3 of the guide)

**Workflow C already works today** — `GET /api/v1/jobs/{command_key}` was built in Phase 2. One gap: the guide's example response includes `completed_at`, which the `Job` struct already stores (`CompletedAt`) but the current `jobResponse` in `cmd/api` doesn't expose. Small, immediate, additive fix — add it to `jobResponse` regardless of when Phase 8 itself starts, since it costs nothing and closes a real contract gap.

**Webhooks** are new infrastructure, not a wrapper around what exists: an outbox pattern, same shape as the jobs queue itself —

```sql
CREATE TABLE webhook_subscriptions (
    id UUID PRIMARY KEY,
    account_id TEXT,              -- NULL = fleet-wide subscription
    target_url TEXT NOT NULL,
    secret_ref TEXT NOT NULL,     -- HMAC signing secret, via secrets manager (v3 §11.7 pattern)
    event_types TEXT[] NOT NULL,  -- e.g. {'JOB_COMPLETED'}
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE webhook_deliveries (
    id UUID PRIMARY KEY,
    subscription_id UUID NOT NULL REFERENCES webhook_subscriptions(id),
    event_type TEXT NOT NULL,
    payload JSONB NOT NULL,
    status TEXT NOT NULL DEFAULT 'PENDING' CHECK (status IN ('PENDING', 'DELIVERED', 'FAILED')),
    attempts INTEGER NOT NULL DEFAULT 0,
    last_attempt_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

On job completion (`SUCCESS`/`FAILED`), enqueue a delivery row per matching subscription; a worker POSTs with HMAC signature, retry/backoff, and delivery-status tracking — the exact same durable-queue-plus-worker pattern already proven for CWMP jobs in Phase 2, applied to outbound HTTP instead of outbound CWMP RPCs.

### 5.5 BSS-facing authentication

Distinct from both CPE auth (Digest/mTLS, v3 §11.2) and operator RBAC (OIDC/JWT, v3 §11.5) — this is system-to-system B2B auth, the credential class v3 §11.3 already lists but Phase 6 hadn't detailed yet. Short-lived client-credentials OAuth2 token or mTLS client certificate per BSS integration, issued and rotated independently of CPE-facing credentials.

### 5.6 Error contract

Implement the guide's §4 mapping directly — a standard error envelope (`{"error": "ErrDeviceNotMapped", "message": "..."}`) so `400`/`404`/`502` map to the reasons the guide already documents (`ErrInvalidRequest`, `ErrDeviceNotMapped`, `ErrACSUnreachable`). Note: fix the typo in the draft's `ErrACSUunreachable` while implementing.

### 5.7 Sub-phase breakdown

```text
8a. Durable account-device mapping (§5.2) + POST /bss/v1/mappings, GET /bss/v1/mappings/{account_id}
8b. Order dispatch + service template registry + idempotency (§5.3) — resolve the
    SUSPEND design question before building it
8c. completed_at fix (do immediately, independent of the rest) + webhook engine (§5.4)
8d. BSS-facing auth (§5.5) + error contract (§5.6) + TM Forum shape conformance pass
```

---

## 6. Frontend (Phase 2/3) — What Actually Got Built

The frontend went straight from a static HTML mockup (validating the data-dense/tabular direction against a real data snapshot) to a real wired app once the backend existed to wire it against — `frontend/`: Vite + React 19 + TypeScript, `@tanstack/react-table` for the grid, `@tanstack/react-virtual` for row virtualization.

**Note on TanStack Table's version**: `npm install @tanstack/react-table` installs v9 by default now, which turns out to be a ground-up rewrite with a completely different API (`createCoreRowModel`, a `ReactTable` class, `TableFeatures` generic constraints — not `useReactTable`/`getCoreRowModel`/`flexRender`). That's not the stable, documented API this plan assumed. Pinned to `^8` instead. Worth knowing before anyone else on this runs a bare `npm install` and gets a different major version than what's actually wired up.

### 6.1 Screens built

| Screen | Backend | What it does |
|---|---|---|
| Device Fleet | `GET /devices` (paginated) | Review/inspect working-set scale (up to 500 devices in one virtualized page) — filter, sort, click a row to open Device Detail inline (parameter cache, recent jobs, Connection Request / refresh-cellular / set-SSID actions) |
| **Fleet Control** *(not in the original plan — added below)* | `GET /devices/summary`, `GET /devices` (paginated), `POST /devices/bulk-actions` | Mass review/control at real fleet scale |
| Jobs | `GET /jobs` | Fleet-wide job history, sortable/filterable, every status the system produces |

### 6.2 Fleet Control — the scope addition, and why

Mid-build, live review of the Device Fleet screen (156 real seeded devices) surfaced a real gap against this plan's own stated design: §3.2 says "virtualized rendering... not pagination-only," but the first cut of `DataTable` rendered every row into the DOM unconditionally — fine at 6 devices, not fine at the 18,432-device scale this plan's own example cites. That's a straightforward bug, fixed via `@tanstack/react-virtual` in the shared `DataTable` component.

The bigger gap it surfaced: **nothing let an operator act on more than one device at a time.** Every write endpoint (`PUT parameters`, `POST connection-request`, `POST refresh-cellular`) takes exactly one device ID. Reviewing "150 Huawei units, act on 40 of them" wasn't possible at any screen size, virtualized or not. That's Fleet Control — a genuinely separate screen (not just a mode toggle on Device Fleet), built around three new backend pieces:

- **`GET /api/v1/devices/summary`** — fleet counts grouped by vendor/status/reachability, computed with `GROUP BY` in SQL. This is what makes "review at a glance" cheap regardless of fleet size — the alternative (paging through every device to count client-side) is exactly what pagination exists to avoid.
- **`GET /api/v1/devices?page=&page_size=`** — v3 §8.1 already documented these query params; the original `List()` just never implemented them. Capped at 500/page (`maxPageSize`) so a client can't request the whole fleet in one response.
- **`POST /api/v1/devices/bulk-actions`** — `{device_ids: [...], action, ...}` fans out to N independent per-device jobs (capped at 500 devices/request — `maxBulkDevices`). Each device gets its own `command_key`; a failure on one (e.g. it no longer exists) doesn't block the rest — the response reports per-device outcome rather than one all-or-nothing result.

**Known scope boundary, stated plainly** *(closed — see "Gap-filling pass" below)*: row selection in Fleet Control accumulates across page changes (page through, keep selecting), but there's no "select all 4,000 matching this filter" without actually paging through them. That needs a backend select-all-matching query — a real further step, not implemented here, not silently pretended to exist.

### 6.3 Verified

Playwright (installed directly as a dev dependency — `chromium-cli` from the `run` skill assumes a Linux container this Windows environment doesn't have) drove the real dev server against the real backend: 156 devices seeded through the actual Inform pipeline, virtualization confirmed (31 DOM rows rendering 156 logical rows), group-filter → select-100-via-header-checkbox → bulk `CONNECTION_REQUEST` → confirmed via direct Postgres query that 100 real jobs were created, not just a UI success message. Both themes screenshotted and visually checked, not just asserted.

One real bug caught in this pass: swapping `@tanstack/react-table` from v9 to v8 mid-session while the Vite dev server was still running left a stale dependency-optimization cache, producing an "Invalid hook call" from two React module instances. Fixed by killing the dev server, clearing `node_modules/.vite`, and restarting — not a code bug, but the kind of thing that looks like one if you don't check.

---

## 7. API Security & Rate Limiting

Added once three separate HTTP-facing services actually existed to reason about (`cmd/acs`, `cmd/api`, `cmd/bssadapter`) — each has a different exposure and needs a different answer, not one blanket rate limiter.

### 7.1 Per-surface exposure and plan

| Surface | Faces | Current state | Rate-limit plan |
|---|---|---|---|
| `cmd/acs` (`/cwmp`) | The CPE fleet — cellular devices, potentially spoofable/compromised | Body size capped at 4 MiB (§build plan Phase 0), Digest auth optional until v3 P3 resolves | Per-device token bucket keyed on authenticated OUI+Serial (catches a misbehaving/compromised CPE hammering the endpoint); coarser per-IP limit ahead of auth to blunt an unauthenticated flood |
| `cmd/api` (operator REST) | Internal operators/tools | **No authentication at all yet** (v3 §11.5 OIDC/JWT is Phase 6 scope, not built) | Auth has to land *before* rate limiting is meaningful here — an unauthenticated limiter can only key on IP, which is weak for an internal API. Interim: per-IP limit as defense-in-depth now; per-operator-token quota once OIDC lands |
| `cmd/bssadapter` (`/bss/v1`) | External BSS/CRM systems — the most internet-adjacent surface in this design | Bearer token auth (interim, §5.5) | Per-token quota (identity already exists, unlike `cmd/api`) — highest priority given this is the explicitly external-facing integration point |

### 7.2 Gap found while planning this: unbounded request bodies on `cmd/bssadapter`

`cmd/acs` already wraps every request in `http.MaxBytesReader` (Phase 0). **`cmd/bssadapter`'s handlers don't** — `json.NewDecoder(r.Body).Decode(&req)` reads an unbounded body. For an externally-facing service this is a real gap, not a hypothetical one: an oversized `POST /bss/v1/orders` body could tie up memory/handler goroutines before any auth or business-logic rejection kicks in. Same fix as Phase 0 already established (`http.MaxBytesReader`), just not yet applied here — flagging it now rather than letting it sit unaddressed like `completed_at` did.

### 7.3 Implementation approach

- **`golang.org/x/time/rate`** (token bucket), same lightweight in-memory-map-plus-mutex style already used for `SessionStore` in `internal/cwmp` — no new infrastructure dependency for a single-instance deployment.
- **Caveat this creates**: an in-memory limiter is per-process. Phase 7 (Fleet Operations) is where running multiple replicas of a service first becomes a real scenario — at that point an in-memory limit stops being a *global* limit (each replica enforces its own), and a shared store (Redis — already in the stack per §1) becomes necessary. Fine to defer until Phase 7 actually needs it; wrong to forget the caveat exists.
- **A reverse proxy/WAF/CDN in front of all three services (production deployment, not this repo) is the first line of defense** and cheaper than building coarse IP-blocking in Go — application-level per-identity limiting here is the second line, for the abuse patterns a generic WAF can't distinguish (e.g. a legitimately-authenticated BSS integration submitting orders faster than intended).
- **Order-specific consideration for `cmd/bssadapter`**: beyond a general per-token API quota, consider a per-*device* limit on order submission specifically — orders ultimately consume a scarce downstream resource (the CPE's own CWMP session), so a burst of orders for one device can back up its job queue regardless of which token submitted them.

### 7.4 Sub-phase breakdown

```text
7a. Fix the unbounded-body gap on cmd/bssadapter (§7.2) — small, independent,
    do it whenever, same class of fix as the completed_at gap.
7b. Per-token rate limiting on cmd/bssadapter (highest exposure, auth already exists).
7c. cmd/acs per-device + per-IP limiting.
7d. cmd/api: ship alongside Phase 6's OIDC/JWT work, not before it — an
    unauthenticated rate limit on an internal API is a weak substitute for
    actual auth, not a reason to delay it.
```

**7a and 7b: done.** New `internal/ratelimit` package (`golang.org/x/time/rate` token bucket, same in-memory map-plus-mutex shape `internal/cwmp.SessionStore` already uses — process-local, with the Redis-once-multi-replica caveat from §7.3 still standing). Wired into `cmd/bssadapter` as `withRateLimit`, placed *after* `withAuth` deliberately: keying on the (already-verified) `Authorization` header means an attacker spraying bogus tokens can't dodge the bucket by generating a fresh key per request — each gets rejected at auth (401) before ever reaching the limiter. Falls back to `RemoteAddr` when auth itself is disabled (no `ACS_BSS_API_TOKEN` configured). Tunable via `ACS_BSS_RATE_LIMIT_PER_SECOND`/`ACS_BSS_RATE_LIMIT_BURST` (defaults 5/sec, burst 10).

**Live-verified** (Docker access came back later in the session): a bad bearer token gets `401` every single time under repeated hammering, never touching the limiter — confirming an attacker can't dodge per-token buckets by spraying fake tokens. A valid token under concurrent load (20 simultaneous requests against `rate=2/sec, burst=3`) produced a real mix of `404` (passed the limiter into business logic) and `429 ErrRateLimited`, exactly the token-bucket behavior expected — sequential, slowly-spaced curl calls initially looked like the limiter wasn't firing at all (each call's own process/TCP overhead gave the bucket time to refill between requests), which is itself a useful reminder that a token-bucket has to be *load-tested* concurrently, not polled one at a time. A well-spaced legitimate request for a real device serial still succeeded normally (`200`), confirming the limiter doesn't interfere with ordinary traffic.

**7c and 7d: done.** `cmd/acs` gained two limiters (`internal/ratelimit`, same package as 7b): a coarse per-IP bucket ahead of Digest auth and body parsing entirely (cheapest possible rejection for an unauthenticated flood), and a per-device bucket keyed on the Inform's natural key (before a session cookie exists) or the session cookie itself afterward — deliberately not a DB lookup on every request, since the cookie is 1:1 with a device for that session's lifetime. Burst is generous enough not to trip on a legitimate Phase 5 diagnostics poll loop's rapid-fire requests within one session. `cmd/api` gained a per-operator bucket, keyed on the JWT subject when auth is enabled and falling back to remote address otherwise — shipped *after* Phase 6's JWT work specifically because an unauthenticated limiter here could only key on IP, which §7.3 already called out as weak for an internal API. Both configurable via env vars, both tunable defaults documented in each `cmd`'s source.

**Verified live**: per-device CWMP rate limiting produced a real mix of `200`s and `429`s under concurrent load against a tight test limit, confirmed independent buckets (a second device's Inform was unaffected), and confirmed the rejection counter incremented on `cmd/acs`'s own `/metrics`.

### SOAP/XML hardening — done, and empirically proven, not assumed

Added three tests (`internal/cwmp/xml_hardening_test.go`) that construct real attack-shaped payloads — a classic XXE (`<!DOCTYPE>` with a `SYSTEM` external entity) and a "billion laughs" nested-entity expansion bomb — and confirm `ParseEnvelope` rejects both. A third test confirms the 5 predefined XML entities (`&amp;`, `&lt;`, etc., which real CPE payloads legitimately use) still parse correctly, so the hardening claim isn't accidentally also a functionality regression. The underlying reason both attacks are structurally unavailable: `encoding/xml.Unmarshal` is used directly with no custom `Decoder.Entity` map and no DTD/external-entity-fetching capability exists in the stdlib package at all — unlike libxml2-based parsers in other language ecosystems, there's no configuration flag to get this wrong. Body size is already capped at 4 MiB (`maxBodyBytes`, since Phase 0), which bounds the cost of any input regardless of shape.

### Credential rotation (§11.6) — done

The one credential-rotation flow v3 actually specifies as buildable: ACS-to-CPE Connection Request. (v3 §11.5 explicitly warns the CPE-to-ACS Digest direction often can't be rotated remotely at all, vendor-dependent — not attempted.) New `device_credentials` table (migration 0014): versioned, not in-place updates, because the flow needs a grace period and an audit trail neither of which survive an overwrite. State machine: `PENDING -> ACTIVE -> GRACE -> REVOKED`, with a partial unique index enforcing at most one `ACTIVE` row per device+credential-type at the database level, not just in application logic.

REST surface implements v3's 6-step flow as four calls: `POST .../credentials/rotate` (generate + queue `SetParameterValues` — steps 1-3), `GET .../credentials` (review before acting), `POST .../credentials/{id}/activate` (steps 4-5: switch the ACS's Connection Request client over, demote the previous `ACTIVE` row to `GRACE`, both in one transaction), `POST .../credentials/{id}/revoke` (step 6, only valid from `GRACE` or `PENDING` — an `ACTIVE` credential can't be revoked out from under itself). `cmd/api`'s `connreq_worker` now resolves a per-device `ACTIVE` credential before falling back to the shared credential every device used pre-rotation — "switch the client" is implemented as "look it up fresh every request" rather than mutating a long-lived client object, which needed no in-memory state at all. Passwords are generated with `crypto/rand` (never hand-rolled), and never appear in any REST response or audit record (`"password": "***"` — v3 §11.7/§11.8's masking requirement) — an operator never needs the value, only the ACS's own Connection Request client does.

**Verified live, the full 6-step flow**: rotated a real device's credential, confirmed the rendered `SetParameterValues` carried the generated username/password, simulated the CPE ack, confirmed the job showed `SUCCESS` via REST, activated it and confirmed `ACTIVE`. Rotated a *second* time and confirmed the transaction correctly demoted v1 to `GRACE` while activating v2 (checked the DB directly: exactly one `ACTIVE` row, invariant held). Revoked the `GRACE` credential and confirmed the terminal `REVOKED` state; confirmed attempting to revoke the currently-`ACTIVE` credential is rejected (404, not in a revocable state) rather than silently succeeding. Confirmed the audit log shows real actors and masked secrets throughout.

**Not built**: v3's canary-group/maintenance-window/automatic-rollback framing around rotation — this pass built the state machine and protocol mechanics, not a scheduling layer on top. Would compose with Phase 7's scheduled-jobs/canary-rollout work if built.

### mTLS (§11.2) — done

Optional, additive to Digest, not a replacement — `tls.ClientAuth: VerifyClientCertIfGiven` means the server always *offers* to receive a client certificate but never refuses a handshake for lacking one, so devices that can't do mTLS yet (prerequisite P3 unresolved per-vendor, v3 §13) keep working over Digest on the exact same endpoint. Enabled via `ACS_MTLS_CA_CERT`; any certificate a client *does* present is chain-verified against that CA during the TLS handshake itself, before `handleCWMP` ever runs — the handler only ever sees already-verified `PeerCertificates`. mTLS supersedes Digest for a given request when both could apply, matching v3's "mTLS preferred" framing. New `devices.cwmp_auth_mode` column (migration 0015) records `MTLS`/`DIGEST`/`NONE` from how each request *actually* authenticated, not from server config — a fleet can have some devices on each simultaneously.

**Verified live** with a real CA and certificates (generated via OpenSSL; a small Go test client was used instead of Windows' curl build, which is Schannel-based and can't import a plain PEM client cert the way an OpenSSL-linked client can): a request with a CA-signed client cert and *no* Digest header succeeded and recorded `cwmp_auth_mode = MTLS`; a request with no client cert at all still succeeded (dual-mode tolerance) and recorded `NONE`. A request with a certificate signed by a *different*, untrusted CA was rejected at the TLS handshake (`tls: unknown certificate authority`) — but only once the test client was forced to actually present it via `GetClientCertificate`. The first attempt at this test looked like a false pass (`200`) because Go's TLS *client* politely filters candidate certificates against the server's advertised acceptable-CA list itself and silently sent no certificate when the configured one didn't match — a real TLS behavior worth knowing, not a gap in the server-side check it initially looked like.

### Frontend interactivity pass — done

Not a build-plan phase item — a follow-up pass making the whole frontend feel live rather than click-to-refresh, requested directly rather than derived from the design doc. Four pieces, applied uniformly across every screen rather than as one-offs:

- **Toast notifications** (`src/lib/toast.ts` + `src/components/Toast.tsx`): a module-level pub-sub store (same pattern as `auth/tokenStore.ts`) so any handler can fire `toast(message, "success"|"error"|"info")` without a provider. Mounted once in `App.tsx`. Replaced the ad hoc inline `actionMessage`/banner state that Device Detail, Device Groups, Scheduled Jobs, Policies, and Firmware Rollouts had each been rolling separately — and along the way, fixed several call sites (`DeviceGroups.onDelete/onRemoveMember`, `ScheduledJobs.onToggle/onDelete`, `Policies.onToggle/onDelete`) that had no error handling at all: a failed delete would previously throw unhandled and silently leave the UI stale.
- **Live auto-refresh** (`src/lib/useLive.ts`): every list/detail screen (Device Fleet, Fleet Control, Jobs, Device Detail) now polls in the background on a `Live`/`Paused` toggle, pausing itself when the tab isn't visible (`document.visibilityState`). The load functions all took a `background` parameter so a poll tick never flips the blocking `loading` state or the Refresh button's label — polling updates the table/detail data in place with no flicker. Two race conditions this had to actually solve, not just note: Fleet Control's poll skips itself entirely while a bulk action is in flight (checked via a ref, since a plain state closure would be stale inside `setInterval`), and Device Detail's poll never overwrites the tags input while the operator has unsaved edits in it (tracked via a `tagsDirty` flag, reset on successful save).
- **Keyboard shortcuts** (`src/lib/hotkeys.ts`): `/` focuses the search box on Device Fleet, Fleet Control, and Jobs (skipped if focus is already in a text input); `Escape` closes whatever detail panel is open (Device Detail, Device Groups' member panel, Firmware Rollouts' detail panel).
- **Motion**: CSS transitions on buttons/rows/pills/panels/focus rings, a subtle pulse on the `Live` toggle's dot and on `ONLINE`-status pills, and a toast slide-in — all in `index.css`, no new dependency.

**Verified live** against real running `cmd/acs` + `cmd/api` (auth-enabled, admin password reset via direct DB update since the prior session's cleanup didn't record it) + Vite: `/` correctly focused the Device Fleet search input; the `Live` toggle's label and pulse state flipped on click; opening a device, editing its tags, and saving produced a toast reading `Tags saved: verify-tag-…` that auto-dismissed after ~4s; opening a device and pressing `Escape` closed the detail panel. `tsc --noEmit` clean throughout. Test tag cleaned up via direct SQL afterward; all three dev processes stopped and confirmed down.

### Gap-filling pass — Operators UI, Fleet Health, select-all-matching — done

Closed the three remaining gaps that had a concrete, buildable answer (the fourth, SUSPEND/ACTIVATE, still doesn't — see below).

**Operator-management UI**: `POST /api/v1/auth/operators` (admin-only) has existed since Phase 6; there was just no way to *see* who already exists short of querying Postgres directly, and no UI to create one either. Added `GET /api/v1/auth/operators` (`listOperators`, admin-only, password hashes never leave the handler) and a new `Operators` screen — a create form plus a table of every operator and their role, only reachable/rendered when `canAdmin(role)`, matching the backend's own `requireRole(admin, ...)` gate. The `Operators` nav tab itself is hidden for non-admins rather than shown-then-403ing.

**Fleet Health screen** (§3.3 / design doc v3 §16.1's "the one screen where charts are appropriate"): new `GET /api/v1/fleet-health`, aggregating four live SQL queries — `devices.CountByOnlineStatus` (already existed, powering the Prometheus gauge), a new `devices.CountByReachability` (connection-request-mode breakdown), a new `devices.InformRecencyBuckets` (devices bucketed by how long ago their last Inform was, so a slow fleet-wide drift toward staleness is visible before every device has tipped over), and a new `jobs.StatusCountsSince` (job outcome counts over a real trailing 24h window, not all-time). Rendered as proportional CSS bars — no charting library, same "no new dependency" discipline as the rest of the frontend. Grafana already had this as a time series (Phase 7 dashboards); this is the live-snapshot, no-separate-login version for triage.

**Fleet Control "select all N matching this filter"**: new `devices.MatchingIDs` (capped at 5,000) resolves the same manufacturer/online-status/connection-request-mode/search filter Fleet Control's group chips and search box already express client-side, server-side, via `GET /api/v1/devices/ids`. A "Select all N matching" button appears next to the search box whenever a filter is active, replacing the selection set with every matching device ID — not just the current page — in one call.

**SUSPEND/ACTIVATE — still not built, and still shouldn't be guessed**: re-checked `internal/bss/template.go`; it already refuses these two actions with a clear error pointing at this section, for the reason recorded back in §5.3 — the draft's naive `Device.IP.Interface.1.Enable=false` risks cutting the CWMP session that a follow-up ACTIVATE would need. That's a real per-vendor product/network decision (what a walled-garden redirect looks like on each CPE family), not a code gap a build pass can close by picking an answer — left exactly as scoped.

**Verified live** against real running `cmd/acs` + `cmd/api` (auth-enabled) + Vite: logged in as admin, confirmed the `Operators` tab is visible and the screen lists the real `admin`/`viewer` operators; created a real operator through the form and confirmed both the toast and the table updated. Opened Fleet Health and confirmed all four panels rendered live figures against the real seeded fleet (164 devices, a real 7.1% 24h job success rate — not a placeholder). On Fleet Control, searched "5G-CPE-Pro", confirmed a "Select all 152 matching" button appeared, clicked it, and confirmed the selected-count stat jumped to 152 — more devices than fit on one page, selected without paging through them. `go build`/`vet`/`test` and `tsc --noEmit` both clean. Zero console errors. Test operator and all other test artifacts cleaned up from the database afterward; all three dev processes stopped and confirmed down.

### Phase 4/5/8 firm-up — done

A follow-up pass closing the concrete, buildable gaps left in Firmware OTA (Phase 4), Diagnostics (Phase 5), and BSS/CRM Integration (Phase 8) — requested directly by phase number rather than derived from a gap list. Four pieces:

**Multi-wave rollout advance** (Phase 4): `advance` used to dispatch every remaining `ELIGIBLE` device in one shot — "canary, then everyone else." It now dispatches one more wave, sized `canary_percentage`% of the rollout's *original* eligible pool (new `rollout.Repository.TotalDevices`, not however many happen to remain — so a 10%-canary rollout across 1,000 devices walks in ~10 waves of ~100, not "10, then 900"), gated on the accumulated failure rate exactly as before. The response now reports `final_wave: bool` so a caller knows whether to expect another `advance` call; when a wave consumes everything left `ELIGIBLE`, the rollout flips straight to `COMPLETED` instead of needing one more no-op call to notice.

**Automatic rollback dispatch** (Phase 4): when a rollout gets `BLOCKED` (failure rate breach) and a `rollback_firmware_image_id` was configured at creation, `dispatchRollback` now queues that image as a `FIRMWARE_DOWNLOAD` job to every device whose *own* canary/wave download actually reached `SUCCESS` (new `rollout.Repository.SuccessfulDeviceIDs`) — not devices whose download failed, which never received the bad build in the first place, and not devices still `ELIGIBLE`, which never got dispatched at all. Idempotent via a new `rollback_dispatched_at` column (migration 0019) — a rollout can only reach `BLOCKED` once in the current state machine, but this guards against a retried request re-queuing rollback downloads.

**Traceroute + WiFi associated-devices** (Phase 5): `DIAGNOSTICS_TRACEROUTE` is the identical trigger/poll pattern `DIAGNOSTICS_PING` already proved (same `DiagnosticsState` polling loop, same `job.Attempts`-as-phase-discriminator, same max-attempts cap against a stuck poll) applied to `Device.IP.Diagnostics.TraceRoute.*` instead of `IPPing.*` — `diagnosticsState()` took a `prefix` parameter rather than being duplicated. `RouteHops.{i}.*` is a dynamic-length table TR-069 itself defines with no fixed path list to poll, so only `RouteHopsNumberOfEntries` is read back; per-hop detail is a follow-up `GET_PARAMETER`, the same boundary `refresh-cellular` already draws. The WiFi associated-devices convenience endpoint (`POST .../parameters/refresh-wifi-clients`) queues a `GET_PARAMETER` over the partial path `Device.WiFi.AccessPoint.` — TR-069's own mechanism for reading a whole dynamic-length subtree without the ACS knowing its length ahead of time, the same reason `cmd/probe` uses `GetParameterNames` rather than guessing indices.

**Webhooks + configurable walled-garden SUSPEND/ACTIVATE** (Phase 8): see below, each detailed separately since both are substantial.

Frontend: a Diagnostics panel added to Device Detail (Ping/Traceroute host inputs, a Refresh WiFi clients button — results land in the parameter cache and Recent Jobs, no separate results UI needed); a firmware image upload form added to the Rollouts screen (multipart, wired to the upload endpoint that's existed since Phase 4 but never had a UI); a rollback-image picker added to the rollout create form; the Advance button relabeled "Advance next wave" and a rollback-status line added to the rollout detail panel.

#### Webhooks — done

New infrastructure, not a wrapper around what exists, exactly as build plan §5.4 called for: `webhook_subscriptions` + `webhook_deliveries` tables (migration 0021, an outbox pattern — the same shape `internal/jobs`'s durable queue already uses, applied to outbound HTTP instead of outbound CWMP RPCs). Owned by `cmd/bssadapter`, not `cmd/acs`: a subscription is inherently BSS-facing (keyed by `account_id`, delivered to a BSS-owned URL), matching the existing "BSS-facing concerns stay in bssadapter, ACS core stays unaware of BSS structures" boundary (§5.1) — `cmd/acs` needed zero changes.

Two independent poll loops (`cmd/bssadapter/webhook_worker.go`): a **notify loop** (every 10s) walks `bss_orders` rows that haven't yet produced a delivery (new `notified_at` column), checks each one's job status via the *same* `ACSClient.GetJobStatus` call Workflow C already used for polling, and — once a job goes terminal — enqueues one `JOB_COMPLETED` delivery per matching subscription (fleet-wide `account_id IS NULL` subscriptions, or scoped to that specific account). A **delivery loop** (every 10s) drains due deliveries with exponential backoff (2^attempts minutes, capped at 8 attempts before a delivery is left `FAILED` for good) and POSTs each with an `X-Webhook-Signature: hex(HMAC-SHA256(secret, body))` header, the standard webhook-authenticity contract. `POST/GET/DELETE /bss/v1/webhooks` manage subscriptions.

**Verified live**: created a real subscription, submitted a real `MODIFY_WIFI` order, marked its job `SUCCESS` directly in Postgres (simulating a CPE ack), and confirmed within one poll cycle that a delivery was enqueued, POSTed to a real listening HTTP server, and marked `DELIVERED` — the `X-Webhook-Signature` header was independently recomputed against the delivered body and matched byte-for-byte. Also confirmed the notify loop correctly processes two real orders left over from earlier BSS testing sessions (one `FAILED`, one `SUCCESS`) with zero matching subscriptions at the time — enqueuing nothing, not a bug, since no subscription existed yet.

**Real bug found during this verification, not introduced by it**: with `cmd/api`'s operator JWT auth enabled, `cmd/bssadapter`'s calls to the internal ACS REST API (`SetParameters`, `GetJobStatus`) get `401`'d — `ACSClient` has never carried any operator credential, because bssadapter's own auth (a shared bearer token, §5.5) and cmd/api's operator JWT are different, unconnected credential classes, and nothing bridges them. This isn't new in this pass; it just wasn't exercised end-to-end against an auth-enabled `cmd/api` until webhook verification needed real order completion detection. **Flagged as an open item below**, not fixed here — it's a real design question (does bssadapter get a service-account JWT? a separate service-to-service auth bypass keyed on source, like `/metrics` gets?) worth its own decision, not a one-line patch.

**Resolved 2026-08-06, see §9**: `ACS_INTERNAL_SERVICE_TOKEN` — a shared secret, set identically on both processes — is checked by `cmd/api`'s `withJWTAuth` (grants a synthetic `service:bssadapter` superadmin identity, bypassing the operator-login path entirely) and sent by `cmd/bssadapter`'s `ACSClient` on every internal-API call. Neither side treats it as optional-and-silently-fine: both log a loud `WARN` at startup if it's unset while JWT auth is otherwise enabled, the same "off unless configured, loud warning when it isn't" convention as everything else security-relevant in this codebase.

#### Configurable walled-garden SUSPEND/ACTIVATE — done

`internal/bss/template.go`'s `Translate` now accepts a `WalledGardenConfig{Parameter, SuspendValue, ActiveValue}`, sourced from `ACS_WALLED_GARDEN_PARAMETER`/`ACS_WALLED_GARDEN_SUSPEND_VALUE`/`ACS_WALLED_GARDEN_ACTIVE_VALUE` — the same "off unless configured, loud warning when it isn't" convention every other credential/feature gate in this codebase already uses (Digest auth, JWT auth, mTLS, Connection Request credentials). This is deliberately **not** a guess at which parameter isolates a device on any real vendor's CPE — build plan §5.3's concern stands exactly as written (there's no universal safe parameter across vendors; picking one here would be inventing a product/network decision, not building it). What this closes is the *mechanism*: once a deployer has verified a real walled-garden parameter against their fleet, wiring it in is three environment variables, not a code change. Left unconfigured, `SUSPEND`/`ACTIVATE` are still rejected with a clear error, exactly as before.

**Verified live**: with the walled-garden env vars set, submitted a real `SUSPEND` order and confirmed via direct Postgres inspection that the queued job's payload was *exactly* the configured parameter/value — `Device.X_TEST_WalledGarden.Enable = "true"` — never `Device.IP.Interface.1.Enable`, the unsafe parameter build plan §5.3 originally flagged. Unit tests cover both the unconfigured-rejection and configured-acceptance paths (`internal/bss/template_test.go`).

**Verified live, all four pieces together**: `go build`/`vet`/`test` clean throughout (including the updated `internal/bss` suite). Frontend `tsc --noEmit` clean. Against a real running three-service stack: dispatched a 152-device rollout in 15-device waves (confirmed each `advance` call dispatched another 15, not the remaining 137), forced a 33% failure rate and confirmed `BLOCKED` correctly fired rollback downloads to precisely the 20 devices whose canary download had actually succeeded — confirmed by direct Postgres query, not just an audit log line — while the 10 failed devices correctly received nothing. Diagnostics panel: queued a real ping and a real traceroute from Device Detail and confirmed both `DIAGNOSTICS_PING`/`DIAGNOSTICS_TRACEROUTE` jobs appeared in Recent Jobs with distinct command keys. Firmware upload: uploaded a real file through the new form and confirmed it appeared in both the rollout image picker and the new rollback-image picker. All test rollouts, jobs, orders, webhook subscriptions/deliveries, and the test firmware image cleaned up from the database afterward; all dev processes (including the temporary webhook-sink listener) stopped and confirmed down.

### Critical feature backlog: AddObject/DeleteObject/Reboot/FactoryReset + credential encryption — done

A gap analysis against off-the-shelf ACS platforms (GenieACS/Axiros/Incognito-class) turned up the single biggest protocol-completeness hole in this build: every write path (`SetParameterValues`) could only edit parameters that already existed on a device. Provisioning anything genuinely new — a second WLAN SSID, a port-forward rule, a VoIP line — was impossible without `AddObject`. `Reboot`/`FactoryReset` were also entirely missing, despite being table-stakes support-agent actions on any real ACS. Alongside those, `device_credentials.password` was plaintext in Postgres. This pass closes all four.

**`AddObject`/`DeleteObject`** (`internal/cwmp/object.go`): `AddObjectResponse` carries the CPE-assigned `InstanceNumber` — the one piece of RPC-completion data with nowhere else to live, so a new `jobs.result_detail JSONB` column (migration 0022) was added rather than overloading `fault_string`. `ADD_OBJECT`'s REST endpoint (`POST .../objects`) and `DELETE_OBJECT`'s (`POST .../objects/delete` — a `DELETE`-with-body shape was rejected as a design smell, matching the `POST .../refresh-cellular`-style "action endpoint" convention already used elsewhere) both validate `object_path` ends in `"."`, TR-069's own convention for "this names a container."

**`Reboot`/`FactoryReset`** (`internal/cwmp/reboot.go`): both are argument-light RPCs whose real outcome is a *later* Inform (event code "M Reboot"), not anything the synchronous response carries — the same "accepted now, confirmed later" shape `Download`/`TransferComplete` already established for firmware. `FactoryReset` gets a client-side confirm dialog (this build's first — every other destructive action here fires on click) and a distinct `.btn.danger` style, since it's the one action in this app that's destructive to the physical device, not just ACS-side bookkeeping.

**Credential encryption at rest** (`internal/credentials/credentials.go`): `device_credentials.password` is now AES-256-GCM-encrypted when `ACS_CREDENTIAL_ENCRYPTION_KEY` is set (SHA-256-derived from any passphrase, so an operator doesn't need to hand-generate an exact-length key) — the same "off unless configured, loud warning when it isn't" convention every other credential gate in this codebase uses. A `enc:` prefix on the stored value distinguishes an encrypted row from a legacy plaintext one, so turning encryption on for an existing deployment needs no backfill migration — old rows keep decrypting (as a no-op) until they're next rotated. Unit tests (`internal/credentials/credentials_test.go`) cover the round-trip, the disabled-by-default path, legacy-plaintext backward compatibility, and — importantly — that decrypting with the wrong key actually fails (GCM's authentication tag doing its job, not just silently returning garbage).

**Verified live**, all four RPCs, via a hand-built CWMP session simulator (real HTTP POSTs to `cmd/acs`, real session cookie) rather than trusting the code to be correct because it compiled: queued all four jobs, then chased the session's actual behavior — each POST's response can already carry the next dispatched RPC, the same pattern proven for diagnostics polling — confirming `AddObject`, `DeleteObject`, `Reboot`, and `FactoryReset` were each rendered correctly and in the right order. Answered each with a real `*Response`, confirmed all four jobs reached `SUCCESS` via REST, and confirmed `AddObjectResponse`'s `InstanceNumber=7` landed correctly in `result_detail`. (First attempt at this queued jobs one at a time with a session round-trip in between — the session went idle and closed between jobs, since only one was ever queued at a time; fixed by queuing all four up front, a reminder that CWMP session lifecycle assumptions need checking against the real dispatch loop, not assumed.) Frontend: confirmed the Reboot/Factory reset buttons and Object management panel render on Device Detail, that Factory reset's confirm dialog fires (and Reboot's doesn't), and that both `ADD_OBJECT` and `REBOOT` jobs appeared in Recent Jobs. `go build`/`vet`/`test` clean (68 tests), `tsc --noEmit` clean. All test data (job rows, the throwaway session-simulator device, its `cwmp_sessions`/`audit_log` rows) cleaned up afterward.

**Not built this pass, from the same gap analysis** — flagged, not attempted, each for a stated reason: STUN-based NAT traversal for Connection Request (a separate infrastructure component — a UDP STUN server plus CPE-side coordination — not an RPC addition, and risky to squeeze into a batch with everything else); live `data_model_root` branching across every hardcoded `Device.*` write path (a systemic refactor touching most of the write surface — needs its own pass with full regression verification, not a rider on this one).

### Nice-to-have feature backlog — done

The remainder of the gap-analysis backlog: `ScheduleInform`, `SetParameterAttributes`/`GetParameterAttributes`, `Upload`, parameter value history, and config templates. The last of these was reshaped mid-build from a direct ask — a single named, reusable, multi-parameter template (e.g. a full WiFi profile) that bulk-applies to a device selection or group, not just a one-shot auto-provisioning rule.

**`ScheduleInform`** (`internal/cwmp/schedule_inform.go`): tells a CPE to Inform again after a delay, independent of its periodic interval or an ACS-initiated Connection Request. Same argument-light, accepted-now shape as Reboot.

**`SetParameterAttributes`/`GetParameterAttributes`** (`internal/cwmp/attributes.go`): configures a parameter's active-notification level (0 off / 1 passive / 2 active) — the first RPC pair in this codebase that changes what the *CPE* proactively tells the ACS, rather than the ACS always initiating. `AccessList` is deliberately not exposed as a control surface — every write already goes through operator RBAC at the REST layer, so CWMP's own separate per-parameter access-list mechanism isn't something this build's UI needs to expose.

**`Upload`** (`internal/cwmp/upload.go`, `internal/uploads/`): the CPE-to-ACS direction of file transfer — a vendor config backup or log file pulled *from* a device, mirroring `Download`'s shape in reverse and sharing its exact completion signal (`TransferComplete`, not a separate `UploadComplete`). A new `uploaded_files` table (migration 0024) tracks PENDING→RECEIVED; the receipt endpoint (`PUT /api/v1/uploads/{id}/receive`) is public (CPE traffic, no operator JWT to present — the same precedent the firmware file-serve route already set), streams straight to disk via `internal/uploads.Storage` (the same deliberate local-disk stand-in for S3 `internal/firmware.Storage` already is), and computes SHA256 while writing.

**Parameter value history** (`internal/parameters/cache.go`, migration 0025): the existing `device_parameter_cache` was latest-value-only by design; `parameter_history` is an insert-only companion, written inside the same transaction as the cache upsert, and — critically — only when a value actually *changes* from what was already cached, not on every report. That diff-then-write happens once, in `Repository.Upsert`, so every existing caller (Inform, GET_PARAMETER confirms, SET_PARAMETER confirms) gets history for free without being touched.

**Config templates** (`internal/templates/`, migration 0026): a named, reusable set of parameter writes. Two independent application paths:
- **On-demand bulk apply** — `POST /api/v1/config-templates/{id}/apply` with a device-ID list or a `device_groups` group, fanning out one independent `SET_PARAMETER` job per device (the exact same per-device-outcome shape `bulkAction` already established for Fleet Control — reused, not reinvented). Wired into two places: a standalone Templates screen (create, edit parameter rows, apply), and a new "Apply config template" option in Fleet Control's existing bulk-action toolbar, so an operator can select devices first (including via "select all N matching") and apply a template to that exact selection.
- **Zero-touch auto-apply** — `auto_apply` + `model_filter` on a template; `cmd/acs/provision.go` checks every `0 BOOTSTRAP` Inform (a device's genuine first contact, not a reboot — event code "1 BOOT" is deliberately not matched) against templates with a matching filter and queues them automatically, no operator action required. Requires a non-empty `model_filter` — auto-applying to *every* device on first contact isn't supported, rejected at creation time.

A template's parameter paths are plain TR-181 (Device:2), the same convention every other write in this codebase already uses uniformly — **one template genuinely applies across different manufacturers/models** sharing that data model root (confirmed live below, across three real different manufacturers). The one honest caveat: TR-098/IGD:1 legacy devices use a different root entirely and aren't covered — that's the pre-existing `data_model_root` branching gap, not something this feature papers over.

**Verified live**, all five pieces, via REST + a hand-built CWMP session simulator (same response-chasing pattern the critical-feature batch needed):
- Created a template with two parameters, applied it to 5 real devices spanning three different manufacturer/product_class combinations, and confirmed via direct Postgres query that every job's payload carried *both* parameters identically across all of them.
- Sent a real `0 BOOTSTRAP` Inform for a brand-new device matching an `auto_apply` template's filter and confirmed the matching `SET_PARAMETER` job was queued automatically with no API call — the audit log shows `ConfigTemplateAutoApplied`.
- `ScheduleInform` and `Upload` round-tripped in one continuous session: queued both, the session dispatched each RPC in turn, `Upload`'s response moved the job to `AWAITING_TRANSFER_COMPLETE`, a real HTTP `PUT` delivered file bytes to the receipt endpoint, a `TransferComplete` in a fresh session (matching real CWMP behavior — the CPE may not report completion until well after the dispatching session closed) resolved it to `SUCCESS`, and the downloaded file byte-for-byte matched what was "uploaded." (First attempt at this test used the wrong substring in a debug helper and looked like the RPC never rendered — turned out to be a bug in the test script's own string-prefixing, not the product; caught by printing the raw response instead of trusting the boolean.)
- Parameter history: wrote a value, confirmed one history row; wrote a *different* value, confirmed a second row appended (newest first); re-wrote the *same* value a third time and confirmed no third row was added — the "only on actual change" behavior working as designed, not just as commented.
- Frontend: created a template through the real Templates screen (dynamic parameter-row editor, add/remove rows) and applied it to real devices through the UI, confirmed via a real toast reading "3 of 3 devices queued successfully"; confirmed Fleet Control's new "Apply config template" bulk-action option renders its template picker correctly once selected.

Backend: `go build`/`vet`/`test` clean throughout (68 tests). Frontend: `tsc --noEmit` clean. All test templates, jobs, uploaded files, sessions, and the throwaway BOOTSTRAP-test device cleaned up from the database afterward; all dev processes stopped and confirmed down.

**Not built, stated plainly**: a broader per-vendor canonical-parameter registry (the gap analysis's item 13) — genuinely needs real vendor documentation this build doesn't have access to, not code that can be written speculatively. `SetParameterAttributes`/`GetParameterAttributes` have REST endpoints but no frontend UI yet (a niche capability relative to the rest of this pass — API-only for now, the same scope boundary operator-management sat at for one earlier pass before getting a screen). An OpenAPI spec was deferred given the size already delivered in this batch.

---

## 8. Immediate next actions (historical — Phase 0 kickoff, 2026-08-04)

Kept verbatim for the record. Every item below is long since done — the codebase is well past Phase 0. **Do not use this as the current punch list — see §10.**

```text
1. Confirm or override the defaults in §1 (Go, React+TypeScript, TanStack Table, Docker Compose, monorepo).
2. Scaffold backend/ and frontend/ directory trees (empty modules, no logic).
3. Write infra/docker-compose.yml (Postgres, Redis, MinIO, RabbitMQ).
4. Build Phase 0 lab harness against one real CPE — this is the actual gating
   step; nothing else should be built ahead of it per v3's Phase 0 rule.
5. In parallel: build the Device Fleet + Jobs screens against frontend/src/mock/
   fixture data so the data-dense grid design gets validated before the
   backend REST API exists.
6. When Phase 8 starts: resolve the SUSPEND/walled-garden design question (§5.3)
   and the BSS adapter topology decision (§1) before writing bssadapter code.
7. Fix the unbounded-body gap on cmd/bssadapter (§7.2) — cheap, independent,
   no need to wait for the rest of §7.
```

---

## 9. Admin Platform Backlog (2026-08-06) — undocumented until this pass

A distinct batch of work, requested directly by the user rather than derived from v3 (v3 is a CWMP/TR-069 protocol design doc and has no section covering any of this — same relationship Phase 8's BSS/CRM integration had to v3, per §4's Phase 8 intro). Landed as migrations `0027`-`0039`, all present in the repo's single initial import commit, so there's no per-feature commit history — dates below come from the migrations' own comments ("confirmed with the user, 2026-08-06"), not git.

Two things distinguish this batch from Phases 0-8's earlier gap-filling passes, worth stating up front:

1. **Three features are real, working code that is deliberately not yet functional end-to-end** — SSH/Telnet console, device web-GUI embed, and the WireGuard VPN concentrator. Each was scoped by the user's own explicit call: "build now, functional later." That's not a euphemism for a stub — the SSH dial, the Curve25519 keypair generation, the xterm.js-to-WebSocket bridge are all real. What's missing is a way to actually *reach* a CGNAT'd device on ports other than the CWMP gateway's own 7547 (see §10's CGNAT item — the same underlying blocker as Annex G).
2. **Closes two items this document itself previously left open**: the bssadapter→cmd/api auth bridge gap (§5.4's "flagged as an open item below" — resolved, see the note at that paragraph and below) and the BSS shared-token-only auth (§5.5 listed it as the interim state; OAuth2 client-credentials now supersedes it).

### RBAC tier expansion

Migration `0032`. The user asked for `superadmin`/`Manager`/`NOC`/`Read-only(ISP)` with superadmin-configurable per-role capabilities. Scope decision confirmed with the user 2026-08-06: rather than replace all ~72 routes' simple rank check with individually configurable permissions (large diff, high regression risk on a security-critical surface), the existing rank gate stays for routine read/write routes (`superadmin` > `manager` > `noc` > `readonly`, direct rename of the old `admin`/`operator`/`readonly` three-tier scheme — existing operators migrated in place by the migration itself), and a new `role_permissions` table adds a genuinely configurable layer only for the ~13 highest-stakes capabilities (bulk actions, credential management, CLI/VPN access, firmware/rollout management, template/policy/schedule/group management, tenancy management, diagnostics, connection requests — the full catalog lives in `internal/operators`'s permission constants, e.g. `PermCLIAccess`, `PermTenancyManage`). `superadmin` is never a row in `role_permissions` — it always has every permission, enforced in code (`internal/operators.HasPermission`), not editable even by another superadmin, so there's no path to a fleet with zero permission-configurable superadmin. `GET`/`PUT /api/v1/auth/role-permissions` (superadmin-only) manage the table; the `Operators` screen gained a permissions matrix. Also added: an `operators.email` column and a full self-service password-reset flow (`POST /api/v1/auth/password-reset/request` / `.../confirm`, `password_reset_tokens` table, tokens single-use with an expiry) — email delivery goes through the new `internal/mailer` package (below).

### Multi-tenancy

Migration `0033`. Shape confirmed with the user 2026-08-06: a single-owner hierarchy — `regions` → `customers` → `devices.customer_id` — for ISP/customer organization, with `projects` as a separate cross-cutting many-to-many tag a device can carry several of (not part of the ownership hierarchy; a device can be one customer's but tagged into several projects at once). Operator accounts are scoped by assignment (`operator_scopes`: `(operator_id, scope_type, scope_id)` where `scope_type` is `region` or `customer`) — no scope rows means unrestricted (backward-compatible default for every operator that existed before this migration), one or more rows means restricted to just those regions/customers, with a region scope implicitly covering every customer under it, resolved at query time rather than denormalized. `h.deviceScope(r)` (referenced by the Excel export handler below, and by every device-list/read endpoint) is the one place this filter is actually applied — every device read in the app respects the calling operator's tenancy scope. REST: `POST/GET/DELETE /api/v1/regions`, `/customers`, `/projects` (structural CRUD is superadmin-only — "the org chart"), `PUT /api/v1/devices/{id}/customer`, `PUT/GET /api/v1/devices/{id}/projects` (assignment is the narrower `tenancy.manage` permission, not full superadmin). New `Tenancy` screen (org-chart CRUD) and a `DeviceTenancy` panel in Device Detail (assign customer/projects).

### Per-user dashboards

Migration `0034`. One `dashboard_layouts` row per operator — `widgets` is an ordered JSONB array of `{id, enabled}`, deliberately freeform (not one column per widget) so a new widget type never needs a migration, only a new `id` the frontend recognizes. `GET /api/v1/dashboard` (live aggregated fleet figures) and `GET`/`PUT /api/v1/dashboard/layout` (the per-operator arrangement). New `Dashboard` screen and `internal/dashboard` package.

### Device location + Excel reporting

Migration `0035`, plus the unrelated-in-schema-but-same-pass Excel import/export. `devices.location` is free text (no structured address/GPS fields — real-world entries range from "Rack 4, POP-West" to a full street address, and TR-069 has no standard parameter for physical location; it's operator-entered metadata, same category as `tags`) — `PUT /api/v1/devices/{id}/location`. Paired with two report-shaped endpoints that shipped in the same pass: `GET /api/v1/reports/devices/export` streams a real `.xlsx` workbook (`github.com/xuri/excelize/v2`) — serial, manufacturer, model, MAC, status, firmware version, current SSID, location, customer, region — filterable by region/customer/project and always further filtered by the calling operator's own tenancy scope; `POST /api/v1/devices/import?format=json|csv|xml` bulk-creates/updates devices from an uploaded file (capped at 5,000 rows/request). New `Reports` screen.

### Parameter discovery

Migrations `0027`-`0028`. A `GetParameterNames(root, false)` sweep, run automatically on a device's first connect (`0 BOOTSTRAP` Inform — the same genuine-first-contact event config templates' auto-apply already keys on) and available on demand thereafter (`POST /api/v1/devices/{id}/discover-parameters`, `GET .../parameter-names`). Two purposes: lets the console show what a CPE actually supports instead of relying solely on the static vendor registry (`internal/devices/adapters`), and — the more structural payoff — the ACS now *learns* each device's real data model root (`devices.data_model_root`, `data_model_root_confirmed_at`) instead of leaving it `UNKNOWN` forever, which was true of every device before this migration regardless of Phase. Results land in `device_parameter_names` (one JSONB blob per device, name → writable bool) — replaced wholesale on each discovery run, not merged, since a stale entry here (a parameter the firmware no longer exposes) is actively misleading in a way a stale cached *value* isn't. `0028` is a same-day bugfix: `0027` added the `PARAMETER_DISCOVERY` job type in Go but missed widening `jobs_type_check`, caught live on the first BOOTSTRAP-triggered discovery job.

**This is a real mitigation for, but not a resolution of, design-v3 prerequisite P1** (Device:2 vs IGD:1 data model support) — see §10.

### STUN NAT traversal — partial

Migration `0029`. `internal/stun` is a real RFC 5389 Binding-Response server (`ACS_STUN_ADDR`, default `:3478` UDP, runs inside `cmd/acs`). Once a STUN-capable CPE binds against it, its next Inform reports `udp_connection_request_address` (the reflexive host:port from the Binding Response) and `nat_detected` — both now captured on `devices`. This is deliberately scoped as *detection only*: recording these two fields is separate from actually sending a TR-069 Annex G UDP Connection Request datagram to wake a NAT'd device instantly, which is **not built** — the Annex G signature/wire format couldn't be sourced from an authoritative spec (every mirror of the real ITU/BBF document found was blocked or paywalled), and shipping a guessed HMAC scheme would look done while silently failing against real hardware. `internal/connreq` still only performs a plain HTTP GET to `ConnectionRequestURL`; it does not yet consume `udp_connection_request_address` or attempt a UDP send. Confirmed still true as of this pass — see §10.

### Device CLI/SSH/Telnet console — scaffolded, functional later

Migration `0030`, `internal/cliaccess` (`bridge.go`, `cliaccess.go`, `telnet.go`, `webgui.go`), frontend `RemoteShell.tsx`/`DeviceConsole.tsx`. Real code: a genuine SSH dial (`golang.org/x/crypto/ssh`) and Telnet client, bridged to the browser over `GET /api/v1/devices/{id}/cli/connect` — a long-lived WebSocket deliberately *not* wrapped in the usual `route()`/metrics-instrumentation helper (a terminal session isn't a request/response call; the duration histogram would just accumulate one unbounded bucket for as long as the session stays open), though `requireRole` + the `cli.access` permission still gate it exactly as tightly as everything else. Credentials are per-device (`device_cli_credentials` — protocol/host/port/username/password, encrypted at rest under the same `enc:`-prefix convention as `internal/credentials` when `ACS_CREDENTIAL_ENCRYPTION_KEY` is set), not shared, because unlike the CWMP Connection Request password (one shared secret works fleet-wide, since the ACS itself chose it), SSH/Telnet credentials are whatever the device's own OS-level account already is.

**Explicitly scaffolding, per the user's own framing**: the ACS reaching a device's shell port (22/23) has the identical CGNAT reachability constraint as Connection Request/Annex G, just on a different port than 7547 — unusable against a real NAT'd device until a VPN/tunnel path exists. The code is real; the reachability it depends on is not.

### Device web-GUI embed — scaffolded, functional later

Migration `0031`, `cmd/api/webgui_handlers.go`, frontend `DeviceWebGUI.tsx`. Same "scaffold now, functional later" call, same CGNAT blocker as CLI access above. One `device_webgui_config` row per device (base URL + optional HTTP Basic Auth pair — not versioned/rotated like `device_cli_credentials`, since a device's own web-UI URL and credentials change rarely enough that a single overwritten row is simpler and matches the lower churn). `PUT`/`GET`/`DELETE /api/v1/devices/{id}/webgui` manage the config; `/api/v1/devices/{id}/webgui/proxy/{path...}` is the actual embed — deliberately not wrapped by the standard `route()` helper and registered with no HTTP method prefix, because a device's own admin UI needs `POST` (its forms, settings changes) as much as `GET` (page loads/assets) — gated by the same `cli.access` permission as configuring it, since this is a write-capable channel to the device, not a read-only view.

### WireGuard VPN concentrator

Migrations `0036` (peer registry), `0037`/`0038` (bugfixes), `internal/vpn`, frontend `VPNTunnel.tsx`. Deliberately built last in this backlog, and the most partial of the three "scaffold now" pieces. What's real: Curve25519 keypair generation (`internal/vpn/keys.go` — unambiguous, unlike Annex G's undocumented signature format) and overlay-IP allocation (`internal/vpn/overlay.go`, subnet from `ACS_VPN_OVERLAY_SUBNET`) against a `device_vpn_peers` registry (`POST /api/v1/devices/{id}/vpn/enroll`, `GET .../vpn/config`, `GET /api/v1/vpn/peers`, `DELETE /api/v1/vpn/peers/{peer_id}`). What this table explicitly does **not** do on its own: stand up an actual OS-level WireGuard interface. TR-069 has no native "here's your VPN config" RPC, and most consumer/rebranded CPE firmware (the ZOWEE test unit from `deployment-testing-onboarding-guide.md` included) can't enroll in an operator-controlled VPN at all — a real concentrator needs a separate process (`wireguard-go` or the kernel module) applying this table via `wg syncconf`, which `go.mod` confirms was never added (only `golang.org/x/crypto` and `golang.org/x/net` — no WireGuard library). `vpn.RevokePeer`'s own comment states plainly that revoking a peer updates the database but "does not (cannot, from this process) tear down a live tunnel."

`0037`/`0038` are same-class live-caught bugs: `device_vpn_peers.device_id` and `.overlay_ip` were both blanket `UNIQUE` columns, so a `REVOKED` peer's row (kept for audit history, not deleted) permanently blocked that device from re-enrolling and permanently claimed its IP even though the application-level logic only ever meant to count `ENROLLED` peers as occupying either. Fixed with partial unique indexes scoped to `status = 'ENROLLED'`.

### BSS OAuth2 client-credentials

Migration `0039`, replacing the single shared `ACS_BSS_API_TOKEN` this document's §5.5/§7 always flagged as an interim mechanism. `bss_oauth_clients` — one row per registered BSS/CRM integration, `client_secret` bcrypt-hashed, never stored or returned in plaintext after creation (same posture as `operators.password_hash`). `POST /bss/v1/oauth/token` (RFC 6749 §4.4 client-credentials grant, `client_id`/`client_secret` via HTTP Basic or form body) issues a 1-hour bearer JWT; every `/bss/v1/*` route accepts either that token or the legacy shared token, so existing integrations don't break mid-migration. `GET/POST /api/v1/bss/oauth-clients`, `DELETE .../{id}` (superadmin-only) manage clients — revoking blocks new token issuance immediately, but already-issued tokens remain valid until their own (max 1-hour) expiry. See `bss-integration-guide.md` §3 for the integration-facing documentation of this, now corrected to match.

### BSS integration admin panel

New `cmd/api` routes, `admin` (superadmin) gated: `GET/POST /api/v1/bss/mappings`, the OAuth-client and webhook-subscription CRUD above, `GET /api/v1/bss/stats`, `GET /api/v1/bss/health`, and four troubleshooting endpoints (`POST /api/v1/bss/troubleshoot/{mapping-lookup,auth-check,job-status,order-dispatch}`) that let an operator debug a BSS integration issue (e.g. "why is this order failing") from the console instead of needing direct database/log access. New `BSSIntegration` screen ties the whole surface together — onboarding a new OAuth2 client, viewing webhook subscriptions, running the troubleshooting checks.

### Mailer

`internal/mailer` — operator-facing transactional email (currently just password-reset links), stdlib `net/smtp` only, no new dependency. `ACS_SMTP_HOST`/`_PORT`/`_USERNAME`/`_PASSWORD`/`_FROM`; the same "off unless configured, loud warning when it isn't" convention as everything else — with no SMTP host configured, `Send` logs the message instead of emailing it, which doubles as a convenient dev-mode (the reset link is right there in the server log).

---

## 10. Current status & outstanding items — corrected 2026-08-11

Supersedes §8 and every scattered "flagged as an open item" callout in §§1-9 above; this is the accurate current list, cross-checked against the actual code (not just prior documentation) as of 2026-08-11. `go build ./...`, `go vet ./...` (backend) and `tsc -b` (frontend) all pass clean.

**Genuinely outstanding:**

- **CGNAT reachability for anything beyond periodic Inform — the one real blocker, and the load-bearing gap behind several "done" items above.** TR-069 Annex G's UDP Connection Request wire format is still unsourced from an authoritative spec (§9's STUN section); SSH/Telnet console, web-GUI embed, and the VPN concentrator are all real code waiting on the same underlying problem (reaching a device that only ever dials out). The plan, per `deployment-testing-onboarding-guide.md` §9, is to determine Annex G's real format from a packet capture of actual device STUN/keep-alive traffic once a real device is on the network reporting `udp_connection_request_address`, rather than continue guessing.
- **`data_model_root` branching across hardcoded `Device.*` write paths** (TR-181 vs legacy TR-098 `InternetGatewayDevice.`) is still not built. Parameter discovery (§9) mitigates this by *detecting* which root a device actually uses instead of leaving it `UNKNOWN`, but every write path in this codebase still hardcodes the `Device.` (TR-181) prefix — a genuine IGD:1-only device's writes would go to the wrong tree. This remains a systemic refactor across most of the write surface, not a rider on any single pass.
- **SUSPEND/ACTIVATE walled-garden parameter** is mechanism-complete (`ACS_WALLED_GARDEN_PARAMETER`/`_SUSPEND_VALUE`/`_ACTIVE_VALUE`, §5.3/§5's "Configurable walled-garden SUSPEND/ACTIVATE" pass) but deliberately unconfigured by default — `bss-integration-guide.md` correctly documents both actions as returning `400` until a real per-vendor safe parameter is set. Still correct, still not something to guess.
- **Per-vendor canonical-parameter registry** (gap analysis item 13, §"Nice-to-have feature backlog") — still not built; needs real vendor documentation this project doesn't have. Parameter discovery is a related, shipped mitigation, not a substitute.
- **`SetParameterAttributes`/`GetParameterAttributes`** — REST endpoints exist, no frontend UI.
- **OpenAPI spec** — still deferred.
- **`ParameterKey`/Inform event-code correlation** — `SetParameterValues` sets `ParameterKey` to the job's `CommandKey` on the outbound request, but it's never consumed when a CPE reports it back on a `4 VALUE CHANGE` Inform event (`internal/cwmp/rpc.go`). Small, real, previously unflagged.
- **No OS-level WireGuard interface process** for the VPN concentrator (§9) — the peer registry and crypto are real, nothing applies them to an actual tunnel yet.

**Resolved since this document was last accurate (do not treat these as open anymore):**

- The bssadapter→cmd/api internal auth bridge (§5.4) — `ACS_INTERNAL_SERVICE_TOKEN`, §9.
- BSS-facing auth being shared-token-only (§5.5/§7) — OAuth2 client-credentials, §9, migration `0039`.
- `bss-integration-guide.md` §0/§4 previously (before this pass) said webhooks were "not implemented yet" — they were, as of the Phase 8 firm-up pass documented earlier in this file (`webhook_subscriptions`/`webhook_deliveries`, migration `0021`, `cmd/bssadapter/webhook_worker.go`). That guide is corrected in this same update pass.
