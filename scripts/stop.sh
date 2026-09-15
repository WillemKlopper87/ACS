#!/bin/bash
# Stops every host process scripts/start.sh starts, using PID files first
# and then a conservative stray-process cleanup for pre-PID deployments.
LOG_DIR="$HOME/acs-logs"

for name in acs api bssadapter uspc frontend; do
  PID_FILE="$LOG_DIR/$name.pid"
  if [ -f "$PID_FILE" ]; then
    PID=$(cat "$PID_FILE")
    if kill -0 "$PID" 2>/dev/null; then
      kill "$PID" 2>/dev/null
      echo "Stopped $name (pid $PID)"
    else
      echo "$name (pid $PID) was not running"
    fi
    rm -f "$PID_FILE"
  fi
done

# Belt-and-suspenders: catch anything from an old `go run` invocation
# or an interrupted deployment that a PID file would not know about.
for name in acs api bssadapter uspc; do
  pkill -f "backend/bin/$name" 2>/dev/null && echo "Killed a stray bin/$name process" || true
  pkill -f "go run ./cmd/$name" 2>/dev/null && echo "Killed a stray 'go run ./cmd/$name' process" || true
done
pkill -f "scripts/spa-server.py" 2>/dev/null && echo "Killed a stray frontend server" || true
pkill -f "http.server 5173" 2>/dev/null && echo "Killed a stray old-style frontend server" || true

# Durable state/monitoring containers are deliberately left running.
echo "Done. (Postgres + Prometheus/Alertmanager/Grafana containers left running;"
echo " 'cd infra && docker compose stop' stops those as well.)"
