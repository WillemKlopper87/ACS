#!/bin/bash
# Starts the full ACS stack (Postgres, cmd/acs, cmd/api, frontend) as
# backgrounded, nohup'd processes with PID files — so a single SSH
# session is enough, and closing the terminal or losing the connection
# doesn't kill anything. Rerun freely: stops any previous run first.
#
# Uses BUILT binaries (not `go run`) deliberately — `go run` wraps the
# real binary in a subprocess, and killing the wrapper can leave the
# actual binary running and still holding its port.
set -e

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
LOG_DIR="$HOME/acs-logs"
mkdir -p "$LOG_DIR"

echo "=== Loading/generating credentials ==="
source "$ROOT/scripts/gen-env.sh"

echo "=== Detecting public IP ==="
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
echo "Public IP: $PUBLIC_IP"
# The API uses this value for its CORS allow-origin. Keep it derived from the
# same address used by the frontend so an EC2 stop/start with a new public IP
# cannot leave the API serving a stale browser origin from the secrets file.
export ACS_FRONTEND_BASE_URL="http://$PUBLIC_IP:5173"

# docker compose reads infra/.env. gen-env.sh creates it before the public
# IP is known, so update run-specific bind/root values here.
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
for _ in $(seq 1 60); do
  if curl -fsS -m 2 http://127.0.0.1:9090/-/ready >/dev/null 2>&1; then
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
for _ in $(seq 1 60); do
  if curl -fsS -m 2 http://127.0.0.1:3000/api/health 2>/dev/null | grep -q '"database": *"ok"'; then
    GRAFANA_UP=1
    break
  fi
  sleep 2
done
if [ -n "$GRAFANA_UP" ]; then
  echo "Grafana is up."
else
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
echo "VITE_API_BASE_URL=http://$PUBLIC_IP:8080" > .env.local
npm install --silent
npm run build

echo "=== Starting frontend static server (:5173) ==="
cd "$ROOT/frontend/dist"
nohup python3 "$ROOT/scripts/spa-server.py" 5173 "$ROOT/frontend/dist" "http://$PUBLIC_IP:8080" > "$LOG_DIR/frontend.log" 2>&1 &
echo $! > "$LOG_DIR/frontend.pid"
sleep 1
if ! kill -0 "$(cat "$LOG_DIR/frontend.pid")" 2>/dev/null; then
  echo "frontend server failed to start — check $LOG_DIR/frontend.log"; tail -20 "$LOG_DIR/frontend.log"; exit 1
fi

echo ""
echo "=================================================="
echo "  ACS is running"
echo "=================================================="
CWMP_SCHEME="http"
if [ -n "$ACS_TLS_CERT" ] && [ -n "$ACS_TLS_KEY" ]; then
  CWMP_SCHEME="https"
fi
echo "Console:    http://$PUBLIC_IP:5173"
echo "API:        http://$PUBLIC_IP:8080"
echo "CWMP URL:   $CWMP_SCHEME://$PUBLIC_IP:7547/cwmp"
echo "STUN:       $PUBLIC_IP:3478 (UDP)"

if [ "${ACS_GRAFANA_PUBLIC:-}" = "1" ]; then
  echo "Grafana:    http://$PUBLIC_IP:3000"
else
  echo "Grafana:    http://127.0.0.1:3000 on the instance (SSH tunnel required)"
fi
if [ "${ACS_PROMETHEUS_PUBLIC:-}" = "1" ]; then
  echo "Prometheus: http://$PUBLIC_IP:9090"
else
  echo "Prometheus: http://127.0.0.1:9090 on the instance (SSH tunnel required)"
fi

echo ""
echo "Login: $ACS_BOOTSTRAP_ADMIN_USERNAME / $ACS_BOOTSTRAP_ADMIN_PASSWORD"
echo "Grafana login: admin / $GRAFANA_ADMIN_PASSWORD"
echo "(credentials are also saved in ~/.acs-secrets.env)"
if [ "${ACS_PROMETHEUS_PUBLIC:-}" = "1" ]; then
  echo ""
  echo "WARNING: Prometheus has no login in this dev mode. Restrict 9090/tcp"
  echo "         in the security group to your test IP/CIDR; do not expose it"
  echo "         broadly for a production deployment."
fi
echo ""
echo "Logs:   $LOG_DIR/{acs,api,frontend}.log"
echo "Watch:  scripts/logs.sh"
echo "Stop:   scripts/stop.sh"
echo "=================================================="
