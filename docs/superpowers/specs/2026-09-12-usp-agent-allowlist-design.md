# USP Agent Allowlist — Design

## 1. Purpose and driver

`cmd/uspc` (the TR-369/USP controller, built across sub-projects 0, A, B-1,
B-2, B-3a, B-3b, B-3c on branch `design/usp-controller`) currently accepts
any agent that completes the WebSocket subprotocol/query-parameter
handshake or publishes to the MQTT controller topic. There is no allowlist
of permitted network peers and no check on agent identity before it is
reconciled onto a `devices` row. This is a known, explicitly documented gap
(`backend/cmd/uspc/main.go:14-17,71`): the service must not be exposed to
an untrusted network until it lands. This is the last unimplemented piece
of `docs/superpowers/specs/2026-09-09-usp-controller-design.md`'s own
programme.

## 2. Two independent gates

The allowlist is two layers, enforced at different points in a connection's
lifecycle, because they answer different questions: "can this network peer
reach the listener at all" and "is this agent's identity one this
controller is willing to manage."

### 2.1 Network-level: CIDR allowlist

A remote IP that isn't in the configured allowlist never completes a
protocol handshake. Mirrors `internal/netguard`'s existing
`Policy.AllowedCIDRs` convention exactly: a non-empty list restricts to
those networks; an empty list is permissive (any address passes), matching
that package's existing default-permissive-until-configured precedent
elsewhere in this codebase.

- New config: `ACS_USP_ALLOWED_CIDRS` — comma-separated CIDR list, parsed
  with the same `ParseCIDRList`-style helper `netguard` already has.
- Enforcement point: a filtering `net.Listener` wrapper placed in front of
  both the WebSocket `http.Server`'s listener and the embedded MQTT
  broker's listener in `cmd/uspc/main.go`. Its `Accept()` calls the
  underlying `Accept()`, checks `RemoteAddr()`'s IP against the policy, and
  for a disallowed address closes the connection immediately and loops to
  accept the next one — never returning the rejected connection to the
  caller. No handshake, no protocol data exchanged, no information leaked
  beyond "a TCP connection was possible."
- One wrapper, one policy, applied identically to both MTPs — the
  listener-level enforcement point doesn't care which protocol runs on top.

### 2.2 Identity-level: known-device gate

An agent whose identity (OUI + ProductClass + SerialNumber) doesn't already
correspond to a known `devices` row is refused, even if it passes the
network-level gate.

**"Known" means:** a `devices` row already exists for that
`oui_serial` — created via `devices.Repository.PreRegister` (the existing
bulk-import path, `cmd/api/bulk_import_handlers.go`), a prior CWMP Inform,
a prior successful USP onboarding (before this gate existed), or manual
creation. CWMP's own `UpsertFromInform` is untouched by this design and
keeps its existing zero-touch auto-provisioning behavior — this gate is
USP-specific.

**Where it's enforced:** `devices.Repository.UpsertFromOnBoard`
(`backend/internal/devices/usp.go`) is the single function both of
`cmd/uspc/identity.go`'s identity-resolution paths call —
`reconciler.onBoard` (the primary `OnBoardRequest` path) and
`reconciler.fromProbeFallback` (the `Get`-fallback "last resort" path,
design spec §5.3). Gating this one function covers both call sites without
duplicating the check.

**Contract change:** the function's behavior changes from "insert-or-update"
to "update-only, reject if unknown." It no longer contains an unconditional
`INSERT`. It looks up the `devices` row by `oui_serial`; if none exists, it
returns a new sentinel error, `ErrUnknownDevice`, without creating anything.
If a row exists, its existing `UPDATE`-on-conflict logic runs unchanged
(refresh `online_status`, `last_updated_at`, append `'USP'` to
`management_protocols` if not already present) plus the `data_model_root`
fix below.

Because the contract fundamentally changed (a function that used to always
succeed for a syntactically valid identity now routinely refuses one), the
function is renamed to `ReconcileFromOnBoard` — a name that doesn't imply
row creation.

