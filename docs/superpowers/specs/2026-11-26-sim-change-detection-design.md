# SIM change detection (alert only) — design

Status: revised after a builder check against the code; for approval. Phase 3
of the CPE telemetry programme (`2026-11-25-cpe-telemetry-design.md`).
Approach A was chosen: judge at the parameter-cache write, with a small
check-in sampler so the SIM fields are read.

## Goal

Tell the operator, and the NaaS/BSS side, when the SIM in an in-service CPE
is replaced, and say whether the new SIM is on a network the customer's plan
allows. **Alert only**: nothing is changed on the CPE.

## Verdicts

| Situation | Verdict | Result |
|---|---|---|
| SIM identity unchanged | none | nothing |
| First SIM ever read for the device | baseline | stored as the known SIM; no alert unless its network is not allowed |
| New SIM (ICCID or IMSI changed), network allowed | `notice` | lower-priority incident, banner, event; open until someone approves with a reason |
| New SIM, network not allowed by the plan | `alert` | higher-priority incident, banner, event; approval is possible (reason required) as a deliberate override |

"Allowed" is `ServicePlan.AllowsIMSI` (`internal/onboarding/types.go`): one
definition shared with onboarding's `checkSIM`. A device with **no
IN_SERVICE onboarding, no plan, or a plan with no allowed networks allows
every network**, so such a device can only get `notice`, never `alert`.

Rules for readings:
- Empty, sentinel or unreadable values are "no reading", never "SIM removed".
- **Interface 1 only.** `onboarding.SIMIdentifiers` matches any interface and
  iterates a map, so the new code filters to `Device.Cellular.Interface.1.`.
- The judge works on the incoming values **merged over the cached ones**, so
  a reply with only the ICCID is not checked against a stale IMSI.

## Precondition: which leaves identify the SIM on the FastMile

`SIMIdentifiers` reads `USIM.ICCID` / `USIM.IMSI`. The FastMile also exposes
`X_ALU-COM_CurrentUsedIMSI` (see `cmd/api/sim_handlers.go`). If the lab probe
shows the standard leaves are empty on the real CPE, detection would never
fire. So the build starts with a task that checks which identity leaves the
lab CPE reports (the probe in `docs/CPE-PROBE.md`) and adds the vendor leaf
as an identity source when needed. Network is derived from the first 5–6
digits of the IMSI, as `checkSIM` already does.

## Detection

1. **Reading.** A `simWatchSampler` in `cmd/acs`, in the pattern of
   `cpeMetricsSampler`, queues one `GetParameterValues` at a check-in when the
   device is due. Created-by `system:sim-watch`. It reads **only the SIM
   identity paths** of interface 1: `MissingReadPaths` fails the whole job
   (PARTIAL_READ) if any requested path is absent, so extra network leaves
   must not be added. It copies the guards of the onboarding caller
   (`cmd/acs/onboarding.go`: skip when no discovered SIM paths exist rather
   than using the TR-181 defaults) and of the signal sampler (skip during
   active discovery). Default interval 1 hour (`ACS_SIM_WATCH_INTERVAL`, `0`
   disables, an invalid value logs a warning and uses the default); same
   slack rule as the signal sampler.
   Due-ness is decided from `device_sim_watch.last_checked_at` (set when a
   reply is judged) as well as the in-process throttle, so a restart does not
   re-read every device at once or skip a read for a whole interval.
2. **Judging.** `cacheParameterValues` already reads the old cache, upserts,
   then publishes. A new `detectSIMChange` runs in that same function, before
   the upsert, and **does not inherit the `tmfEvents == nil` early return** of
   `detectCellularStateChanges`. Any reply that carries the identity fields
   is judged, whoever asked for it (a probe, onboarding's SIM read, the
   sampler). The `system:sim-watch` reply needs no special routing: unknown
   creators already fall through to `cacheParameterValues` (`dispatch.go`).
3. **Plan lookup.** A new onboarding repository method
   `PlanForDevice(deviceID)` returns the plan of the latest IN_SERVICE
   onboarding bound to the device. (`OpenForDevice` excludes IN_SERVICE
   rows, and `GetPlan` needs an id, so neither fits.)
4. **Durability order.** The verdict is **persisted before the cache is
   overwritten**, because after the upsert the old SIM is no longer
   available to compare. If persisting fails, the SIM identity paths are
   left out of that upsert (the next read re-judges) and an error is logged
   (device id, no identifiers). The write is an idempotent upsert, so a retry
   after an upsert failure repeats harmlessly. An identical repeat of an open
   verdict changes nothing.

## Storage

Migration `0106_device_sim_watch.sql` (0105 is the latest), one row per
device:

