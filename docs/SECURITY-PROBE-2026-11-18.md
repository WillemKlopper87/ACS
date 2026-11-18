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

## 6. Second pass: the stack in the production profile

The same day, against a local stack started as production runs it: `ACS_DEPLOYMENT_PROFILE=production`, NaaS mode, the adapter in strict mode (customer-bound clients only, source allowlist, sealed secrets only, allocated devices only), TLS on the CWMP port, a TLS proxy in front of the API and the adapter, two-step sign-in required, and each service on its own database login. `scripts/security/prod_probe.py`.

Still a local stack: no real firewall, hosts, certificates or network, and no real router.

### What held

| Area | Checked |
|---|---|
| Refusing an unsafe configuration | The adapter does not start with a shared token, without a source allowlist, with fleet-wide clients, without a metrics token, or on a public address without TLS. The API does not start with CORS `*`, without a metrics token, without two-step sign-in for the writing roles, or with a short signing secret. The CWMP gateway does not start without TLS, with TLS below 1.2, with Basic authentication, with the shared credential for established devices, or without a device-network allowlist. |
| TLS | TLS 1.0 and 1.1 refused, 1.2 and 1.3 accepted, on all three listeners; RC4, 3DES, CBC-SHA and NULL suites refused; plain HTTP to the CWMP port is `400`. |
| Two-step sign-in | A password alone gives no session; neither step token works as a session or at the other step; enrolment cannot be restarted; four wrong codes and the next is `429`; a one-time code cannot be used twice. |
| Sessions | The cookie is `HttpOnly`, `Secure`, `SameSite=Strict`, scoped to `/api/`. A cookie-authenticated change is refused without the CSRF header, from a foreign, look-alike or `null` origin. Edited, `alg: none` and extended tokens are `401`. Logout, disabling an operator and removing one stop the session at once. |
| Roles | All 209 registered routes were called with no session (none answered) and every changing route as a read-only operator (none changed anything, apart from its own logout and the public password-reset request). No route answered `5xx`. A `noc` operator cannot create operators or list integration clients. |
| Adapter, strict mode | A fleet-wide client's token is refused on every route (`ErrClientNotCustomerBound`). Mapping an unallocated, another customer's or an unknown serial is refused, and the three answers are identical. A call from an unlisted network is refused, and a client cannot choose its source address through the proxy. A Wi-Fi password in clear is refused. Factory reset is off. The order ceiling refused the sixth restart in the hour. |
| CWMP | No or wrong credentials and Basic authentication are refused. An external entity reads no file; an entity-expansion bomb is rejected at once; an 8 MiB Inform is refused; garbage is `400`; a connection that never finishes its headers is dropped after 30 seconds. |
| Database logins | The adapter's login cannot read operator password hashes, two-step secrets or device credentials, change operators, or create or drop tables. The gateway's cannot read integration client secrets or operator hashes or delete the audit log. The API's cannot alter the schema or create roles. None is a superuser. |
| Request forgery | The device web-GUI target refuses loopback and link-local addresses. Webhook targets on loopback, link-local, `localhost`, decimal and hexadecimal forms of `127.0.0.1`, and non-HTTPS schemes are refused at creation. |

### Findings

| # | Sev | Finding | Status |
|---|---|---|---|
| D1 | Medium | **An integration could make the ACS connect to private addresses on its own network.** A webhook `target_url` on a private address (10/8, 172.16/12, 192.168/16) was accepted, the dispatcher connected to it (observed: a TCP connection to a listener on the host's own private address), and the delivery error told the integration whether something answered. An ISP's client could map the ACS operator's internal network from outside. Only where `ACS_BSS_WEBHOOK_ALLOWED_CIDRS` was unset, which was the default. | **Fixed.** In production a webhook target on a private, shared (100.64/10) or unique-local address is refused at creation and at delivery unless `ACS_BSS_WEBHOOK_ALLOWED_CIDRS` lists it. Existing subscriptions to such addresses stop delivering ("target refused by the network policy"). |
| D2 | Low | **The API's database login can delete audit rows.** It is granted for the retention job that runs inside the API. Someone who takes over the API can erase the trail of what they did. | Open. Ship the audit trail off the host (14 November, item 4), or move retention to the owner role run from a scheduled `cmd/migrate`-style job so the API's login can be `INSERT`/`SELECT` only. |
| D3 | Low | **The bootstrap credential registers any serial the ACS has never seen** (known as S2 since 30 October; confirmed here: an Inform with an invented serial was accepted and created a device row). Anyone holding the fleet's bootstrap credential, which every unprovisioned router carries, can fill the device table and squat on serials. With allocated-devices-only on, a squatted serial cannot be mapped by an integration. | Open. Pre-register expected serials (or a per-batch bootstrap credential) and refuse the rest. |
| D4 | Info | **The API serves plain HTTP on whatever address it is given**, production profile included. `scripts/start.sh` binds it to loopback and the compose file publishes it on loopback only; the binary itself does not refuse a public address the way the adapter does (it cannot tell a container's `0.0.0.0` from a host's). | Open, by design. Never set `ACS_API_ADDR` to a public address. |
| D5 | Info | **A fleet-wide client can still be created and gets a token in strict mode.** The token is refused on every route. | Open. Refuse creation and issuance in strict mode so nothing misleading exists. |

## 7. Running the probes

Against a throwaway stack only. `scripts/security/probe.py` (lab profile, two ISPs with simulated routers) and `scripts/security/prod_probe.py` (production profile behind a TLS proxy) each name the ports and settings they expect, and exit non-zero when a check fails.
