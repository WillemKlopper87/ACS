# Security probe: 2026-11-18

An internal pass on the BSS adapter and the operator API after BSS contract 2.1.0 (restart, status refresh, factory reset and the status read). It follows `docs/SECURITY-ASSESSMENT-2026-11-14.md` and does not replace the external penetration test that assessment recommends.

## 1. What was done

- **Live probe.** `scripts/security/probe.py` against a local stack (`cmd/api`, two `cmd/bssadapter`, `cmd/acs`, one Postgres) with two ISPs, each with a simulated router. 95 checks.
- **Code review** of the 2.1.0 change (`3da868b..b07fb1a`, `backend/`), done independently of the probe.

Not done, and not to be read as covered: fuzzing, load or denial-of-service testing, the deployed system (TLS, proxy, network rules, hosts), the operator console in a browser, the CWMP device plane beyond two authentication checks, and anything on a real router.

## 2. What held

| Area | Checked |
|---|---|
| One customer reaching another's | Reads (mapping, status, capabilities, job, reconciliation, TMF638 and TMF640 by the real service id), orders and preflights for all four actions, device list, event replay, mapping by serial, sealed secret handles, webhooks. All refused as `404`; the other customer's router received nothing. |
| Tokens | No token, garbage, a payload edited to another customer, `alg: none`, a guessed signing secret, an edited expiry, the console's token and the internal service token on the adapter, an adapter token on the operator API. All `401`. A read-only client cannot order, map or store a secret. A revoked client gets no new token and its issued token stops within the documented 15 seconds. |
| Factory reset switch | Refused without the exact confirmation, with it in another case, with an extra parameter; refused on an adapter where it is off for the order, the preflight, other spellings of the action and a TMF641 attempt. No reset reached the router. |
| Internal service token | Refused on every operator route tried outside its allowlist, including path tricks (trailing segment, `..`, encoded dots). With service calls required to name a customer, a call naming none, or naming another customer, cannot restart, reset or refresh a device. |
| Input | A 2 MiB body, traversal and quote characters in ids, deeply nested JSON, an SSID carrying XML (it stayed one value on the router). Errors carry no stack trace, SQL or file path. |
| Limits | A burst by one client is rate limited and another client is not slowed; repeated wrong operator passwords are throttled. |

## 3. Findings, all fixed in this change

No High or Medium finding. None let one customer reach another's data or devices.

| # | Sev | Finding | Fix |
|---|---|---|---|
| P1 | Low | **The factory-reset switch was checked when an order was taken, not when it was sent.** An order accepted while the switch was on, whose dispatch failed, was retried by the outbox for hours; switching factory reset off in between did not stop the retry from wiping the router. | `sendOrder` checks the switch again. A refused order is dead-lettered at once. |
| P2 | Low | **A refused dispatch was retried eight times and answered `500`.** A status refresh for a device with no status parameter discovered was refused by the operator API (`409`), reported to the integration as an internal error, and retried. | The order is dead-lettered and the integration gets `409 ErrUnsupportedAction`. |
| P3 | Low | **`NaN` or `Inf` from a router broke the status read.** Go parses both as numbers; JSON cannot carry them, so the read answered `200` with an empty body. | Such a value is not a reading and is left out (`null`). |
| P4 | Low | **A NUL byte in an account id or job key was a `500`** (Postgres refuses it), as was a TMF service id that is not a UUID. No data was exposed. | A path with a control byte is refused (`400`) before any handler; ids in a body are checked; a mapping id that is not a UUID is `404`. |
| P5 | Low | **`external_order_id` and `account_id` had no length limit** (a 5,000-character order id was accepted, stored and logged). | 255 bytes, no control characters; otherwise `400 ErrInvalidRequest`. |
| P6 | Low | **Parameter names reported by a device went unescaped into a query string** on the adapter's call to the operator API. | Each name is escaped. |

Tests for each are in `backend/cmd/bssadapter/operations_test.go`, `backend/internal/bss/status_test.go` and `backend/internal/bss/acsclient_test.go`. The probe passes 95 of 95 after the fixes.

## 4. Hardening added after the probe

| # | What | How |
|---|---|---|
| H1 | **A ceiling on destructive orders per customer.** The per-token rate limit alone lets a compromised or faulty integration restart or reset thousands of its routers in an hour. | A new order over the hourly ceiling is refused with `429 ErrRateLimited` and `Retry-After`, before anything is written or sent, and audited as `BSSOrderCeilingReached`. Defaults: 120 restarts and 10 factory resets per customer per hour (`ACS_BSS_MAX_REBOOTS_PER_HOUR`, `ACS_BSS_MAX_FACTORY_RESETS_PER_HOUR`). Suspend has no default ceiling, because a billing run can rightly suspend many accounts in an hour; set `ACS_BSS_MAX_SUSPENDS_PER_HOUR` to add one. `0` means none. Counted in the database, so it holds across replicas. A retry by the outbox and a resend of an order already placed are not counted again. |
| H2 | **A late retry no longer lands on a router that left the account.** A restart, reset or refresh waiting in the outbox is ended if its device is no longer assigned to the order's account when the retry comes. | `DeviceStillAssigned` in the reconciler; the order is dead-lettered. Parameter writes are not checked this way: some are meant to follow an unassignment. |
| H3 | **A resent status refresh is the same request.** The operator API chooses the parameter list itself, so the same key after discovery found more parameters returned a conflict the caller could do nothing about. | The job already queued for that key is returned. |
| H4 | **Response headers.** | Every operator API answer carries `X-Content-Type-Options: nosniff` and `Referrer-Policy: no-referrer`, and `Cache-Control: no-store` under `/api/` unless the handler sets its own. The device web-GUI proxy is left alone. Every adapter answer carries `nosniff` and `no-store`. |

## 5. Still open

- **The service token can order a factory reset directly on the operator API**, with no switch and no confirmation: the switch lives in the adapter. Where service calls need not name a customer (the default outside the production NaaS profile) that reaches every device. Keep `ACS_SERVICE_CALLS_REQUIRE_CUSTOMER=true` and treat the service token as the most sensitive secret in the stack. The ceiling (H1) is also the adapter's, not the operator API's.
- **The ceiling is per action, not per device**, and a few orders over it can pass when requests arrive at the same instant.
- Everything listed as open or as deployment work in the 14 November assessment, including the external penetration test.

## 6. Running the probe

Against a throwaway stack only. `scripts/security/probe.py` names the ports and settings it expects. It exits non-zero when a check fails.
