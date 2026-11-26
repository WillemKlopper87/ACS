# SIM change detection Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Detect a replaced SIM in an in-service CWMP CPE, judge it against the plan's allowed networks, and raise an incident, an event and a console banner with an audited "approve this SIM" step. Alert only; nothing is written to the CPE.

**Architecture:** A pure judge (`internal/simwatch`) compares the incoming SIM identity (merged over the cached one) with the cached one inside `cacheParameterValues`, before the cache is overwritten. The verdict (masked hints only) is persisted first into a new `device_sim_watch` table; a small check-in sampler makes sure the identity leaves are read. The BSS adapter's incident worker turns `notice`/`alert` rows into incidents; the API serves the state on the existing SIM view and takes the approval.

**Tech Stack:** Go (module `acs`, in `backend/`), Postgres migrations, React/TypeScript console (`frontend/`), OpenAPI (`backend/openapi.yaml`, generated `frontend/src/api/generated.ts`).

**Spec:** `docs/superpowers/specs/2026-11-26-sim-change-detection-design.md`

## Global Constraints

- POPIA/RICA: full IMSI/ICCID/MSISDN are never written to `device_sim_watch`, logs, events, incident details, audit rows or API replies of the new endpoint; only `parameters.MaskSubscriberIdentifier(parameters.SubscriberICCID|SubscriberIMSI, v)` hints and the PLMN (first 5 digits of the IMSI, as `checkSIM` does) leave the ACS. SIM PIN is never touched.
- Interface 1 only (`Device.Cellular.Interface.1.`).
- Alert only: no write to the CPE, no change to onboarding state.
- Migration number is `0106` (latest is `0105_device_cpe_samples.sql`). The dbroles manifest is generated (`ACS_UPDATE_DB_GRANTS=1`), never hand-edited. The ACS must not import any package that contains incident SQL.
- Contract tags never move; the new event type is a contract addition and is documented, not released.
- Go commands: `GOTOOLCHAIN=go$(awk '/^go /{print $2}' backend/go.mod)` from `backend/`. DB tests use `ACS_TEST_POSTGRES_DSN="postgres://acs:acs@127.0.0.1:55440/acs?sslmode=disable"` (throwaway; its tests drop the schema; never point it elsewhere; use `-p 1`). Frontend unit tests: `cd frontend && npx --yes -p node@22 node ./node_modules/vitest/vitest.mjs run <files>`. Do not run `scripts/ci-local.sh` inside a task; the final task does.
- No GitHub Actions; no logging of identifiers (device id and verdict only).
- Priorities are the policy's P1–P4 (`alerting.Priority`); defaults chosen below.

## Review Focus

- A reply that carries only the new IMSI while the cached ICCID is the old one: the judge uses incoming-over-cached, so the verdict still fires once and never twice (Task 1, Task 3).
- A SIM read that returns empty/sentinel after a swap, or a read that fails: no verdict, nothing stored, cache unchanged for the identity paths only when persisting failed (Task 1, Task 3).
- A CPE with several cellular interfaces: only interface 1 counts, regardless of map order (Task 1).
- ACS restart: due check comes from `last_checked_at`, not only from the process-local throttle (Task 3).
- Approving with a whitespace-only reason, approving when nothing is open, and the same SIM being re-read after approval raising nothing (Task 5).
- A device with no customer mapping: banner and event, no incident, no error loop (Task 4).

---

### Task 1: Pure judge and identity extraction

**Files:**
- Create: `backend/internal/simwatch/simwatch.go`, `backend/internal/simwatch/simwatch_test.go`
- Add to the commit: the spec `docs/superpowers/specs/2026-11-26-sim-change-detection-design.md` and this plan.

