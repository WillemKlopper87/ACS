# CPE telemetry: signal trend, link-change times, CPE-reported location

Status: design for approval; the operator's two decisions (staged build, probe
may read coordinates) are recorded below. Phases 1 and 2 of a four-phase plan. Phases 3 and 4
(SIM watch, response ladder) are outside this document and get their own design.

## Intent and why

The operator runs fixed-wireless CPEs (Nokia FastMile first) over a mobile
network and wants three things the console cannot show today:

1. **How the network performs over time.** Signal quality as a trend line, not
   only the last reading and a change log.
2. **When the link went off and on.** The Availability panel (PR 127) shows
   silence between check-ins and, at best, that the CPE restarted. It cannot show
   a drop that recovered between two check-ins, and it bounds an outage's end only
   by the next check-in.
3. **Where a CPE is,** if the CPE itself can say, without anyone typing
   coordinates.

These are also the data foundation for phases 3 and 4: the same sampling carries
the SIM and network state a later phase will compare.

## Goals

- A signal trend (RSRP, RSRQ, SNR, RSSI, band, access technology) per device,
  charted over 24 hours, 7 days and 30 days.
- Link-change times with exact timestamps where the CPE reports them, shown in
  the Availability panel, including short flaps with no silent gap.
- The latest CPE-reported location (source, age, coordinates) beside the manual
  one, never overwriting it.
- A read-only probe first, so what is built rests on what the FastMile really
  returns.

## Non-goals

- SIM change detection, alerts and any response (phases 3 and 4).
- The walled garden. The FastMile has no parameter for it (its discovered
  parameter names contain nothing for walled garden, captive portal, parental
  control or URL filtering); it belongs in the mobile core.
- Location history or movement tracking. Only the latest fix is kept.
- Looking positions up from cell IDs through an outside service.
- CPE-side configuration (`InformParameter`, notification attributes).
- USP devices. They open no CWMP sessions; the sampler is CWMP only.

## Anti-goals

- Anything that writes to the CPE in these phases. Everything here is a read.
- Flooding `parameter_history` or the check-in path. A signal value changes on
  every reading, so it must not go through the cache-and-history write.
- Subscriber identifiers (IMSI, ICCID, MSISDN, IMEI) in the new tables or logs.
- Making location more visible than the manual coordinates already are.

## What is known about the FastMile

From the device's discovered parameter names (lab CPE, 2026-11-25). Names exist;
**their values have not been read**, which is what phase 1 settles.

- **Link-change timers (read-only):** `Device.Cellular.Interface.1.LastChange`,
  `Device.IP.Interface.{1,2}.LastChange`, `Device.Ethernet.Interface.1.LastChange`,
  `Device.Ethernet.Link.{1,2}.LastChange`, `Device.DeviceInfo.UpTime`. TR-181
  defines `LastChange` as seconds since the interface entered its current state.
- **Location (read-only):** `Device.DeviceInfo.Location.1.{Source, AcquiredTime,
  ExternalSource, ExternalProtocol}` and Nokia `X_ALU-COM_{Latitude, Longitude,
  Altitude, TimeStamp}`. Whether it holds a real fix, and what `Source` says, is
  unknown.
- **Signal:** the Nokia `X_ALU-COM_Attached5GCellStat` and `AttachedLteCellStat`
  objects (RSRP, RSRQ, SNR, RSSI, band, cell ID), already read by "Refresh
  cellular diagnostics" and resolved vendor-aware in
  `internal/devices/adapters/cellular.go`.
- **Reachability:** the lab CPE is behind NAT (console: "at next check-in"), XMPP
  is not enabled, so the ACS can only read at check-ins.

## Approaches

**A. Configure the CPE to attach values to every Inform** through
`Device.ManagementServer.InformParameter` (11 writable slots exist). No extra
RPC, but it writes CPE configuration, and nothing is known about how many slots
are free, whether the values survive a factory reset, or how `EventList` and
Nokia's `X_ALU-COM_ParameterGroup` behave. Resolution equals the check-in
interval.

