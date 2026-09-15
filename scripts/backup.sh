#!/bin/bash
# Full backup of everything the ACS needs to recover: a custom-format
# PostgreSQL dump plus the firmware and CPE-upload stores. Produces one
# timestamped tarball and SHA-256 sidecar; pair with scripts/restore.sh.
#
# Usage: ACS_POSTGRES_DSN=postgres://... scripts/backup.sh [dest-dir]
# Optional:
#   ACS_FIRMWARE_STORAGE_ROOT / ACS_UPLOAD_STORAGE_ROOT
#   ACS_DB_CLIENT_MODE=auto|compose|local (default auto)
#
# In the standard quickstart, PostgreSQL runs in Docker. auto mode uses
# that container's pg_dump when the DSN targets localhost:5432/acs, which
# guarantees the client major version matches the server. External DBs use
# the host pg_dump instead.
set -euo pipefail

: "${ACS_POSTGRES_DSN:?ACS_POSTGRES_DSN is required}"
DEST="${1:-./backups}"
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
FW="${ACS_FIRMWARE_STORAGE_ROOT:-$ROOT/backend/firmware-storage}"
UP="${ACS_UPLOAD_STORAGE_ROOT:-$ROOT/backend/upload-storage}"
MODE="${ACS_DB_CLIENT_MODE:-auto}"
STAMP="$(date -u +%Y%m%dT%H%M%SZ)"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

case "$MODE" in auto|compose|local) ;; *) echo "ACS_DB_CLIENT_MODE must be auto, compose or local" >&2; exit 2 ;; esac

compose_postgres_container() {
  command -v docker >/dev/null 2>&1 || return 1
  docker ps --filter 'label=com.docker.compose.service=postgres' --format '{{.ID}}' 2>/dev/null | head -n1
}

is_default_local_dsn() {
  case "$ACS_POSTGRES_DSN" in
    postgres://*@localhost:5432/acs*|postgresql://*@localhost:5432/acs*|postgres://*@127.0.0.1:5432/acs*|postgresql://*@127.0.0.1:5432/acs*) return 0 ;;
    *) return 1 ;;
  esac
}

mkdir -p "$DEST"
echo "[$STAMP] dumping database"
PG_CONTAINER="$(compose_postgres_container || true)"
if [ "$MODE" = "compose" ] || { [ "$MODE" = "auto" ] && is_default_local_dsn && [ -n "$PG_CONTAINER" ]; }; then
  if [ -z "$PG_CONTAINER" ]; then
    echo "compose DB client requested but no running Compose postgres service was found" >&2
    exit 1
  fi
  echo "[$STAMP] using pg_dump from running Postgres container $PG_CONTAINER"
  docker exec "$PG_CONTAINER" pg_dump --format=custom --no-owner --no-privileges -U acs -d acs > "$WORK/acs.pgdump"
else
  command -v pg_dump >/dev/null 2>&1 || { echo "pg_dump not found; install a client matching your external PostgreSQL server" >&2; exit 1; }
  echo "[$STAMP] using host pg_dump for external/local database"
  pg_dump --format=custom --no-owner --no-privileges --file="$WORK/acs.pgdump" "$ACS_POSTGRES_DSN"
fi

archive_store() {
  local archive_name="$1" dir="$2"
  if [ -d "$dir" ]; then
    echo "[$STAMP] archiving $archive_name from $dir"
    # Archive the directory contents, not its basename. That makes a
    # backup portable across custom storage-root paths at restore time.
    tar -C "$dir" -cf "$WORK/$archive_name.tar" .
  else
    echo "[$STAMP] $archive_name not present at $dir — skipping"
  fi
}
archive_store firmware-storage "$FW"
archive_store upload-storage "$UP"

OUT="$DEST/acs-backup-$STAMP.tar.gz"
tar -C "$WORK" -czf "$OUT" .
sha256sum "$OUT" > "$OUT.sha256"
echo "[$STAMP] wrote $OUT ($(du -h "$OUT" | cut -f1)) — checksum in $OUT.sha256"
