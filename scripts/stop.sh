#!/bin/bash
# Stops everything scripts/start.sh started, using the PID files it
# wrote — avoids the "port already in use" problem that comes from
# losing track of a `go run` process after a stray Ctrl+Z.
LOG_DIR="$HOME/acs-logs"

for name in acs api frontend; do
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
# (before this script existed) that a PID file wouldn't know about.
pkill -f "backend/bin/acs" 2>/dev/null && echo "Killed a stray bin/acs process" || true
pkill -f "backend/bin/api" 2>/dev/null && echo "Killed a stray bin/api process" || true
pkill -f "go run ./cmd/acs" 2>/dev/null && echo "Killed a stray 'go run ./cmd/acs' process" || true
pkill -f "go run ./cmd/api" 2>/dev/null && echo "Killed a stray 'go run ./cmd/api' process" || true

echo "Done."
