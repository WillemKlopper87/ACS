# TMF641 Service Ordering Management (Sub-project C-4) — Design

> **Delivery status:** Implemented on `main`. See [`docs/TMF-API-STATUS.md`](../../TMF-API-STATUS.md) for current routes and behavior; unbuilt statements below are historical scope.

## 1. Purpose and driver

`bss-integration-guide.md` §6 opens its known-limitations list with:

> **One primary device per account.** Order dispatch resolves the
> account's most recently active mapping. An account genuinely managing
> multiple devices needs a different order shape (not yet designed).

C-4 designs that order shape, and designs it as TMF641 rather than as
`/bss/v1/orders/v2`. The work has to happen either way; doing it as the
standard costs roughly the same and yields a published schema, a
generatable client, and an order lifecycle that BSS stacks already
understand.

## 2. A corrected premise

The guide's limitation is **half stale**, and the half that is stale is
the expensive half.

Migration 0052 (`0052_device_assignment_roles.sql`, sub-project A) already
replaced "most recently active mapping" with a role-addressed, temporal
model:

- `role` column, `CHECK (role IN ('gateway','ont','extender','stb','ata','other'))`
- `account_device_mappings_active_role_idx` — unique on
  `(account_id, role) WHERE unassigned_at IS NULL`, so *"the gateway for
  account X"* resolves to exactly one row or none
- `assigned_at` / `unassigned_at` / `unassign_reason` — assignment is
  temporal, history is rows with `unassigned_at` set

and `internal/bss/mapping.go` already exposes `ActiveDeviceForAccount(ctx,
accountID, role)`, `ErrNoDeviceForRole`, `AssignDevice`,
`UnassignDevice`, `SwapDevice` and `AssignmentHistory`.

So the *data model* for multi-device accounts is built, constrained and
tested. What is missing is only the **API shape to address it** — no
`/bss/v1` endpoint accepts a role, and none of `UnassignDevice`,
`SwapDevice` or `AssignmentHistory` is reachable over the BSS surface at
all. `POST /bss/v1/mappings` and `GET /bss/v1/mappings/{account_id}` are
the entire BSS-facing assignment API today.

This makes C-4 substantially cheaper than the guide implies, and its scope
different: C-4 is an API surface over existing, already-correct domain
operations, not a data-model project.

## 3. Scope

**Built**, at base path `/tmf-api/serviceOrdering/v4/`:

- `POST /serviceOrder` — submit an order with one or more `orderItem`s
- `GET /serviceOrder/{id}` — order with derived state and per-item state
- `GET /serviceOrder` — list, filtered by `relatedParty.id` and `state`
- `PATCH /serviceOrder/{id}` — restricted to `state: "cancelled"` on an
  order no item of which has dispatched (§6.3)

**Not built**: `DELETE /serviceOrder` (an order is a record of intent;
deleting it destroys the audit trail the outbox exists to provide),
`/hub` and `/listener` (C-5 builds one hub for all APIs), TMF641's
appointment, qualification and `orderRelationship` structures (no ACS
data behind them).

**Version**: v4, matching C-2's verified TMF640 v4.0.0 and C-3's TMF638
v4, per the programme-wide decision in
`2026-09-13-tmf638-service-inventory-design.md` §2.

**Verification requirement**: as with C-2 and C-3, the implementation plan
must check `TMF641_ServiceOrdering` v4.0.0's real field list and state
enum against the tables below before transcribing them, and correct this
document if they differ.

## 4. Resource mapping

### 4.1 `orderItem.action` → ACS operation

TMF641's `ServiceOrderItem.action` is `add` / `modify` / `delete` /
`noChange`. Each maps onto a domain operation that already exists:

| `action` | `service` payload carries | ACS operation | Exposed on `/bss/v1` today? |
|---|---|---|---|
| `add` | device identity (`oui_serial`), `role`, optional `service_plan` | `AssignDevice` | Yes — `POST /bss/v1/mappings` |
| `modify` | `serviceCharacteristic` `SSID`/`WiFiPassword`, **or** `state` | `Translate` → `dispatchOrder` (`MODIFY_WIFI`/`SUSPEND`/`ACTIVATE`) | Partly — `POST /bss/v1/orders`, no role addressing |
| `delete` | `role`, `unassign_reason` | `UnassignDevice` | **No** |
| `noChange` | — | accepted, no-op, item goes straight to `completed` | n/a |

A **device swap** (RMA, upgrade) is expressed the TMF way, as one order
containing a `delete` item and an `add` item for the same role. Because
`account_device_mappings_active_role_idx` forces close-before-open, the
two items must execute in that order — see §6.2. `SwapDevice` already
implements exactly this transaction, so a two-item order whose items are
a `delete` and an `add` on the same role is detected and routed to
`SwapDevice` as a single atomic call rather than two independent ones.
This is the one place C-4 collapses items rather than dispatching them
independently, and it is done because the constraint makes the
non-atomic version genuinely incorrect, not as an optimisation.

