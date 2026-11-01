# ACS alignment with real devices — gap report (2026-10-30)

Scope: every console page, backend feature and monitoring artefact checked against what two real Nokia devices actually expose and how they behave (field test on EC2, branch `fix/cwmp-cpe-initiated-rpcs`). Three read-only audits (console, backend, monitoring); the top findings were re-checked in code.

**Devices.** A = Nokia FastMile 5G Gateway 2 (3TG02241, SW 1.2501.00.0798, TR-181 2.12, EasyMesh controller). B = Nokia WiFi Beacon 3.1 (manufacturer `ALCL`, SW 3TN00626IJLJ03, TR-181 2.13, 101 parameters, no `Device.WiFi.`/`Cellular.`). Both behind carrier CGNAT: Connection Request never works; all work runs on the next periodic Inform (60 s). Discovered trees: `ci/compatibility/discoveries/`.

**Architecture decision recorded:** the ACS is the mandatory, authoritative device manager; NaaS integrates on top through ACS northbound APIs (`docs/WIFI-MANAGEMENT-SPEC.md` §6, §8).

## Headline

Most features *run*, but many are built on assumptions these devices break: standard-only TR-181 leaf names, instance `.1` everywhere, Connection Request reachability, and "a failed request is transient". The result is not crashes but **wrong or misleading data** — signal values that are sentinels, empty RSRP/RSRQ, spurious cellular events, every CGNAT device showing failed jobs and UNKNOWN reachability, and dashboards/alerts that are broken or will page constantly at scale.

## P0 — wrong data or misreported state (fix first)

| # | Area | Problem (verified) | Fix |
|---|---|---|---|
| 1 | Cellular / 5G values | Only `2147483647` on standard leaves is treated as "no value" (`adapters/fiveg.go:80`, `cellular.go:207`). Nokia LTE stats `-32768` / `4294967295` are published as **real, fresh readings**, including northbound and in history. | Per-type sentinel set (int16 min, int32 max, uint32 max) → `unavailable`, applied before normalisation and history. |
| 2 | Cellular field mapping | Matcher needs leaf exactly `RSRP`/`RSRQ`/`ARFCN` (`cellular.go:174-191`). Nokia uses `RSRPCurrent`, `RSRQCurrent`, `Downlink-NR-ARFCN` → **RSRP, RSRQ, channel empty** in `/cellular`. 5G-vs-LTE choice depends on sort order. | Catalog-driven mapping (`nokia.xml` already lists the paths); explicit 5G-before-LTE precedence. No SINR on this firmware. |
| 3 | Cellular state events | Leaf fallback matches any `*.Status` / `Cellid` (`cellular.go:164-169`, called on every Inform/GPV) → Wi-Fi/Ethernet/IP status changes emitted as **CellularStateChanged** TMF events, even for B (no radio). | Require `Device.Cellular.` prefix or catalog path. |
| 4 | CGNAT reachability | CR TIMEOUT → job FAILED, `connection_request_mode` stays **UNKNOWN forever** (`connreq_worker.go:176-190`). Every CR (button = primary action, bulk default, schedules) fails after 10 s and drags the job success rate → Dashboard "elevated job failure" alarm, FleetHealth red. "CGNAT tightening" sets a fixed 300 s Inform — *looser* than A's 60 s. | Classify private/100.64/10 or non-matching CR URLs as `PERIODIC_FALLBACK_ONLY` on Inform; complete CR jobs as "queued for next Inform"; exclude CR from success rate; only ever lower the inform interval; relabel the CR button. |
| 5 | Session drop recovery | A drops sessions with no fault. In-flight GET/SET wait 15 min; Download/Upload/Reboot dead-letter as `LEASE_EXPIRED_UNSAFE_RETRY`; a later TransferComplete is ignored → **a successful firmware flash counted as FAILED, can block a rollout and trigger rollback** (`jobs/lease.go`, `session.go:734`). Discovery already has this recovery (`ea11cf6`, `ca8fc8f`). | On next Inform, reclaim the previous session's in-flight job; keep Download as AWAITING_TRANSFER_COMPLETE; reconcile late TransferComplete. **Required before any firmware test.** |
| 6 | Cellular refresh path list | `CellularDiagnosticParamsFromNames` substring-matches across the whole tree (`vendor.go:47-63`) → pulls Wi-Fi band/bandwidth and runtime `STA.1.SignalStrength` instances; one vanished instance faults the whole GPV (9005); PARTIAL_READ caches nothing. Bulk REFRESH_CELLULAR on B queues an empty GPV and reports success. | Restrict to `Device.Cellular.` (or catalog list), skip runtime instances, or read `Device.Cellular.Interface.1.`; skip devices with no paths. |
| 7 | Monitoring: Alerting dashboard | `grafana_ro` has no grant on `alert_incidents` → **every panel errors** (`scripts/grafana-db-role.sh`). | Add column grants (exclude `details`); also `cwmp_sessions`, `device_credentials` (exclude `password`), `device_discovery_snapshots`. |
| 8 | Monitoring: CWMPAuthFailures alert | Counts every HTTP 4xx from cmd/acs; **every Inform starts with a normal Digest 401**, so it fires permanently at ~60 devices on a 60 s inform. | Dedicated `acs_cwmp_auth_total{result}`; alert on bad credentials vs ok, not HTTP 4xx. |

