# TMF638 Service Inventory Management (Sub-project C-3) — Design

## 1. Purpose and driver

Sub-project C-2 (`docs/superpowers/specs/2026-09-13-bss-tmf640-design.md`)
builds TMF640 Service Activation and Configuration: `GET`/`PATCH` on a
`Service` resource plus a `Monitor` for async tracking. C-2's §7 records
TMF638 as explicitly out of its scope — "not requested". This document
requests it, as sub-project C-3.

The driver is not standards completeness. It is that C-2 must already
build a `Service` serializer, and TMF638 is the *same SID `Service`
resource* read through a different API. Building 638 immediately after
C-2, while that code is fresh, costs a serializer reuse plus query
parameters. Building it a year later costs a second serializer that
drifts from the first the day someone patches one of them.

TMF638 also gives BSS integrators the one thing `/bss/v1` has never
offered: a searchable inventory. Today `GET /bss/v1/mappings/{account_id}`
answers "what devices does this account have" and nothing answers "which
accounts have a suspended service", "show me page 3", or "give me only
the `id` and `state` fields".

## 2. Scope

**Built**: `GET /service` (with TMF630 filtering, pagination and field
selection) and `GET /service/{id}`, read-only, at base path
`/tmf-api/serviceInventoryManagement/v4/`.

**Also built, and the reason this is not purely additive**: the
`internal/tmf` package extraction described in §5. This is the structural
change C-3 carries on behalf of C-3, C-4 and C-5 together.

**Not built**: `POST`, `PATCH`, `DELETE /service`. TMF638 is an inventory
API; in TM Forum's own decomposition, writes to a `Service` go through
TMF640 (activation) or TMF641 (ordering), both of which are covered
elsewhere in this programme. Also not built: the `/hub` and `/listener`
notification endpoints TMF638 defines — those are C-5's subject, built
once for all APIs rather than per-API.

**Version choice**: TMF638 is published at both v4.0.0 and v5.0.0. This
design targets **v4**, matching the v4 that C-2 verified for TMF640, so
the two `Service` resources on the same host are the same schema
generation. Adopting v5 for one API and v4 for another would mean two
incompatible `Service` shapes served from one process, which is worse
than being one version behind. Revisit as a programme-wide move, not
per-API.

**Verification requirement before implementation**: C-2 set the standard
here by verifying TMF640's shape directly against the TM Forum swagger
rather than working from memory, and correcting a wrong premise in the
original architecture note as a result. The implementation plan for C-3
must do the same — check `TMF638_ServiceInventory` v4.0.0's actual field
list and query conventions before transcribing the tables below, and
correct this document if they differ.

## 3. Relationship to C-2

C-3 depends on C-2 landing. It shares C-2's `Service` field mapping
(`2026-09-13-bss-tmf640-design.md` §4.1) unchanged: one `Service` per
active `account_device_mappings` row (`WHERE unassigned_at IS NULL`),
`id` = the mapping's own id, `relatedParty` = `account_id`, `category` =
`"customer facing service"`, `serviceCharacteristic` = current `SSID`
value read through `ACSClient.GetParameters`.

**Correction:** an earlier draft of this section (and this line) named
`WiFiPassword` here too, matching C-2's *original* field mapping. That
mapping changed mid-implementation: a security review of C-2 found `GET
/service` reflecting the device's live WiFi passphrase in cleartext to
any authenticated BSS integrator, and it was redacted (C-2 design §4.1's
own correction, commit `4c4103b` on `design/bss-tmf640`) — reads never
resolve or return `WiFiPassword` at all; only `PATCH` can still write it.
"Shares C-2's mapping unchanged" therefore now means: `serviceCharacteristic`
carries `SSID` only, on both APIs. Building C-3's read path against the
stale two-characteristic mapping would reopen the exact exposure C-2
closed, through a second API surface — and would immediately fail this
document's own §6 acceptance test (byte-identical `Service` from both
APIs), just not until implementation time. `role` (§4.1) remains the only
other `serviceCharacteristic` C-3 adds.

Two fields differ, and both are deliberate:

| Field | C-2 (TMF640) | C-3 (TMF638) | Why |
|---|---|---|---|
| `href` | `/tmf-api/serviceActivationAndConfiguration/v4/service/{id}` | `/tmf-api/serviceInventoryManagement/v4/service/{id}` | `href` is self-referential per TMF630 — a resource served from two APIs carries the href of the API that served it. Both are correct; neither is canonical. |
| `state` | always `"active"` | derived (§4.2) | C-2 hardcodes `active` because a mapping either exists or doesn't. C-3 has the same rows available but a genuine reason to distinguish, since inventory is where "show me suspended services" is asked. |

Everything else is one serializer, called from two places.

## 4. Resource mapping

### 4.1 `serviceRelationship` — the multi-device account

C-2 omits `serviceRelationship`. C-3 populates it, and this is the
substantive modelling addition of the sub-project.

Migration 0052 (`0052_device_assignment_roles.sql`, sub-project A) gave
`account_device_mappings` a `role` column
(`gateway`/`ont`/`extender`/`stb`/`ata`/`other`) with a unique index
guaranteeing at most one active device per role per account. An account
with a gateway, an ONT and two extenders is therefore already
representable — and already correctly constrained — in the data model.

TMF638 expresses this natively. Each mapping is its own `Service`; the
account's services reference each other through `serviceRelationship`:

```json
{
  "id": "3f2a…",
  "@type": "Service",
  "category": "customer facing service",
  "state": "active",
  "relatedParty": [{ "id": "ACCT-10023", "role": "customer" }],
  "serviceCharacteristic": [
    { "name": "role", "valueType": "string", "value": "extender" }
  ],
  "serviceRelationship": [
    { "relationshipType": "dependency", "service": { "id": "9c1b…",
      "href": "/tmf-api/serviceInventoryManagement/v4/service/9c1b…" } }
  ]
}
```

The relationship rule is deliberately narrow: **every non-gateway service
on an account declares one `dependency` relationship onto that account's
gateway service; the gateway declares none.** An extender genuinely
depends on the gateway. This is not a general graph, and C-3 does not
invent relationships the data cannot support — if an account has no
active gateway role, its other services simply carry an empty
`serviceRelationship`, which is valid.

`role` is additionally surfaced as a `serviceCharacteristic` so a client
can filter on it without traversing relationships.

### 4.2 `state` derivation

`account_device_mappings.status` already has a `CHECK` constraint
admitting `PENDING_ACTIVE`, `ACTIVE`, `SUSPENDED`, `TERMINATED`
(migration 0007). TMF's `ServiceStateType` enum is
`feasibilityChecked`, `designed`, `reserved`, `active`, `inactive`,
`terminated`. The mapping is:

| `account_device_mappings.status` | TMF `state` |
|---|---|
| `PENDING_ACTIVE` | `reserved` |
| `ACTIVE` | `active` |
| `SUSPENDED` | `inactive` |
| `TERMINATED` | `terminated` |

Note the interaction with temporality: C-2's projection filters
`unassigned_at IS NULL`, so `TERMINATED` rows are in principle reachable
only while still assigned. C-3 keeps that same filter by default —
inventory means *current* inventory — and does not expose unassigned
history. Exposing assignment history is a real capability, but it needs
its own decision about whether a released device is a `Service` at all;
it is out of scope here (§7).

`feasibilityChecked` and `designed` are never emitted. ACS has no
pre-provisioning lifecycle; claiming otherwise would be a lie in the
schema's own vocabulary.

### 4.3 Query parameters

TMF630 Part 2 defines the filtering conventions. C-3 supports the subset
with a real consumer, per the pragmatic-conformance decision:

| Parameter | Behaviour |
|---|---|
| `relatedParty.id` | Filter by `account_id`. The most important one — this is the TMF equivalent of today's `GET /bss/v1/mappings/{account_id}`. |
| `state` | Filter by derived state, reverse-mapped through §4.2's table to a `status` value before the query. |
| `category` | Accepted; matches all rows, since category is fixed. Supported so a generic TMF client's default query does not 400. |
| `serviceCharacteristic.role` | Filter by `role` column. |
| `fields` | Comma-separated field selection per TMF630. Applied after serialization. |
| `offset`, `limit` | Pagination. `limit` defaults to 50, hard cap 500. Response carries `X-Total-Count` and `X-Result-Count`. |

