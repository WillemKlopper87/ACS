# ACS — TR-069 Auto Configuration Server

A CWMP (TR-069) ACS for managing CPE fleets: device inventory and
parameter cache, jobs (SetParameterValues, GetParameterValues, Download,
Upload, Reboot, diagnostics, …), firmware rollouts, scheduled jobs,
policies, multi-tenant operator scoping, a BSS/CRM integration adapter,
and an operator console.

## Architecture

| Component | Path | Role |
|---|---|---|
| CWMP gateway | `backend/cmd/acs` | Terminates CPE sessions on `:7547` (Digest or mTLS), dispatches leased jobs, runs the stale-lease reaper, STUN on `:3478/udp`. |
| Operator API | `backend/cmd/api` | REST API on `:8080` (JWT auth, RBAC, tenancy scoping), connection-request and schedule workers, retention pruning, firmware/upload file serving. |
| BSS adapter | `backend/cmd/bssadapter` | `/bss/v1` on `:8090` for CRM/BSS systems (OAuth2 client credentials or shared token), webhooks. |
| Migrate tool | `backend/cmd/migrate` | Applies embedded migrations standalone (CI, pre-deploy). |
| Console | `frontend/` | React 19 + Vite operator UI, served by nginx in containers. |
| Persistence | PostgreSQL 18 | 41 forward-only embedded migrations, advisory-locked and checksum-verified at startup. Firmware and CPE uploads on local disk. |
| Observability | `infra/` | Prometheus, Alertmanager, Grafana provisioning, alert rules. |

Contracts: [backend/openapi.yaml](backend/openapi.yaml) (operator API — a
Go test fails if it drifts from the registered routes) and
[backend/openapi-bssadapter.yaml](backend/openapi-bssadapter.yaml).

## Secure quick start

Every service **fails closed**: a missing, placeholder (`change-me`), or
too-short secret is a fatal startup error, not a warning. The only
opt-out is `ACS_INSECURE_DEV_MODE=true`, for isolated local development.

Bare-metal (Postgres from compose, services via `go run`):

```bash
docker compose -f infra/docker-compose.yml up -d postgres
source scripts/gen-env.sh            # generates and persists real secrets in ~/.acs-secrets.env
scripts/start.sh
```

Fully containerized:

```bash
cp infra/.env.containerized.example infra/.env.containerized   # then replace every change-me
export GRAFANA_ADMIN_PASSWORD=...  ACS_POSTGRES_PASSWORD=...
docker compose -f infra/docker-compose.yml --profile containerized up -d --build
```

Only CWMP (`7547`) and STUN (`3478/udp`) are published on all
interfaces; Postgres, the API, the BSS adapter, the console, Prometheus,
Alertmanager and Grafana bind to `127.0.0.1` — put a TLS-terminating
reverse proxy in front of the API and console.

### Required secrets

| Variable | Service | Purpose |
|---|---|---|
| `ACS_JWT_SIGNING_SECRET` (≥32 B) | api | Operator JWTs, browser tickets, transfer-URL tokens |
| `ACS_CREDENTIAL_ENCRYPTION_KEY` (≥16 B) | api | Device/CLI/VPN credentials at rest |
| `ACS_INTERNAL_SERVICE_TOKEN` (≥32 B) | api, bssadapter | Adapter → API machine identity (narrow route allowlist) |
| `ACS_DIGEST_USERNAME` / `ACS_DIGEST_PASSWORD` (≥16 B) **or** `ACS_MTLS_CA_CERT` | acs | CPE authentication |
| `ACS_BSS_OAUTH_SIGNING_SECRET` (≥32 B) **or** `ACS_BSS_API_TOKEN` | bssadapter | Inbound BSS caller auth |

