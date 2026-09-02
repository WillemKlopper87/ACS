# TR-069 ACS — Handoff (state as of 2026-08-18)

Written for the next agent/engineer picking this project up. It records what
exists, what was verified today, what is missing, and what the previous
reviewer would have done differently. Read this before the 90 KB build plan.

> **Current status addendum — 2026-09-02:** This handoff contains historical
> snapshots below. The current source of truth is the root `README.md`, the
> latest commits on `main`, and this addendum. Since the original August review,
> ACS has added fail-closed production configuration, centralized tenancy
> enforcement, expiring transfer tokens and bounded uploads, SSRF/SSH controls,
> Digest replay protection, lease recovery, graceful service shutdown, pool and
> migration hardening, retention pruning, object storage, CI/SBOM/image gates,
> generated API contracts, route-based frontend loading, TanStack Query,
> Playwright/axe coverage, and a committed mock-CPE/load harness.
>
> Current repository checks on `main` pass: backend `go test ./...` and
> `go vet ./...`; frontend Vitest, lint, TypeScript build, and production build.
> The remaining release evidence is primarily real-device compatibility and
> load/soak qualification. `docs/COMPATIBILITY.md` currently has no real-device
> rows and no recorded load run. XMPP connection requests and TR-098 writes
> remain unsupported. A production backup/restore drill, external CI branch
> protection, and organization-level security/licensing approvals must also be
> recorded before release.

---

## 0. Orientation — two lineages, one live codebase

