# Work summary, 9 to 14 November 2026

Everything done on branch `claude/great-euler-c0t5ar`: what was asked,
what was built, how it was checked, and what is still open. Each section
points to the document that has the detail.

## 1. At a glance

- **19 commits**, 192 files, about 13,900 lines added and 990 removed.
  No pull request has been opened; nothing is merged to `main`.
- **Five schema migrations** (0095 to 0099), all forward-only.
- **State of the checks at the last commit:** the Go suite passes against
  PostgreSQL (54 packages), the console's 189 unit tests and 16 browser
  tests pass, and the production preflight matrix passes.
- **Six decisions or actions are yours** (section 9). The most important:
  the integration contract has changed meaning in two places and has not
  been re-versioned.

| Area | Outcome |
|---|---|
| Codebase assessment | Written: 33 findings, 18 recommendations |
| Device and group management | Plan of 10 steps; step 1 built (bulk operations, job cancel and retry) |
| Live check of the stack | Done locally with a simulated CPE fleet; findings fixed or recorded |
| Console look, feel and workflow | Redesign steps A to G built; no function removed |
| Two-step sign-in (MFA) | Built |
| Security assessment | 3 findings fixed at once; of 14 left open, 6 are now closed, 8 remain (1 medium, 7 low) |
| Standalone / NaaS switch | Built |
| Production profile enforcement | Built for the operator API and the BSS adapter |
| Database login per service | Built, opt-in |
| Account and order IDs per ISP | Built |
| Console session out of browser storage | Built |
| Adapter calls confined to one customer | Built |
| Resent order with a changed body | Built |

## 2. Commits, in order

| Commit | What |
|---|---|
| `35121ed` | Codebase and platform assessment |
| `7ddf521` | Bulk operations; cancel and retry a job |
| `4441c34` | Live-check fixes: cross-account order ID, bulk retry, Jobs screen actions |
| `0af82b6` | Operator two-step sign-in; first console redesign steps |
| `66e2f10` | Security assessment; web-GUI proxy isolation; adapter rate-limit key |
| `29101f7`, `627f5de` | Console: Templates, Policies, Scheduled Jobs, Rollouts, Operators, Alerting on one list pattern; plain labels |
| `bf8da4e` | Production profile covers the operator API and the BSS adapter |
| `959f775`, `712411c` | Console: navigation in areas with tabs, device health row, dashboard, header search, shorter help, themes |
| `5c2f21f` | Integration switch (standalone / NaaS); strict device ownership |
| `863542d` | Separate database logins per service |
| `fd3d417`, `bc3fbab`, `009db2a` | Per-ISP account and order IDs: design, phase 1, phase 2 |
| `3cc1317` | Console session in an HttpOnly cookie |
| `15858a4` | Adapter service calls name their customer |
| `3dd1ebf` | Refuse a resent order whose request changed |

## 3. Assessments and plans

**Codebase assessment** (`docs/ASSESSMENT-2026-11-09.md`). 33 findings and
18 recommendations across architecture, security, operations, testing and
the console. It set the agenda; it is not a list of things done.

**Device and group management** (`docs/DEVICE-MANAGEMENT-PLAN.md`). Six
findings for a single device, eight for groups, and a 10-step plan.
Step 1 is built:

- A bulk action or template apply is now a recorded **bulk operation**:
  progress, which devices failed and why, cancel what is still queued,
  retry what failed or timed out.
- A single job can be **cancelled** while queued and **retried** after it
  fails. Jobs owned by a workflow (a rollout, a sale) cannot be cancelled
  from under it.
- A Bulk operations screen, and Cancel / Retry on the Jobs screen.

Steps 2 to 10 are not built: server-side device search, dynamic groups,
more bulk actions with a preview, maintenance windows, edit in place, a
device timeline, config snapshot and diff, a compliance view, and group
dashboards.

**Live check** (`docs/LIVE-CHECK-2026-11-14.md`). The four services were
run locally against PostgreSQL with a simulated fleet of CWMP devices.
Nine console findings and eight NaaS-integration findings. The one that
mattered: an order ID already used by another ISP was answered with that
ISP's job key and status. Fixed the same day; superseded later by per-ISP
IDs.

