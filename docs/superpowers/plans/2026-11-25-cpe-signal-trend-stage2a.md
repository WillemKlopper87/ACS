# CPE Signal Trend (telemetry stage 2a) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Record each cellular CPE's signal readings at its check-ins and show them as a trend line on the device's Cellular & SIM page.

**Architecture:** A sampler in `cmd/acs`, modelled on the Wi-Fi insights sampler, queues one `GetParameterValues` at a check-in when the device's last sample is older than the interval; the reply is parsed and stored in a new `device_cpe_samples` table, and is never written to the parameter cache or `parameter_history`. The API serves the samples, averaged into at most about 500 points, and the console draws them as an inline-SVG line chart.

**Tech Stack:** Go (pgx via `database/sql`), PostgreSQL, React 19, TypeScript 6, Vitest.

**Spec:** `docs/superpowers/specs/2026-11-25-cpe-telemetry-design.md`, section "Phase 2: telemetry" (signal parts only) and "Sequencing" stage 2a. Link-change columns, `device_cpe_location` and the Availability markers are stage 2b and are not in this plan.

## Global Constraints

- Forward-only migration, next number `0105`; no subscriber identifiers (IMSI, ICCID, MSISDN, IMEI) and no location in any new table, log line or API field.
- Everything here is a read: nothing in this plan writes to a CPE.
- Signal replies never go through `parameters.Repository.Upsert` (the cache and `parameter_history` stay untouched; a signal value changes on every reading).
- Sentinel readings (`adapters.IsCellularSentinel`, e.g. Nokia LTE `-32768` while attached only to 5G) are stored as NULL, never as a value.
- `internal/cpemetrics` (reads and pure logic) must contain no write SQL: only `cmd/acs` may hold INSERT rights on `device_cpe_samples` (writer lives in `internal/cpemetrics/recorder`); the database-role manifest must show it.
- Defaults: sample interval `ACS_CPE_SAMPLE_INTERVAL` 15 minutes (`0` turns sampling off); retention `ACS_RETENTION_CPE_SAMPLES_DAYS` 30; API range `hours` 1 to 720, default 168.
- Go tests that reset the schema run against a throwaway Postgres only (`ACS_TEST_POSTGRES_DSN`, as `scripts/ci-local.sh` sets it), one package at a time (`-p 1`); never the lab database.
- Checks run with `scripts/ci-local.sh` (Go toolchain from `go.mod`, Node 22 for vitest). GitHub Actions is unavailable.

## Review Focus

- Sentinel values: a Nokia LTE reply of `-32768` while on 5G must be stored as NULL and must not draw a point at -32768 dBm (Tasks 3 and 6).
- A device with no discovered signal paths (not cellular, or not yet discovered): no job is queued and no row is written (Task 4).
- A reply that yields no usable metric, or a read the device only partly answered: no empty row (Tasks 3 and 4; `dispatch.go` already fails a partial read before the recorder runs).
- Rapid or repeated check-ins: one open job at a time, the per-device throttle holds, and a duplicate `(device_id, sampled_at)` is ignored without error (Tasks 2 and 4).
- A reply to the history API that is empty or lacks `samples`, or a metric the device never reports (e.g. SNR on a model without it): the panel shows a readable empty state and does not crash (Task 6).

## File Structure

- Modify `backend/internal/devices/adapters/cellular.go` and its test: an SNR field.
- Create `backend/internal/store/migrations/0105_device_cpe_samples.sql`.
- Create `backend/internal/cpemetrics/` (`cpemetrics.go`, `reads.go`, `history.go` and tests): types, read plan and parsing, the history query. Create `backend/internal/cpemetrics/recorder/recorder.go` (+ test): the only INSERT.
- Modify `backend/internal/retention/retention.go` (+ test), `backend/internal/store/dbroles/roles.go` and the generated `manifest.go`.
- Create `backend/cmd/acs/cpe_metrics.go` (+ test); modify `backend/cmd/acs/main.go`, `session.go`, `dispatch.go`; modify `backend/internal/observability/metrics.go`.
- Create `backend/cmd/api/signal_handlers.go` (+ test); modify `backend/cmd/api/routes.go`, `main.go`, `backend/openapi.yaml`, `frontend/src/api/generated.ts`.
- Create `frontend/src/lib/signalSeries.ts` and `frontend/src/components/SignalPanel.tsx` (+ tests); modify `frontend/src/api/client.ts`, `types.ts`, `frontend/src/screens/DeviceDetail.tsx`.
- Create `docs/CPE-SIGNAL-HISTORY.md`.

