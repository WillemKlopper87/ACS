# Wi-Fi management page — specification

Status: draft, 2026-10-30. Grounded in the Nokia FastMile 5G Gateway 2 (3TG02241, firmware 1.2501.00.0798) and Nokia WiFi Beacon 3.1 (3TN00626IJLJ03) field test; see `ci/compatibility/discoveries/` for the discovered trees.

## 1. Goal

A controller-style page (in the spirit of TP-Link Omada / Grandstream GWN) to manage **SSIDs, Wi-Fi clients and access methods** per gateway and across the fleet, while staying **vendor-neutral**: every control is shown only when the device's own discovery says the path exists, and editable only when it is writable. The same page must work for Nokia, Huawei, ZTE and future CPEs without hard-coded vendor paths.

The ACS must work standalone **and** as a component of the ISP NaaS platform (§8).

## 2. Ground rules

1. **Discovery-gated.** A control renders only if its path (or the first instance of its table) is in the device's latest discovery snapshot; it is editable only if discovered `RW`. Missing → hidden or shown as "not supported by this firmware" (for features operators expect, e.g. rate limiting).
2. **Instances are discovered, never assumed.** Today the ACS hard-codes instance 1 (`adapters/canonical.go`: `Device.WiFi.SSID.1.SSID`, `AccessPoint.1.Security.KeyPassphrase`, `SSID.1.Enable`). The page must enumerate `WiFi.SSID.{i}` / `WiFi.AccessPoint.{i}` / `WiFi.Radio.{i}` and join them by reference (`AccessPoint.{i}.SSIDReference` → `SSID.{k}`, `SSID.{k}.LowerLayers` → `Radio.{r}`).
3. **Every write is a verified job.** Writes go through `PUT /api/v1/devices/{id}/parameters` (discovered-writable check + read-back `Verify: true`). The UI shows queued → applied → verified, never optimistic success.
4. **Periodic-inform reality.** Behind CGNAT, Connection Request never succeeds, so reads/writes complete on the next Inform (≈60 s at the qualification interval; operators often use 300–3600 s). Show "pending until next check-in (≈N s)" and an *as of* timestamp on all live data. Batch edits into one job per save.
5. **Small reads.** The FastMile drops CWMP sessions on long request bursts. Read targeted table roots (one GPV per table), never the whole `Device.WiFi.` subtree.
6. **Secrets are write-only in the UI.** Passphrases, PSKs, WEP keys and RADIUS secrets are never displayed or exported (they currently land in plaintext in parameter cache/history — review finding S1; fix that before shipping this page).

## 3. Page structure

Gateway context: `Devices → <gateway> → Wi-Fi`. Fleet context: `Wi-Fi` top-level with the same tabs aggregated (search clients across fleet, bulk SSID policy via config templates).

### 3.1 Networks (SSIDs)

| Field | TR-181 path (per `{i}`) | FastMile 1.2501.00.0798 |
|---|---|---|
| Name | `WiFi.SSID.{k}.SSID` | RW |
| Enabled | `WiFi.SSID.{k}.Enable` / `WiFi.AccessPoint.{i}.Enable` | RW / RW |
| Status | `WiFi.SSID.{k}.Status`, `WiFi.AccessPoint.{i}.Status` | RO |
| Band / radio | `WiFi.SSID.{k}.LowerLayers` → `WiFi.Radio.{r}.OperatingFrequencyBand` | RW / RW |
| BSSID | `WiFi.SSID.{k}.BSSID` | RO |
| Hidden | `WiFi.AccessPoint.{i}.SSIDAdvertisementEnabled` (inverted) | RW |
| Guest network | `WiFi.SSID.{k}.X_ALU-COM_IsGuestSSID` (vendor) | RW |
| Security mode | `WiFi.AccessPoint.{i}.Security.ModeEnabled` ∈ `ModesSupported` | RW / RO |
| Passphrase | `WiFi.AccessPoint.{i}.Security.KeyPassphrase` (write-only) | RW |
| RADIUS (Enterprise) | `Security.RadiusServerIPAddr`, `RadiusServerPort`, `SecondaryRadiusServerIPAddr` (+ secret if exposed) | RW |
| Traffic | `WiFi.SSID.{k}.Stats.{Bytes,Packets}{Sent,Received}` | RO |

Create / delete SSID: `WiFi.SSID.` and `WiFi.AccessPoint.` are discovered as writable tables, so `POST /objects` (AddObject) **may** work. Ship "Create network" only after the AddObject test in §9 passes for the exact firmware; otherwise offer "enable/rename an existing (guest) SSID" instead — the Nokia web UI reports up to 8 guest SSIDs.

