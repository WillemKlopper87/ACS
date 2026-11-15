#!/bin/sh
# Creates the per-service database logins for the Compose "containerized"
# profile (the db-roles service runs this once, before migrate).
#
# One login per service that has a password set in infra/.env:
#   ACS_GATEWAY_DB_PASSWORD -> acs_gateway (cmd/acs)
#   ACS_API_DB_PASSWORD     -> acs_api     (cmd/api)
#   ACS_BSS_DB_PASSWORD     -> acs_bss     (cmd/bssadapter)
#   ACS_USP_DB_PASSWORD     -> acs_usp     (cmd/uspc)
# A service without a password keeps the owner login, and nothing is
# created for it. The table privileges come from the migrate service,
# which runs after this one (backend/internal/store/dbroles). The script
# is the Compose counterpart of scripts/db-service-roles.sh and is safe
# to run again: it resets the passwords to the ones given.
set -eu

role() {
  name="$1" password="$2"
  if [ -z "$password" ]; then
    echo "service-roles: no password for $name, that service uses the owner login"
    return 0
  fi
  psql --no-psqlrc --quiet --set ON_ERROR_STOP=1 --set role="$name" --set pw="$password" --set db="$PGDATABASE" <<'SQL'
SELECT format('CREATE ROLE %I', :'role')
 WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = :'role') \gexec
SELECT format('ALTER ROLE %I WITH LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS NOINHERIT PASSWORD %L', :'role', :'pw') \gexec
SELECT format('GRANT CONNECT ON DATABASE %I TO %I', :'db', :'role') \gexec
SQL
  echo "service-roles: $name ready"
}

role acs_gateway "${ACS_GATEWAY_DB_PASSWORD:-}"
role acs_api "${ACS_API_DB_PASSWORD:-}"
role acs_bss "${ACS_BSS_DB_PASSWORD:-}"
role acs_usp "${ACS_USP_DB_PASSWORD:-}"