---

### Task 1: An SNR field in the cellular adapter

**Files:**
- Modify: `backend/internal/devices/adapters/cellular.go`
- Test: `backend/internal/devices/adapters/cellular_test.go` (add to the existing file)

**Interfaces:**
- Produces: `const CellularSNR CellularField = "snr"`; `cellularLeafMatches(CellularSNR, leaf)` is true for the compacted leaves `SNR`, `SINR`, `SNRCURRENT`, `SINRCURRENT` and false for `AvgSNRMonth`, `MinSNRMonth`, `MaxSNRMonth`; `CellularPathCandidates(root, CellularSNR, instance)` returns `nil` (TR-181 defines no SNR leaf).

- [ ] **Step 1: Write the failing test** `TestResolveCellularReadPathFindsVendorSNR`: with discovered names `Device.Cellular.Interface.1.X_ALU-COM_Attached5GCellStat.SNRCurrent` and `...AttachedLteCellStat.SNRCurrent` and `...Attached5GCellStat.AvgSNRMonth`, `ResolveCellularReadPath("DEVICE2", CellularSNR, "1", names)` returns the `Attached5GCellStat.SNRCurrent` path (5G preferred) with `Discovered: true`; with only the LTE leaf it returns the LTE path; with only `AvgSNRMonth` it returns false.
- [ ] **Step 2: Run to verify it fails.** `cd backend && go test ./internal/devices/adapters -run SNR -count=1`. Expected: FAIL (undefined `CellularSNR`).
- [ ] **Step 3: Add the constant, its alias list in `cellularLeafMatches`, and a `nil` entry in `CellularPathCandidates`.** Do not add it to `CellularStateFields` or to any existing diagnostics read list (those lists stay as they are).
- [ ] **Step 4: Run the package tests.** `go test ./internal/devices/adapters -count=1`. Expected: PASS.
- [ ] **Step 5: Commit** `feat(adapters): SNR as a cellular field`.

### Task 2: Storage, retention and role grants

**Files:**
- Create: `backend/internal/store/migrations/0105_device_cpe_samples.sql`, `backend/internal/cpemetrics/cpemetrics.go`, `backend/internal/cpemetrics/history.go`, `backend/internal/cpemetrics/recorder/recorder.go`
- Modify: `backend/internal/retention/retention.go`, `backend/internal/store/dbroles/roles.go`, generated `backend/internal/store/dbroles/manifest.go`
- Test: `backend/internal/cpemetrics/history_test.go`, `backend/internal/cpemetrics/recorder/recorder_test.go`, the retention test file

**Interfaces:**
- Produces, in package `cpemetrics`:
  - `const CreatedBy = "system:cpe-metrics"`
  - `type Sample struct { RSRP, RSRQ, SNR, RSSI *float64; Band, RAT string }`
  - `type Point struct { At time.Time; RSRP, RSRQ, SNR, RSSI *float64; Band, RAT string }`
  - `type History struct { From, To time.Time; BucketSeconds, IntervalSeconds int64; Samples []Point }` (JSON tags `from`, `to`, `bucket_seconds`, `interval_seconds`, `samples`; point tags `at`, `rsrp`, `rsrq`, `snr`, `rssi`, `band`, `rat`; null metrics serialise as `null`, empty text as `""`)
  - `type Repository struct{ db *sql.DB }`; `NewRepository(db *sql.DB) *Repository`; `(*Repository).LastSampleAt(ctx, deviceID string) (time.Time, error)` (zero time when none); `(*Repository).History(ctx, deviceID string, from, to time.Time, maxPoints int) (History, error)`.
