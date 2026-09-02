# ACS independent real-system audit

**Audit date:** 2026-09-02  
**Repository:** `C:\applications\ACS`  
**Reviewed revision:** `0cf5e5d` on `main`  
**Perspective:** production TR-069/CWMP ACS, ISP/NOC operations, fleet scale, device interoperability, security, persistence, and operator console

## Executive assessment

ACS is now a substantial Go/PostgreSQL plus React operator platform with a real CWMP gateway, REST API, BSS adapter, job queue, firmware transfers, tenancy/RBAC, connection-request handling, object storage support, retention, observability, CI, generated API contracts, Playwright/axe tests, and a committed mock-CPE integration harness.

The repository is materially beyond prototype quality, but it is not yet proven as a production ACS for a real multi-vendor fleet. The largest remaining risk is evidence, not basic scaffolding: the compatibility matrix has no real-device rows, the load table has no recorded run, and Annex G has not been validated against a real STUN-capable CPE. Two repository-verifiable gaps also deserve priority: device liveness is one-directional, and CWMP session cookies should be hardened explicitly.

## Validation performed

- Backend `go test ./...`: passed.
- Backend `go vet ./...`: passed.
- Frontend Vitest: 32 tests passed.
- Frontend Playwright: 6 tests passed.
- Frontend lint: passed.
- Frontend production build: passed with route-based code splitting.
- The backend DB-backed integration suite was not run locally because the required PostgreSQL test DSN was not configured; CI defines that job.
- The Go race suite was not run locally because the Windows environment lacks a working CGO compiler toolchain.

## Findings

### P0: Real-device and fleet-scale qualification is absent

**References:** [docs/COMPATIBILITY.md](docs/COMPATIBILITY.md), [backend/cmd/acs/session_integration_test.go](backend/cmd/acs/session_integration_test.go)

The mock-CPE suite covers vendor profiles, transfers, malformed messages, and load-mode code paths, but the compatibility matrix contains no real-device results and the load-evidence table has no recorded run. Annex G is implemented from specification text but explicitly unvalidated against real hardware.

**Why this blocks release:** a CWMP ACS can pass unit tests while failing on SOAP namespaces, authentication quirks, empty-body behavior, connection-request semantics, parameter roots, transfer callbacks, firmware timing, or vendor-specific data models.

**Required evidence:** at least one real device and firmware per supported vendor, bootstrap/periodic/value-change Inform traces, Get/SetParameterValues, diagnostics, firmware Download/TransferComplete, direct and NAT connection requests, reconnect behavior, and a repeatable load/soak result at the expected fleet rate.

### P1: Device liveness is only updated positively

**References:** [backend/internal/devices/repository.go](backend/internal/devices/repository.go), [backend/cmd/acs/workers.go](backend/cmd/acs/workers.go)

`UpsertFromInform` changes a device to `ONLINE` whenever an Inform arrives. The gateway polls status counts, but no visible repository worker transitions devices to `OFFLINE` or `UNREACHABLE` based on missed Informs or failed connection requests. The status values exist in aggregates and dashboards, but the state machine is not complete.

**Impact:** fleet health, `NoDevicesOnline`, reachability reporting, and operator actions can remain optimistic after a CPE disappears.

**Recommendation:** define per-device liveness policy using `PeriodicInformInterval`, tolerance windows, last connection-request outcomes, and provisioning state. Add a bounded reaper that transitions `ONLINE` to `OFFLINE`/`UNREACHABLE`, records the reason and transition time, emits audit/metrics events, and tests hysteresis to avoid flapping.

### P1: CWMP session cookie lacks explicit transport and cross-site policy

**Reference:** [backend/cmd/acs/session.go](backend/cmd/acs/session.go)

The `acs_session` cookie is `HttpOnly` but does not explicitly set `Secure` or `SameSite`. Production deployments should not rely only on a reverse proxy or browser defaults for a session identifier.

