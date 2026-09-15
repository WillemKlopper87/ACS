# Real CPE field-validation runbook

This is the execution procedure for qualifying physical TR-069/CWMP and
TR-369/USP devices against ACS. It complements
[`COMPATIBILITY.md`](COMPATIBILITY.md), which remains the authoritative
matrix of supported vendor/model/firmware combinations.

A green CI run, a simulator, or a BBF reference agent is **not** a real-device
qualification. A device becomes supported only after the exact firmware is
run through this procedure and the resulting evidence is recorded.

## 1. Scope and safety

Use a dedicated test device or a device that can be safely recovered. Do not
perform FactoryReset or firmware Download against production/customer CPE.
Firmware testing requires an approved image, known hardware compatibility,
local recovery access, and a rollback image/procedure.

Retained evidence must never contain plaintext passwords, bearer tokens,
HTTP Authorization values, private keys, Wi-Fi PSKs, SIP credentials, or
other subscriber secrets. Use the ACS capture export only after checking its
redaction, and sanitize any supplementary packet/log capture before storing
it in Git.

For a plaintext USP lab/field run, isolate the management network and make the
exception explicit when running preflight. Production-facing USP requires TLS.

## 2. Pin the release under test

Deploy from `main` or a release tag. Record the exact commit before touching
the device:

```bash
git rev-parse HEAD
git status --short
```

The worktree should be clean. Run the deployment preflight:

```bash
# Required only when this controlled lab intentionally uses plaintext USP.
export ACS_FIELD_TEST_ACK=1
./scripts/field-preflight.sh
```

Preflight must pass before changing the CPE's management-server/controller
configuration. Any warnings become part of the test record.

## 3. Create the field evidence record

Use one record per **model + hardware revision + firmware**. Recommended ID:
`YYYYMMDD-vendor-model-firmware`.

Record this header before the first session:

```text
Test ID:
Date/time (UTC):
Operator:
ACS commit/tag:
Environment / host:
Device vendor:
Model / product class:
OUI:
Serial (masked or hashed in retained evidence):
Hardware revision:
Firmware/software version:
Protocols under test: CWMP / USP / both
Network path: direct / NAT / APN / VPN / lab LAN
CPE management IP (sanitize if required):
Compatibility knobs enabled:
Expected data-model root:
Rollback/recovery method:
```

Do not put secrets in this record.

## 4. Capture first

Start capture **before** the onboarding/reconnect that is being qualified.
For an already-known device, use Device Detail → Capture and select `CWMP` or
`USP`. For an unknown device, use the fleet capture browser with a known
identity where possible. Remote-IP capture is global-only and should be a
last resort on shared NAT.

The equivalent operator API surfaces are:

- `POST /api/v1/devices/{deviceId}/captures` — device-backed capture;
- `POST /api/v1/captures` — identity/remote-IP capture;
- `GET /api/v1/captures/{id}/events` — ordered transcript;
- `POST /api/v1/captures/{id}/stop` — stop;
- `GET /api/v1/captures/{id}/export` — sanitized export.

Record every capture ID. A field result without a usable protocol transcript
is incomplete when the failure or vendor-specific behavior cannot otherwise
be reproduced.

## 5. CWMP qualification sequence

Run the sequence in order. Stop on an unexpected destructive behavior rather
than compensating with a fleet-wide compatibility weakening.

