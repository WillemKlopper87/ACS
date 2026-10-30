# Console UX proposal: device workspace, navigation, onboarding, troubleshooting

Status: proposal, 2026-10-30. Nothing here is built yet; §6 lists the decisions needed before building. It answers these requests from the user:

1. There is too much information per page; split it up or add sub-navigation.
2. Separate each device's configuration into sub-items (Wi-Fi, SIM, and so on).
3. Add a real onboarding workflow (where, what, who the device belongs to) that the BSS/NaaS integration can drive too.
4. Model it on the sale or procurement: device and SIM mapped to the end user or ISP by commercial model, APN/DNN settings applied, and the CPE network-locked to the MNO's MCC/MNC.
5. Move session captures into a troubleshooting area for NOC and tier-2 support.
6. Compare with typical ACS workflows in open-source and commercial products (§7).
7. Position the ACS as a BSS-integrated, multi-protocol device management platform, with multi-CPE group management for business clients, e.g. an event SSID across a client's sites (§8).

The findings come from reading the console source (`frontend/src`) and from the live FastMile on the EC2 lab. File references are at the time of writing.

## 1. What is wrong today

### The device page carries everything
`screens/DeviceDetail.tsx` (about 1,000 lines) renders inline under the fleet table, in a two-column grid.

| Fact | Effect |
|---|---|
| 19 panels and about 70 controls on one scroll | An operator looking for the SIM or Wi-Fi has to scan past credentials, object management, parameter attributes, VPN, firmware and uploads. |
| About 23 API calls when it opens, with a 5 s poll | The API's default burst limit is 30 (`ACS_API_RATE_LIMIT_BURST`). Each new panel brings the page closer to "rate limit exceeded", which already shows in the development build. |
| No URL of its own: `/fleet?device=<id>` under the fleet list | A device cannot be bookmarked, linked from a BSS ticket or an alert, or opened in two tabs. Closing it means losing your place in the list. |
| Reading and changing are mixed | Read-only information (SIM, Wi-Fi insights, parameters) sits between write tools (factory reset, object add/delete, parameter attributes). A read-only operator sees about 60 greyed-out controls. |
| The same action in two or three places | The device console repeats Reboot, Factory reset, Ping, Traceroute, Add/Delete object, Schedule Inform and Discover. The onboarding panel repeats Connection Request, Refresh cellular, Discover and Capture. Discover also appears under Device compatibility. |

### The navigation is long and mixes jobs
- 18 screens in 5 groups (`App.tsx:31-54`). There is no sub-navigation; only BSS Integration has tabs inside the page.
- **Captures** is filed under *Records*, next to the Audit Log and Reports. It is really a troubleshooting tool.
- **Dashboard** and **Fleet Health** show the same three charts (online status, reachability, Inform recency).
- Several screens carry two or more jobs:

  | Screen | Jobs on one page |
  |---|---|
  | Operators | Users, plus the role-permission matrix |
  | Tenancy | Regions, customers, projects and bulk device import |
  | Alerting | Configuring policies, plus triaging incidents |
  | Rollouts | The firmware image catalogue, plus rollouts |

## 2. Proposal A: a device workspace with sub-pages

Give every device its own route, **`/devices/<id>/<section>`**, with a fixed header and a left-hand sub-navigation. Keep `/fleet?device=<id>` working as a redirect, so existing links still open.

### Header (on every sub-page)
- **Name and identity:** label or OUI-serial, vendor and model, firmware, data model.
- **State:** online/rebooting badge, last Inform, reachability mode (direct, XMPP, periodic fallback).
- **Ownership:** customer, site and service, which link to the Ownership sub-page (see §4).
- **Mesh:** "satellite of …" or "N satellites".
- **Actions** menu with the common, safe operations: Connection Request, Schedule Inform, Reboot. Destructive ones (Factory reset) sit at the bottom of the menu behind a confirmation.

### Sub-pages