- Produces, in package `recorder`: `type Recorder struct{ db *sql.DB }`; `New(db *sql.DB) *Recorder`; `(*Recorder).Record(ctx, deviceID string, at time.Time, s cpemetrics.Sample) error` (an INSERT with `ON CONFLICT (device_id, sampled_at) DO NOTHING`).
- Produces: `retention.Policy.CPESamplesDays int` (default 30, env `ACS_RETENTION_CPE_SAMPLES_DAYS`) and a rule pruning `device_cpe_samples` where `sampled_at < $1` by key `id`.

- [ ] **Step 1: Write the failing tests.**
  - `history_test.go` (DB test, skipped without `ACS_TEST_POSTGRES_DSN`, same setup style as `internal/connectivity/connectivity_test.go`): `TestHistoryReturnsRowsInOrderAndNullsStayNull` (insert three rows through the recorder, one with a nil SNR; the nil comes back nil), `TestHistoryAveragesIntoAtMostMaxPoints` (insert 1,200 rows over 30 days, `History(..., 500)` returns at most 500 points, `BucketSeconds > 0`, an averaged metric lies between its inputs, `Band` is the last non-empty value in a bucket), `TestHistoryExcludesOtherDevicesAndOutOfRange`.
  - `recorder_test.go`: `TestRecordIgnoresADuplicateSample` (same `(device, sampled_at)` twice: one row, no error).
  - retention test: the default is 30 days, the env var overrides it, and `0` prunes nothing for this table (follow the existing tests for `WiFiInsightsDays`).
- [ ] **Step 2: Run to verify they fail.** `go test -p 1 ./internal/cpemetrics/... ./internal/retention -count=1` with `ACS_TEST_POSTGRES_DSN` pointing at the throwaway Postgres. Expected: FAIL (packages and table missing).
- [ ] **Step 3: Implement.**
  - Migration: `device_cpe_samples (id BIGSERIAL PRIMARY KEY, device_id UUID NOT NULL REFERENCES devices(id) ON DELETE CASCADE, sampled_at TIMESTAMPTZ NOT NULL, rsrp/rsrq/snr/rssi DOUBLE PRECISION NULL, band TEXT NOT NULL DEFAULT '', rat TEXT NOT NULL DEFAULT '', UNIQUE (device_id, sampled_at))` and an index on `sampled_at` (mirror `wifi_samples`).
  - `History`: read the rows in range (cap 50,000), bucket in Go when more than `maxPoints`, with `BucketSeconds = ceil(span / maxPoints)`; average each numeric metric ignoring NULLs (all NULL stays nil); `IntervalSeconds` is the median gap between raw rows (0 with fewer than two).
  - Retention: add the policy field, env parse and the rule next to the `wifi_samples` rule.
  - Roles: add `"device_cpe_samples": {"SELECT", "DELETE"}` to `Extra["api"]` in `roles.go`, then regenerate the manifest: `ACS_UPDATE_DB_GRANTS=1 go test -p 1 ./internal/store/dbroles -run TestManifestCoversTheCode` and read the diff.
- [ ] **Step 4: Run to verify they pass**, including the role tests: `go test -p 1 ./internal/cpemetrics/... ./internal/retention ./internal/store/dbroles -count=1`. Expected: PASS, and the manifest diff gives `acs` SELECT and INSERT on `device_cpe_samples`, `api` SELECT and DELETE, and no other service anything.
- [ ] **Step 5: Commit** `feat(cpemetrics): device_cpe_samples storage, retention and grants`.

### Task 3: The read plan and the reply parser

**Files:**
- Create: `backend/internal/cpemetrics/reads.go`
- Test: `backend/internal/cpemetrics/reads_test.go`

**Interfaces:**
- Consumes: `adapters.ResolveCellularReadPath`, `adapters.NormalizeCellularValue`, the field constants including `CellularSNR`; `cwmp.ParameterValueStruct{Name, Value string}`; `Sample` from Task 2.
- Produces: `type Read struct { Field adapters.CellularField; Path string }`; `func PlanReads(root string, names map[string]bool) []Read` (fields RSRP, RSRQ, SNR, RSSI, Band, RAT, instance `"1"`, only fields the names resolve, one entry per distinct path, order RSRP, RSRQ, SNR, RSSI, Band, RAT; `nil` when names is `nil` or empty); `func Parse(reads []Read, values []cwmp.ParameterValueStruct) (Sample, bool)` (the bool is true only when at least one metric or text field is present).