| Step | Operation | Pass evidence |
|---|---|---|
| 1 | Configure `ManagementServer.URL` and credentials, then reconnect/reboot the test CPE as appropriate. | Initial Inform is accepted; device identity maps to one intended ACS device; capture shows the challenge/auth/session exchange. |
| 2 | Observe BOOTSTRAP/BOOT/PERIODIC behavior. | Event codes, CWMP namespace, `cwmp:ID`, session close and repeat Inform behavior are coherent. |
| 3 | Trigger **Discover parameter model**. | Root is correctly identified as `Device.` (TR-181) or `InternetGatewayDevice.` (TR-098); `GetParameterNames` completes or a precise fault is retained. |
| 4 | Run representative live GPV reads. | Standard identity/version/status paths and at least one vendor-specific path, if present, return with correct types/values. |
| 5 | Choose one reversible writable parameter and perform SPV. | Job completes, the value changes, and a subsequent live GPV reads the same value back. Restore the original value before continuing. |
| 6 | Trigger an HTTP Connection Request. | ACS receives an accepted 2xx from the CPE and the expected Connection Request/Event 6 Inform follows; capture records the actual Digest/Basic challenge behavior. |
| 7 | If behind NAT, validate the required STUN/Annex G path. | Learned address/NAT state is correct and a UDP connection request works when the deployment requires it. Do not mark Annex G supported for this firmware without this step. |
| 8 | Run IPPing and/or TraceRoute diagnostics if the CPE exposes them. | Trigger, polling and terminal result are visible in the device job history. |
| 9 | Queue Reboot on the dedicated test unit. | CPE reboots, reconnects, sends the expected post-boot Inform, and the ACS device identity remains stable. |
| 10 | Exercise Upload/Download only when explicitly approved for this model. | Signed transfer works, completion/fault is correlated to the job, and firmware version is confirmed after an approved upgrade. |
| 11 | Interrupt connectivity or restart ACS during a non-destructive session/job. | CPE retries/reconnects and ACS lease/retry behavior does not duplicate a harmful action. |

For every queued operation, record its **command key** and final state/fault.
Do not accept a green UI toast as sufficient evidence; verify the device
state or read-back result.

## 6. USP qualification sequence

A real USP device must be authorized/pre-registered according to the deployed
allowlist policy before testing. The obuspa CI suite proves controller/reference
agent interoperability; this section proves the behavior of the physical CPE.

| Step | Operation | Pass evidence |
|---|---|---|
| 1 | Configure the CPE's controller endpoint/transport. | WebSocket and/or MQTT connection establishes to the intended stable `ACS_USP_CONTROLLER_ID`. |
| 2 | Reconcile endpoint identity. | The USP endpoint maps to the intended ACS device/resource; no duplicate device is created for the same physical unit where identity rules support correlation. |
| 3 | Run Get against representative standard paths. | GetResp values/types are recorded and match device state. |
| 4 | Run one reversible Set and read it back. | Operation succeeds, Get confirms the new value, then the original value is restored. |
| 5 | Run a safe Operate action where supported. | Request/response and final job state are correlated. |
| 6 | Validate desired subscriptions. | Required ValueChange/Event/ObjectCreation/ObjectDeletion/OperationComplete notifications are received where the device supports them. |
| 7 | Disconnect/reconnect or restart the controller. | Session reconnects and desired subscription reconciliation survives/re-establishes correctly. |
| 8 | Stop/export the USP capture. | Both directions are present in order and sensitive content is redacted. |

Record whether the qualified transport was WebSocket, MQTT 5, MQTT 3.1.1, or
a subset. Do not infer support for an untested transport from another one.

## 7. Dual-protocol and multi-device cases

When a physical CPE exposes both CWMP and USP, verify that both protocol
identities converge on the intended inventory resource where the identity
model supports it. Record any case where the same hardware creates two
resources.

For gateway + ONT/extender/STB/ATA arrangements, verify assignment roles and
resource relationships individually. For a replacement/swap, verify that
historical mapping remains intact while the active assignment moves to the
new unit. For devices sharing one NAT address, prove capture events do not
cross-contaminate device-backed sessions.

## 8. TMF/OSS verification

The TMF check confirms that device execution is reflected northbound rather
than proving the protocol itself.

1. After onboarding, verify the device appears in **TMF639 Resource Inventory**
   (`/tmf-api/resourceInventoryManagement/v5/resource`) with safe operational
   characteristics and without credentials/connection-request secrets.
2. If the test account has an operational service mapping, verify **TMF638**
   (`/tmf-api/serviceInventoryManagement/v4/service`) links service state to
   the expected resource. Absence is not a device failure when no service
   intent/mapping was provisioned.
3. Where a controlled order test is in scope, submit a reversible **TMF641**
   service order and confirm it drives the existing **TMF640** activation/job
   path rather than a parallel execution engine.
4. Trigger or observe an intentional qualifying condition and verify the
   applicable **TMF688 event**, **TMF642 alarm**, and **TMF656 service problem**
   lifecycle. Do not manufacture service-impact evidence merely to make all
   surfaces non-empty; mark a surface `N/A` when the scenario does not apply.
