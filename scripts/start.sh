#!/bin/bash
# Starts the full ACS stack (Postgres, cmd/acs, cmd/api, frontend) as
# backgrounded, nohup'd processes with PID files — so a single SSH
# session is enough, and closing the terminal or losing the connection
# doesn't kill anything. Rerun freely: stops any previous run first.
#
# Uses BUILT binaries (not `go run`) deliberately — `go run` wraps the
# real binary in a subprocess, and killing the wrapper (e.g. via Ctrl+Z
# then losing track of the job) can leave the actual binary running and
# still holding its port. A built binary has no such wrapper.
set -e

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
LOG_DIR="$HOME/acs-logs"
mkdir -p "$LOG_DIR"

echo "=== Loading/generating credentials ==="
source "$ROOT/scripts/gen-env.sh"

echo "=== Stopping any previous ACS processes ==="
"$ROOT/scripts/stop.sh" || true
sleep 1

echo "=== Starting Postgres ==="
(cd "$ROOT/infra" && docker compose up -d postgres)
echo "Waiting for Postgres to accept connections..."
until docker exec infra-postgres-1 pg_isready -U acs >/dev/null 2>&1; do sleep 1; done
echo "Postgres is up."

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

echo "=== Detecting public IP ==="
# Needed BEFORE the frontend build, not after: VITE_API_BASE_URL gets
# baked into the compiled JS bundle at build time and then evaluated in
# the *visitor's* browser. A fallback of "http://localhost:8080" means
# "localhost" resolves to whoever's laptop loaded the page — never this
# server — which is exactly why the console showed "Failed to reach the
# API": the bundle had no real address for cmd/api at all.
detect_public_ip() {
  if [ -n "$ACS_PUBLIC_IP" ]; then
    echo "$ACS_PUBLIC_IP"
    return
  fi
  # IMDSv2 first — required on current-generation EC2 AMIs (a plain GET
  # against the metadata endpoint gets silently rejected without a
  # session token, and `curl -s` swallows the failure as empty output
  # rather than an error, so this used to "succeed" with nothing).
  # `|| true` on every curl below is deliberate: off of EC2 (or with IMDS
  # unreachable) these fail fast with connection-refused, and under
  # `set -e` an unguarded failure here kills the whole script right here
  # — silently, before the empty-PUBLIC_IP check below ever runs, and
  # before it can print the "set ACS_PUBLIC_IP=..." hint. We only care
  # about captured stdout, never curl's own exit status.
  local token
  token="$(curl -s -m 2 -X PUT "http://169.254.169.254/latest/api/token" -H "X-aws-ec2-metadata-token-ttl-seconds: 21600" 2>/dev/null)" || true
  if [ -n "$token" ]; then
    curl -s -m 2 -H "X-aws-ec2-metadata-token: $token" http://169.254.169.254/latest/meta-data/public-ipv4 2>/dev/null || true
    return
  fi
  # IMDSv1 fallback, for instances/AMIs that still allow it.
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

echo "=== Building frontend ==="
cd "$ROOT/frontend"
echo "VITE_API_BASE_URL=http://$PUBLIC_IP:8080" > .env.local
npm install --silent
npm run build

echo "=== Starting frontend static server (:5173) ==="
# Plain python3 http.server — no npm global-install/sudo dance, and
# python3 is already on every stock Ubuntu image.
cd "$ROOT/frontend/dist"
nohup python3 -m http.server 5173 > "$LOG_DIR/frontend.log" 2>&1 &
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
echo "Console:   http://$PUBLIC_IP:5173"
echo "API:       http://$PUBLIC_IP:8080"
echo "CWMP URL:  $CWMP_SCHEME://$PUBLIC_IP:7547/cwmp"
echo "STUN:      $PUBLIC_IP:3478 (UDP)"
echo ""
echo "Login: $ACS_BOOTSTRAP_ADMIN_USERNAME / $ACS_BOOTSTRAP_ADMIN_PASSWORD"
echo "(credentials are also saved in ~/.acs-secrets.env)"
echo ""
echo "Logs:   $LOG_DIR/{acs,api,frontend}.log"
echo "Watch:  scripts/logs.sh"
echo "Stop:   scripts/stop.sh"
echo "=================================================="