### 3.2 Guest access

Vendor feature on Nokia: `WiFi.X_ALU-COM_WLANForGuest.{g}.{Enable, Duration, Expiry (RO), PortIsolation, SSIDReference}`.

- "Open guest Wi-Fi for *N* hours": set `Duration`, `Enable=true`, optional `PortIsolation`, bound to a guest SSID; show `Expiry` countdown after the next Inform.
- Optional passphrase rotation per guest session (voucher-style without a captive portal).
- Units of `Duration` (minutes vs seconds) must be confirmed on-device before release (§9).

### 3.3 Clients

One row per client MAC, merged from three sources (all RO):

| Column | Source |
|---|---|
| Hostname, IP | `Hosts.Host.{h}.{HostName, IPAddress, IPv4Address.{n}.IPAddress, IPv6Address.{n}.IPAddress}`; EasyMesh `STA.{s}.{Hostname, IPV4Address, IPV6Address}` |
| MAC | `AccessPoint.{i}.AssociatedDevice.{j}.MACAddress`, `STA.{s}.MACAddress`, `Hosts.Host.{h}.PhysAddress` |
| Network / band | parent `AccessPoint.{i}` → SSID; `Hosts.Host.{h}.X_ALU-COM_WifiBand` |
| **Mesh node** (FastMile vs Beacon) | parent `DataElements.Network.Device.{n}` (Beacon clients are visible **only** here — the Beacon has no `Device.WiFi.` over CWMP) |
| Signal | `AssociatedDevice.{j}.SignalStrength` (dBm); `STA.{s}.SignalStrength` (RCPI → convert), `X_ALU-COM_EWMA_SignalStrength`; `Hosts.Host.{h}.X_ALU-COM_WifiRSSI` |
| Noise | `STA.{s}.MultiAPSTA.Noise` |
| Rates | `AssociatedDevice.{j}.LastDataDownlinkRate/UplinkRate`; `STA.{s}.EstMACDataRateDownlink/Uplink`, `X_ALU-COM_EWMA_LastData*Rate` |
| Wi-Fi standard / capabilities | `AssociatedDevice.{j}.OperatingStandard`; `STA.{s}.{HT,VHT,HE}Capabilities` |
| **Authenticated** | `AssociatedDevice.{j}.AuthenticationState`; method = parent AP's `Security.ModeEnabled` (PSK vs Enterprise) |
| Retries / errors | `AssociatedDevice.{j}.Retransmissions`; `STA.{s}.{RetransCount, ErrorsSent, ErrorsReceived}` |
| Connected since | `STA.{s}.LastConnectTime`, `MultiAPSTA.AssociationTime` |
| Steering history | `STA.{s}.MultiAPSTA.SteeringHistory.{n}.{Time, APOrigin, APDestination, TriggerEvent, SteeringApproach, SteeringDuration}` |
| Guest device | `Hosts.Host.{h}.X_ALU-COM_GuestDevice` |

Refresh = one GPV per table root (`Device.WiFi.AccessPoint.`, `Device.WiFi.DataElements.Network.Device.`, `Device.Hosts.Host.`), not the whole tree. Reuse the existing projection `adapters.ProjectWiFiTopology` (`GET /devices/{id}/wifi-topology`, currently unused by the console) for the mesh/node join.

Per-client actions: none native on this firmware (no MAC deny list). "Block device" is possible only if `Firewall.Chain.{c}.Rule.{r}` supports a source-MAC match — verify (§9) before offering it.

### 3.4 Radios

| Field | Path | FastMile |
|---|---|---|
| Enable, Status, Name | `WiFi.Radio.{r}.{Enable,Status,Name}` | RW/RO/RO |
| Band | `OperatingFrequencyBand` ∈ `SupportedFrequencyBands` | RW |
| Standards | `OperatingStandards` ∈ `SupportedStandards` | RW |
| Channel | `Channel` ∈ `PossibleChannels`; `AutoChannelEnable` (if `AutoChannelSupported`); `ChannelsInUse` | RW |
| Width | `OperatingChannelBandwidth`; `CurrentOperatingChannelBandwidth` | RW / RO |
| Transmit power | `TransmitPower` ∈ `TransmitPowerSupported` (%) | RW |
| Beacon / DTIM / preamble | `BeaconPeriod`, `DTIMPeriod`, `PreambleType` | RW |
| Stats | `Stats.{Bytes,Packets,Errors}{Sent,Received}` | RO |

