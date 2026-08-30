# ACS codebase audit

**Audit date:** 2026-08-28  
**Scope:** `backend/`, `frontend/`, `infra/`, `scripts/`, API contracts, and deployment documentation  
**Method:** Read-only source review plus local build, test, vet, lint, coverage, and dependency checks. Existing uncommitted changes were preserved.

## Executive summary

The project is a substantial, coherent TR-069 ACS implementation rather than a prototype: three Go services, a React operator console, 39 PostgreSQL migrations, protocol and vendor adapters, firmware/rollout support, BSS integration, tenancy/RBAC, observability, and container assets are present. The current branch builds and its available unit tests pass.

It is **not production-ready for multi-tenant or internet-facing use**. The most important blockers are:

1. operator authentication and credential encryption fail open when secrets are absent;
2. tenancy enforcement is inconsistent and sometimes explicitly fail-open;
3. public upload/firmware endpoints use bearer-by-identifier semantics without expiry or request authentication, and upload size is unbounded;
4. the device web-GUI proxy is an SSRF primitive and the SSH bridge skips host verification;
5. durable jobs have no lease expiry/recovery, while API workers lack graceful shutdown;
6. CWMP Digest nonces are not server-validated, enabling replay;
7. CI, end-to-end testing, realistic CPE simulation, migration concurrency controls, retention, backup/restore evidence, and load evidence are absent.

The recommended next tranche is to close the authentication/tenancy/public-transfer boundary first, then add job recovery and lifecycle hardening, then establish CI plus a mock-CPE integration suite before structural refactoring.

## Repository snapshot

- Backend: 123 Go files, about 18,118 lines; Go services are `cmd/acs`, `cmd/api`, and `cmd/bssadapter`.
- Frontend: 42 TypeScript/TSX files, about 6,714 lines; React 19, TypeScript 6, Vite 8.
- Persistence: PostgreSQL with 39 forward-only embedded migrations; local filesystem for firmware and CPE uploads.
- Operations: Dockerfiles and an opt-in Compose application profile, Prometheus, Grafana, and shell-based EC2 workflows.
- History: only five commits; most implementation arrived in one large import, limiting useful blame/bisect history.
- Worktree: materially dirty before the audit, including source edits, tests, Dockerfiles, OpenAPI files, and tracked `.exe` binaries.

## Validation results

| Check | Result | Interpretation |
|---|---|---|
| `go test ./...` | Pass | All currently runnable Go unit tests pass; five commands and nine internal packages still report no tests. |
| `go vet ./...` | Pass | No standard vet findings. |
| `go test -cover ./...` | Pass | Strong in a few protocol/security utilities, but 0% in all service commands and core repositories such as devices, sessions, scheduler, policy, tenancy, templates, and parameters. |
| `go test -race ./...` | Not run | Local Go has CGO disabled; `-race` requires CGO. |
| `npm test -- --run` | Pass | 4 files, 32 tests. This is useful but narrow component/utility coverage. |
| `npm run lint` | Pass with 5 warnings | Two missing-hook-dependency warnings, two unused-expression warnings, and one fast-refresh warning. |
| `npm run build` | Pass with warning | Production JS is one 752.57 kB chunk (196.65 kB gzip), above Vite's 500 kB warning threshold. |
| `npm audit --omit=dev` | Pass | No production dependency vulnerability reported. |
| `npm audit` | Fail | One high-severity advisory in development dependency `nanoid <3.3.18`. |
| Go vulnerability scan | Not available | `govulncheck`, `gosec`, and `golangci-lint` are not installed; no vulnerability conclusion can be made. |
| Container/live DB/CPE/browser/load tests | Not run | No claim is made about live Postgres migrations, real CPE interoperability, browser E2E, Compose health, HA, or target-scale performance. |

## Prioritized findings

### P0 — release blockers

#### P0.1 Authentication and encryption fail open

