# Codebase and platform assessment: 2026-11-09

An independent read of the repository at `dcc519b` (main), asking one question: what stands between this code and an effective production device-management system?

**Short answer.** The protocol core and the safety rails are strong and well tested. The platform around them is not yet proven: there is no evidence it scales, survives a failure, or can be deployed and upgraded repeatably, and no CPE model is fully qualified. Most of the work that remains is operational, not feature work.

## 1. Method and limits

What was done:

- Read the README, handoff, audits, gap analysis, runbook and compatibility matrix.
- Built and vetted the backend (`go build ./...`, `go vet ./...`, Go 1.26.10): both clean.
- Ran `go test -count=1 ./...` without a database: all 50 packages with tests pass; 12 have no tests.
- Checked GitHub Actions run history.
- Verified each finding below in the code. References are `file:line` at this commit.

What was not done:

- No database-backed test run. Those suites skip without a DSN, so the local pass covers unit tests only. CI does run them.
- No frontend build, lint or test run locally (CI covers them).
- No running stack, no load test, no real device.
- Items marked "not re-verified" are carried from earlier project documents without a fresh check.

## 2. Size and shape

| | |
|---|---|
| Backend | ~64k lines of Go plus ~34k lines of tests; four services (`cmd/acs`, `cmd/api`, `cmd/bssadapter`, `cmd/uspc`) and 48 `internal/` packages |
| Frontend | ~27k lines of TypeScript (9.5k generated), React 19 + Vite, 81 source files, 32 unit test files, one Playwright spec |
| Database | PostgreSQL 18, 94 forward-only migrations, advisory-locked and checksum-verified |
| Direct Go dependencies | 15 (pgx, Prometheus client, AWS S3 SDK, MQTT server, websocket, protobuf, excelize, gosnmp, `x/crypto`, `x/net`, `x/time`, uuid) |
| CI | Two workflows: `ci` (Go, frontend, migrations, containers, Trivy scan, USP interop against obuspa) and `field-rc` (boots all four services) |
| Documentation | 12 Markdown files in the repository root, 27 in `docs/`, plus integration, security, field-note and plan subfolders |

## 3. What is strong

These are worth protecting as the system grows.

- **Fail-closed configuration.** Missing or placeholder secrets are fatal at startup; a production profile refuses plaintext, Basic auth and wildcard listeners.
- **Device identity.** Constrained bootstrap identity, graduation to a unique per-device Digest credential, activation only after a bound Inform, Postgres-backed replay protection.
- **Write safety.** A protected-parameter guard (`internal/parameters/protected.go`) refuses writes that could cut a device off, on every path.
- **Job queue.** `FOR UPDATE SKIP LOCKED` leasing with expiry recovery; the scheduler and BSS order claimers use the same pattern, so they are safe across replicas.
- **Contracts.** Two OpenAPI specs with a Go test that fails on drift from registered routes; generated frontend types checked in CI; versioned BSS contract fixtures.
- **Northbound.** TMF638/639/640/641/642/656/688 with a durable event outbox. The gap analysis is right that this is ahead of what competitors document.
- **Supply chain.** Actions pinned by commit, pinned tool versions, distroless non-root backend images, SBOMs, Trivy with expiring exceptions, gitleaks, Dependabot.
- **Small dependency surface.** No web framework, no ORM.

## 4. Findings

Severity: **High** blocks production use at fleet scale; **Medium** should be fixed before a customer rollout; **Low** is hygiene.

### 4.1 Scale and availability

| # | Sev | Finding | Evidence |
|---|---|---|---|
| A1 | High | No load or soak evidence. No load harness is committed (mock-CPE code exists only inside tests), and no run is recorded. | `docs/COMPATIBILITY.md`; search of `backend/`, `scripts/`, `ci/` |
| A2 | High | No restore drill. The RTO table has one row and it says none is recorded. | `docs/OPERATIONS-RUNBOOK.md:189-191` |
| A3 | High | Connection-request worker leases one job per one-second tick. A "do this now" action across 10,000 devices takes close to three hours per API replica. | `backend/cmd/api/connreq_worker.go:24,122-129` |
| A4 | Medium | Rate limiting is in-memory per process, so every limit (login, per-device, per-IP, BSS) multiplies by the replica count. The package comment says so. | `backend/internal/ratelimit/limiter.go:1-8` |
| A5 | Medium | Multi-replica operation is designed for in places (job leases, Digest replay, scheduler) but never exercised: no test or runbook covers two CWMP gateways, failover or a rolling upgrade. | No such test or procedure found |
| A6 | Medium | Device list paging sorts on `last_inform_at` alone with `LIMIT/OFFSET`. Rows that share a timestamp or change between pages are skipped or duplicated, and offset paging degrades on large fleets. | `backend/internal/devices/repository.go:272-275` |
| A7 | Medium | Only `tmf_events` is partitioned. Parameter history, jobs, sessions and audit rows are pruned by batched `DELETE`, which bloats at fleet scale. | `backend/internal/store/migrations/0090_event_stream.sql`; `backend/internal/retention/retention.go:143` |
| A8 | Low | Webhook delivery: order and service-order claiming use `SKIP LOCKED`, but no row claim was found on the webhook delivery path, consistent with the earlier report of double-sends across replicas. Not traced end to end. | `backend/internal/bss/order.go:263`; `backend/cmd/bssadapter/webhook_worker.go:172` |