| Location | What it is | Status |
|---|---|---|
| `../tr069-acs-{architecture,prerequisites,roadmap,todo}.md` (ACS folder root, Aug 1–2) | Docs for an earlier **Python** ACS (`tr069-acs/src/tr069_acs/…`, `run_server.py`) | **Superseded. The Python source is not in this folder or anywhere on this machine.** Useful only for protocol rationale (the architecture doc's spec-corrections table is still good). |
| `ACS-main/` (this repo, Aug 4–10) | The **Go + React/TS** rebuild — `tr069-acs-application-design-v3.md` → `tr069-acs-build-plan.md` → code | **Live.** Remote: `github.com/WillemKlopper87/ACS`, branch `master`, 3 commits, clean tree. |
| `../ACS-main.zip` (Aug 7) | Snapshot before the 3 git commits | Can be deleted; git has everything. |

Git history is one 37k-line squash ("Import ACS…", `df309c5`) plus two script
fixes. There is **no per-feature history**; the build plan (§4–§7 of
`tr069-acs-build-plan.md`) is the only record of *why* things are the way they
are. Its running log stops before the last "admin-platform backlog" work landed
(see §2.4 below).

## 1. Verified today (2026-08-18, this machine)

```
backend:  go build ./...   OK   (go 1.26.5)
          go vet ./...     OK
          go test ./...    OK   — 83 tests / 20 files, but see coverage note in §4
frontend: tsc -b && vite build   OK  (750 kB single chunk, no code splitting)
          oxlint                 0 errors, 5 warnings
```

Nothing was run against Postgres or a browser today; the docs' "verified live"
claims date from Aug 4–10 and were done with an ad-hoc CWMP session simulator
**that is not committed** (only golden XML fixtures under `backend/test/fixtures/`).

## 2. What has been built

### 2.1 Backend (`backend/`, Go, 115 files / ~19k lines, `database/sql` + pgx stdlib, Postgres 16)

Three services + two tools:

| Binary | Role |
|---|---|
| `cmd/acs` | CWMP gateway. Catch-all `POST /` (any path) → Inform → InformResponse → serial one-RPC-in-flight dispatch from the DB job queue → TransferComplete. TLS/mTLS, Digest (+ optional Basic) auth, per-device + per-IP rate limit, embedded STUN responder on UDP :3478, policy enforcement + zero-touch template auto-apply + parameter discovery on `0 BOOTSTRAP`. 1,212-line `main.go`. |
| `cmd/api` | Operator REST API (~119 routes, listed in the audit table below), JWT auth + 4-tier RBAC (`readonly < noc < manager < superadmin`) + curated per-role permission matrix, plus two in-process workers (connection-request dispatcher, scheduled jobs). 1,419-line `main.go`. |
| `cmd/bssadapter` | BSS/CRM-facing gateway: account↔device mapping, `MODIFY_WIFI` order dispatch (idempotent on `external_order_id`), job-status passthrough, OAuth2 client-credentials issuer (own signing secret), HMAC-signed webhook delivery worker with backoff. Talks to `cmd/api` over HTTP with `ACS_INTERNAL_SERVICE_TOKEN`. |
| `cmd/probe` | Phase-0 lab harness (no DB): accepts a real CPE Inform, probes GetRPCMethods/GetParameterNames on both roots, writes JSONL. |
| `cmd/hashpw` | bcrypt hash CLI for manual admin password resets. |

CWMP RPCs implemented (`internal/cwmp/`): Inform, GetRPCMethods, Get/SetParameterValues, GetParameterNames, Set/GetParameterAttributes, AddObject/DeleteObject, Download, Upload, TransferComplete, Reboot, FactoryReset, ScheduleInform. Namespace detection cwmp-1-0…1-4, `cwmp:ID` echo, XXE/billion-laughs hardening tests. **Not implemented:** GetOptions/SetVouchers/Kicked/RequestDownload/AutonomousTransferComplete/ChangeDUState; TR-098/IGD:1 write paths (all writes hardcode `Device.*`).

Domain packages (`internal/`, 25): `jobs` (DB queue, `FOR UPDATE SKIP LOCKED` lease), `sessions` (Postgres session rows + state machine), `devices` (+ `devices/adapters` embedded per-vendor path catalog), `parameters` (cache + change-only history), `firmware` + `rollout` (canary %, max failure rate, rollback image, start/advance), `templates` (config templates, bulk + auto-apply), `policy` (continuous compliance on every Inform), `scheduler`, `credentials` (versioned per-device connreq creds, AES at rest), `connreq` (HTTP Digest connection request), `stun`, `ratelimit`, `operators` + `auth` (bcrypt, HS256 JWT, RBAC, password reset tokens), `observability` (audit log + Prometheus), `bss`, `uploads`, `tenancy` (regions → customers → devices, projects as tags, operator scopes), `dashboard` (per-operator widget layout), `cliaccess` (SSH/Telnet ↔ WebSocket bridge, device web-GUI reverse proxy), `vpn` (WireGuard peer registry/keygen/config render), `mailer` (SMTP password reset), `store` (embedded migrations 0001–0039, no advisory lock, no down-migrations).

### 2.2 Frontend (`frontend/`, React 19 + TS 6 + Vite 8, ~7.5k lines TSX)

17 screens, all wired to real endpoints (no mock data, no TODOs): Dashboard (customizable widgets), Device Fleet, Fleet Control (bulk actions incl. "select all N matching"), Fleet Health, Jobs, Groups, Templates, Scheduled Jobs, Rollouts, Policies, Audit Log, Operators (+ RBAC matrix, scopes), Tenancy (+ bulk device import json/csv/xml), Reports (xlsx export), BSS Integration (+ 4 troubleshoot probes), Login (+ forgot/reset), and a 548-line Device Detail panel (parameters, history, live GET, discovery, tags, location, jobs, credentials, ping/traceroute, add/delete object, schedule-inform, reboot, factory reset, and embedded Console / RemoteShell (xterm.js) / WebGUI iframe / VPN / Tenancy sub-panels).

Shared `DataTable` (TanStack Table + virtual), `StatusBadge`, toast, hand-rolled CSS token system with 4 themes, `useLive` 6 s polling that pauses on hidden tab. **No router, no server-state library, ~zero a11y.** Since the 2026-08-30 hardening pass: screens are lazy-loaded chunks with an error boundary, and there is a small Vitest suite (auth, roles, format, StatusBadge).

### 2.3 Infra / deploy

- `infra/docker-compose.yml`: Postgres 16 + Prometheus + Grafana (provisioned "ACS Fleet Health" dashboard, 3 alert rules, **no Alertmanager**). This is a *monitoring* stack — the Go services are **not containerized**.
- Real deploy path = `scripts/quickstart.sh` → `start.sh` on an Ubuntu EC2 host: `go build`, `nohup` the two binaries with PID files, `python3 -m http.server` for the built SPA, secrets generated once by `gen-env.sh` into `~/.acs-secrets.env` (chmod 600). nginx and systemd units exist only as heredocs inside `EC2-DEPLOYMENT-GUIDE.md`. Dockerfiles for all four services plus a `containerized` compose profile, and a GitHub Actions gate (`.github/workflows/ci.yml`) now exist; still no IaC beyond the EC2 shell scripts.
- It **has been deployed to EC2 with real CPEs** (a ZTE-family "ZOWEE 5G CPE Max 6" behind CGNAT — see `deployment-testing-onboarding-guide.md`). Commit `df309c5` was driven by devices failing to connect: cwmp:ID echo, whitespace-only POST = empty request, accept any path, TLS floor 1.0 + legacy suites, Basic auth fallback, drain body before 401. Whether the device ultimately onboarded end-to-end is not recorded anywhere.

### 2.4 Things that exist in code but the build plan never mentions

The "admin-platform backlog" (migrations 0027–0039, ~Aug 6–7): parameter discovery, STUN NAT-traversal columns, per-device CLI credentials + SSH/Telnet bridge, device web-GUI proxy, RBAC tier expansion, multi-tenancy, dashboards, device location, WireGuard VPN concentrator (peer re-enroll, IP reuse), BSS OAuth clients. Only migration doc-comments and package doc-comments describe them. `cliaccess` and `vpn` are explicitly "scaffold now, functional later" — the routes are live but **cannot work against a CGNAT'd device** until a tunnel exists.

## 3. Known gaps the docs already admit (don't re-discover these)

- **Instant device actions don't work behind CGNAT.** HTTP Connection Request only reaches devices with a routable IP; the TR-069 Annex G UDP Connection Request datagram is **not** implemented (spec for the HMAC signature couldn't be sourced — `deployment-testing-onboarding-guide.md` §9). Everything else reaches a device on its next periodic Inform.
- BSS `SUSPEND`/`ACTIVATE` refused by design pending a per-vendor walled-garden answer; BSS webhooks documented as target design, `/bss/v1/*` has no rate limiting per the guide (adapter code has an in-process limiter — reconcile).
- No TR-098 write paths; no per-vendor canonical parameter registry beyond the embedded catalog; OpenAPI specs exist (`backend/openapi.yaml`, `backend/openapi-bssadapter.yaml`) and a Go test fails on drift from the registered routes; `SetParameterAttributes` has no UI.
- Prerequisites P1–P5 (data-model root, CWMP amendment, mTLS at factory, NAT/STUN, third-party license) were never confirmed against real units — the compatibility matrix `docs/device-compatibility-matrix.md` the plan calls for was never produced.
- Production hardening checklist in `EC2-DEPLOYMENT-GUIDE.md` §12: all 9 boxes unticked.

