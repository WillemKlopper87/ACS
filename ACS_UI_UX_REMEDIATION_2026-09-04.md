# ACS — UI/UX & Functional Remediation, 2026-09-04

Working checklist for the codebase review of 2026-09-04 (visual + functional
improvements to the operator console and the API surface behind it).

**Scope:** the operator-facing console (`frontend/`) and the parts of
`cmd/api` it depends on. Security findings are tracked separately in
`ACS_REMEDIATION_EXECUTION_PROTOCOL_2026-09-03.md` and `fable5.1_review.md`;
items below marked *(prior)* were raised in `fable5.1_review.md` and verified
still open in this tree at `57f4c91`.

**Method:** every item was verified by reading the code, not inferred. Each
carries the `file:line` it was found at. Fixes land one coherent change per
commit, pushed to `main`.

---

## P0 — functional bugs

- [x] **P0.1 — CORS omits `DELETE`, breaking every delete in the product.**
  `Access-Control-Allow-Methods` is `GET, POST, PUT, OPTIONS`
  (`backend/cmd/api/middleware.go:62`) while `routes.go` registers 13 `DELETE`
  routes. The console talks to the API cross-origin (no `proxy_pass` in
  `frontend/nginx.conf`; its CSP `connect-src` names the API origin
  explicitly), so every delete fails preflight in the shipped topology.
  Undetected because the integration server is built without `withCORS`.
  *(prior: M-15)*

- [x] **P0.2 — Tenancy deletes fail silently.**
  `onClick={async () => { await api.deleteRegion(r.id); load(); }}` —
  no `try`/`catch`, no confirmation, no toast (`frontend/src/screens/Tenancy.tsx:114`,
  `:140`, `:164`). A rejected delete (FK violation from devices still owning the
  customer, or P0.1) produces an unhandled promise rejection and zero UI
  feedback; `load()` never runs so the row stays on screen.

- [x] **P0.3 — `LEASE_EXPIRED_UNSAFE_RETRY` renders as benign grey.**
  `TONE_BY_VALUE` (`frontend/src/components/StatusBadge.tsx:4-31`) has no entry
  for it, so it falls through to `neutral`. This is the status meaning "a
  non-repeatable job's lease expired and it was deliberately not retried" — the
  one that most needs operator attention. `AWAITING_TRANSFER_COMPLETE`
  (rollout device state) is missing too.

- [x] **P0.4 — The connection indicator is always green.**
  `index.css:247` defines `.conn-indicator.down .dot { background: var(--danger) }`
  but nothing ever applies `.down` (`frontend/src/App.tsx:125-127`). The dot
  reads "connected" even when the API is unreachable.

- [x] **P0.5 — Hardcoded `dev` environment badge ships in every build.**
  `frontend/src/App.tsx:85`. *(prior: L-19)*

- [x] **P0.6 — The SSH/Telnet WebSocket leaks on unmount and device switch.**
  The only cleanup effect (`frontend/src/components/RemoteShell.tsx:62-66`)
  disposes the xterm instance and the resize listener but never closes
  `ws.current`. Closing the panel leaves an authenticated shell bridge open
  server-side; switching devices keeps the *previous* device's session live
  while the UI shows the new one. *(prior: H-9)*

- [ ] **P0.7 — The fleet screen silently truncates at 500 devices.**
  `api.listDevices(1, 500)` (`frontend/src/screens/DeviceFleet.tsx:47`) requests
  exactly the backend's `maxPageSize`, there is no pagination UI, and
  `stats.total` is computed from `devices.length` (`:79`). A 2000-device fleet
  displays "500". The handler already returns `total`; the frontend discards it.

## P1 — backend capability with no operator UI

- [ ] **P1.1 — Sign-out never revokes the session server-side.**
  `logout: clearAuth` (`frontend/src/auth/AuthContext.tsx:16`) is local only.
  `POST /api/v1/auth/logout` exists and revokes every session of the caller
  (`backend/cmd/api/routes.go:41`) and is in the generated client — never
  called. *(prior: H-6)*

- [ ] **P1.2 — Uploaded files can be requested but never retrieved.**
  `createUpload` is wired (`frontend/src/screens/DeviceDetail.tsx:208`), but
  `GET /api/v1/devices/{id}/uploads` and `GET /api/v1/uploads/{id}/file`
  have no UI at all; `api.listDeviceUploads` is dead code. An operator can ask a
  device for a config backup or log bundle, and has no way to see or download
  the result.