- [ ] **Step 1: Write the failing tests** with the FastMile-shaped names of Task 1's test plus `Device.Cellular.Interface.1.CurrentAccessTechnology`:
  - `TestPlanReadsPrefers5GAndSkipsUnresolved`: the plan has `Attached5GCellStat` paths for RSRP, RSRQ, SNR, RSSI and Band (not the LTE ones) and the RAT path; a names map with no cellular paths gives `nil`; `nil` names give `nil`; `"InternetGatewayDevice."` root gives `nil`.
  - `TestParseStoresSentinelAsNil`: a reply with LTE-style `-32768` for RSRP and `10` for SNR gives `RSRP == nil`, `*SNR == 10`, `ok == true`.
  - `TestParseReturnsNotOKWhenNothingUsable`: all sentinels or an empty reply gives `ok == false`.
  - `TestParseKeepsTextFields`: `Band "n78"` and `RAT "NR5G-SA"` come through trimmed.
- [ ] **Step 2: Run to verify they fail.** `go test ./internal/cpemetrics -run 'PlanReads|Parse' -count=1`. Expected: FAIL.
- [ ] **Step 3: Implement `PlanReads` and `Parse`** over the Task 1 and adapter functions; numeric fields use `NormalizeCellularValue` and take the value only when it is a `float64`, text fields only when it is a non-empty string.
- [ ] **Step 4: Run to verify they pass**, same command. Expected: PASS.
- [ ] **Step 5: Commit** `feat(cpemetrics): signal read plan and reply parser`.

### Task 4: The sampler

**Files:**
- Create: `backend/cmd/acs/cpe_metrics.go`
- Modify: `backend/cmd/acs/main.go` (handler field and construction next to `wifiInsights`, env `ACS_CPE_SAMPLE_INTERVAL`), `backend/cmd/acs/session.go` (call at the check-in, beside `maybeQueueWiFiInsights`), `backend/cmd/acs/dispatch.go` (route a `GetParameterValuesResponse` whose job `CreatedBy == cpemetrics.CreatedBy` to the recorder and return before `cacheParameterValues`), `backend/internal/observability/metrics.go`
- Test: `backend/cmd/acs/cpe_metrics_test.go`

**Interfaces:**
- Consumes: Task 2's `Repository.LastSampleAt`, `recorder.Recorder.Record`, `CreatedBy`; Task 3's `PlanReads`, `Parse`; `h.jobs.HasOpenJobByCreator`, `h.jobs.ActiveDiscovery`, `h.jobs.Create`, `h.params.GetNames` as `wifi_insights.go` uses them.
- Produces: `type cpeMetricsSampler struct { repo *cpemetrics.Repository; rec *recorder.Recorder; interval time.Duration; lastTried sync.Map }`; `func (h *handler) maybeQueueCPEMetrics(ctx context.Context, device *devices.Device)`; `func (h *handler) recordCPEMetrics(ctx context.Context, deviceID string, list []cwmp.ParameterValueStruct)`; metric `CPESamplesTotal *prometheus.CounterVec` (label `result`: `recorded`, `failed`, `empty`).

