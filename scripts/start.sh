#!/bin/bash
# Starts the full host-based ACS stack: Postgres/monitoring containers plus
# cmd/acs, cmd/api, cmd/bssadapter, cmd/uspc and the frontend. Application
# processes are backgrounded with PID files so one SSH session is enough.
# Rerun freely: the previous application processes are stopped first.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
LOG_DIR="$HOME/acs-logs"
mkdir -p "$LOG_DIR"

if [ "${ACS_ENV_ALREADY_LOADED:-0}" = "1" ]; then
  echo "=== Using preloaded production environment ==="
else
  echo "=== Loading/generating credentials ==="
  # shellcheck disable=SC1091
  source "$ROOT/scripts/gen-env.sh"
fi

PROFILE="${ACS_DEPLOYMENT_PROFILE:-lab}"
case "$PROFILE" in
  lab|production) ;;
  *)
    echo "ERROR: ACS_DEPLOYMENT_PROFILE must be 'lab' or 'production' (got '$PROFILE')." >&2
    exit 1
    ;;
esac

# Production never publishes the operator console or bearer-token API from
# their plain-HTTP development listeners. The externally visible origin is an
# HTTPS endpoint owned by a real reverse proxy/load balancer; the processes
# started below are loopback-only upstreams. Keeping this logic here (rather
# than only in start-production.sh) means `ACS_DEPLOYMENT_PROFILE=production
# ./scripts/start.sh` is fail-closed too.
validate_https_origin() {
  local name="$1" value="${!1:-}"
  if ! python3 - "$value" <<'PY'
import sys
from urllib.parse import urlsplit

u = urlsplit(sys.argv[1])
try:
    _ = u.port
    valid_port = True
except ValueError:
    valid_port = False
valid = (
    u.scheme == "https"
    and bool(u.hostname)
    and valid_port
    and u.path in ("", "/")
    and not u.query
    and not u.fragment
    and not u.username
    and not u.password
)
raise SystemExit(0 if valid else 1)
PY
  then
    echo "ERROR: $name must be a valid HTTPS origin with no path/query/fragment, e.g. https://acs.example.com (got '${value:-<unset>}')." >&2
    exit 1
  fi
}

if [ "$PROFILE" = "production" ]; then
  validate_https_origin ACS_FRONTEND_BASE_URL
  validate_https_origin ACS_API_PUBLIC_URL
  FRONTEND_PUBLIC_URL="${ACS_FRONTEND_BASE_URL%/}"
  API_PUBLIC_URL="${ACS_API_PUBLIC_URL%/}"

  # The TLS ingress is the only public operator surface. Every deliberately
  # plain-HTTP host control listener stays loopback-only even if persisted lab
  # defaults were manually changed. Keep internal service URLs aligned with
  # those forced listeners before running the fail-closed preflight.
  export ACS_API_ADDR="127.0.0.1:8080"
  export ACS_BSS_ADDR="127.0.0.1:8090"
  export ACS_USP_HTTP_ADDR="127.0.0.1:8092"
  export ACS_FRONTEND_BIND="127.0.0.1"
  export ACS_INTERNAL_API_URL="http://127.0.0.1:8080"
  export ACS_BSS_ADAPTER_URL="http://127.0.0.1:8090"

  # One shared fail-closed production gate covers device transports, operator
  # ingress, host control listeners, monitoring exposure, TLS floors and
  # plaintext compatibility flags. Running it here means direct production use
  # of start.sh cannot bypass the checks that start-production.sh relies on.
  "$ROOT/scripts/security-preflight.sh"
else
  FRONTEND_PUBLIC_URL=""
  API_PUBLIC_URL=""
  export ACS_FRONTEND_BIND="${ACS_FRONTEND_BIND:-0.0.0.0}"
fi

echo "=== Detecting public IP ==="
detect_public_ip() {
  if [ -n "${ACS_PUBLIC_IP:-}" ]; then
    echo "$ACS_PUBLIC_IP"
    return
  fi
  local token
  token="$(curl -s -m 2 -X PUT "http://169.254.169.254/latest/api/token" -H "X-aws-ec2-metadata-token-ttl-seconds: 21600" 2>/dev/null)" || true
  if [ -n "$token" ]; then
    curl -s -m 2 -H "X-aws-ec2-metadata-token: $token" http://169.254.169.254/latest/meta-data/public-ipv4 2>/dev/null || true
    return
  fi
  curl -s -m 2 http://169.254.169.254/latest/meta-data/public-ipv4 2>/dev/null || true
}

PUBLIC_IP="$(detect_public_ip)"
if [ -z "$PUBLIC_IP" ]; then
  echo "Could not auto-detect a public IP (not on EC2, IMDS blocked, or metadata endpoint unreachable)."
  echo "Set ACS_PUBLIC_IP=<your-ip-or-hostname> and rerun, e.g.:"
  echo "  ACS_PUBLIC_IP=13.245.18.190 ./scripts/start.sh"
  exit 1