## 4. Console

Detail: `docs/CONSOLE-REDESIGN-AND-MFA.md`. Nothing the console could do
before was removed.

- **One pattern for list screens**: a page bar, a table, and a side
  drawer for create and edit. Device Groups, Templates, Policies,
  Scheduled Jobs, Firmware Rollouts, Operators and Alerting were
  converted.
- **Navigation in areas** (Overview, Devices, Activity, Configuration,
  Records & tools, Administration, Help), each with tabs. Every screen
  kept its URL.
- **One confirmation dialog** replaced 20 browser `confirm()` calls.
- **Search from any screen** (Ctrl+K) for a device or subscriber.
- **Device health row** on the device page; devices shown by name in job
  and operation lists; plain-language labels from one table.
- **Dashboard** reduced to one statement per value; shorter Help.
- **Themes**: Dark and Light are supported; the two others are marked as
  not maintained.

Not done: the optional housekeeping from step A (shared form-field and
empty-state components, a fixed type scale, a lint rule against inline
styles).

## 5. Sign-in

**Two-step sign-in (MFA)**. Time-based codes from any authenticator app,
enrolled with a QR code, with one-time recovery codes. Secrets are
encrypted at rest. `ACS_MFA_REQUIRED_ROLES` makes it mandatory per role;
a superadmin can reset another operator's MFA, and
`scripts/reset-admin-password.sh` clears it for the bootstrap admin.

**Session cookie** (finding O6). The console no longer keeps the session
token in browser storage. A sign-in sets a cookie that page scripts
cannot read. A request that relies on it must carry a header only the
console can add and must come from the console's origin; the remote
shell and device web-GUI routes still require a 60-second ticket.
Scripts that send a bearer token are unaffected. Operators sign in once
more after upgrading. The console and the API must be on the same site.

## 6. Security

Detail: `docs/SECURITY-ASSESSMENT-2026-11-14.md`. It is a code review,
not a penetration test.

### Fixed when found

| ID | Severity | Finding |
|---|---|---|
| F1 | High | A device's web pages, relayed through the API, ran on the API's origin and received the operator's credentials. Now sandboxed, credentials stripped. |
| F2 | Medium | The adapter's rate limit was keyed on the token, so a client could mint a new token and start again. Now keyed on the client or address. |
| F3 | Low | A failed OAuth client login left no trace. Now logged. |

### Left open at the time, and where they stand

| ID | Severity | Finding | Status |
|---|---|---|---|
| O1 | Medium | Production did not force the strict adapter settings | **Fixed**: enforced at start-up |
| O2 | Medium | Account and order IDs shared one namespace across ISPs | **Fixed** (section 7) |
| O3 | Medium | An ISP's client could claim any unassigned device by serial | **Built, off by default**: `ACS_BSS_REQUIRE_ALLOCATED_DEVICES=true` |
| O4 | Medium | A browser ticket is not bound to one device or route | Open |
| O5 | Medium | The adapter held a database login and a service token that each reached everything | **Fixed**: service token confined per customer (on by default in production); database logins per service (**opt-in**) |
| O6 | Medium | Session token in browser storage | **Fixed** |
| O7 | Medium | A resent order with a changed body was answered as accepted | **Fixed** |
| O8 | Low | Webhooks may target plain `http://` | Open |
| O9 | Low | `/metrics` needs no credentials | Open |
| O10 | Low | Rate limits are per process, so replicas multiply them | Open |
| O11 | Low | Sign-in timing reveals whether a username exists | Open |
| O12 | Low | Operator passwords need only eight characters | Open |
| O13 | Low | Token issuance and adapter reads are not audited | Open |
| O14 | Info | Frontend container runs as root; images pinned by tag | Open |

Earlier summaries in this work said every code finding taken on was
closed. That is true of the ones worked on; O4 and O8 to O14 were never
started and are still open.

### What the fixes do

- **Production profile** (`ACS_DEPLOYMENT_PROFILE=production`). The
  operator API and the adapter refuse to start with lab settings: no dev
  mode, MFA for every role that can change devices, the device web-GUI
  sandbox on, an exact console origin, customer-bound OAuth clients only,
  TLS or a loopback listener, and a list of source networks for the
  adapter.
