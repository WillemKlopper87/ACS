# The integration model

Read this before writing production code. Almost every integration defect
we see comes from one of five assumptions in this document being wrong.

## 1. Nothing is synchronous

The ACS cannot reach a device on demand. Customer premises equipment sits
behind carrier NAT, sleeps, loses power, and opens a session to the ACS on
its own schedule. The ACS queues work and waits.

So every write returns `202 Accepted` with a `command_key`, and completion
arrives seconds to hours later.

```
BSS                    ACS                      Device
 │  POST /orders        │                          │
 ├─────────────────────▶│  queue job               │
 │◀── 202 + command_key │                          │
 │                      │                          │
 │                      │◀──── Inform (check-in) ──┤
 │                      ├──── SetParameterValues ─▶│
 │                      │◀──────── response ───────┤
 │                      ├──── read back to verify ▶│
 │                      │◀──────── values ─────────┤
 │◀── webhook: SUCCESS ─┤                          │
```

**Consequences for your design:**

- Never block a user-facing request on order completion.
- Never treat `202` as "done". It means "accepted and durably recorded".
- Model the order as pending in your own system until you observe a
  terminal status.
- Set no timeout shorter than your slowest realistic device check-in
  interval. If you must time out, mark the order *unknown*, not *failed* —
  and then poll, because it may still complete.

**`SUCCESS` is stronger than it looks.** The ACS reads the parameters back
from the device and confirms them before reporting success. It is not
merely the device's acknowledgement of the request.

## 2. Idempotency: `external_order_id` is the contract

`external_order_id` is yours to choose, must be unique per logical order,
and is the idempotency key.

Resubmitting an identical order returns the **original** job with its
**current** status. Not a duplicate, not a conflict, not an error.

| Situation | Do this |
|---|---|
| Request timed out, outcome unknown | **Resend the identical order.** |
| Connection dropped mid-request | **Resend the identical order.** |
| `5xx` from the ACS | Back off, then resend the identical order. |
| Genuinely a new change | New `external_order_id`. |
| Retrying a *failed* order after fixing the cause | New `external_order_id` — the original is terminal. |

**The anti-pattern that causes real incidents:** appending a suffix or a
timestamp to retry (`ORD-123-retry-2`). That is a *new* order, and it will
dispatch a *second* change to the device. Retry means byte-identical
resubmission.

Order intent is recorded durably **before** dispatch is attempted, and a
background reconciler retries failed dispatches with exponential backoff,
dead-lettering after repeated failure. Your retry and ours cooperate
rather than compounding.

## 3. Error classes — what to retry and what not to

| Status | Code | Class | Your action |
|---|---|---|---|
| `400` | `ErrInvalidRequest` | Permanent | Fix the request. Never retry unchanged. |
| `401` | `ErrUnauthorized` | Permanent | Refresh the token once; if it recurs, stop and alert. |
| `404` | `ErrDeviceNotMapped` | Permanent | The device is unknown or the account has no active mapping. |
| `404` | `ErrJobNotFound` | Permanent | Wrong `command_key`. |
| `409` | `ErrRoleAlreadyAssigned` | Needs a decision | The role is occupied. Unassign or swap — do not retry. |
| `429` | — | **Retryable** | Back off and retry. **Not** an auth problem. |
| `502` | `ErrACSUnreachable` | **Retryable** | Internal ACS engine unavailable. Back off and retry. |
| `500` | `ErrInternal` | **Retryable**, and report it | Back off, retry, and tell us. |

Every error body has the same shape:

```json
{ "error": "ErrInvalidRequest", "message": "human-readable detail" }
```

Switch on `error`, not on `message` — messages are for humans and may be
reworded.

**Blanket retry-everything is wrong here.** Retrying a `400` hides a real
defect behind noise; retrying a `409` will never succeed. Classify first.

## 4. Rate limiting

A token-bucket limiter, per credential: **5 requests/second, burst 10** by
default. Exceeding it returns `429`.

- Back off exponentially with jitter. Do not retry tightly.
- Cache your OAuth2 token for its full hour. Fetching a token per request
  is the most common way integrations exhaust their own budget.
