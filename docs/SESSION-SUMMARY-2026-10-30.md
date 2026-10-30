# ACS work session 2026-10-30: requests, decisions, findings and delivered work

This summary is for another Claude agent working on this repository. It records what the user asked for in one long session on 2026-10-30, what was decided, what was learnt from real Nokia devices, and what was built. **The actionable next steps live in [`docs/HANDOFF-PLAN-2026-10-30.md`](HANDOFF-PLAN-2026-10-30.md)**; this file is the context behind them.

## 0. Coordination: read before changing code

- **All of this work is on branch `fix/cwmp-cpe-initiated-rpcs`, PR #79 → `main`.** It is not merged to `main`.
  - Size: 41 first-parent commits, 185 files, about +19k/−0.7k lines.
  - If you are working on `main` or another branch, expect conflicts until PR #79 lands. Merge or rebase onto it rather than re-implementing anything listed here.
- **Migration numbers 0079–0085 are taken by this branch**, so the next free one is **0086**:

  | Migration | Purpose |
  |---|---|
  | 0079 | Re-enrolment window |
  | 0080 | REBOOTING state |
  | 0081 | XMPP |
  | 0082 | Diagnostics awaiting-complete |
  | 0083 | Mesh gateway link |
  | 0084 | Wi-Fi insights |
  | 0085 | Redact cached secrets |

  If your branch already added migrations in that range, renumber yours.
- **Files changed heavily here, so conflicts are likely:**
  - `backend/cmd/acs/{session,dispatch,discovery,main}.go`
  - `backend/cmd/api/{main,routes,device_handlers,connreq_worker,diagnostics_handlers,wifi_handlers}.go`
  - `backend/openapi.yaml` and `frontend/src/api/generated.ts`. For these two, resolve conflicts in the yaml and then regenerate with `npm run generate:api`.
  - `frontend/src/screens/{DeviceDetail,FleetControl}.tsx`
  - `docs/COMPATIBILITY.md`
- **State at pause:**
  - Local backend and frontend checks pass.
  - **GitHub Actions CI does not run at all.** Every job is refused with *"recent account payments have failed or your spending limit needs to be increased"* (code review finding D1, since about 2026-09-23). So **the DB-backed tests for the new SQL have never run anywhere.** Run them against a throwaway Postgres (see §5) before merging or deploying.
  - Nothing after commit `ac658eb` is deployed to the EC2 lab.

## 1. What the user asked for (in order) and the outcome

