#!/bin/bash
# Restore a scripts/backup.sh tarball. This is destructive on the target
# database and file stores: public schema and restored storage roots are
# replaced with the backup contents.
#
# Usage: ACS_POSTGRES_DSN=postgres://... scripts/restore.sh <backup.tar.gz>
# Optional:
#   ACS_FIRMWARE_STORAGE_ROOT / ACS_UPLOAD_STORAGE_ROOT
#   ACS_DB_CLIENT_MODE=auto|compose|local (default auto)
set -euo pipefail

: "${ACS_POSTGRES_DSN:?ACS_POSTGRES_DSN is required}"
FILE="${1:?usage: restore.sh <backup.tar.gz>}"
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
FW="${ACS_FIRMWARE_STORAGE_ROOT:-$ROOT/backend/firmware-storage}"
UP="${ACS_UPLOAD_STORAGE_ROOT:-$ROOT/backend/upload-storage}"
MODE="${ACS_DB_CLIENT_MODE:-auto}"
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

if [ -f "$FILE.sha256" ]; then
  (cd "$(dirname "$FILE")" && sha256sum -c "$(basename "$FILE").sha256")
fi
tar -C "$WORK" -xzf "$FILE"
[ -s "$WORK/acs.pgdump" ] || { echo "backup does not contain acs.pgdump" >&2; exit 1; }

echo "restoring database (drops the current public schema)"
PG_CONTAINER="$(compose_postgres_container || true)"
if [ "$MODE" = "compose" ] || { [ "$MODE" = "auto" ] && is_default_local_dsn && [ -n "$PG_CONTAINER" ]; }; then
  if [ -z "$PG_CONTAINER" ]; then
    echo "compose DB client requested but no running Compose postgres service was found" >&2
    exit 1
  fi
  echo "using psql/pg_restore from running Postgres container $PG_CONTAINER"
  docker exec "$PG_CONTAINER" psql -U acs -d acs -v ON_ERROR_STOP=1 -c 'DROP SCHEMA public CASCADE; CREATE SCHEMA public;'
  docker exec -i "$PG_CONTAINER" pg_restore --no-owner --no-privileges -U acs -d acs < "$WORK/acs.pgdump"
else
  command -v psql >/dev/null 2>&1 || { echo "psql not found" >&2; exit 1; }
  command -v pg_restore >/dev/null 2>&1 || { echo "pg_restore not found" >&2; exit 1; }
  psql -v ON_ERROR_STOP=1 "$ACS_POSTGRES_DSN" -c 'DROP SCHEMA public CASCADE; CREATE SCHEMA public;'
  pg_restore --no-owner --no-privileges --dbname="$ACS_POSTGRES_DSN" "$WORK/acs.pgdump"
fi

restore_store() {
  local archive_name="$1" target="$2"
  if [ -f "$WORK/$archive_name.tar" ]; then
    echo "restoring $archive_name into $target"
    rm -rf "$target"
    mkdir -p "$target"
    tar -C "$target" -xf "$WORK/$archive_name.tar"
  fi
}
restore_store firmware-storage "$FW"
restore_store upload-storage "$UP"

echo "restore complete — start the services; store.Migrate applies any migrations newer than the backup"