### 4.2 `ServiceOrder` resource

| TMF641 field | ACS source |
|---|---|
| `id` | generated UUID, the order's own identity |
| `href` | `/tmf-api/serviceOrdering/v4/serviceOrder/{id}` |
| `externalId` | client-supplied; the idempotency key (§6.1) |
| `state` | derived from item states (§5.2) — never stored |
| `orderDate` | `created_at` |
| `completionDate` | max of item completion times, once terminal |
| `relatedParty` | `account_id`, role `customer` |
| `orderItem[]` | one per `service_order_items` row |
| `note[]` | populated on failure with the item's `last_error` |

`priority`, `category`, `requestedStartDate`, `expectedCompletionDate` are
accepted on `POST` and echoed back, but **not acted on** — ACS has no
scheduling for BSS orders. Echoing without honouring is disclosed here
and in §8 rather than silently ignored; a client that needs scheduled
activation should use `cmd/api`'s existing scheduled-jobs mechanism, not
this API.

## 5. Schema and state

### 5.1 New tables

`bss_orders` is `external_order_id TEXT PRIMARY KEY` with a single
`action` per row. It cannot host a multi-item order, and widening it
would break C-1's outbox invariants and every existing `/bss/v1` caller.
C-4 adds a parent table and leaves `bss_orders` untouched as the
per-item dispatch record:

```sql
-- 00NN_service_orders.sql  (NN assigned at implementation time — next free number)
CREATE TABLE service_orders (
    id              UUID PRIMARY KEY,
    external_id     TEXT NOT NULL UNIQUE,   -- client idempotency key
    account_id      TEXT NOT NULL,
    cancelled_at    TIMESTAMPTZ,
    raw_request     JSONB NOT NULL,         -- echoed fields, audit
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX service_orders_account_idx ON service_orders (account_id);

CREATE TABLE service_order_items (
    id                UUID PRIMARY KEY,
    order_id          UUID NOT NULL REFERENCES service_orders(id) ON DELETE CASCADE,
    seq               INTEGER NOT NULL,     -- execution order within the order
    action            TEXT NOT NULL
                        CHECK (action IN ('add','modify','delete','noChange')),
    role              TEXT,                 -- addressed role, NULL for noChange
    mapping_id        UUID REFERENCES account_device_mappings(id),
    external_order_id TEXT REFERENCES bss_orders(external_order_id),
    status            TEXT NOT NULL DEFAULT 'PENDING'
                        CHECK (status IN ('PENDING','DISPATCHED','COMPLETED','FAILED','SKIPPED')),
    last_error        TEXT,
    completed_at      TIMESTAMPTZ,
    UNIQUE (order_id, seq)
);
CREATE INDEX service_order_items_order_idx ON service_order_items (order_id);
```

`external_order_id` is a nullable FK into `bss_orders`: only `modify`
items dispatch through the outbox, so `add`/`delete`/`noChange` items
leave it NULL. This is what keeps every dispatching item inside C-1's
existing write-ahead, reconciler-retry and dead-lettering guarantees
instead of getting a parallel path.

### 5.2 Derived order state

The parent's `state` is **computed at read time from its items and never
stored.** A stored parent state is a second state machine that can
disagree with the children; deriving it makes disagreement impossible.

| Item states | `ServiceOrder.state` |
|---|---|
| `cancelled_at` set | `cancelled` |
| all `PENDING` | `acknowledged` |
| any `DISPATCHED`, or a mix of `PENDING` and terminal | `inProgress` |
| all `COMPLETED` (or `SKIPPED`) | `completed` |
| all `FAILED` | `failed` |
| mix of `COMPLETED` and `FAILED`, none outstanding | `partial` |

Per-item state maps the same way onto TMF's
`ServiceOrderItemStateType`. A `modify` item's status is not stored
independently either — it is read through its `bss_orders` row and the
underlying job, reusing exactly the derivation C-2 §4.3 already defines
for `Monitor.state`. One derivation, three APIs.

`rejected`, `held`, `pending`, `assessingCancellation` and
`pendingCancellation` are never emitted: ACS has no approval workflow, no
hold mechanism and no asynchronous cancellation assessment. Emitting
states the system cannot actually enter would be a lie in the schema's
own vocabulary.

## 6. Execution semantics

### 6.1 Idempotency

`externalId` is `UNIQUE` and is the idempotency key. `POST /serviceOrder`
with an `externalId` that already exists returns **200 with the existing
order** and its current derived state — never a second order, never a
409. This mirrors `/bss/v1/orders`' existing behaviour on a repeated
`external_order_id`, which integrators already rely on.

Unlike C-2's TMF640 PATCH, no `X-Idempotency-Key` header is needed:
TMF641 has `externalId` in the resource body for exactly this purpose,
so the header C-2 had to layer on is unnecessary here. Each dispatching
item derives its own `bss_orders.external_order_id` deterministically as
`{order externalId}:{seq}`, so a reconciler replay is idempotent per item.

