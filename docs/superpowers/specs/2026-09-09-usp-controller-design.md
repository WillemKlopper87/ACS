# TR-369/USP Controller — Design

**Date:** 2026-09-09
**Status:** Design approved; implementation plan not yet written
**Sub-project:** B (see *Programme context* below)

## 1. Purpose and driver

Add TR-369/USP support to ACS so the platform is a dual-stack CWMP + USP
device-management system.

The driver is **strategic/procurement parity**. TR-369 is the defining gap in
the market comparison: every commercial platform (AVSystem, Axiros, Friendly,
Nokia) now ships dual-stack TR-069 and TR-369 in a single installation. Success
is therefore **demonstrable, genuinely interoperable USP capability** — proven
against a real USP agent, not a mock we wrote ourselves. It is explicitly *not*
broker high availability, USP Services, or fleet-scale USP deployment.

Two decisions were taken during design and are settled inputs here:

- **Both MTPs from the start** — WebSocket and MQTT, behind a pluggable
  interface.
- **The full message set including Notify/subscriptions** — the headline USP
  capability, and the basis for live telemetry.

## 2. Programme context

This design covers sub-project **B** only. The wider programme:

| | Sub-project | Depends on |
|---|---|---|
| 0 | Defect fixes (cellular paths, Huawei passphrase, webhook replay signing) | — |
| A | Fleet data model (`service`, temporal `device_assignment`, role, quarantine) | — |
| **B** | **USP controller (this document)** | **A** |
| C | BSS improvements (outbox, action set, TMF640 shapes, DLQ) | A |

Sequencing is `0 → A → (B ∥ C)`. **B depends on A**: USP is designed for
multi-device homes (gateway + ONT + extenders), and a flat `account → device`
model cannot represent that. A must land first.

## 3. Verified protocol facts

Everything in this section was verified against primary sources during design,
not taken from secondary summaries. It is recorded here because several points
contradict common assumptions.

### 3.1 Specification and schemas

- Current USP release is **1.5 (January 2026)**; releases run 1.0 (2018), 1.1,
  1.2, 1.3, 1.4, 1.5.
- The protobuf schemas (`usp-record-1-*.proto`, `usp-msg-1-*.proto`, all
  versions 1.0–1.5) are published in the Broadband Forum `usp` repository under
  **BSD-3-Clause**, which permits vendoring.
- They are small: `usp-record-1-3.proto` is 131 lines, `usp-msg-1-3.proto` is
  594. The codec is a `protoc` invocation plus mapping code, not a hand-written
  parser. `google.golang.org/protobuf` is already an indirect dependency.

### 3.2 Record and message structure

`Record` carries `version`, `to_id`, `from_id`, a payload-security mode
(`PLAINTEXT`), optional `mac_signature` and `sender_cert`. Payload variants
include `NoSessionContextRecord`, `SessionContextRecord` (segmentation),
per-MTP connect records (`WebSocketConnectRecord`, `MQTTConnectRecord`,
`STOMPConnectRecord`, `UDSConnectRecord`) and `DisconnectRecord`.

The message set is `Get`, `GetSupportedDM`, `GetInstances`, `Set`, `Add`,
`Delete`, `Operate`, `Notify`, `GetSupportedProtocol`, `Register`, `Deregister`.

### 3.3 Facts that shape this design

- **`Notify.OnBoardRequest` carries `oui`, `product_class`, `serial_number`**
  and the agent's supported protocol versions. This is the protocol's native
  onboarding signal and supplies exactly the identity triple the device registry
  keys on.
- **`Notify.OperationComplete` carries a `command_key`** — the same correlation
  field `jobs.ByCommandKey` already uses for CWMP `TransferComplete`.
- **`GetSupportedDM` returns per-parameter `ParamAccessType`**
  (`PARAM_READ_ONLY` / `PARAM_READ_WRITE` / `PARAM_WRITE_ONLY`),
  `ParamValueType`, and per-command `CmdType` (`CMD_SYNC` / `CMD_ASYNC`).
  Writability and synchronicity are therefore discoverable, not inferred.
- **USP error codes are 7000–7026**, including `7006` permission denied,
  `7013` attempt to update non-writeable parameter, `7016` object does not
  exist, `7022` command failure.