| # | Request | Outcome |
|---|---|---|
| 1 | Clone the ACS repo, find the most recently worked-on branch, review the code | Done. The review was kept outside the repo as an untracked `CODE_REVIEW_2026-10-30.md`. |
| 2 | Field-test a **Nokia FastMile 5G Gateway 2** (and the **Nokia WiFi Beacon 3.1** behind it) against the ACS on **AWS EC2 Ubuntu**; provide a ready startup script; make sure CPE and server settings match | Done. Both devices bootstrapped, graduated to per-device Digest credentials and pass read-only qualification. EC2 `13.247.225.68` runs via `scripts/start.sh`. |
| 3 | Fix the issues found | Done; see §4 "CWMP / discovery / jobs". |
| 4 | Bump backend dependency versions | Done: Go 1.26.10 plus dependency bumps (`886313b`). |
| 5 | Run qualification tests; then the remaining field items (reversible write, reboot, STUN/Connection Request, TLS, `nokia.xml`/COMPATIBILITY) | Read-only PASS on both devices. Reversible write: the write is verified; **the restore is unconfirmed**. Reboot measured. STUN is impossible on this firmware. **TLS is not yet tested.** |
| 6 | Export the discovered parameter trees into a **per-firmware master library** (also copied to `C:\applications\acs-discoveries`) | Done: `scripts/export-discovery-library.py` writes to `ci/compatibility/discoveries/<vendor>/<model>/<firmware>/`. |
| 7 | Audit all console pages, backend features and Grafana against real device behaviour | Done. The gaps are listed in `docs/NOKIA-FIELD-GAP-REPORT-2026-10-30.md`, and the P0 items are fixed. |
| 8 | Design a **Wi-Fi management page** (SSIDs, clients, access methods) like TP-Link Omada / Grandstream controllers | Spec written: `docs/WIFI-MANAGEMENT-SPEC.md`. **Page not built.** |
| 9 | Could the ACS host its own "cloud controller" with RADIUS/AAA, and can the Nokia devices point to it? | Assessed (spec §8–§9a). WPA-Enterprise modes are supported, but there is **no TR-069 parameter for the RADIUS shared secret** (web UI only). No QoS shaper and no captive portal on this firmware. |
| 10 | Make the device page's **parameter cache** easier to navigate | Done: `ParameterBrowser` component with a tree view, search and filters. |
| 11 | Fix the P0 gaps; then per-instance Wi-Fi | Done. The Wi-Fi resolver renames SSID.1 and SSID.5 (one per band), never the guest or disabled SSIDs. |
| 12 | Build **XMPP Connection Request** using a parallel agent | Done (merged). **Not field-tested.** |
| 13 | Work on the other open items in parallel; **can we get SIM/IMSI information?** | Three parallel agents (Wi-Fi follow-ups, diagnostics, mesh link) finished and are merged. SIM: see the decision in §2. |
| 14 | Add GDMS-style Wi-Fi information (user shared Grandstream GDMS dashboard screenshots: bandwidth, client counts, new/returning clients, average time, client vendor/OS, top clients/devices/SSIDs, guest sessions) | Feasibility mapped (spec §3.3a) and **backend built**. **Console page not built.** |
| 15 | How do we protect the CPE ↔ ACS link? Port 7547 is open: what would a scanner see, and can it be exploited? | Answered (see §3). Hardening plan written. The first fix, secrets in the parameter cache, is done. |
| 16 | Wi-Fi insights backend (chosen over starting hardening first) | Done. |
| 17 | Do backend hardening **once the background tasks are done**. TLS: **IP address for now, a DNS name later** | Started (secret redaction). The rest is planned in the handoff plan §3. |
| 18 | Pause; write the plan down and push it to GitHub for the other local Claude agent; then this summary | `docs/HANDOFF-PLAN-2026-10-30.md` and this file. |

## 2. Decisions the user made (treat as requirements)

1. **Role of the ACS vs the NaaS platform.** The ACS is mandatory for managing devices and adds value on top. The user's ISP **NaaS platform** integrates with it: it may issue commands or workflows to enforce rules or scenarios, or to assist with troubleshooting. The ACS must also be able to **operate standalone**.
2. **Controller authority.** The ACS is the controller of record for CPE configuration. The Nokia cloud controller (NWCC) URLs exist on the device but are not used; they stay on the dangerous-parameter denylist. See `docs/WIFI-MANAGEMENT-SPEC.md` §6/§8.
3. **SIM / subscriber identity.**
   - The user's organisation is the **MNO**. Full IMSI/SIM data is required for **RICA and the lawful-interception plan**, but **only for the super-admin group**.
   - Implemented as follows:
     - masked for all roles by default;
     - full values only via an audited, reason-required reveal by a **human superadmin**, with the BSS service token explicitly refused;
     - SIM PIN never returned to anyone.
   - Subscriber identifiers count as personal information under POPIA. Keep them out of logs, exports and the discovery library.
4. **TLS:** use the EC2 public **IP** now; a **DNS name will come later**. The design must make the switch to a public-CA certificate a configuration change only.
5. **Hardening timing:** start after the background agents finished. They have; hardening is now the next work item.
6. **Wi-Fi page:** a separate page for SSIDs, clients and access methods (spec). Client authentication via portal, forms or tokens is a later phase, belonging with NaaS.
7. **Secrets and dangerous parameters.** The Wi-Fi page and API never return passphrases. A server-side denylist covers:
   - `Tr69Disabled`
   - `RemoteWebDebug`
   - the Nokia server URL / operator lock
   - the NWCC URLs
   - `RegulatoryDomain`
   - the ManagementServer URL and credentials