## P1 — reads/writes to paths that don't exist, or incomplete

| # | Area | Problem | Fix |
|---|---|---|---|
| 9 | Wi-Fi clients | "Refresh WiFi clients" hard-coded to `Device.WiFi.AccessPoint.` (`canonical.go:208`): B always faults 9005 (raised as a TMF alarm); A misses every client on the Beacon (only in `DataElements…STA`). No client UI exists; `/wifi-topology` is unused. | Wi-Fi page (`WIFI-MANAGEMENT-SPEC.md`); gate on capability; route satellites to their controller. |
| 10 | SSID / MODIFY_WIFI / TMF640 | Hard-coded `SSID.1` / `AccessPoint.1` (console, FleetControl, BSS template, TMF640, reports). On an EasyMesh controller that may be one band only → split SSID; B rejected or 9005. `Type:"boolean"` rendered as `xsd:string`. | Resolve all fronthaul SSIDs from discovery; refuse without discovery; emit `xsd:` types. |
| 11 | Diagnostics | Known C1 (polls itself to TIMEOUT in the triggering session) and C8 (numerics as `xsd:string`) confirmed; no capability gate (B: 9005); scheduled ping sends repetitions/timeout as 0. | Defer polling to next session / `8 DIAGNOSTICS COMPLETE`; typed values; gate on discovery like TR-143. |
| 12 | Mesh / TR-111 | B's `GatewayInfo` (points at A) unused; no gateway↔satellite relationship anywhere (UI, TMF639). | Link B→A; TMF639 `resourceRelationship`; show Beacons as nodes of the gateway. |
| 13 | Policies & templates | Policies only check parameters that arrive in an Inform → most never fire; writes always `xsd:string`; no dedupe (re-queues every minute). Templates offer `int`/`boolean` without `xsd:` → coerced to string; zero-touch template runs before discovery (one bad path fails the SPV). Model filters `ILIKE 'Nokia%'` miss B (`ALCL`). | Check against cache + notifications; dictionary/catalog types; dedupe; defer templates until discovery; vendor-family filters. |
| 14 | Firmware | Both devices blocked by the fail-closed qualification gate (correct for now); UI may preselect another model's image; backend doesn't check image vendor/model; success not confirmed against `SoftwareVersion`. | Image↔device model check; confirm version from 1 BOOT / 7 TRANSFER COMPLETE Inform; fix #5 first. |
| 15 | Web GUI proxy / CLI / VPN | ACS would dial 10.10.178.14 (CGNAT) and 192.168.1.48 (A's LAN) — unreachable. Panels render then fail. | Detect unreachable addressing and say so; only USP/VPN-based access can help. |
| 16 | Vendor identity | Frontend `isNokia` misses `ALCL`; Fleet lists "ALCL" as a vendor; API lacks ModelName/SoftwareVersion → headers "ALCL Beacon 3.1", "Nokia 3TG02241". | Vendor-family mapping (ALCL→Nokia); expose ModelName/SoftwareVersion. |
| 17 | Monitoring: other | HighJobFailureRate has no volume floor and misses cmd/api failures; JobsDeadLettered misses `dead_lettered_unsafe_retry`; RateLimit alert not service-scoped (CGNAT puts many CPEs behind one IP); `info` alerts page; ServiceDown permanent for uspc in host mode and acs in production (TLS on 7547); BSS panels misreport pending/dead-lettered orders; "Recent job faults" floods with CR rows. | See monitoring list in the audit; all are config/query changes. |
| 18 | Online threshold | Fixed 5 min / 90 min (`cmd/acs/workers.go:77`). Fine at 60 s; devices on 300–3600 s informs will flap OFFLINE. | Derive from each device's `PeriodicInformInterval`. |

## P2 — smaller

- Reports: MAC = first cached `%MACAddress%` (can be a client/BSSID); SSID blank for B.
- Dashboard temperature lists every `TemperatureSensor` leaf as a reading.
- Console `get` labelled "live" but waits for the next Inform; parameter cache unsearchable and re-rendered every 5 s (2,000+ rows on A).
- Compatibility panel overstates "Wi-Fi advertised" when only DataElements exists.
- `cmd/api` retention fails every cycle: `password_reset_tokens` idColumn should be `token`.
- No metrics for discovery outcomes, session drops/duration, CR outcomes, graduation; no radio panels (would need vendor paths + sentinels).

## What works as-is

Bootstrap → unique credential graduation; Inform/session handling; discovery (after today's fixes); read-only qualification; reversible write with read-back; catalog/model matching for both devices; online status at 60 s inform; captures, uploads, reboot/reset/schedule-inform job plumbing; tenancy, groups, audit, operators; Prometheus scrape wiring and metric names; most Postgres panels.

## Suggested order

1. **Device-truth fixes:** #1–#3, #6 (cellular), #4 (CGNAT), #5 (session-drop recovery).
2. **Monitoring that works:** #7, #8, #17, retention fix.
3. **Before NaaS integration:** #10, #12, #13, review S1/S3/S5/C9.
4. **New capability:** Wi-Fi management page (#9) per spec; diagnostics (#11); firmware (#14).
5. Remaining field tests: STUN/Annex G, reboot/reconnect, TLS profile, USP channel (manual TR-369 broker config), FreeRADIUS Enterprise SSID (secret via web UI).