- **Integration switch** (`ACS_INTEGRATION_MODE`). `standalone` runs the
  ACS from its own console: the adapter answers 503 and runs no workers,
  the API accepts no service token, and the console hides the BSS / NaaS
  screen. `naas` is the default. Nothing is deleted when switching.
- **Strict device ownership** (O3). With the setting on, an ISP's
  integration can take only a device an operator has already allocated
  to that ISP.
- **Database login per service** (O5). `scripts/db-service-roles.sh`
  gives the gateway, the API, the adapter and the USP controller a login
  each, with only the table privileges its code uses. The adapter cannot
  read operator accounts, MFA secrets or device credentials; no service
  can change the schema. The privilege table is generated from the code
  and a test fails if the two drift. `--disable` goes back.
- **Adapter confined per customer** (O5). Every adapter call to the
  operator API names the customer it acts for, and the API treats it
  like an operator scoped to that customer. This also fixed
  reconciliation and the TMF640 Wi-Fi read, which returned 502 whenever
  operator authentication was on.
- **Resent orders** (O7). The same order ID with a different request is
  refused with 409; the original stands. An identical resend is still a
  retry. Orders placed before this are not compared.

## 7. Account and order IDs per ISP

Detail: `docs/TENANT-SCOPED-IDS-DESIGN.md`.

Before, `account_id`, `external_order_id`, the TMF641 `externalId` and
event and alarm source keys were unique across the whole ACS. Two ISPs
could not use the same numbering, and one could block another by using
an ID first. Now each is unique per ISP.

- **Phase 1** (migration 0098): an account registry with an internal
  key; every table that names an account points at it. No behaviour
  change.
- **Phase 2** (migration 0099): the switch. A customer-bound client's
  IDs are its own. A fleet-wide client or an operator names the customer
  (`?customer_id=`) when an ID is used by more than one, and is refused
  with 409 otherwise.
- **A hole found on the way**: rows loaded by their own ID (a service, an
  order, a sale, a webhook subscription) were authorized on the row's
  account ID, which two ISPs can now share. Such lookups now run inside
  the caller's customer and the gate checks the row itself.
- **One departure from the approved design**: an unowned account that
  already has mappings or orders still needs an operator to assign it
  before an ISP's client can write to that ID.
- The console shows the customer beside each account ID and passes it
  along.

## 8. What changed for someone running or integrating with the ACS

### Migrations

| Migration | What |
|---|---|
| 0095 | Bulk operations; `CANCELLED` job status |
| 0096 | Operator MFA |
| 0097 | Event triggers run with the owner's rights (for per-service logins) |
| 0098 | Account registry and internal keys; order key, scope and request digest |
| 0099 | IDs unique per customer; source keys per customer / account |

0098 and 0099 rewrite the account columns of the event table once. Back
up first and run them in a quiet period.

### Settings added or changed

| Setting | Service | Meaning |
|---|---|---|
| `ACS_INTEGRATION_MODE` | api, bssadapter | `naas` (default) or `standalone` |
| `ACS_MFA_REQUIRED_ROLES` | api | Roles that must use two-step sign-in |
| `ACS_WEBGUI_ALLOW_SAME_ORIGIN` | api | Lab only: turns the device-page sandbox off |
| `ACS_BSS_ALLOWED_CIDRS` | bssadapter | Networks the integration may call from |
| `ACS_BSS_REQUIRE_ALLOCATED_DEVICES` | bssadapter | Strict device ownership |
| `ACS_SERVICE_CALLS_REQUIRE_CUSTOMER` | api | Refuse adapter calls that name no customer (default on in production with `naas`) |
| `ACS_CWMP_POSTGRES_DSN`, `ACS_API_POSTGRES_DSN`, `ACS_BSS_POSTGRES_DSN`, `ACS_USP_POSTGRES_DSN` | each | A database login per service |
| `ACS_DB_MIGRATIONS` | acs | `apply` or `check` |
| `ACS_API_CORS_ORIGIN` | api | Existing; `*` is now refused in production |

### Scripts

