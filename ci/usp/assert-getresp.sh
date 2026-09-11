#!/usr/bin/env bash
# Poll cmd/uspc's log for evidence that a given agent answered the
# interop probe's Get(["Device.DeviceInfo."]) over a given MTP.
#
# cmd/uspc's probe (backend/cmd/uspc/probe.go, probe.handle) logs one
# "usp probe: parameter" line per resolved parameter on a successful
# GetResp, via slog's TextHandler. A matching line looks like:
#
#   time=... level=INFO msg="usp probe: parameter" endpoint=os::012345-CIAGENT mtp=WebSocket param_path=Device.DeviceInfo.SoftwareVersion value=11.0.0
#
# so this asserts on that message, the agent's endpoint id, the MTP
# name, and Device.DeviceInfo.SoftwareVersion specifically (a parameter
# every obuspa build reports, from AGENT_SOFTWARE_VERSION) -- proof the
# agent connected, was accepted (an unprovisioned controller id would
# never reach this line: OnRecord's usp.DecodeRecord checks To first),
# and returned real device-model data, not just an empty or error
# response.
#
# Prints both logs and fails on timeout, so a red CI step shows what
# each side said rather than just "timed out".
set -euo pipefail

log="$1"; agent="$2"; mtp="$3"; obuspa_log="${4:-}"

for _ in $(seq 1 60); do
  if [ -f "$log" ] \
     && grep -q 'msg="usp probe: parameter"' "$log" \
     && grep -q "endpoint=$agent" "$log" \
     && grep -q "mtp=$mtp" "$log" \
     && grep -q "param_path=Device.DeviceInfo.SoftwareVersion" "$log"; then
    echo "OK: $agent answered the probe Get over $mtp"
    grep 'msg="usp probe: parameter"' "$log" | grep "endpoint=$agent" | grep "mtp=$mtp" | tail -5
    exit 0
  fi
  sleep 1
done

echo "FAIL: no GetResp evidence from $agent over $mtp within 60s"
echo "--- uspc log ($log) ---"
cat "$log" 2>/dev/null || echo "(missing)"
if [ -n "$obuspa_log" ]; then
  echo "--- obuspa log ($obuspa_log) ---"
  cat "$obuspa_log" 2>/dev/null || echo "(missing)"
fi
exit 1