`backend/cmd/api/main.go:81-98` only warns when `ACS_JWT_SIGNING_SECRET` or `ACS_CREDENTIAL_ENCRYPTION_KEY` is absent. `backend/cmd/api/auth_handlers.go:64-69` then bypasses authentication entirely, and role/permission middleware follows the same behavior. Device, CLI, web-GUI, and VPN credentials may consequently be stored in plaintext.

**Impact:** a deployment/configuration error exposes the entire operator API and sensitive credentials. A warning is not a security boundary.

**Recommendation:** introduce an explicit `ACS_ENV=development` or `ACS_INSECURE_DEV_MODE=true`; otherwise require JWT, internal-service, encryption, bootstrap, and CPE-auth secrets at startup. Reject placeholder values such as `change-me`, enforce minimum entropy/length, and expose a redacted configuration validation summary.

#### P0.2 Multi-tenant authorization is incomplete and fails open

`backend/cmd/api/main.go:553-571` returns unrestricted access when operator lookup or scope resolution fails. `listDevicesSummary` (`:602-614`) and `listMatchingDeviceIDs` (`:623-636`) call unscoped repository methods. Many device-specific handlers call `h.devices.Get` but never verify `deviceInScope`; examples include firmware, upload, credentials, console, web-GUI, VPN, object/RPC, scheduling, and tenancy actions. Dashboard code also documents fleet-wide rollout/job aggregates for scoped users.

**Impact:** a valid scoped operator can discover identifiers, view cross-tenant aggregates, and potentially operate on another customer's device by supplying its UUID. Transient DB errors can turn restricted access into unrestricted access.

**Recommendation:** make scope enforcement mandatory and centralized. Put an authorization guard in middleware/repository methods that accepts the authenticated principal and target device; return an error on scope-resolution failure. Ensure every list, aggregate, export, job, group, rollout, upload, and mutation joins through the authorized customer set. Add negative tests for every route category using two tenants and unassigned devices.

#### P0.3 Public transfer endpoints are weakly authorized; upload size is unbounded

`backend/cmd/api/auth_handlers.go:105-117` makes firmware download, CPE upload receipt, and metrics public. `backend/cmd/api/upload_handlers.go:95-121` treats knowledge of a persistent upload UUID as authorization and streams an unlimited body to disk. There is no expiry, one-time signed token, expected-size ceiling, content validation, or status gate preventing overwrite/replay. Firmware URLs are similarly permanent identifiers.

**Impact:** disk exhaustion, upload-slot overwrite, unintended firmware disclosure, and indefinite replay if an identifier leaks through logs, CPE configuration, or support tooling.

**Recommendation:** issue high-entropy, time-limited, purpose-bound transfer tokens; store only hashes; require `PENDING` and atomically transition once; reject replay; enforce global and per-file byte limits with `MaxBytesReader`/limited readers; validate expected device, content type, and optional checksum; rate-limit by IP and token. Put metrics on a private listener or protect it at the network layer.

#### P0.4 Web-GUI proxy enables SSRF and SSH skips host authenticity

`backend/cmd/api/webgui_handlers.go:45-66` accepts an operator-supplied absolute URL, and `:110-128` creates a reverse proxy to it with no scheme allowlist, resolved-IP policy, redirect policy, transport timeout, or private/metadata-address denylist. It can reach services available to the ACS host. `backend/internal/cliaccess/bridge.go:28-39` uses `ssh.InsecureIgnoreHostKey()`.

**Impact:** operators with the relevant permission—or an attacker who obtains one account—can probe internal services/cloud metadata, while SSH sessions are vulnerable to machine-in-the-middle attacks.

**Recommendation:** do not accept arbitrary URLs. Derive targets from device inventory/VPN assignments; allow only `http/https`, approved ports, and device address ranges; resolve and validate every connection and redirect; deny loopback, link-local, multicast, metadata, and infrastructure networks; add strict dial/response/TLS timeouts. Store per-device SSH fingerprints/known-host keys and require verification, with an audited enrollment/rotation path.

#### P0.5 CWMP Digest authentication is replayable

`backend/internal/auth/digest.go` generates a challenge nonce but does not authenticate its origin/expiry on receipt and does not track nonce-count values. The fleet also defaults to shared CPE credentials.