`RegulatoryDomain` is RW but **must not** be editable from this page (legal limits).

### 3.5 Policies

| Control | Path | FastMile |
|---|---|---|
| Max clients per SSID | `AccessPoint.{i}.MaxAssociatedDevices` | RW |
| Client isolation | `AccessPoint.{i}.IsolationEnable` | RW |
| WMM | `AccessPoint.{i}.WMMEnable` (if `WMMCapability`) | RW |
| WPS | `AccessPoint.{i}.WPS.Enable`, `ConfigMethodsEnabled` | RW |
| **Rate limiting** | `Device.QoS.Shaper` / `Policer` / `Queue` or vendor rate-limit | **not exposed** — only `QoS.Classification` (marking/priority). Show "not supported by this firmware — apply per-subscriber limits upstream (§8)". |
| MAC allow/deny | `AccessPoint.{i}.MACAddressControlEnabled` / `AllowedMACAddress` | **not exposed** |
| Captive portal | `Device.CaptivePortal.` | **not exposed** — use RADIUS or guest WLAN (§3.2, §8) |

## 4. Mesh (Beacon) handling

- Beacon 3.1 reports `Manufacturer=ALCL`, has no `Device.WiFi.`/`Cellular.`; its SSIDs follow the gateway's EasyMesh config. The page shows Beacons as **nodes of the gateway** (via `GatewayInfo.{ManufacturerOUI,ProductClass,SerialNumber}` on the Beacon matched to the gateway's identity, and via `DataElements.Network.Device.{n}`), with their clients, not as separately configurable APs.
- SSID edits are applied on the gateway only; the UI explains they propagate to all Beacons.

## 5. Dangerous parameters (never on this page; gated elsewhere behind explicit confirmation)

| Path | Risk |
|---|---|
| `X_ALU-COM_LanAccessCfg.Tr69Disabled` | Disables TR-069 → ACS loses the device |
| `X_ALU-COM_RemoteWebDebug.{Enable,DebugServerURL}` | Opens remote debug access |
| `X_ALU-COM_ModemDeviceInfo.X_ALU-COM_NokiaServerURL`, `*OperatorLock*`, `ForceDownload*`, `ImageDownloadTimer` | Changes firmware/config sources, operator lock |
| `X_ALU-COM_FastmileController.URL`, `X_ALU-COM_NWCC.*.URL` | Repoints Nokia's own cloud controller |
| `ManagementServer.{URL,Username,Password,EnableCWMP}` | ACS reachability/identity (owned by credential graduation) |
| `WiFi.Radio.{r}.RegulatoryDomain` | Regulatory compliance |
| `Security.WEPKey`, WEP/open modes | Insecure; hide WEP modes even if `ModesSupported` lists them |

Implement as a server-side denylist (vendor-scoped, from the catalog) enforced in `validateWritableParameters`, not only hidden in the UI.

## 6. Controller authority (decided 2026-10-30)

**Decision: the ACS is the mandatory, authoritative device manager.** Every CPE is managed through the ACS; nothing else writes device configuration. The NaaS platform integrates *on top of* the ACS (§8) and never talks to CPEs directly.

Consequences for the Nokia WiFi Cloud Controller (NWCC):
- The FastMile carries `X_ALU-COM_NWCC.{L1Server,L2Server,RemoteMobileAccess}.URL`, `X_ALU-COM_FastmileController.URL` and a TR-369 agent pointed at controller `self.nokia:nwcc` (MQTT/TLS, broker currently empty). NWCC is **not** used as a parallel controller.
- The ACS takes over the controller role, preferably also over USP by pointing the CPE's TR-369 agent at the ACS's own USP controller (`cmd/uspc`, MQTT over TLS) — a persistent CPE-initiated connection that works through CGNAT. NWCC's proprietary (non-TR-181) features are out of scope.
- The ACS records the NWCC/controller URLs as evidence and **alerts if any becomes non-empty** or if config changes appear that the ACS did not make (`4 VALUE CHANGE` with an empty CommandKey, already audited as `UnsolicitedValueChange`). Clearing them is an explicit, confirmed operator action (they are on the §5 denylist for the Wi-Fi page).

## 7. API additions