5. Record correlation/external IDs so the northbound request can be traced to
   the ACS job and protocol exchange.

OAuth/account entitlements used for northbound testing must be the narrowest
scope required for the test account.

## 9. Capture and redaction acceptance

Before retaining an export, inspect it for:

- chronological inbound/outbound ordering;
- correct protocol and device/correlation identity;
- SOAP/USP operation/fault summaries needed to reproduce the behavior;
- no passwords, Authorization headers, bearer/client secrets, Wi-Fi PSKs,
  SIP credentials or configured sensitive values;
- correct stop/expiry behavior.

If redaction fails, treat that as a **blocking defect**. Do not attach the
unsanitized artifact to GitHub or another shared evidence store.

## 10. Result classification

Each test step is one of:

- **PASS** — expected behavior was observed and retained evidence supports it;
- **FAIL** — the device/ACS behavior is reproducibly incorrect or unsafe;
- **BLOCKED** — prerequisite/environment/device capability prevented a valid
  test; this is not a pass;
- **N/A** — the capability is intentionally not applicable to this device or
  test scenario, with the reason recorded.

A firmware can be marked `Real device: qualified` in `COMPATIBILITY.md` only
when mandatory onboarding, identity, discovery, representative GPV, reversible
SPV/read-back, reconnection/recovery and the management transport actually
used in production have passed. Optional capabilities may remain explicitly
`N/A` or documented limitations.

## 11. Evidence table

Complete this per device/firmware and retain it with the test notes or linked
issue:

| Test | Result | Capture ID | Command/correlation key | Observed result / fault | Evidence path or issue |
|---|---|---|---|---|---|
| Preflight / deployed commit |  |  |  |  |  |
| Initial Inform / USP connect |  |  |  |  |  |
| BOOTSTRAP / BOOT / PERIODIC |  |  |  |  |  |
| GPN / object discovery |  |  |  |  |  |
| Representative GPV/Get |  |  |  |  |  |
| Reversible SPV/Set + read-back |  |  |  |  |  |
| Connection Request / reconnect |  |  |  |  |  |
| STUN / Annex G (if applicable) |  |  |  |  |  |
| Diagnostics |  |  |  |  |  |
| Reboot / recovery |  |  |  |  |  |
| Upload/Download (if approved) |  |  |  |  |  |
| Capture redaction/export |  |  |  |  |  |
| TMF639 resource projection |  |  |  |  |  |
| TMF638/640/641 flow (if in scope) |  |  |  |  |  |
| TMF688/642/656 assurance (if in scope) |  |  |  |  |  |

Also record observed latency for Inform/session establishment, queued job
completion, connection-request-to-Inform, and reconnect after reboot/network
loss. These are observations, not universal SLAs, until enough field samples
exist.

## 12. Vendor-profile output

For a poorly documented CPE, the qualification deliverable is more than a
PASS/FAIL result. Record:

- manufacturer/OUI/product class/model/hardware revision/firmware;
- actual management protocol(s) and data-model root;
- discovered standard and vendor-specific object/parameter paths;
- writable/non-writable behavior of important parameters;
- vendor path → canonical ACS concept / known BBF-standard equivalent;
- mapping source (`observed`, `manual`, `heuristic`), confidence and approval
  state;
- every compatibility knob required and why;
- repeatability result on a second unit or a second clean session where
  practical.

If the device exposes a new valid SOAP namespace, HTTP-auth challenge,
compression form, fault shape or USP message shape, add a **sanitized** fixture
under `backend/test/fixtures/` and a regression test before declaring the
quirk supported.

## 13. Close-out

1. Stop the capture and retain only sanitized exports.
2. Restore every reversible parameter changed during the test.
3. Update the exact firmware row in `COMPATIBILITY.md`; never generalize one
   firmware result to an entire model family.
4. Create defects for every FAIL/BLOCKED item that requires ACS work, linking
   capture IDs, job keys and sanitized fixtures.
5. Record approved vendor mappings and their evidence/confidence.
6. Rerun `./scripts/field-preflight.sh` and `./scripts/healthcheck.sh` after any
   remediation/deployment change before repeating device tests.
7. Keep Goal #48 open until at least the planned known and poorly documented
   real-device qualification cycle has completed successfully.