## 3. Facts learnt from the real devices (2026-10-30)

### Nokia FastMile 5G Gateway 2

Identity: OUI A4FC77, ProductClass 3TG02241, firmware 1.2501.00.0798, TR-181 2.12.

**CWMP and protocol behaviour**
- The SPV `ParameterKey` must be ≤ 32 characters, otherwise the device returns fault **9003**.
- It drops CWMP sessions mid-burst at random objects. Discovery now retries an object once, then skips it.
- The vendor prefix is `X_ALU-COM_` (not `X_NOKIA_`). Most `*NumberOfEntries` counters are absent.
- LTE values come back as sentinels (−32768 / 4294967295) when the device is not on LTE.

**Reachability and Connection Request**
- It sits behind **carrier CGNAT**: the Connection Request URL is 10.10.178.14 and the public egress is 66.18.65.65. Direct Connection Request is impossible.
- There is **no STUN / Annex G**.
- It supports **XMPP Connection Request**:
  - `SupportedConnReqMethods=XMPP,HTTP` and `X_ALU_COM_XMPP_Enable=1`;
  - the `Device.XMPP.Connection.` table is writable.
- It informs every 60 s.

**Wi-Fi layout**
- 8 SSIDs, with AccessPoint.n ↔ SSID.n:
  - primary: SSID.1 (2.4 GHz) and SSID.5 (5 GHz);
  - guest: SSID.2 and SSID.6 (`X_ALU-COM_IsGuestSSID=1`);
  - disabled factory SSIDs: 3, 4, 7 and 8.
- It acts as the EasyMesh controller (`WiFi.DataElements`, where node 2 is the Beacon). Clients behind the Beacon appear only there.

**Cellular and SIM data**
- `Cellular.Interface.1.USIM.{IMSI,ICCID,MSISDN,Status,PIN,PINCheck}`
- `IMEI`/`IMEISV`
- `X_ALU-COM_CurrentUsedIMSI`
- `X_ALU-COM_ESIM.{Eid,ActiveProfileICCID,…}`
- `X_ALU-COM_LifeTimeDataCounter`
- No DHCP option fingerprint (DHCP pools expose Chaddr/IP/lease only), so client OS can only be guessed from hostnames.

**Security-relevant settings (read the values before writing any)**
- `IP.Interface.{i}.X_ALU-COM_WanAccessCfg.{HttpsDisabled,SshDisabled,Tr69Disabled}`
- `ManagementServer.X_ALU-COM_ACSIPv4List`
- web-login lockout settings
- `UserInterface.RemoteAccess.*`
- There is **no CA-certificate parameter**, so it is unknown whether the device validates the ACS TLS certificate.
- The USP agent cannot be configured over TR-069; it has to be set up in the web UI.

### Nokia WiFi Beacon 3.1

- Manufacturer string is **"ALCL"** (OUI F89B6E); TR-181 2.13; 101 parameters.
- **No `Device.WiFi.`, no Cellular, no IP diagnostics.**
- It has TR-111 `GatewayInfo`, used for the mesh link.
- Reboot recovery took **8–11 min** (mesh backhaul) and once needed manual recovery.

### Port 7547 exposure (answer given to the user)

- **On the ACS:** a scanner sees only an HTTP 401 Digest challenge; there is per-IP rate limiting. The real risks are:
  - plain-HTTP interception of SPV values and Digest exchanges;
  - fake-CPE enrolment if a bootstrap credential leaks.
- **On the CPE's own 7547:** unreachable from the internet because of CGNAT, but possibly reachable from other subscribers on the same APN.
- Known 7547 exploits were vendor-stack specific: RomPager "Misfortune Cookie" CVE-2014-9222, and the 2016 TR-064 injection used by Mirai. No published flaw for this firmware was checked; check Nokia PSIRT.
- The serious threat is redirecting the CPE's ACS URL to a rogue ACS. Defences:
  - TLS;
  - per-device Connection Request credentials;
  - the operator lock;
  - no WAN-side web UI;
  - an APN firewall.