## 4. Findings from this review (verified, with locations)

### Security / correctness — fix before any wider exposure
1. **Auth fails open.** `cmd/api/auth_handlers.go:66` — empty `ACS_JWT_SIGNING_SECRET` ⇒ every route (operators CRUD, factory-reset, CLI shell, VPN keys) is unauthenticated with only a startup warning. Same fail-open shape for `ACS_CREDENTIAL_ENCRYPTION_KEY` (plaintext device passwords) and CWMP Digest (`internal/auth/digest.go:44`). `gen-env.sh` does set these, so the EC2 path is fine — but a bare `go run` isn't. Should be fail-closed outside an explicit `ACS_DEV_MODE`.
2. **Digest nonce is never stored/validated** (`internal/auth/digest.go:52-91`): any echoed nonce accepted, `nc` untracked ⇒ unbounded replay. Also one shared CPE→ACS credential for the whole fleet.
3. **JWT accepted from `?token=` on every route** (`cmd/api/auth_handlers.go:160`) — needed only for the WebSocket shell and web-GUI iframe (`frontend/src/api/client.ts:438-456`); tokens will land in nginx/proxy logs and Referer. Use a short-lived one-time ticket for those two paths.
4. **`ACS_INTERNAL_SERVICE_TOKEN` is a static, non-expiring superadmin bypass** (`auth_handlers.go:77-81`), presentable via `?token=`.
5. **No job-lease expiry.** A job leased `RPC_SENT`/`IN_PROGRESS` when the process dies is stranded forever (`internal/jobs/job.go:306-363`, no reaper anywhere). Combined with 6 below this is a real data-loss path.
6. **`cmd/api` has no graceful shutdown** — `context.Background()` for both workers, no `signal.NotifyContext`, no `Shutdown` (`cmd/api/main.go:59,363,372,389`). `cmd/acs` does it right; copy that.
7. **CORS default `*`** (`cmd/api/main.go:380`); web-GUI reverse proxy uses `DefaultTransport` with no timeout (`cmd/api/webgui_handlers.go:110`); API server sets only `ReadHeaderTimeout` (`main.go:384`); SSH bridge uses `ssh.InsecureIgnoreHostKey()` (`internal/cliaccess/bridge.go:38`).
8. Public unauthenticated CPE endpoints `GET /firmware/images/{id}/file` and `PUT /uploads/{id}/receive` — no visible size cap on the receive path. Embedded STUN answers anyone on the internet (no message integrity by design, `internal/stun/server.go:12`).
9. Migration runner has no advisory lock (two instances race on startup); no `SetMaxOpenConns`/`ConnMaxLifetime` anywhere; `excelize` is a real runtime dep listed as indirect in `go.mod` (`go mod tidy`).
10. Infra: Grafana `admin/admin` + anonymous Viewer enabled; Postgres `acs/acs`; all three compose services bind `0.0.0.0`. `scripts/reset-admin-password.sh:20` interpolates the hash into SQL; hardcoded container name `infra-postgres-1`; `start.sh` builds/starts everything *before* failing on IP detection.