**B. Queue a read at check-ins, as the Wi-Fi insights sampler already does**
(`cmd/acs/wifi_insights.go`). When a device's last sample is older than the
interval, nothing is pending and no discovery walk is running, queue one
`GetParameterValues`; it runs in that same session, so no connection request is
needed. One extra RPC per sample, no CPE configuration, any path can be read,
and the pattern is proven in this codebase.

**C. Change notifications** (`4 VALUE CHANGE`). Good for state changes, useless
for a trend line.

**Chosen: B.** *This reverses my earlier recommendation of A.* I had said B
needed extra sessions and a reachable CPE; that is wrong here, because a job
queued at a check-in is answered in that check-in's session. B has no CPE-side
risk and reuses a tested path. A stays an optional later optimisation if the RPC
cost matters at fleet scale; C is for phase 3.

## Sequencing

*(The operator's decision, 2026-11-25.)* Three stages, each its own pull request
and migration, so no dead tables are created ahead of the evidence:

1. **Probe** (phase 1): the read control and the FastMile write-up.
2. **Signal trend** (phase 2a): the sampler, `device_cpe_samples` with the signal
   columns, the API and the Signal panel. It needs nothing from the probe: the
   signal paths and value forms are already known from "Refresh cellular
   diagnostics".
3. **Link changes and location** (phase 2b): the `*_last_change_seconds` columns
   (added by a second migration), `device_cpe_location`, the Availability markers
   and the location row. Built only after the probe write-up exists and the
   operator has read it and said go. The operator reads the write-up before this
   stage starts; it does not follow on automatically.

## Design

### Phase 1: probe (read-only, small)

- The Parameters page gets a **"Read parameters now"** control: a box of paths
  and a Read button that calls the existing `POST /devices/{id}/parameters/get`
  (permission `devices.write`, as today), plus one preset, "FastMile telemetry
  probe", that fills the path list below. Results land in the parameter cache
  through the existing check-in path and show in the cache panel.
- Probe list: the cellular signal paths; `Device.Cellular.Interface.1.LastChange`
  and `NetworkInUse`; `Device.IP.Interface.{1,2}.LastChange`;
  `Device.Ethernet.Link.{1,2}.LastChange`; `Device.DeviceInfo.UpTime`;
  `Device.DeviceInfo.Location.1.{Source, AcquiredTime, X_ALU-COM_TimeStamp,
  X_ALU-COM_Latitude, X_ALU-COM_Longitude, X_ALU-COM_Altitude}`;
  `Device.Cellular.RoamingStatus`. The coordinates are read so their *format* is
  known; the write-up records the format (units, precision, what an empty or
  no-fix value looks like) and never the position itself. *(The operator agreed
  to this read on the lab CPE, 2026-11-25.)*
- The result is written up in `docs/` as "what the FastMile returns": the value
  forms and units, whether `LastChange` resets on a link drop (tested by pulling
  and re-seating the SIM or antenna on the lab CPE), and the `Location.Source`
  values. **Gate:** phase 2's link-change and location parts are built only as far
  as the probe supports them (see Risks).

### Phase 2: telemetry

- **Package `internal/cpemetrics`** (not `telemetry`: `internal/tmf/telemetry` and the live `/5g-telemetry` and `/telemetry-health` endpoints already exist, and are left alone), imported by `cmd/acs` (writes) and `cmd/api`
  (reads), following `internal/wifiinsights`. Kept out of `internal/devices` so the
  database-role manifest does not give unrelated services access to the new
  tables.
- **Sampler** in `cmd/acs`, called where `maybeQueueWiFiInsights` is called:
  same gates (throttle per device, last sample age, no open job by this creator,
  no active discovery). *(Author's call:)* it samples a CWMP device only if its
  discovered names contain at least one signal or `LastChange` path the cellular
  adapter resolves, matched by discovered names and not by vendor name; other
  devices get no samples, no empty chart and no extra RPC. Paths come from the discovered names through the cellular
  vendor adapter, extended with `LastChange`, `UpTime` and the `Location.1` leaves,
  so an unsupported path is never requested (an invalid name faults the whole
  read). The reply is recognised by the job's creator, as the Wi-Fi sample is, and
  is **not** written to the parameter cache or `parameter_history`.
- **Storage**, new forward-only migration:
  - `device_cpe_samples`: `device_id`, `sampled_at`, signal integers and
    band/technology text (all nullable), `uptime_seconds`, and one
    `*_last_change_seconds` column per link timer read. No subscriber identifiers.
  - `device_cpe_location`: one row per device, replaced on each sample that has a
    fix: coordinates, altitude, `source`, `acquired_at`, `fetched_at`. No history.
    *(Author's calls:)* the row is deleted with the device (foreign key, like the
    Wi-Fi samples); a sample with no fix leaves the last one in place and the
    console shows its age; a fix older than 30 days is not shown. A value that is
    empty, not a number, out of range, or exactly 0,0 counts as no fix.
- **Link changes are derived on read**, not stored: a transition time is
  `sampled_at - last_change_seconds`; when it differs between consecutive samples
  by more than 90 seconds, a change happened and that time is its moment.
  *(Author's calls, settled with the probe:)* only the WAN-side timers drive
  markers: the cellular interface and the IP interface carrying the WAN address
  (the probe says which of `IP.Interface.1` and `.2` it is); either one triggers a
  marker, labelled with which. The Ethernet timers are read for the probe but
  do not draw markers in the first version. If `UpTime` at a sample is smaller
  than the time since the previous sample, the CPE restarted and the marker says
  restart, not link change. Several changes between two samples show as one
  marker at the latest change, marked "at least one". A silent outage's end is tightened the same way when a sample shows a
  transition inside the gap. Only the end of an outage can be exact: the start is
  still bounded by the last check-in.
- **API** (read-only, same permission as device reads):
  `GET /api/v1/devices/{id}/signal-history?hours=` returning samples, derived link
  changes and the effective interval; `GET /api/v1/devices/{id}/cpe-location`
  returning the latest fix with its source and age. OpenAPI and the generated
  frontend types are updated.
- **Console:**
  - Cellular & SIM page: a **Signal** panel, an inline-SVG line chart like the
    Availability bar (no new charting dependency), 24 h / 7 d / 30 d, a metric
    selector, a table view, and the gaps between samples left as gaps.
  - Connectivity page: the Availability panel gains link-change markers and
    tightened outage ends, labelled so it is clear which times are exact.
  - Device page: a "CPE-reported location" row next to the manual one, showing
    source and age, with a **Use these coordinates** button. Nothing is copied
    automatically. A fix whose `Source` is not a satellite or network position the
    probe confirms is labelled approximate.
- **Retention and config:** `ACS_RETENTION_CPE_SAMPLES_DAYS` (default 30) in the
  retention worker; `ACS_CPE_SAMPLE_INTERVAL` (default 15 minutes, 0 turns it off).
  The effective resolution is the larger of this and the CPE's check-in interval,
  and the console says so.
- **Chart size and permission** *(author's calls)*: the API averages a long range
  into at most about 500 points; "Use these coordinates" needs `devices.write`,
  the same as saving the manual location.
- **Roles and metrics:** new tables are granted through the database-role
  manifest (the extractor test is run with `ACS_UPDATE_DB_GRANTS=1` and the diff
  reviewed); a counter records samples taken and failed.

## Constraints

- Reads happen only at check-ins; nothing is instant.
- POPIA/RICA: no subscriber identifiers in samples or logs; location is personal
  information, held as latest-only and shown only to those who can already see the
  manual coordinates.
- One in-flight RPC per session: the sampler yields to discovery and to open jobs.
- Per-service database logins stay least-privilege.
- Checks run with `scripts/ci-local.sh` (no GitHub Actions).

## Risks and what the probe decides

- `LastChange` may not reset on the events that matter, or may count in a
  different unit. If so, link changes stay as silence-based, and only the signal
  trend and location ship.
- `Location` may be empty, stale, or cell-derived. If it is empty, the location
  part is dropped and the manual coordinates remain the only source.
- The signal paths may differ between the 5G and LTE objects by attach state; the
  vendor adapter already handles both and is extended, not replaced.
- A 15-minute sampling interval adds about 96 reads per CPE per day. This is
  acceptable for the lab; the interval is configurable if a larger fleet needs
  less.

## Left to the builder

Exact column names and types; the chart's axis scaling and colours (follow the
existing console tokens and the Availability panel); the metric list shown by
default; test fixtures; the wording of labels and notes.
