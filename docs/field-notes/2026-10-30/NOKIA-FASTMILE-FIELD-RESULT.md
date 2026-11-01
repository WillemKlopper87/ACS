# Nokia FastMile 5G Gateway 2: ACS field result, 2026-10-30

**Result: read-only qualification PASSED** (`ci/compatibility/nokia-read-only.json`)
Scope: scenario evidence only. This is not full firmware qualification (see "Not yet done").

## Setup

| | |
|---|---|
| ACS build | branch `fix/cwmp-cpe-initiated-rpcs` @ `4204c38` (on top of `main` @ `8ed5d7f`) |
| Host | AWS EC2 t3.medium, Ubuntu 24.04, af-south-1, `scripts/quickstart.sh` lab profile, Go 1.26.10 |
| Transport | CWMP over plain HTTP :7547, Digest MD5 qop=auth, cwmp-1-0 |
| CPE | Nokia FastMile 5G Gateway 2: OUI `A4FC77`, ProductClass `3TG02241`, serial `CSC25430018F`, HW `3TG02241AAAA`, SW `1.2501.00.0798`, `RootDataModelVersion` 2.12 (TR-181) |
| Also onboarded | Nokia Beacon 3.1 mesh unit: OUI `F89B6E`, serial `ALCLB499A341`, manufacturer `ALCL`. The FastMile passed its ACS settings to it. |
| Network | 5G NR band n78, NR-ARFCN 632640, behind carrier CGNAT (WAN `10.10.178.14`, public egress `66.18.65.65`) |

## Evidence chain

| Stage | Result |
|---|---|
| First contact with constrained bootstrap Digest identity | PASS |
| Credential install SPV (`ManagementServer.Username/Password`) | PASS after fix (see F1) |
| Reconnect with unique device-bound Digest, PENDING → ACTIVE | PASS: FastMile 09:08:22Z, Beacon 09:08:55Z |
| Bootstrap identity no longer used | PASS (device-bound credential in use from then on) |
| Parameter discovery | PASS after fixes (F2, F3): **2,878 parameters, 804 writable, 4 objects skipped**. Beacon: 101 / 46 / 0. |
| Identity check (HW/SW vs scenario) | PASS, no mismatches |
| Read-only 5G/cellular reads (discovered paths only) | PASS: e.g. `X_ALU-COM_Attached5GCellStat` Band n78, RSRP −92 dBm, RSRQ −11 dB, RSSI −80 dBm, PCI 1 |
| Connection Request | Not reachable (CGNAT), as expected. Periodic Inform (60 s) used instead. |

## Firmware findings (FastMile 1.2501.00.0798)

- **F1. ParameterKey limited to 32 characters.** An SPV with a longer ParameterKey is rejected with **9003 Invalid arguments** (TR-069 defines it as string(32)). ACS sent a 51-character `cwmp-bootstrap-<uuid>` key; fixed in `64f6380`. The device now reports `ManagementServer.ParameterKey` = `cwmp-bootstrap-cb9ed82a-ba0f-466` (the clamped 32 characters).
- **F2. Drops the CWMP session mid-burst on GetParameterNames.** No fault and no response; the TCP session simply ends. Which object it drops on **varies between runs**:
  - earlier runs: `DynamicDNS.Client.`, `Routing.Router.`, `NAT.InterfaceSetting.1.`, `Hosts.Host.22.IPv6Address.`, `FaultMgmt.SupportedAlarm.4.`, `InterfaceStack.28.`, `Cellular.Interface.1.Stats.X_ALU-COM_CellularTime.`
  - final run: `DeviceInfo.X_CT-COM_ServiceManage.`, `DynamicDNS.Server.2.`, `DeviceInfo.TemperatureStatus.TemperatureSensor.1.`, `WiFi.DataElements.AssociationEvent.AssociationEventData.1.`

  This points to agent instability under a long request burst (device memory was at 83%), not to specific broken objects. Fixed on the ACS side with skip-and-continue (`ea11cf6`). Pacing discovery per session is a possible improvement.
- **F3. Large runtime-state tables.** `Hosts.Host` (22+ rows), EasyMesh `WiFi.DataElements` event logs and neighbour scans, `FaultMgmt`, `InterfaceStack` and `VendorLogFile` pushed discovery past its 512-object budget. Fixed by walking one instance of runtime tables and capping any table at 32 instances (`7cfdfd0`, `4204c38`).
- **F4. The vendor extension prefix is `X_ALU-COM_`, not `X_NOKIA_`.** The static catalog `backend/internal/devices/adapters/catalogs/nokia.xml` guesses `Device.X_NOKIA_5G_Diagnostics.*` and `X_NOKIA_COM_Cellular.*`, neither of which exists (reads fault **9005**). The real 5G statistics are under `Device.Cellular.Interface.1.X_ALU-COM_Attached5GCellStat.*` and `…X_ALU-COM_AttachedLteCellStat.*`. LTE values read `-32768` / `4294967295` when not attached to an LTE cell.
- **F5. The mesh inherits the gateway's ACS settings.** The Beacon 3.1 started TR-069 with the same bootstrap credentials. If the FastMile re-pushes its saved settings later, the Beacon could fall back to bootstrap, which ACS rejects for established devices. Watch this.

## ACS defects found and fixed during the test

| Commit | Fix |
|---|---|
| `e301f17` | Answer CPE-initiated GetRPCMethods / unknown methods (Fault 8000) |
| `886313b` | Go 1.26.10 and dependency bumps; govulncheck 13 → 0 reachable |
| `64f6380` | Bootstrap ParameterKey ≤ 32 characters; log bootstrap auth failures and install faults |
| `ea11cf6` | Discovery skips objects the CPE drops/faults on instead of stalling |
| `7cfdfd0` | Collapse runtime tables; one active discovery per device |
| `b754492` | Console onboarding panel recognises `DEVICE2` / `IGD1` roots |
| `4204c38` | Collapse scan/alarm/stack tables; cap 32 instances per table |

Also observed, not fixed: the `cmd/api` retention job fails every cycle with `prune password_reset_tokens: column "id" does not exist`.

## Not yet done (per `docs/NOKIA-CLOUD-QUALIFICATION.md` §9–§12)

- Reversible write test: read, SPV a harmless discovered writable value, read back, restore.
- Reboot and reconnect with independent observation.
- Connection Request / Event 6. Blocked by CGNAT; STUN/Annex G not tested.
- TLS / production profile (Pass B).
- Correct `nokia.xml` from the discovery export (F4) and add the `docs/COMPATIBILITY.md` row.
