# API tenancy classification

Remediation P2.2 (`ACS_REMEDIATION_EXECUTION_PROTOCOL_2026-09-03.md` §7):
every REST resource classified by tenancy model, so a reviewer can tell
at a glance whether a route needs `h.deviceScope`-style enforcement, and
so a new endpoint has a checklist item instead of an implicit default.
Routes are as registered in `backend/cmd/api/routes.go`.

## The four classes

- **platform-global** — no tenant owner; superadmin-only (or an
  explicit `GlobalAccess` grant, audit P0.1) by design. Regions,
  customers, projects, operator management, BSS integration admin,
  role-permission matrix.
- **region-owned** — scoped via the region a customer belongs to,
  resolved through `AccessibleCustomerIDs`' region-scope expansion.
  ACS has no resource whose *primary* key is a region; regions only
  appear as one of the two `operator_scopes.scope_type` values.
- **customer-owned** — has (or, after this remediation pass, now has)
  a `customer_id` and must be filtered by the caller's
  `AccessibleCustomerIDs` on every read and write. Devices themselves,
  plus the five control objects device groups/config templates/
  policies/scheduled jobs/firmware rollouts can target.
- **device-derived** — has no `customer_id` of its own but belongs to
  exactly one device (credentials, CLI/VPN/web-GUI config, jobs,
  uploads, parameter data); scoped by resolving that device's
  `customer_id`, the same `getScopedDevice`/`h.deviceScope` pattern.

A route with **no tenant boundary at all** (health checks, metrics,
login, CPE-facing public endpoints authenticated by their own
purpose-bound token rather than an operator scope) is marked
**boundary: n/a**, not platform-global — nothing here is superadmin
gated because nothing here is operator-gated.

## Enforcement column

- **scoped** — enforces the classification today (verified in this
  remediation pass, with an integration test).
- **superadmin-only** — platform-global by construction: the route
  itself requires `admin` rank, so there is nothing further to scope.
- **n/a** — no tenant boundary applies (see above).

Anything not marked **scoped** or **superadmin-only** is a known gap —
check the execution protocol's P0/P1/P2 sections and `fable5.1_review.md`
before assuming it's fine.

## Health / auth / operator management — platform-global or n/a

| Resource | Routes | Class | Enforcement |
|---|---|---|---|
| Health/metrics | `GET /healthz`, `/readyz`, `/metrics` | n/a | n/a |
| Login/logout/password reset | `POST /auth/login`, `/logout`, `/password-reset/*` | n/a | n/a (identity, not tenancy) |
| Browser ticket | `POST /auth/ticket` | n/a | n/a — audience/version-bound (P1.5), not tenant-bound |
| Operator CRUD | `POST/GET /auth/operators`, `PUT .../password` | platform-global | superadmin-only |
| Operator scopes/global-access | `POST/GET .../scopes`, `PUT .../global-access` | platform-global | superadmin-only (this *is* the scope-assignment mechanism) |
| Role/permission matrix | `GET/PUT /auth/role-permissions` | platform-global | superadmin-only |

## Tenancy structural CRUD — platform-global

| Resource | Routes | Class | Enforcement |
|---|---|---|---|
| Regions/customers/projects | `POST/GET/DELETE /regions`, `/customers`, `/projects` | platform-global | superadmin-only (the org chart itself) |

## Devices and device-derived resources

