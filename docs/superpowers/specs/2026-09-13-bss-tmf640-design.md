# TMF640 Service Activation and Configuration (Sub-project C-2) — Design

## 1. Purpose and driver

Sub-project C ("BSS improvements") named four items: outbox, action set,
TMF640 shapes, DLQ. Outbox/action-set/DLQ shipped as sub-project C-1
(`docs/superpowers/specs/2026-09-13-bss-improvements-design.md`), which
deliberately deferred TMF640 as a separable, additive API surface with no
dependency on the outbox's internals beyond reusing them. This document
covers that remaining item, as sub-project C-2.

`backend/cmd/bssadapter`'s existing `/bss/v1/*` custom JSON contract is
untouched by this work — real integrators use it today, and nothing here
changes its request/response shapes, routes, or behavior.

## 2. A corrected premise

The original architecture note that named this work
(`tr069-acs-build-plan.md:294`, an aspiration never implemented) described
the target as "TM Forum / standard REST JSON (TMF640 Service Activation,
TMF638 Service Inventory)" and implicitly assumed an order-placement
shape — a `ServiceOrder` resource with `orderItem`s and an
acknowledged→inProgress→completed→failed lifecycle.

That description is **not what TMF640 actually is**, verified directly
against the TM Forum Open API swagger specification
(`tmforum-apis/TMF640_ActivationConfiguration`, v4.0.0). TMF640 is CRUD on
a `Service` resource (`GET`/`POST`/`PATCH`/`DELETE /service`), with a
separate `Monitor` resource (`GET /monitor`, `GET /monitor/{id}`) that
tracks the async execution of a request against that resource — a request/
response echo with its own `state` (`InProgress` / `Completed` /
`InError`), not a business order lifecycle. The `ServiceOrder`/`orderItem`/
acknowledged-inProgress-completed shape the original note assumed belongs
to a *different* TM Forum API, TMF641 (Service Ordering Management) — not
implemented here, not requested.

`Service.state` uses TM Forum's standard `ServiceStateType` enum:
`feasibilityChecked`, `designed`, `reserved`, `active`, `inactive`,
`terminated`.

## 3. Scope

**Built**: `GET /service`, `GET /service/{id}`, `PATCH /service/{id}`,
`GET /monitor/{id}` — the read, update, and async-tracking surface. This
is a thin translation layer over `internal/bss`'s existing repository and
outbox calls (C-1); it adds **no new dispatch logic**. `POST /service`
(create) and `DELETE /service` (delete) are explicitly **not** built —
see §7.

**Base path**: `/tmf-api/serviceActivationAndConfiguration/v4/`, matching
TM Forum's own standard versioned-path convention for this API family.

**Same process, same trust boundary**: mounted in `cmd/bssadapter`
alongside `/bss/v1/*`, behind the same `withAuth` (OAuth2 client
credentials or shared token) and rate-limiting middleware, unchanged. No
new service, no new database, no new migration.

## 4. Resource mapping

### 4.1 `Service` ↔ `account_device_mappings`

One `Service` resource per active mapping row
(`WHERE unassigned_at IS NULL`) — reusing sub-project A's existing
role-aware, temporal mapping model rather than inventing a new identity.

| TMF640 field | ACS source |
|---|---|
| `id` | the mapping's own `id` |
| `href` | `/tmf-api/serviceActivationAndConfiguration/v4/service/{id}` |
| `state` | always `"active"` — a mapping either exists (active) or doesn't; ACS has no `reserved`/`designed`/`feasibilityChecked` concept for an already-provisioned CPE |
| `serviceCharacteristic` | current-value reflection of the two writable characteristics this increment supports: `SSID`, `WiFiPassword` — a read, not a pending-write view |
| `relatedParty` | the mapping's `account_id` |
| `category` | `"customer facing service"` (fixed) |