| Endpoint | Purpose |
|---|---|
| `GET /devices/{id}/wifi` | Projection: radios, SSIDs, APs, security (secrets redacted), guest WLANs, policies, each with `supported`/`writable` from discovery and `as_of` |
| `GET /devices/{id}/wifi/clients` | Merged client table (§3.3) incl. mesh node; extends `ProjectWiFiTopology` |
| `POST /devices/{id}/wifi/refresh` | One targeted GPV job (table roots), returns command key |
| `PUT /devices/{id}/wifi/networks/{k}` | Canonical → discovered paths, one SET job, verified |
| `POST /devices/{id}/wifi/guest` | Guest WLAN enable/duration/rotation |
| `POST /devices/{id}/wifi/networks` | AddObject SSID+AP (only where §9 proved it) |
| Fleet: `GET /wifi/clients?q=` | Cross-fleet client search |

Canonical model extension in `adapters/canonical.go`: replace instance-1 constants with resolvers over discovered instances (`wifi.ssid[k].name`, `wifi.ap[i].security.mode`, …), vendor extensions via the catalog (`nokia.xml`: `X_ALU-COM_IsGuestSSID`, `X_ALU-COM_WLANForGuest`).

## 8. NaaS / RADIUS boundary

- **Standalone:** the ACS configures the CPE (SSIDs, security, guest WLAN, radios, policies) and shows clients. It must be fully usable without NaaS.
- **With NaaS:** NaaS owns subscribers, access policy and **per-user authentication** (portal, form, token, certificate) through its **RADIUS/AAA**; the ACS points the SSID at that RADIUS (`Security.RadiusServer*`, Enterprise mode) and exposes the client view northbound (TMF639/BSS adapter).
- **How NaaS drives the ACS** (all through ACS northbound APIs, never directly to CPEs):

  | NaaS need | ACS mechanism (existing → to extend) |
  |---|---|
  | Issue commands | BSS adapter orders / TMF641 service orders → generalise beyond `MODIFY_WIFI` to the Wi-Fi API (§7) |
  | Workflows that enforce rules/scenarios | Policies (enforcement on Inform), config templates, scheduled jobs → expose as NaaS-callable APIs with idempotency keys |
  | Troubleshooting | Diagnostics (fix C1/C8 first), on-demand parameter reads, CWMP/USP session captures, discovery/compatibility evidence |
  | State and events | TMF639 inventory, TMF642 alarms, TMF688 events / webhooks |
  | Capability catalogue | `ci/compatibility/discoveries/` (plain files) |

  Prerequisites from the 2026-10-30 review before NaaS is connected: BSS tenant isolation (S3, S5), per-instance Wi-Fi resolution instead of `SSID.1` (§7), webhook delivery claiming (C9), cellular sentinel/field mapping so northbound signal data is correct.
- **Rate limiting:** not available on the CPE (this firmware). NaaS applies per-subscriber bandwidth upstream (BNG/UPF, 5G QoS/slice — the FastMile exposes `Cellular.AccessPoint.{i}.X_ALU-COM_{SST,SD,DNN}`) or via RADIUS attributes at the edge that enforces them.
- The discovery library (`ci/compatibility/discoveries/`) is the shared capability catalogue both systems read.

## 9. Field tests required before release (FastMile 1.2501.00.0798)

1. **AddObject** `Device.WiFi.SSID.` and `Device.WiFi.AccessPoint.` → instance numbers, default state, DeleteObject cleanup.
2. **Guest WLAN**: `Duration` units, `Expiry` behaviour, interaction with `X_ALU-COM_IsGuestSSID`.
3. **Security**: actual `ModesSupported` values; switching PSK ↔ Enterprise with RADIUS IP/port; whether a RADIUS shared-secret parameter exists.
4. **Firewall rule fields**: can a `Chain.{c}.Rule.{r}` match source MAC (per-client block)?
5. **Radio**: channel/width/power writes, auto-channel; read-back and whether a radio restart drops the session.
6. **Propagation**: SSID change on the gateway reaches the Beacon (observe Beacon `DataElements`/client roaming).
7. **NWCC**: confirm NWCC is inactive (empty broker) and does not overwrite ACS changes.
8. **Client data freshness**: values change between Informs; `Hosts.Host` vs `AssociatedDevice` vs `STA` consistency.

## 10. Prerequisites in the ACS

- Redact secret-class parameters in cache/history/API (review S1) — mandatory before this page handles passphrases.
- Server-side dangerous-parameter denylist (§5).
- Replace instance-1 canonical WiFi constants (§7) — also fixes BSS `MODIFY_WIFI` and config templates on multi-SSID devices.