### 4.2 Correctness

| # | Sev | Finding | Evidence |
|---|---|---|---|
| B1 | High | `ExtendLease` is defined and never called. A long USP Operate or any job that outlives its 15-minute session lease is recovered as expired while still running. | `backend/internal/jobs/lease.go:21-23,186`; no callers |
| B2 | Medium | A transfer that never reports back waits a fixed 24 hours before failing. This stalls and skews firmware rollouts, whose failure-rate gate depends on timely outcomes. | `backend/internal/jobs/lease.go:23` |
| B3 | Medium | Online/offline uses fixed defaults of 5 and 90 minutes, not each device's own periodic inform interval. Devices on 300 to 3600 second intervals flap. | `backend/internal/devices/repository.go:148-154` |
| B4 | Medium | Policy enforcement writes every value as `xsd:string`. Devices that check types reject boolean and integer parameters. | `backend/cmd/acs/enforce.go:56` |
| B5 | Medium | Firmware rollouts have a model filter, but it is optional, and nothing ties an image's declared vendor and model to the target device. Post-upgrade version confirmation: not re-verified. | `backend/internal/rollout/rollout.go:58,113`; `backend/internal/firmware/firmware.go:32` |

### 4.3 Security

| # | Sev | Finding | Evidence |
|---|---|---|---|
| C1 | High | Operator login is username and password with an 8-hour JWT. No OIDC, SAML or TOTP code exists. Operator security reviews normally require SSO and MFA. | `backend/cmd/api/auth_handlers.go:32`; search of `backend/` |
| C2 | Medium | The console keeps the JWT in `localStorage`, where any script-injection bug can read it. | `frontend/src/auth/tokenStore.ts:29-45` |
| C3 | Medium | The device web-GUI proxy sets no Content-Security-Policy. No such header is set anywhere in the Go code. | search of `backend/` |
| C4 | Medium | The login limiter is the in-memory limiter of A4, so lockout weakens with replicas and resets on restart. | `backend/cmd/api/auth_handlers.go:45` |
| C5 | Low | The frontend image has no `USER` directive, so nginx starts as root. Base images are pinned by tag, not digest. | `frontend/Dockerfile`; `infra/docker-compose.yml` |
| C6 | — | Carried from the gap analysis, not re-verified: bootstrap trust-on-first-use (S2), BSS clients that are not customer-bound (S3/S5), mTLS certificates with an empty CommonName (S8), no default outbound allowlist in production (S9). `netguard` exists and blocks the always-forbidden address classes. | `docs/GAP-ANALYSIS-2026-10-30.md` §2 |

### 4.4 Deployment and operations

| # | Sev | Finding | Evidence |
|---|---|---|---|
| D1 | High | Production is shell scripts on a single host with one Postgres. The repository holds no systemd units, infrastructure-as-code or orchestration manifests, and no release versioning or rollback procedure for the binaries. | `scripts/start-production.sh`; file search |
| D2 | Medium | Backup is `pg_dump` plus a copy of local firmware and upload directories. No point-in-time recovery, and local-disk storage ties the API to one host unless object storage is configured. | `scripts/backup.sh` |
| D3 | Medium | No distributed tracing. Metrics and structured logs exist, but a slow CWMP session cannot be followed across gateway, API and adapter. | no OpenTelemetry dependency in `backend/go.mod` |
| D4 | Low | CI is red on the grouped npm Dependabot PR, which bundles four major upgrades (TypeScript 7, Vitest 5, react-table 9, jsdom 31) with routine patches in one group. The failing job's log was not read, so the majors are the likely cause, not a confirmed one; either way the safe updates are stuck behind them. | [WillemKlopper87/ACS#98](https://github.com/WillemKlopper87/ACS/pull/98); `.github/dependabot.yml` |

### 4.5 Device coverage

| # | Sev | Finding | Evidence |
|---|---|---|---|
| E1 | High | No model is "supported" under the project's own rule. Nokia FastMile and Beacon passed the read-only stage only. Huawei, Teltonika and Zyxel have no real-device result. | `docs/COMPATIBILITY.md` vendor table |
| E2 | Medium | Several paths have never met real hardware: Annex G UDP connection request, XMPP connection request, mTLS, Download with TransferComplete, diagnostics, and USP on a physical CPE. | `docs/COMPATIBILITY.md` protocol table |
| E3 | Medium | The one field device sits behind carrier NAT with no STUN support, so instant actions depend on XMPP, which is implemented but unvalidated. | `docs/COMPATIBILITY.md`, Nokia row |

### 4.6 Code structure