**Impact:** a captured Authorization header can be replayed, and compromise of one shared credential affects the fleet.

**Recommendation:** use HMAC-authenticated timestamped nonces, reject expired/foreign nonces, validate realm/URI/qop, track `(device, nonce, cnonce, nc)` within a bounded replay cache, and move to unique per-device credentials or mTLS. Add replay, expiry, URI mismatch, and nonce-tamper tests.

### P1 — high priority reliability and production hardening

#### P1.1 Jobs can be stranded permanently

`backend/internal/jobs/job.go:308-389` moves work to `RPC_SENT` or `IN_PROGRESS` but records no lease owner/deadline. There is no stale-lease reaper. A crash between lease and completion leaves work permanently non-runnable.

**Recommendation:** add `lease_owner`, `leased_until`, attempt ceilings, idempotency rules, and a reaper that safely requeues or dead-letters expired work. Use heartbeat extension for long operations and metrics/alerts for stale/dead-letter jobs.

#### P1.2 API process lifecycle and HTTP timeouts are incomplete

`backend/cmd/api/main.go:59,373,382-391` uses a background context for workers and directly blocks in `ListenAndServe`; it does not use signal cancellation or `Server.Shutdown`. The API server only sets `ReadHeaderTimeout`, not request-body/read, write, or idle controls. BSS adapter lifecycle is better, and ACS has graceful shutdown, so there is an internal pattern to reuse.

**Recommendation:** use `signal.NotifyContext`, cancel and wait for workers, gracefully shut down HTTP, reject new leases during drain, and set endpoint-appropriate timeouts. Streaming/WebSocket paths should use dedicated controls instead of forcing weak global defaults.

#### P1.3 Database startup and pool controls are incomplete

`backend/internal/store/postgres.go:42-98` creates a migration table and applies migrations transactionally, but has no PostgreSQL advisory lock. No connection-pool sizing, lifetime, idle-time, or wait metrics are configured.

**Impact:** concurrent replicas can race migrations; unconstrained/default pool behavior is not tied to PostgreSQL capacity or workload.

**Recommendation:** acquire a stable advisory lock around migration discovery/application; validate checksums to detect edited migrations; configure and expose pool statistics; set connection lifetime/idle limits; add startup/readiness distinction and migration integration tests against a clean and upgraded database.

#### P1.4 Query tokens expose long-lived credentials in logs and referrers

`backend/cmd/api/auth_handlers.go:148-163` accepts `?token=` for every authenticated route, not only the browser-constrained WebSocket/iframe routes. The internal service token is accepted through the same function and grants synthetic superadmin access.

**Recommendation:** remove general query-token support. Exchange the normal bearer token for a single-use, short-lived, audience/path-bound WebSocket or proxy ticket. Restrict the internal service identity to a narrow route/permission set and prefer mTLS or separately signed service JWTs with rotation.

#### P1.5 CORS and container defaults are development-grade

The API defaults `ACS_API_CORS_ORIGIN` to `*` (`backend/cmd/api/main.go:380-436`). Compose publishes PostgreSQL, API, ACS, BSS adapter, Prometheus, and Grafana ports broadly; PostgreSQL is `acs/acs`, Grafana enables anonymous Viewer, and the admin password is `admin`. The example application secrets are literal `change-me` values.

**Recommendation:** bind management/data services to localhost or an internal network, remove host publishing where not needed, disable anonymous Grafana, require Docker secrets or a secret manager, add TLS termination/security headers, and split explicit development and production Compose overlays. Add container health checks for all application services.

#### P1.6 No automated delivery gate

There is no committed CI workflow enforcing Go tests/vet, frontend test/lint/build, OpenAPI drift, dependency review, secret scanning, container build/scanning, migration tests, or formatting.

**Recommendation:** add a required pull-request pipeline with pinned actions and least permissions: `gofmt` check, `go test`, `go vet`, `govulncheck`, frontend tests/lint/build/audit policy, OpenAPI validation/drift, clean-Postgres migration test, Docker builds, SBOM/image scan, and gitleaks. Add branch protection and dependency update automation.

