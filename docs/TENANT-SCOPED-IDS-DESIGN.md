# Per-ISP account IDs and order IDs: design

Status: **built** (migrations 0098 and 0099). Closes finding O2 of
`docs/SECURITY-ASSESSMENT-2026-11-14.md`. Sections 1 to 8 are the design
as proposed and approved; section 9 is phase 1 as built; section 10 is
phase 2 as built, including where it differs from the proposal.

## 1. The problem

Three identifiers that an integration chooses are unique across the whole
ACS, not per ISP:

| Identifier | Where | Today |
|---|---|---|
| `account_id` | `bss_accounts.account_id` (primary key), and a plain text column in 13 other tables | One namespace. A customer-bound client's first write to an ID nobody owns claims it for that ISP. |
| `external_order_id` | `bss_orders.external_order_id` (primary key) | One namespace. Another ISP's ID is refused with `409 ErrIdempotencyKeyReused`. |
| TMF641 order `externalId` | `service_orders.external_id` (unique) | One namespace. |

What that allows between two ISPs on one ACS:

- **Collision.** Two ISPs that both number accounts `10001…` cannot both
  use them. The second gets `404` on its own account.
- **Squatting.** An ISP's integration (or whoever holds its client
  secret) can claim IDs another ISP will use later and so block that
  ISP's activations. The same works for order IDs.
- **A small oracle.** The `409` on an order ID tells an ISP that some
  other tenant has used that ID.

None of this discloses data: ownership checks already keep one ISP out of
another's accounts. It is denial of service between tenants, and it is
the reason the integration guide tells ISPs to prefix their IDs. With one
ISP connected it cannot happen; with two it can.

## 2. Goal

An ISP's IDs are its own. Two ISPs can use the same `account_id` and the
same `external_order_id` without seeing, blocking or learning about each
other. Nothing changes on the wire for a customer-bound client.

## 3. Options

**A. Keep the global namespace, require a prefix.** What the guide says
today. No code. Collisions are avoided by convention; squatting is not
prevented, because nothing stops a client from writing another ISP's
prefix.

**B. Add `customer_id` to every table that has `account_id`.** Uniqueness
becomes `(customer, account_id)`. The repository already does this for
sites and service plans. The weakness is that the owner of an account is
then stored in 14 places: moving an account between customers has to
update all of them, and a row that disagrees with the registry is a
tenant-isolation bug waiting to be found.

**C. Make `bss_accounts` the registry and reference it by key
(recommended).** Each account gets an internal key. The ISP's ID is
unique per customer in the registry only, and every other table points at
the key. The owner is stored once.

## 4. Recommended design (C)

### Accounts

```sql
-- bss_accounts becomes the registry of every account, owned or not.
id          UUID PRIMARY KEY            -- internal, never on the wire
account_id  TEXT NOT NULL               -- the ISP's ID, unchanged
customer_id UUID NULL REFERENCES customers(id)   -- NULL = unowned
UNIQUE (COALESCE(customer_id::text, ''), account_id)
```

- Every table that has `account_id` today gains
  `account_key UUID NOT NULL REFERENCES bss_accounts(id)` and is looked up
  by it: `account_device_mappings`, `bss_orders`, `service_orders`,
  `webhook_subscriptions` (nullable there: a fleet-wide subscription has
  no account), `webhook_deliveries`, `tmf_alarms`, `tmf_events`
  (nullable), `tmf_service_problems`, `bss_idempotency_keys`,
  `account_profiles` and `account_profile_archive`. A sale
  (`onboardings`) is not one of them: it already carries its own
  `customer_id` next to `account_ref`, so it is scoped by that.
- The text `account_id` stays in those tables as the value shown to
  people and sent on the wire. It is no longer used to find rows.
