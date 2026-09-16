# Operations runbook

Procedures for running the ACS in production (audit P3.2). Pair with the
root [README](../README.md) for architecture and secrets,
[COMPATIBILITY.md](COMPATIBILITY.md) for the per-firmware support contract,
and [FIELD-CPE-VALIDATION.md](FIELD-CPE-VALIDATION.md) for physical-device
qualification.

## Rolling upgrade

1. `git pull` / fetch the release; read migration files newer than the
   running version (`backend/internal/store/migrations/`). All are
   forward-only and applied under an advisory lock with checksums, so
   replicas can't race and edited history fails loudly.
2. `scripts/backup.sh` (below) before any upgrade that adds migrations.
3. Apply migrations ahead of the fleet: `ACS_POSTGRES_DSN=... go run ./cmd/migrate`
   (or let the first new replica do it — the lock serializes either way).
4. Restart services one at a time; each HTTP service drains in-flight work
   on SIGTERM. Recommended order: `bssadapter` → `api` → `uspc` → `acs`.
   The USP controller is now a first-class deployed service; do not omit it
   from a release restart. CPE sessions cut mid-flight are safe: leases
   expire and the reaper requeues the job.
5. Verify the complete control plane with `scripts/healthcheck.sh`. At a
   minimum this covers API `:8080/readyz`, CWMP `:7547/readyz`, BSS/TMF
   `:8090/readyz`, USP `:8092/readyz`, plus the frontend/monitoring stack
   unless explicitly skipped by the deployment profile. Then watch
   `acs_jobs_stale_leases` and the `ServiceDown` alert stay quiet.
6. Rollback = redeploy the previous binaries. Schema rollback does not
   exist (forward-only): if a migration must be undone, restore from
   backup (below) and accept the RPO window, or write a new forward
   migration that reverses the change.

## Real-CPE field preflight

Before pointing hardware at a release candidate, run:

```bash
./scripts/field-preflight.sh
```

The preflight records the exact commit, rejects a dirty worktree by default,
checks credential-file permissions, rejects directly exposed BSS/USP
management binds, calls the same whole-stack readiness check used by CI, and
requires an explicit `ACS_FIELD_TEST_ACK=1` when plaintext USP is deliberately
used on an isolated lab network.

A passing preflight proves the test environment is reproducible and healthy;
it does **not** prove a CPE/firmware is compatible. Record device firmware,
capture IDs and job command keys using `docs/FIELD-CPE-VALIDATION.md`.

## First-contact CPE troubleshooting

Use captures to separate the failure stages instead of changing several
compatibility settings at once:

1. For a CPE that has never successfully onboarded, start a **CWMP remote-IP
   capture** before reproducing the problem. An `AuthenticationFailure`
   event proves the request reached the HTTP/CWMP handler even though the
   device did not authenticate. An `Inform` proves authentication and CWMP
   session startup both succeeded.
2. Optional host-side reachability probe: set
   `ACS_ONBOARDING_LISTENER=once` on `cmd/acs`. It logs incoming POST
   reachability on the normal CWMP endpoint and disables itself after the
   first successful Inform. It **does not bypass authentication** and should
   be treated as temporary troubleshooting telemetry, not a second ACS
   endpoint.
3. Once Inform succeeds, run parameter discovery before writes. Confirm the
   actual `Device.` or `InternetGatewayDevice.` root and vendor-extension
   paths.
4. Test Connection Request and require a subsequent Event 6
   `CONNECTION REQUEST` Inform. Only after that should NAT/STUN/Annex G be
   diagnosed.
5. Preserve the bounded, redacted capture when a new valid vendor SOAP/auth
   or fault shape is found, and turn it into a sanitized regression fixture.

### Huawei-specific rules

Huawei firmware families vary significantly; do not create one global
"Huawei compatibility mode".

- **N5368X / 5G CPE**: keep Connection Request authentication on
  Digest-SHA256. ACS supports SHA-256 in both CPE→ACS and ACS→CPE directions.
  If the device reaches ACS but stops/loops after the first 401, capture that
  behavior first. In an isolated test deployment, try
  `ACS_DIGEST_ALGORITHMS=SHA-256` to reduce the challenge to one minimal
  Digest line before considering Basic authentication.
- **EchoLife / TR-098**: discover writability before Wi-Fi writes. Several
  firmwares advertise `WLANConfiguration.{i}.KeyPassphrase` but reject a
  write; use a discovered writable `PreSharedKey.1.KeyPassphrase` path when
  present.
- A 9005 fault is a path/model mismatch until proved otherwise: rediscover
  the parameter tree and record the exact firmware before adding a vendor
  mapping.
- Do not lower TLS or enable Basic fleet-wide because one Huawei build needs
  it. Isolate the affected test profile and capture evidence for the exact
  model/firmware.

## Backup and restore (RPO/RTO)

- **Backup**: `ACS_POSTGRES_DSN=... scripts/backup.sh /backups` — a PostgreSQL
  custom-format dump plus the firmware/upload stores in one checksummed
  tarball. For the repository's standard Compose PostgreSQL deployment the
  script uses the running database container's matching `pg_dump`, avoiding
  host-client/server major-version mismatches. Set `ACS_DB_CLIENT_MODE=host`
  for an external database where a compatible host client is managed
  separately. Cron the backup; the cadence *is* your RPO (hourly cron ⇒ ≤1 h
  of lost writes).
  With `ACS_OBJECT_STORE=s3` skip the file stores: use bucket versioning
  and lifecycle rules instead, and back up only the database.