- [ ] **P1.3 — No single-device firmware push.**
  `POST /api/v1/devices/{id}/firmware` (`routes.go:175`) is unexposed; firmware
  can only be shipped through a fleet rollout, which is the wrong instrument for
  one-device support work.

- [ ] **P1.4 — Operators cannot be disabled or removed.**
  The API offers create / list / reset-password / scopes / global-access and no
  delete or disable (`routes.go:42-51`). A departing employee's account cannot be
  offboarded. Needs a backend route plus UI; larger than the rest of P1.

## P2 — visual & design system

The token system itself is sound (four themes, consistent naming, real
`:focus-visible`, `.sr-only`). These are the places components escape it.

- [ ] **P2.1 — No small-button variant.** `style={{ padding: "0.15em 0.5em", fontSize: "0.72rem" }}`
  is copy-pasted across 5+ files; the sign-out button is a 10-line inline style
  block (`App.tsx:109-122`). Add `.btn.sm` and a ghost variant, then delete the
  inline copies.
- [ ] **P2.2 — Destructive actions are styled like ordinary ones.** `.btn.danger`
  exists and is used 5 times, but the group / policy / schedule / template /
  tenancy deletes all use plain `.btn`. Add confirmations to the list-screen
  deletes that lack them, matching the device-level actions that already confirm.
- [ ] **P2.3 — No visible page heading.** The only `<h1>` is `sr-only`
  (`App.tsx:133`); screens open straight into a toolbar with nothing naming them
  but the sidebar highlight.
- [ ] **P2.4 — Nothing responds below the sidebar breakpoint.** `.sidebar` is a
  fixed `14.5rem`/`100vh` with no media query while `.split.two-col` collapses at
  900px; with `table-wrap { max-height: 62vh }` and `white-space: nowrap` on every
  `td`, narrow viewports get nested double-scroll.
- [ ] **P2.5 — `.form-row` has no `flex-wrap`**, so the four-field Operators
  create row squashes instead of wrapping.
- [ ] **P2.6 — `--ink-dim` and `--ink-faint` are visually identical in dark**
  (`#98a5bc` vs `#9aa7bb`, `index.css:11-12`) — the two-tier text hierarchy those
  tokens encode does not exist in the default theme.
- [x] **P2.7 — Raw enum strings are shown to users** (`HTTP_200_INFORM_RECEIVED`,
  `PERIODIC_FALLBACK_ONLY`, `LEASE_EXPIRED_UNSAFE_RETRY`). Add a label map beside
  the tone map in `StatusBadge.tsx`.
- [ ] **P2.8 — Login is entirely inline-styled**, bypassing the design system it
  sits next to (`frontend/src/screens/Login.tsx:5-87`).
- [ ] **P2.9 — No `prefers-reduced-motion` guard**, and `dot-pulse` animates
  infinitely on every `.pill-ok::before` (`index.css:430`) — hundreds of
  simultaneous animations in a populated table.

## P3 — accessibility *(prior: M-20)*

- [ ] **P3.1 — Table rows are mouse-only.** `<tr onClick>` with `cursor: pointer`,
  no `tabIndex` / `onKeyDown` / `role` (`DataTable.tsx:161-166`). Row-click is the
  primary route into Device Detail, so that path is keyboard-inaccessible.
- [ ] **P3.2 — Sortable headers are click-only and carry no `aria-sort`**
  (`DataTable.tsx:131-139`).
- [ ] **P3.3 — Select-all and row checkboxes are unlabeled** (`DataTable.tsx:65-88`).
- [ ] **P3.4 — Toasts have no `role`/`aria-live`** (`Toast.tsx:10`), and most
  screens report failures exclusively via toast — so errors are silent for
  screen-reader users. The close button has no `aria-label` and the dismiss
  target is a bare `div` `onClick`.
- [ ] **P3.5 — Error toasts auto-dismiss after 4.2s** regardless of severity
  (`lib/toast.ts:27`).
- [ ] **P3.6 — Placeholder-as-label throughout the create forms** — no accessible
  name, and the text vanishes once the field has content.

## P4 — quality

- [ ] **P4.1 — TypeScript `strict` is off.** `frontend/tsconfig.app.json` sets no
  `strict` and no `strictNullChecks`, so CI's `npm run build` typecheck runs with
  null-safety disabled in a codebase full of optional API fields. *(prior: H-8)*
- [ ] **P4.2 — `useLive` has no in-flight or abort guard** (`lib/useLive.ts:16-22`),
  so overlapping polls commit out-of-order and flicker stale data. *(prior: M-22)*