`scripts/gen-production-env.sh`, `security-preflight.sh` and its test,
`start-production.sh`, `reset-admin-password.sh`, `db-service-roles.sh`
(new), `start.sh` (starts each service without the schema owner's
login), `restore.sh` (says to run the migration step afterwards).

### Operator API

New: bulk operations, job cancel and retry, the MFA routes,
`/api/v1/system/info`, `/api/v1/auth/session`, the `customer_id`
qualifier on account routes. Changed: sign-in may answer with an MFA
step; sign-in also sets a cookie.

### Integration contract (`/bss/v1`, TMF)

For a customer-bound client the requests and responses are unchanged.
The **meaning** of some answers changed:

| Situation | Before | Now |
|---|---|---|
| Write to an account ID another ISP uses | 404 | Succeeds on the caller's own account |
| Order ID another ISP used | 409 | Succeeds as the caller's own order |
| Same order ID, different request | 202 with the original | 409 `ErrIdempotencyKeyReused` |
| Call from outside the allowed networks | n/a | 403 `ErrSourceNotAllowed` |
| Adapter in standalone mode | n/a | 503 `ErrIntegrationDisabled` |
| Fleet-wide client, ID used by two customers | n/a | 409 `ErrAccountAmbiguous` |

One recorded fixture exchange no longer holds (`subscriber-profile`, the
last one). `contract/README.md` lists the changes as unreleased.

## 9. Yours to decide or do

1. **Contract version.** By the contract's own rule a changed meaning is
   a major version. Fixtures have not been re-recorded and no tag cut.
2. **Turn on `ACS_BSS_REQUIRE_ALLOCATED_DEVICES=true`** before a second
   ISP connects.
3. **Run `scripts/db-service-roles.sh`** to switch on per-service
   database logins.
4. **A NaaS test mode.** Whether a development NaaS platform should be
   tested against the production ACS with relaxed rules. The
   recommendation was a separate lab-profile ACS.
5. **External penetration test** of the adapter and console.
6. **Open a pull request** when you want this reviewed or merged; none
   exists.

## 10. Not verified

These were not exercised and should not be read as covered:

- Webhook delivery end to end (the local run refuses loopback targets).
- First-contact credential graduation in the production profile with
  real CPEs.
- `gen-production-env.sh`, `start-production.sh` and `start.sh` end to
  end (they need a Docker host). `reset-admin-password.sh` was
  syntax-checked only.
- Firmware image upload in the redesigned Rollouts screen.
- The device health row's signal, uptime and Wi-Fi figures with data
  from a real device.
- Migration time on a production-sized event table (the lab copy has
  about 100 events).
- The session cookie behind a real TLS proxy with separate console and
  API hostnames.
- A rolling upgrade on a real deployment (restart the adapter before the
  API).
- Per-service database logins: every write route, and the USP controller
  beyond start-up. A generated-from-code test is the guard there.

## 11. Loose ends

- **Queued, not done**: two adapter routes answer 500 instead of 404 for
  an ID that is not a UUID (a task card exists). The operator route
  `GET /devices/{id}/cli/connect` without a `credential_id` does the
  same.
- **Compose `containerized` profile** still uses the shared database
  login.
- **Alert incidents** are labelled with the account ID and the device;
  two ISPs' same-numbered accounts share the label.
- **Device management plan** steps 2 to 10, and the optional console
  housekeeping.
- **Security findings** O4 and O8 to O14.

## 12. Where the detail is

| Document | Contents |
|---|---|
| `docs/ASSESSMENT-2026-11-09.md` | Codebase assessment |
| `docs/DEVICE-MANAGEMENT-PLAN.md` | Device and group plan; step 1 as built |
| `docs/LIVE-CHECK-2026-11-14.md` | Local run findings |
| `docs/CONSOLE-REDESIGN-AND-MFA.md` | Console redesign and MFA |
| `docs/SECURITY-ASSESSMENT-2026-11-14.md` | Findings, status, tests added |
| `docs/TENANT-SCOPED-IDS-DESIGN.md` | Per-ISP IDs: design and both phases as built |
| `docs/OPERATIONS-RUNBOOK.md` | How to run each new thing; upgrade notes |
| `docs/integration/` | Integration guide, updated for the contract changes |
| `contract/README.md` | Unreleased contract changes |
