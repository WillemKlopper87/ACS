# Security assessment: 2026-11-14

Is the ACS a weak point between the ISP NaaS platform, the BSS and OSS, the network and the customers? This is a review of the code at the head of this branch, with the surfaces a NaaS integration touches examined first. Three findings were fixed while doing it (§4); the rest are listed with what to do about them (§5) and what protection to add around the ACS (§6).

## 1. Verdict

The ACS is not an obvious weak point. The controls that matter most for a multi-tenant integration are present and tested: every adapter route authorizes the caller against the account it names, another customer's data answers `404`, the adapter's own credential to the API is confined to five routes, outbound calls are guarded against server-side request forgery at connect time, and no SQL is built from request input.

It is, however, a **high-value target**: one service that can rewrite Wi-Fi credentials, suspend service and reboot or reset every customer's device. Three issues were worth fixing immediately and are fixed. The remaining risk is mostly in **how it is deployed and configured** (which credentials are accepted, what network can reach it, how services share secrets), not in exploitable bugs.

## 2. Scope and method

Reviewed, by reading the code and running targeted tests against a local stack:

- `cmd/bssadapter`: authentication, authorization on every registered route, token issuance, rate limiting, webhooks, idempotency.
- `cmd/api`: session handling, the service-token path, tenancy scoping, the device web-GUI proxy and CLI bridge, file serving.
- Cross-cutting: injection (SQL, command, path), outbound request guards, secrets at rest, the Grafana database role, response headers, dependency scanners.

Not done, and not to be read as covered:

- No penetration test, fuzzing or load-based denial-of-service testing.
- No review of the deployment itself: cloud security groups, TLS termination, host hardening, backups, the reverse proxy.
- No review of the NaaS platform or the BSS on the other side of the integration.
- The CWMP device plane was reviewed in the earlier audits and only spot-checked here.

Severity: **High** could lead to takeover of operator access or cross-customer impact; **Medium** weakens a control or needs another failure to matter; **Low** is hardening.

## 3. What holds

| Control | Evidence |
|---|---|
| Every adapter route checks the caller | A script over the route table found an authorization call in, or one call below, every handler (`tmfPrincipal`, `authorizeBSSAccount`, `authorizeTMF`, `authorizeBSSFleet`). |
| Tenant isolation on accounts and devices | Live: a client bound to one customer could not map, order against or read another customer's device or job (`404`). Contract fixture `not-found-other-account`. |
| The adapter refuses to start without inbound credentials | `config.RequireOneOf` in `cmd/bssadapter/main.go:89`; the unauthenticated mode needs `ACS_INSECURE_DEV_MODE=true`. |
| The adapter's credential to the API is narrow | `isServiceRoute` (`cmd/api/auth_handlers.go:197`): write parameters, read a device, read parameter names, read Wi-Fi networks, read a job. Nothing else. |
| Token forgery and confusion | Tokens are HMAC-SHA256 only (no algorithm negotiation), operator and BSS tokens use different secrets and roles, and ticket and MFA tokens are audience-bound. |
| Client secrets | 256-bit random, stored as bcrypt hashes; revocation is checked on every request. |
| Server-side request forgery | Webhook targets and the web-GUI proxy are checked at save time and again on the connecting socket (`netguard.DialControl`), so a DNS answer that changes cannot slip through; redirects are not followed. |
| Injection | No SQL built from request values (three formatted statements, all from constants); no shell execution; stored files are addressed by ID with `filepath.Base`. |
| Dangerous device writes | The protected-parameter guard refuses, on every path including BSS orders, writes that would repoint or cut off a device. |
| Monitoring database role | `grafana_ro` is granted named columns only; secret columns are excluded (`scripts/grafana-db-role.sh`). |
| Dependencies | `govulncheck`: no vulnerability reachable from this code. `npm audit`: none. |
| Operator sign-in | Rate-limited, no username disclosure in the response, sessions revocable, and now two-step sign-in (`docs/CONSOLE-REDESIGN-AND-MFA.md`). |

## 4. Fixed in this change