- Prefer webhooks to polling. One webhook beats a thousand polls.
- If you must poll, no more than every 30 seconds per job.
- Bulk operations should be paced deliberately, not fired in parallel.

Request bodies are capped at 1 MiB.

## 5. Webhooks versus polling

Use **webhooks** as the primary mechanism and **polling** as reconciliation.

Webhook delivery is at-least-once with retry, so:

- **Verify the HMAC signature on every delivery** before acting. An
  unverified webhook endpoint is a way for anyone to tell your BSS that an
  order succeeded.
- **Be idempotent on receipt.** The same event may arrive twice.
- **Return 2xx quickly.** Enqueue and process asynchronously; a slow
  endpoint causes retries.
- **Do not assume ordering.**

Because delivery is at-least-once and not guaranteed-once, run a periodic
sweep that polls any order still non-terminal after a threshold. Webhooks
make that sweep cheap rather than unnecessary.

## 6. Addressing the right device

An account can hold several devices, one per role: `gateway`, `ont`,
`extender`, `stb`, `ata`, `other`. At most one **active** device per role,
enforced in the database.

- Omitting `role` targets `gateway` — safe for single-device accounts and
  the reason older integrations still work.
- **Send `role` explicitly on every order** once any account can hold more
  than one device. It is the difference between changing the customer's
  gateway and changing their extender.
- Assignment is temporal: releasing a device records when and why, and the
  same device can later be reassigned. A device is bound to at most one
  account at a time.
- **Why** is one of `rma`, `upgrade`, `return`, `moved`, `corrected`
  (TMF641 `unassignReason`). Another value is refused with 400 before
  anything runs. Without one, a swap (delete then add, same role) records
  `upgrade` and a delete records `return`.

### 6.1 Device identity (`oui_serial`)

The canonical device id on every request and response is the ACS natural
key, built from the TR-069 `DeviceIdStruct` the CPE sends (USP agents use
the same rule):

- `OUI+ProductClass+SerialNumber`, joined by `+`, when the CPE reports a
  ProductClass; `OUI+SerialNumber` when it reports none. The middle segment
  is the TR-069 **ProductClass**, exactly as the CPE reports it.