### P2 — correctness, performance, and quality gaps

#### P2.1 Coverage is concentrated away from the riskiest orchestration paths

All service commands (`cmd/acs`, `cmd/api`, `cmd/bssadapter`) report 0% coverage. Devices, sessions, scheduler, policy, tenancy, templates, parameters, dashboard, and observability also report 0%. Jobs (13.2%), rollout (14.5%), BSS (20.2%), credentials (22%), uploads (23.1%), and VPN (22.4%) remain thin. Frontend tests cover only four small units/components.

**Recommendation:** first add route-level authorization tests and ACS session-state integration tests, then job recovery, migrations, BSS idempotency/webhooks, rollout state transitions, and browser smoke flows. Commit a deterministic mock CPE supporting Inform/RPC/fault/transfer/reconnect scenarios. Use coverage thresholds by critical package rather than one easily gamed global number.

#### P2.2 Tenancy queries and aggregates need consistent SQL-level scope

Scope expansion currently loads region customers and passes ID slices through selected methods. This is easy to omit, creates large `ANY(...)` arguments at scale, and cannot protect ad-hoc repository methods.

**Recommendation:** centralize scope predicates using joins/CTEs or PostgreSQL row-level security with transaction-local principal context. Prefer deny-by-default APIs. Index every foreign key and high-volume status/time access path, then validate with representative `EXPLAIN (ANALYZE, BUFFERS)` data.

#### P2.3 Storage growth is unbounded and local-disk storage prevents HA

No retention/pruning workflow exists for sessions, jobs, audit events, parameter history, webhook deliveries, or stale upload reservations. Firmware and uploads live on local disk.

**Recommendation:** define legal/operational retention policies, partition the highest-volume time-series tables, prune/archive in bounded batches, expose storage-age/cardinality metrics, and move binaries to S3-compatible object storage with encryption, integrity metadata, lifecycle rules, and malware/content scanning where appropriate.

#### P2.4 Frontend bundle and state model need restructuring

The app produces a single 752.57 kB JavaScript chunk. It has no URL router/deep links, no error boundary, and each screen manages server state manually. Large files include `DeviceDetail.tsx` (614 lines), `api/client.ts` (498), `BSSIntegration.tsx` (472), and `FleetControl.tsx` (410). Current lint warnings identify real hook-dependency and dead-expression risks.

**Recommendation:** add route-based `React.lazy` splitting, a router, a server-state/query library, shared request/error/abort handling, and error boundaries. Break screens into feature modules and hooks. Fix all lint warnings and fail CI on warnings. Add Playwright flows for login, tenant denial, device drill-down, job creation/status, upload/download, and dangerous-action confirmation.

#### P2.5 API contracts exist but drift controls do not

`backend/openapi.yaml` and `backend/openapi-bssadapter.yaml` now exist, but comments still describe insecure defaults and there is no automated comparison against route registration or typed frontend generation.

**Recommendation:** choose spec-first or code-generated ownership, validate both specs in CI, generate the frontend types/client, and add contract tests for status codes, pagination, errors, auth, and tenancy. Remove hand-maintained duplication from `frontend/src/api/types.ts` and `client.ts` over time.

#### P2.6 Dependency and supply-chain hygiene needs a formal policy

Full `npm audit` reports one high-severity `nanoid` development advisory. Go security scanners are absent. Container tags are mutable and images/actions are not digest-pinned. Built Windows executables are staged in the repository.

**Recommendation:** update the affected lockfile dependency, run `govulncheck` in CI, produce SBOMs, scan images, pin deploy artifacts by digest, sign releases, and remove/ignore generated `.exe` binaries unless they are intentional release artifacts stored outside Git. Add license review for device/vendor assets and dependencies.

### P3 — maintainability and operational maturity

#### P3.1 Large composition files mix wiring, routing, policy, and behavior

`backend/cmd/api/main.go` is 1,411 lines, `cmd/acs/main.go` 1,250, `devices/repository.go` 799, `jobs/job.go` 646, and `cmd/bssadapter/main.go` 547.

