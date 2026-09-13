# BSS Improvements (Sub-project C-1: Outbox, Action Set, DLQ) — Design

## 1. Purpose and driver

`backend/cmd/bssadapter`/`backend/internal/bss` is the BSS/CRM integration
adapter (REST API on `:8090`). Its programme slot — sub-project C, "BSS
improvements (outbox, action set, TMF640 shapes, DLQ)" — is named only as a
one-line table entry in
`docs/superpowers/specs/2026-09-09-usp-controller-design.md` §2 and
`docs/superpowers/specs/2026-09-09-fleet-data-model-design.md` §2; no design
existed for it before this document. Depends on sub-project A (fleet data
model), already complete and merged.

This document covers three of the four named items — **outbox, action set,
DLQ** — as sub-project **C-1**. TMF640 (an additive, TM-Forum-shaped API
surface alongside the existing custom JSON contract) is deliberately
out of scope here; it is a separable concern with no dependency on this
work, deferred to its own future sub-project (C-2).

A survey of the current code (see Decisions below for what it found)
confirmed the real gap: webhook delivery already has a solid outbox
(migration `0021_webhooks.sql`, `internal/bss/webhook.go`), but order
dispatch does not — `bss-integration-guide.md` itself states "a fully
exactly-once guarantee would need an outbox pattern, which is a future
hardening item, not current behavior" (line 400), and `HANDOFF.md`'s
backlog (item #32) names it explicitly. The action set is a hardcoded
3-case `switch`, contradicting the original build plan's stated intent
of a registry. Dead-lettering exists only as a bare `FAILED` status flag
on webhook deliveries, with nothing at all for failed orders.

## 2. Scope boundary with C-2 (TMF640)

This work does not touch `POST /bss/v1/orders`'s request/response JSON
shape, add any TMF640 resource type, or change the external API contract
in any way visible to a BSS caller. The HTTP response for a successful
order stays exactly what it is today (`order_tracking_id`/`command_key`/
`status`/`timestamp`); only the internal durability and retry mechanics
behind that response change.

## 3. Outbox: order dispatch idempotency

**Current gap**: `createOrder` (`cmd/bssadapter/main.go:597-698`) calls
`ACSClient.SetParameters` (a synchronous HTTP call to `cmd/api`, which
itself just enqueues a CWMP job and returns) and only writes the
`bss_orders` idempotency row *after* that call succeeds. A crash or DB
error between the two loses the fact that dispatch already happened — a
caller retrying the same `external_order_id` finds no existing order
(`FindOrder` returns not-found) and the action gets dispatched a second
time, an actual device-side double-execution, not merely a duplicate
log line.

**Fix**: reorder to a write-ahead pattern. `bss_orders` gains a `status`
column (`PENDING_DISPATCH` / `DISPATCHED` / `DEAD_LETTERED`), an
`attempts` counter, and a `last_error` text column. `createOrder`'s
sequence becomes:

1. Validate the request and translate the action (unchanged — this can
   still fail before anything is written, exactly as today).
2. Idempotency check via `FindOrder` on `external_order_id` (unchanged
   behavior: a duplicate returns the existing order's current
   status/`command_key` rather than re-dispatching).
3. **Insert** the `bss_orders` row with `status = 'PENDING_DISPATCH'`
   — this is new, and happens *before* calling `cmd/api`. The
   `external_order_id` primary key still gives the idempotency
   guarantee at write time; what's new is that intent is now durable
   before dispatch is attempted at all.
4. Call `SetParameters` synchronously, exactly as today.
5. On success: update the row to `status = 'DISPATCHED'` with the real
   `command_key`, and return the response the caller sees today
   (unchanged shape, unchanged synchronicity).
6. On failure: leave the row `PENDING_DISPATCH` (increment `attempts`,
   record `last_error`) and return an error to the caller as today —
   the difference is that a durable row now exists for the reconciler
   in §5 to retry, rather than nothing being written at all.

The HTTP contract for a successful order is byte-for-byte identical to
today's. Only a crash or transient failure between steps 3 and 5 changes
behavior — from "silently lost, and a retry double-dispatches" to
"durably recorded, and a reconciler retries exactly this order, not a
fresh duplicate."

## 4. Action set: from a hardcoded switch to a registry

**Current gap**: `bss.Translate` (`internal/bss/template.go:67-101`) is a
`switch` over three string literals. Adding a fourth action requires
editing this function directly; there is no single place that lists
"every action this system knows about" independent of the function body.

**Fix**: a Go-level registry — `map[string]ActionTranslator`, each value
a small function (or a struct bundling a validator + a translator
function) doing what one `case` arm does today. `MODIFY_WIFI`, `SUSPEND`,
`ACTIVATE` become the first three registered entries, their actual
per-action logic unchanged (same TR-181/TR-098 path selection, same
walled-garden-parameter handling). `Translate(action, params, ...)`
becomes a lookup into the registry; an unknown `action` string still
returns the existing `ErrUnsupportedAction`. This mirrors how
`internal/jobs`/`internal/subscriptions` already expose lookup tables
over a fixed, code-defined set of cases rather than hardcoded branching
— no new table, no admin UI. Adding a fourth action remains a deploy,
matching the cost of adding any other action type in this codebase
today (a CWMP or USP job type).

## 5. DLQ: dead-lettering for orders

**Current gap**: webhook deliveries already have a real (if minimal)
terminal `FAILED` status after `maxDeliveryAttempts` exhausted retries
(`internal/bss/webhook.go`) — left alone by this design, per the
decision below. Orders have *no* failure handling at all: a dispatch
failure today is a log line and nothing else (§3's gap).

**Fix**: a background reconciler (new file, e.g.
`cmd/bssadapter/order_reconciler.go`, structurally mirroring
`webhook_worker.go`'s poll-loop shape — a ticker, a bounded batch per
tick) periodically sweeps `bss_orders` rows in `PENDING_DISPATCH`
whose `last_error`/insertion time indicates they're past a grace
window (covers both "the request handler crashed before updating the
row" and "the dispatch call itself failed and needs a retry"). Each
sweep retries `SetParameters` with exponential backoff keyed off
`attempts` (mirroring `webhook.go`'s `2^attempts`-minutes backoff
window), and once `attempts` reaches a capped maximum, sets
`status = 'DEAD_LETTERED'` — a genuine terminal state, not a log
line. This directly mirrors `internal/jobs/lease.go`'s
`Requeued`/`DeadLettered` shape for CWMP jobs, the most mature
dead-lettering pattern already in this codebase.

No manual-requeue endpoint or admin UI is built — `internal/jobs`'s own
dead-lettering has none either (checked directly against `lease.go`
and every `cmd/api` handler file: dead-lettering there is purely
automatic, with no operator-facing requeue action). Visibility is via
`internal/bss/stats.go`, which gains a dead-lettered-order count
alongside its existing webhook-delivery-by-status aggregate, surfaced
through the same BSS admin panel path (`cmd/api`'s BSS admin handlers)
that already shows webhook stats — no new UI surface.

## 6. Testing and acceptance

- **Unit**: the action registry (replacing/extending
  `internal/bss/template_test.go` — same cases as today, now exercised
  through the registry lookup rather than the `switch`).
- **DB-backed** (`ACS_TEST_POSTGRES_DSN`, this codebase's established
  pattern — `internal/bss/mapping_test.go` is the model): the new
  `bss_orders` state-machine columns and transitions
  (`PENDING_DISPATCH`→`DISPATCHED`, `PENDING_DISPATCH`→`DEAD_LETTERED`
  after exhausting attempts), and the reconciler's sweep/backoff/
  dead-letter logic end to end against a real database.
- **Closing an existing gap while here**: `internal/bss/order.go`'s
  repository methods (`FindOrder`/`RecordOrder`/`UnnotifiedOrders`/
  `MarkOrderNotified`) currently have zero tests despite being exactly
  what this design's write-ahead sequencing depends on — add real
  coverage for them as part of this work, not as a separate task.
  `internal/bss/webhook.go`'s repository methods are similarly
  untested; leave them for a future pass unless this work happens to
  touch them (it should not need to, per the DLQ scope decision below).
- **Handler-level**: `createOrder`'s new sequencing (write-then-dispatch,
  the failure-leaves-a-retriable-row path) needs a test that wasn't
  possible before this design — currently `cmd/bssadapter` has zero
  tests for `createOrder` at all (survey finding); add coverage for the
  success path, the translation-rejection path (unchanged), and the new
  ACS-unreachable-leaves-PENDING_DISPATCH path.
- No change to `webhook_signature_test.go`, `mapping_test.go`, or any
  other currently-passing test's expected behavior.

## 7. Decisions made during brainstorming

- **TMF640 stays additive, not a replacement, and is deferred to C-2
  entirely** — the existing `/bss/v1/*` custom JSON contract has real
  integrators against it today; nothing in this design changes it.
- **C is split into C-1 (this document) and C-2 (TMF640)**, sequential,
  since outbox/action-set/DLQ are tightly coupled to the same
  order-dispatch pipeline while TMF640 is a separable, additive API
  surface with no dependency on this work.
- **The action set stays code-based, not DB-backed** — a registry
  replacing the hardcoded switch, not a new table + admin UI. Adding an
  action remains a deploy, consistent with how every other action/job
  type in this codebase is added today.
- **DLQ work is scoped to orders only**, not webhook deliveries —
  webhook delivery's `FAILED` status is an existing, working terminal
  state; extending it wasn't requested and isn't the gap this design
  targets.
- **No transactional (single-DB-transaction) outbox across `bssadapter`
  and `cmd/api`, despite both sharing one Postgres instance
  (`ACS_POSTGRES_DSN`)** — confirmed live that `bss_orders` deliberately
  stores `command_key TEXT`, not `job_id UUID REFERENCES jobs(id)`,
  specifically to preserve the process/service boundary between the two
  services (each owns its own tables; `bssadapter` never writes into
  `internal/jobs`' tables directly, even though it physically could).
  The write-ahead pattern in §3 respects that boundary while still
  closing the durability gap.
- **No manual-requeue tooling for dead-lettered orders** — matches
  `internal/jobs`' own dead-lettering precedent exactly, which has none
  either. A future request for one is a natural follow-up, not required
  here.

## 8. Out of scope

| Excluded | Reason |
|---|---|
| TMF640 API shapes | Sub-project C-2, separable and additive, no dependency on this work |
| DB-backed/operator-editable action registry | Code-based registry chosen instead (§7) |
| Dead-lettering for webhook deliveries | Existing `FAILED` status already serves this; not the gap targeted here |
| Manual requeue endpoint/UI for dead-lettered orders | No precedent for this in the codebase's existing dead-lettering (`internal/jobs`); not requested |
| Changes to the `/bss/v1/orders` request/response JSON contract | Explicitly preserved byte-for-byte (§2) |