**Interfaces:**
- Produces:
  - `const CreatedBy = "system:sim-watch"`
  - `type Identity struct{ ICCID, IMSI string }`; `func (i Identity) Empty() bool`
  - `func ExtractIdentity(values map[string]string) Identity` — takes parameter path→value, interface 1 only: ICCID from `Device.Cellular.Interface.1.USIM.ICCID`; IMSI from `…USIM.IMSI`, else `Device.Cellular.Interface.1.X_ALU-COM_CurrentUsedIMSI`. Trims space; empty and `adapters.IsCellularSentinel` values count as absent. Paths of other interfaces are ignored.
  - `func ReadPaths(names map[string]bool) []string` — the interface-1 identity paths (the three above) that are present in `names`, sorted; nil when `len(names)==0` or none present.
  - `func Merge(cached, incoming Identity) Identity` — incoming non-empty fields override cached.
  - `func Network(imsi string) string` — first 5 digits (the plan's PLMN form), `""` if the IMSI is shorter than 5.
  - `type Kind string` with `KindNone`, `KindBaseline`, `KindNotice`, `KindAlert`
  - `type Verdict struct{ Kind Kind; Network string }`
  - `func Judge(cached, incoming Identity, allowed func(imsi string) bool) Verdict` — `merged := Merge(cached, incoming)`. Merged empty → None. `cached.Empty()` (first SIM ever) → Baseline, or Alert when `merged.IMSI != ""` and `!allowed(merged.IMSI)`. Otherwise changed = a field is non-empty in both `cached` and `incoming` and differs; not changed → None. Changed → Alert when `merged.IMSI != "" && !allowed(merged.IMSI)`, else Notice. `Verdict.Network = Network(merged.IMSI)`.
  - `func Hints(i Identity) (iccidHint, imsiHint string)` — masked via `parameters.MaskSubscriberIdentifier`.

- [ ] **Step 1: Write failing tests** in `simwatch_test.go` (table tests): `TestJudge` rows — same SIM → None; first SIM allowed → Baseline; first SIM not allowed → Alert; ICCID changed, allowed → Notice; IMSI changed, not allowed → Alert; swap back to the previous SIM → Notice; incoming only IMSI changed while ICCID cached → Notice with merged identity; incoming empty/sentinel → None; no allowed func result needed when IMSI empty → Notice. `TestExtractIdentity`: interface 2 paths ignored whatever map order (run the case 50 times with a map holding interface 1 and 2); vendor IMSI used only when `USIM.IMSI` absent; `-32768`-style sentinel and whitespace ignored. `TestReadPaths`: only present paths, sorted, nil for empty names. `TestHintsMasked`: output contains neither the full ICCID nor the full IMSI (assert the full value is not a substring).
- [ ] **Step 2: Run** `go test ./internal/simwatch/ -count=1` — expect FAIL (undefined).
- [ ] **Step 3: Implement** the interfaces above in `simwatch.go` (no SQL, no I/O; imports only `internal/devices/adapters` and `internal/parameters`).
- [ ] **Step 4: Run** the package tests — expect PASS; also `go vet ./internal/simwatch/`.
- [ ] **Step 5: Commit** `feat(simwatch): pure SIM change judge` (include the spec and plan files).

---

### Task 2: Storage, plan lookup and the ACS writer

**Files:**
- Create: `backend/internal/store/migrations/0106_device_sim_watch.sql`, `backend/internal/simwatch/recorder/recorder.go` (+ `_test.go`)
- Modify: `backend/internal/onboarding/store.go` (+ `store_test.go`), `backend/internal/store/dbroles/manifest.go` (regenerated), `backend/internal/store/dbroles` tests as needed

**Interfaces:**
- Consumes: Task 1 `Verdict`, `Kind`.
- Produces:
  - Table `device_sim_watch(device_id UUID PRIMARY KEY REFERENCES devices(id) ON DELETE CASCADE, state TEXT NOT NULL CHECK (state IN ('ok','notice','alert')), network TEXT NOT NULL DEFAULT '' CHECK (char_length(network) <= 6), iccid_hint TEXT NOT NULL DEFAULT '' CHECK (char_length(iccid_hint) <= 64), imsi_hint TEXT NOT NULL DEFAULT '' CHECK (char_length(imsi_hint) <= 64), previous_iccid_hint …, previous_imsi_hint … (same shape), detected_at TIMESTAMPTZ, last_checked_at TIMESTAMPTZ NOT NULL DEFAULT now(), approved_at TIMESTAMPTZ, approved_by TEXT, approval_reason TEXT)`.
  - `func (r *Repository) PlanForDevice(ctx context.Context, deviceID string) (*ServicePlan, error)` in `internal/onboarding`: the plan of the latest `IN_SERVICE` onboarding bound to the device (`ORDER BY in_service_at DESC NULLS LAST, updated_at DESC LIMIT 1`, joined to `service_plans`); `(nil, nil)` when there is no such onboarding or it has no plan.
  - Package `recorder` (the only SQL the ACS uses for this table): `type Repository struct`, `func New(db *sql.DB) *Repository`; `func (r *Repository) LastChecked(ctx, deviceID string) (time.Time, error)` (zero when no row); `func (r *Repository) Record(ctx, deviceID string, v simwatch.Verdict, cur, prev simwatch.Identity, at time.Time) error` — idempotent upsert: `None` only sets `last_checked_at`; `Baseline` inserts/updates `state='ok'` with current hints; `Notice`/`Alert` set state, network, hints, previous hints, `detected_at=at`, `last_checked_at=at` and clear `approved_*`, except that an identical repeat (same state and same current hints already stored, state not `ok`) leaves `detected_at` unchanged.

- [ ] **Step 1: Write failing tests** (DB, `-p 1`): recorder — baseline creates `ok`; notice then identical notice keeps `detected_at`; notice→alert updates state; a new verdict clears `approved_*`; `None` creates no row but `LastChecked` stays zero for a device without a row and updates for one with a row; stored hints never equal the full identity (assert by querying all text columns). onboarding — `PlanForDevice` returns the plan for an IN_SERVICE onboarding, `(nil,nil)` for a PLANNED one and for none.
- [ ] **Step 2: Run** `go test ./internal/simwatch/recorder/ ./internal/onboarding/ -p 1 -count=1` with the DSN — expect FAIL.
- [ ] **Step 3: Implement** migration, recorder and `PlanForDevice`; keep SQL for this table only in `recorder`.
- [ ] **Step 4: Regenerate and check grants:** `ACS_UPDATE_DB_GRANTS=1 go test ./internal/store/dbroles/ -count=1` (with the DSN), then run it without the variable; `acs` has SELECT/INSERT/UPDATE on `device_sim_watch` only through the recorder. Run `go list -deps ./cmd/acs | grep internal/alerting` and confirm incident SQL is not pulled into the ACS (if `alerting` appears, report it instead of continuing).
- [ ] **Step 5: Run** the three packages' tests — PASS. Commit `feat(simwatch): device_sim_watch table, recorder, PlanForDevice`.

---

### Task 3: ACS sampler and judging at the cache write

**Files:**
- Create: `backend/cmd/acs/sim_watch.go`, `backend/cmd/acs/sim_watch_test.go`
- Modify: `backend/cmd/acs/session.go` (`cacheParameterValues` at ~820, call site next to `maybeQueueCPEMetrics` at ~437), `backend/cmd/acs/main.go` (handler field, env setting)

**Interfaces:**
- Consumes: Task 1 (`ExtractIdentity`, `ReadPaths`, `Merge`, `Judge`, `Hints`, `CreatedBy`), Task 2 (`recorder.Repository`, `PlanForDevice`).
- Produces:
  - `type simWatchSampler struct{ rec *recorder.Repository; interval time.Duration; lastTried sync.Map }`; handler field `simWatch *simWatchSampler`.
  - `func simWatchIntervalFromEnv() time.Duration` reading `ACS_SIM_WATCH_INTERVAL` (default 1h, `0` disables, an invalid or negative value logs a warning naming the variable and the default, then uses 1h) — same shape as `cpeSampleIntervalFromEnv`.
  - `func (h *handler) maybeQueueSIMWatch(ctx context.Context, device *devices.Device)` — same gates and slack as `maybeQueueCPEMetrics` (`due := interval - min(interval/10, time.Minute)`; throttle by `lastTried`, store the reference time when found not due), but the reference time is `rec.LastChecked`; also skip when `ActiveDiscovery` is active, when `HasOpenJobByCreator(…, simwatch.CreatedBy)`, when discovered names are nil, or when `simwatch.ReadPaths(names)` is empty (never fall back to defaults). Creates `jobs.TypeGetParameter` with `GetParameterPayload{Paths: …}`, created-by `simwatch.CreatedBy`.
  - `func (h *handler) judgeSIM(ctx context.Context, deviceID string, list []cwmp.ParameterValueStruct) (change *simChange, skipIdentity bool)` where `type simChange struct{ Verdict simwatch.Verdict; Cur, Prev simwatch.Identity }`: no-op (`nil,false`) when the reply contains no identity path; otherwise builds `cached` from `h.params.Get`, `incoming` from `list`, loads `PlanForDevice` (plan nil or no PLMNs → allow all, using `plan.AllowsIMSI`), calls `Judge`, persists with `Record` **before** returning. A persist or read error is logged (device id only) and returns `skipIdentity=true` so the caller leaves the identity paths out of the cache upsert.
  - In `cacheParameterValues`: call `judgeSIM` before the upsert and **not** gated on `h.tmfEvents`; when `skipIdentity`, drop the identity paths from `values`; after a successful upsert, if `change != nil` and `Kind` is Notice or Alert, call `h.publishSIMChange(ctx, deviceID, change, now)` (defined in Task 4; until then a no-op stub in this task's file).

- [ ] **Step 1: Write failing tests** in the style of `cpe_metrics_test.go`: sampler — a device with a recent `last_checked_at` queues nothing; one older than interval−slack queues one job; a second call right after queues nothing; interval 0 disables; discovery active, open job, nil names and names without SIM paths all queue nothing; `simWatchIntervalFromEnv` for `""`, `"0"`, `"30m"`, `"15"`, `"-1m"` (the last two log a warning and return 1h). judge — first reply for a device with no cache stores Baseline and no incident-worthy state; a changed ICCID with allowed network stores Notice; with a plan whose `AllowedPLMNs` excludes the IMSI stores Alert; a reply with only the new IMSI merges with the cached ICCID and fires exactly once (second identical reply stores nothing new); recorder error → identity paths are absent from the upsert while other values are cached; a reply from an operator probe (created-by something else) is judged the same; the verdict persists even when `h.tmfEvents == nil`.
- [ ] **Step 2: Run** `go test ./cmd/acs/ -run 'SIMWatch|JudgeSIM' -p 1 -count=1` — FAIL.
- [ ] **Step 3: Implement** the above; register the sampler in `main.go` next to `cpeMetrics` (field in the handler struct near line 539, construction near line 263); call `maybeQueueSIMWatch` at the `maybeQueueCPEMetrics` call site.
- [ ] **Step 4: Run** the new tests then the whole package `go test ./cmd/acs/ -p 1 -count=1` — PASS.
- [ ] **Step 5: Commit** `feat(acs): SIM watch sampler and judging at the cache write`.

---

### Task 4: `SimChanged` event and contract documents

**Files:**
- Modify: `backend/internal/tmf/telemetry/telemetry.go` (+ test), `backend/cmd/acs/sim_watch.go` (replace the stub), `backend/openapi-bssadapter.yaml` (enum near lines 359 and 618), `docs/bss-integration-guide.md` (~411), `docs/integration/09-events.md` (~52), `contract/README.md` (~36), `scripts/contract/record_fixtures.py` (~830) and the webhook-bodies fixture it writes

**Interfaces:**
- Consumes: Task 3 `simChange`, `simwatch.Hints`, `simwatch.Network`.
- Produces: `type SimChange struct{ Verdict, Network, ICCIDHint, IMSIHint, PreviousICCIDHint, PreviousIMSIHint string }` and `func PublishSimChanged(ctx context.Context, p <the publisher type PublishCellularStateChanged takes>, account, deviceID, protocol string, c SimChange, at time.Time) error`, modelled line for line on `PublishCellularStateChanged` (telemetry.go ~137–148); event type string `SimChanged`; `func (h *handler) publishSIMChange(ctx, deviceID string, c *simChange, at time.Time)` building masked hints and publishing (nil `tmfEvents` → no-op, error logged with device id only).

- [ ] **Step 1: Write failing tests:** a telemetry test asserting the published event's type is `SimChanged` and its serialized payload contains the hints and network and **not** the full identifiers (use test identities and assert absence); a ACS test that a Notice verdict calls the publisher once and None/Baseline never does.
- [ ] **Step 2: Run** `go test ./internal/tmf/telemetry/ ./cmd/acs/ -run 'SimChanged|PublishSIM' -count=1` — FAIL.
- [ ] **Step 3: Implement** the function and wiring; add `SimChanged` to every contract document and fixture listed under Files, copying how `CellularStateChanged` appears in each (no tag or version number is changed; note in `contract/README.md` that the event is an addition pending the next release).
- [ ] **Step 4: Run** the tests plus whatever contract checks `scripts/ci-local.sh` runs for the `spec` stage on its own (`scripts/ci-local.sh spec` if it takes a stage argument; otherwise run the repo's contract-drift test directly) — PASS.
- [ ] **Step 5: Commit** `feat(events): SimChanged event and contract documents`.

---

### Task 5: API — state on the SIM view and the approval endpoint

**Files:**
- Create: `backend/internal/simwatch/reads/reads.go` (+ test)
- Modify: `backend/cmd/api/sim_handlers.go` (+ test), `backend/cmd/api/routes.go` (near line 246), `backend/cmd/api/main.go` (wire the repository), `backend/openapi.yaml`, `frontend/src/api/generated.ts` (regenerated)

**Interfaces:**
- Consumes: Task 2 table.
- Produces:
  - Package `reads` (API-side SQL): `type State struct{ State, Network, ICCIDHint, IMSIHint, PreviousICCIDHint, PreviousIMSIHint string; DetectedAt, ApprovedAt *time.Time; ApprovedBy, ApprovalReason string }`; `func New(db *sql.DB) *Repository`; `func (r *Repository) Get(ctx, deviceID string) (*State, error)` (`nil,nil` when no row); `var ErrNothingToApprove = errors.New("no SIM change awaiting approval")`; `func (r *Repository) Approve(ctx, deviceID, actor, reason string, at time.Time) error` — `UPDATE … SET state='ok', approved_at=$, approved_by=$, approval_reason=$ WHERE device_id=$ AND state IN ('notice','alert')`; zero rows → `ErrNothingToApprove`.
  - `simView` gains `SimWatch *simWatchView \`json:"sim_watch,omitempty"\`` (fields: `state`, `network`, `iccid_hint`, `imsi_hint`, `previous_iccid_hint`, `previous_imsi_hint`, `detected_at`, `approved_at`, `approved_by`); `loadSIMView` fills it from `Get`.
  - `func (h *handler) approveSIMWatch(w http.ResponseWriter, r *http.Request)` on `routePerm("POST", "/api/v1/devices/{id}/sim-watch/approve", operators.PermDevicesWrite, h.approveSIMWatch)`: `getScopedDevice`; body `{reason string}` limited to 4096 bytes; reason trimmed, minimum 10 characters (`const minSIMApproveReason = 10`) else 400; audit **first** with `h.auditor.Record(ctx, operatorFromRequest(r), deviceID, "SimChangeApproved", map[string]any{"reason": reason, "network": state.Network})` and refuse with 503 when the audit write fails (as `revealSIM`); then `Approve`; `ErrNothingToApprove` → 409; success → 200 with the new `simWatchView`.

- [ ] **Step 1: Write failing tests:** reads (DB) — `Get` nil without a row; `Approve` on notice and alert returns to `ok` and stores actor/reason; `Approve` on `ok` or no row → `ErrNothingToApprove`; a re-recorded identical SIM after approval (use the recorder's `Baseline` for the same hints) keeps `ok`. handler — whitespace-only and short reasons → 400; wrong permission → 403; device outside the caller's scope → the same status `getScopedDevice` gives; audit failure → 503 and state unchanged; nothing to approve → 409; success audited with no identifier hints beyond `network`; `GET …/sim` includes `sim_watch` with masked hints only.
- [ ] **Step 2: Run** `go test ./internal/simwatch/reads/ ./cmd/api/ -run 'SIMWatch|Approve' -p 1 -count=1` — FAIL.
- [ ] **Step 3: Implement** the above; add the route, schema (`SimWatch`) and path to `openapi.yaml`; regenerate `generated.ts` with the repo's generate script (`cd frontend && npm run generate:api`, Node 22).
- [ ] **Step 4: Run** the tests, `go test ./cmd/api/ -run OpenAPI -count=1` (the drift test) and the dbroles test with the DSN — PASS; `api` gets SELECT/UPDATE on `device_sim_watch` through `reads` (regenerate the manifest if its test says so, and read the diff).
- [ ] **Step 5: Commit** `feat(api): sim_watch state and approval endpoint`.

---

### Task 6: Incidents in the BSS adapter

**Files:**
- Modify: `backend/internal/alerting/policy.go` (+ test), `backend/internal/alerting/incidents.go` (+ test), `backend/internal/alerting/repository.go` (+ test), `backend/cmd/bssadapter/incident_worker.go` (+ test)

**Interfaces:**
- Consumes: Task 2 table (read through the alerting package: SELECT only).
- Produces:
  - `Classify`: when no policy entry matches, fault code `sim_change_alert` defaults to `P2` and `sim_change_notice` to `P4` (all other codes keep `P3`; offline keeps `P2`).
  - `const SIMChangeConditionKey = "acs-sim-change"`.
  - `type SIMWatchDevice struct{ TenantID, AccountKey, DeviceID, CustomerTier, State, Network, ICCIDHint, IMSIHint, PreviousICCIDHint, PreviousIMSIHint string; DetectedAt time.Time; GroupIDs []string }` and `func (r *Repository) SIMWatchDevices(ctx, limit int) ([]SIMWatchDevice, error)` — devices whose `device_sim_watch.state IN ('notice','alert')`, joined to `account_device_mappings` exactly as `OfflineDevices` does (so an unmapped device is not returned), with `GroupIDsForDevice`.
  - `func (r *IncidentRepository) RecoverSIMWatch(ctx) ([]Incident, error)` — sets `recovered` (same semantics as `SetState(…, "recovered", …)`) for incidents with `condition_key = SIMChangeConditionKey` and state open/acknowledged/suppressed whose device has `device_sim_watch.state = 'ok'` or no row; returns them.
  - `func (r *IncidentRepository) ByKey(ctx, tenantID, deviceID, conditionKey string) (*Incident, error)` (`ErrIncidentNotFound` when absent).
  - `func (h *handler) ingestSIMWatch(ctx)` in the worker, called from `runOfflineIncidentLoop` after `ingestBulkDataHealth`: recover first (notify `ALERT_RECOVERED`), then for each device resolve the policy (`alerting.Resolve`; none → skip), compute `priority := alerting.Classify(p, "sim_change_"+state, false)`, look up `ByKey` for the previous state, call `OpenWithCreated` with summary `"SIM replaced on this CPE (network "+network+"); approval needed"` for notice, `"SIM replaced with a network the plan does not allow ("+network+")"` for alert, details `{state, network, iccid_hint, imsi_hint, previous_iccid_hint, previous_imsi_hint, detected_at}` (hints only), `next` as in `ingestOfflineDevices`; notify `ALERT_OPENED` when created **or** the previous incident was `recovered`/`closed`/absent. A priority change on an already-open incident is applied by `OpenWithCreated` and does not notify.

- [ ] **Step 1: Write failing tests:** policy — the two defaults and an explicit policy mapping overriding them; repository (DB) — `SIMWatchDevices` returns only notice/alert rows with a mapping, none for an unmapped device; `RecoverSIMWatch` recovers when the row is `ok`/missing and leaves a notice-state device's incident open; worker — one incident per device across two cycles, `ALERT_OPENED` once, notice→alert raises priority without a second notification, approval (row `ok`) recovers and notifies, a later new notice reopens and notifies again, an unmapped device never errors or opens anything, a device with no matching policy opens nothing, incident `details` and summary never contain a full identifier.
- [ ] **Step 2: Run** `go test ./internal/alerting/ ./cmd/bssadapter/ -p 1 -count=1` with the DSN — FAIL.
- [ ] **Step 3: Implement** as specified.
- [ ] **Step 4: Run** both packages — PASS; run the dbroles test (bssadapter gets only SELECT on `device_sim_watch`; regenerate the manifest and read the diff).
- [ ] **Step 5: Commit** `feat(bssadapter): SIM change incidents`.

---

### Task 7: Console banner and approval

**Files:**
- Modify: `frontend/src/components/SimPanel.tsx` (+ its test file), `frontend/src/api/client.ts`, `frontend/src/api/types.ts` (if hand-written types are used), `frontend/src/screens/DeviceDetail.history.test.tsx` only if its mock needs the new call
- Modify (e2e): the existing Playwright spec that opens a device's Cellular tab (find it under `frontend/e2e/`)

**Interfaces:**
- Consumes: Task 5 `sim_watch` on `GET /api/v1/devices/{id}/sim`, and `POST /api/v1/devices/{id}/sim-watch/approve` `{reason}`.
- Produces: `api.approveSimWatch(id: string, reason: string)` in `client.ts`; in `SimPanel` a banner above the SIM fields when `sim_watch.state` is `notice` or `alert` (`role="alert"` for alert, `role="status"` for notice) showing the network, the masked hints, the previous hints and detection time, with an **Approve this SIM** button that opens a small dialog with a required reason textarea (button disabled until the trimmed reason has at least 10 characters), submits, refreshes the view, and shows the server's error text on failure. For `ok` with `approved_by` set, a muted line "Approved by … ".

- [ ] **Step 1: Write failing tests** (`SimPanel.test.tsx`): no banner for `ok` or absent `sim_watch`; notice and alert render the banner with the right role and the masked values (assert the full identifiers from the fixture never appear); the approve button is disabled for a 9-character or whitespace reason and enabled for 10+; submitting calls `approveSimWatch` once with the trimmed reason and then reloads the view; a failed call shows the error and keeps the dialog open; double-clicking submit sends one request.
- [ ] **Step 2: Run** the file with the Node 22 vitest command — FAIL.
- [ ] **Step 3: Implement** the banner, dialog and client call, following the panel's existing styling and the `Approve`-style dialogs already in the console (reuse the existing dialog component if one exists).
- [ ] **Step 4: Extend the e2e spec** with one flow: the stubbed `…/sim` reply carries a notice → the banner shows on the Cellular tab → entering a reason and approving removes it. Run the frontend unit suite for the touched files and `npm run build` and `npx tsc --noEmit` — PASS; run the e2e spec with Playwright (`scripts/ci-local.sh --e2e` runs all of them; running just this spec is enough here).
- [ ] **Step 5: Commit** `feat(console): SIM change banner and approval`.

---

### Task 8: Operator documentation and whole-branch verification

**Files:**
- Create: `docs/SIM-WATCH.md`
- Modify: `docs/CPE-PROBE.md` (one cross-link line), `docs/LAB-TLS.md` only if it lists environment variables (add `ACS_SIM_WATCH_INTERVAL`), the ACS environment documentation where `ACS_CPE_SAMPLE_INTERVAL` is listed

- [ ] **Step 1: Write `docs/SIM-WATCH.md`:** what is detected and the verdict table; what an operator sees (banner, incident, event) and how to approve; the privacy rules (hints only); `ACS_SIM_WATCH_INTERVAL` (default 1h, `0` disables, invalid value warns and uses 1h); limits (alert only, interface 1, CWMP only, a swap while the CPE is offline is noticed at its next check-in, an unmapped device gets no incident); and the **lab check**: (1) Parameters page → "Read parameters now" → SIM preset, note which of `USIM.ICCID`, `USIM.IMSI`, `X_ALU-COM_CurrentUsedIMSI` hold values (record only which are populated, never the values); (2) after the first sampler read the Cellular tab's SIM panel shows no banner (baseline); (3) with the user's agreement, swap the SIM and wait one interval: the notice/alert banner and incident appear; approve with a reason; the incident recovers; (4) troubleshooting — failed jobs created by `system:sim-watch`, `device_sim_watch.last_checked_at`.
- [ ] **Step 2: Run the whole check** `scripts/ci-local.sh --e2e` (Go from `go.mod`, Node 22; the throwaway Postgres `acs-ci-pg` must be running) — expect every stage green; report any failure with output instead of retrying blindly.
- [ ] **Step 3: Run** `git diff origin/main --stat` and confirm no file outside the plan's lists changed, no contract tag or `go.mod` change, and that `grep -rn` over the diff finds no full-identifier test value logged.
- [ ] **Step 4: Commit** `docs: SIM watch operator guide`.

---

## Self-review notes

- Spec coverage: verdicts and rules → Task 1; precondition (vendor IMSI leaf as source) → Task 1 `ExtractIdentity` + Task 8 lab check; reading, durability order, plan lookup → Tasks 2–3; storage → Task 2; incident/event/banner/approval → Tasks 4–7; roles → grants steps in Tasks 2, 5, 6; testing and docs → each task and Task 8.
- One deliberate deviation from the spec text: the incident condition key is the constant `acs-sim-change` (like `acs-offline`) instead of `sim-change:<device>`, because the incident's unique key already includes the device id.
- Names used across tasks: `simwatch.{Identity, Verdict, Kind, Judge, Merge, ReadPaths, ExtractIdentity, Hints, Network, CreatedBy}`, `recorder.Repository.{LastChecked, Record}`, `reads.Repository.{Get, Approve}`, `PlanForDevice`, `telemetry.PublishSimChanged`, `alerting.SIMChangeConditionKey`.
