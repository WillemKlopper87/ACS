#!/bin/bash
# Restore a scripts/backup.sh tarball (audit P3.2). Destructive on the
# target database: it drops and recreates the public schema first. Run
# against a staging DSN to rehearse and to time your RTO.
#
# Usage: ACS_POSTGRES_DSN=postgres://... scripts/restore.sh <backup.tar.gz>
# Optional: ACS_FIRMWARE_STORAGE_ROOT / ACS_UPLOAD_STORAGE_ROOT targets.
set -euo pipefail

: "${ACS_POSTGRES_DSN:?ACS_POSTGRES_DSN is required}"
FILE="${1:?usage: restore.sh <backup.tar.gz>}"
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
FW="${ACS_FIRMWARE_STORAGE_ROOT:-$ROOT/backend/firmware-storage}"
UP="${ACS_UPLOAD_STORAGE_ROOT:-$ROOT/backend/upload-storage}"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

if [ -f "$FILE.sha256" ]; then
  (cd "$(dirname "$FILE")" && sha256sum -c "$(basename "$FILE").sha256")
fi
tar -C "$WORK" -xzf "$FILE"

echo "restoring database (drops the current public schema)"
psql -v ON_ERROR_STOP=1 "$ACS_POSTGRES_DSN" -c 'DROP SCHEMA public CASCADE; CREATE SCHEMA public;'
pg_restore --no-owner --no-privileges --dbname="$ACS_POSTGRES_DSN" "$WORK/acs.pgdump"

for pair in "firmware-storage:$FW" "upload-storage:$UP"; do
  name="${pair%%:*}"; target="${pair#*:}"
  if [ -f "$WORK/$name.tar" ]; then
    echo "restoring $name into $target"
    mkdir -p "$target"
    tar -C "$(dirname "$target")" -xf "$WORK/$name.tar"
  fi
done

echo "restore complete — start the services; store.Migrate applies any migrations newer than the backup"
