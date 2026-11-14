#!/usr/bin/env bash
# Give each ACS service its own database login (security assessment O5).
#
# By default every service connects as "acs", the role that owns the
# schema, so taking over any one of them gives read and write access to
# every table. After this script:
#
#   acs_gateway  cmd/acs         (CWMP, faces the CPEs)
#   acs_api      cmd/api         (operator console API)
#   acs_bss      cmd/bssadapter  (BSS / NaaS integration)
#   acs_usp      cmd/uspc        (TR-369)
#
# each log in with a password of their own and hold only the table
# privileges their code uses (backend/internal/store/dbroles/manifest.go).
# The adapter, for one, can no longer read operator accounts, two-step
# sign-in secrets or device credentials, and none of the four can change
# the schema. "acs" stays the owner and is used by cmd/migrate, backups
# and restores only.
#
# What the script does:
#   1. creates the four roles (or resets their passwords),
#   2. writes ACS_CWMP_POSTGRES_DSN, ACS_API_POSTGRES_DSN,
#      ACS_BSS_POSTGRES_DSN and ACS_USP_POSTGRES_DSN to the secrets file,
#   3. runs cmd/migrate, which grants the privileges. Every later
#      migrate run (scripts/start.sh does one on each start) re-applies
#      them, so a new release's tables are covered without re-running this.
#
# Idempotent. Restart the services afterwards.
#
# Usage:
#   scripts/db-service-roles.sh            enable, or rotate the passwords
#   scripts/db-service-roles.sh --disable  go back to the shared login
#
# Honours ACS_SECRETS_FILE (default ~/.acs-secrets.env) and, like
# scripts/grafana-db-role.sh, ACS_POSTGRES_HOST / _PORT / _DB / _USER.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
SECRETS_FILE="${ACS_SECRETS_FILE:-$HOME/.acs-secrets.env}"
if [ ! -f "$SECRETS_FILE" ]; then
  echo "$SECRETS_FILE not found: run scripts/start.sh once (or scripts/gen-env.sh) first" >&2
  exit 1
fi
# shellcheck disable=SC1090
source "$SECRETS_FILE"

HOST="${ACS_POSTGRES_HOST:-127.0.0.1}"
PORT="${ACS_POSTGRES_PORT:-5432}"
DB="${ACS_POSTGRES_DB:-acs}"
OWNER="${ACS_POSTGRES_USER:-acs}"

# role:variable, in the order of backend/internal/store/dbroles.Services.
ROLES=(
  "acs_gateway:ACS_CWMP_POSTGRES_DSN"
  "acs_api:ACS_API_POSTGRES_DSN"
  "acs_bss:ACS_BSS_POSTGRES_DSN"
  "acs_usp:ACS_USP_POSTGRES_DSN"
)

owner_psql() {
  PGPASSWORD="${ACS_POSTGRES_PASSWORD:-acs}" psql \
    --host "$HOST" --port "$PORT" --username "$OWNER" --dbname "$DB" \
    --no-psqlrc --quiet --set ON_ERROR_STOP=1 "$@"
}

# Replace a variable's line in the secrets file, or add it.
set_secret() {
  local var="$1" line="$2" tmp
  tmp="$(mktemp "${SECRETS_FILE}.XXXXXX")"
  chmod 600 "$tmp"
  grep -v "^export ${var}=" "$SECRETS_FILE" > "$tmp" || true
  [ -n "$line" ] && printf '%s\n' "$line" >> "$tmp"
  mv "$tmp" "$SECRETS_FILE"
}

if [ "${1:-}" = "--disable" ]; then
  for entry in "${ROLES[@]}"; do
    role="${entry%%:*}"; var="${entry#*:}"
    if [ "$var" = "ACS_USP_POSTGRES_DSN" ]; then
      # cmd/uspc has no fallback: point it back at the shared login.
      set_secret "$var" 'export ACS_USP_POSTGRES_DSN="$ACS_POSTGRES_DSN"'
    else
      set_secret "$var" ""
    fi
    # The login is closed, not dropped: a role that owns nothing but
    # holds grants cannot be dropped without revoking them first, and a
    # later re-enable reuses it.
    owner_psql --set role="$role" <<'SQL'
SELECT format('ALTER ROLE %I NOLOGIN', :'role')
 WHERE EXISTS (SELECT 1 FROM pg_roles WHERE rolname = :'role') \gexec
SQL
  done
  echo "Per-service database logins disabled: all services use ACS_POSTGRES_DSN again."
  echo "Restart the services (scripts/stop.sh, then scripts/start.sh)."
  exit 0
fi
if [ -n "${1:-}" ]; then
  echo "unknown argument: $1 (the only option is --disable)" >&2
  exit 1
fi

for entry in "${ROLES[@]}"; do
  role="${entry%%:*}"; var="${entry#*:}"
  # Hex only, so the password needs no escaping in a connection URL.
  password="$(openssl rand -hex 24)"
  owner_psql --set role="$role" --set pw="$password" --set db="$DB" <<'SQL'
SELECT format('CREATE ROLE %I', :'role')
 WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = :'role') \gexec
-- A plain login: no superuser, no new roles or databases, no replication,
-- no row-security bypass, and nothing inherited from other roles.
SELECT format('ALTER ROLE %I WITH LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS NOINHERIT PASSWORD %L', :'role', :'pw') \gexec
SELECT format('GRANT CONNECT ON DATABASE %I TO %I', :'db', :'role') \gexec
SQL
  set_secret "$var" "export ${var}=\"postgres://${role}:${password}@${HOST}:${PORT}/${DB}?sslmode=${ACS_POSTGRES_SSLMODE:-disable}\""
  echo "role $role ready; $var written to $SECRETS_FILE"
done

# The privileges themselves come from cmd/migrate, as the owner.
echo "=== Granting privileges (cmd/migrate) ==="
if [ -x "$ROOT/backend/bin/migrate" ] && [ -z "${ACS_ROLES_REBUILD_MIGRATE:-}" ]; then
  "$ROOT/backend/bin/migrate"
else
  (cd "$ROOT/backend" && go run ./cmd/migrate)
fi

cat <<EOM

Per-service database logins are in place. Restart the services:
  scripts/stop.sh && scripts/start.sh
cmd/acs then verifies the schema instead of changing it, and its log says
"migrations verified, none applied".
To go back to the shared login: scripts/db-service-roles.sh --disable
EOM
