# BSS & Customer Management Integration Guide

Status: Draft — reflects the actual implementation as of 2026-08-04, verified live against a running stack (not speculative).  
Audience: BSS/CRM integration teams (Salesforce Comm Cloud, Amdocs, Netcracker, custom operator CRM).  
Implementation: `backend/cmd/bssadapter`, `backend/internal/bss` — see `tr069-acs-build-plan.md` §5 for the design rationale and open items.

---

## 0. Implementation status

Read this section first — it tells you what you can integrate against today versus what's planned.

| Capability | Status |
|---|---|
| Account-device mapping (Workflow A) | **Live** |
| Order dispatch — `MODIFY_WIFI` | **Live** |
| Order dispatch — `SUSPEND` / `ACTIVATE` | **Not implemented.** Returns `400 ErrInvalidRequest`. See §2.2. |
| Job status polling (Workflow C) | **Live** |
| Idempotent order retry | **Live** |
| Webhook notifications (§4) | **Not implemented yet.** Do not build against this section — it documents the target design, not a working endpoint. |
| Authentication | **Live**: OAuth2 client-credentials (recommended, per-integration) + optional mTLS. Legacy shared token still accepted but deprecated. See §3. |

---

## 1. Architectural Principles

1. **Decoupled Identity**: the ACS platform does not track customer names or billing details. It operates entirely on `Device UUID` and `OUI+SerialNumber`. The BSS maintains the relationship between the `Customer Account ID` and the physical router — the adapter's mapping endpoint is what records that link on the ACS side, and it verifies the device actually exists before accepting it.
2. **Asynchronous Execution (Job-Based)**: router configuration is not synchronous. Routers operate in remote environments and may be powered off or behind cellular NAT. All write endpoints return `202 Accepted` with a `command_key` you use to poll for completion.
3. **Idempotent by `external_order_id`**: retrying the same order (same `external_order_id`) is always safe — you get back the original `command_key` and its *current* status, never a second job.

---

## 2. Integration Workflows

### Workflow A: Device Onboarding & Binding

```http
POST /bss/v1/mappings
Authorization: Bearer <token>
Content-Type: application/json

{
  "account_id": "ACC-88203",
  "oui_serial": "001349+NR5103+S230Q12345678",
  "service_plan": "5G_HOME_ULTRA"
}
```

`oui_serial` must match a device already known to the ACS (it will have reported in via at least one TR-069 `Inform`) — the adapter resolves and validates it against the device registry rather than trusting a caller-supplied device ID outright. If you already know the ACS `device_uuid`, you may include it; it will be cross-checked against what `oui_serial` resolves to, and rejected if they don't match.

Captured response (`200 OK`):

```json
{
  "account_id": "ACC-88203",
  "device_uuid": "65ee0038-6583-4075-b54b-246655b0fd90",
  "oui_serial": "001349+NR5103+S230Q12345678",
  "service_plan": "5G_HOME_ULTRA",
  "status": "ACTIVE"
}
```

Unknown `oui_serial` (`404`):

```json
{
  "error": "ErrDeviceNotMapped",
  "message": "no device found for oui_serial: nonexistent-serial"
}
```

List an account's mapped devices:

```http
GET /bss/v1/mappings/{account_id}
Authorization: Bearer <token>
```

---

### Workflow B: Modifying Router Settings

#### 1. BSS Request

```http
POST /bss/v1/orders
Authorization: Bearer <token>
Content-Type: application/json

{
  "external_order_id": "ORD-2026-0804-13",
  "account_id": "ACC-88203",
  "service_type": "INTERNET_SERVICE",
  "action": "MODIFY_WIFI",
  "parameters": {
    "wifi_ssid": "Verify_Test_SSID",
    "wifi_password": "AnotherSecret456"
  }
}
```

`action` must currently be `MODIFY_WIFI`. `parameters` needs at least one of `wifi_ssid` / `wifi_password` — either alone is fine (only the fields you send get written).

#### 2. ACS Immediate Response — captured, `202 Accepted`

```json
{
  "order_tracking_id": "ORD-2026-0804-13",
  "command_key": "setparam_20260804_aa92ae2f",
  "status": "QUEUED",
  "timestamp": "2026-08-04T12:56:42.7402618Z"
}
```

#### 3. Execution Lifecycle