- **USP mandates TR-181 Device:2**, so the TR-098 root ambiguity does not apply
  to USP agents.

### 3.4 Interop target: obuspa

The Broadband Forum reference agent (`obuspa`, current line 11.0.7) is the
acceptance gate. Verified behaviours that the controller must accommodate:

- It emits `Record.version = "1.3"` and advertises
  `agent_supported_protocol_versions = "1.0,1.1,1.2,1.3"` **despite release
  notes headlining "USP 1.4"**. The controller must not require 1.4.
- **The access gate is the endpoint-ID allowlist, not TLS or roles.** A record
  is rejected unless `to_id` exactly matches the agent's EndpointID *and*
  `from_id` is a pre-provisioned controller. An unknown controller is refused
  outright, not demoted to an untrusted role.
- Plaintext (non-TLS) MTPs are fully supported and, by default, inherit
  full-access permissions — so CI needs no certificate setup.
- Default agent EndpointID is `os::<OUI>-<SerialNumber>`, percent-encoded. A
  database value or the `USP_ENDPOINT_ID` environment variable overrides it, so
  the derived form **cannot be relied on**.
- MQTT v5 uses the `Response Topic` property and content type `usp.msg`;
  MQTT v3.1.1 uses the `<topic>/reply-to=<%2F-escaped-topic>` convention. Both
  must be implemented.
- Its committed conformance results provide a 436-row TP-469 interop matrix to
  mirror as our test checklist.
- Its `-c` CLI (`get`, `set`, `perm`, `dump subscriptions`, …) is an
  out-of-band oracle for asserting device state independently of our controller.

## 4. Architecture

### 4.1 Service topology

USP is a **new service**, not an extension of `cmd/acs`. The CWMP gateway is a
request/response HTTP terminator; a USP controller holds many long-lived
connections and has a different lifecycle, failure mode and scaling profile.

| Unit | Role |
|---|---|
| `cmd/uspc` | USP Controller service. Terminates MTPs, owns connection state, dispatches jobs, ingests Notify. Wiring only. |
| `internal/usp` | Protocol core, transport-free: Record/Message codec, Endpoint ID handling, correlation, error mapping. |
| `internal/usp/mtp` | Transport abstraction plus WebSocket and MQTT implementations. |
| `internal/usp/dm` | TR-181 path semantics shared with CWMP: search expressions, instance addressing. |
| `internal/subscriptions` | Durable subscription lifecycle and Notify routing. Protocol-agnostic, so CWMP value-change events can later feed the same path. |

**Dependency rule:** `internal/usp` never imports `devices`, `jobs` or `store`.
It speaks protocol only. `cmd/uspc` binds protocol to domain. This keeps the
codec independently testable and avoids repeating the 1,200-line `main.go`
problem recorded in `HANDOFF.md`.

### 4.2 What is reused unchanged

The job queue and its lease/reaper machinery, device registry, parameter cache,
templates, policies, rollouts, scheduler, tenancy, RBAC, audit and
observability. **This reuse is the entire value of the approach**: USP becomes a
second way to reach a device, not a second platform. Every feature above the job
queue works over USP without modification.

## 5. Device identity and registry

### 5.1 Principle

Identity is **OUI + ProductClass + SerialNumber** (consistent with sub-project
A). `endpoint_id` is a *routing address*, not an identity. A dual-stack device
resolves to exactly one device record.

### 5.2 Schema

Migration `NNNN_usp_agents.sql` — the number is assigned at implementation
time, since sub-project A adds migrations first:

```
  devices.management_protocols  TEXT[]        -- {'CWMP'}, {'USP'}, or both
  usp_agents
    device_id      FK devices(id) UNIQUE
    endpoint_id    TEXT UNIQUE                -- routing address
    mtp_kind       TEXT                       -- WEBSOCKET | MQTT
    connected      BOOL
    last_connected_at, last_seen_at
    supported_protocol_versions TEXT[]
    controller_role TEXT
```

### 5.3 Reconciliation on first contact

In priority order:

1. **`Notify.OnBoardRequest`** — primary path. Supplies OUI, ProductClass and
   SerialNumber directly. Match-or-create against the device registry.