- [ ] **Step 1: Write the failing tests** (in the style of the repository's other `cmd/acs` handler tests; add a table-driven test of the gating if the handler cannot be built in a unit test):
  - `TestMaybeQueueCPEMetricsQueuesOneReadWhenDue`: a device whose discovered names include FastMile signal paths and whose last sample is older than the interval gets exactly one `GET_PARAMETER` job created by `cpemetrics.CreatedBy` with the planned paths.
  - `TestMaybeQueueCPEMetricsSkips`: no job when the interval is 0, when the last sample is newer than the interval, when a job by this creator is open, when a discovery walk is active, when names are not discovered, and when `PlanReads` is empty (a non-cellular device).
  - `TestRecordCPEMetricsStoresAndIgnoresEmpty`: a usable reply writes one row (verify through `History`); a reply of only sentinels writes none and counts `empty`.
  - `TestCPEMetricsReplyBypassesTheParameterCache`: after a recorded reply the device's parameter cache and `parameter_history` have no new rows for the signal paths.
- [ ] **Step 2: Run to verify they fail.** `go test -p 1 ./cmd/acs -run CPEMetrics -count=1` with the throwaway Postgres. Expected: FAIL.
- [ ] **Step 3: Implement** `cpe_metrics.go` by the pattern of `wifi_insights.go` (same gates, same throttle map, same order of checks), pass `device` (for `DataModelRoot`) from `session.go`, add the routing branch in `dispatch.go` beside the Wi-Fi one, parse the env as the Wi-Fi interval is parsed (default `15*time.Minute`; a value of `0` disables), declare the counter the way `JobsCreatedTotal` is declared and increment it in `recordCPEMetrics`.
- [ ] **Step 4: Run to verify they pass**, then the package: `go test -p 1 ./cmd/acs -count=1`. Expected: PASS.
- [ ] **Step 5: Commit** `feat(acs): sample signal readings at check-ins`.

### Task 5: The history API

**Files:**
- Create: `backend/cmd/api/signal_handlers.go`
- Modify: `backend/cmd/api/routes.go`, `backend/cmd/api/main.go` (handler field `cpeMetrics *cpemetrics.Repository`), `backend/openapi.yaml`, `frontend/src/api/generated.ts` (regenerated)
- Test: `backend/cmd/api/signal_handlers_test.go`

**Interfaces:**
- Consumes: `cpemetrics.Repository.History`, `h.getScopedDevice`, `writeJSON` (see `connectivity_handlers.go` for the pattern).
- Produces: `GET /api/v1/devices/{id}/signal-history?hours=` (route permission `ro`) returning the `cpemetrics.History` JSON of Task 2 with `samples` always an array (never `null`); `hours` outside 1 to 720 or not a whole number is 400; a device outside the caller's scope is 404; OpenAPI schema `SignalHistory` and `SignalPoint`.

- [ ] **Step 1: Write the failing tests** (`TestSignalHistory...`, following how the repository's other API handler tests build a handler and device): default range works and `samples` is `[]` for a device with none; `hours=0`, `hours=721` and `hours=abc` give 400; another tenant's device gives 404; a device with three stored samples returns them with `null` metrics preserved.
- [ ] **Step 2: Run to verify they fail.** `go test -p 1 ./cmd/api -run SignalHistory -count=1`. Expected: FAIL.
- [ ] **Step 3: Implement the handler and route, add the repository to the handler, and extend `openapi.yaml`** (the endpoint under the Devices tag with a description saying samples come from check-ins, are averaged to about 500 points and that resolution is the larger of the sample interval and the CPE's check-in interval). Regenerate the types: `cd frontend && npm run generate:api` (under Node 22).
- [ ] **Step 4: Run to verify they pass**, then `go test -p 1 ./cmd/api -count=1` and `bash scripts/ci-local.sh --only spec`. Expected: PASS and "All selected checks passed."
- [ ] **Step 5: Commit** `feat(api): signal history endpoint`.

### Task 6: The Signal panel

**Files:**
- Create: `frontend/src/lib/signalSeries.ts`, `frontend/src/components/SignalPanel.tsx`
- Modify: `frontend/src/api/client.ts` (`getSignalHistory`), `frontend/src/api/types.ts` (`SignalPoint`, `SignalHistory`), `frontend/src/screens/DeviceDetail.tsx` (render the panel on the Cellular page, after `SimPanel`)
- Test: `frontend/src/lib/signalSeries.test.ts`, `frontend/src/components/SignalPanel.test.tsx`, `frontend/src/screens/DeviceDetail.history.test.tsx` (extend its Cellular case)

**Interfaces:**
- Consumes: `GET /api/v1/devices/{id}/signal-history?hours=`; `fmtTime` and `fmtDuration` from `frontend/src/lib/format.ts`.
- Produces: `export type SignalMetric = "rsrp" | "rsrq" | "snr" | "rssi"`; `export function buildSeries(points: SignalPoint[], metric: SignalMetric, from: number, to: number, intervalSeconds: number): { segments: { x: number; y: number }[][]; min: number; max: number; count: number }` (x in 0 to 1000 across the window, y in 0 to 100 with 0 at the top for the largest value; a metric `null` is skipped; a gap longer than `max(2.5 * intervalSeconds, 300)` seconds, or a skipped point, breaks the segment); `export function SignalPanel(props: { deviceId: string }): JSX.Element` (a `div.panel` with an `h3` "Signal"); `api.getSignalHistory(id: string, hours: number): Promise<SignalHistory>`.

- [ ] **Step 1: Write the failing tests.**
  - `signalSeries.test.ts`: two close points make one segment; a gap over the threshold makes two segments; a `null` metric point is skipped and splits; a single point is one segment of one point; no points gives `count: 0`; `min` and `max` ignore nulls; with a flat series `max > min` is not required but `y` must not be `NaN`.
  - `SignalPanel.test.tsx` (mock `../api/client`): shows the metric buttons (RSRP, RSRQ, SNR, RSSI) and range buttons (24 hours, 7 days, 30 days) and asks for 168 hours first; choosing 30 days asks for 720; a reply with samples draws an SVG with a role and label naming the metric and count; "Show as table" lists the rows with their times; a reply with `samples: []` shows "No signal samples yet" and the sample interval note; a reply with no `samples` key (malformed) shows the same empty state and does not throw; a metric with no readings shows "No readings for this metric on this device."; a rejected request shows "Could not load the signal history."; the note states that the resolution is the larger of the sample interval and the CPE's check-in interval.
  - `DeviceDetail.history.test.tsx`: the Cellular page renders the Signal heading (the mock's generic fallback means `getSignalHistory` returns an object without `samples`, which must not crash the page).
- [ ] **Step 2: Run to verify they fail.** `cd frontend && npx --yes -p node@22 node ./node_modules/vitest/vitest.mjs run src/lib/signalSeries.test.ts src/components/SignalPanel.test.tsx src/screens/DeviceDetail.history.test.tsx`. Expected: FAIL.
- [ ] **Step 3: Implement** the types, client method, `buildSeries` and the panel: an inline-SVG line chart in the style of `ConnectivityTimeline.tsx` (no new dependency, thin line, console colour tokens, text min and max labels and the time ends, a table view for accessibility); read `data?.samples ?? []` so a malformed reply cannot crash the page. Render `<SignalPanel deviceId={id} />` in the `section === "cellular"` block after `SimPanel`.
- [ ] **Step 4: Run to verify they pass**, then `bash scripts/ci-local.sh --only spec,frontend`. Expected: PASS and "All selected checks passed."
- [ ] **Step 5: Commit** `feat(console): Signal panel on the Cellular page`.

### Task 7: Documentation and the full check

**Files:**
- Create: `docs/CPE-SIGNAL-HISTORY.md`

- [ ] **Step 1: Write `docs/CPE-SIGNAL-HISTORY.md`** with these sections only: what is sampled and where it comes from (signal metrics, band and technology, at the device's check-ins); what the chart's resolution is (the larger of `ACS_CPE_SAMPLE_INTERVAL` and the check-in interval); the two settings and their defaults (`ACS_CPE_SAMPLE_INTERVAL` 15 minutes, `0` turns it off; `ACS_RETENTION_CPE_SAMPLES_DAYS` 30); what it does not store (no subscriber identifiers, no location, nothing in the parameter cache or history); how to check it on the lab CPE (after two check-ins the Signal panel on the Cellular & SIM page shows points; if not, check that the device has discovered cellular parameters).
- [ ] **Step 2: Run the full local checks.** `bash scripts/ci-local.sh --only go,migrate,spec,frontend,vuln`. Expected: "All selected checks passed." Record any stage that fails and fix it before the review.
- [ ] **Step 3: Commit** `docs: CPE signal history`.