**On `ErrUnknownDevice`:** `identity.go`'s `onBoard`/`fromProbeFallback`
log at Warn (mirroring the existing `logReconcileFailure` pattern, with
wording that makes "refused: agent identity not pre-registered" explicit
and distinct from other reconciliation failure causes already logged
there) and the connection is closed via the existing `mtp.Conn.Close(reason)`
— no further USP messages from that connection are processed. This mirrors
the design spec's own stated philosophy for the symmetric agent-side gate
(§8: "an unknown controller is refused outright, not demoted to an
untrusted role").

### 2.3 No disruption to the existing fleet

Any device that already has a `devices` row — from CWMP, from a prior USP
onboarding before this gate existed, or from a bulk `PreRegister` import —
keeps working unchanged. The gate only affects a *first-ever* USP contact
from an identity `devices` has never seen. No migration or backfill is
needed for the existing fleet.

## 3. Existing-code fix folded in: `data_model_root` on a pre-registered device

`PreRegister` (`internal/devices/repository.go:70-85`) never sets
`data_model_root`, so a pre-registered row defaults to the column's own
`'UNKNOWN'` default (`internal/store/migrations/0001_devices.sql:14`).
`UpsertFromOnBoard`'s `ON CONFLICT` branch deliberately never touches
`data_model_root` (to protect a CWMP-discovered root from being overwritten
by a later USP onboarding of the same physical device). Put together: a
device pre-registered via bulk import and then onboarded via USP for the
first time — with no CWMP contact ever — would be stuck at
`data_model_root = 'UNKNOWN'` permanently, since nothing in the existing
code path ever sets it for that specific sequence.

This design's `ReconcileFromOnBoard` update path fixes it: set
`data_model_root = 'DEVICE2'` **only when the current value is
`'UNKNOWN'`**, via a `CASE WHEN devices.data_model_root = 'UNKNOWN' THEN
'DEVICE2' ELSE devices.data_model_root END`. A real CWMP-discovered root
(`'IGD1'` or a CWMP-set `'DEVICE2'`) is never overwritten; only the
genuinely-never-set case is filled in.

## 4. CI impact

`ci/usp/assert-getresp.sh`, `ci/usp/assert-job-dispatch.sh`, and
`ci/usp/assert-subscription.sh` all currently rely on obuspa's test device
auto-onboarding on first contact. Once the identity gate lands, each script
needs a `PreRegister`-equivalent raw SQL insert into `devices` (matching
the raw-insert pattern already established by `assert-job-dispatch.sh` and
`assert-subscription.sh` in this same CI job) before launching obuspa, or
every existing interop assertion breaks.

One new CI step is required: prove the gate actually refuses a real,
unregistered agent — connect a *second*, deliberately-unregistered obuspa
instance (or reuse the same binary against a different, never-pre-registered
serial number) and assert its connection is refused/closed rather than
silently accepted, against real agent behavior, not just a unit fixture.
This mirrors this whole programme's established acceptance gate: protocol
code that has never met a real agent is an intention, not a capability.

The network-level CIDR gate needs no CI changes — `ACS_USP_ALLOWED_CIDRS`
stays unset (permissive) in CI, matching `netguard`'s own existing
default-permissive-until-configured precedent, so it's untested against a
real reject scenario here; a unit test on the filtering listener itself
(a fake `net.Listener` with connections from allowed/disallowed IPs) covers
its logic directly, the same way `internal/usp` and `internal/netguard`
are unit-tested transport-free.

## 5. Deployment documentation

`cmd/uspc/main.go`'s startup warning (`"agent allowlisting is not
implemented in this build; do not expose uspc to an untrusted network"`)
and the equivalent caveats in `README.md` and
`deployment-testing-onboarding-guide.md` are replaced with real guidance:
how to `PreRegister` devices ahead of their first USP contact, and how to
configure `ACS_USP_ALLOWED_CIDRS` for a production deployment.

## 6. Testing and acceptance

- Unit: the CIDR filtering listener (allowed/disallowed IPs, empty-policy
  permissive default) — transport-free, no real network needed.
- Unit: `ReconcileFromOnBoard` — known identity updates and returns the
  device; unknown identity returns `ErrUnknownDevice` and creates nothing;
  the `data_model_root` `'UNKNOWN'`→`'DEVICE2'` fill-in fires only when the
  column is currently `'UNKNOWN'`, never overwriting `'IGD1'` or an
  already-set `'DEVICE2'`.
- Unit: `identity.go`'s `onBoard`/`fromProbeFallback` — on
  `ErrUnknownDevice`, the connection's `Close` is called and no
  `usp_agents` row is created or linked.
- CI (real obuspa): existing three interop scripts updated to pre-register
  their test device; one new script proves a genuinely unregistered real
  agent is refused.

## 7. Out of scope

| Excluded | Reason |
|---|---|
| Per-agent TLS client certificates | The CIDR + identity gates are the agreed-scope answer; mTLS is a heavier, separate deployment concern not requested here. |
| An operator-facing UI/API surface for managing the USP allowlist specifically | `PreRegister`'s existing bulk-import surface already serves this; no new management UI is being built. |
| Rate limiting / connection-attempt throttling for repeatedly-refused peers | Not requested; the CIDR gate already bounds who can attempt at all in a properly configured deployment. |
| Revoking a currently-connected agent's access retroactively | The gate only affects new onboarding; forcibly disconnecting an already-reconciled agent is a separate feature. |
