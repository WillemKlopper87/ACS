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

## 4. Still open

- **The service token can order a factory reset directly on the operator API**, with no switch and no confirmation: the switch lives in the adapter. Where service calls need not name a customer (the default outside the production NaaS profile) that reaches every device. Keep `ACS_SERVICE_CALLS_REQUIRE_CUSTOMER=true` and treat the service token as the most sensitive secret in the stack.
- **No ceiling on destructive orders per client.** A compromised or faulty integration can restart or reset every router of its own customer as fast as the rate limit allows (5 requests a second by default). The NaaS platform limits these per router; the ACS does not.
- **A retried order goes to the device recorded when the order was taken.** If that device is re-mapped to another account of the same customer before a delayed retry, the retry acts on the new subscriber's router. Cross-customer moves are refused. Not traced through the mapping lifecycle.
- **A status refresh resent after discovery changed** is refused as a conflict by the operator API (the job's parameter list differs), and the order is dead-lettered although the first job was queued. Rare; the integration sees a refused order and can send a new one.
- **The API's JSON responses carry no security headers** (`X-Content-Type-Options`, `Cache-Control`). The console is served by nginx, which sets its own; worth adding `nosniff` and `no-store` on the API.
- Everything listed as open or as deployment work in the 14 November assessment, including the external penetration test.

## 5. Running the probe

Against a throwaway stack only. `scripts/security/probe.py` names the ports and settings it expects. It exits non-zero when a check fails.