**Impact:** accidental transmission over cleartext misconfiguration and weaker cross-site request protections.

**Recommendation:** set `Secure: true` for TLS deployments, use an explicit `SameSite` policy appropriate to the CPE interaction, set a bounded lifetime if compatible with long CWMP sessions, and add integration assertions for cookie attributes. If HTTP and HTTPS must coexist in a lab, make the behavior an explicit development-only configuration.

### P1: TR-069 capability coverage is still incomplete for common fleets

**References:** [README.md](README.md), [docs/COMPATIBILITY.md](docs/COMPATIBILITY.md)

The current product explicitly does not implement XMPP connection requests or TR-098/IGD:1 writes. Other long-tail RPCs such as `GetOptions`, `SetVouchers`, `RequestDownload`, `AutonomousTransferComplete`, and `ChangeDUState` remain unsupported.

**Impact:** legacy CPEs may read successfully but fail configuration writes, and CGNAT fleets without Annex G support fall back to periodic Inform delays.

**Recommendation:** make unsupported capabilities explicit during device onboarding and in the UI. Prioritize TR-098 root-aware writes for legacy devices, then XMPP/USP only where the target fleet requires them. Do not advertise a capability as supported solely because its route exists.

### P1: Raw CWMP troubleshooting evidence is limited

The ACS stores parsed device/session/job state, parameter cache, and audit events, but a sanitized, durable per-session SOAP transcript is not a first-class operator artifact.

**Impact:** vendor interoperability failures are difficult to reproduce or escalate because the exact request/response, namespace, headers, and fault body may be unavailable after the live session.

**Recommendation:** add opt-in, access-controlled session transcripts with payload-size limits, secret/credential redaction, retention policy, correlation IDs, and download permissions. Keep raw capture disabled by default where data classification requires it.

### P2: Job lifecycle needs stronger business semantics around retries

The queue now has leases, reaping, attempt limits, and dead-letter outcomes. The remaining risk is command semantics: retry safety varies by operation. A repeated `SET_PARAMETER`, reboot, factory reset, firmware download, or object mutation does not have the same idempotency profile.

**Recommendation:** classify each job type as idempotent, conditionally idempotent, or non-repeatable; store idempotency keys and device-side confirmation rules; require explicit operator confirmation for destructive retry; and expose retry reason/attempt history in the UI.

### P2: Frontend operational state is still mostly polling and local state

The console now has routing, lazy loading, TanStack Query, error boundaries, and browser E2E coverage. However, high-value workflows such as long-running jobs, firmware rollouts, diagnostics, connection requests, and bulk actions still need stronger live progress, cancellation, and per-device outcome presentation than periodic refresh alone provides.

**Recommendation:** add server-sent events or WebSockets for job/rollout progress where operationally justified, preserve query invalidation as fallback, show batch-level progress and partial failure details, and make stale data timestamps prominent.

### P2: Accessibility is improved but not complete

Playwright/axe coverage passes the current configured assertions, including serious-violation checks for fleet/dashboard and login landmark/heading semantics. Broader accessibility qualification is still needed for every screen, keyboard-only workflows, focus management in dialogs, color-independent status meaning, screen-reader table actions, and high-contrast/high-DPI modes.

**Recommendation:** expand axe coverage to all major routes and add keyboard/focus regression flows for destructive actions, bulk selection, device detail panels, and firmware rollout confirmation.

### P2: Backend integration coverage is concentrated in selected routes

The committed mock-CPE and API integration tests are valuable, but many domain packages still have limited direct tests, and a local DB-backed integration run was not available in this audit environment.

**Recommendation:** require the PostgreSQL integration job in branch protection, publish test artifacts, add failure-injection cases for migrations/jobs/transfers, and include duplicate/delayed Inform, CPE reconnect, partial transfer, database outage, and worker restart scenarios.

### P2: Operational recovery evidence is incomplete

