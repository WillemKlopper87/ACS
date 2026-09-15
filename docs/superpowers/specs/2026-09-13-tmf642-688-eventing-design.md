# TMF642 Alarm + TMF688 Event Management (Sub-project C-5) — Design

> **Delivery status:** Implemented on `main`. See [`docs/TMF-API-STATUS.md`](../../TMF-API-STATUS.md) for current routes and behavior; unbuilt statements below are historical scope.

## 1. Purpose and driver

C-5 is the only sub-project in the TMF programme that **retires existing
code rather than adding a parallel surface**. `cmd/bssadapter` already
has a webhook subscription mechanism, an HMAC-signed delivery outbox with
retry, and exactly one event type (`JOB_COMPLETED`). That mechanism is
bespoke: every integrator writes a custom receiver, and nothing in it is
reusable by an operator assurance stack.

C-5 does two things:

1. **TMF642** gives CWMP faults and USP notifications an alarm model —
   severity, raise/clear lifecycle, deduplication — which ACS does not
   have today in any form. This is genuinely new capability, not a
   reshaping of existing data.
2. **TMF688** puts a standard subscribe/notify hub over the delivery
   infrastructure that already exists, and carries alarm, service and
   order events through it.

## 2. A corrected premise

It would be easy to describe C-5 as "wrap the existing webhook in TMF
shapes". That understates one half and overstates the other.

**Understated**: ACS has no alarm concept anywhere. `jobs.fault_code` /
`fault_string` record that *one job* failed; `device_events` records that
a USP device sent *one notification*. Neither has severity, neither has a
cleared state, and neither deduplicates — a device flapping for an hour
produces hundreds of independent rows and, today, hundreds of independent
`JOB_COMPLETED` webhooks. Turning that into alarms is a real modelling
job with real decisions (§5), not a serialization change.

**Overstated**: the *delivery* half genuinely is mostly reshaping.
`webhook_subscriptions` is already a hub in all but name — scoped by
`account_id`, targeting a URL, filtered by `event_types[]` — and
`webhook_deliveries` is already a retrying outbox with a partial pending
index. TMF688's hub maps onto them closely enough that C-5 builds no new
delivery infrastructure.

## 3. Scope

**Built**:

- `/tmf-api/alarmManagement/v4/` — `GET /alarm`, `GET /alarm/{id}`,
  `PATCH /alarm/{id}` (acknowledge / clear / comment)
- `/tmf-api/event/v4/` — `GET /event`, `GET /event/{id}`, and the hub:
  `POST /hub`, `GET /hub`, `GET /hub/{id}`, `DELETE /hub/{id}`
- An `alarms` table with raise/clear/dedup semantics (§5)
- Alarm derivation from **both** CWMP faults and USP events (§4)
- TMF688 event envelopes for alarm, service and service-order lifecycle
  events, delivered through the existing outbox (§6)

**Not built**: `POST /alarm` (alarms are raised by the system from
observed faults, not submitted by clients), `DELETE /alarm` (cleared, not
deleted — the history is the point), TMF642's bulk operations
(`POST /ackAlarms`, `/clearAlarms`, `/unAckAlarms`) which have no
identified consumer, and `POST /event` (events are emitted, not injected).

**Version**: v4 for both, per the programme-wide decision.

**Verification requirement**: as with C-2/C-3/C-4, check
`TMF642_AlarmManagement` and `TMF688_EventManagement` v4.0.0 against the
tables below before transcribing, and correct this document if they
differ. TMF688's hub shape in particular (`{id, callback, query}`) should
be confirmed rather than assumed.

## 4. Alarm sources

Three sources, all already persisted. No new collection path, no agent
change, no protocol work.

| Source | Table | Becomes |
|---|---|---|
| CWMP / USP job failure | `jobs` where `status='FAILED'`, carrying `fault_code`/`fault_string` | An alarm per (device, fault code) |
| USP notification | `device_events` (`event_name`, `obj_path`, `params`), from B-3c | An alarm only for the subset in §4.2 |
| BSS order dead-lettered | `bss_orders` where `status='DEAD_LETTERED'` | An alarm per order |

### 4.1 CWMP fault → alarm

`fault_code` is the CWMP fault number (9001 request denied, 9002 internal
error, 9003 invalid arguments, 9005 invalid parameter name, 9007 notify
rejected, and so on). Mapping to `perceivedSeverity`:

| CWMP fault class | `perceivedSeverity` | Rationale |
|---|---|---|
| 9001, 9002, 9004 (resource exceeded), 9800+ vendor | `major` | Device-side failure the operator must act on |
| 9003, 9005, 9006, 9007 | `minor` | Almost always an ACS-side request defect, not a device fault |
| Job `FAILED` with no fault code (timeout, no session) | `warning` | Frequently transient — a device that is simply offline |

`alarmType` is `equipmentAlarm` for the 9001/9002/9004 class and
`processingErrorAlarm` for the rest, per ITU-T X.733's categories which
TMF642 inherits. `probableCause` carries the fault code, `specificProblem`
the fault string.

The severity table is a judgement call and is expected to need tuning
against real fleet data. It is defined in one map in `internal/tmf` so
tuning is a one-line change, not a hunt.

### 4.2 USP event → alarm

**Most USP notifications are not alarms**, and treating them as such
would drown the alarm list. `ValueChange` and `ObjectCreation` are normal
operation. C-5 raises alarms only for:

- `event_name = 'Boot!'` with a boot cause indicating an unexpected
  restart → `warning`, `alarmType` `equipmentAlarm`
- `event_name = 'Periodic!'` **absent** beyond the expected interval —
  see §5.3, this is a derived absence alarm, not a received event
- vendor `Event!` notifications whose `obj_path` matches an
  operator-configured allowlist → severity from that configuration

Everything else in `device_events` remains queryable as a TMF688 `Event`
(§6.2) without becoming an `Alarm`. The distinction is the point: events
are a log, alarms are a work queue.

## 5. The `alarms` table

None of the three sources has raise/clear semantics, so C-5 adds them.

```sql
-- 00NN_alarms.sql  (NN assigned at implementation time — next free number)
CREATE TABLE alarms (
    id                 UUID PRIMARY KEY,
    correlation_key    TEXT NOT NULL,       -- see 5.1
    device_id          UUID REFERENCES devices(id),
    account_id         TEXT,                -- denormalised at raise time
    alarm_type         TEXT NOT NULL,
    perceived_severity TEXT NOT NULL
        CHECK (perceived_severity IN
            ('critical','major','minor','warning','indeterminate','cleared')),
    probable_cause     TEXT,
    specific_problem   TEXT,
    source_kind        TEXT NOT NULL
        CHECK (source_kind IN ('cwmp_fault','usp_event','order_dead_letter')),
    source_ref         TEXT,                -- job id / device_events id / order id
    occurrences        INTEGER NOT NULL DEFAULT 1,
    state              TEXT NOT NULL DEFAULT 'raised'
        CHECK (state IN ('raised','updated','cleared')),
    ack_state          TEXT NOT NULL DEFAULT 'unacknowledged'
        CHECK (ack_state IN ('unacknowledged','acknowledged')),
    acked_by           TEXT,
    raised_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    changed_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    cleared_at         TIMESTAMPTZ,
    clear_reason       TEXT
        CHECK (clear_reason IN ('auto_success','auto_timeout','manual','device_replaced')),
    comments           JSONB NOT NULL DEFAULT '[]'
);

-- At most one uncleared alarm per correlation key. This is the
-- deduplication guarantee; a repeat updates that row instead of inserting.
CREATE UNIQUE INDEX alarms_active_correlation_idx
    ON alarms (correlation_key) WHERE cleared_at IS NULL;

CREATE INDEX alarms_device_idx  ON alarms (device_id, raised_at DESC);
CREATE INDEX alarms_account_idx ON alarms (account_id, raised_at DESC);
CREATE INDEX alarms_open_idx    ON alarms (perceived_severity, raised_at DESC)
    WHERE cleared_at IS NULL;
```

### 5.1 Deduplication

`correlation_key` is `{source_kind}:{device_id}:{alarm_type}:{probable_cause}`.
The partial unique index makes "at most one open alarm per key" a database
guarantee rather than application discipline — the same technique
`account_device_mappings_active_role_idx` (0052) already uses for role
uniqueness, and the same technique the pending-delivery partial indexes
use. A repeat occurrence performs an upsert that increments `occurrences`,
sets `changed_at`, and moves `state` to `updated`.