Recommended hardening variables: `ACS_DEVICE_NET_ALLOWED_CIDRS` (SSRF
allowlist for the web-GUI proxy and console bridge),
`ACS_API_CORS_ORIGIN` (defaults to `ACS_FRONTEND_BASE_URL`),
`ACS_UPLOAD_MAX_BYTES`, `ACS_DB_MAX_OPEN_CONNS`, `ACS_RETENTION_*_DAYS`,
`ACS_TLS_CERT`/`ACS_TLS_KEY`. The full variable reference is in
[deployment-testing-onboarding-guide.md](deployment-testing-onboarding-guide.md) §7.

## Checks

```bash
cd backend && gofmt -l . && go vet ./... && go test -race ./...
cd frontend && npm ci && npm run lint && npm run build && npm test -- --run && npm audit
```

CI ([.github/workflows/ci.yml](.github/workflows/ci.yml)) runs those plus
`govulncheck`, OpenAPI validation, a Postgres-backed migration job
(clean apply, idempotent re-run, three concurrent runners, service boot
with `/readyz` probes, placeholder-secret refusal), container builds with
a Trivy scan, and gitleaks. Make every job required in branch protection.

## Operations

- Health: `GET /healthz` (liveness) and `GET /readyz` (readiness, 503
  while Postgres is unreachable) on all three services; `GET /metrics`
  for Prometheus.
- Alerts: `infra/alert_rules.yml` → Alertmanager → the webhook in
  `ACS_ALERT_WEBHOOK_URL` (`infra/alertmanager.yml`).
- Backups: `scripts/backup.sh` (pg_dump + firmware + uploads, checksummed
  tarball) and `scripts/restore.sh`. Your RPO is the backup cadence;
  measure RTO by rehearsing a restore on staging.
- Migrations: applied automatically at service start under an advisory
  lock; `go run ./cmd/migrate` applies them ahead of a rolling deploy.
- Stranded jobs: leases expire (15 min session / 5 min worker); the
  reaper requeues or dead-letters them (`acs_jobs_recovered_total`,
  `acs_jobs_stale_leases`).
- SSH console: host keys are pinned per device on first connect; a
  legitimate key change requires deleting the `device_ssh_host_keys` row.

## Supported / unsupported

| Capability | Status |
|---|---|
| CWMP 1.0–1.4 sessions, Digest/Basic/mTLS CPE auth, replay-protected nonces | Supported |
| Connection Request (direct IPv4/IPv6, STUN/UDP), periodic-Inform fallback | Supported |
| Download/Upload with expiring signed transfer URLs, TransferComplete | Supported |
| TR-181 and TR-098 reads, TR-181 writes, parameter discovery | Supported |
| Multi-tenant operator scoping (region/customer), RBAC permissions | Supported, enforced centrally |
| Annex G UDP connection requests (STUN-learned address, signed datagram, EventCode 6 confirmation) | Implemented from the spec text; not yet validated against real CPE hardware |
| XMPP connection requests, TR-098 writes | Not implemented |
| Per-device CPE→ACS Digest credentials (`CWMP_DIGEST` rotation, self-activating on first Inform) | Supported, alongside the shared credential and mTLS |
| Object storage (S3) for firmware/uploads, multi-node HA | Not implemented (local disk) |
| Real-device compatibility matrix, load/soak evidence | Not yet recorded |

## Documents

- [ACS_CODEBASE_AUDIT_2026-08-28.md](ACS_CODEBASE_AUDIT_2026-08-28.md) — the audit this hardening work follows; see git history for `audit P0.x`/`P1.x` commits.
- [HANDOFF.md](HANDOFF.md) — project handoff notes.
- [deployment-testing-onboarding-guide.md](deployment-testing-onboarding-guide.md) — full environment/variable reference and test workflow.
- [EC2-DEPLOYMENT-GUIDE.md](EC2-DEPLOYMENT-GUIDE.md) — single-host EC2 deployment.
- [bss-integration-guide.md](bss-integration-guide.md) — BSS/CRM integration contract.
- [tr069-acs-application-design-v3.md](tr069-acs-application-design-v3.md), [tr069-acs-build-plan.md](tr069-acs-build-plan.md) — design and build plan (historical; the code is the source of truth where they differ).