- **One rule for what an ID means.** Every repository function that
  takes an account ID resolves it to a key first, through one package
  (`internal/accounts`), and then finds rows by the key. In phase 2 the
  resolver uses the caller's customer, which the adapter's authentication
  layer sets once per request. A bound client can only ever resolve
  inside its own customer, so another ISP's account with the same ID is
  not "forbidden", it simply is not there. A test fails the build if any
  SQL finds rows by the text ID outside a short reviewed list.
- **Claiming** stays as it is (first write creates the registry row), but
  can no longer be blocked: the row is created in the caller's own
  namespace.
- **Unowned accounts** (created from the console with no customer, or by
  a global client in the lab) live in the unowned namespace. As today, a
  bound client does not see them until an operator assigns them.
- **Assigning or moving an account** to a customer is one row update. It
  is refused with `409` if that customer already has an account with the
  same ID.

### Orders

```sql
-- bss_orders
id                UUID PRIMARY KEY
account_key       UUID NOT NULL REFERENCES bss_accounts(id)
external_order_id TEXT NOT NULL
UNIQUE (order_scope, external_order_id)   -- order_scope = owning customer, '' if unowned
```

- Order IDs are unique **per ISP**, not per account. An ISP that reuses
  an order ID on a different one of its own accounts still gets
  `409 ErrIdempotencyKeyReused`, which catches integration bugs. Another
  ISP using the same ID gets its own order and no `409`.
- `service_order_items` references `bss_orders(id)` instead of the text
  ID. `service_orders.external_id` becomes unique per ISP the same way.
