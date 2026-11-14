# Console redesign and two-step sign-in

Two things: why the console feels assembled from parts and how to fix that without removing anything an operator can do today (§1 to §4), and two-step sign-in for operators (§5). Written 2026-11-14 from a live walk of every screen with simulated devices (`docs/LIVE-CHECK-2026-11-14.md`) and a read of the styling code. §4 and §5 say what is built.

Related: `docs/CONSOLE-UX-PROPOSAL-2026-10-30.md` (device workspace, navigation areas, onboarding) and `docs/DEVICE-MANAGEMENT-PLAN.md` (device and group workflow).

## 1. Why it feels thrown together

| # | Cause | Evidence |
|---|---|---|
| 1 | **Three layouts for the same job.** Groups and Templates put a create form and an empty "Select a…" panel across the top half, with the list below. Operators puts a full-width form on top. Onboarding and the device page use a side sub-menu. | `DeviceGroups.tsx`, `ConfigTemplates.tsx`, `Operators.tsx`, `Onboarding.tsx` |
| 2 | **Create forms are always open.** On most list screens the data the operator came for starts halfway down the page, under a form they use once a week. | Groups, Templates, Policies, Scheduled Jobs, Rollouts, Operators, Tenancy, BSS |
| 3 | **No shared building blocks.** 343 inline `style={{…}}` overrides (48 on the device page), 70 hard-coded colours outside the theme tokens, 25 distinct font sizes. | `grep` of `frontend/src` |
| 4 | **Two kinds of confirmation.** 20 native browser `confirm()` pop-ups beside 7 custom dialogs: different look, different buttons, no theme, "OK" for "Delete". | Fixed, §4 |
| 5 | **Developer wording on screen.** `PERIODIC_FALLBACK_ONLY`, `under_5m`, `oui_serial`, `command_key`, "Workflow A", and paragraphs about the data model inside forms. | Dashboard, BSS, Templates, Jobs |
| 6 | **Navigation grew by accretion.** 20 items in 7 groups with near-duplicates ("Bulk actions" / "Bulk operations", "Jobs" / "Scheduled Jobs"). At 900 px height the last three groups are below the fold with no scroll cue. | `NAV` in `App.tsx` |
| 7 | **Devices are named by machine identity.** Lists lead with `4C62DF+3TG02241+NK2400000011`; the label or subscriber, when there is one, comes second. | Inventory, Bulk actions, Jobs, Groups |
| 8 | **Widgets that have nothing to say.** Full-width bars for a single value; a Temperature card that reports no device has one. The device overview leaves most of its width empty while firmware, uptime and signal are a click away. | Dashboard, device Overview |
| 9 | **Four themes.** Every new screen must be checked in four palettes, and mostly is not: hard-coded colours (cause 3) are what breaks in the others. | `index.css` |

None of this is about missing features. It is the same handful of jobs (list things, make one, change one, confirm something risky) solved a little differently each time a request came in.

## 2. Principles

1. **Nothing is removed.** Every field, action and URL that works today keeps working. Screens are rearranged, not cut.
2. **One pattern per job.** A list screen looks like every other list screen; a confirmation looks like every other confirmation.
3. **The data comes first.** A screen opens on what the operator came to see. Creating something is a button, not a standing form.
4. **Say it in the operator's words**, with the raw value on hover for whoever needs it.
5. **A person before a serial.** Name or subscriber, then model, then the identifier.
6. **Change one screen at a time**, each reviewable on its own, on top of shared components. No big-bang rewrite.

## 3. Plan

| Step | What | Removes cause |
|---|---|---|
| **A** | **Shared components**: confirmation dialog, drawer, page bar. Done, §4. Still to add: form field, empty state, a five-step type scale, and a lint rule against new inline styles and raw colours. | 3, 4 |
| **B** | **The list-screen pattern** on every list screen: a one-line description and the primary action on the right, filters, then the table; create and edit in a drawer; a row opens its detail in a drawer. Groups is done (§4). Next, in this order: Templates, Policies, Scheduled Jobs, Rollouts, Operators, Alerting, Tenancy, BSS. | 1, 2 |
| **C** | **Plain-language labels** from one table (the status badges already work this way): reachability, inform recency, job types, BSS states. Form help text shortened to one line, with the detail behind a "?" . | 5 |
| **D** | **Navigation in five areas**, related screens as tabs of one page: *Overview* (Dashboard, Fleet Health, Alerting), *Devices* (Inventory, Onboarding, Groups), *Activity* (Jobs, Bulk operations, Scheduled Jobs, Bulk actions as its "New" button), *Configuration* (Templates, Policies, Rollouts), *Administration* (Operators, Tenancy, BSS/NaaS, Audit, Reports, Captures). Old URLs redirect. A global search box in the top bar using the existing subscriber lookup. | 6 |
| **E** | **Device first screen**: name or subscriber, model and serial under it, then a health row (firmware, uptime, signal, Wi-Fi clients, last job) in the space that is empty today. Lists show label or subscriber first, serial second. | 7, 8 |
| **F** | **Dashboard**: a widget with one value shows a number, not a bar; a widget with nothing to show collapses to a line. | 8 |
| **G** | **Themes**: keep Dark and Light as supported; keep Unfold and Fluent selectable but stop designing for them until the raw colours are gone. Decision needed. | 9 |