- The OUI is upper-case hex with `:`, `-` and spaces removed.
- Any field may contain any character the CPE sends. A literal `+` or `\`
  inside a field is escaped as `\+` or `\\`, so ids never collide (a serial
  `SN+2` appears as `SN\+2`). Send ids exactly as the ACS returns them or as
  you computed them with this rule.
- For pre-provisioning, `OUI-ProductClass-Serial` and `OUI-Serial` (TR-069
  Annex style, unescaped) are also accepted; the ACS matches the sale on OUI
  and serial when the CPE first connects. Prefer the canonical form.

## 7. What the ACS does not know

It holds `account_id` as an opaque string and the device identity. It has
no customer name, address, tariff, billing state or contract.

**Your BSS remains the system of record.** Do not expect the ACS to answer
questions about customers, and do not store BSS-authoritative data here —
it will drift.

The `service_plan` field on a mapping is a label the ACS stores and hands
back. It does not interpret it or change device behaviour based on it.

## 8. Security expectations

- **Credentials:** OAuth2 client-credentials, per integration. The secret
  is shown once. Store it in a secret manager, not in configuration files
  or source control.
- **Revocation** takes effect within about 15 seconds, for both new token
  requests and already-issued tokens. If you suspect a leak, ask the
  operator to revoke immediately; there is no rotate-in-place, a
  compromised client is revoked and replaced.
- **Wi-Fi passwords are write-only.** You can set one; no API returns one.
  This is deliberate and will not change.
- **mTLS** is available as additional transport hardening. It supplements
  the bearer token rather than replacing it.
- **Log hygiene:** `wifi_password` appears in request bodies you send.
  Redact it in your own logs.

### 8.1 Customer-bound clients (one ISP, one customer)

An OAuth client can be **bound to one ACS customer** (an ISP). This is the
recommended model for an ISP or NaaS platform, and the only one accepted
when the ACS runs with `ACS_BSS_REQUIRE_CUSTOMER_BOUND_CLIENTS=true`.

- **Accounts belong to customers.** The first *write* a bound client makes
  for a new `account_id` (a mapping, an order, a TMF641 order, a webhook)
  claims that account for its customer. From then on only that customer's
  clients can read or change it. Every other client gets **404**, never
  403, so account ids are no existence oracle across ISPs. A read of an
  account nobody owns yet is also 404.
- **Account ids stay globally unique.** Mint them so they cannot collide
  across ISPs, for example `<tenant prefix>.<uuid>`. Ownership, not
  per-customer uniqueness, is what keeps one ISP out of the accounts of
  another.
- **Devices follow the account.** Mapping a device with no customer to a
  bound account gives the device that customer. A device of another
  customer is not found (404 `ErrDeviceNotMapped`). The device only
  changes customer when the mapping is actually written.
- **Pre-provisioned sales** (`pre_provision`, TMF641 `add` for an unseen
  CPE) belong to the account's customer. Plan codes resolve only to that
  customer's plans or shared ones. An explicit `customer_id` (mapping) or
  `customerId` (TMF641 order) must be the account's customer, else the
  request is refused (409 on mappings, a FAILED item on TMF641).
- **Accounts from before binding.** An account that already has mappings,
  orders, service orders or webhooks from a global client cannot be
  claimed by a bound client; it stays 404 until an operator assigns it
  (`PUT /api/v1/bss/accounts/{account_id}/customer`, superadmin). Only an
  operator can move an account between customers.
- **Scopes apply to `/bss/v1` too.** A bound client needs the same scope
  as the TMF counterpart of each call: `tmf:read` for reads, job status,
  reconciliation and preflight; `tmf:write` for mappings and webhooks;
  `tmf:execute` for orders. A missing scope is 403 `insufficient_scope`,
  checked before an account is claimed. Grant an integration only what
  it uses. (Clients that predate binding keep the older rule, where scopes
  govern only the TMF APIs.)
- **Asserting your binding.** The token response carries `customer_id`,
  and the JWT carries it as `cust`. A client cannot be both bound and
  fleet-wide; `account_ids` on a bound client narrows it further.
- **Fleet-wide operations** (a webhook with no `account_id`, TMF lists
  without `accountId`) need a global client, which
  `ACS_BSS_REQUIRE_CUSTOMER_BOUND_CLIENTS=true` disables together with the
  shared `ACS_BSS_API_TOKEN` and the unauthenticated lab mode.

## 9. Known limits to design around

- **One action per order.** `MODIFY_WIFI` only, today. An order carries one
  action against one device.
- **`SUSPEND` / `ACTIVATE` return `400`** until the operator configures a
  walled-garden mechanism. This is deliberate: disabling a router's WAN
  interface would sever the very path the ACS needs to reactivate it. If
  your rollout needs suspend, raise it early — it needs a per-vendor
  decision before it can be built safely.
- **No scheduled activation.** Orders execute as soon as the device
  permits. Schedule on your side.
- **Dead-lettered orders are not automatically requeued.** After repeated
  dispatch failure an order is parked for operator attention. Surface such
  orders rather than retrying indefinitely.
- **Exactly-once has one narrow residual gap.** Concurrent submissions and
  multiple ACS instances are handled safely and cannot double-dispatch. A
  process crash in the precise window between a dispatch succeeding and
  that success being recorded could, in principle, allow the reconciler to
  dispatch a second time. Closing it fully requires idempotency support on
  an internal interface that does not yet have it. For a Wi-Fi change the
  practical effect is the same value written twice; we disclose it so you
  can judge it for your own use cases.

## 10. A correct integration, in short

1. Cache the OAuth2 token for its hour.
2. Register one webhook; verify its signature; be idempotent on receipt.
3. Send `role` explicitly on every mapping and order.
4. Generate a stable `external_order_id` per logical order and **persist it
   before calling**, so a retry after a crash reuses it.
5. Treat `202` as accepted, never as done.
6. Classify errors — retry only `429`, `502`, `5xx`.
7. Back off with jitter.
8. Sweep periodically for non-terminal orders.
9. Never log a Wi-Fi password.
10. Keep the BSS authoritative for customer data.