## 4. Delivered work by area (all on PR #79)

### CWMP / bootstrap
- Answers CPE-initiated GetRPCMethods; unsupported methods get fault 8000.
- `ParameterKey` is clamped to 32 characters.
- Bootstrap auth failures are logged.
- An `acs_cwmp_auth_total` metric (challenge / ok / stale / bad_credentials / bootstrap_*).

### Discovery
- Skips or retries objects the CPE cannot serve, and recovers abandoned walks.
- Collapses runtime tables (clients, scans, alarms) to one instance, with a 32-instance cap.
- Runs one active walk per device.

### Jobs
- Recovers jobs from dropped sessions:
  - GET and SET are requeued;
  - a reboot counts as succeeded when the next `M Reboot` carries its command key;
  - transfers wait for TransferComplete;
  - other jobs fail as outcome-unknown.
- Late TransferComplete is reconciled.

### Connection Request / NAT
- Devices behind NAT are classified on Inform. `PERIODIC_FALLBACK_ONLY` devices skip the dial, recorded as `BEHIND_NAT`.
- The periodic inform interval is only ever tightened.
- **XMPP Connection Request:** client, provisioning state machine, Prosody (compose profile `xmpp`), API, runbook `docs/XMPP-CONNECTION-REQUEST.md`.

### Devices
- Operator **re-enrolment window** for factory-reset devices (`POST /devices/{id}/re-enroll`).
- **REBOOTING** state with a measured reboot duration.
- **Mesh gateway link:**
  - satellite ↔ gateway via GatewayInfo;
  - `GET /devices/{id}/satellites`;
  - TMF639 relationships;
  - console mesh panel, satellite warnings, ALCL treated as Nokia.

### Cellular
- Nokia field aliases, sentinel handling, and cellular-scoped state events.

### Wi-Fi
- **Per-instance resolver** `GET /devices/{id}/wifi/networks`. Roles are primary / guest / unbound / disabled; passphrases are never returned.
- These now use primary networks only:
  - BSS `MODIFY_WIFI`;
  - the console Set SSID;
  - bulk `SET_WIFI`;
  - the TMF640 SSID;
  - the report export.
- **Wi-Fi insights** (backend only): sampled every 5 min; `GET /devices/{id}/wifi/insights`.

### Diagnostics
- Results are polled in a later session or on `8 DIAGNOSTICS COMPLETE`.
- Inputs are typed.
- Gated on discovery: the Beacon gets 409, and the FastMile's TraceRoute omits `Timeout`/`DSCP`.
- Scheduled-ping defaults.

### SIM / privacy
- Subscriber identifiers are masked everywhere; superadmin-only audited reveal (`GET /sim`, `POST /sim/reveal`).
- **Secrets are never stored** in the parameter cache or its history (migration 0085 scrubs existing rows).

### Monitoring
- Grafana read-only grants corrected (`scripts/grafana-db-role.sh`).
- Alert rules: CWMP auth failures and dead-lettered jobs.
- Retention key fix.
- Personal-data tables are never granted to `grafana_ro`.

### Console
- Parameter browser.
- REBOOTING badges.
- Mesh panel.
- Fleet Control `SET_WIFI` with per-device outcomes.
- Diagnostics gating.

### Compatibility and docs
- Catalogs `nokia.xml` (model 3TG02241) and `nokia-beacon.xml`.
- `ci/compatibility/*.json` scenarios and `ci/compatibility/discoveries/`.
- `docs/COMPATIBILITY.md`, `docs/WIFI-MANAGEMENT-SPEC.md`, `docs/NOKIA-FIELD-GAP-REPORT-2026-10-30.md`, `docs/XMPP-CONNECTION-REQUEST.md`.

## 5. EC2 lab: operational lessons from today