### Test coverage
- **Thin tests** in `cmd/acs` (the 1.2k-line CWMP state machine — still zero), `sessions`, `devices`, `scheduler`, `policy`, `tenancy`, `parameters`, `templates` (zero). Tests exist for wire format, auth (incl. Digest replay/expiry), jwt, bss, connreq, credentials, ratelimit, stun, vpn, cliaccess, store, adapters, config, transfer, netguard, jobs, rollout, firmware/uploads storage, mailer, operators, and `cmd/api` (auth route policy, scope predicate, OpenAPI drift). No DB-backed integration harness or mock CPE yet — see ACS_CODEBASE_AUDIT_2026-08-28.md P2.1/P3.3.
- Frontend: none. `playwright` is a dead devDependency.

### Frontend
- No router ⇒ no URLs/deep links/back button (`frontend/src/App.tsx:23-68`); no server-state cache (17 screens each doing raw `useEffect` fetches — `client.ts:1-4` predicts this); no error boundaries; 750 kB unsplit bundle; `BSSIntegration.tsx` 26 `useState`s, `FirmwareRollouts.tsx` 22; two real `exhaustive-deps` warnings (`ConfigTemplates.tsx:184`, `DeviceGroups.tsx:135`); `frontend/README.md` is still the stock Vite template; `VITE_API_BASE_URL` silently falls back to `localhost:8080` (this already caused a live bug per `start.sh` comments).

## 5. What I would have done differently

Opinionated, in rough order of how much it would have mattered:

1. **Ship a committed mock-CPE simulator (`backend/test/mockcpe/`) before Phase 1**, exactly as the build plan's own repo layout specified. Every "verified live" claim in the plan came from a hand-built, throwaway session simulator. With a real emulator you get end-to-end tests for `cmd/acs`, a fixture for the frontend, and a regression net for the CPE-compat fixes that were needed on EC2 — and the next agent doesn't have to trust prose.
2. **Fail closed on missing secrets.** "Off unless configured, loud warning" is a fine convention for optional integrations (SMTP, VPN), not for auth and encryption.
3. **Keep the process-per-service split, but not the 1.2k/1.4k-line `main.go`s.** Route registration, middleware, and handlers should be packages under `internal/api/…` and `internal/gateway/…` with the CWMP session state machine as its own tested type; `main.go` should be wiring only. Same for `devices/repository.go` (799 lines) — split by concern (list/scope, tags/location, groups).
4. **Job queue needs a visibility timeout / lease reaper and `cmd/api` needs graceful shutdown** from day one — a DB queue without lease expiry is not durable in the way the docs describe it.
5. **Postgres-only was right, but `database/sql` over pgx-stdlib without pool settings or an advisory-locked migrator isn't.** Use `pgxpool` directly (or at least set pool limits) and take `pg_advisory_lock` in `Migrate`.
6. **Don't register scaffolded routes.** `cliaccess`/`vpn`/`webgui` are honestly documented as non-functional behind CGNAT, yet they're in the nav and the route table. Feature-flag them off by default so a reviewer/operator can't mistake them for working features.
7. **Frontend: add react-router and TanStack Query on day one.** Both were foreseeable at 17 screens; retrofitting URLs into a `useState<Screen>` switch and dedup/caching into 17 hand-rolled fetch effects is now a real refactor. Lazy-load screens (`React.lazy` on `SCREEN_COMPONENT`).
8. **Containerize the three Go binaries** and put nginx + systemd (or compose) under `infra/` as versioned files, not heredocs in a markdown guide. Add a minimal CI (`go vet/test`, `tsc`, `oxlint`) — none exists.
9. **Commit incrementally.** One 37k-line squash means no `git blame`; the build plan is doing the job git should.
10. **Write the compatibility matrix.** Phase 0's gate ("do not start Phase 1 until one real CPE has been probed") was skipped; `cmd/probe` exists but its output was never committed as `docs/device-compatibility-matrix.md`. The CPE-compat problems on EC2 are the predictable cost.
11. Smaller: bind compose ports to `127.0.0.1`, drop Grafana anonymous access, `pgxpool`, `go mod tidy`, delete `playwright` or use it, replace `frontend/README.md`, root `.gitignore` for `.env*`/`node_modules`, `.gitattributes` for LF + exec bits.

## 5b. Missing capabilities that would be beneficial (feature gaps, not bugs)

Grouped by area; each item was checked against the code on 2026-08-18. Effort is a rough
S/M/L. Items marked ★ are the ones that most change day-to-day usefulness for an ISP NOC.

