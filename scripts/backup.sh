#!/bin/bash
# Full backup of everything the ACS needs to come back from nothing
# (audit P2.3/P3.2): a pg_dump of the database plus the two local file
# stores (firmware images, CPE uploads). Produces one timestamped
# tarball; pair with scripts/restore.sh.
#
# Usage: ACS_POSTGRES_DSN=postgres://... scripts/backup.sh [dest-dir]
# Optional: ACS_FIRMWARE_STORAGE_ROOT / ACS_UPLOAD_STORAGE_ROOT (default
# ./backend/firmware-storage and ./backend/upload-storage).
#
# RPO is the interval you run this at (cron it hourly for a ~1h RPO);
# RTO is dominated by pg_restore time — measure it with restore.sh
# against a staging database and record the number in your runbook.
set -euo pipefail

: "${ACS_POSTGRES_DSN:?ACS_POSTGRES_DSN is required}"
DEST="${1:-./backups}"
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
FW="${ACS_FIRMWARE_STORAGE_ROOT:-$ROOT/backend/firmware-storage}"
UP="${ACS_UPLOAD_STORAGE_ROOT:-$ROOT/backend/upload-storage}"
STAMP="$(date -u +%Y%m%dT%H%M%SZ)"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

mkdir -p "$DEST"
echo "[$STAMP] dumping database"
pg_dump --format=custom --no-owner --no-privileges --file="$WORK/acs.pgdump" "$ACS_POSTGRES_DSN"

for dir in "$FW" "$UP"; do
  name="$(basename "$dir")"
  if [ -d "$dir" ]; then
    echo "[$STAMP] archiving $name"
    tar -C "$(dirname "$dir")" -cf "$WORK/$name.tar" "$name"
  else
    echo "[$STAMP] $name not present at $dir — skipping"
  fi
done

OUT="$DEST/acs-backup-$STAMP.tar.gz"
tar -C "$WORK" -czf "$OUT" .
sha256sum "$OUT" > "$OUT.sha256"
echo "[$STAMP] wrote $OUT ($(du -h "$OUT" | cut -f1)) — checksum in $OUT.sha256"
