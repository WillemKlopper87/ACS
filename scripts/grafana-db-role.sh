#!/usr/bin/env bash
# Create (or update) the SELECT-only Postgres role the Grafana dashboards
# use. Grafana's Postgres datasource runs whatever SQL a dashboard panel
# contains, so it must never hold the application's own credentials — an
# editor with write access to the ACS schema is a data-loss incident
# waiting to happen.
#
# Grants are per-column, not per-table, because three of the tables the
# dashboards need also hold secrets:
#   webhook_subscriptions.secret  HMAC signing key for outbound webhooks
#   webhook_deliveries.payload    the delivered event body
#   jobs.payload / result_detail  RPC arguments — a SET_PARAMETER job's
#                                 payload can contain a WPA key
# Those columns are deliberately excluded. There is no
# ALTER DEFAULT PRIVILEGES blanket grant either: a future migration must
# not silently make a new secret-bearing table world-readable to anyone
# with a Grafana login. Re-run this script after a migration that adds a
# table a dashboard needs.
#
# Idempotent: safe to re-run.
#
# Usage:
#   ACS_GRAFANA_DB_PASSWORD=... scripts/grafana-db-role.sh
#
# Honours the same connection variables as the rest of the stack:
#   ACS_POSTGRES_PASSWORD  password for the owning "acs" role (default: acs)
#   ACS_POSTGRES_HOST      default 127.0.0.1
#   ACS_POSTGRES_PORT      default 5432
#   ACS_POSTGRES_DB        default acs
set -euo pipefail

if [ -z "${ACS_GRAFANA_DB_PASSWORD:-}" ]; then
  echo "ACS_GRAFANA_DB_PASSWORD is required (generate one, e.g. openssl rand -base64 24)" >&2
  exit 1
fi

HOST="${ACS_POSTGRES_HOST:-127.0.0.1}"
PORT="${ACS_POSTGRES_PORT:-5432}"
DB="${ACS_POSTGRES_DB:-acs}"
OWNER="${ACS_POSTGRES_USER:-acs}"

# "table:col,col,..." — an empty column list means every column, used only
# for tables that carry no secrets.
READ_GRANTS=(
  "devices:"
  "regions:"
  "customers:"
  "projects:"
  "device_projects:"
  "bss_orders:"
  "jobs:id,command_key,device_id,type,status,attempts,max_attempts,created_by,created_at,updated_at,started_at,completed_at,fault_code,fault_string"
  "webhook_subscriptions:id,account_id,target_url,event_types,created_at"
  "webhook_deliveries:id,subscription_id,event_type,status,attempts,last_attempt_at,created_at"
)

grants=""
for entry in "${READ_GRANTS[@]}"; do
  table="${entry%%:*}"
  cols="${entry#*:}"
  if [ -z "$cols" ]; then
    target="ON public.${table}"
  else
    target="(${cols}) ON public.${table}"
  fi
  grants+="    IF to_regclass('public.${table}') IS NOT NULL THEN
      EXECUTE 'REVOKE ALL ON public.${table} FROM grafana_ro';
      EXECUTE 'GRANT SELECT ${target} TO grafana_ro';
    END IF;
"
done

PGPASSWORD="${ACS_POSTGRES_PASSWORD:-acs}" psql \
  --host "$HOST" --port "$PORT" --username "$OWNER" --dbname "$DB" \
  --no-psqlrc --set ON_ERROR_STOP=1 \
  --set grafana_pw="$ACS_GRAFANA_DB_PASSWORD" <<SQL
DO \$\$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'grafana_ro') THEN
    CREATE ROLE grafana_ro LOGIN;
  END IF;
END
\$\$;

-- Set separately from the DO block so the password arrives as a psql
-- variable rather than being interpolated into a string literal.
ALTER ROLE grafana_ro WITH PASSWORD :'grafana_pw';

-- No table creation, no temp objects: this role reads and nothing else.
REVOKE ALL ON SCHEMA public FROM grafana_ro;
GRANT CONNECT ON DATABASE "$DB" TO grafana_ro;
GRANT USAGE ON SCHEMA public TO grafana_ro;

-- Clear any grant an older, table-level version of this script left behind
-- before re-granting per column.
DO \$\$
DECLARE t text;
BEGIN
  FOR t IN SELECT tablename FROM pg_tables WHERE schemaname = 'public' LOOP
    EXECUTE format('REVOKE ALL ON public.%I FROM grafana_ro', t);
  END LOOP;
END
\$\$;

DO \$\$
BEGIN
$grants
END
\$\$;
SQL

echo "grafana_ro refreshed: SELECT on ${#READ_GRANTS[@]} dashboard tables, secret columns excluded."
