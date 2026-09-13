# Roadmap

What is coming, so you can plan rather than rework. **No dates are given
here** — treat this as direction, and confirm timing with the operator
before depending on anything below.

## Today: `/bss/v1`

Live, stable, documented, and not going away without a long notice
period and a migration path. Build against it now.

## Coming: TM Forum Open APIs

A standards-based surface is being added alongside `/bss/v1`. If your BSS
already speaks TM Forum, this will let you use a generated client and your
existing connectors instead of a bespoke integration.

| API | Purpose | Status |
|---|---|---|
| **TMF640** Service Activation and Configuration | Read and modify a service | In development. **Not yet reachable** — do not build against it. |
| **TMF638** Service Inventory | Search and list services | Designed, not built |
| **TMF641** Service Ordering | Multi-item orders, full order lifecycle | Designed, not built |
| **TMF642 / TMF688** Alarms and Events | Standard alarm model and subscribe/notify hub | Designed, not built |

Conformance will be a documented pragmatic subset: correct resource
shapes and standard state enumerations, with the filter parameters that
have real consumers, and every deviation listed explicitly rather than
left for you to discover.

### What this means for you now

**Nothing changes today.** `/bss/v1` remains the integration surface, and
the TMF endpoints are not routed yet.

Two things worth knowing while you design:

- **TMF640 is CRUD on a `Service` plus a `Monitor` for async tracking.**
  It is *not* an order-placement API. If you are planning around a
  `ServiceOrder` with an `acknowledged → inProgress → completed`
  lifecycle, that is **TMF641**, a different API. Getting these two
  confused is common and leads to building against the wrong resource.
- **Wi-Fi passwords will remain write-only** on the TMF surface too.

### Multi-device accounts

The data model already supports several devices per account, addressed by
`role`, with at most one active device per role. `/bss/v1` exposes this on
mappings and orders today.

What it does not yet expose is **unassignment and device swap** over the
API — releasing a device or replacing one under RMA currently needs
operator action. Both are planned to arrive with TMF641, where they are
naturally expressed as order items. If your rollout needs programmatic
device swap, say so early.

## Not planned

- Reading Wi-Fi passwords back. Write-only, by design, permanently.
- The ACS holding customer, billing or product data. Your BSS stays the
  system of record.
- Scheduled activation on this API. Schedule on your side.

## Influencing this

Priorities respond to real integration needs. If something above is
blocking you, or something absent would unblock you, raise it — a
concrete use case with volumes attached carries considerably more weight
than a feature request.