- **Restore**: `ACS_POSTGRES_DSN=... scripts/restore.sh <tarball>` —
  destructive on the target schema; services started afterwards apply any
  newer migrations automatically. The archive records logical firmware and
  upload store names, so a restore can target custom storage roots.
- **Drill (do this before you need it)**: restore the latest backup into
  a staging database quarterly, time it, and record it here. The `field-rc`
  workflow also performs a destructive database + file-store rehearsal on
  every candidate/main change.

| Date | Backup size | Restore time (RTO observed) | Verified by |
|---|---|---|---|
| — | — | — | no physical/staging drill recorded yet; CI rehearsal is automated |

## Secret rotation

- `ACS_JWT_SIGNING_SECRET`: rotating it invalidates every operator
  session, browser ticket, and transfer-URL token at once — rotate in a
  maintenance window; in-flight firmware downloads with old signed URLs
  will fail and be dead-lettered after the 24 h transfer deadline.
- Operator sessions: `POST /api/v1/auth/logout` (self) or a superadmin
  password reset both bump `token_version` and revoke outstanding JWTs.
- CPE credentials: rotate per device via
  `POST /devices/{id}/credentials/rotate` (`CWMP_DIGEST` self-activates
  on the device's next authenticated Inform; `CONNECTION_REQUEST` needs
  the explicit activate step). The shared `ACS_DIGEST_*` pair keeps
  working for un-rotated devices.
- `ACS_CREDENTIAL_ENCRYPTION_KEY`: set identically on cmd/api and
  cmd/acs. There is no re-encryption tool; rotating it orphans stored
  credential ciphertexts — rotate device credentials afterwards.

## Alert responses

| Alert | First moves |
|---|---|
| `ServiceDown` | `scripts/healthcheck.sh`, then service logs; a fail-closed config error prints exactly which variable is missing or placeholder. Include `uspc` rather than checking only ACS/API/BSS. |
| `StaleJobLeases` / `JobsDeadLettered` | Is cmd/acs running and reaching Postgres? Inspect `SELECT * FROM jobs WHERE fault_code='LEASE_EXPIRED' ORDER BY updated_at DESC LIMIT 20;` — the fault_string names the last holder. Requeue by re-creating the job via the API. |
| `FirmwareTransfersTimingOut` | Can the CPE reach `ACS_FIRMWARE_BASE_URL`? Signed URLs expire after 24 h — a rollout paused longer than that needs re-queuing. |
| `DatabasePoolSaturated` | Raise `ACS_DB_MAX_OPEN_CONNS` within Postgres `max_connections` headroom, or find the slow query (`pg_stat_activity`). |
| `CWMPAuthFailures` | Start a remote-IP capture before changing auth. Check for a fleet-wide credential mismatch/recent `ACS_DIGEST_PASSWORD` change or a scanner; for Huawei inspect the Digest retry/challenge behavior first. |
| `NoDevicesOnline` | The CWMP listener or its TLS cert: check `:7547` reachability from outside and certificate expiry. |

## SSH host-key change on a device

A legitimate key change (firmware reset) makes console sessions fail
with "host key does not match the key pinned". After verifying the
change out of band:
`DELETE FROM device_ssh_host_keys WHERE device_id = '<id>';` — the next
session re-pins (trust-on-first-use).

## Tenancy note (audit P2.2)

Row-level security was evaluated and deliberately not adopted: every
read/mutation path already goes through the central scope guard
(`getScopedDevice` / SQL scope predicates, negative-tested per route in
CI), and Postgres RLS would require per-request transaction-local
principal context on every one of ~25 repositories for a second copy of
the same predicate. Revisit if repositories ever get written against by
code that bypasses the handler layer.
## Production device-plane profile

The ordinary `scripts/gen-env.sh` and `scripts/quickstart.sh` flows are for
controlled lab and field qualification. Convert a host before admitting
production CPE traffic:

```bash
export ACS_PRODUCTION_BIND_ADDRESS=10.20.0.10
export ACS_PRODUCTION_ALLOWED_CIDRS=10.30.0.0/16
export ACS_PRODUCTION_TLS_CERT=/etc/acs/tls/fullchain.pem
export ACS_PRODUCTION_TLS_KEY=/etc/acs/tls/privkey.pem
export ACS_PRODUCTION_USP_CLIENT_CA_CERT=/etc/acs/tls/usp-client-ca.pem
source scripts/gen-production-env.sh
```

The generated profile applies the management CIDRs to both CWMP and USP,
requires a non-wildcard device-plane bind, and configures the USP client CA
used by the durable certificate-principal authentication layer.

Restart the host services after generating the profile and run
`scripts/field-preflight.sh`. Do not set
`ACS_CWMP_ALLOW_SHARED_ESTABLISHED=true` in production. The fleet-wide
CWMP credential is limited to the first Inform for a new inventory identity;
provision a per-device `CWMP_DIGEST` credential or mTLS identity before that
device's next session. This prevents one compromised fleet credential from
claiming an established device and receiving its queued work.
