# Gap analysis: 2026-10-30

How far the ACS is from the product described in `docs/CONSOLE-UX-PROPOSAL-2026-10-30.md` §8: a BSS-integrated, multi-protocol device management platform for an MNO/ISP. It covers retail and business customers and Nokia and other vendors.

**Sources:**
- the open items recorded in the project's own documents (code review, Nokia field gap report, compatibility matrix, Wi-Fi spec, handoff plan, operations runbook, XMPP runbook, TMF status, the August–September audits);
- a check of the code on this date;
- the market desk research in the proposal §7.

References are `file:line` at the time of writing.

> **Correction: there is no dangerous-parameter denylist in the code.** `HANDOFF-PLAN-2026-10-30.md`, `SESSION-SUMMARY-2026-10-30.md` and the console proposal describe a server-side denylist (`Tr69Disabled`, `RemoteWebDebug`, the Nokia server URL and operator lock, the NWCC URLs, `RegulatoryDomain`, ManagementServer URL and credentials). The code has none. A grep of `backend/` and `frontend/src` finds no such list. The only write check, `validateDiscoveredWritableParameters` (`backend/cmd/api/device_handlers.go:520`), accepts any discovered, writable parameter. On the FastMile that includes `ManagementServer.URL`, `X_ALU-COM_WanAccessCfg.Tr69Disabled` and the operator-lock fields. One wrong template, policy, bulk write or API call can cut a CPE off from the ACS. This is the top item below.
>
> **Fixed the same day.** `internal/parameters/protected.go` now lists the protected parameters, taken from the field FastMile and Beacon trees and generalised to TR-181 and TR-098. Job creation refuses any write to them on every path: API, console, bulk, templates, policies, schedules and BSS orders. The API answers 403 with the reason, and templates, policies and schedules that contain one cannot be saved. Only credential rotation and XMPP provisioning are exempted, through `jobs.AllowProtectedWrites`; bootstrap graduation writes through its own path. Tests: `internal/parameters/protected_test.go`, `internal/jobs/protected_write_test.go`, `cmd/api/protected_writes_integration_test.go`.

## 1. Scorecard