2. **Endpoint ID parse** — optimisation for an agent already known, when the ID
   follows the `os::<OUI>-<Serial>` convention.
3. **`Get` on `Device.DeviceInfo.`** — last resort, for an agent that connects
   without ever having sent an OnBoardRequest.

A device record **must never be created from an Endpoint ID alone**; opaque
forms (`proto::`, `self::`) and configured overrides make it unreliable, and
doing so produces a duplicate fleet.

`data_model_root` is always `DEVICE2` for USP agents.

### 5.4 Two integration details

**Liveness.** The existing liveness reaper infers state from missed Informs. For
USP that inference is wrong — MTP connection state is authoritative and
immediate. **The reaper must skip USP-reachable devices**, or it will mark a
connected agent `UNREACHABLE` because it never Informs. This is a regression
risk against a fix that only recently landed.

**Tenancy.** `usp_agents` deliberately carries no `customer_id`. Scoping flows
through `device_id` to the device's customer, so USP inherits the centrally
enforced tenancy guard and adds no new scoping surface.

## 6. Dispatch

### 6.1 Push, not pull

CWMP dispatch waits for an Inform, then calls `Lease(deviceID)`. USP agents are
already connected, so `cmd/uspc` dispatches as soon as a job is queued.

- Trigger: Postgres `LISTEN/NOTIFY` on job insert (the project already uses
  pgx), with a periodic sweep as the safety net for missed notifications.
- A connection registry maps `endpoint_id → live MTP connection`. Single
  instance initially, matching the platform's existing posture; the registry is
  the seam for later HA.
- Lease, timeout and reaper semantics are unchanged from CWMP.

This yields instant device actions without Annex G or STUN — the capability the
CGNAT fleet currently lacks.

### 6.2 Job type mapping

| Job type | USP message |
|---|---|
| `GET_PARAMETER` | `Get` |
| `SET_PARAMETER` | `Set` |
| `ADD_OBJECT` / `DELETE_OBJECT` | `Add` / `Delete` |
| `REBOOT` / `FACTORY_RESET` | `Operate` on the corresponding TR-181 command |
| `FIRMWARE_DOWNLOAD` | `Operate` on the firmware-image Download command |
| `DIAGNOSTICS_PING` / `DIAGNOSTICS_TRACEROUTE` | `Operate` on the diagnostics command |
| `PARAMETER_DISCOVERY` | `GetSupportedDM` |

### 6.3 Synchronous and asynchronous operations

`GetSupportedDM` reports each command as `CMD_SYNC` or `CMD_ASYNC`, and the
result is cached per device.

- **Sync** — completes in the `OperateResp`; the job is resolved inline.
- **Async** — the agent acknowledges, then later sends
  `Notify.OperationComplete` carrying `command_key`. The controller resolves the
  job via the existing `jobs.ByCommandKey` lookup.

No new correlation concept is introduced: this is the same path CWMP
`TransferComplete` already uses.

### 6.4 Error mapping

USP `7xxx` codes map onto existing job failure states. Codes warranting specific
handling:

| Code | Meaning | Handling |
|---|---|---|
| `7013` | Non-writeable parameter | Fail with a distinct reason; surface in UI. This is the Huawei-class trap, typed rather than silent on USP. |
| `7016` | Object does not exist | Fail; trigger re-discovery. |
| `7006` | Permission denied | Fail; indicates a controller-trust misconfiguration, not a device fault. |
| `7022` | Command failure | Fail with the agent's `err_msg`. |

Unmapped codes fail the job with code and message recorded verbatim.

## 7. Subscriptions and Notify

### 7.1 Desired state

`usp_subscriptions` holds desired state per device: notification type, reference
path, persistence, and creating operator.

### 7.2 Reconciliation on every connect

An agent that rebooted or was factory-reset may have lost or drifted its
`Device.LocalAgent.Subscription.{i}` table. On every connect the controller:

1. reads the agent's actual subscriptions,
2. diffs against desired state,
3. issues `Add`/`Delete` to converge.

This is the same intent-versus-actual reconciliation loop that sub-project C
needs for provisioning, so **it is built once, generically**, in
`internal/subscriptions`.