- `device_id` (PK, FK, cascade), `state` (`ok` | `notice` | `alert`),
  `network` (PLMN; not subscriber-identifying), `iccid_hint`, `imsi_hint`
  (masked with `parameters.MaskSubscriberIdentifier`), `previous_iccid_hint`,
  `previous_imsi_hint`, `detected_at`, `last_checked_at`, `approved_at`,
  `approved_by`, `approval_reason`.
- No `incident_id`: the incident is keyed by condition key (below).
- Full IMSI/ICCID/MSISDN are **never** written here, to logs, events,
  incidents or audit rows. The parameter cache and the audited superadmin
  reveal remain the only places full values live.

## Alerting

- **Incident.** A new `ingestSIMWatch` step in the BSS adapter incident
  worker, following `ingestOfflineDevices`: it needs the account mapping
  (`account_device_mappings`) for the tenant id, so **a device with no
  customer mapping gets the banner and the event but no incident**; it goes
  through `alerting.Resolve`/`Classify` with two new fault codes, so
  priorities are the policy's P-levels (not "low/high"; the default policy
  maps notice to a lower and alert to a higher level) and a device with no
  matching policy opens nothing; it calls `notifyIncident`. Condition key
  `sim-change:<device>`. When the row returns to `ok` the step resolves the
  open incident with `SetState(id, "recovered", actor)`. A notice that
  becomes an alert updates the open incident's priority and summary (a small
  new repository method). Because there is no stored incident id, a second
  change after an approval simply opens a new incident under the same key.
- **Event.** `SimChanged`, published by the ACS through the TMF publisher
  with a new function modelled on `PublishCellularStateChanged`
  (`internal/tmf/telemetry`), carrying masked hints, network, verdict, time.
  It is a **contract addition** like 2.1.0, not a silent minor change: the
  event enum in `openapi-bssadapter.yaml`, `bss-integration-guide.md`,
  `docs/integration/09-events.md`, `contract/README.md`,
  `scripts/contract/record_fixtures.py` and the webhook-bodies fixture are
  updated together, and the work does **not** move any contract tag (only the
  user cuts one). NaaS is pinned to 1.2.1 and is told when a release is cut.
- **Banner and approval.** The state rides on the existing
  `/devices/{id}/sim` view (`simView` in `cmd/api/sim_handlers.go`), which
  `SimPanel` already loads; the banner and the **Approve this SIM** button
  live in `SimPanel`. `POST /api/v1/devices/{id}/sim-watch/approve` with a
  required `reason` (minimum length enforced server-side) uses
  `routePerm(…, operators.PermDevicesWrite, …)` and `getScopedDevice` for
  tenancy, records the audit entry through `h.auditor.Record` and **fails
  closed** if the audit write fails (as `revealSIM` does), sets `approved_*`
  and `state='ok'`, and never touches the CPE. The route is added to
  `openapi.yaml` and `generated.ts` (the drift test requires it). Rate
  limiting is the existing global per-client limiter.

## Security and privacy (POPIA/RICA)

- Identifiers are compared inside the ACS; only masked hints and a verdict
  leave it. Incident `details` carry no raw values. No log line carries an
  identifier (device id and verdict only).
- Database roles (the dbroles manifest is generated by
  `ACS_UPDATE_DB_GRANTS=1`; its keys are `acs`, `api`, `bssadapter`). The
  extractor grants a service every privilege for the SQL in any package it
  imports, so the judge stays a pure package (`internal/simwatch`, no SQL)
  and the SQL goes into three small packages: an ACS writer, an API
  approve/read, and a reader/updater used by the adapter. The ACS must not
  import any incident SQL. The manifest is regenerated and its test must
  pass; the exact resulting grants are reviewed in the PR.

## Testing

- Judge (pure): every verdict row, empty/sentinel inputs, merged
  incoming-over-cached values, interface-1 filter, allowed/not allowed, no
  plan, repeat of an open verdict, SIM changing back.
- ACS: due check (restart, slack, disabled, invalid setting, discovery
  skip, no-discovered-paths skip); the judge runs on non-sampler replies;
  verdict persisted before upsert, and a failed persist leaves the cache
  unchanged for the SIM paths; no dependency on `tmfEvents`.
- Store and dbroles tests against the throwaway Postgres.
- Adapter: incident opened once, none without a mapping, priority per
  verdict, upgraded notice→alert, resolved on approval, reopened on a later
  change.
- API: approve requires reason, permission and scope; audit failure fails the
  request; openapi drift test.
- Frontend: banner and approval dialog unit tests; one browser e2e check.

## Out of scope (later phases)

Any action on the CPE; a SIM removed while the CPE is offline; eSIM profile
changes behind an unchanged ICCID; interfaces other than 1; **USP devices**
(`cmd/uspc` has its own cellular path); roaming detection; a history of past
SIMs (only the previous hint is kept).
