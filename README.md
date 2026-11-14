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
| USP controller | `backend/cmd/uspc` | TR-369/USP controller: WebSocket (`:9877`) and MQTT (`:1883`) MTPs, `/healthz`/`/readyz`/`/metrics` on `:8092`. Dispatches queued jobs, reconciles subscriptions, and gates connections with a network CIDR allowlist (`ACS_USP_ALLOWED_CIDRS`) plus an identity allowlist (the agent must already be a known `devices` row — see below). |
| Migrate tool | `backend/cmd/migrate` | Applies embedded migrations standalone (CI, pre-deploy). |
| Console | `frontend/` | React 19 + Vite operator UI, served by nginx in containers. |
| Persistence | PostgreSQL 18 | Forward-only embedded migrations, advisory-locked and checksum-verified at startup. Firmware and CPE uploads on local disk. |
| Observability | `infra/` | Prometheus, Alertmanager, Grafana provisioning, alert rules. |

Contracts: [backend/openapi.yaml](backend/openapi.yaml) (operator API — a
Go test fails if it drifts from the registered routes) and
[backend/openapi-bssadapter.yaml](backend/openapi-bssadapter.yaml).

## Standalone or through the NaaS platform

One setting, `ACS_INTEGRATION_MODE`, switches between the two ways of
running the ACS. It is read by `cmd/api`, `cmd/bssadapter`, the production
scripts and (through the API) the console; change it and restart.

| | `standalone` | `naas` (default) |
|---|---|---|
| Who drives the ACS | Operators, from the console | The ISP NaaS/BSS platform through `/bss/v1` and the TMF APIs, and operators |
| `cmd/bssadapter` | Starts, reports healthy, answers every integration route `503 ErrIntegrationDisabled`, runs no workers, needs no credentials or database | Full service |
| `cmd/api` | Accepts no service token | Accepts the adapter's service token on its five routes |
| Production profile | Asks for none of the adapter's settings | Requires customer-bound clients, no shared token, a source allowlist |
| Console | No BSS / NaaS screen; the sidebar shows "Standalone" | BSS / NaaS under Administration |

Switching does not delete anything: accounts, mappings, OAuth clients and
webhook subscriptions stay in the database and work again in `naas` mode.
Devices, onboarding, jobs, templates, policies and rollouts are the same in
both.

## Secure quick start

Every service **fails closed**: a missing, placeholder (`change-me`), or
too-short secret is a fatal startup error, not a warning. The only
opt-out is `ACS_INSECURE_DEV_MODE=true`, for isolated local development.

Bare-metal (Postgres from compose, services via `go run`):

```bash
source scripts/gen-env.sh            # generates and persists real secrets in ~/.acs-secrets.env
scripts/start.sh                     # Postgres + Prometheus/Alertmanager/Grafana + the Go
                                     # services + the console, in one pass
```

`gen-env.sh` is the lab/field-test profile. Before carrying production
device traffic, generate the fail-closed production device-plane profile:

```bash
export ACS_PRODUCTION_BIND_ADDRESS=10.20.0.10
export ACS_PRODUCTION_ALLOWED_CIDRS=10.30.0.0/16,10.40.0.0/16
export ACS_PRODUCTION_TLS_CERT=/etc/letsencrypt/live/acs.example/fullchain.pem
export ACS_PRODUCTION_TLS_KEY=/etc/letsencrypt/live/acs.example/privkey.pem
export ACS_PRODUCTION_USP_CLIENT_CA_CERT=/etc/acs/tls/usp-client-ca.pem
export ACS_PRODUCTION_INTEGRATION_MODE=standalone         # or naas, with the line below
# export ACS_PRODUCTION_BSS_ALLOWED_CIDRS=198.51.100.0/24 # naas only: where the platform calls from
source scripts/gen-production-env.sh
```

The production profile also covers the operator and integration plane, and
`cmd/api` and `cmd/bssadapter` refuse to start without it: the BSS adapter
accepts customer-bound OAuth clients only (no shared token), answers its
integration routes only to `ACS_BSS_ALLOWED_CIDRS`, and every operator role
that can change devices must use two-step sign-in. `scripts/security-preflight.sh`
reports every missing setting at once.

This requires readable TLS material, refuses wildcard listener addresses,
uses TLS 1.2 or newer, disables Basic/plaintext transport, restricts CWMP and USP
source networks, and keeps the shared CWMP credential bootstrap-only.
Established devices must use their own CWMP credential or mTLS identity.