**Recommendation:** keep `main` limited to validated configuration and dependency wiring. Extract API server/router construction, auth policy, tenant guard, ACS session engine, worker supervisors, and repositories by bounded concern. Refactor only after integration characterization tests exist.

#### P3.2 Health, readiness, alert delivery, backup, and recovery evidence are incomplete

Prometheus rules and Grafana dashboards exist, but no Alertmanager delivery path is present. There is no demonstrated readiness contract, backup/restore test, disaster-recovery objective, rolling-upgrade test, or failover exercise.

**Recommendation:** add separate liveness/readiness endpoints, Alertmanager receivers, tested database/object-store backup and restore scripts, documented RPO/RTO, and operational drills. Dashboards should distinguish configuration errors, DB saturation, stale leases, CPE-auth failures, Inform lag, transfer failures, and webhook backlog.

#### P3.3 Protocol compatibility and scale claims lack reproducible evidence

Unit-level CWMP coverage is relatively good, but there is no committed multi-device emulator, compatibility matrix generated from real devices, soak test, or captured evidence against the target fleet size. Annex G/XMPP connection requests and TR-098 writes remain notable functional gaps for CGNAT/legacy fleets.

**Recommendation:** commit a mock-CPE harness with vendor profiles, protocol transcript fixtures, malformed XML/auth replay cases, delayed/duplicate events, and thousands-of-device load mode. Record tested firmware/model/root/CWMP-version behavior in a compatibility matrix and make unsupported capability states explicit in the UI.

#### P3.4 Documentation and repository hygiene are inconsistent

The root has no concise current README, while `frontend/README.md` remains generic. Several documents contain mojibake (`Â`, `â`) and older statements are now stale (for example, claiming there are no Dockerfiles, tests, or OpenAPI specs). Generated binaries are staged and the repository has minimal history.

**Recommendation:** create a root README with architecture, supported/unsupported matrix, secure quick start, checks, and doc index; fix encoding; mark historical design documents; update handoff claims; add contributor/release/security policies; and keep generated artifacts out of source control.

## Checks that exist today

- CWMP XML size limits and XML-hardening tests.
- Digest and JWT unit tests, bcrypt operator passwords, AES-GCM option for stored credentials.
- Role and permission middleware, audit recording, per-operator/IP in-process rate limiting.
- Parameterized SQL in reviewed repositories and transactional migrations.
- `FOR UPDATE SKIP LOCKED` queue leasing.
- Firmware SHA-256 calculation and upload filename sanitization.
- Go HTTP header timeouts in services, fuller timeouts and graceful shutdown in the ACS service.
- Prometheus metrics, Grafana provisioning, and alert rules.
- Multi-stage/distroless non-root Go images and a multi-stage frontend image.
- Frontend type-check, lint, build, and a small Vitest suite.

These are useful controls, but several are optional, local-process only, inconsistently applied, or not enforced by CI.

## Missing or insufficient checks

- Fail-closed production configuration validation and placeholder-secret rejection.
- Central deny-by-default tenant authorization for every read and mutation.
- Login-specific brute-force controls keyed by normalized username plus trusted client IP.
- JWT revocation/refresh/rotation, MFA/SSO, narrow machine identities, and one-time browser tickets.
- Authenticated, bounded, expiring CPE transfer URLs.
- SSRF controls and SSH host verification.
- Digest nonce authenticity, expiry, and replay detection.
- Request-body limits on JSON/multipart/upload routes consistently.
- Job visibility timeout, recovery, dead-lettering, and worker leadership.
- Migration locking/checksums and production database pool tuning.
- Retention, partitioning, archival, backup restore, and object-store HA.
- CSP/HSTS/referrer/frame/content-type security headers at the reverse proxy.
- CSRF is not currently the primary issue because API auth uses bearer headers, but it must be revisited if cookies are introduced.
- SAST, Go vulnerability scanning, SBOM, image scanning/signing, secret scanning, and license gates.
- Integration, browser E2E, real-device compatibility, migration-upgrade, load/soak, chaos/failover, and restore tests.
- Accessibility checks (keyboard/focus semantics, labels, contrast, screen-reader smoke tests).
- OpenAPI drift and generated-client checks.