### 6.2 Ordering

Items execute in `seq` order, which is the order they appeared in the
request. Execution is **sequential, not parallel**, and stops at the
first `FAILED` item — remaining items become `SKIPPED`, and the order
derives to `partial` (or `failed` if nothing completed).

Sequential-and-halt is chosen over parallel-and-continue because the
role-uniqueness constraint makes item ordering semantically meaningful:
a `delete`-then-`add` swap is correct in that order and a constraint
violation in the other. A client wanting independent items can submit
independent orders.

### 6.3 Cancellation

`PATCH /serviceOrder/{id}` accepts exactly one body,
`{"state": "cancelled"}`, and succeeds only while **no item has left
`PENDING`**. Once anything has dispatched, cancellation returns `409`
with a TMF error envelope: ACS cannot recall a CWMP RPC already sent to a
device, and pretending otherwise would be the most dangerous lie in this
design. Any other PATCH body is `400`, matching C-2's one-recognized-shape
rule.

## 7. Testing and acceptance

- **Unit** (`internal/tmf`, no DB): request → item decomposition for each
  action; swap detection (`delete`+`add`, same role, two items) and its
  non-triggering near-misses (different roles; three items; `add`
  before `delete`); every row of §5.2's derivation table; cancellation
  body validation.
- **DB-backed** (`ACS_TEST_POSTGRES_DSN`): a two-item order writes one
  `service_orders` row and two `service_order_items`; a `modify` item
  genuinely writes a `bss_orders` row through the **same**
  `dispatchOrder` path `/bss/v1/orders` uses — asserted on the resulting
  row, not the HTTP response, following C-2's precedent; a duplicate
  `externalId` returns the original order and creates no second row; a
  failing item leaves later items `SKIPPED` and derives `partial`;
  cancellation after dispatch returns 409 and changes nothing.
- **Constraint test**: a swap order against an account whose role is
  already occupied exercises `SwapDevice`'s close-before-open and does
  not violate `account_device_mappings_active_role_idx`.
- **No behaviour change to `/bss/v1` or to C-2/C-3 routes**, proven by
  running the existing suite unmodified.

## 8. Out of scope

| Excluded | Reason |
|---|---|
| `DELETE /serviceOrder` | An order is an audit record; deleting it destroys the trail the outbox exists to keep. |
| Scheduling (`requestedStartDate`, `expectedCompletionDate`) | Accepted and echoed, **not honoured** — ACS has no BSS-order scheduler. Disclosed, not silent. |
| `priority`, `category` | Same: echoed, not acted on. |
| Appointment, qualification, `orderRelationship` | No ACS data behind them. |
| `/hub`, `/listener` | C-5. |
| Approval / hold workflows (`rejected`, `held`, `pending`) | No such mechanism exists; states never emitted. |
| Parallel item execution | Role-uniqueness makes ordering semantically meaningful. Independent items → independent orders. |
| Cancellation after dispatch | A sent CWMP RPC cannot be recalled. 409, explicitly. |
| New actions beyond the registry | `MODIFY_WIFI`/`SUSPEND`/`ACTIVATE` only; the registry (C-1, `internal/bss/template.go`) is where actions grow, independent of which API exposes them. `SUSPEND`/`ACTIVATE` remain gated on the walled-garden config, unchanged. |

## 9. Decisions made during brainstorming

- **The data model already supports multi-device** (0052) — C-4 is an API
  surface over existing correct operations, not a data-model project.
  This is the finding that most changes the sub-project's size.
- **Every dispatching item goes through C-1's outbox** via the shared
  `dispatchOrder` extracted in C-2 Task 3. No parallel dispatch path, so
  no new failure modes.
- **Parent state is derived, never stored** — one state machine, not two.
- **`modify` item state reuses C-2's `Monitor` derivation verbatim** —
  one derivation serving TMF640, TMF641 and (via C-5) alarms.
- **Swap is the one collapsed case**, routed to the existing atomic
  `SwapDevice` because the role constraint makes the non-atomic version
  incorrect.
- **`externalId` is the idempotency key**, no header needed — TMF641
  provides in-body what C-2 had to layer on.
- **TMF641 closes a real gap**: `UnassignDevice` and `SwapDevice` exist in
  the domain layer but are unreachable over the BSS API today. C-4 is the
  first surface to expose them.

## 10. Dependencies and sequencing

Depends on C-2 (the `dispatchOrder` extraction from Task 3, and
`Monitor` state derivation) and C-3 (`internal/tmf`, and the `Service`
serializer the `orderItem.service` payload reuses). C-3's
`serviceOrderItem` back-reference field becomes populatable once C-4
lands and should be filled then.

Sequence: **C-2 → C-3 → C-4 → C-5.**