| # | Sev | Finding | Fix |
|---|---|---|---|
| F1 | **High** | **Device web-GUI proxy.** A device's own web pages are relayed through the API and so are served from the API's origin. (a) The proxy forwarded the operator's browser ticket, in the query string, and any `Authorization` header to the device. A device can reach the API, and a ticket is not tied to one device, so a compromised CPE could use it to open the SSH console of other devices for the ticket's 60 seconds. (b) Nothing isolated the device's scripts. Where the console and API share an origin, which is the documented production layout, a script on a malicious or compromised CPE could read the operator's session out of browser storage: full operator access. | The proxy strips the ticket and `Authorization` before forwarding. Every proxied response carries `Content-Security-Policy: sandbox` without `allow-same-origin`, so the page runs in an origin of its own and cannot touch the console or its storage; a device's own policy header is replaced and its `Set-Cookie` dropped. The console's frame has the matching `sandbox` attribute. `ACS_WEBGUI_ALLOW_SAME_ORIGIN=true` turns the sandbox off for a lab. Tests: `cmd/api/webgui_isolation_test.go`. |
| F2 | **Medium** | **Rate limiting keyed on the `Authorization` header.** A client can mint a new token at will and each token is a different header, so each got its own allowance. On the token endpoint every guessed secret was a different header, so guesses were not limited at all, each costing a bcrypt comparison: unthrottled guessing and a cheap way to burn CPU. | The bucket is the authenticated client, or the caller's address on the token endpoint. Tests: `cmd/bssadapter/ratelimit_key_test.go`. |
| F3 | Low | **A failed OAuth client login left no trace**: no log line, no audit record. | Logged with the client ID and caller address (never the secret). |

Also fixed earlier on this branch and relevant here: a second customer reusing another customer's `external_order_id` was answered with that customer's job key and status (`docs/LIVE-CHECK-2026-11-14.md`, N1).

**F1 changes behaviour**: a device web page that relies on its own cookies or browser storage will not work inside the sandbox. That is the price of not running device code as the console. The lab switch exists for qualification work.

## 5. Open findings

| # | Sev | Finding | Recommendation |
|---|---|---|---|
| O1 | **Medium** | **Production does not force the strict adapter mode.** By default the adapter also accepts the legacy shared token (`ACS_BSS_API_TOKEN`, minimum 16 bytes, all scopes, every account) and global OAuth clients. `ACS_BSS_REQUIRE_CUSTOMER_BOUND_CLIENTS=true` closes both, but `scripts/gen-production-env.sh` and the security preflight do not set or check it, and TLS on the adapter is optional. | Make the production profile require customer-bound clients, refuse the shared token, and require TLS (or a declared TLS-terminating proxy) on the adapter. Add all three to `security-preflight.sh`. |
| O2 | **Medium** | **Account IDs are one namespace across all customers, claimed by first write.** A customer-bound client that writes to an account ID nobody owns claims it. Two ISPs with the same numbering scheme collide, and one can claim the other's future IDs so that the other gets `404` on its own accounts. Denial of service between tenants, not disclosure. | Scope accounts and `external_order_id` by customer in the schema (a composite key). Until then, require ISPs to prefix their IDs and say so in the integration guide. |
| O3 | **Medium** | **Any customer-bound client can claim an unassigned device by serial** (live check, N3). Serials are printed on the box. | In production, require a prior sale or import record that ties the serial to the customer (the onboarding "strict mode"). |
| O4 | **Medium** | **A browser ticket is not bound to a device or route.** It is short (60 s) and F1 stops it reaching devices, but one issued to open one device's console is valid for any other. | Put the device ID and purpose in the ticket and check them. |
| O5 | **Medium** | **Blast radius of the adapter.** It is the most exposed service and holds (a) the internal service token, which can write parameters on any device, with no customer attached to the call, and (b) the same database credential as every other service, so its compromise is a compromise of the whole database, including operator password hashes and sealed secrets. | Pass the acting customer with service calls and enforce it in the API. Give each service its own database role with only the tables it needs. |
| O6 | **Medium** | **The operator session token is in browser storage** (earlier assessment, C2). Any script-injection flaw anywhere on the console origin yields the session. F1 removed the most direct route to that; the token is still there. | Move the session to an `HttpOnly`, `SameSite=Strict` cookie with CSRF protection. |
| O7 | **Medium** | **Same order ID, changed body, same account** is answered as accepted with the original order (live check, N2). An integration that amends and resends is told it succeeded. | Reject with `409`; needs a contract decision because it changes an existing endpoint's meaning. |
| O8 | Low | **Webhooks**: plain `http://` targets are accepted, so events about customers' devices can leave unencrypted; signing secrets are stored in the clear (they must be recoverable, but can be sealed like MFA secrets); any non-empty secret is accepted. | Require `https` outside lab mode, seal the secrets, require 32 bytes. |
| O9 | Low | **`/metrics` needs no credentials** on the API and the adapter. It exposes route names, volumes and error rates. Harmless on loopback, which is the compose default; not if the port is published. | Keep it off public listeners, or put it on a separate internal port. |
| O10 | Low | **Limits are per process** (login, MFA, adapter). With several replicas each multiplies. | Shared limiter in Postgres when replicas are introduced. |
| O11 | Low | **Sign-in timing**: an unknown username returns before the password hash is computed, so response time tells a valid username from an invalid one. Same on the OAuth token endpoint for client IDs (which are random, so of little use). | Compare against a fixed dummy hash when the user is unknown. |
| O12 | Low | **Operator passwords** need only eight characters. | Twelve, plus a breached-password check; make two-step sign-in mandatory for privileged roles (`ACS_MFA_REQUIRED_ROLES`). |
| O13 | Low | **Token issuance and adapter reads are not audited**, only mutating actions. After an incident it is not possible to say which client read which customer's devices. | Audit token issuance and, at least in summary, reads of personal data. |
| O14 | Info | The frontend container runs nginx as root and images are pinned by tag, not digest (earlier assessment). | Unprivileged nginx image; pin digests. |