| Sub-page | Contents (existing components move here) | Audience |
|---|---|---|
| **Overview** | Health summary: onboarding status, open alarms, last 5 jobs, mesh topology, key facts (uptime, firmware, signal). One screen that answers "is this customer OK?" | Everyone |
| **Wi-Fi** | Networks (SSID per band, guest; the passphrase is write-only), clients, insights (charts), Refresh clients. It later grows into the Networks / Guest / Clients / Radios / Policies sections of `docs/WIFI-MANAGEMENT-SPEC.md`. | NOC, support |
| **Cellular & SIM** | SIM panel (masked, superadmin reveal), cell and signal data (5G/LTE band, RSRP, SINR), Refresh cellular diagnostics, data counters | NOC, support |
| **Connectivity** | How the ACS reaches the device: Connection Request mode and history, XMPP set-up, CWMP Digest and Connection Request credentials, NAT/CGNAT facts, VPN | Tier 2 |
| **Diagnostics** | Ping, traceroute, TR-143 speed tests, connection diagnostics, onboarding checks (today's "Onboarding & troubleshooting" panel), and links to start a session capture or open the device console | NOC, tier 2 |
| **Firmware & files** | Push firmware to this one device (with the note pointing to Rollouts), config backup and log uploads with their status | Tier 2 |
| **Parameters** *(advanced)* | Parameter browser and history, get/set parameters, parameter attributes, add/delete object, discovery and compatibility evidence | Tier 2, engineering |
| **Ownership** | Customer, project, site and service (from onboarding, §4), label, tags, location on a map, device audit trail | Support, admin |
| **Remote access** *(advanced)* | Device console, SSH/Telnet, web GUI proxy | Tier 2 |

What this changes:
- **Only the open sub-page loads.** Overview needs about 8 calls instead of 23, which takes the page off the rate limit's edge. Each sub-page polls only what it shows.
- **Each action has one home.** Reboot and Connection Request live in the header menu; Discover moves to Parameters; capture and console become links from Diagnostics instead of copies.
- **Role-aware:** *Parameters* and *Remote access* are marked "advanced" and collapse by default. Read-only operators see read-only views without rows of disabled buttons.
- **Deep links:** BSS tickets, alerts and audit entries can link straight to `/devices/<id>/wifi` or `/devices/<id>/diagnostics`.
- **The fleet list stays a list:** clicking a row opens the workspace. An optional slim preview (status, ownership, last Inform) on hover or selection keeps quick scanning possible.

**Vendor-neutral by design** (user, 2026-10-30): Nokia is the first vendor because it was the first to connect and be discovered, but other CPEs with similar or different capabilities will follow. Which sub-pages a device shows therefore comes from its discovered data model, not its vendor:
- Wi-Fi appears for `Device.WiFi.` (or TR-098 `WLANConfiguration`), and for mesh satellites, whose Wi-Fi page points to the gateway.
- Cellular & SIM appears for `Device.Cellular.`.
- A device that has not been discovered yet is offered every sub-page, plus a prompt to run discovery.

**Status: Phase 1 part 1 built** on branch `feat/console-device-workspace`:
- the `/devices/<id>/<section>` workspace and its sub-pages as listed above, with old `/fleet?device=` links redirecting;
- a reusable `SubNav` component;
- the regrouped sidebar (Inventory, Bulk actions, Troubleshooting → Session captures, Integrations → BSS / NaaS).

Still open in Phase 1: tabs for Operators, Tenancy, Firmware and Alerting; the merged Overview dashboard; and the Troubleshooting items beyond captures.

## 3. Proposal B: navigation with clear areas and sub-items

The main changes are a new **Troubleshooting** area and an **Onboarding** item under Devices.

```
Monitor
  Overview            (Dashboard + Fleet Health merged; widgets as now)
  Alarms              (incident triage; was the bottom half of Alerting)
  Jobs
Devices
  Inventory           (the fleet list; rows open the device workspace)
  Onboarding          (new: planned, waiting, unclaimed and in-progress devices; §4)
  Bulk actions        (was Fleet Control)
  Groups
Provisioning
  Templates
  Policies
  Schedules
  Firmware            (tabs: Images | Rollouts)
Troubleshooting       (new, for NOC and tier-2)
  Session captures    (moved from Records)
  Connection Requests (fleet view: which devices are unreachable and why, CGNAT/XMPP/STUN)
  Device evidence     (discovery and compatibility evidence, standards assessment, the discovery library export)
  Integration checks  (BSS lookup tools; was BSS Integration → Troubleshoot)
Integrations
  BSS / NaaS          (tabs: Setup | Health)
  Alert routing       (alert policies and escalation; was the top half of Alerting)
Administration
  Operators           (tabs: Users | Roles & permissions)
  Tenancy             (tabs: Regions | Customers | Projects | Sites)
  Audit log
  Reports
Help
```

- **Sub-items are real routes** (`/troubleshooting/captures`), so the sidebar can highlight the current one and links can be shared.
- **One reusable `Tabs` component** (accessible tabs, with the tab in the URL) replaces BSS Integration's hand-made buttons. Operators, Tenancy, Firmware and the device workspace all use it.
- **The sidebar stays collapsible** per area, as today.

## 4. Proposal C: a business onboarding workflow

### What exists today (facts from the code)
"Onboarding" today means the CPE reaching the ACS: bootstrap credential, graduation to a per-device Digest credential, then discovery. The pieces around it:

- **Device fields:** `customer_id` (the ISP tenant), projects, tags, a label, a free-text `location` and coordinates (migrations 0013, 0033, 0035, 0050, 0051).
- **`account_device_mappings`** (0007, 0052): a BSS `account_id` (a plain string), `service_plan` (free text), a role (gateway/extender/…) and a status. Only `ACTIVE` is ever written.
- **First contact** (`cmd/acs/session.go:380-390`): auto-apply config templates matched by model and customer, auto-discovery, mesh adoption.
- **BSS adapter:**
  - `POST /bss/v1/mappings` and TMF641 `add` link an account to a device that already exists.
  - Orders support only MODIFY_WIFI, SUSPEND and ACTIVATE.
  - A write-ahead outbox, webhooks and TMF events are in place.

### Gaps
| Gap | Consequence |
|---|---|
| No **site/premise** (address, coordinates, region) entity; location is free text on the device | "Where is it?" cannot be searched, mapped or handed to field techs consistently. A Beacon cannot inherit its gateway's site. |
| No **service plan / product** entity; `service_plan` is free text | Nothing ties "Home 5G 100" to the templates, policies and Wi-Fi defaults it should apply. Auto-apply works by model and customer only. |
| No link from a BSS account to the tenant, site or device ownership | Orders never set the device's customer, site, label or templates (no such code in `cmd/bssadapter`). |
| **No pre-provisioning** | TMF641 `add` and `/bss/v1/mappings` fail at once if the CPE has not connected yet (`tmf641_reconciler.go:91-94`, `main.go:548`). A BSS order placed before installation, which is the normal order of events, cannot be accepted. |
| **No onboarding state** | Nobody can see "planned / installed / secured / configured / in service" for a device or an order. |
| **Unknown serials are admitted** through bootstrap with no check that anyone expected them (`devices/bootstrap.go:21`) | Any device holding the bootstrap credential gets a row and a per-device credential. |
| Bulk import cannot carry account, site, plan or location, and **drops `label`** (parsed at `bulk_import_handlers.go:89`, never passed on at `:161`) | Import is identity-only; the label column is silently lost. |

### Proposed model
Four small additions. They use external references rather than personal data wherever possible, since subscriber identity and addresses are personal information under POPIA.

| Entity | Key fields | Notes |
|---|---|---|
| **Site** | `customer_id`, `external_ref` (BSS premise id), name, structured address (street, suburb, city, postal code, country), latitude/longitude, `region_id` | A device belongs to one site. Mesh satellites inherit their gateway's site. |
| **Service plan** | `customer_id`, `code` (matches the BSS `servicePlan`), name, allowed models/roles, bound templates, policies and groups, Wi-Fi defaults (SSID pattern; passphrase from the order, never stored after it is sent), and a **cellular profile** (below) | Replaces the free-text `service_plan`. It is what an order means by "what". |
| **Account** | `customer_id`, `external_ref` (BSS account id) | Only the reference by default; names and contact details stay in the BSS. |
| **Onboarding** | Expected device (OUI + serial and/or IMEI), expected **SIM** (ICCID, and the IMSI when known), account, site, service plan, role, commercial model, source (`console` / `bss` / `import`), external order ref, state, timestamps, requested_by, last error | One record per device being brought into service, including replacements. The SIM identifiers follow the existing SIM rules: masked in every view, full values only through the audited superadmin reveal. |

### The sale drives onboarding: device, SIM, owner, cellular settings
The user's flow, 2026-10-30. A sale or procurement:
1. pairs a **device with a SIM**, and
2. assigns them to the **end user or to an ISP partner**, depending on the commercial model.

The CPE then gets the right **APN/DNN** settings and should be **network-locked to the MNO's MCC/MNC**. In this model:

- **Who, by commercial model:**
  - *Retail:* the MNO's own retail tenant plus the subscriber's account reference.
  - *Wholesale:* the ISP partner's tenant (the existing "customer") plus that ISP's account reference.

  The order says which model applies, and the tenant follows from it. Operator scoping, templates and reports already work per tenant.
- **Device–SIM pairing:** checked at CONTACTED.
  - The Inform reports IMEI and the SIM reports ICCID/IMSI (the FastMile exposes both; see the SIM panel).
  - A mismatch with the sale holds the onboarding (ON_HOLD, "SIM does not match the sale") instead of configuring the wrong device.
  - After go-live, a changed ICCID raises a **SIM swap** alarm. This is relevant to RICA, and to theft of the CPE or SIM.
- **Cellular profile** (part of the service plan), applied in CONFIGURING with read-back. On FastMile 1.2501.00.0798 these are all writable over TR-069:

  | Setting | FastMile path |
  |---|---|
  | APN | `Device.Cellular.AccessPoint.1.APN` |
  | 5G DNN | `Device.Cellular.AccessPoint.1.X_ALU-COM_DNN` |
  | APN authentication | `…AccessPoint.1.Username` / `Password`. The password is a secret: write-only, and redacted once the job finishes (migration 0086). |
  | MTU | `…AccessPoint.1.X_ALU-COM_APNMTU` |
  | Bootstrap APN | `Device.Cellular.Interface.1.X_ALU-COM_APNForBootstrap` |
  | Roaming policy | `Device.Cellular.X_ALU-COM_RoamingConfiguration` |

  Change the APN only after a read and on one device first. A wrong APN cuts the CPE's data path, and with it the ACS, until it is corrected locally.
- **Network lock to the MNO's MCC/MNC:** on this firmware it is not a list the ACS can write.
  - The FastMile's **operator lock** comes from Nokia's pre-configuration server. The settings involved are `X_ALU-COM_ModemDeviceInfo.X_ALU-COM_NokiaServerURL`, `ForceDownloadPreconfigFromServer`, `ObtainOPIDFromPLMN` and `QueryBOENGServerInOperatorLock`.
  - TR-069 exposes only switches around the lock, such as `X_ALU-COM_BlockingTrafficByOperatorLock`.
  - These are deliberately on the server-side dangerous-parameter denylist: a wrong value can strand the device. (That protection was missing until 2026-10-30, gap #1 in `docs/GAP-ANALYSIS-2026-10-30.md`; it is now enforced for every write path.)

  So:
  1. **Set up the lock with Nokia:** an operator pre-configuration for MCC 655 / MNC 06 (the PLMN the field FastMile reports, `USIM.X_ALU-COM_PLMN = 65506`), applied by Nokia's server or at the factory.
  2. **The ACS enforces by checking.** During onboarding and on every Inform it compares the SIM's home PLMN and the attached network (`USIM.X_ALU-COM_PLMN`, `…CellStat.PLMNName`, `RoamingStatus`) against the plan's allowed MCC/MNC list. A foreign SIM or unexpected roaming raises an alarm and can put the onboarding ON_HOLD.
  3. **Only after Nokia confirms the semantics on one lab unit** should the ACS write `BlockingTrafficByOperatorLock` itself, as an explicit plan option behind confirmation.
  4. **For other vendors**, a PLMN allow-list parameter (where the model has one) becomes a normal, verified write from the same cellular profile.

  To do before designing further: read the current values of the operator-lock and roaming fields on the lab FastMile (`POST /parameters/get`, read-only), so we know what Nokia ships today.

### Onboarding states
```
PLANNED ──first Inform matched──▶ CONTACTED ──own credential active──▶ SECURED
   │                                                                      │
   │ (expires after N days: flagged)                   discovery (if the model needs it)
   ▼                                                                      ▼
CANCELLED                     IN_SERVICE ◀──read-back verified── CONFIGURING ◀── DISCOVERED
                                    ▲
         FAILED (step + reason, Retry) / ON_HOLD can occur at any step
```

- **PLANNED:** created from the console, an import or a BSS order *before* the device exists. This is the missing pre-provisioning.
- **CONTACTED → SECURED → DISCOVERED:** the existing bootstrap, graduation and discovery steps. They are now recorded against the onboarding instead of being invisible.
- **CONFIGURING:** applies the service plan's cellular profile (APN/DNN, roaming), templates, policies, group membership and the order's Wi-Fi settings as verified jobs. Every write is read back. The network-lock check (above) runs before IN_SERVICE.
- **IN_SERVICE:** the BSS mapping becomes `ACTIVE` (today `PENDING_ACTIVE` is defined but never used). The TMF641 item completes and a webhook/TMF event fires.
- **Unclaimed devices:** a CPE that informs with no PLANNED record lands in an *Unclaimed* queue. An operator claims it (account, site, plan) or blocks it. An optional strict mode (`ACS_ONBOARDING_REQUIRE_PLAN`) would keep unclaimed devices on the bootstrap credential, with no graduation, until claimed. That closes the "any serial is admitted" gap.
- **Replacement (RMA):** onboarding a device for an account/site/role that already has one hands over the Wi-Fi settings and service plan, and unassigns the old device with a reason.

### BSS / NaaS integration
- **TMF641 `add`** for an unknown serial creates a PLANNED onboarding instead of failing. The order item stays `RUNNING` ("awaiting device") and completes at IN_SERVICE, or fails with the step and reason.
- **`POST /bss/v1/mappings`** uses the same path.
- **Webhooks** gain `ONBOARDING_STATE_CHANGED`, and TMF688 events carry the same transitions. The NaaS platform then sees installation progress without polling.
- **TMF639** resources gain the site (place) relationship. A TMF673 geographic address can follow if the BSS wants it.

### Console
- **Devices → Onboarding:** tabs *Planned*, *In progress*, *Unclaimed* and *Completed / failed*. Each row shows the state timeline and the current blocking check. The existing technical checks from `DeviceOnboardingPanel` become the detail of the CONTACTED/SECURED steps.
- **New-onboarding wizard:**
  1. **Device:** serial, scan, or pick from Unclaimed.
  2. **Who:** ISP tenant, then the account reference.
  3. **Where:** pick a site or create one (address and map pin).
  4. **What:** service plan, which shows exactly which templates, policies and Wi-Fi settings will apply.
  5. **Review**, then create.
- **Bulk import** accepts account, site, plan, role, label and location columns (and the label bug gets fixed).
- **Device workspace:** the Overview shows the onboarding timeline, and the Ownership sub-page shows account, site and plan.

## 5. Suggested order of work

| Phase | Scope | Risk |
|---|---|---|
| **1. Structure (frontend only)** | Shared `Tabs` component; device workspace routes and sub-pages, moving today's components into them; regrouped navigation with the Troubleshooting area; merged Overview dashboard | Low: no API or data change. Each sub-page can ship on its own. |
| **2. Onboarding core** | Migrations 0087+ for site, service plan, account and onboarding; the state machine driven by existing first-contact hooks; the Unclaimed queue; import columns and the label fix; the Onboarding screen and wizard | Medium: new tables and a state machine on the Inform path |
| **3. BSS pre-provisioning** | TMF641 / mappings create PLANNED onboardings; mapping states; webhooks and TMF events; optional strict mode | Medium: changes external API behaviour, so agree it with the BSS/NaaS team first |
| **4. Wi-Fi management** | The Networks / Guest / Clients / Radios / Policies sections of `WIFI-MANAGEMENT-SPEC.md` inside the device Wi-Fi sub-page | Medium |

## 6. Decisions needed
1. **Subscriber data:** keep only BSS references (recommended, POPIA), or also store subscriber names and contacts in the ACS?
2. **Master of sites and addresses:** does the BSS own them and the ACS mirror them, or can the ACS create sites itself (needed for the console wizard)?
3. **Strict mode:** should an unplanned device be held at bootstrap until claimed, and from when?
4. **Device page form:** a full page per device (recommended), or keep a quick-look drawer over the fleet list as well?
5. **Network lock:** will Nokia deliver the MCC 655 / MNC 06 operator lock as a pre-configuration, with the ACS checking and alarming (recommended), or must the ACS also be able to switch it?
6. **Commercial models:** which models exist (retail, wholesale ISP, enterprise), and does each map to one tenant or to several?

## 7. How other ACS products do it (desk research, 2026-10-30)

Web research covered:
- **Open source:** GenieACS, Oktopus, FreeACS/LibreACS, and OpenWISP (as a comparison).
- **Commercial:** Axiros AXESS/AX USP, Incognito, Friendly, Nokia Corteca, Zyxel, Grandstream GDMS, TP-Link Omada, Huawei NCE, Calix.

**Evidence is thin for commercial products:**
- Mostly marketing pages.
- Adtran Mosaic, CommScope/Arris and Calix's console could not be verified.
- The official TM Forum API pages refused access.

Treat the commercial rows as indicative.

| Pattern seen elsewhere | Source | Here today | In this proposal |
|---|---|---|---|
| Onboarding keys off the first BOOTSTRAP Inform; configuration follows automatically | Axiros, GenieACS, TR-069 vendor manuals | Yes: auto-apply templates, discovery and mesh adoption on event `0` | Kept; it becomes the CONTACTED → CONFIGURING steps of the onboarding |
| Declarative templates targeted by tag, group or filter | GenieACS presets, Incognito customer groups, GDMS site/group/model | Templates by model + tenant, policies, groups | Service plans bind templates, policies and groups, and orders pick the plan |
| Attributes looked up from an external system by serial | GenieACS `ext()`; Incognito LDAP/SQL | No | Planned onboardings carry account, site, plan and SIM from the BSS before first contact |
| Unknown devices are not auto-provisioned | OpenWISP can switch off device self-creation; no ACS documents a claim queue | No: any serial with the bootstrap credential is admitted | Unclaimed queue plus optional strict mode (§4) |
| Separate helpdesk and operations views | Incognito CSR vs Ops dashboards; Friendly Support Center; Axiros RBAC | One page for everyone, with disabled controls | Device Overview for helpdesk; advanced sub-pages and a Troubleshooting area for tier-2 (§2, §3) |
| One-click diagnostics: ping, traceroute, TR-143/TR-471 | Axiros, Incognito, Zyxel, GDMS | Ping, traceroute, TR-143 | Grouped on the Diagnostics sub-page; TR-471 is a candidate |
| CWMP message dumps, schema-validated, for tier-2 | Axiros | Redacted session captures | Moved to Troubleshooting; schema validation is a candidate |
| XMPP Connection Request for devices behind NAT | Zyxel | Yes (Annex K, just built) | Connectivity sub-page |
| Maintenance windows for mass operations | Axiros, Incognito | Canary rollouts; scheduled jobs | Candidate for Bulk actions and Firmware |
| Disruptive RPCs deferred while the subscriber is using the device | GDMS delays reboot during a call | No | Candidate: hold a reboot or factory reset while Wi-Fi clients are active, or until a window, unless forced |
| Northbound: REST/SOAP plus pushed reports; no webhooks and no TM Forum APIs in public material | GenieACS, Incognito, Friendly | REST, TMF638/639/640/641/642/656/688, webhooks, a write-ahead order outbox | Extended with onboarding events; this is an area where this ACS is ahead of what vendors publish |
| TR-069 and TR-369 side by side | Corteca, Axiros, Incognito, Friendly, Zyxel, Oktopus | Yes (`cmd/uspc`) | Unchanged |

**Takeaways for this ACS:**
- **The technical core already matches or exceeds the market:** per-device credential graduation, XMPP, USP, TMF APIs with webhooks, and data-protection masking.
- **The gap is the operator layer around it:**
  - a business onboarding record that exists *before* the device does
  - a claim queue for unexpected devices
  - role-shaped views for helpdesk and tier-2

  That is what Phases 1–3 add.
- **Ideas to keep in the backlog:**
  - deferring disruptive actions while the CPE is in use
  - maintenance windows for bulk work
  - TR-471 speed tests
  - schema-validated session dumps

Main sources:
- [GenieACS provisions](https://genieacs.readthedocs.io/en/stable/provisions.html)
- [GenieACS CPE authentication](https://raw.githubusercontent.com/genieacs/genieacs/master/docs/cpe-authentication.rst)
- [GenieACS API reference](https://github.com/genieacs/genieacs/wiki/API-Reference)
- [Axiros AXESS](https://axiros.com/products/axess-acs)
- [Incognito ACS](https://www.incognito.com/products/auto-configuration-server/)
- [Friendly cloud ACS](https://friendly-tech.com/friendly-tr-069-cloud-acs/)
- [openwisp-config](https://github.com/openwisp/openwisp-config)
- [Grandstream TR-069 guide](https://documentation.grandstream.com/knowledge-base/tr-069-guide/)
- [Zyxel DMS docs (development domain, lower confidence)](https://shared-cloud-docs.dev.tr069.cloud/docs/intro)
- [Nokia Corteca news](https://www.nokia.com/newsroom/nokias-corteca-cloud-for-device-and-wi-fi-management-adds-hundreds-of-legacy-broadband-devices-mwc25/)

## 8. Positioning and Proposal D: business estates and Wi-Fi campaigns

### What this ACS should be
The user's aim (2026-10-30): more than a TR-069 server. A **device management platform for an MNO/ISP** that integrates deeply with the BSS. It should give the operator connectivity, control and rich features to remotely assess, report on and troubleshoot every device it sells or hands out. It should also let large clients be managed as estates of many CPEs, for example adding an SSID for an event across all of a client's sites (as Grandstream GDMS does).

The five pillars:

| Pillar | Built today | Added by this proposal |
|---|---|---|
| **Connectivity**: reach every device | CWMP with per-device credentials; XMPP Connection Request for CGNAT; a USP (TR-369) controller over WebSocket and MQTT; a VPN overlay (peer generation); SNMP used only as an identity fallback (`internal/oidfallback`) | The Connectivity sub-page; a fleet-wide reachability view in Troubleshooting |
| **Control**: configure safely at scale | Verified writes with read-back; templates; policies; groups; bulk Set Wi-Fi; canary firmware rollouts; scheduled jobs | Service plans with cellular profiles; onboarding; estates; Wi-Fi campaigns (below) |
| **Insight**: assess and report | Wi-Fi insights; cellular/5G telemetry; SIM view; device report export; dashboards | Estate- and campaign-level reports |
| **Assurance**: troubleshoot | Diagnostics (ping, traceroute, TR-143); session captures; onboarding checks; compatibility evidence; alarms and incidents | The Troubleshooting area; the Diagnostics sub-page; SIM-swap and network-lock alarms |
| **Integration**: BSS/NaaS-native | TMF638/639/640/641/642/656/688; a write-ahead order outbox; webhooks; OAuth2 clients | BSS pre-provisioning; onboarding events; campaigns callable from the NaaS |

"Multi-protocol" is real for CWMP and USP today. Other device protocols should be added only with a concrete device to support, rather than claimed up front.

### Estates: a business client's devices across many sites
- **Estate** = one business account (from §4) with many **sites**, each with one or more gateways and mesh satellites. Retail subscribers are an estate of one; nothing extra is needed for them.
- **Dynamic groups:** today's groups are static member lists. Add rule-based groups (tenant, account/estate, site, model, tag, service plan, firmware), so "all Nokia gateways of client X" stays correct as devices are added or replaced.
- **Estate view** (under Devices or the account): sites on a map, devices per site, online/offline, open alarms, Wi-Fi clients and usage per site, and firmware/compliance roll-up. Estate reports (uptime, signal, usage per site) come from the same data as device reports.
- **Delegated access (later):** a business client's own administrator gets a read-only, estate-scoped login. That is possible because operator scoping already exists per tenant; it would need an estate scope.

### Wi-Fi campaigns: for example an event SSID across an estate
A **campaign** applies one Wi-Fi network definition to many CPEs for a period, then reverts it.

**Definition:**
- SSID name and band(s)
- security: WPA2/WPA3-Personal with a passphrase, or Enterprise against the NaaS RADIUS
- hidden or advertised
- client isolation
- max clients
- guest flag
- start and end time, with timezone
- target: an estate, a group, sites or devices

**Preview (dry run), per device:**
- which SSID slot it will use
- exactly what it will write
- why a device is skipped: no free slot, mesh satellite (its SSIDs follow the gateway), unsupported setting, or offline

**Execution:**
1. At the start time, every target gets a verified job:
   - it records the slot's current values
   - it writes the definition
   - it reads the values back
2. Progress shows per device: applied, waiting for check-in, failed (with reason), or skipped.
3. At the end time the ACS restores the recorded values (or disables the slot) and verifies again.
4. A device that was offline catches up at its next check-in. Restore always runs, even for a device that never applied the campaign.

**Safety:**
- Campaigns never touch the primary or existing guest networks.
- Passphrases are write-only and redacted once the job finishes (migration 0086).
- One campaign per slot per device.

**Reporting:** devices applied, clients and usage on the campaign SSID (Wi-Fi insights already sample per SSID), and an export.

**On FastMile 1.2501.00.0798** (discovered tree):
- **Writable on a spare slot:** the four factory-disabled slots (SSID/AccessPoint 3, 4, 7, 8) expose:
  - `SSID.{i}.SSID` and `Enable`
  - `AccessPoint.{i}.Security.ModeEnabled` and `KeyPassphrase`
  - `IsolationEnable`
  - `MaxAssociatedDevices`
  - `SSIDAdvertisementEnabled`
  - `SSID.{i}.X_ALU-COM_IsGuestSSID`
- **Not available:**
  - no per-SSID rate limiting or QoS
  - no captive portal (that belongs to the NaaS, §8 of the Wi-Fi spec)
  - no on-device schedule
- **Timing:** start and stop are ACS-driven, so behind CGNAT they take effect at the next check-in. With XMPP Connection Request they take seconds. Event campaigns are a strong reason to roll XMPP out.
- **To field-test before release:** whether an enabled extra SSID is propagated by the FastMile's EasyMesh controller to its Beacons.

**Building blocks that already exist:**
- the per-instance Wi-Fi resolver (to find free slots and know primary/guest)
- verified SPV jobs
- bulk `SET_WIFI` with per-device outcomes
- scheduled jobs
- groups
- Wi-Fi insights per SSID
- secret redaction

What is new:
- the campaign record with its saved prior values
- slot allocation
- the start/end scheduler
- the campaign screen

### Where it fits in the phases
- **Phase 2b (quick win, after Phase 1):** Wi-Fi campaigns on today's static groups, with schedule, preview and revert. This needs no onboarding entities and delivers the event-SSID use case early.
- **Phase 5:** estates on top of the Phase 2 site and account entities, plus dynamic groups, estate reports, and later delegated client access.