fi
echo "Public IP: $PUBLIC_IP"

if [ "$PROFILE" = "lab" ]; then
  FRONTEND_PUBLIC_URL="http://$PUBLIC_IP:5173"
  API_PUBLIC_URL="http://$PUBLIC_IP:8080"
  export ACS_FRONTEND_BASE_URL="$FRONTEND_PUBLIC_URL"
fi

set_compose_env() {
  local key="$1" value="$2" file="$ROOT/infra/.env"
  if grep -q "^${key}=" "$file"; then
    sed -i "s|^${key}=.*|${key}=${value}|" "$file"
  else
    echo "${key}=${value}" >> "$file"
  fi
}

if [ "${ACS_GRAFANA_PUBLIC:-}" = "1" ]; then
  GRAFANA_BIND="0.0.0.0"
  set_compose_env GRAFANA_ROOT_URL "http://$PUBLIC_IP:3000"
else
  GRAFANA_BIND="127.0.0.1"
  set_compose_env GRAFANA_ROOT_URL "http://localhost:3000"
fi
set_compose_env GRAFANA_BIND "$GRAFANA_BIND"

if [ "${ACS_PROMETHEUS_PUBLIC:-}" = "1" ]; then
  PROMETHEUS_BIND="0.0.0.0"
else
  PROMETHEUS_BIND="127.0.0.1"
fi
set_compose_env PROMETHEUS_BIND "$PROMETHEUS_BIND"

echo "=== Stopping any previous ACS application processes ==="
"$ROOT/scripts/stop.sh" || true
sleep 1

echo "=== Starting Postgres ==="
(cd "$ROOT/infra" && docker compose up -d postgres)
echo "Waiting for Postgres to accept connections..."
until docker exec infra-postgres-1 pg_isready -U acs >/dev/null 2>&1; do sleep 1; done
echo "Postgres is up."

# --- Monitoring stack -------------------------------------------------
echo "=== Grafana's read-only database role ==="
if command -v psql >/dev/null; then
  "$ROOT/scripts/grafana-db-role.sh" || echo "WARNING: grafana-db-role.sh failed — Postgres-backed dashboards will show auth errors."
else
  echo "WARNING: psql not found, skipping the grafana_ro role."
fi

echo "=== Starting monitoring stack (Prometheus, Alertmanager, Grafana) ==="
(cd "$ROOT/infra" && docker compose up -d prometheus alertmanager grafana)

wait_url() {
  local name="$1" url="$2" pid_file="${3:-}"
  for _ in $(seq 1 60); do
    if curl -fsS -m 2 "$url" >/dev/null 2>&1; then
      echo "$name is ready."
      return 0
    fi
    if [ -n "$pid_file" ] && [ -f "$pid_file" ]; then
      local pid
      pid="$(cat "$pid_file")"
      if ! kill -0 "$pid" 2>/dev/null; then
        echo "$name exited before becoming ready — check $LOG_DIR/${pid_file##*/}" >&2
        return 1
      fi
    fi
    sleep 2
  done
  echo "$name did not become ready within 120s ($url)." >&2
  return 1
}

wait_url "Prometheus" "http://127.0.0.1:9090/-/ready"
# Grafana's health endpoint can return 200 before the DB field is useful;
# the final whole-stack healthcheck validates the response body as well.
wait_url "Grafana" "http://127.0.0.1:3000/api/health"

echo "=== Building backend binaries ==="
cd "$ROOT/backend"
mkdir -p bin
go build -o bin/acs ./cmd/acs
go build -o bin/api ./cmd/api
go build -o bin/bssadapter ./cmd/bssadapter
go build -o bin/uspc ./cmd/uspc

start_process() {
  local name="$1"
  shift
  echo "=== Starting $name ==="
  nohup "$@" > "$LOG_DIR/$name.log" 2>&1 &
  echo $! > "$LOG_DIR/$name.pid"
  sleep 1
  if ! kill -0 "$(cat "$LOG_DIR/$name.pid")" 2>/dev/null; then
    echo "$name failed to start — check $LOG_DIR/$name.log" >&2
    tail -40 "$LOG_DIR/$name.log" || true
    exit 1
  fi
}

start_process acs "$ROOT/backend/bin/acs"
wait_url "cmd/acs" "${ACS_HEALTH_ACS_URL:-http://127.0.0.1:7547/readyz}" "$LOG_DIR/acs.pid"

start_process api "$ROOT/backend/bin/api"
wait_url "cmd/api" "${ACS_HEALTH_API_URL:-http://127.0.0.1:8080/readyz}" "$LOG_DIR/api.pid"