- **Run `start.sh` as `ubuntu`, never with `sudo`.** `sudo` created `/root/.acs-secrets.env` and overwrote `infra/.env`, and Postgres auth then failed. The working command:
  ```bash
  sg docker -c "PATH=\$PATH:/usr/local/go/bin ./scripts/start.sh"
  ```
  The explicit `PATH` avoids "go: command not found".
- **The repo is private.** The `curl raw.githubusercontent.com … | bash` quickstart returns 404. Clone with a PAT or deploy key, then run `./scripts/quickstart.sh`.
- **`git pull` on EC2 conflicted earlier** on a file-mode change and on locally exported discoveries. Fixed with `git config core.fileMode false` and `git checkout -- scripts/grafana-db-role.sh`. The EC2 discovery exports were moved to `~/discoveries-ec2-backup-20261030`.
- **Long-running jobs on EC2:**
  - Don't background a whole `&&` chain: it stopped on a TTY read. Background only the long process, and check for stopped jobs with `jobs`.
  - **Run one qualification or write test at a time.** Duplicate concurrent runs happened and interfere with each other.
- **Logs:** `~/acs-logs/` holds `acs.log`, `api.log` and `write-test.log`. In log lines the device id comes *after* the message, so grep for the id, not a prefix.
- **SSH key:** `ACS_Key.ppk` is in PuTTY format, so use `pscp`/`plink`, not `scp`. The file was readable by another local group; tighten it with `icacls` and keep it outside shared folders.
- **Device behaviour seen on EC2:**
  - The FastMile's discovery takes many sessions because of mid-burst drops. That is expected; discovery resumes.
  - A Beacon reboot through the ACS took 8–11 minutes, during which its LED was red and it showed "Backhaul Disconnected". That is normal recovery, not a bug.
- **Never** point `ACS_TEST_POSTGRES_DSN` at the EC2 database: the DB-backed tests run `DROP SCHEMA public CASCADE`. Use a throwaway container, for example:
  ```bash
  docker run -d --name acs-testdb -e POSTGRES_USER=acs -e POSTGRES_PASSWORD=acs -e POSTGRES_DB=acs -p 55432:5432 postgres:18-alpine
  ```
  Then, from `backend/`:
  ```bash
  ACS_TEST_POSTGRES_DSN='postgres://acs:acs@localhost:55432/acs?sslmode=disable' go test -p 1 -count=1 ./...
  ```

## 6. Where to find more

- `docs/HANDOFF-PLAN-2026-10-30.md`: the next steps, in order.
- `docs/field-notes/2026-10-30/`:
  - `CODE_REVIEW.md`: the morning review of `main`, with an end-of-day status note.
  - `NOKIA-FASTMILE-EC2-RUNBOOK.md`: EC2 setup and the CPE TR-069 settings used.
  - `NOKIA-FASTMILE-FIELD-RESULT.md`: qualification evidence.
- The full conversation transcript was exported for the user to share. It is not in the repo, because it contains raw logs and device data. Search it for detail this summary leaves out.

## 7. Open items

These are listed with order and detail in `docs/HANDOFF-PLAN-2026-10-30.md`:

1. Run the DB-backed tests locally (CI cannot run; see §0 and §5) and fix any failures.
2. Deploy to EC2 and run the field checks: secrets, SIM, insights, mesh, diagnostics, bulk Wi-Fi, XMPP, the write-test restore.
3. **Hardening:**
   - security group and CIDR allowlist;
   - IP certificate plus a second TLS listener (7548) for a safe migration;
   - FastMile over HTTPS, set up through its web UI;
   - production profile;
   - CPE-side WAN access and ACS IP allowlist;
   - masking secrets in scheduled-job payloads and stored job payloads.
4. **Console:**
   - Wi-Fi insights page;
   - SIM panel with superadmin reveal;
   - XMPP provision button;
   - the full Wi-Fi management page.
5. Policy and template typing (P1 #13).
6. Remaining field tests: TLS, USP channel, RADIUS Enterprise SSID.