`GET /service`/`GET /service/{id}` are pure reads — no outbox
involvement, no dispatch. Reading the two characteristics' current
values does **not** import `internal/parameters` directly (that would
break `bssadapter`'s established process-boundary discipline — it never
touches ACS-owned data except through `cmd/api`'s HTTP surface, the same
rule `internal/bss/acsclient.go`'s doc comment states and every existing
`ACSClient` method already follows). `cmd/api` already exposes
`GET /api/v1/devices/{id}/parameters?paths=<comma-separated>` for exactly
this (`cmd/api/device_handlers.go`'s `getParameters`) — `ACSClient` gains
one new method, `GetParameters(ctx, deviceID string, paths []string)
(map[string]CachedParameter, error)`, wrapping that call the same way
`GetDevice`/`GetJobStatus`/`SetParameters` already wrap their own
endpoints. The two paths requested are computed via
`internal/devices/adapters.ResolvePath(dataModelRoot, adapters.WiFiSSID)`/
`ResolvePath(dataModelRoot, adapters.WiFiKeyPassphrase)` — the exact same
canonical-parameter resolution `internal/bss/template.go`'s
`translateModifyWifi` already uses for the write side, so read and write
paths can never disagree about which TR-181/TR-098 path a characteristic
means. `internal/devices/adapters` is a pure, boundary-safe helper
package `internal/bss` already imports directly (unlike `internal/devices`
itself, which stays off-limits) — reusing it here is consistent with that
existing precedent, not a new exception. `feature`, `serviceRelationship`,
`supportingResource`, and the other TMF640 fields not listed above are
omitted (TMF's own `@schemaLocation`/`@baseType`/`@type` extensibility
pattern means omission of an unused field is valid, not an error).

### 4.2 `PATCH /service/{id}` — action interpretation

Requires header `X-Idempotency-Key` (**not part of the TMF640 spec
itself** — layered on top of it, a common REST convention already used by
systems like Stripe). Missing header → `400`. This header's value becomes
the `external_order_id` passed into the exact same `InsertPending` call
`/bss/v1/orders` already uses — TMF640 itself has no client-supplied
idempotency concept, and without layering one on, an ordinary network-level
retry of the same PATCH would create a fresh order and double-dispatch,
reopening the exact risk C-1's outbox exists to close.

The JSON merge-patch body is interpreted into **exactly one** of the three
actions the existing registry (`internal/bss/template.go`, C-1) already
knows:

| PATCH body shape | Interpreted action |
|---|---|
| `{"state": "inactive"}` | `SUSPEND` |
| `{"state": "active"}` | `ACTIVATE` |
| `{"serviceCharacteristic": [{"name": "SSID", "value": "..."}]}` or `{"name": "WiFiPassword", ...}` | `MODIFY_WIFI` |

A body that mixes a `state` change with `serviceCharacteristic` changes,
or names any other field, or names a `serviceCharacteristic` outside
`SSID`/`WiFiPassword`, is rejected with `400` — one recognized shape per
PATCH, matching the "one action per order" invariant the outbox and
action registry are already built around. This is a real, disclosed scope
limit (§7), not silently ignored.

The interpreted `(action, params)` pair is handed to the same call
sequence `createOrder` (`cmd/bssadapter/main.go`) already makes:
`bss.Translate` → `Repository.InsertPending` → `ACSClient.SetParameters`
→ `Repository.MarkDispatched`/`MarkDispatchFailed`. No new function
duplicates this sequence; the TMF640 handler calls the same internal
helper `createOrder` itself will be refactored to expose (see plan
Task decomposition — the dispatch sequence becomes a shared function
both the `/bss/v1/orders` handler and the new TMF640 handler call, rather
than being copy-pasted).

Response: `202` with a `Monitor` resource body (TMF640's own "server
provides monitor as POST/PATCH response" pattern), not the `Service`
resource itself.

### 4.3 `Monitor` ↔ `bss_orders`

`GET /monitor/{id}`, `id` = `external_order_id` (the same value the
`X-Idempotency-Key` header supplied). Reshapes `bss_orders`' existing
state, already tracked by C-1, into TMF640's envelope:

| `bss_orders.status` (+ underlying job status once `DISPATCHED`) | `Monitor.state` |
|---|---|
| `PENDING_DISPATCH` | `InProgress` |
| `DISPATCHED`, underlying job still running | `InProgress` |
| `DISPATCHED`, underlying job `SUCCESS` | `Completed` |
| `DISPATCHED`, underlying job `FAILED`/`TIMEOUT` | `InError` |
| `DEAD_LETTERED` | `InError` |

`sourceHref` = the `Service` resource's href. `response.statusCode`/
`response.body` are populated once the state is terminal (`Completed` or
`InError`), carrying the same information `GET /bss/v1/jobs/{command_key}`
(Workflow C) already exposes today, TMF-shaped. No new polling worker —
this is a read computed at request time from data the reconciler (C-1)
already maintains.

`GET /monitor` (the list form) is **not** built — TMF640 permits it, but
nothing in this design needs "list every monitor," and building it would
mean deciding pagination/filtering semantics for no real consumer. A
future increment can add it if a real need appears.

## 5. Testing and acceptance

- Unit: PATCH-body-to-action interpretation (all 3 recognized shapes,
  every rejection case — mixed fields, unknown characteristic, missing
  idempotency header).
- DB-backed (`ACS_TEST_POSTGRES_DSN`, established pattern): `GET /service`
  reflects real `account_device_mappings` rows; a `PATCH` genuinely reaches
  the same `bss_orders` write-ahead path `/bss/v1/orders` does (proven by
  asserting the resulting row, not just the HTTP response); `GET /monitor`
  reflects every state transition in the table above.
- No change to any existing `/bss/v1/*` test's behavior — confirmed by
  running the full existing suite unmodified.

## 6. Decisions made during brainstorming

- **Thin translation layer, not a parallel implementation** — TMF640
  handlers call the exact same `bss.Translate`/outbox sequence
  `/bss/v1/orders` already uses. No new dispatch logic, no new failure
  modes beyond what C-1 already hardened.
- **One `Service` per active mapping**, reusing sub-project A's
  role-aware model, not a new device-level identity scheme.
- **`X-Idempotency-Key` required on PATCH**, layered on top of the TMF640
  spec (which doesn't define one) specifically to preserve C-1's
  double-dispatch protection for this new entry point.
- **`POST`/`DELETE /service` not built** — neither maps onto how ACS
  actually provisions or deprovisions a device (zero-touch CWMP/USP
  onboarding; unassignment is Workflow A's concern), so building them
  would add API surface with no real behavior behind it.
- **One recognized PATCH shape per request, rejecting anything mixed or
  unrecognized** — matches the "one action per order" invariant already
  built into the action registry and outbox; not attempting to support
  arbitrary multi-field TMF640 patches in this increment.

## 7. Out of scope

| Excluded | Reason |
|---|---|
| `POST /service` (create), `DELETE /service` (delete) | No corresponding ACS operation (§6) |
| `GET /monitor` (list form) | No real consumer identified; add if one appears |
| Event subscription (`POST /hub`, `/listener/*` callbacks) | TMF640 supports webhook-style event notification; `bssadapter` already has its own webhook subscription mechanism (C-1's predecessor work) serving the same purpose for the existing contract — not duplicating it for TMF640 in this increment |
| New/additional business actions beyond `MODIFY_WIFI`/`SUSPEND`/`ACTIVATE` | Out of C-2's scope; the action registry (C-1) is where a new action would be added, independent of which API surface exposes it |
| A PATCH combining a state change with characteristic changes in one request | Rejected with 400 (§4.2) — a real, disclosed scope limit, not silently dropped |
| TMF641 (Service Ordering), TMF638 (Service Inventory), or any other TMF API | Explicitly not requested; this document covers TMF640 only |