Backup/restore scripts, retention, readiness endpoints, advisory-locked migrations, object storage, alerts, and runbook procedures exist. The runbook still has no recorded restore drill, RPO/RTO measurement, real alert delivery test, rolling-upgrade exercise, or failover result.

**Recommendation:** run quarterly restore drills, record observed RTO/RPO, exercise a rolling upgrade with active CPE sessions, verify alert delivery, and test object-store/database recovery independently.

### P3: Deployment topology is still an operational decision, not a proven platform

Container images and CI build/scan paths exist, but production topology, ingress/TLS termination, secrets manager integration, HA leadership for workers, distributed rate limiting, and multi-instance CWMP session behavior need environment-specific proof.

**Recommendation:** document the supported production topology as deployable infrastructure, define single-instance versus HA guarantees, test leader/reaper behavior under replica loss, and establish release promotion/rollback artifacts.

## Real ACS capability checklist

### Device lifecycle and provisioning

- Present: pre-registration, first Inform upsert, model/OUI/serial identity, data-model root detection, auto-provisioning templates, parameter discovery, periodic/value-change event capture.
- Still needed: real-device compatibility results, vendor-specific parameter catalogs, robust liveness transitions, explicit unsupported-capability state, and pre-provisioning acceptance against actual serial/OUI data.

### CWMP session and RPC engine

- Present: namespace handling, CWMP ID echoing, Inform, one-RPC-in-flight dispatch, faults, TransferComplete correlation, delayed/duplicate transfer handling, job leases, and bounded request bodies.
- Still needed: broader real-CPE session evidence, complete legacy/adjacent RPC coverage where required, transcript capture, and command-specific retry/idempotency policy.

### Connection requests and NAT

- Present: direct HTTP connection requests, STUN status, Annex G datagram generation/sending, EventCode 6 confirmation, and periodic fallback.
- Still needed: real STUN/Annex G hardware validation, NAT timeout/port-change behavior, IPv6 coverage evidence, and clear UI indication when an action is deferred to periodic Inform.

### Firmware and file transfer

- Present: signed expiring transfer URLs, bounded uploads, checksum metadata, TransferComplete handling, object-store abstraction, rollout/canary state, and rollback image support.
- Still needed: real firmware/device qualification, resume/interrupted-transfer evidence, malware/content scanning policy, storage lifecycle proof, and verified rollback on hardware.

### Multi-tenancy and operator operations

- Present: region/customer scopes, centralized device guards, RBAC, audit logging, operator UI, reports, and bulk selection.
- Still needed: formal authorization matrix coverage for every sensitive operation in production CI, SSO/MFA integration where required, distributed rate limiting for HA, and operational review of support/admin break-glass access.

### Reliability and operations

- Present: readiness/liveness, graceful shutdown, stale-job recovery, retention worker, connection pool settings, migration lock/checksums, Prometheus/Alertmanager configuration, backup/restore scripts, and CI gates.
- Still needed: measured restore/failover/upgrade drills, fleet-scale load evidence, alert delivery confirmation, and real incident-response exercises.

## Recommended implementation order

1. Add device liveness transitions and tests.
2. Harden and test CWMP session cookie attributes.
3. Record real-device compatibility rows and Annex G results.
4. Run PostgreSQL-backed integration and load/soak tests under CI with published evidence.
5. Add access-controlled raw CWMP transcripts.
6. Define command-specific retry/idempotency semantics.
7. Complete TR-098/XMPP/other protocol features only according to the target fleet.
8. Perform restore, failover, rolling-upgrade, alert-delivery, and security operations drills.

## Release decision

**Repository status:** strong engineering baseline; current local tests and frontend/browser checks pass.  
**Production ACS status:** release-blocked until real-device compatibility, liveness correctness, Annex G behavior, fleet-scale load, restore/failover, and deployment security evidence are completed for the intended ISP fleet.
