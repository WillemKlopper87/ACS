# Session Capture — Design

## 1. Purpose and driver

`acs.log` doesn't carry everything needed to troubleshoot a real CPE
session: what the device actually sent — the exact Digest challenge
response, the raw Inform, a SetParameterValues body — versus what the log
happens to have chosen to print. This document designs an on-demand,
application-level capture of real CWMP (`cmd/acs`) and USP (`cmd/uspc`)
session traffic, viewable and exportable from the operator console, so a
protocol-level bug (the exact class this session's own work already found
— a malformed nonce, a `uri` mismatch, a rejected XML boolean) can be
diagnosed from what a device actually said, not just what the log
happened to print.

## 2. A settled premise: application-level, not network-level

Both `cmd/acs` and `cmd/uspc` terminate TLS themselves
(`server.ListenAndServeTLS`, no reverse proxy in the default deployment) —
so a genuine network packet capture (tcpdump/Wireshark) would see only
encrypted bytes for any TLS-enabled deployment, unless a capture session
either ran with TLS off or exported TLS session keys (`SSLKEYLOGFILE`) —
a new, real secret-on-disk to manage, arguably a worse problem than the
one being solved.

This design captures at the application level instead: the same parsed
Inform/RPC/USP-message structs `cmd/acs`/`cmd/uspc` already build for
their own normal processing, redacted, recorded as structured session
transcripts. This works identically whether the deployment uses TLS or
not, and needs no new TLS plumbing.

## 3. Scope

**Built**: on-demand capture (never always-on) of CWMP and USP session
content, in three trigger modes (§4), stored short-term in Postgres,
viewable and exportable from the console.

**Not built** (§10): a real network-level pcap/Wireshark export; an
always-on rolling buffer; capturing the CPE's very first, wholly
unauthenticated empty POST (no identity or reliably stable key exists for
that one packet — see §4's own limitation note).

## 4. Trigger modes

An operator starts a capture one of three ways, each producing one
`capture_sessions` row (§5):

| Mode | Use case | Matched against |
|---|---|---|
| **By device** | An already-onboarded device is misbehaving. | The device's `oui_serial` natural key (read from its existing `devices` row at capture-start time) — every subsequent Inform/message whose *claimed* identity computes to that same key. `device_id` is set immediately, since it's already known. |
| **By expected identity** | A brand-new device has never successfully authenticated, so it has no `devices.id` yet, but its OUI+Serial (or per-device Digest username) is known in advance from a provisioning/shipping record. | The same `oui_serial` natural key shape, entered by the operator — matched against the claimed identity in **every** request, authenticated or not, including a wrong/malformed Digest response, which is exactly where the useful diagnostic content is. `device_id` is backfilled once the device does authenticate for real. |
| **By remote IP** | The device's identity isn't known yet, but its network location is (a lab bench, a DHCP reservation). | The connection's remote address, exact string match against a single IP in `match_value`. Not a CIDR/range matcher — entering a CIDR-shaped string would simply never match anything, since a real remote address is never string-equal to a range (§10). |

**Limitation, stated plainly**: none of the three modes can capture the
CPE's very first bare empty POST (the one with no `Authorization` header
and no claimed identity at all) — there is nothing to key on for that one
packet. This is an accepted gap, not an oversight: that packet carries no
diagnostic content beyond "a connection happened," which `acs.log`
already shows. The next request — where the device sends its actual
(possibly wrong) Digest response, or its Inform — is what all three modes
do catch, and is where the real diagnostic value is.

## 5. Schema

Next free migration number at implementation time:

```sql
CREATE TABLE capture_sessions (
    id           UUID PRIMARY KEY,
    device_id    UUID REFERENCES devices(id),        -- denormalized resolution column: set immediately for match_type='device' (already known), backfilled for 'identity'/'remote_ip' once the device authenticates (§4)
    match_type   TEXT NOT NULL CHECK (match_type IN ('device','identity','remote_ip')),
    -- The actual matching key, always expressed in its mode's own terms:
    -- 'device' and 'identity' both use the device's oui_serial natural
    -- key (cwmp.DeviceID.NaturalKey() shape, e.g. "001349+S230Q12345678")
    -- -- deliberately the SAME representation for both, so cmd/acs's
    -- per-event check is one query shape ("does this Inform's computed
    -- NaturalKey match an ACTIVE session with match_type IN ('device',
    -- 'identity')?"), not two. 'remote_ip' uses a single exact IP address
    -- string instead -- not a CIDR/range (§10). device_id is never used as the match key itself, even for
    -- match_type='device' -- it is a resolved-identity convenience
    -- column, not a second representation of the same key.
    match_value  TEXT NOT NULL,
    protocol     TEXT NOT NULL CHECK (protocol IN ('CWMP','USP')),
    status       TEXT NOT NULL DEFAULT 'ACTIVE' CHECK (status IN ('ACTIVE','STOPPED','EXPIRED')),
    started_by   TEXT NOT NULL,      -- operator identity, audit trail
    started_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    stopped_at   TIMESTAMPTZ,
    expires_at   TIMESTAMPTZ NOT NULL   -- hard cap, default now() + ACS_CAPTURE_MAX_DURATION
);
-- At most one active capture per distinct match target -- an operator
-- can still run several different captures concurrently (e.g. comparing
-- three affected devices during a firmware rollout issue).
CREATE UNIQUE INDEX capture_sessions_active_match_idx
    ON capture_sessions (match_type, match_value) WHERE status = 'ACTIVE';
CREATE INDEX capture_sessions_device_idx ON capture_sessions (device_id) WHERE device_id IS NOT NULL;

CREATE TABLE capture_events (
    id          UUID PRIMARY KEY,
    session_id  UUID NOT NULL REFERENCES capture_sessions(id) ON DELETE CASCADE,
    seq         INTEGER NOT NULL,
    direction   TEXT NOT NULL CHECK (direction IN ('inbound','outbound')),
    kind        TEXT NOT NULL,        -- e.g. "Inform", "SetParameterValues", "USP Notify"
    occurred_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    summary     TEXT NOT NULL,        -- one-line human summary for the event list
    body        TEXT,                 -- redacted rendering of the parsed message; may be large
    UNIQUE (session_id, seq)
);
CREATE INDEX capture_events_session_idx ON capture_events (session_id, seq);
```

No new inter-process channel. `cmd/acs`/`cmd/uspc` already touch the
database once per Inform/message for their own normal processing
(`UpsertFromInform` and equivalents) — the "is this device/identity/IP
being captured?" check is one more indexed lookup at that same existing
checkpoint, not a new background poller and not a new live channel to
`cmd/api` (which does not exist today and this design does not add).

## 6. Redaction

Redaction operates on the **already-parsed** Go structs `cmd/acs`/
`cmd/uspc` build for their own normal processing — never on raw bytes or
XML via regex, which is fragile and bypassable. Both processes fully
parse every CWMP `ParameterValueStruct` and USP Set/Get message before
capture would ever touch them, so capture hooks in at that structured
point, redacts, then renders the redacted version for storage.