| # | Sev | Finding | Evidence |
|---|---|---|---|
| F1 | Medium | `cmd/api` is 54 non-test files and ~16.6k lines in `package main`. Handlers cannot be imported, reused or tested without the whole binary. `cmd/bssadapter/main.go` (1,138 lines), `cmd/uspc/dispatcher.go` (1,186) and `cmd/acs/session.go` (1,014) have the same problem on a smaller scale. | line counts |
| F2 | Medium | 12 packages have no tests. | `go test` output |
| F3 | Low | The frontend has a hand-written `client.ts` (784 lines) and `types.ts` (524 lines) beside the generated types, which invites drift between the two. `DeviceDetail.tsx` is 1,148 lines. | line counts |
| F4 | Low | Browser coverage is one Playwright spec against a mocked API. Nothing drives the console against the real services, although `field-rc` already boots them. | `frontend/e2e/console.spec.ts` |

### 4.7 Documentation

| # | Sev | Finding | Evidence |
|---|---|---|---|
| G1 | Medium | About 40 status, audit, handoff and plan documents, with no single current one. Several contradict the code or each other. | see §5 |
| G2 | Low | `CODE_REVIEW.md` is cited by line number throughout the gap analysis but is not in the repository, so its open items (C2 to C11, S2 to S10, D1 to D4) cannot be looked up. | `docs/GAP-ANALYSIS-2026-10-30.md` §3 |
| G3 | Low | Open work is tracked in prose tables across documents, not as issues, so nothing shows what is assigned, in progress or closed. | — |

## 5. Stale statements found

- **"CI is dead (billing)"** in `docs/GAP-ANALYSIS-2026-10-30.md` §1, §2 and §6. CI is running: 547 runs of `ci`, the latest on 2026-11-08, green on the backend dependency PR.
- **"Expired `.trivyignore`"** (same document, §3). The file is now empty with a note that the entry expired and the fix shipped.
- **"A committed mock-CPE/load harness"** in the `HANDOFF.md` addendum. Mock-CPE code exists only inside test files; no standalone load tool was found.
- **`HANDOFF.md` body** describes Postgres 16, three commits and 83 tests. It is labelled historical, but it is still the first file a newcomer is told to read.

## 6. Recommendations

In the order I would do them.

### Now: small fixes with large effect

1. **Call `ExtendLease`** from the USP Operate path and any in-session job that can outlive its lease (B1).
2. **Batch the connection-request worker**: lease N jobs per tick and process them through a bounded pool (A3).
3. **Type policy and template writes** from the discovered parameter type instead of `xsd:string` (B4).
4. **Add a stable tiebreak** (`, id`) to device paging, then move the large lists to keyset paging (A6).
5. **Derive liveness from each device's inform interval** with a multiplier, falling back to the current defaults (B3).
6. **Split Dependabot groups** into minor/patch and major so routine updates merge (D4).

### Next: prove the platform

7. **Build a load harness** as a real command (a mock-CPE fleet generator), run it at 10k and 100k devices, and record the results. Fix what it finds before anything else in this section (A1).
8. **Run a restore drill** on a staging host and fill in the RTO table (A2).
9. **Run two replicas of every service in CI** behind a proxy, kill one mid-session, and assert nothing is lost (A5). Move the rate limiter to Postgres or accept and document per-replica limits (A4, C4).
10. **Choose a deployment target** and commit it: container images with version tags, a managed Postgres with point-in-time recovery, object storage for firmware and uploads, and a tested rolling upgrade with rollback (D1, D2).

### Then: security and qualification

11. **Add OIDC login** against Keycloak or Entra, which provides MFA, and keep local accounts as break-glass only (C1). Move the session to an HttpOnly cookie at the same time (C2).
12. **Close the carried isolation items**: strict onboarding mode, customer-bound BSS clients as the default, empty-CN certificates rejected, a required outbound allowlist in the production profile, CSP on the web-GUI proxy (C3, C6).
13. **Qualify one device end to end** (TLS, a reversible write, firmware download, XMPP connection request) before starting a second vendor. Make a passed qualification scenario the definition of "supported" in release notes (E1 to E3).
14. **Require a model match on firmware rollouts** and confirm the reported software version after upgrade; shorten the 24-hour transfer wait to a per-rollout setting (B2, B5).

### Ongoing: maintainability

15. **Move handlers out of `package main`** into `internal/` packages per domain, starting with whichever area changes most (F1). Add tests to the 12 untested packages as they are touched (F2).
16. **Generate the frontend client** from the OpenAPI spec and delete the hand-written types (F3). Add a short Playwright suite to `field-rc` against the real stack (F4).
17. **Collapse the documentation**: one living status page, the runbook, the compatibility matrix and the integration guide; archive the dated audits and handoffs; move open items into GitHub issues with the existing identifiers (G1 to G3).
18. **Add OpenTelemetry tracing** across the four services once there is a multi-replica deployment to observe (D3).

## 7. What would change this assessment

- A recorded load run at target fleet size and a completed restore drill would remove the two largest unknowns.
- A fully qualified device (all stages, not read-only) would turn the compatibility matrix from intent into evidence.
- Recovering `CODE_REVIEW.md` would let the carried items in §4.3 be confirmed or closed.