## Refactoring roadmap

### Tranche 1: security boundary (release blocker)

1. Add strict production config validation and remove all implicit auth/encryption disablement.
2. Introduce a central `AuthorizeDevice(principal, deviceID)`/scoped repository boundary; fix fail-open errors; cover every route with two-tenant negative tests.
3. Replace permanent public transfer identifiers and query JWTs with expiring one-time tickets; add upload caps and atomic state transitions.
4. Constrain/feature-flag the web-GUI proxy and require SSH host-key verification.
5. Implement Digest nonce verification/replay protection and per-device credentials.

### Tranche 2: durability and lifecycle

1. Add job lease deadlines, recovery, dead-letter behavior, and observability.
2. Add graceful shutdown and worker supervision to API/BSS services and complete HTTP timeout policies.
3. Lock/checksum migrations, configure DB pools, and add clean/upgrade integration tests.
4. Add retention and object-store abstraction; document/test backup and restore.

### Tranche 3: delivery confidence

1. Add CI security/build/test/contract/container gates.
2. Build the mock CPE and ACS/API/BSS integration suite.
3. Add browser E2E, accessibility, real-device compatibility, and load/soak evidence.
4. Establish release artifacts, SBOM/signing, environment promotion, and rollback procedures.

### Tranche 4: structural optimization

1. Extract server construction and domain services from oversized `main.go` files.
2. Centralize config parsing, validation, middleware, error responses, and observability correlation IDs.
3. Split repositories and introduce purpose-specific interfaces around transactions.
4. Add router/query cache/lazy loading in the frontend; generate the client from OpenAPI.
5. Optimize SQL from measured production-like plans rather than speculative rewrites.

## Suggested acceptance gates for production

- No P0 findings open; tenant-denial suite passes for every endpoint category.
- All required secrets validated; insecure mode cannot be enabled accidentally.
- CI is required and green, with no unexplained lint/vet/test/security warnings.
- Clean and previous-version migration tests pass under concurrent startup.
- Mock-CPE end-to-end scenarios cover successful, failed, duplicated, replayed, delayed, and interrupted sessions/transfers.
- Load/soak results meet explicit Inform rate, API latency, queue age, DB utilization, and recovery targets at expected fleet size.
- Backup restore and rollback have been exercised and timed against documented RPO/RTO.
- At least one supported device/firmware per vendor has a recorded compatibility result.
- External exposure has TLS, private management-plane boundaries, protected metrics, secure headers, alert delivery, and reviewed network rules.

## Audit limitations

This was a static/local audit, not a penetration test or production certification. No live database was mutated, no containers were started, and no real CPE, SMTP, BSS, VPN, SSH, web-GUI, Prometheus, or Grafana integration was exercised. The Go race detector and Go-specific vulnerability/SAST tools were unavailable. Dependency advisories and local source state reflect 2026-08-28 only.
 


## Product improvement appendix

This appendix turns the audit into an explicit product and engineering backlog. It remains subordinate to the priorities, safety constraints, and acceptance gates above.

### Ideal features to add

- Role-specific operational dashboards with queue aging, SLA risk, exception ownership, and drill-down audit history.
- Configurable workflow designer with versioned policies, separation-of-duties rules, escalation timers, and delegation.
- Durable notification centre, self-service reporting, and an integration hub for identity, ERP, documents, and webhooks.

### Consolidated gaps to close

- Close the P0 security boundary issues before enabling external or privileged users.
- Prove authentication, authorization, audit integrity, persistence, recovery, and failure behavior through full-stack tests.
- Define data ownership, retention, deletion, backup/restore, RPO/RTO, and CI-enforced quality/security gates.

### Code optimization, reduction, and improvement

- Split large routes into domain services, repositories, policy guards, and transport adapters.
- Replace repeated validation/authorization with shared typed schemas and declarative policies.
- Remove confirmed dead/compatibility paths, batch related queries, paginate collections, and profile hot endpoints.
