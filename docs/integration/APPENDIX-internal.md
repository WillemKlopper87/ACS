# Appendix — internal only

**Remove this file before sending the pack externally.** Everything else
in `docs/integration/` is written to be external-safe; this file is not.

## Pre-send checklist

Before handing the pack to a third party:

1. Delete `APPENDIX-internal.md` (this file).
2. Decide whether to include `bss-integration-guide.md`. It is good and
   the pack's README links to it as the workflow reference, but it
   contains internal cross-references — "audit P0.1", "audit P2.3",
   "the build plan's Phase 3", "design §5.1", "the reference
   `internal_bss_adapter.go` draft". None is sensitive, all are
   meaningless to an outsider and read as leaked internal notes. Either
   strip those references or replace the link with a curated extract.
3. Confirm `backend/openapi-bssadapter.yaml` is current — the drift test
   guarantees route coverage, not description accuracy.
4. Replace "Ask the operator" / "Report integration issues" with a real
   contact address.
5. Check `04-roadmap.md` still matches reality. It is the file that goes
   stale fastest and the one an integrator will plan against.

## Current build state — 2026-09-13

Accurate as of branch `design/tmf-expansion`, commit `8c98a43`.

| Surface | State |
|---|---|
| `/bss/v1` — 8 routes | Live, wired in `cmd/bssadapter/main.go:175-182` |
| TMF640 `GET /service`, `GET /service/{id}` | Handlers exist in `cmd/bssadapter/tmf640.go` (`getService`, `listServices`) but **no route registers them** — Task 7 has not run, so they are unreachable |
| TMF640 `GET /monitor/{id}` | Task 5, not started |
| TMF640 `PATCH /service/{id}` | Task 6, not started |
| TMF638 / TMF641 / TMF642+688 | Specced only (C-3/C-4/C-5, branch `design/tmf-expansion`) |

This is why `04-roadmap.md` says TMF640 is "not yet reachable — do not
build against it". **If Task 7 lands, that line must change**, and
`openapi-bssadapter.yaml` needs the TMF paths added or the drift test
will fail.

## A contradiction fixed in the guide

`bss-integration-guide.md` §6 said:

> One primary device per account. Order dispatch resolves the account's
> most recently active mapping.

That was stale and directly contradicted the guide's own §2, which
documents `role` on both mappings and orders. Migration 0052
(`0052_device_assignment_roles.sql`) replaced most-recently-active
resolution with role addressing, backed by
`account_device_mappings_active_role_idx` — unique on
`(account_id, role) WHERE unassigned_at IS NULL`. `ActiveDeviceForAccount`
and `ErrNoDeviceForRole` have existed in `internal/bss/mapping.go` since.

Corrected in the same change as this pack. The residual, genuine gap is
narrower: `UnassignDevice`, `SwapDevice` and `AssignmentHistory` exist in
the repository layer but are **not exposed on any HTTP surface**. That is
what `04-roadmap.md` describes as arriving with TMF641.

## Security decisions worth remembering

**WiFiPassword redaction** (`8c98a43`). The C-2 design originally
reflected the live `WiFiPassword` value in `GET /service`. A security
review flagged that as exposing a plaintext credential to any
authenticated BSS integrator. The fix does not merely omit it from the
response — the value is never requested from `ACSClient.GetParameters` at
all, so the plaintext does not transit that path. `PATCH` will still
write it, matching normal TR-069/TR-369 practice of treating
`KeyPassphrase` as write-only.

Stated externally in the pack as "write-only by design", without the
review history. That framing is accurate and is the right level of detail
for an integrator.

**Revocation cache.** The 15-second worst-case window after revoking a
client is a deliberate trade (one Postgres lookup per client per 15s
rather than per request). The guide discloses it; the pack repeats the
15-second figure without the rationale.

## The residual idempotency gap

`02-integration-model.md` §9 discloses the crash-window double-dispatch
possibility. Internally: the write-ahead outbox closed the concurrent and
multi-instance cases; what remains needs an actual process crash between
dispatch succeeding and `MarkDispatched` committing. Full exactly-once
needs idempotency-key support on `cmd/api`'s internal interface, which
does not exist yet.

Worth keeping disclosed. An integrator who finds an undisclosed
double-write trusts nothing else in the document.

## Deliberately omitted from the external pack

- Internal architecture: `cmd/api` versus `cmd/bssadapter`, the process
  boundary, the outbox and reconciler internals.
- Table and column names.
- The USP/TR-369 controller programme (sub-project B) — not BSS-facing.
- Test infrastructure, `ACS_TEST_POSTGRES_DSN`, CI.
- Anything under `docs/superpowers/`.

The external pack describes **behaviour and contract**, never
implementation. That is what lets the implementation change without a
customer conversation.
