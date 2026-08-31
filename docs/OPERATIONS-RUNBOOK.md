# Operations runbook

Procedures for running the ACS in production (audit P3.2). Pair with the
root [README](../README.md) for architecture and secrets, and
[COMPATIBILITY.md](COMPATIBILITY.md) for device coverage.

## Rolling upgrade

1. `git pull` / fetch the release; read migration files newer than the
   running version (`backend/internal/store/migrations/`). All are
   forward-only and applied under an advisory lock with checksums, so
   replicas can't race and edited history fails loudly.
2. `scripts/backup.sh` (below) before any upgrade that adds migrations.
3. Apply migrations ahead of the fleet: `ACS_POSTGRES_DSN=... go run ./cmd/migrate`
   (or let the first new replica do it — the lock serializes either way).
4. Restart services one at a time; each drains in-flight HTTP for up to
   15 s on SIGTERM. Order: `bssadapter` → `api` → `acs`. CPE sessions cut
   mid-flight are safe: leases expire and the reaper requeues the job.
5. Verify: `curl -fsS localhost:8080/readyz localhost:7547/readyz localhost:8090/readyz`,
   then watch `acs_jobs_stale_leases` and the `ServiceDown` alert stay quiet.
6. Rollback = redeploy the previous binaries. Schema rollback does not
   exist (forward-only): if a migration must be undone, restore from
   backup (below) and accept the RPO window, or write a new forward
   migration that reverses the change.

## Backup and restore (RPO/RTO)

- **Backup**: `ACS_POSTGRES_DSN=... scripts/backup.sh /backups` — pg_dump
  plus the firmware/upload stores in one checksummed tarball. Cron it;
  the cadence *is* your RPO (hourly cron ⇒ ≤1 h of lost writes).
  With `ACS_OBJECT_STORE=s3` skip the file stores: use bucket versioning
  and lifecycle rules instead, and back up only the database.
- **Restore**: `ACS_POSTGRES_DSN=... scripts/restore.sh <tarball>` —
  destructive on the target schema; services started afterwards apply any
  newer migrations automatically.
- **Drill (do this before you need it)**: restore the latest backup into
  a staging database quarterly, time it, and record it here:

| Date | Backup size | Restore time (RTO observed) | Verified by |
|---|---|---|---|
| — | — | — | no drill recorded yet |

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
| `ServiceDown` | `journalctl`/container logs; a fail-closed config error prints exactly which variable is missing or placeholder. |
| `StaleJobLeases` / `JobsDeadLettered` | Is cmd/acs running and reaching Postgres? Inspect `SELECT * FROM jobs WHERE fault_code='LEASE_EXPIRED' ORDER BY updated_at DESC LIMIT 20;` — the fault_string names the last holder. Requeue by re-creating the job via the API. |
| `FirmwareTransfersTimingOut` | Can the CPE reach `ACS_FIRMWARE_BASE_URL`? Signed URLs expire after 24 h — a rollout paused longer than that needs re-queuing. |
| `DatabasePoolSaturated` | Raise `ACS_DB_MAX_OPEN_CONNS` within Postgres `max_connections` headroom, or find the slow query (`pg_stat_activity`). |
| `CWMPAuthFailures` | Fleet-wide credential mismatch (recent `ACS_DIGEST_PASSWORD` change?), or a scanner — check source IPs in cmd/acs logs. |
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