Not re-verified here and still open from `docs/GAP-ANALYSIS-2026-10-30.md`: the bootstrap credential admits any not-yet-established serial (S2), and a client certificate with an empty common name skips device binding (S8).

## 6. Additional protection to put around the ACS

In the order I would do them. The first four cost configuration, not code.

1. **Decide what can reach what.** The adapter should be reachable only from the NaaS platform's addresses (network allowlist), the operator console only through the corporate VPN or SSO proxy, and the CWMP listener only from the device networks. Postgres, `/metrics`, Grafana and the adapter's port should never be on a public interface.
2. **Run the adapter in its strict mode** (O1): customer-bound OAuth clients only, no shared token, TLS required. Add mutual TLS between the NaaS platform and the adapter (`ACS_BSS_MTLS_CA_CERT` already exists), so a leaked client secret alone is not enough.
3. **Require two-step sign-in for every operator who can write** (`ACS_MFA_REQUIRED_ROLES=superadmin,manager,noc`).
4. **Ship logs and the audit trail off the host** to wherever the security team watches, and alert on: failed OAuth client logins, `OperatorMFAFailed`, `OperatorMFAReset`, rate-limit rejections, and any use of the shared token.
5. **Limit the blast radius** (O5): per-service database roles, customer-scoped service calls.
6. **Close the tenant-namespace gaps** (O2, O3) before a second ISP is onboarded. With one ISP they are theoretical; with two they are not.
7. **Secrets**: keep them in a secrets manager rather than `~/.acs-secrets.env`, and rehearse rotating each one (the runbook describes the effects).
8. **Bound the financial actions.** Suspend, activate and plan change are orders like any other. Consider a per-client ceiling on how many suspensions an integration can issue in an hour, with an alert, so a compromised or buggy BSS cannot suspend a customer base in one run.
9. **Commission an external penetration test** of the adapter and console once items 1 to 3 are in place. This review reads code; it does not replace someone attacking the running system.

## 7. Tests added

- `backend/cmd/api/webgui_isolation_test.go`: no operator credential reaches a device; device content is sandboxed whatever headers it sends; the lab opt-out.
- `backend/cmd/bssadapter/ratelimit_key_test.go`: fresh tokens share one allowance; token-endpoint guesses share one allowance per address.