### 7.3 Notify routing

| Notification | Handling |
|---|---|
| `ValueChange` | Parameter cache and change history (existing stores). |
| `ObjectCreation` / `ObjectDeletion` | Cache invalidation for the affected subtree. |
| `OperationComplete` | Job completion via `command_key`. |
| `Event` | Device event timeline. |
| `OnBoardRequest` | Registry reconciliation (§5.3). |

Notifications are **at-least-once**. Handlers must be idempotent, deduplicating
on `subscription_id` plus content. `NotifyResp` is sent when `send_resp` is set.

An unknown `subscription_id` is logged and ignored, and triggers a
reconciliation pass rather than an error.

## 8. Security and trust

- The controller presents a stable EndpointID that must be pre-provisioned in
  each agent's controller table. Every outbound record sets `to_id` to the
  agent's exact EndpointID.
- TLS is used for production MTPs (WSS, MQTT over TLS). Plaintext is permitted
  only for local CI against obuspa.
- Encrypted payloads and E2E session context are **not supported** — obuspa
  rejects them and the feature is experimental in the specification.
- Operator-facing authorisation is unchanged: USP actions are ordinary jobs and
  inherit existing RBAC and tenancy enforcement.

## 9. Testing and acceptance

**Acceptance gate: interop with a real agent.** Protocol code that has never met
a real agent is an intention, not a capability — this is the lesson of the
unvalidated Annex G implementation, and it must not repeat.

- **CI**: obuspa built from its `ci/Dockerfile`, run against Mosquitto over
  plaintext MQTT, with the controller's EndpointID pre-provisioned in the
  agent's factory-reset config. The agent database must be removed between runs
  or the factory-reset file is ignored.
- **Coverage**: the full message set, both MTPs, sync and async `Operate`,
  subscription reconciliation across an agent restart, and duplicate Notify
  handling.
- **Checklist**: mirror obuspa's committed TP-469 conformance test IDs.
- **Oracle**: obuspa's `-c` CLI asserts device-side state independently of our
  controller.
- **Unit tests**: `internal/usp` is transport-free and pure, so codec,
  correlation and error mapping are directly testable without a broker.

Results are recorded in `docs/COMPATIBILITY.md` alongside CWMP results.

## 10. Out of scope

| Excluded | Reason |
|---|---|
| CoAP MTP | Deprecated as a USP MTP; unmaintained and untested in obuspa. |
| STOMP MTP | Legacy; MQTT and WebSocket cover the certified device base. |
| E2E session context / encrypted payloads | Experimental in spec; rejected by obuspa. |
| USP Services, Broker, `Register`/`Deregister` | For on-device software modules, not ISP CPE management. |
| USP high availability / multi-instance | Matches the platform's current single-instance posture. |
| TR-369 for subscriber self-care | Separate product, not a controller concern. |

## 11. Risks

| Risk | Mitigation |
|---|---|
| No real USP hardware available for qualification | obuspa is the acceptance gate; hardware qualification follows the same per-firmware rule as CWMP. |
| Agent EndpointID overridden, breaking identity assumptions | `OnBoardRequest` is the primary identity path; Endpoint ID parsing is only an optimisation. |
| Liveness reaper marks connected USP agents unreachable | Explicit skip for USP-reachable devices, with a regression test. |
| Subscription drift after agent factory reset | Reconciliation on every connect (§7.2). |
| MQTT v3.1.1 vs v5 reply-topic divergence | Both conventions implemented and covered in CI. |
| Scope creep into USP Services/Broker | Explicitly out of scope (§10). |

## 12. Open questions

1. **MQTT broker: embedded or external?** An embedded Go broker (as Oktopus
   uses) avoids an infrastructure dependency and a new administrator ask; an
   external broker is better operationally at scale. Deferred to the
   implementation plan; the MTP abstraction makes it reversible.
2. **Controller EndpointID scheme.** Needs to be stable across restarts and
   unique per deployment. Proposal: derive from a configured deployment
   identifier rather than a hostname.
3. **Whether `internal/subscriptions` should absorb CWMP value-change events**
   in this sub-project or a later one. Designed to allow it; not scheduled here.
