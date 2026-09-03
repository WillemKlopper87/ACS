# Security architecture decisions

Remediation P2.3/P2.4 (`ACS_REMEDIATION_EXECUTION_PROTOCOL_2026-09-03.md`
§7) both ask for an explicit, documented choice among named options
rather than leaving the tradeoff implicit. This is that record.

## P2.3 — BSS OAuth client revocation semantics

**Chosen: active revocation checking on every request**, not just at
token issuance.

The other three options the protocol names: accept the residual
one-hour token lifetime as-is, shorten the token TTL, or add a
client-side token-version epoch (the same mechanism operator sessions
already use). Active checking was chosen over all three because it
closes the gap outright rather than bounding it, and because the cost
is small: `internal/bss.OAuthRepository.IsRevoked` is checked in
`cmd/bssadapter`'s `withAuth` behind a 15-second in-process cache
(`clientRevoked`), the same shape and TTL as `cmd/api`'s existing
`tokenCurrent`/`versionCache` for operator JWT revocation — one
Postgres lookup per client per 15 seconds, not per request.

**What this means in practice:** revoking a BSS OAuth client from the
admin panel cuts off both new token issuance (already true before this
change) and any already-issued token, within at most 15 seconds — down
from the previous worst case of the token's full one-hour lifetime.
The 15-second figure is a real residual window, not zero: `cmd/api`
(where revocation happens) and `cmd/bssadapter` (where tokens are
verified) are separate processes with independent caches and no
cross-process invalidation push. Closing that last 15 seconds would
require either a pub/sub invalidation channel or moving the check
off-cache entirely (a Postgres round trip on every BSS API call) —
judged not worth it against BSS integration traffic assumptions;
revisit if that assumption changes.

Emergency procedure for a suspected compromised client_secret or
token: see `bss-integration-guide.md` §3.1.

## P2.4 — Browser operator token storage

**Chosen: strong CSP plus a deliberately bounded token lifetime, kept
in `localStorage`** — not a BFF/HttpOnly session cookie, not a
short-lived-access-plus-refresh-token flow.

Both alternatives are legitimate and either closes the underlying
concern (an XSS payload reading the bearer token out of `localStorage`)
more completely than a CSP-based mitigation can. They were not chosen
for this pass because both are frontend session-architecture changes —
a BFF cookie flow needs a server-side session store and CSRF
protection that don't exist today; a refresh-token flow needs a new
token class, rotation, and revocation semantics of its own — and the
concrete precondition for the risk (an XSS payload existing to read the
token in the first place) is not currently met: `fable5.1_review.md`'s
independent review found no XSS surface in the frontend (`grep` for
`dangerouslySetInnerHTML`/`innerHTML`/`eval` came back empty; every
device-reported string renders through JSX text nodes; the one
stored-URL vector — the web-GUI "open in new tab" link — is
scheme-validated server-side). Mitigating a vulnerability class with no
current instance is lower priority than the P0/P1 findings that had
concrete exploit paths.

What's already in place, and stays as the chosen mitigation:

- `frontend/nginx.conf` ships a real CSP (`script-src 'self'`, no
  `unsafe-inline`/`unsafe-eval` for scripts), `X-Frame-Options: DENY`,
  `Referrer-Policy: no-referrer`, and a restrictive `Permissions-Policy`.
- The session JWT's lifetime is bounded (`jwtTTL`, `cmd/api/auth_handlers.go`)
  and revocable within the same 15-second cache window as P2.3's OAuth
  check (`tokenCurrent`/`versionCache`) — a stolen token is not valid
  indefinitely even without an XSS bug providing a delivery mechanism.
- WebSocket/iframe handshakes (the CLI console, the web-GUI proxy) never
  put the session JWT in a URL at all — `client.ts`'s browser-ticket
  design mints a 60-second, audience-bound, single-purpose ticket just
  before use instead (P1.4/P1.5), so the long-lived session token's
  blast radius if it *did* leak is smaller than it would otherwise be.

**Revisit this decision if** an XSS-capable dependency or code path is
introduced (a new rich-text/markdown renderer, a new
`dangerouslySetInnerHTML` use, a third-party widget), or if the
operator console starts handling data from a source this document's
"no XSS surface" premise doesn't cover.