### Device / protocol
| # | Capability | Why it matters | Effort |
|---|---|---|---|
| ★1 | **Offline / unreachable detection.** `online_status` is only ever written to `ONLINE` on Inform (`internal/devices/repository.go:46`); no reaper marks a device OFFLINE when it misses N × `PeriodicInformInterval`, and nothing sets `UNREACHABLE` after failed connection requests. | Fleet health, `acs_devices_online`, and the `NoDevicesOnline` alert are all one-directional and therefore misleading. | S |
| ★2 | **Annex G UDP Connection Request** (and/or **XMPP Connection Request, TR-069 Annex K**) | Instant actions for a CGNAT'd cellular fleet; today everything waits for the next periodic Inform. | M–L |
| 3 | **TR-098 / IGD:1 write paths** (root-aware parameter resolution) | Prerequisite P1 says Huawei/Teltonika may be IGD:1; today all writes hardcode `Device.*`. | M |
| 4 | **TR-143 throughput diagnostics** (`DownloadDiagnostics`/`UploadDiagnostics`), NSLookup, WiFi neighbor scan | Nokia FastMile and Zyxel advertise TR-143; the fleet's #1 support question is "is my speed OK". Ping/traceroute already exist to copy from. | M |
| 5 | **Per-device CPE→ACS Digest credentials** (currently one shared fleet secret) + nonce validation | Compromise of one CPE = fleet-wide credential; also unblocks proper credential rotation on the CPE side. | M |
| 6 | **Raw SOAP capture per session** (`payload_xml`/`payload_json` per design v3 §7) with a "download session transcript" action | The single most useful troubleshooting tool for vendor tickets; today only event codes are stored. | S–M |
| 7 | **Device event/Inform timeline** (event codes, boot/reboot history, value-change events as alarms) | Value-change notifications are subscribed but nothing turns `4 VALUE CHANGE` into an operator-visible alarm. | M |
| 8 | **Pre-provisioning by serial** (device record + template bound *before* first Inform) | Zero-touch today keys on `model_filter` only; ISPs usually know serial → subscriber before shipping. Tenancy import gets halfway there. | S |
| 9 | **Object-store backend for firmware/uploads** (S3/MinIO; local disk today) | Needed for HA/multi-instance and for large firmware catalogs. | M |
| 10 | Remaining RPCs: `GetOptions/SetVouchers`, `RequestDownload`, `AutonomousTransferComplete`, `ChangeDUState` (TR-157 software modules) | Long-tail; `AutonomousTransferComplete` matters if CPEs self-update. | S each |
| 11 | **TR-369 / USP** path for the Nokia devices that advertise it | Future-proofing only; not urgent. | L |

### Operations / platform
| # | Capability | Why | Effort |
|---|---|---|---|
| ★12 | **Data retention / pruning jobs** for `parameter_history`, `audit_log`, `cwmp_sessions`, `jobs` (nothing prunes anything today) | Postgres will grow unbounded at 18k devices × periodic Informs. | S |
| ★13 | **Alertmanager + notification channels** (email/Slack/PagerDuty) — rules exist, nobody is paged | Turns monitoring into on-call. | S |
| 14 | **Health/readiness endpoints** on `cmd/acs` (catch-all `/` swallows everything) and `cmd/api`; startup config validation summary | Load balancers, systemd, k8s probes. | S |
| 15 | **Dockerfiles + compose for the Go services, CI (vet/test/tsc/lint), IaC for EC2** | Repeatable deploys; today `nohup` + PID files. | M |
| 16 | **Multi-instance readiness**: distributed rate limiter (Redis), advisory-locked migrations, leader election for the connreq/scheduler workers, sticky or shared CWMP sessions | Required for HA / warm standby in the hardening checklist. | L |
| 17 | **Backup/restore runbook + scripts** for Postgres and firmware storage | Not documented anywhere. | S |
| 18 | **OpenTelemetry tracing + request IDs** correlating REST → job → CWMP session → TransferComplete | Debugging async job paths across three processes. | M |
| 19 | **OpenAPI spec** (generated) + typed frontend client from it | 119 routes hand-mirrored in `client.ts`; drift is inevitable. | M |
| 20 | **Load/soak test harness** (mock-CPE × N) against the 18k-device target in the build plan | No performance evidence exists. | M |
| 21 | Secrets-manager integration (AWS Secrets Manager/Vault) instead of `~/.acs-secrets.env` | Hardening checklist item. | S |

### Security / operator access
| # | Capability | Why | Effort |
|---|---|---|---|
| 22 | **OIDC/SSO for operators** (design v3 §11.5) + MFA; refresh tokens / revocation (JWT is 8 h HS256, no refresh, no revocation list) | Enterprise NOC requirement; also closes the `?token=` problem via short-lived tickets. | M |
| 23 | Tenant scoping enforced at the repository/middleware layer (today each handler must remember `Scoped: true`) | One forgotten flag leaks the fleet across ISPs. | S–M |
| 24 | Login brute-force protection keyed by IP+username (currently keyed by operator only after auth) | Public endpoint. | S |