**Rule**: a parameter whose *name* matches a sensitivity pattern
(case-insensitive substring: `password`, `passphrase`, `secret`, `psk`,
`presharedkey`, `privatekey`) has its *value* replaced with a fixed
marker (`"***REDACTED***"`) — the name stays visible (so an operator can
still see *which* parameter was being set), only the value is masked.
Deliberately pattern-based rather than an exact allowlist tied to this
codebase's known canonical parameters (`internal/devices/adapters`'s
`WiFiKeyPassphrase` etc.) — CWMP/USP can target arbitrary vendor-specific
paths (a hypothetical `X_HUAWEI_AdminPassword`), and a pattern catches
those too. Over-redacting something that merely contains "password" in
its name but isn't actually secret is a cheap false positive;
under-redacting a real secret is not acceptable. This extends the same
"never persist a live credential value, even to an authenticated
operator" rule this session already established for TMF640's `GET
/service` (design `docs/superpowers/specs/2026-09-13-bss-tmf640-design.md`
§4.1's correction).

Applies symmetrically: parameters the ACS *writes* (`SetParameterValues`)
and any a device *echoes back* on read (`GetParameterValuesResponse`) —
some CPEs, unlike this codebase's own TMF640 API, do return a current
`KeyPassphrase` on a read.

**Auth headers**: a `Digest` `Authorization` header is captured as-is —
its `response` field is a one-way hash, not the password, and seeing it
verbatim is the entire point of this feature (it is what would have let
an operator diagnose the `uri`-mismatch and nonce-overflow bugs this
session already found, directly, instead of by code archaeology). A
`Basic` `Authorization` header is different — a trivially-reversible
base64 encoding of the actual credential — so it is never captured
verbatim; only `"Basic auth, username=<user>"` is recorded.

## 7. Lifecycle and retention

- **Starting**: `cmd/api` validates no other `ACTIVE` session shares the
  same `(match_type, match_value)` (the partial unique index in §5 makes
  this a database guarantee, not application discipline — the same
  technique this codebase's outbox and alarm-dedup work already lean on)
  and inserts the row.
- **Capturing**: on each Inform/message for a matched device, identity,
  or remote address, `cmd/acs`/`cmd/uspc` write redacted `capture_events`
  rows for every `ACTIVE`, unexpired session matching that request.
- **Expiry needs no new background worker.** The per-event check is
  already `status = 'ACTIVE' AND expires_at > now()`, so an expired
  session simply stops matching new events on its own. The existing
  `internal/retention` sweep (already used elsewhere in this codebase for
  time-based pruning) gets two more jobs: flip `ACTIVE`→`EXPIRED` once
  past `expires_at` (for correct console display) and purge `STOPPED`/
  `EXPIRED` sessions (events cascade-delete via the FK) after the
  retention window.
- **Stopping early**: an operator's "Stop" action flips
  `status='STOPPED'`; the next per-event check anywhere sees the
  non-`ACTIVE` status and stops writing further events for that session.
  No race that matters — at most one already-in-flight request's event
  slips through.
- **Defaults**, both configurable via env vars matching this codebase's
  existing `ACS_*` convention: max session duration `30m`
  (`ACS_CAPTURE_MAX_DURATION`), post-completion retention `24h`
  (`ACS_CAPTURE_RETENTION_HOURS`) — long enough to review the next day,
  short enough not to accumulate a permanent record of fleet protocol
  exchanges by default.
- **Export**: a "Download transcript" action in the console, redacted
  JSON. No pcap synthesis — consistent with §2's decision to capture at
  the application level, not the network level.

## 8. Frontend UX

Grounded in this console's actual current structure (`frontend/src/
screens/DeviceFleet.tsx` — a fleet-wide list screen; `DeviceDetail.tsx` —
a single scrolling per-device page with `<h3>`-headed sections like
"Diagnostics", "Recent jobs", "Parameter cache", not a tabbed interface):

- **New top-level screen, `CaptureSessions.tsx`**, added to the main nav
  alongside Fleet/Jobs/etc. This is where "by expected identity" and "by
  remote IP" captures are started, since neither has an existing device
  page to launch from (the device isn't correlated to a `devices` row
  yet). It also lists every active/recent capture fleet-wide regardless
  of how it was started, so it is the one place to come back and review
  one later.
- **`DeviceDetail.tsx` gets a "Start capture" action**, alongside the
  existing Diagnostics section — the shortcut for the common case (an
  operator already looking at a known misbehaving device). One click
  starts a `match_type='device'` session and opens its live view.
- **A shared capture-detail view**, used by both entry points: a
  live-updating event list (polled every 1-2s while `ACTIVE` — no new
  live-streaming channel, per §5) showing timestamp/direction/kind/
  one-line summary per row; expanding a row shows the full redacted body.
  "Stop" and "Download transcript" actions. This is the one genuinely new
  nontrivial UI component; everything else is a thin list/button addition
  to existing screens.

## 9. Testing and acceptance

- **Unit**: the redaction helper (every pattern name, case-insensitivity,
  a non-matching name passes through unredacted, a `Basic` header is
  never rendered verbatim); the `(match_type, match_value)` computation
  for each of the three modes given a parsed `DeviceID`/request.
- **DB-backed** (`ACS_TEST_POSTGRES_DSN`, the established pattern): the
  partial unique index genuinely rejects a second `ACTIVE` session for
  the same `(match_type, match_value)` (23505, matching the technique
  this codebase's outbox/alarm-dedup work already uses); `capture_events`
  ordering (`seq`) is stable under concurrent writers; an `EXPIRED`
  session's own late-arriving event is rejected (not silently accepted
  past the deadline); the retention sweep flips status and later purges,
  cascading `capture_events` via the FK.
- **Integration** (real CWMP exchange, the same fixture style
  `cmd/acs/session_integration_test.go` already uses): starting a
  `match_type='identity'` capture for a not-yet-onboarded device, then
  driving a real (mock) CPE session against it, produces a
  `capture_events` row for the Inform with the Digest response visible
  and any `KeyPassphrase`-named parameter redacted — the concrete
  acceptance bar this feature exists to clear.
- **No behavior change** to existing CWMP/USP session handling, proven by
  the full existing `cmd/acs`/`cmd/uspc` suites passing unmodified with no
  active capture session in play (the added check is a cheap no-op read
  when nothing is capturing).

## 10. Out of scope

| Excluded | Reason |
|---|---|
| Real network-level pcap / Wireshark export | TLS terminates in-process; a real capture would need TLS off or `SSLKEYLOGFILE` export, a new secret-on-disk worse than the problem being solved (§2). |
| Always-on / rolling-buffer capture | Explicitly decided against — on-demand only, smallest security surface, matches "troubleshooting a known issue" rather than fleet-wide always-watching. |
| Capturing the CPE's first, wholly unauthenticated empty POST | No identity or stable key exists to match it against (§4). Carries no diagnostic content beyond "a connection happened," already visible in `acs.log`. |
| CIDR-range overlap detection for `remote_ip` captures | Exact string match against `match_value` only. A real range-matching engine is more machinery than a troubleshooting tool needs; revisit if it becomes a real limitation. |
| A new live inter-process streaming channel (gRPC/SSE) between `cmd/api` and `cmd/acs`/`cmd/uspc` | Considered and rejected (§5) — no such channel exists today, and polling every 1-2s is plenty responsive for troubleshooting an already-reproducible issue. Reusing the existing DB-mediated pattern (jobs/outbox) is dramatically cheaper and more consistent with this codebase's architecture. |

## 11. Decisions made during brainstorming

- **Application-level capture, not network-level** — TLS terminates
  in-process; a real pcap would need new, riskier plumbing than the
  problem it solves.
- **Both CWMP and USP from the start** — a shared capture abstraction
  now, rather than retrofitting USP onto a CWMP-shaped format later.
- **On-demand only, never always-on** — smallest security surface,
  matches the actual troubleshooting use case.
- **Three trigger modes, not just device-keyed** — the scenario that
  motivated this feature (a brand-new device that never authenticates)
  needs to be captured by expected identity or remote IP, since no
  `devices.id` exists yet for the device-keyed mode to key on.
- **Pattern-based redaction, not an exact allowlist** — catches
  vendor-specific secret parameters this codebase has no named constant
  for, at the cost of occasional (safe-direction) over-redaction.
- **Digest auth headers captured as-is; Basic auth never captured
  verbatim** — a Digest response is a one-way hash and is the actual
  diagnostic payload this feature exists to show; Basic is a
  reversible-in-practice credential encoding.
- **DB-mediated, reusing the existing jobs/outbox pattern** — no new
  inter-process channel; `cmd/acs`/`cmd/uspc` already touch the database
  once per Inform/message, so one more indexed lookup at that checkpoint
  is the entire mechanism.
- **No background worker for expiry** — an expired session simply stops
  matching new events on its own (`expires_at` in the same query every
  check already runs); only cleanup (status flip + purge) rides the
  existing `internal/retention` sweep.