Run `gen-env.sh` first: it generates `GRAFANA_ADMIN_PASSWORD` and
`ACS_GRAFANA_DB_PASSWORD` and writes them to `infra/.env`, which is what
lets `docker compose` interpolate the whole file (it does that before
selecting services, so Grafana's mandatory variables must resolve even
for `up -d postgres`). Starting compose by hand without that file still
needs both variables set. `start.sh` then creates the `grafana_ro`
database role, brings Grafana up on `127.0.0.1:3000` with its datasources
and dashboards already provisioned, and prints the generated admin
password. Reach it with `ssh -L 3000:127.0.0.1:3000 …`, or rerun with
`ACS_GRAFANA_PUBLIC=1` to publish it (restrict port 3000 to your own
address if you do).

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
| `ACS_CREDENTIAL_ENCRYPTION_KEY` (≥16 B) | api | Device/CLI/VPN credentials at rest; operators' two-step sign-in secrets |
| `ACS_CWMP_POSTGRES_DSN`, `ACS_API_POSTGRES_DSN`, `ACS_BSS_POSTGRES_DSN`, `ACS_USP_POSTGRES_DSN` (optional) | acs, api, bssadapter, uspc | A database login per service, each with only the tables that service uses, instead of the shared `ACS_POSTGRES_DSN`. Written by `scripts/db-service-roles.sh`; see "Separate database logins per service" in the runbook. `cmd/acs` then verifies the schema at start instead of migrating it (`ACS_DB_MIGRATIONS=check`). |
| `ACS_INTEGRATION_MODE` | api, bssadapter | `naas` (default) or `standalone`. See "Standalone or through the NaaS platform". |
| `ACS_BSS_REQUIRE_ALLOCATED_DEVICES` (optional) | bssadapter | `true`: an ISP's integration can map or pre-provision only a device the operator has already allocated to that ISP (device import, or the device's customer set in the console). Off by default: an unassigned device joins the first ISP that names its serial. Turn it on before a second ISP is connected. |
| `ACS_BSS_ALLOWED_CIDRS` (required in production with `naas`) | bssadapter | Source networks the `/bss/v1` and `/tmf-api` routes accept calls from; others get `403 ErrSourceNotAllowed` before authentication. Unset in lab: any source. Behind a proxy, set `ACS_TRUSTED_PROXY_CIDRS` so the real caller is seen. |
| `ACS_BSS_REQUIRE_CUSTOMER_BOUND_CLIENTS` (required `true` in production) | bssadapter | Accept only OAuth clients bound to one customer: no shared token, no fleet-wide client. |
| `ACS_WEBGUI_ALLOW_SAME_ORIGIN` (optional) | api | `true` turns off the sandbox on device web pages relayed by the web-GUI proxy. Lab only: without the sandbox a device's scripts run on the API's origin (`docs/SECURITY-ASSESSMENT-2026-11-14.md`, F1). |
| `ACS_MFA_REQUIRED_ROLES` (optional) | api | Roles that must use two-step sign-in: a comma-separated list (`superadmin,manager`) or `all`. Unset: each operator chooses. Needs `ACS_CREDENTIAL_ENCRYPTION_KEY`. |
| `ACS_INTERNAL_SERVICE_TOKEN` (≥32 B) | api, bssadapter | Adapter → API machine identity (narrow route allowlist) |
| `ACS_DIGEST_USERNAME` / `ACS_DIGEST_PASSWORD` (≥16 B) **or** `ACS_MTLS_CA_CERT` | acs | CPE authentication |
| `ACS_BSS_OAUTH_SIGNING_SECRET` (≥32 B) **or** `ACS_BSS_API_TOKEN` | bssadapter | Inbound BSS caller auth |

Recommended hardening variables: `ACS_DEVICE_NET_ALLOWED_CIDRS` (SSRF
allowlist for the web-GUI proxy and console bridge),
`ACS_USP_ALLOWED_CIDRS` (network-level allowlist for the USP
controller's WebSocket/MQTT listeners; identity-level allowlisting is
unconditional — pre-register a device via the bulk-import API before
its first USP contact),
`ACS_API_CORS_ORIGIN` (defaults to `ACS_FRONTEND_BASE_URL`; the console's
exact origin. The console's session is a cookie, which needs one exact
origin on the same site as the API: `*` is refused in production and, in
the lab, makes the console ask for a sign-in on every reload),
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
- Device and tenant notifications: subscribe an external NMS or integration
  relay through the TMF688 event hub; alarms remain queryable through TMF642.
  See [Alerting and NMS integration](docs/integration/08-alerting-and-nms.md)
  for the signed contract, NetPod-style setup, retries, and receiver checks.
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
| Object storage for firmware/uploads (`ACS_OBJECT_STORE=s3`: AWS S3, MinIO, any S3-compatible) | Supported; local disk remains the default |
| Compatibility matrix and load mode (`docs/COMPATIBILITY.md`, `TestIntegration_CPELoad`) | Mock coverage per vendor profile; real-device rows not yet recorded |

## Documents

- [ACS_CODEBASE_AUDIT_2026-08-28.md](ACS_CODEBASE_AUDIT_2026-08-28.md) — the audit this hardening work follows; see git history for `audit P0.x`/`P1.x` commits.
- [docs/OPERATIONS-RUNBOOK.md](docs/OPERATIONS-RUNBOOK.md) — rolling upgrades, backup/restore drills (RPO/RTO), secret rotation, alert responses.
- [docs/COMPATIBILITY.md](docs/COMPATIBILITY.md) — per-vendor and per-capability coverage; where real-device results get recorded.
- [HANDOFF.md](HANDOFF.md) — project handoff notes.
- [deployment-testing-onboarding-guide.md](deployment-testing-onboarding-guide.md) — full environment/variable reference and test workflow.
- [EC2-DEPLOYMENT-GUIDE.md](EC2-DEPLOYMENT-GUIDE.md) — single-host EC2 deployment.
- [bss-integration-guide.md](bss-integration-guide.md) — BSS/CRM integration contract.
- [docs/TMF-API-STATUS.md](docs/TMF-API-STATUS.md) — current TMF638/639/640/641/642/656/688 routes and behavior.
- [tr069-acs-application-design-v3.md](tr069-acs-application-design-v3.md), [tr069-acs-build-plan.md](tr069-acs-build-plan.md) — design and build plan (historical; the code is the source of truth where they differ).