| Area | State | Biggest gaps |
|---|---|---|
| **CWMP core** (sessions, auth, RPCs) | Strong | 17 RPC job types; per-device Digest with graduation; XMPP; dropped-session recovery. Missing: TR-471 / UDP echo, `ChangeDUState`, transfer-queue RPCs, CWMP 1.4+ negotiation. Plus correctness items C2, C3, C5, C6, C11. |
| **USP (TR-369)** | Partial | Controller handles Get/Set/Add/Delete/Operate/Notify over WebSocket and MQTT. Never qualified on a physical USP CPE; long Operate loses its lease (C3). |
| **Safety of writes** | Partial | Protected-parameter guard done (above). Policies and templates write `xsd:string` (P1 #13). No firmware image↔model check (P1 #14). |
| **Security** | Partial | Masking, redaction (0085/0086), audited SIM reveal and TLS listener are done. Open: S2–S10 from the code review; no operator SSO/MFA; plaintext CWMP on the lab host; credential rotation over SPV. |
| **Vendor coverage** | Weak | **No model is "supported"** under the project's own rule (`COMPATIBILITY.md:126`). Nokia FastMile and Beacon are partial (read-only passed). Huawei, Teltonika and Zyxel are not yet tested on real devices. |
| **Wi-Fi** | Partial | Per-instance resolver, bulk Set Wi-Fi and insights (backend + panel) are done. Missing: the management page; event/campaign SSIDs; the hard-coded client refresh (P1 #9). |
| **Cellular / SIM** | Partial | SIM masking and reveal, cellular telemetry, and (through onboarding) APN/DNN profiles, the allowed-network check and device–SIM pairing at install are done. Missing: SIM-swap alarm after IN_SERVICE. |
| **Onboarding** | Partial | Slice 1 done ([ONBOARDING.md](ONBOARDING.md)): sites, service plans, onboarding states driven by Informs, SIM and network check, APN/DNN profile, Unclaimed list and claim, API. Console screens and BSS pre-provisioning (TMF641 add / mappings for unseen CPEs, PENDING_ACTIVE → ACTIVE, onboarding events) done too. Missing: strict mode, import columns (and the `label` bug). |
| **BSS / NaaS** | Partial | TMF638–688, outbox and webhooks are strong compared with the market. But orders only do MODIFY_WIFI / SUSPEND / ACTIVATE; there is no pre-provisioning; S3 and S5 (cross-tenant mapping, legacy scopes) and C9 (webhook double-send) are open; there is no partner sandbox. |
| **Business estates / campaigns** | Missing | Groups are static lists; no estates; no scheduled Wi-Fi campaigns. |
| **Troubleshooting** | Partial | Captures, onboarding checks and diagnostics exist and are being grouped (PR #80). Missing: guided flows, TR-471, live job progress, a fleet reachability view. |
| **Reporting** | Weak | One Excel device report; Grafana dashboards. No scheduled, estate or SLA reports; no usage export to the BSS. |
| **Console** | Improving | Device workspace and navigation in PR #80. Open: tabs for multi-job screens, merged dashboards, onboarding UI, Wi-Fi page, campaign UI. |
| **Operations** | **Weak** | No CI since ~2026-09-23 (billing). Lab-only compose; HA and scale not proven; no restore drill; monitoring noise (P1 #17); nothing after `ac658eb` deployed until today. |

## 2. Critical gaps: fix before more field work or any customer device

| # | Gap | Why | Size |
|---|---|---|---|
| 1 | ~~**Dangerous-parameter denylist**~~ **done** | Any write path can repoint or disable the ACS link, change the operator lock, or enable remote debug. It must be enforced server-side for every write path: API, templates, policies, bulk, BSS orders, the console's `set`. | Small: one shared check plus tests |
| 2 | **CI is dead** (D1) | No automated gate on 80+ commits today. DB suites have run once, locally. | Billing fix, or a self-hosted runner |
| 3 | **Lab CWMP in plaintext; allowlist not applied on EC2** | SPV values (Wi-Fi and APN passwords) and Digest exchanges can be intercepted. Security group and `ACS_CWMP_ALLOWED_CIDRS` are not set on the host. | Deploy steps exist (handoff §3a) |
| 4 | **S2 bootstrap trust-on-first-use** | A leaked bootstrap secret enrols any not-yet-established serial. The onboarding "strict mode" closes this. | Medium |
| 5 | **S3 / S5 / S6 tenant isolation** | S3 is closed for customer-bound BSS clients (I1, [02-integration-model.md §8.1](integration/02-integration-model.md)): they act only on their own accounts and cannot map another customer's device; `ACS_BSS_REQUIRE_CUSTOMER_BOUND_CLIENTS=true` makes them the only kind accepted. Still open: global and account-listed clients can map any device (S3 without the flag); legacy `/bss/v1` ignores OAuth scopes for clients that are not customer-bound (S5; bound clients need the TMF scope of each call); S6 is closed: a scoped manager can assign a device only to a customer in their scope, and no one can move a device away from the customer that owns its BSS account while it is mapped. | Medium |
| 6 | **S7 / S8 / S9 / S4** | Web-GUI proxy has no CSP and forwards the ticket; an mTLS certificate with an empty CN skips device binding; production has no outbound allowlist (SSRF); the Digest username parser mismatch. | Small to medium each |
| 7 | **Operator SSO / MFA** | Operator login is username and password with an 8 h JWT (`cmd/api/auth_handlers.go:32`). There is no SSO (Azure AD / Keycloak) or MFA. MNO security reviews normally require both. | Medium |

## 3. High: correctness and safety at scale

| Gap | Source |
|---|---|
| C2: Download/Upload `Status=0` waits 24 h, then FAILED; skews rollouts | `CODE_REVIEW.md:70` |
| C3: USP Operate loses its lease after 15 min (no `ExtendLease` caller) | `CODE_REVIEW.md:71` |
| C5, C6, C11: reaper race; stale USP replies; sessions never expire | `CODE_REVIEW.md:73-79` |
| C7: device paging without tiebreak (TMF639 skips and duplicates) | `CODE_REVIEW.md:75` |
| C9, C10: webhooks double-send across replicas; Connection Request worker leases 1 job/s per replica (10k devices ≈ 2.8 h) | `CODE_REVIEW.md:77-78` |
| P1 #13: policies and templates send `xsd:string`, no dedupe, run before discovery, Nokia filter misses ALCL | `NOKIA-FIELD-GAP-REPORT:34` |
| P1 #14: firmware has no image↔model check and no `SoftwareVersion` confirmation | `NOKIA-FIELD-GAP-REPORT:35` |
| P1 #18: fixed 5/90 min online threshold flaps devices on 300–3600 s informs | `NOKIA-FIELD-GAP-REPORT:39` |
| P1 #17: monitoring noise (no volume floor, `info` pages, misreporting BSS panels) | `NOKIA-FIELD-GAP-REPORT:38` |
| HA not proven: multi-replica CWMP, worker leadership, distributed rate limit, failover, rolling upgrade | `ACS_INDEPENDENT_REAL_ACS_AUDIT:105-109` |
| No load or soak test at fleet scale (only 100 mock CPEs); no restore drill or RTO | `COMPATIBILITY.md:108-110`, `OPERATIONS-RUNBOOK.md:106-121` |
| D3, D4: expired `.trivyignore`; nginx as root; images not digest-pinned | `CODE_REVIEW.md:87-88` |

## 4. Product gaps against the stated goals

| Goal | What exists | Missing |
|---|---|---|
| **Sale-driven onboarding** (device + SIM → owner → APN/DNN → network lock) | Bootstrap, graduation, discovery; auto-apply templates by model and tenant; `account_device_mappings` (string account, free-text plan) | Site, service plan, account and onboarding records; PLANNED → IN_SERVICE states; device–SIM pairing; Unclaimed queue; strict mode; import of account/site/plan (and the `label` bug). Proposal §4. |
| **Deep BSS integration** | TMF638/639/640/641/642/656/688, outbox, webhooks, OAuth clients | Pre-provisioning (orders before the device connects); more order actions (APN, plan change, replace, campaigns); `PENDING_ACTIVE` lifecycle; onboarding events; TMF conformance tests; a partner sandbox |
| **Remote assess / report / troubleshoot** | Diagnostics (ping, traceroute, TR-143); captures; Wi-Fi insights; cellular telemetry; alarms | TR-471 capacity tests and UDP echo; guided troubleshooting by symptom; live job progress (SSE/WebSocket); scheduled and estate reports; SLA/KPI reports; usage export to the BSS |
| **Business estates and group management** | Static groups per tenant; bulk actions with per-device outcomes; scheduled jobs; canary rollouts | Estates; dynamic (rule-based) groups; Wi-Fi campaigns (event SSID with preview, schedule, revert); estate dashboards; delegated client access. Proposal §8. |
| **Cellular control** | SIM view, cellular telemetry, refresh | APN/DNN profiles per plan (writable on the FastMile); MCC/MNC check and alarm; SIM-swap alarm; roaming policy; Nokia operator-lock pre-config agreed with Nokia |
| **Wi-Fi control** | Per-instance resolver, Set SSID, bulk Set Wi-Fi, insights | Wi-Fi management page (networks, guest, clients, radios, policies); client refresh not hard-coded to `AccessPoint.` (P1 #9); RADIUS for Enterprise SSIDs; spec §9 field tests |
| **Other vendors and protocols** | TR-181 and TR-098 paths; vendor catalogs; USP controller; capability-driven console (PR #80) | Real-device qualification of Huawei, Teltonika and Zyxel; vendor evidence packs (P1–P5 all unknown); a physical USP CPE; `ChangeDUState` for apps on the CPE; Annex G on a real device |
| **Multi-tenant ISP platform** | Regions, customers (ISPs), projects; operator scopes | S3/S5/S6 isolation fixes; tenant-scoped BSS clients; estate scope for client admins |

## 5. Comparison with other ACS products

Public evidence is thin for commercial products (proposal §7). "Them" means what at least one product documents publicly.

| Capability | This ACS | Them |
|---|---|---|
| Zero-touch on first contact | Yes | Yes (GenieACS, Axiros, Incognito) |
| Pre-provisioning from an external system before first contact | **No** | GenieACS `ext()` lookups; Incognito LDAP/SQL |
| Holding unknown devices for approval | **No** (any bootstrap holder is admitted) | OpenWISP (registration switch) |
| Declarative config by group/tag/plan | Partial (model + tenant) | Yes (GenieACS presets, Incognito groups, GDMS site/group/model) |
| Helpdesk vs operations views | Starting (PR #80) | Yes (Incognito CSR vs Ops, Friendly Support Center) |
| One-click diagnostics incl. TR-471 | TR-143 only | TR-143 + TR-471 (Axiros, Incognito) |
| Schema-validated CWMP dumps | Redacted captures, no validation | Axiros |
| Maintenance windows for mass operations | Rollouts only | Axiros, Incognito |
| Northbound TM Forum APIs + webhooks | **Yes**: ahead of anything public | REST/SOAP, no TMF or webhooks documented |
| Per-device credentials, XMPP, USP | Yes | Common in commercial products |
| Operator SSO/MFA and fine-grained RBAC | RBAC yes, SSO/MFA **no** | Axiros RBAC wizard; SSO commonly expected |
| HA / scale proof | **No** | Claimed by commercial vendors |

The technical core already competes. This ACS falls behind on safety rails (denylist, firmware checks), on operator workflow (onboarding, pre-provisioning, claim queue, campaigns) and on operations (CI, HA, SSO).

## 6. Recommended order

1. **This week: safety.**
   - ~~Implement the denylist on every write path, with tests.~~ Done.
   - Fix CI: the billing problem, or a self-hosted runner.
   - Finish the EC2 deploy with TLS and the allowlist.
   - Run handoff field checks 1–8.
   - Read the FastMile's operator-lock and roaming values.
2. **Next: isolation and correctness.**
   - S3, S5, S6, then S2 (via onboarding strict mode).
   - P1 #13 (typed writes), P1 #14 (firmware checks), P1 #18 (online threshold), C2, C3.
   - Decide SSO/MFA.
3. **Then: the operator layer.**
   - Onboarding core with BSS pre-provisioning (proposal §4 phases 2–3), including APN/DNN profiles and the network-lock check.
   - Wi-Fi campaigns on static groups (§8 quick win).
   - Finish the console Phase 1 tabs.
4. **Then: scale and breadth.**
   - Estates and dynamic groups; the Wi-Fi management page; scheduled and estate reports.
   - TR-471.
   - Qualification of a second vendor.
   - A load/soak test and an HA design with a restore drill.

## 7. Stale statements corrected
- **Dangerous-parameter denylist:** described as in place in `HANDOFF-PLAN-2026-10-30.md` and `SESSION-SUMMARY-2026-10-30.md`; it is not implemented.
- **XMPP console button:** `XMPP-CONNECTION-REQUEST.md:269` says there is none; `XmppPanel` exists.
- **Beacon as a Wi-Fi client:** the handoff backlog lists excluding the Beacon from the gateway's client list as open; it was done in `043807b`.
- **S1 status:** the code review lists `jobs.payload` and `GET /schedules` as still open; both were fixed in `89bfac7` and `2474988`.
