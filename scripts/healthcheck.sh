#!/bin/bash
# Deterministic whole-stack readiness check for field/test deployments.
# Every URL can be overridden so CI can use non-default ports.
set -euo pipefail

check() {
  local name="$1" url="$2" pattern="${3:-}"
  local body
  if ! body="$(curl -fsS --connect-timeout 2 --max-time 5 "$url")"; then
    echo "FAIL: $name is not reachable at $url" >&2
    return 1
  fi
  if [ -n "$pattern" ] && ! printf '%s' "$body" | grep -Eq "$pattern"; then
    echo "FAIL: $name at $url returned an unexpected response: $body" >&2
    return 1
  fi
  echo "OK:   $name"
}

check "CWMP/ACS" "${ACS_HEALTH_ACS_URL:-http://127.0.0.1:7547/readyz}"
check "Operator API" "${ACS_HEALTH_API_URL:-http://127.0.0.1:8080/readyz}"
check "BSS adapter" "${ACS_HEALTH_BSS_URL:-http://127.0.0.1:8090/readyz}"
check "USP controller" "${ACS_HEALTH_USPC_URL:-http://127.0.0.1:8092/readyz}" '^ready$'

if [ "${ACS_HEALTH_SKIP_FRONTEND:-0}" != "1" ]; then
  check "Frontend" "${ACS_HEALTH_FRONTEND_URL:-http://127.0.0.1:5173/}"
fi

if [ "${ACS_HEALTH_SKIP_MONITORING:-0}" != "1" ]; then
  check "Prometheus" "${ACS_HEALTH_PROMETHEUS_URL:-http://127.0.0.1:9090/-/ready}"
  check "Grafana" "${ACS_HEALTH_GRAFANA_URL:-http://127.0.0.1:3000/api/health}" '"database"[[:space:]]*:[[:space:]]*"ok"'
fi

if [ -n "${ACS_POSTGRES_DSN:-}" ] && command -v psql >/dev/null 2>&1; then
  if ! psql "$ACS_POSTGRES_DSN" -v ON_ERROR_STOP=1 -tAc 'SELECT 1' | grep -qx '1'; then
    echo "FAIL: PostgreSQL readiness query failed" >&2
    exit 1
  fi
  echo "OK:   PostgreSQL"
fi

echo "All requested ACS readiness checks passed."
