# Handoff plan: Nokia field work, hardening and open items (paused 2026-10-30)

This plan is for a fresh Claude Code session picking up this work. Work on branch `fix/cwmp-cpe-initiated-rpcs` (PR #79 → `main`) in a checkout of this repository. Read this file first, then the docs it points to.

## 1. Where things stand

### Live lab
- **ACS:** Ubuntu EC2 at `13.247.225.68`, run as user `ubuntu` with `scripts/start.sh`. Plain HTTP CWMP on 7547, lab profile.
- **Nokia FastMile 5G Gateway 2** (OUI A4FC77, ProductClass 3TG02241, firmware 1.2501.00.0798): device id `42efae72-40f8-4064-afb3-994f08a23a59`.
  - Behind carrier CGNAT: Connection Request URL 10.10.178.14, public egress 66.18.65.65.
  - Informs every 60 s.
- **Nokia WiFi Beacon 3.1** mesh satellite (Manufacturer "ALCL"): device id `91032c5d-ae14-4012-8b70-3314087c1a94`. Its Connection Request URL is on the gateway LAN.
- Both devices are ACTIVE (graduated Digest credentials), ONLINE and `PERIODIC_FALLBACK_ONLY`.
- The EC2 instance last ran commit `ac658eb`. **Everything below is pushed but not deployed.**

### Branch contents since `ac658eb` (all pushed)

| Commit | What |
|---|---|
| `906c35e` | SIM identifiers (IMSI/ICCID/MSISDN/IMEI/EID) masked for every role in `/parameters`, `/parameters/history` and `/cellular-capabilities`. `GET /devices/{id}/sim` gives the masked view. `POST /devices/{id}/sim/reveal` is for human superadmins only (the BSS service token is refused), requires a reason, and is audited as `SubscriberIdentityRevealed`. SIM PIN is never returned. |
| `2077281`, `82b02dc` | Wi-Fi insights (Grandstream GDMS-style): `internal/wifiinsights`, migration 0084, `GET /devices/{id}/wifi/insights?window=2h\|1d\|1w\|1m`. cmd/acs samples on check-ins (`ACS_WIFI_INSIGHTS_INTERVAL`, default 5m). Spec in `docs/WIFI-MANAGEMENT-SPEC.md` §3.3a. |
| merge `585c929` | Mesh: Beacon→gateway link from TR-111 GatewayInfo (migration 0083), `GET /devices/{id}/satellites`, TMF639 resourceRelationship, console mesh panel and satellite reboot warning, ALCL grouped as Nokia. |
| merge `3d6adf9` | Wi-Fi follow-ups: bulk `SET_WIFI` (Fleet Control), TMF640 SSID and report export use the per-instance resolver; report MAC comes from the Ethernet interface. |
| `65a4971` | Console: Set SSID hidden on satellites; Nokia Connection Request hint says XMPP, not STUN. |
| merge `9bf0a9e` | XMPP Connection Request (Annex K): `internal/xmpp`, `internal/xmppprov`, migration 0081, Prosody under compose profile `xmpp`, `POST /devices/{id}/xmpp/provision`. Runbook: `docs/XMPP-CONNECTION-REQUEST.md`. |
| merge `ba61978` | Diagnostics: C1 (wait for a later session or `8 DIAGNOSTICS COMPLETE`; migration 0082), C8 (typed inputs), gating by discovery (Beacon → 409), scheduled-ping defaults. |
| `84088ff` | Secrets are never stored in the parameter cache or its history. Migration 0085 scrubs existing rows. |

### Verification so far
- Local: backend `go vet` and `go test ./...` pass; frontend `tsc -b`, `vitest` (19 files, 95 tests) and lint pass.
- **No Postgres was available locally.** The DB-backed tests (all new SQL: insights, mesh link, bulk SET_WIFI, report identity, diagnostics lifecycle, XMPP worker) have only run in CI, if at all. **First task: check PR #79's CI and fix any failure in the DB-backed step.**

## 2. Deploy and field-test the merged work (EC2)

```bash
cd ~/ACS && git pull
sg docker -c "PATH=\$PATH:/usr/local/go/bin ./scripts/start.sh"   # applies migrations 0081–0085
```

Run these checks in order, using the device ids above:

1. **Secrets:**
   ```sql
   SELECT count(*) FROM parameter_history
   WHERE value <> '***REDACTED***' AND lower(name) ~ '(password|passphrase|psk)';
   ```
   Expected 0. Then `GET /parameters` must show no passphrase.
2. **SIM:**
   - `GET /api/v1/devices/<fastmile>/sim`: values masked. If `refresh_paths` is non-empty, read them with `POST /parameters/get`.
   - `POST .../sim/reveal {"reason":"lab verification of reveal flow"}` as superadmin returns full values. A manager gets 403.
   - The audit log has a `SubscriberIdentityRevealed` entry.
3. **Wi-Fi insights:**
   - Within about 10 minutes, `api.log`/`acs.log` show "Wi-Fi insights sample recorded".
   - `GET /devices/<fastmile>/wifi/insights?window=2h` returns `bandwidth`, `clients` and `top_ssids`.
   - Watch for any rise in dropped CWMP sessions. If it rises, raise `ACS_WIFI_INSIGHTS_INTERVAL`.
   - Optional: download the IEEE `oui.csv` onto the EC2 instance and set `ACS_OUI_CSV`.
4. **Mesh:**
   - After the Beacon's next `1 BOOT` or a discovery, `GET /devices/<beacon>` has `gateway_device_id` = the FastMile id.
   - `GET /devices/<fastmile>/satellites` lists the Beacon.
   - Unverified: whether the Beacon's GatewayInfo really carries the FastMile identity. If only `gateway_oui_serial` is set, compare it with the FastMile `oui_serial`.
5. **Diagnostics:**
   - `POST /devices/<fastmile>/diagnostics/ping {"host":"8.8.8.8"}`: the SPV is typed and there is no GPV in the same session.
   - The next Inform does exactly one poll and ends SUCCESS.
   - TraceRoute shows `skipped_inputs:["Timeout","DSCP"]`.
   - The Beacon gets 409.
   - Record whether the FastMile sends `8 DIAGNOSTICS COMPLETE`.
6. **Bulk SET_WIFI:**
   - Only on a test SSID name you can revert.
   - It writes SSID.1 and SSID.5 (and their passphrases if given), never guest or disabled SSIDs.
7. **XMPP:** follow `docs/XMPP-CONNECTION-REQUEST.md`:
   - Allow TCP 5222 from 66.18.65.65/32 only.
   - Set `ACS_XMPP_ENABLED=1`, then provision with the lab cert-relax flags.
   - Expect `DONE` after about 6–10 minutes.
   - Then `POST /connection-request` gives `XMPP_SENT_INFORM_RECEIVED`.
   - Confirm on the device:
     - whether `Server.1` is auto-created
     - the `ConnReqXMPPConnection` format
     - bare vs full JID in `ConnReqAllowedJabberIDs`
     - the SASL mechanism
     - the TLS version
8. **Still unconfirmed from before:** the ProvisioningCode write-test restore. `~/acs-logs/write-test.log` should show `restore exit=0`.

## 3. Hardening plan (IP-only now, DNS name later)

Do these in this order. Change one CPE setting at a time, and keep the Nokia web UI (admin access) as the recovery path: a wrong ACS URL or TLS failure cuts the device off from the ACS.

### 3a. ACS / EC2
1. **Security group:**
   - 7547/tcp: only the carrier CGNAT egress ranges (start with `66.18.65.65/32`; get the full egress list from the core team).
   - 5222/tcp: same ranges, only if XMPP is enabled.
   - 3478/udp: closed. STUN is unusable on this firmware.
   - 22: admin IP only.
   - Grafana, Prometheus, API and Postgres stay on 127.0.0.1 (already the compose default). Reach them over an SSH tunnel.
2. **ACS source-IP allowlist** `ACS_CWMP_ALLOWED_CIDRS` with the same ranges: defence in depth if the security group is widened by mistake. Check whether the lab profile enforces it when set (`cmd/acs/production_config.go`, `internal/netguard`). If it is only enforced in the production profile, make it apply in the lab profile whenever it is set.
3. **TLS with an IP certificate:**
   - Add `scripts/acs-cert.sh`, modelled on `scripts/xmpp-cert.sh`. It generates a private CA plus a server cert with SAN `IP:13.247.225.68` and sets `ACS_TLS_CERT`/`ACS_TLS_KEY` (the env names already exist in `cmd/acs/main.go`).
   - Add an **optional second listener**, e.g. `ACS_TLS_ADDR=:7548`. Today cmd/acs serves either HTTP or HTTPS on one address. The second listener keeps HTTP on 7547 alive during migration so a failed TLS test cannot strand a device.
   - Set `ACS_TLS_MIN_VERSION=1.2`.
   - **Later, with a DNS name:** replace the cert files with a public CA cert (e.g. Let's Encrypt) for that name. Only the cert and the CPE URL change; no code changes.
4. **CPE TLS test (FastMile first, not the Beacon):**
   - The data model has no CA-certificate parameter, so it is unknown whether the FastMile verifies the ACS certificate. Test both: a self-signed cert first, then (once there is a DNS name) a public-CA cert.
   - Change `ManagementServer.URL` to `https://13.247.225.68:7548/` from the Nokia web UI, not over TR-069, so recovery stays manual.
   - Record the result in `docs/COMPATIBILITY.md`. Only once the FastMile works over TLS: switch to the production profile (`ACS_DEPLOYMENT_PROFILE=production`, which requires TLS, TLS ≥1.2, allowed CIDRs and a non-wildcard `ACS_ADDR`), then close plain 7547.
   - The Beacon inherits ACS settings from the FastMile, so do it last and expect its 8–11 min recovery.
5. **Remaining secret exposure:**
   - Scheduled-job payloads are returned by `GET /schedules` (`cmd/api/schedule_handlers.go`); mask secret-named parameters there with `parameters.IsSecretParameter`.
   - The jobs table `payload` keeps SPV secrets for the 90-day retention. Consider redacting secret values in the payload once the job reaches a terminal state.
   - The XMPP password, like Connection Request credential rotation, travels in the SPV. That stays plain text on the wire until TLS (step 3) is in place.

### 3b. CPE side (FastMile 1.2501, all over TR-069 unless noted)

First read the current values with `POST /parameters/get`. Do not write until you know them.
- `Device.IP.Interface.{wan}.X_ALU-COM_WanAccessCfg.{HttpsDisabled,SshDisabled}`: confirm WAN-side web UI and SSH are disabled (=1). **Never** write `Tr69Disabled`: it is on the server-side denylist and would cut off the ACS.
- `Device.ManagementServer.X_ALU-COM_ACSIPv4List`: appears to restrict which source IPs may send a Connection Request or act as ACS. Read the value, then test setting it to the ACS public IP. This needs care: a wrong value may lock out the ACS.
- `Device.UserInterface.X_ALU-COM_Web{Normal,Super}Lockout.*`: check the lockout settings for web-UI brute force.
- `Device.UserInterface.RemoteAccess.{Port,Protocol}`: record the values.
- Rotate per-device Connection Request credentials with the existing rotation API. They are unique per device, so one device's leak doesn't expose the others.
- With the core team: APN-level client isolation, and a firewall so that only the ACS (and XMPP server) can reach the CPE on 7547 inside the carrier network.

### 3c. Note for the user
A scanner hitting EC2:7547 today sees an HTTP 401 Digest challenge and nothing else. The main risks are:
- plain-HTTP interception of SPV values and Digest exchanges
- a fake CPE enrolling with a leaked bootstrap credential, if shared bootstrap is ever enabled

Known 7547 exploits target specific CPE web stacks (RomPager "Misfortune Cookie" CVE-2014-9222; the 2016 TR-064 injection that Mirai used). No published flaw for this FastMile firmware was checked; check Nokia PSIRT.

## 4. Remaining backlog (after deploy and hardening)

1. **Console pages:**
   - Wi-Fi insights page (bandwidth chart, client counts, top SSIDs/clients, vendors, inferred OS, client list), using `GET /wifi/insights`.
   - SIM panel: masked view, plus a superadmin "Reveal" dialog with reason and reference.
   - XMPP provision button.
2. **Policy and template typing (P1 #13):**
   - `internal/policy` enforce.go: SPV types from the TR-181 dictionary, deduplicate writes, skip writes the cache shows are already in place.
   - Templates deferred until discovery; vendor-family (ALCL = Nokia) filters.
3. **Wi-Fi management page** per `docs/WIFI-MANAGEMENT-SPEC.md` (Networks, Guest, Clients, Radios, Policies).
4. **Insights follow-ups:**
   - fleet-wide top devices
   - exclude the Beacon's own MAC from the gateway's client list
   - the "new clients" count is inflated by MAC randomisation (documented)
5. Items from `docs/NOKIA-FIELD-GAP-REPORT-2026-10-30.md` not yet done: monitoring extras, deriving the online threshold from the inform interval.
6. **Field tests still open:**
   - USP agent: manual broker setup on the Nokia TR-369 page; it can't be configured over TR-069.
   - FreeRADIUS Enterprise SSID: the RADIUS secret goes in via the web UI; there is no TR-069 parameter for it.

## 5. Conventions for the next session

- **Migrations:** 0081–0085 are used, so the next one is **0086**. They are embedded from `backend/internal/store/migrations`.
- **Checks before every commit:**
  - backend: `gofmt -l .`, `go vet ./...`, `go test ./...`
  - frontend: `npx tsc -b`, `npx vitest run`, `npm run lint`
  - any OpenAPI change: `npm run generate:api` (the drift test `TestOpenAPIMatchesRegisteredRoutes`)
  - DB-backed tests need `ACS_TEST_POSTGRES_DSN`. CI runs them, and a new DB-backed package must be added to the list in `.github/workflows/ci.yml`.
- **Commit trailer:** `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.
- **Data protection (POPIA / RICA):**
  - Never log or export subscriber identifiers or secrets.
  - Full IMSI/ICCID/MSISDN/IMEI/EID only through the audited superadmin reveal.
  - Never return SIM PIN or Wi-Fi/credential secrets.
  - Client MACs and hostnames are personal data: keep them out of `grafana_ro` and out of the discovery library.
- **Dangerous CPE parameters** stay on the server-side denylist: Tr69Disabled, RemoteWebDebug, the Nokia server URL / operator lock, the NWCC URLs, RegulatoryDomain, ManagementServer URL and credentials.
- **Don't run `docker system prune`** or other destructive commands on the EC2 host. The user approves deploys and pushes to `main`.
- **Field-test write-ups** from this session (`NOKIA-FASTMILE-EC2-RUNBOOK.md`, `NOKIA-FASTMILE-FIELD-RESULT-2026-10-30.md`, `CODE_REVIEW_2026-10-30.md`) were kept outside the repository and are not part of this handoff. Their findings are folded into `docs/COMPATIBILITY.md`, `docs/NOKIA-FIELD-GAP-REPORT-2026-10-30.md` and this file.