### Frontend / UX
| # | Capability | Why | Effort |
|---|---|---|---|
| ★25 | **Router + deep links** (device/job/rollout URLs), browser back, bookmarkable filters | NOC hand-offs ("look at this device") need URLs. | M |
| 26 | **Server-state cache (TanStack Query) + WebSocket/SSE push** instead of 6 s polling on every screen | Scale + freshness; also enables job-progress live views. | M |
| 27 | **Alarm/notification center** in the UI (device offline, rollout blocked, policy drift, webhook failures) | Nothing surfaces problems proactively today. | M |
| 28 | **Map view** for device `location` (free-text now; add lat/lng) — the user's GIS platform could be a consumer | Field ops. | S–M |
| 29 | Global search (serial/OUI/tag/customer), saved filters, column chooser, CSV export on every table | Daily NOC ergonomics. | S each |
| 30 | Bulk-action outcome tracking (a "batch" entity with per-device progress) — today the toast is the only record | Fleet Control at scale. | S–M |
| 31 | Error boundaries, code-splitting, a11y pass, Playwright e2e | Robustness. | S–M |

### BSS
| # | Capability | Why | Effort |
|---|---|---|---|
| 32 | `SUSPEND`/`ACTIVATE` via a per-vendor walled-garden parameter (needs the vendor answer first), webhook delivery finished/documented, outbox for true idempotency, `/bss/v1/*` rate limit reconciled with the guide | Completes the integration contract the guide promises. | M |
| 33 | Multi-device-per-account mapping (today one primary device per account) | Real subscribers have more than one CPE. | S |

## 6. Suggested starting order for the next agent

1. **Stabilize what exists (1–2 days):** items §4.1, §4.5, §4.6 (fail-closed auth, lease reaper, graceful shutdown), offline/unreachable reaper (§5b #1), retention pruning (§5b #12), `go mod tidy`, pool settings, migration advisory lock, `?token=` restricted to the two WS/iframe routes.
2. **Build the mock CPE + `cmd/acs` integration tests**, then a CI workflow. Everything after this becomes safely refactorable.
3. **Get the real-device answer.** Re-run `cmd/probe` (or the gateway with `ACS_DEBUG`) against the ZOWEE unit on EC2, commit `docs/device-compatibility-matrix.md`, and confirm whether onboarding + a `SET_PARAMETER` round-trip actually completes today. Resolve P1/P2 for at least that vendor.
4. **Annex G UDP Connection Request** (or a WireGuard tunnel via the existing `vpn` package) — the single feature that turns "acts on next periodic Inform" into "acts now" for a CGNAT fleet. Needs a packet capture from a real STUN-enabled CPE.
5. Then frontend structural work (router, query cache, lazy loading, tests), then production hardening checklist, then the BSS `SUSPEND`/webhook items.

## 7. Doc map

| Read this for… | File |
|---|---|
| Protocol/architecture rationale (spec corrections table) | `../tr069-acs-architecture.md`, `tr069-acs-application-design-v3.md` |
| Phase-by-phase log of what was built and why (long) | `tr069-acs-build-plan.md` §4–§8 |
| Component/port table + env-var reference + first-device test | `deployment-testing-onboarding-guide.md` |
| EC2 deploy runbook + hardening checklist + CPE troubleshooting | `EC2-DEPLOYMENT-GUIDE.md` |
| BSS/CRM external contract (with implementation-status table) | `bss-integration-guide.md` |
| Fleet-specific open questions P1–P5 | `../tr069-acs-prerequisites.md` |
| Undocumented later features | migration doc-comments `backend/internal/store/migrations/0027–0039_*.sql` and package doc-comments in `internal/{tenancy,vpn,cliaccess,mailer,dashboard,stun}` |
| How to run locally | `scripts/start.sh` (Linux/macOS only); env vars in `scripts/gen-env.sh`; frontend `npm run dev` with `VITE_API_BASE_URL` |
