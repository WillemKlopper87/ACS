#!/bin/bash
# Starts the full ACS stack (Postgres, cmd/acs, cmd/api, frontend) as
# backgrounded, nohup'd processes with PID files — so a single SSH
# session is enough, and closing the terminal or losing the connection
# doesn't kill anything. Rerun freely: stops any previous run first.
#
# The host quickstart deployment uses Nginx as one public operator ingress:
#   http://<public-ip>/             console
#   http://<public-ip>/api/...      REST API
#   http://<public-ip>/grafana/     Grafana
#   http://<public-ip>/prometheus/  Prometheus (Basic Auth)
# Grafana and Prometheus themselves remain bound to 127.0.0.1.
#
# Uses BUILT binaries (not `go run`) deliberately — `go run` wraps the
# real binary in a subprocess, and killing the wrapper can leave the real
# binary running and still holding its port.
set -e

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
LOG_DIR="$HOME/acs-logs"
mkdir -p "$LOG_DIR"

echo "=== Loading/generating credentials ==="
source "$ROOT/scripts/gen-env.sh"

echo "=== Detecting public IP ==="
# Needed before the monitoring containers and the frontend build: both
# Grafana/Prometheus external URLs and VITE_API_BASE_URL must describe the
# address the operator's browser actually uses.
detect_public_ip() {
  if [ -n "$ACS_PUBLIC_IP" ]; then
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
echo "Public IP/host: $PUBLIC_IP"

# Nginx is installed by quickstart. Preserve a functional legacy path for
# people who run start.sh on an older/manual host without Nginx: the
# console/API keep their historical :5173/:8080 public URLs and monitoring
# remains local/tunnel-only in that case.
INGRESS_AVAILABLE=""
if command -v nginx >/dev/null 2>&1; then
  INGRESS_AVAILABLE=1
  PUBLIC_ORIGIN="http://$PUBLIC_IP"
  FRONTEND_API_ORIGIN="$PUBLIC_ORIGIN"
else
  PUBLIC_ORIGIN="http://$PUBLIC_IP"
  FRONTEND_API_ORIGIN="http://$PUBLIC_IP:8080"
  echo "WARNING: nginx is not installed; unified /grafana and /prometheus URLs will not be configured."
  echo "         Run scripts/quickstart.sh (recommended) or install nginx and rerun."
fi

# docker compose reads infra/.env. gen-env.sh creates it before we know the
# public IP, so update deployment-specific values now. Values here contain
# no literal '|' characters, making it a safe sed delimiter.
set_compose_env() {
  local key="$1" value="$2" file="$ROOT/infra/.env"
  if grep -q "^${key}=" "$file"; then
    sed -i "s|^${key}=.*|${key}=${value}|" "$file"
  else
    echo "${key}=${value}" >> "$file"
  fi
}

if [ "$ACS_GRAFANA_PUBLIC" = "1" ]; then
  GRAFANA_BIND="0.0.0.0"
else
  GRAFANA_BIND="127.0.0.1"
fi
set_compose_env GRAFANA_BIND "$GRAFANA_BIND"

if [ -n "$INGRESS_AVAILABLE" ]; then
  set_compose_env GRAFANA_ROOT_URL "$PUBLIC_ORIGIN/grafana/"
  set_compose_env GRAFANA_SERVE_FROM_SUB_PATH "true"
  set_compose_env PROMETHEUS_EXTERNAL_URL "$PUBLIC_ORIGIN/prometheus/"
  set_compose_env PROMETHEUS_ROUTE_PREFIX "/prometheus/"
else
  set_compose_env GRAFANA_ROOT_URL "http://localhost:3000"
  set_compose_env GRAFANA_SERVE_FROM_SUB_PATH "false"
  set_compose_env PROMETHEUS_EXTERNAL_URL "http://localhost:9090"
  set_compose_env PROMETHEUS_ROUTE_PREFIX "/"
fi

echo "=== Stopping any previous ACS processes ==="
"$ROOT/scripts/stop.sh" || true
sleep 1

echo "=== Starting Postgres ==="
(cd "$ROOT/infra" && docker compose up -d postgres)
echo "Waiting for Postgres to accept connections..."
until docker exec infra-postgres-1 pg_isready -U acs >/dev/null 2>&1; do sleep 1; done
echo "Postgres is up."

# --- Monitoring stack -------------------------------------------------
echo "=== Grafana's read-only database role ==="
# The Postgres-backed dashboards query through a SELECT-only role, never
# the application's own credentials. Creating it is idempotent.
if command -v psql >/dev/null; then
  "$ROOT/scripts/grafana-db-role.sh" || echo "WARNING: grafana-db-role.sh failed — the Postgres-backed dashboards will show auth errors."
else
  echo "WARNING: psql not found, skipping the grafana_ro role."
  echo "         The Prometheus dashboards still work; the Postgres-backed ones"
  echo "         won't until you 'sudo apt-get install -y postgresql-client' and rerun."
fi

echo "=== Starting monitoring stack (Prometheus, Alertmanager, Grafana) ==="
(cd "$ROOT/infra" && docker compose up -d prometheus alertmanager grafana)

echo "Waiting for Prometheus to become ready..."
PROMETHEUS_UP=""
PROM_READY_PATH="/-/ready"
if [ -n "$INGRESS_AVAILABLE" ]; then
  PROM_READY_PATH="/prometheus/-/ready"
fi
for _ in $(seq 1 60); do
  if curl -fsS -m 2 "http://127.0.0.1:9090${PROM_READY_PATH}" >/dev/null 2>&1; then
    PROMETHEUS_UP=1
    break
  fi
  sleep 2
done
if [ -n "$PROMETHEUS_UP" ]; then
  echo "Prometheus is up."
else
  echo "WARNING: Prometheus did not report ready within 120s."
  echo "         Check: cd infra && docker compose logs prometheus"
fi

echo "Waiting for Grafana to become healthy..."
GRAFANA_UP=""
GRAFANA_HEALTH_PATH="/api/health"
if [ -n "$INGRESS_AVAILABLE" ]; then
  GRAFANA_HEALTH_PATH="/grafana/api/health"
fi
for _ in $(seq 1 60); do
  if curl -fsS -m 2 "http://127.0.0.1:3000${GRAFANA_HEALTH_PATH}" 2>/dev/null | grep -q '"database": *"ok"'; then
    GRAFANA_UP=1
    break
  fi
  sleep 2
done
if [ -n "$GRAFANA_UP" ]; then
  echo "Grafana is up."
else
  # Not fatal: the ACS itself does not depend on dashboards.
  echo "WARNING: Grafana did not report healthy within 120s."
  echo "         Check: cd infra && docker compose logs grafana"
fi

echo "=== Building backend binaries ==="
cd "$ROOT/backend"
mkdir -p bin
go build -o bin/acs ./cmd/acs
go build -o bin/api ./cmd/api

echo "=== Starting cmd/acs (CWMP :7547, STUN :3478) ==="
nohup "$ROOT/backend/bin/acs" > "$LOG_DIR/acs.log" 2>&1 &
echo $! > "$LOG_DIR/acs.pid"
sleep 1
if ! kill -0 "$(cat "$LOG_DIR/acs.pid")" 2>/dev/null; then
  echo "cmd/acs failed to start — check $LOG_DIR/acs.log"; tail -20 "$LOG_DIR/acs.log"; exit 1
fi

echo "=== Starting cmd/api (REST :8080) ==="
nohup "$ROOT/backend/bin/api" > "$LOG_DIR/api.log" 2>&1 &
echo $! > "$LOG_DIR/api.pid"
sleep 1
if ! kill -0 "$(cat "$LOG_DIR/api.pid")" 2>/dev/null; then
  echo "cmd/api failed to start — check $LOG_DIR/api.log"; tail -20 "$LOG_DIR/api.log"; exit 1
fi

echo "=== Building frontend ==="
cd "$ROOT/frontend"
# With Nginx the browser talks to the same origin for both UI and API.
# On the legacy no-Nginx path retain the historical direct :8080 API.
echo "VITE_API_BASE_URL=$FRONTEND_API_ORIGIN" > .env.local
npm install --silent
npm run build

echo "=== Starting frontend static server (:5173) ==="
cd "$ROOT/frontend/dist"
nohup python3 "$ROOT/scripts/spa-server.py" 5173 "$ROOT/frontend/dist" "$FRONTEND_API_ORIGIN" > "$LOG_DIR/frontend.log" 2>&1 &
echo $! > "$LOG_DIR/frontend.pid"
sleep 1
if ! kill -0 "$(cat "$LOG_DIR/frontend.pid")" 2>/dev/null; then
  echo "frontend server failed to start — check $LOG_DIR/frontend.log"; tail -20 "$LOG_DIR/frontend.log"; exit 1
fi

INGRESS_UP=""
if [ -n "$INGRESS_AVAILABLE" ]; then
  echo "=== Configuring public Nginx ingress (:80) ==="
  # Use bash explicitly because a file created through GitHub's contents
  # API may not carry an executable bit until quickstart's chmod step.
  if bash "$ROOT/scripts/configure-web-ingress.sh"; then
    INGRESS_UP=1
    if ! curl -fsS -m 5 "http://127.0.0.1/" >/dev/null 2>&1; then
      echo "WARNING: Nginx reloaded, but the console did not answer through port 80."
    fi
    if ! curl -fsS -m 5 "http://127.0.0.1/grafana/api/health" >/dev/null 2>&1; then
      echo "WARNING: Grafana did not answer through the /grafana/ ingress path."
    fi
    if ! curl -fsS -m 5 -u "admin:$GRAFANA_ADMIN_PASSWORD" "http://127.0.0.1/prometheus/-/ready" >/dev/null 2>&1; then
      echo "WARNING: Prometheus did not answer through the authenticated /prometheus/ ingress path."
    fi
  else
    echo "WARNING: Nginx ingress configuration failed. ACS processes are running, but use the legacy direct URLs below."
  fi
fi

echo ""
echo "=================================================="
echo "  ACS is running"
echo "=================================================="
CWMP_SCHEME="http"
if [ -n "$ACS_TLS_CERT" ] && [ -n "$ACS_TLS_KEY" ]; then
  CWMP_SCHEME="https"
fi

if [ -n "$INGRESS_UP" ]; then
  echo "Console:     $PUBLIC_ORIGIN/"
  echo "API:         $PUBLIC_ORIGIN/api/v1/..."
  echo "Grafana:     $PUBLIC_ORIGIN/grafana/"
  echo "Prometheus:  $PUBLIC_ORIGIN/prometheus/"
  echo ""
  echo "Grafana login:    admin / $GRAFANA_ADMIN_PASSWORD"
  echo "Prometheus login: admin / $GRAFANA_ADMIN_PASSWORD"
  echo "Only port 80 is needed for these operator web surfaces; 3000 and 9090"
  echo "remain bound to localhost and should stay closed in the security group."
else
  echo "Console:     http://$PUBLIC_IP:5173"
  echo "API:         http://$PUBLIC_IP:8080"
  if [ "$ACS_GRAFANA_PUBLIC" = "1" ]; then
    echo "Grafana:     http://$PUBLIC_IP:3000 (direct-public legacy mode)"
  else
    echo "Grafana:     http://127.0.0.1:3000 on the instance (SSH tunnel required)"
  fi
  echo "Prometheus:  http://127.0.0.1:9090 on the instance (SSH tunnel required)"
  echo "Grafana login: admin / $GRAFANA_ADMIN_PASSWORD"
fi

echo "CWMP URL:    $CWMP_SCHEME://$PUBLIC_IP:7547/cwmp"
echo "STUN:        $PUBLIC_IP:3478 (UDP)"
echo ""
echo "ACS login: $ACS_BOOTSTRAP_ADMIN_USERNAME / $ACS_BOOTSTRAP_ADMIN_PASSWORD"
echo "(credentials are also saved in ~/.acs-secrets.env)"
echo ""
echo "Logs:   $LOG_DIR/{acs,api,frontend}.log"
echo "Watch:  scripts/logs.sh"
echo "Stop:   scripts/stop.sh"
echo "=================================================="
