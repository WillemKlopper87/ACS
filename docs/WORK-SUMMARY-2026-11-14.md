# Work summary, 9 to 14 November 2026

Everything done on branch `claude/great-euler-c0t5ar`, merged to `main`
on 15 November 2026 as pull request 104: what was asked,
what was built, how it was checked, and what is still open. Each section
points to the document that has the detail.

Last updated 15 November, to include the remaining security findings
(O4, O8 to O14) and the parts of them that were first left out.

## 1. At a glance

- **24 commits**, 227 files, about 16,300 lines added and 1,050 removed.
  No pull request has been opened; nothing is merged to `main`.
- **Seven schema migrations** (0095 to 0101), all forward-only.
- **State of the checks at the last commit:** the Go suite passes against
  PostgreSQL (55 packages) and the production preflight matrix passes.
  The console's 189 unit tests and 16 browser tests passed when last run,
  which was before the final commit; that commit changed no console code.
- **Every finding of the security assessment has been addressed.** Four
  protections are built but stay off until a setting is turned on
  (section 6).
- **Nine decisions or actions are yours** (section 9). The one that
  matters most now: an existing production host needs two changes
  before it will start on this version. The contract has since been
  released as 2.0.0.

| Area | Outcome |
|---|---|
| Codebase assessment | Written: 33 findings, 18 recommendations |
| Device and group management | Plan of 10 steps; step 1 built (bulk operations, job cancel and retry) |
| Live check of the stack | Done locally with a simulated CPE fleet; findings fixed or recorded |
| Console look, feel and workflow | Redesign steps A to G built; no function removed |
| Two-step sign-in (MFA) | Built |
| Security assessment | 3 findings fixed at once; all 14 left open have since been addressed (some parts are opt-in, listed in section 6) |
| Standalone / NaaS switch | Built |
| Production profile enforcement | Built for the operator API and the BSS adapter; a `/metrics` token is required on all four services |
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
| `728d74b` | This summary, first version |
| `06ae6a3` | The remaining security findings: O4 and O8 to O14 |
| `c12ddef` | Third-party Compose images pinned by digest |
| `bd591ad` | The parts of O9, O10 and O12 first left out: `/metrics` token required in production, breached-password lookup, shared request limit |

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
| O4 | Medium | A browser ticket is not bound to one device or route | **Fixed**: a ticket is for one purpose on one device |
| O5 | Medium | The adapter held a database login and a service token that each reached everything | **Fixed**: service token confined per customer (on by default in production); database logins per service (**opt-in**) |
| O6 | Medium | Session token in browser storage | **Fixed** |
| O7 | Medium | A resent order with a changed body was answered as accepted | **Fixed** |
| O8 | Low | Webhooks may target plain `http://` | **Fixed**: 32-character secrets, `https` in production, secrets sealed at rest when a key is set |
| O9 | Low | `/metrics` needs no credentials | **Fixed**: `/metrics` needs a token, required in production |
| O10 | Low | Rate limits are per process, so replicas multiply them | **Fixed** for sign-in attempts; general rate limits shared with `ACS_SHARED_RATE_LIMIT=true` (**opt-in**) |
| O11 | Low | Sign-in timing reveals whether a username exists | **Fixed** |
| O12 | Low | Operator passwords need only eight characters | **Fixed**: 12 characters and basic rules; breached-password lookup with `ACS_PASSWORD_BREACH_CHECK_URL` (**opt-in**) |
| O13 | Low | Token issuance and adapter reads are not audited | **Fixed**: token issuance audited; reads audited in summary |
| O14 | Info | Frontend container runs as root; images pinned by tag | **Fixed**: unprivileged console image; all images pinned by digest |

O4 and O8 to O14 were done after this summary was first written. The
runbook section "Smaller hardening changes" says what each needs from
whoever runs the ACS.

Four protections are built but **off until a setting is turned on**:

| Protection | Turn on with | Why it is not on by default |
|---|---|---|
| Strict device ownership (O3) | `ACS_BSS_REQUIRE_ALLOCATED_DEVICES=true` | Changes how an ISP's first mapping of a device works; needed before a second ISP |
| Database login per service (O5) | `scripts/db-service-roles.sh` | Creates database roles and changes how the gateway handles migrations |
| Breached-password lookup (O12) | `ACS_PASSWORD_BREACH_CHECK_URL` | An outbound call to a service outside the ACS |
| Shared request limit (O10) | `ACS_SHARED_RATE_LIMIT=true` | Only useful with more than one replica; costs a database exchange a second |