An unrecognized query parameter is **ignored, not rejected** — TMF630's
own guidance, and the behaviour generic TMF clients assume. This is the
one place C-3 is deliberately lenient; every other input error is a 400.

`?sort=` is not supported; results are ordered by `assigned_at DESC` and
that ordering is documented rather than configurable. Sorting has no
identified consumer and interacts awkwardly with the pagination cap.

## 5. The `internal/tmf` package

This is the structural decision C-3 carries for the whole programme.

**Status correction.** An earlier draft of this section claimed the
extraction was free because `cmd/bssadapter/tmf640.go` did not yet exist.
That is no longer true: C-2's Task 4 landed as commit `ba56c4c` while
this spec was being written, creating `backend/cmd/bssadapter/tmf640.go`
(156 lines) with `tmfService`, `tmfServiceCharacteristic`,
`tmfRelatedParty`, the two base-path constants and
`handler.serviceFromMapping` all inline, exactly as the unamended plan
specified.

The extraction is therefore a **small retroactive refactor, not a
placement choice**. It is still worth doing, and still cheapest now:

- Three structs (~20 lines) move to `internal/tmf` and become exported.
- `serviceFromMapping` splits into a handler-side fetch (`GetDevice`,
  `adapters.ResolvePath`, `ACSClient.GetParameters` — all of which stay
  in `cmd/bssadapter`, preserving the process-boundary rule C-2 §4.1
  establishes) and a pure `tmf.ServiceFromMapping(m, chars, api)`.
- The two `tmf*BasePath` constants become `tmf.Href(api, resource, id)`
  arguments.

`tmf640_test.go` (173 lines) already covers the behaviour, so the
refactor is verifiable rather than speculative. Doing it before C-2's
Tasks 5 and 6 add `Monitor` and the PATCH handler keeps it at roughly
60 lines; deferring it past them grows it and adds C-4's and C-5's
resources on top.

**`backend/internal/tmf`** holds, with no HTTP handler and no direct
database access:

- Resource structs: `Service`, `ServiceCharacteristic`,
  `ServiceRelationship`, `RelatedParty`, `Monitor`, and (added by C-4/C-5)
  `ServiceOrder`, `Alarm`, `Event`, `Hub`.
- `@type` / `@baseType` / `@schemaLocation` population, applied uniformly.
- `Href(api, resource, id string) string` — the single place base paths
  are constructed, so C-3's inventory href and C-2's activation href
  differ by an argument rather than by two string literals in two files.
- `ServiceFromMapping(m *bss.AccountDeviceMapping, chars map[string]string, api string) Service` —
  the one serializer both APIs call.
- The TMF error envelope (`code`, `reason`, `message`, `status`,
  `referenceError`, `@type: "Error"`), which is *not* the existing
  `{error, message}` shape `writeError` produces for `/bss/v1`.

**`cmd/bssadapter`** keeps routing, auth, rate limiting and the
DB/`ACSClient` calls, and translates results through `internal/tmf`.

Two rules make the deferred `/bss/v1` decision genuinely deferrable:

1. **No TMF handler calls a `/bss/v1` handler, and no `/bss/v1` handler
   calls a TMF handler.** Both call `internal/bss` independently. If
   `/bss/v1` is sunset later, its handlers delete cleanly; if it lives
   forever, nothing in the TMF path is coupled to it.
2. **`internal/tmf` imports `internal/bss`, never the reverse.** The
   domain layer stays unaware it is being rendered as TMF.

### 5.1 Required amendment to C-2

C-2's plan carries an amendment block on Task 4
(`docs/superpowers/plans/2026-09-13-bss-tmf640.md`) recording this split.
Because Task 4 has already landed, the amendment now applies as
**follow-up work on the C-2 branch before Tasks 5 and 6 run**, not as a
change to how Task 4 is implemented:

1. Create `internal/tmf` and move the three structs, `Href` and the pure
   `ServiceFromMapping` into it.
2. Rewrite `handler.serviceFromMapping` to fetch characteristics and
   delegate.
3. Run `tmf640_test.go` unmodified — it must pass without edits. If it
   needs edits, the refactor changed behaviour and is wrong.
4. Then dispatch Tasks 5 and 6 against the split layout: `Monitor` is an
   `internal/tmf` type; its state derivation and the PATCH dispatch stay
   in the handler.

This changes no route, no response body and no status code. C-2's
existing tests passing unmodified is the acceptance criterion.

## 6. Testing and acceptance

- **Unit, `internal/tmf`** (no DB, no HTTP): serializer output for a
  gateway mapping, a non-gateway mapping with a gateway present
  (relationship populated), a non-gateway mapping with no active gateway
  (relationship empty), all four `status` → `state` mappings, `fields`
  selection including a request for a field that does not exist.
- **DB-backed** (`ACS_TEST_POSTGRES_DSN`, the established pattern):
  `GET /service?relatedParty.id=` returns exactly the account's active
  mappings and no unassigned ones; a mapping whose `unassigned_at` is set
  disappears from inventory; `X-Total-Count` is the unpaginated total,
  not the page size; `limit` above the cap is clamped rather than
  rejected.
- **Cross-API consistency, the test that justifies §5**: fetch the same
  mapping through C-2's `GET /service/{id}` and C-3's `GET /service/{id}`
  and assert every field except `href` is byte-identical. This test fails
  loudly the first time someone edits one serializer and not the other,
  which is the entire reason the shared package exists.
- **No behaviour change to `/bss/v1`**, proven by running the existing
  suite unmodified — the same gate C-2 sets.

## 7. Out of scope

| Excluded | Reason |
|---|---|
| `POST`/`PATCH`/`DELETE /service` | TMF638 is inventory. Writes belong to TMF640 (C-2) and TMF641 (C-4). |
| `/hub`, `/listener` notification endpoints | C-5 builds one hub for all APIs rather than one per API. |
| Assignment history (unassigned mappings) | Needs its own decision about whether a released device is still a `Service`. Real capability, separate question. |
| `?sort=` | No identified consumer; interacts awkwardly with the pagination cap. Fixed `assigned_at DESC`, documented. |
| `serviceOrderItem` back-references | Cannot be populated until C-4 exists. C-3 omits the field; C-4 adds it. |
| `supportingResource`, `supportingService`, `feature`, `place` | No ACS data behind them. TMF's extensibility model makes omission valid. |
| TMF638 v5 | Deliberate: match C-2's verified v4 rather than serve two schema generations from one process. |

## 8. Decisions made during brainstorming

- **`internal/tmf` extracted now, before C-2 Task 4 writes the
  serializer** — the timing is the whole point; this is free today and a
  refactor in a week.
- **Pragmatic conformance, deviations documented** — filter parameters
  with a real consumer only, unknown parameters ignored per TMF630, every
  omission listed in §7 rather than silently absent.
- **v4 across the programme**, not v5 for the APIs where it exists.
- **`serviceRelationship` is narrow and honest** — one `dependency` edge
  onto the account's gateway, empty when there is no gateway. No invented
  graph.
- **Current inventory only**; history deferred with a stated reason
  rather than half-built.
- **The cross-API byte-identity test is a required acceptance criterion**,
  not a nice-to-have. It is the mechanism that keeps three APIs' `Service`
  from drifting.

## 9. Dependencies and sequencing

Depends on C-2 landing (specifically Tasks 1–4, since C-3 reuses
`GetMappingByID`, `ACSClient.GetParameters` and the serializer). Blocks
nothing, but is the recommended next sub-project because it exercises
`internal/tmf` under real use before C-4 and C-5 build on it.

Sequence: **C-2 → C-3 → C-4 (TMF641) → C-5 (TMF642/688).**