This is the mechanism that turns an hour of flapping into one alarm with
`occurrences: 340`, and it is the single most important design element in
the sub-project.

### 5.2 Clearing

Every alarm must have a defined clear path, or the alarm list becomes
write-only. The rules:

| Alarm | Cleared when | `clear_reason` |
|---|---|---|
| CWMP fault on a job type | A later job of the same type on the same device reaches `SUCCESS` | `auto_success` |
| Order dead-lettered | The order is manually requeued and dispatches | `auto_success` |
| Unexpected boot | Next successful session after a quiet period | `auto_success` |
| Missed-Periodic (§5.3) | Next `Inform`/Notify received from the device | `auto_success` |
| Any alarm on a device whose mapping is unassigned | Immediately | `device_replaced` |
| Anything else | Operator `PATCH /alarm/{id}` | `manual` |

Auto-clear is evaluated in the same background worker that already polls
for terminal jobs to notify on (`webhook_worker.go`), not in a new
process — it is one more query on a tick that already runs.

### 5.3 The one derived alarm

Missed-Periodic is the only alarm raised from an *absence* rather than an
event: a device whose `PeriodicInformInterval` has elapsed by a
configurable multiple without checking in. It is included because "the
CPE went dark" is the single most operationally useful alarm an ACS can
raise, and no source event exists for it by definition.

It is computed by the same worker tick against `devices.last_inform_at`,
raised at `warning`, escalating to `major` after a second configurable
threshold. Both thresholds are operator configuration with conservative
defaults, because a fleet-wide misconfiguration here would raise an alarm
per device.

### 5.4 Retention

`internal/retention` already exists and handles time-based pruning for
other tables; cleared alarms join it with their own retention window
(default 90 days). Open alarms are never pruned — an alarm old enough to
prune while still open is itself the signal.

## 6. TMF688 hub and events

### 6.1 Hub over `webhook_subscriptions`

`POST /hub {callback, query}` becomes a `webhook_subscriptions` row:

| TMF688 | Column |
|---|---|
| `callback` | `target_url` |
| `query` (e.g. `eventType=AlarmCreateEvent`) | parsed into `event_types[]` |
| `id` | `id` |
| account scope (vendor extension, §6.3) | `account_id` (NULL = fleet-wide, unchanged) |

One column is added:

```sql
ALTER TABLE webhook_subscriptions
    ADD COLUMN kind TEXT NOT NULL DEFAULT 'legacy'
        CHECK (kind IN ('legacy','tmf688'));
```

`DEFAULT 'legacy'` backfills every existing subscription correctly, so no
current integrator is affected. **This column is what makes the
`/bss/v1` deprecation decision genuinely deferrable**: the one delivery
worker reads `kind` and emits either the existing `JOB_COMPLETED` body or
a TMF688 envelope to the same outbox, for as long as both need to exist.

### 6.2 Event types emitted