| Resource | Routes | Class | Enforcement |
|---|---|---|---|
| Device CRUD/list/parameters/tags/location | `GET/PUT /devices*` | customer-owned | scoped (`getScopedDevice`/`deviceScope`, P0.2, oldest fix in the codebase) |
| Device customer/project assignment | `PUT /devices/{id}/customer`, `/projects` | customer-owned | scoped |
| Bulk import | `POST /devices/import` | customer-owned | scoped (P2.1/H-3: caller must name an in-scope `customer_id`; cannot touch an existing foreign-tenant device) |
| Bulk actions | `POST /devices/bulk-actions` | device-derived | scoped (per-device, P0.2) |
| Dashboard / fleet-health / summary / matching-ids | `GET /dashboard*`, `/fleet-health`, `/devices/summary`, `/devices/ids` | customer-owned (aggregate) | scoped |
| Reports export | `GET /reports/devices/export` | customer-owned (aggregate) | scoped |
| Device credentials (Connection Request rotation) | `POST /devices/{id}/credentials/*` | device-derived | scoped (P2.1/H-3: activate/revoke also verify `cred.DeviceID` against the path device, not just the path device's own scope) |
| CLI credentials | `POST/GET/DELETE /devices/{id}/cli/credentials*` | device-derived | scoped (P2.1/H-3: delete loads the credential first, since the route has no device_id to check up front in the raw handler logic) |
| CLI console / web-GUI proxy | `GET .../cli/connect`, `PUT/GET/DELETE .../webgui*` | device-derived | scoped, plus SSRF policy on the proxied target (P1.2-class, `netguard.Policy`) |
| VPN enroll/config | `POST/GET /devices/{id}/vpn/*` | device-derived | scoped |
| VPN peer list | `GET /vpn/peers` | device-derived (aggregate) | scoped (P2.1/M-12: `ListPeersForCustomers` for a scoped caller) |
| VPN peer revoke | `DELETE /vpn/peers/{peer_id}` | device-derived | scoped (P2.1/H-3: caught while writing this table — loaded the peer to learn its device before authorizing, same shape as the credential/CLI-cred fixes) |
| VPN concentrator config | `GET /vpn/concentrator` | platform-global | n/a — no per-device secret, just the shared concentrator public config |
| Diagnostics (ping/traceroute/refresh) | `POST /devices/{id}/diagnostics/*`, `/parameters/refresh-*` | device-derived | scoped |
| Object/parameter RPC actions | `POST /devices/{id}/objects*`, `/reboot`, `/factory-reset`, `/schedule-inform`, `/parameter-attributes*`, `/connection-request`, `/discover-parameters` | device-derived | scoped |
| Jobs | `GET /devices/{id}/jobs`, `/jobs`, `/jobs/{command_key}` | device-derived | scoped |
| Firmware download to device | `POST /devices/{id}/firmware` | device-derived | scoped |
| CPE uploads (operator side) | `POST /devices/{id}/uploads`, `GET .../uploads`, `GET /uploads/{id}/file` | device-derived | scoped |
| CPE upload receipt | `PUT /uploads/{id}/receive` | n/a | n/a — CPE-facing, authorized by the expiring transfer token, not an operator scope (P0.3); race-safe as of P1.3 |
| Firmware image file serve | `GET /firmware/images/{id}/file` | n/a | n/a — CPE-facing, transfer-token authorized |

## Control objects that can act on devices asynchronously — customer-owned

Everything in this section is the P0.2-P0.7 remediation: each table now
carries `customer_id` (`NULL` = platform-global, superadmin/GlobalAccess
only), enforced at creation, at read, and re-checked at fire/execution
time where the object acts later than the request that created it.

| Resource | Routes | Class | Enforcement |
|---|---|---|---|
| Device groups | `POST/GET/DELETE /device-groups*`, `/members*` | customer-owned | scoped (P0.2); adding a member also requires the device to share the group's own customer |
| Config templates | `POST/GET/DELETE /config-templates*`, `/apply` | customer-owned | scoped (P0.4); `apply`'s device_ids/group_id fan-out is scoped per device (closed a direct cross-tenant config-write, H-3) |
| Policies | `POST/GET/DELETE /policies*`, `/enable`, `/disable` | customer-owned | scoped (P0.5); enforcement (`ForDevice`) requires matching `customer_id`, not just model |
| Scheduled jobs | `POST/GET/DELETE /scheduled-jobs*`, `/enable`, `/disable` | customer-owned | scoped (P0.6); target re-validated again at fire time, not just creation |
| Firmware rollouts | `POST/GET /firmware/rollouts*`, `/start`, `/advance` | customer-owned | scoped (P0.7); eligibility computation itself is bounded by `customer_id`, not just filtered after the fact |
| Firmware images (catalog) | `GET/POST /firmware/images` | platform-global | superadmin/`firmware.manage` — a firmware binary isn't itself tenant data; what a *rollout* does with it is |

## Audit log

| Resource | Routes | Class | Enforcement |
|---|---|---|---|
| Audit log | `GET /audit-log` | device-derived (aggregate) | scoped (P2.1/M-12) — entries with no `device_id` at all (platform/admin actions) are visible only to an unscoped caller |

## BSS integration admin panel — platform-global

| Resource | Routes | Class | Enforcement |
|---|---|---|---|
| Mappings, OAuth clients, webhooks, stats, health, troubleshoot | `GET/POST/DELETE /bss/*` | platform-global | superadmin-only. BSS integration is one platform-wide relationship, not per-tenant — see P2.3 for the separate question of *revocation* semantics on this surface |

## Maintaining this table

A new route must be added to the section matching its resource, with an
explicit class and enforcement note — "n/a" and "scoped" both count as
answers, an empty cell does not. If enforcement isn't "scoped" or
"superadmin-only" yet, say so and link the tracking item; don't merge a
customer-owned or device-derived resource silently unscoped.