Steps A and B change how screens are laid out, not what they do, so they can ship screen by screen. D is the only step an operator will notice as "things moved", and should go out with a short note in Help.

## 4. Built so far

**One confirmation dialog** (`frontend/src/lib/confirm.ts`, `components/ConfirmHost.tsx`). All 20 native `confirm()` calls now go through it. It is themed, keyboard-safe (focus starts on "Keep as it is"; Escape and a click outside mean no; focus returns where it was), and the button names the action ("Delete", "Factory reset", "Reboot") in the danger colour when the action takes something away. A message with a blank line splits into the question and its consequence. With no host mounted, as in a component unit test, it falls back to the browser's own confirm, so a destructive action is never left unasked.

**Drawer and page bar** (`components/Drawer.tsx`, `.page-bar` in `index.css`). A panel over the right edge for creating or inspecting one thing while its list stays put.

**Groups on the list-screen pattern** (`screens/DeviceGroups.tsx`). The screen opens on the groups table with a "New group" button. The create form and the member manager are the same forms as before, in drawers. Nothing was removed: create, delete, add by picker, add by pasted serials and remove member all work as they did.

Verified in a browser against the running stack and by two new Playwright tests (confirmation dialog; two-step sign-in).

## 5. Two-step sign-in (MFA)

### What it is

A time-based one-time code (TOTP, RFC 6238) from an authenticator app, asked for after the password. It applies to operators signing in to the console. Devices and BSS/NaaS OAuth clients are not affected.

### How sign-in works now

1. `POST /auth/login` with the password, as before.
2. If the operator has no second step and their role does not require one: a session, as before.
3. If they have one: no session. The answer is `{"mfa":"verify","mfa_token":…}`. The token lasts five minutes, is bound to this one purpose and is refused everywhere else. `POST /auth/login/mfa` with the token and a code returns the session.
4. If their role requires a second step and they have none: `{"mfa":"enroll",…}`. They set it up on the sign-in page (`/auth/login/mfa/enroll`, then `/confirm` with a first code), are shown their recovery codes once, and are signed in.

### Rules

| Rule | Why |
|---|---|
| A code is accepted once. The last accepted 30-second step is stored and anything at or before it is refused. | A code seen over a shoulder or in a proxy log cannot be replayed. |
| One step of clock drift either way is accepted. | Phones run a little off. |
| Five guesses, then one per 12 seconds, per operator, on every route that takes a code. | Six digits are only safe if they cannot be tried many times. |
| The secret is sealed with AES-256-GCM under a key derived from `ACS_CREDENTIAL_ENCRYPTION_KEY`, bound to the operator's id. | A database dump alone does not yield working secrets, and a sealed secret cannot be moved to another account. |
| Ten recovery codes, shown once, stored as hashes, each usable once. They are never written to the audit log. | A lost phone must not mean a lost account, and the recovery path must not be the weak one. |
| Set-up gates nothing until a first code is confirmed. | A half-finished set-up must not lock anyone out. |
| Turning it off, or making new recovery codes, needs a current code. | An unlocked session on a desk is not enough. |
| A wrong code inside a session is `403`, not `401`. | The session is still good; a client must not read it as "signed out". |
| `ACS_MFA_REQUIRED_ROLES` (`superadmin,manager` or `all`) makes it mandatory for those roles; they cannot turn it off. The API refuses to start with it set and no encryption key. | Enforce where it matters, and fail at start-up rather than at each sign-in. |

### Lost phone

- **Another superadmin exists:** Operators → *Reset two-step* on the row. The operator's sessions end; the reset is audited.
- **No other admin:** `scripts/reset-admin-password.sh` now clears the bootstrap admin's second step as well.

### Where it is in the console

- **Sign-in**: the code prompt, and the set-up flow for roles that require it.
- **Account** (the operator's name in the sidebar): turn it on with a QR code or a typed key, see how many recovery codes are left, make new ones, turn it off.
- **Operators**: a "Two-step" column and the reset button.

### Audit

`OperatorMFAEnabled`, `OperatorMFADisabled`, `OperatorMFAReset`, `OperatorMFAFailed`, `OperatorMFARecoveryCodeUsed`, `OperatorMFARecoveryCodesRegenerated`; `OperatorLogin` records whether a code or a recovery code was used.

### Not done, and worth knowing

- **Existing sessions are not ended when someone turns MFA on.** They run to their 8-hour expiry or sign-out.
- **The password-reset email is unchanged**: it resets the password only. With MFA on, the second step still stands after a reset, which is the intent.
- **The guess limit is per API process**, like the other limits (`docs/ASSESSMENT-2026-11-09.md`, A4). With several replicas it multiplies.
- **The session token is still kept in browser storage** (same assessment, C2). MFA protects sign-in, not a token already issued.
- **Single sign-on** (OIDC) remains the better end state for a large operator and is not blocked by this.

### Tests

- `backend/internal/mfa/totp_test.go`: RFC 6238 vectors, drift window, replay, recovery-code shape.
- `backend/cmd/api/mfa_integration_test.go`: self set-up and sign-in, replayed code, recovery code used once, token of one step refused by another, admin reset ending the session, recovery codes absent from the audit log, required role forced to enrol and unable to turn it off, the guess limit.
- `frontend/e2e/console.spec.ts`: code prompt, wrong code, sign-in.