Sealing of webhook signing secrets (O8) turns itself on when
`ACS_BSS_SECRET_KEY` is set, which it may already be for Wi-Fi secret
handles.

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
- **Browser tickets** (O4). The 60-second ticket that opens the remote
  shell or a device's web GUI is issued for that one purpose on that one
  device, only for a device the operator may see.
- **Webhooks** (O8). A new signing secret needs 32 characters. In the
  production profile a new `http://` target is refused. With
  `ACS_BSS_SECRET_KEY` set, signing secrets are stored encrypted and
  bound to their subscription; ones already stored are encrypted when
  the adapter next starts. Existing `http://` subscriptions and shorter
  secrets keep working.
- **`/metrics`** (O9). With `ACS_METRICS_TOKEN` set, `/metrics` on all
  four services needs that bearer token. The production profile requires
  it: `gen-production-env.sh` generates it and `start.sh` renders the
  Prometheus configuration with it. This matters most on the CWMP
  listener, which device networks can reach.
- **Attempt and request limits across replicas** (O10). Password and
  two-step sign-in attempts are also counted in the database, always.
  The general request limit of the API and the adapter can be shared
  too: each process exchanges its counts about once a second, so the
  limit is approximate by about that much.
- **Sign-in timing** (O11). An unknown username or OAuth client costs
  the same hash comparison as a wrong password or secret.
- **Passwords** (O12). Twelve characters; not the username, a run, or a
  common word with digits added. Checked when a password is set, so
  existing ones keep working. The optional lookup refuses passwords
  found in published breaches; only the first five characters of the
  password's SHA-1 digest leave the API, and a failed lookup does not
  block a password change.
- **Audit trail** (O13). Issuing a token and a refused client login are
  recorded. Reads are recorded in summary: one row per client, customer
  and route for each five minutes in which that client read that route.
- **Containers** (O14). The console image runs nginx as an unprivileged
  user on port 8080. The base images of the five Dockerfiles and the six
  third-party images in the Compose file are pinned by digest, and
  Dependabot raises them.

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
| 0100 | Sign-in attempt counts shared by all replicas |
| 0101 | Request-rate counts shared by all replicas (used only when switched on) |
| 0102 | Alert policy tied to one account (added after the merge) |

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
| `ACS_METRICS_TOKEN` | all four | Bearer token for `/metrics`; **required in production** |
| `ACS_BSS_SECRET_KEY` | bssadapter, acs, api | Existing; now also seals webhook signing secrets. Set it on the API too |
| `ACS_PASSWORD_BREACH_CHECK_URL` | api | Turns the breached-password lookup on |
| `ACS_SHARED_RATE_LIMIT` | api, bssadapter | `true`: the request limit holds across replicas |

### Scripts

`scripts/gen-production-env.sh` (also generates the `/metrics` token),
`security-preflight.sh` and its test, `start-production.sh`,
`reset-admin-password.sh`, `db-service-roles.sh` (new), `start.sh`
(starts each service without the schema owner's login, and renders the
Prometheus configuration with the `/metrics` token when one is set),
`restore.sh` (says to run the migration step afterwards).

### Containers and monitoring

- The console container listens on **8080**, not 80. The Compose file is
  updated; any other port mapping or proxy upstream for that container
  has to change.
- All images are pinned by digest. A build or pull that cannot find a
  digest fails instead of using different content.
- A Prometheus that `start.sh` does not start needs the `/metrics` token
  added to each ACS scrape job by hand.
- New audit actions: `BSSTokenIssued`, `BSSClientAuthFailed`,
  `BSSReadsSummary`.

### Operator API

New: bulk operations, job cancel and retry, the MFA routes,
`/api/v1/system/info`, `/api/v1/auth/session`, the `customer_id`
qualifier on account routes. Changed: sign-in may answer with an MFA
step; sign-in also sets a cookie; `POST /api/v1/auth/ticket` now needs
the device and the purpose in its body; a new operator password must
pass the password rules; `/metrics` may need a bearer token.
`PUT /api/v1/dashboard/layout` without a widget list is now `400`. It used
to save an empty value, after which that operator's Dashboard showed an
error on every visit; a layout already saved that way now reads as the
default. Found while taking the screenshots below.

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
| New webhook with a secret under 32 characters | Accepted | 400 `ErrInvalidRequest` |
| New webhook with an `http://` target, production profile | Accepted | 400 `ErrInvalidRequest` |