| TMF688 `eventType` | Raised on |
|---|---|
| `AlarmCreateEvent` | New alarm (not a dedup increment) |
| `AlarmStateChangeEvent` | Alarm cleared, or acknowledged |
| `AlarmAttributeValueChangeEvent` | Severity escalation, or `occurrences` crossing a configured threshold |
| `ServiceStateChangeEvent` | Mapping `status` change (C-3's derived state) |
| `ServiceCreateEvent` / `ServiceDeleteEvent` | Device assigned / unassigned |
| `ServiceOrderStateChangeEvent` | C-4 order reaching a terminal derived state |
| `ServiceOrderCreateEvent` | C-4 order accepted |

`ServiceOrderStateChangeEvent` on a terminal order is the **TMF-shaped
equivalent of today's `JOB_COMPLETED`**, and the two are emitted from the
same worker tick for the same underlying transition — which is what
allows an integrator to migrate one subscription at a time.

A dedup increment deliberately does **not** emit an event. Emitting on
every occurrence would reproduce, at the notification layer, exactly the
flooding the alarm table was designed to prevent.

### 6.3 Two honest deviations

**Signing.** TMF688's `Hub` has no field for a shared secret, but
dropping HMAC signing to gain conformance would be a security regression.
C-5 generates a secret on `POST /hub`, returns it **once** in the creation
response under a vendor-namespaced field, and keeps signing deliveries
with the existing `X-Webhook-Signature` header. Documented as a
deviation; the alternative was worse.

**Account scoping.** `webhook_subscriptions.account_id` scopes a
subscription to one account, which TMF688's `query` cannot express in a
way generic clients agree on. C-5 accepts it as a vendor-namespaced field
on `POST /hub`, defaulting to fleet-wide (NULL), preserving today's
behaviour.

## 7. Testing and acceptance

- **Unit** (`internal/tmf`, no DB): every row of §4.1's severity table;
  the §4.2 USP filter, including that `ValueChange` does **not** produce
  an alarm; TMF688 envelope shape per event type; `query` string parsing,
  including an unparseable query (400) and an empty one (all types).
- **DB-backed** (`ACS_TEST_POSTGRES_DSN`): raising the same
  `correlation_key` twice produces **one** row with `occurrences = 2` —
  the central dedup guarantee, asserted at the database level;
  concurrent raises of the same key do not violate
  `alarms_active_correlation_idx`; each clear rule in §5.2 fires; a
  cleared alarm followed by a new occurrence produces a *new* row, not a
  resurrection.
- **Delivery**: a `legacy` subscription and a `tmf688` subscription on
  the same account both receive their own correctly-shaped body for one
  underlying transition, from one worker tick. This is the test that
  proves the deprecation decision stayed deferred.
- **Regression**: every existing webhook test passes unmodified, and an
  existing subscription row (backfilled `kind='legacy'`) behaves exactly
  as before.
- **Load sanity**: 500 rapid faults on one device produce one alarm and
  at most the configured number of events — asserted, because this is the
  failure mode that makes alarm systems unusable.

## 8. Out of scope

| Excluded | Reason |
|---|---|
| `POST /alarm` | Alarms are raised from observed faults, not client-submitted. |
| `DELETE /alarm` | Cleared, never deleted — history is the point. Retention prunes. |
| TMF642 bulk ops (`/ackAlarms`, `/clearAlarms`, `/unAckAlarms`) | No identified consumer; add if one appears. |
| `POST /event` | Events are emitted, not injected. |
| Operator console UI for acknowledgement | `PATCH /alarm/{id}` is the API; no console work in this sub-project. Real gap, stated. |
| Root-cause analysis, `correlatedAlarm`, `isRootCause` | Modelled as fields, never populated. Correlation across devices is a genuinely hard problem and not one this sub-project pretends to solve. |
| `affectedService` on alarms | Not a dependency problem — C-3 and C-4 are both landed by then. It is a modelling question this sub-project does not answer: a device-level fault on a gateway plausibly affects every service on the account, and deciding the blast radius correctly needs a service-impact model that does not exist. Emitting a guess would be worse than omitting the field. |
| Alarm-driven automation | Alarms are reported, never acted on automatically. |

## 9. Decisions made during brainstorming

- **Alarms are a work queue, events are a log** — the §4.2 filter exists
  because treating every USP notification as an alarm makes the alarm
  list useless.
- **Deduplication is a database guarantee**, not application discipline —
  a partial unique index, matching the technique 0052 and the delivery
  outbox already use.
- **Every alarm has a defined clear path** (§5.2), or the list becomes
  write-only.
- **Missed-Periodic is included** despite being the only absence-derived
  alarm, because "the CPE went dark" is the most operationally useful
  thing an ACS can tell you.
- **The `kind` column is the deprecation hedge** — one worker, two body
  shapes, `DEFAULT 'legacy'` so nothing existing changes.
- **Two deviations taken deliberately** (§6.3): HMAC signing retained
  over strict conformance; account scoping kept as a vendor extension.
- **No new delivery infrastructure** — `webhook_deliveries` already
  retries with a partial pending index.
- **A dedup increment emits no event** — otherwise the flood moves from
  the table to the wire.

## 10. Dependencies and sequencing

Depends on C-3 (`internal/tmf`) and, for `ServiceOrder*` events, C-4.
The alarm half depends on neither and could in principle be built first;
it is sequenced last anyway because it is the only sub-project touching
the existing webhook contract, and that risk is best taken once the
other three surfaces are stable.

Largest of the three by a clear margin — the alarm model, its clear
rules, the derived Missed-Periodic alarm and the dual-emission worker are
each substantial on their own. Expect it to need its own decomposition at
the implementation-plan stage.

Sequence: **C-2 → C-3 → C-4 → C-5.**