- `command_key` is minted by the ACS and stays globally unique, so
  `GET /bss/v1/jobs/{command_key}` is unchanged (it is already checked
  against the caller's customer).
- While the table is being changed, add `request_hash` (a digest of the
  order body). It is recorded but not enforced. It is what finding O7
  (same order ID resent with a changed body) needs, so that decision can
  be taken later without another migration.

### What each caller sees

| Caller | Change |
|---|---|
| Customer-bound OAuth client (the production model) | No request or response changes. Two behaviours relax: an ID another ISP uses is no longer `404`/`409`, it is simply yours. |
| Global client or shared token (lab only; production refuses them) | An `account_id` that exists under more than one customer is ambiguous. Routes accept `customer_id` (`customerId` on TMF) to say which; without it the answer is `409 ErrAccountAmbiguous`. An ID that exists once resolves as today. |
| Webhook and event consumers | Payloads already carry `customer_id` and `account_id`. No change. |
| Operator API and console | Routes with `{account_id}` in the path (`/bss/accounts/{id}/customer`, `/accounts/{id}/profile`, `/bss/mappings/{id}/{role}`) take an optional `customer_id` query parameter, required only when the ID is ambiguous. Account lists and the header search show the customer next to the ID. A scoped operator already sees only their customers' accounts. |
| Standalone mode | Unaffected: the adapter is off and console-created accounts are unowned. |

### What does not change

Device ownership (`ACS_BSS_REQUIRE_ALLOCATED_DEVICES`), OAuth scopes, the
404-not-403 rule, sealed secret handles (already per customer), and the
event stream's per-customer sequence.

## 5. Migration

One forward-only migration, run in a maintenance window after
`scripts/backup.sh`:

1. Add `bss_accounts.id`; allow `customer_id` to be NULL; insert an
   unowned registry row for every `account_id` that appears in a child
   table but has no registry row yet.
2. Add `account_key` to each child table and fill it by joining on
   `account_id`. This is unambiguous, because until this migration IDs
   are globally unique.
3. Add `bss_orders.id` and `order_scope`, repoint
   `service_order_items`, replace the primary keys and unique constraints.
4. Set the new columns `NOT NULL`, add foreign keys and indexes.

Cost: `tmf_events` and `webhook_deliveries` are the only tables that can
be large (bounded by retention, 90 and 30 days by default); the backfill
rewrites them once under a lock. There is no schema rollback: restore
from the backup.

## 6. Build order

| Phase | What | Behaviour change |
|---|---|---|
| 1 | The migration; repositories look up by `account_key`; the adapter and API resolve IDs at the edge, still treating IDs as global. | None. The existing suite is the regression test. |
| 2 | Resolution becomes `(customer, ID)`; drop the global uniqueness; order scope per ISP; `customer_id` qualifier on operator and global-client routes; console shows the customer. | The one in section 4. New tests: two ISPs with identical account and order IDs, squatting attempt, ambiguous ID for a global client, account move refused on conflict. |
| 3 | Integration guide (remove "globally unique" and the prefix advice), OpenAPI for both services, contract fixtures, security assessment. | Documentation only. |

Size, from the code as it stands: 14 tables, about 43 SQL lookups in 30
Go files, 9 console files, both OpenAPI documents. The database role
privileges (`dbroles/manifest.go`) regenerate from the code.

To verify during phase 1 rather than assume: how event `source_key`
values (globally unique) are built, and whether any embeds an order or
account ID; and the Grafana role's column list for `bss_orders`.

## 7. Risks

- **A missed lookup.** A query still keyed on the text `account_id`
  after phase 2 could return another ISP's row. Mitigation: phase 1
  changes every lookup while IDs are still unique, and a test greps the
  repositories for `account_id = $` so a new one fails the build.
- **Long lock on a large event table** during the migration. Mitigation:
  run the retention pruner first; measure on a restored copy.
- **Operators with bookmarks or scripts** that address an account by ID
  alone keep working until the first real duplicate exists.

## 8. Decisions (taken 2026-11-14)

All six were answered "as recommended": design C; order IDs unique per
ISP; `409 ErrAccountAmbiguous` plus a `customer_id` qualifier for global
clients; `request_hash` recorded now. Who mints account IDs (2) does not
change the design. The contract version (6) is decided at release time.

The questions as asked:

1. **Go ahead with C**, or stay on A (prefix by convention) for now?
2. **Who mints account IDs in practice?** If the NaaS platform mints all
   of them for every ISP, accidental collisions cannot happen and what
   remains is squatting by a misbehaving or compromised ISP client. If
   each ISP's BSS mints its own, collisions are likely as soon as there
   are two. This changes the urgency, not the design.
3. **Order ID scope:** per ISP (recommended) or per account?
4. **Global clients in the lab:** `409 ErrAccountAmbiguous` plus a
   `customer_id` qualifier (recommended), or no support for duplicates
   outside bound clients?
5. **Record `request_hash` now** for O7 (recommended), enforcement
   decided separately?
6. **Contract version.** For bound clients the change only relaxes two
   refusals, and adds one error code for global clients. Whether that is
   a minor version, and when fixtures are re-recorded, is a release
   decision.

## 9. Phase 1 as built

Migration `0098_account_registry.sql`, rehearsed on a copy of the lab
database and covered by `internal/store/migration_0098_test.go`:

- `bss_accounts` has an internal `id` and holds unowned accounts
  (`customer_id` NULL). Every account any table mentioned was registered.
- `account_key` on the ten tables of section 4, filled from the registry,
  with a check that a row names an account by ID and key together or by
  neither. Uniqueness that was on the text ID (active mapping per role,
  idempotency keys, profiles) is on the key.
- `bss_orders` has `id`, `order_scope` and `request_hash`;
  `service_order_items.bss_order_id` replaces the foreign key on the text
  order ID. Order state changes address the order by `id`.
- The event triggers of 0090 use the key.
- IDs are still unique across the ACS: the constraints named `*_global_*`
  keep that until phase 2 drops them. The per-customer indexes already
  exist beside them.

Code: every lookup goes through `accounts.Resolve` (read) or
`accounts.Ensure` (write). `internal/accounts/lookup_guard_test.go`
lists the five places that may still use the text ID, with the reason
each is safe.

Unchanged in phase 1, checked by the existing suite and a live run of the
stack on the migrated copy: every response, status code and event.

One difference worth knowing: an account that exists only because
something was written for it (a mapping from the console, a profile) now
has a registry row with no owner, where before it had no row. The rules
that depended on "no row" (claiming, who may see a profile) test for "no
owner" instead.

## 10. Phase 2 as built

Migration `0099_account_ids_per_customer.sql` drops the constraints that
kept IDs unique across the ACS. What remains:

| Identifier | Unique within |
|---|---|
| `account_id` | a customer (and the unowned accounts) |
| `external_order_id`, TMF641 `externalId` | a customer |
| service problem `externalId` | an account |
| event `sourceKey` | a customer |
| alarm `sourceKey` | an account |

The last two were not in the proposal. A `sourceKey` is chosen by whoever
reports the event or alarm, an integration included, and was unique
across the ACS: one ISP's key made another's identical key a silent
no-op. Same problem, same fix.

### Who an ID belongs to

`internal/accounts` resolves every ID, from the request's context:

| Caller | Set by | IDs resolve |
|---|---|---|
| Customer-bound client | the adapter's authentication, from the token | in that customer only |
| Global client, shared token, lab profile | `?customer_id=` (`none` = unowned) if given | in the named customer; else the ID must name one account, or `409 ErrAccountAmbiguous` |
| Client limited to listed account IDs | never (it may not name a customer) | only while unambiguous |
| Operator API | `?customer_id=` on the account routes | as for a global client; `409` when ambiguous |
| Worker handling an order | the order's own scope | in the order's customer |
| Telemetry, alert incidents | the device | the account of that ID the device is mapped to |
| A sale | the sale's customer | in that customer |

### Rows found by their own ID

This was the one hole the proposal missed, found by the existing tests.
Handlers that load a row by its own ID (a TMF service, an order, a
Monitor, a subscription, a sale) used to authorize on the row's account
ID. Once two customers can share an account ID that is not enough: a
caller with an account "1001" of its own could present another
customer's row whose account is also "1001". Two things now prevent it:

1. such lookups run inside the caller's customer, so the row is not
   found at all; and
2. the account gate checks that the account the ID names for the caller
   is the very account the row belongs to (`authorizeTMFRow`,
   `authorizeBSSRow`), and that a sale is the caller's customer's.

Either alone is sufficient; a test exercises the second without the
first.

### Differences from the proposal

- **Accounts from before binding** keep their protection. The proposal
  said a claim "can no longer be blocked". It still is in one case: an
  unowned account of that ID with mappings, orders, service orders or
  webhooks. That is almost certainly the same ISP's account from before
  its client was bound, and letting the client start an empty one beside
  it would orphan the history. Only operators and global clients can
  create unowned accounts, so this is not a way for one ISP to block
  another.
- **Alerting** still labels incidents with the account ID, qualified by
  the device. Two ISPs' same-numbered accounts share the label; their
  incidents do not mix, because an incident is per device. Left as is.

### Tests

- `cmd/bssadapter/tenant_ids_test.go`: two ISPs with the same account ID
  and order ID; an ID used first by another ISP; another customer's row
  by its ID; a global client with and without `customer_id`, a listed
  client, a bound client that names another customer; the same
  `sourceKey` from two ISPs; a move onto an existing ID.
- `cmd/api/tenant_ids_test.go`: the operator routes, unqualified (409,
  nothing changed) and named.
- A live run on a copy of the lab database: two bound integrations
  created through the API, same account ID, same order ID, each with its
  own order and its own retry; the operator API refusing the unqualified
  call and answering the named ones.

### Contract

For a customer-bound client, requests and responses are unchanged. Two
answers change meaning: a write to an ID another customer uses (was
`404`, or `409` for an order ID) now succeeds on the caller's own
account or order. One recorded fixture exchange no longer holds
(`subscriber-profile`, the last one). Nothing has been re-recorded or
tagged; `contract/README.md` says so.