Released as contract 2.0.0 on 15 November: all fixtures recorded again,
three added (`ids-per-customer`, `order-resend-changed`,
`webhook-secret-too-short`). `contract/README.md` has the table.

## 9. Yours to decide or do

**Before an existing production host is upgraded**

1. **Give it a `/metrics` token.** The services refuse to start in the
   production profile without `ACS_METRICS_TOKEN`. Run
   `gen-production-env.sh` again, or add the line by hand, first.
2. **Check what points at the console container.** It now listens on
   8080.

**Decisions**

3. **Contract version.** Done: 2.0.0, a major version by the contract's
   own rule. The tag `bss-contract-v2.0.0` goes on the merge commit.
4. **Turn on `ACS_BSS_REQUIRE_ALLOCATED_DEVICES=true`** before a second
   ISP connects.
5. **Run `scripts/db-service-roles.sh`** to switch on per-service
   database logins.
6. **Breached-password lookup.** Whether the API may call the public
   service, a copy inside your network, or neither.
7. **A NaaS test mode.** Whether a development NaaS platform should be
   tested against the production ACS with relaxed rules. The
   recommendation was a separate lab-profile ACS.
8. **External penetration test** of the adapter and console.
9. **Open a pull request** when you want this reviewed or merged; none
   exists.

Also worth doing: move any existing `http://` webhook targets to `https`
(the runbook has the query that lists them), and keep
`ACS_BSS_SECRET_KEY` safe, since losing it stops deliveries for sealed
subscriptions until each secret is rotated.

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
- The container images end to end. The final stage of the console image
  was built and run unprivileged and read-only; the stage that runs
  `npm ci`, and the four backend images, were not built.
- Five of the six pinned Compose digests were read from Docker Hub but
  the images were not pulled or started (Prometheus was).
- The `/metrics` token with the adapter and the USP controller running.
  Prometheus scraped the gateway and the API with the rendered
  configuration, and was refused with the plain one.
- The breached-password lookup against the real public service; it was
  tested against a stand-in that answers in the same format.
- CI on any of these pushes.

## 11. Loose ends

Closed since this was first written (15 November, after the merge of
pull request 104):

- **IDs that are not UUIDs.** The two adapter routes were fixed on `main`
  before the merge. `GET /devices/{id}/cli/connect` now answers 400 for a
  missing or malformed `credential_id`. It also refuses a credential that
  belongs to another device than the one in the path: the browser ticket
  names one device, and the credential was not checked against it.
- **Alert policies and same-numbered accounts.** A tenant-scoped alert
  policy named its account by ID alone, so it applied to every customer's
  account of that ID. A policy is now tied to one account (migration
  0102). Creating one for an ID that two customers use needs
  `customer_id` (409 without it); the console form has a customer picker.
  Incidents themselves were never mixed up: an incident is per device.
  A policy made earlier for an ID that several customers already share
  keeps matching all of them until it is recreated.
- **TMF641 orders resent with a different body** were already refused
  with 409 `ErrIdempotencyKeyReused`; the entry here was wrong.

Still open:

- **Compose `containerized` profile** still uses the shared database
  login. Separate logins there need a migration step of its own (a new
  image) and a place to create the roles; not started.
- **Device management plan** steps 2 to 10, and the optional console
  housekeeping.
- **Existing data is not brought up to the new rules**: operator
  passwords shorter than 12 characters, webhook secrets shorter than 32,
  and `http://` webhook targets keep working until they are changed.
  Whether to force a change is a decision, not a fix.
- **The shared request limit is approximate**: a caller can exceed it by
  what the replicas let through in the second before they exchange
  counts. This is how the design works, not a defect to fix.

## 12. Screenshots and older documents

- **Screenshots**: 32 screens of the current console in
  `docs/screenshots/2026-11-15/`, indexed in `docs/screenshots/README.md`.
  The September set in `docs/uat-screenshots/` is unchanged, because the
  UAT readiness report is a dated record built from it. The Grafana
  dashboards were not re-captured.
- **Older documents brought in line**: `docs/CONSOLE-REDESIGN-AND-MFA.md`
  (two of its "not done" items are now done), `docs/LAB-TLS.md` (console
  and API names must be the same site for the session cookie),
  `frontend/README.md` (session, tests, image port),
  `EC2-DEPLOYMENT-GUIDE.md` (`/metrics` with a token) and the settings
  list in `deployment-testing-onboarding-guide.md`.

## 13. Where the detail is

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
| `contract/README.md` | Contract 2.0.0: what changed, and the fixtures |