* The order is translated into TR-069 parameter writes and queued as a job in PostgreSQL.
* The router applies them the next time it opens a TR-069 session (either on its normal periodic check-in, or sooner once Connection Request wake-up is available — not yet implemented, see the build plan's Phase 3).
* The ACS confirms the write actually took by reading the parameters back from the device afterward — it does not just trust the router's acknowledgement.
* Job status becomes `SUCCESS` (or `FAILED` with a fault code/message, e.g. if the router rejected a parameter).

#### 4. Retrying an order is safe

Resubmitting the exact same `external_order_id` — captured response, same job, now reporting its real status instead of a stale `QUEUED`:

```json
{
  "order_tracking_id": "ORD-2026-0804-13",
  "command_key": "setparam_20260804_aa92ae2f",
  "status": "SUCCESS",
  "timestamp": "2026-08-04T12:57:05.4480256Z"
}
```

### 2.2 `SUSPEND` / `ACTIVATE` — not yet available

Captured response for `"action": "SUSPEND"` (`400`):

```json
{
  "error": "ErrInvalidRequest",
  "message": "unsupported or not-yet-implemented BSS action: \"SUSPEND\" needs a walled-garden design decision before it can be implemented safely"
}
```

This is deliberate, not an oversight: a naive suspend implementation (disabling the router's WAN interface) would cut off the same connection TR-069 uses to reach the device, risking the router becoming unreachable for its own follow-up `ACTIVATE` order. A safe mechanism (firewall/walled-garden redirect rather than interface disable) needs a per-vendor design decision first. If your rollout needs suspend/reactivate, flag it — this is the next thing to design, not build blind.

---

### Workflow C: Querying Job Status

```http
GET /bss/v1/jobs/{command_key}
Authorization: Bearer <token>
```

Captured response:

```json
{
  "command_key": "setparam_20260804_aa92ae2f",
  "device_id": "65ee0038-6583-4075-b54b-246655b0fd90",
  "type": "SET_PARAMETER",
  "status": "SUCCESS",
  "created_at": "2026-08-04T14:56:42+02:00",
  "updated_at": "2026-08-04T14:56:55+02:00",
  "completed_at": "2026-08-04T14:56:55+02:00"
}
```

`status` is one of `QUEUED`, `RPC_SENT`, `SUCCESS`, `FAILED`. On `FAILED`, `fault_code`/`fault_string` are populated with the CWMP fault the router returned.

Unknown `command_key` → `404 ErrJobNotFound`.

---

## 3. Authentication

Status 2026-08-06: **the planned production scheme is live.** Every
`/bss/v1/*` endpoint accepts either of two credential types.

### 3.1 OAuth2 client-credentials (recommended)

Register an integration through the ACS admin panel (BSS Integration →
Onboarding/Setup) to get a `client_id`/`client_secret` pair — shown once,
same as any other generated credential in this system. Exchange it for a
short-lived (1 hour) bearer token, per RFC 6749 §4.4:

```http
POST /bss/v1/oauth/token
Authorization: Basic base64(client_id:client_secret)
Content-Type: application/x-www-form-urlencoded

grant_type=client_credentials
```

(`client_id`/`client_secret` as form fields instead of Basic auth is also
accepted — some OAuth2 client libraries only support the body form.)

```json
{ "access_token": "eyJ...", "token_type": "Bearer", "expires_in": 3600 }
```

Use `access_token` as the bearer token on every subsequent `/bss/v1/*`
call, and request a new one when it expires. Revoking a client (admin
panel) blocks new token requests immediately; already-issued tokens
remain valid until their own expiry (max 1 hour).

### 3.2 Legacy shared token (deprecated, still accepted)

```http
Authorization: Bearer <shared token>
```

One static token for every integration, configured server-side —
kept working for backward compatibility during migration, but not
per-integration and not rotatable without a restart. New integrations
should use 3.1; existing ones should migrate off this before go-live.

Either credential type, missing or invalid → `401`:

```json
{ "error": "ErrUnauthorized", "message": "missing or invalid Authorization header" }
```

### 3.3 mTLS (optional, additive)

The adapter can also require TLS and verify client certificates
(`ACS_BSS_TLS_CERT`/`ACS_BSS_TLS_KEY`/`ACS_BSS_MTLS_CA_CERT`) — same
posture as `cmd/acs`'s CWMP mTLS: a cert is requested but not required,
and if one *is* presented it must chain to the configured CA or the TLS
handshake itself fails. This is a transport-layer hardening layer on top
of 3.1/3.2, not a replacement — a valid client cert alone does not
satisfy the bearer-token check above.

### 3.4 VPN-to-BSS: deferred

A dedicated VPN tunnel between the ACS and a specific BSS/CRM system
(rather than per-request mTLS) was considered and deliberately deferred
— see the admin-platform backlog memory for the reasoning. mTLS + OAuth2
is the baseline; a VPN option may be added later for partners who want
it, but pushes real setup cost onto that partner's network team and
isn't needed for standard internet-facing B2B integrations.

---

## 4. Webhook Notifications — planned, not implemented

The original design intent (still the target) is push-based job completion callbacks instead of polling Workflow C:

```json
{
  "event": "JOB_COMPLETED",
  "external_order_id": "ORD-2026-0804-12",
  "command_key": "setparam_20260804_00921",
  "status": "SUCCESS",
  "completed_at": "2026-08-04T14:30:08Z"
}
```

There is no subscription endpoint yet and no delivery worker running. **Poll Workflow C for now.** This section will be updated with real request/response examples once it's built.

---

## 5. Error Reference

Every error response has the shape:

```json
{ "error": "<code>", "message": "<human-readable detail>" }
```

| HTTP Code | Error Code | Meaning |
| :--- | :--- | :--- |
| `400` | `ErrInvalidRequest` | Missing required field, or an action that isn't supported yet (`SUSPEND`/`ACTIVATE`), or a `device_uuid` that doesn't match the resolved `oui_serial` |
| `401` | `ErrUnauthorized` | Missing/incorrect bearer token |
| `404` | `ErrDeviceNotMapped` | `oui_serial` doesn't match a known device, or the account has no active device mapping |
| `404` | `ErrJobNotFound` | No job exists for that `command_key` |
| `502` | `ErrACSUnreachable` | The internal ACS engine didn't respond |
| `500` | `ErrInternal` | Unexpected server-side failure — worth reporting if you see this |

---

## 6. Known limitations to plan around

- **One primary device per account.** Order dispatch resolves the account's most recently active mapping. An account genuinely managing multiple devices needs a different order shape (not yet designed) to name which device.
- **Idempotency is best-effort under a rare failure window.** If the ACS successfully queues a job but then fails to persist the order's idempotency record (a narrow DB-write failure right after success), a retry with the same `external_order_id` could dispatch a second job. This is logged loudly on the ACS side when it happens; a fully exactly-once guarantee would need an outbox pattern, which is a future hardening item, not current behavior.
- **No rate limiting yet** on `/bss/v1/*`. Under active design — see the build plan for the planned approach before assuming unlimited throughput is safe to rely on.
