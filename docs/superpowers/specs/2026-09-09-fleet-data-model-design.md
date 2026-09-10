# Fleet Data Model — Design

**Date:** 2026-09-09
**Status:** Design approved; implementation plan not yet written
**Sub-project:** A (see *Programme context* below)

## 1. Purpose

Make device assignment **addressable by role** and **temporal**, so that:

- a BSS order targets a specific device deterministically, never by an
  arbitrary tiebreak;
- a device swap (RMA, upgrade, return) preserves history instead of
  overwriting it; and
- the USP controller (sub-project B) and the BSS improvements (sub-project C)
  have a shared, subscriber-aware view of which devices serve which account.

## 2. Programme context

| | Sub-project | Depends on |
|---|---|---|
| 0 | Defect fixes | — (complete, PR #16) |
| **A** | **Fleet data model (this document)** | — |
| B | USP controller | A |
| C | BSS improvements | A |

A is the shared foundation. B needs it because USP is designed for multi-device
homes; C needs it for `SWAP_DEVICE` and for deterministic order dispatch.

## 3. What actually exists today

This section corrects an earlier framing. The readiness assessment and the BSS
research described the platform as "one primary device per account". That is
not a schema limitation:

- `account_device_mappings` has `UNIQUE (account_id, device_id)` — unique on
  the *pair* — and `ListByAccount` already returns multiple rows.
- The real defect is **addressing**: order dispatch
  (`cmd/bssadapter/main.go`) calls `PrimaryDeviceForAccount`, which selects
  `WHERE status = 'ACTIVE' ORDER BY updated_at DESC LIMIT 1`. On a multi-device
  account, an order silently targets whichever device was most recently
  touched.

The three genuine gaps:

1. **No way to address a specific device** — no role, no explicit target.
2. **No assignment history** — the mapping row is mutable, so a swap
   overwrites the record a care agent later needs.
3. **`status` conflates two concerns** — it sits on the device mapping but
   carries subscription-lifecycle values (`PENDING_ACTIVE`, `ACTIVE`,
   `SUSPENDED`, `TERMINATED`).

Two vocabularies coexist and must not be confused: `customers` (tenancy) means
**ISP tenant** and is what operator scoping is built on; BSS `account_id` means
**subscriber**. This design touches only the subscriber side.

## 4. Decisions

Three decisions were taken during design and are settled inputs:

- **No service/subscription entity.** The BSS is the authority on what a
  subscription *is*. ACS records only which devices serve which account and in
  what role. Introducing a service entity would duplicate state ACS cannot keep
  truthful.
- **Orders without a role resolve to `gateway`.** Existing BSS callers keep
  working and gain deterministic behaviour. An account with no gateway
  assigned yields a typed error rather than a guess.
- **Evolve `account_device_mappings` in place** (approach A below). Not a
  separate history table (two sources of truth that drift), and not a new
  table with a compatibility view (largest migration for no gain at this
  scale).

## 5. Schema

One forward-only migration; number assigned at implementation time.

```sql
ALTER TABLE account_device_mappings
  ADD COLUMN role            TEXT NOT NULL DEFAULT 'gateway'
      CHECK (role IN ('gateway','ont','extender','stb','ata','other')),
  ADD COLUMN assigned_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
  ADD COLUMN unassigned_at   TIMESTAMPTZ,
  ADD COLUMN unassign_reason TEXT
      CHECK (unassign_reason IN ('rma','upgrade','return','moved','corrected'));

ALTER TABLE account_device_mappings
  DROP CONSTRAINT account_device_mappings_account_id_device_id_key;

-- A device is assigned to an account at most once *currently*; it may be
-- assigned, released and reassigned over time.
CREATE UNIQUE INDEX account_device_mappings_active_idx
  ON account_device_mappings (account_id, device_id)
  WHERE unassigned_at IS NULL;

-- One active device per role per account: "the gateway for account X"
-- resolves to exactly one row or none.
CREATE UNIQUE INDEX account_device_mappings_active_role_idx
  ON account_device_mappings (account_id, role)
  WHERE unassigned_at IS NULL;

CREATE INDEX account_device_mappings_device_active_idx
  ON account_device_mappings (device_id)
  WHERE unassigned_at IS NULL;
```

The constraint name in the `DROP CONSTRAINT` is Postgres's auto-generated
name for the unnamed inline `UNIQUE (account_id, device_id)` in migration
`0007`; no later migration has altered this table. The plan should still
assert the name against a live database before relying on it.

### 5.1 The temporal model

History is the set of rows with `unassigned_at` set. Current state is the set
with `unassigned_at IS NULL`. There is one table and one source of truth.

### 5.2 The role-unique index

`account_device_mappings_active_role_idx` is the constraint that makes
addressing safe. It also **forces close-before-open on swap**: a replacement
gateway cannot be inserted while the old one is active. That is the discipline
that prevents duplicate billing entries and cross-subscriber leakage, enforced
by the database rather than remembered by the application.

### 5.3 Known boundary: multiple extenders

`extender` is the one role that legitimately needs several per account, and
the role-unique index forbids that. This is a deliberate boundary, not an
oversight: mesh satellites are managed as a set, not addressed individually by
orders, and the safe common case is worth more than day-one mesh support. When
mesh becomes real, the change is an `instance` discriminator added to the
index, not the index's removal.

### 5.4 `status`

`status` keeps its column and values for API compatibility. **Nothing reads it
to decide whether a device currently serves an account** — `unassigned_at IS
NULL` is that answer. `SUSPENDED` is already refused in practice (suspension
belongs at the network layer). If sub-project C removes `SUSPENDED` from the
BSS contract, the column can be retired with it.

### 5.5 Backfill semantics

`role` backfills to `'gateway'`, so every existing single-device account keeps
working with no BSS change. `assigned_at` backfills to `now()` at migration
time; this is a **lower bound on a fact never previously recorded**, and must
be documented as such rather than presented as real history.

## 6. Role semantics and addressing

**Role is what a device does for this account, not what the device is.** The
same model can be a `gateway` at one address and an `extender` at another. Role
therefore lives on the assignment row and never on `devices`, which stays
protocol- and subscriber-neutral — a property sub-project B depends on.

**Resolution has one rule.** `ActiveDeviceForAccount(accountID, role)` returns
the single row matching `account_id`, `role`, and `unassigned_at IS NULL`, or
`ErrNoDeviceForRole`. No ordering, no tiebreak, no `LIMIT 1` hiding a
multiplicity.

`PrimaryDeviceForAccount` is **deleted**, not wrapped. Keeping it would leave
the arbitrary-tiebreak behaviour reachable, and it has exactly one caller.

## 7. The swap operation

```
BEGIN
  UPDATE account_device_mappings
     SET unassigned_at = now(), unassign_reason = $reason
   WHERE account_id = $acct AND role = $role AND unassigned_at IS NULL;
  INSERT INTO account_device_mappings (account_id, device_id, role, assigned_at, ...)
  VALUES ($acct, $new_device, $role, now(), ...);
COMMIT
```

Close, then open, in one transaction. Outside a transaction the role-unique
index would reject the insert after the update committed, leaving the account
with **no** gateway — worse than the defect being fixed. The index enforces the
ordering; the transaction makes it atomic.

The care-agent view — every assignment for an account by `assigned_at`, with
role, device, dates and reason — falls out of the same table.

**Deliberate omissions:** no quarantine state (a device with no active row *is*
unassigned; a status enum would be a second source of truth), and no
`SWAP_DEVICE` BSS action here (that is sub-project C; A supplies the repository
operation it calls).

## 8. Repository API (`internal/bss`)

| Operation | Notes |
|---|---|
| `AssignDevice(ctx, accountID, ouiSerial, role, servicePlan)` | Replaces `CreateMapping`. Fails on the role-unique index when that role is already filled. |
| `ActiveDeviceForAccount(ctx, accountID, role)` | Replaces `PrimaryDeviceForAccount` (deleted). Returns `ErrNoDeviceForRole`. |
| `UnassignDevice(ctx, accountID, role, reason)` | Sets `unassigned_at` and `unassign_reason`. |
| `SwapDevice(ctx, accountID, role, newOUISerial, reason)` | The §7 transaction. |
| `ListByAccount(ctx, accountID)` | Now returns active assignments only. |
| `AssignmentHistory(ctx, accountID)` | New. All rows for the account ordered by `assigned_at`. |

**Two existing queries must gain `WHERE unassigned_at IS NULL` or they break
quietly.** `ListAll` (admin onboarding view) would show released assignments as
current. `Stats` would count history and drift upward indefinitely — a metric
that looks plausible while being wrong.

### 8.1 BSS API surface

`GET /bss/v1/mappings/{account_id}` returns active assignments and gains a
`role` field per entry. `POST /bss/v1/mappings` accepts an optional `role`
(default `gateway`). `POST /bss/v1/orders` accepts an optional `role`
(default `gateway`). Assignment history is exposed to the operator console;
whether it is exposed on `/bss/v1` is a sub-project C decision.

### 8.2 Tenancy

Untouched. Operator scoping flows `operator_scopes → customers →
devices.customer_id`; none of that reads the mapping table. Assignments add no
scoping surface.

## 9. Testing

The behaviour that must not regress silently is the constraint behaviour:

- the role-unique index rejects a second active device in the same role;
- `SwapDevice` is atomic under that index;
- a released device can be reassigned to the same or another account;
- `AssignmentHistory` ordering;
- `ErrNoDeviceForRole` when a role is unfilled;
- `ListAll` and `Stats` exclude released assignments.

These are DB-backed tests. **CI's DB-backed job currently runs only
`-run Integration ./cmd/api/ ./cmd/acs/`**, so a test placed in `internal/bss`
would pass locally and never run in CI — already true of four existing test
groups. The implementation plan must **extend CI's run list** to include
`./internal/bss/` (and, as a side effect, the other orphaned DB-backed groups)
rather than relocate tests away from the code they cover.

## 10. Out of scope

| Excluded | Reason |
|---|---|
| A service/subscription entity | BSS owns subscription state (§4). |
| `SWAP_DEVICE` / `DECOMMISSION` BSS actions | Sub-project C; A supplies the repository operations. |
| Multiple extenders per account | Deliberate boundary (§5.3). |
| Quarantine state | Redundant with "no active assignment". |
| Removing `status` | Deferred until C settles the BSS contract. |
| Changes to `devices` or tenancy | Role lives on the assignment, not the device. |

## 11. Risks

| Risk | Mitigation |
|---|---|
| Existing BSS caller has an account with several active mappings today | Backfill sets every row to `gateway`; the role-unique index creation will **fail** on such data. The migration must detect this and the plan must specify handling (pre-UAT: none expected; verify before applying). |
| `assigned_at` backfill mistaken for real history | Documented as a lower bound (§5.5); operator console labels backfilled rows. |
| Constraint tests never run in CI | CI run list extended (§9). |
| Swap implemented as two statements | `SwapDevice` is the only write path; unit test asserts atomicity by injecting an insert failure and checking the release rolled back. |

## 12. Open questions

1. **Migration behaviour on conflicting data.** If any account has two active
   mappings when the role-unique index is created, the migration fails. The
   plan must decide between failing loudly (recommended pre-UAT) and a
   pre-migration step that marks all but the newest as released with reason
   `corrected`.
2. **Whether `AssignmentHistory` is exposed on `/bss/v1`** — deferred to C.