start_process bssadapter "$ROOT/backend/bin/bssadapter"
wait_url "cmd/bssadapter" "${ACS_HEALTH_BSS_URL:-http://127.0.0.1:8090/readyz}" "$LOG_DIR/bssadapter.pid"

if [ "${ACS_USP_ALLOW_PLAINTEXT:-false}" = "true" ] && { [ -z "${ACS_USP_TLS_CERT:-}" ] || [ -z "${ACS_USP_TLS_KEY:-}" ]; }; then
  echo "WARNING: USP WebSocket/MQTT is explicitly running plaintext for this quickstart/field-test deployment."
  echo "         Configure ACS_USP_TLS_CERT/ACS_USP_TLS_KEY and set ACS_USP_ALLOW_PLAINTEXT=false before production exposure."
fi
start_process uspc "$ROOT/backend/bin/uspc"
wait_url "cmd/uspc" "${ACS_HEALTH_USPC_URL:-http://127.0.0.1:8092/readyz}" "$LOG_DIR/uspc.pid"

echo "=== Building frontend ==="
cd "$ROOT/frontend"
echo "VITE_API_BASE_URL=$API_PUBLIC_URL" > .env.local
npm install --silent
npm run build

echo "=== Starting frontend static server (:5173) ==="
cd "$ROOT/frontend/dist"
nohup python3 "$ROOT/scripts/spa-server.py" 5173 "$ROOT/frontend/dist" "$API_PUBLIC_URL" > "$LOG_DIR/frontend.log" 2>&1 &
echo $! > "$LOG_DIR/frontend.pid"
wait_url "frontend" "${ACS_HEALTH_FRONTEND_URL:-http://127.0.0.1:5173/}" "$LOG_DIR/frontend.pid"

echo "=== Whole-stack readiness ==="
bash "$ROOT/scripts/healthcheck.sh"

echo ""
echo "=================================================="
echo "  ACS is running"
echo "=================================================="
CWMP_SCHEME="http"
if [ -n "${ACS_TLS_CERT:-}" ] && [ -n "${ACS_TLS_KEY:-}" ]; then
  CWMP_SCHEME="https"
fi
USP_SCHEME="ws"
if [ -n "${ACS_USP_TLS_CERT:-}" ] && [ -n "${ACS_USP_TLS_KEY:-}" ]; then
  USP_SCHEME="wss"
fi
if [ "$PROFILE" = "production" ]; then
  echo "Console/API: $FRONTEND_PUBLIC_URL (same-origin HTTPS ingress)"
  echo "  SPA upstream: http://127.0.0.1:5173"
  echo "  API upstream: http://127.0.0.1:8080"
else
  echo "Console:     $FRONTEND_PUBLIC_URL"
  echo "API:         $API_PUBLIC_URL"
fi
echo "CWMP URL:    $CWMP_SCHEME://$PUBLIC_IP:7547/cwmp"
echo "STUN:        $PUBLIC_IP:3478 (UDP)"
echo "USP WS:      $USP_SCHEME://$PUBLIC_IP:9877/usp"
echo "USP MQTT:    $PUBLIC_IP:1883"
echo "BSS adapter: http://127.0.0.1:8090 (host-local; use TLS reverse proxy for northbound access)"
echo "USP health:  http://127.0.0.1:8092/readyz"

if [ "${ACS_GRAFANA_PUBLIC:-}" = "1" ]; then
  echo "Grafana:     http://$PUBLIC_IP:3000"
else
  echo "Grafana:     http://127.0.0.1:3000 on the instance (SSH tunnel required)"
fi
if [ "${ACS_PROMETHEUS_PUBLIC:-}" = "1" ]; then
  echo "Prometheus:  http://$PUBLIC_IP:9090"
else
  echo "Prometheus:  http://127.0.0.1:9090 on the instance (SSH tunnel required)"
fi

echo ""
echo "Login: $ACS_BOOTSTRAP_ADMIN_USERNAME / $ACS_BOOTSTRAP_ADMIN_PASSWORD"
echo "Grafana login: admin / $GRAFANA_ADMIN_PASSWORD"
echo "USP controller ID: $ACS_USP_CONTROLLER_ID"
echo "(credentials/settings are also saved in ~/.acs-secrets.env)"
if [ "${ACS_PROMETHEUS_PUBLIC:-}" = "1" ]; then
  echo ""
echo "WARNING: Prometheus has no login in this dev mode. Restrict 9090/tcp"
echo "         in the security group to your test IP/CIDR; do not expose it"
echo "         broadly for a production deployment."
fi
echo ""
echo "Logs:   $LOG_DIR/{acs,api,bssadapter,uspc,frontend}.log"
echo "Health: scripts/healthcheck.sh"
echo "Watch:  scripts/logs.sh"
echo "Stop:   scripts/stop.sh"
echo "=================================================="
