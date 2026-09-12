#!/usr/bin/env bash
# assert-allowlist.sh proves the USP agent allowlist's identity-level gate
# (design docs/superpowers/specs/2026-09-12-usp-agent-allowlist-design.md
# S2.2; gate implemented in backend/internal/devices/usp.go's
# ReconcileFromOnBoard and wired into backend/cmd/uspc/handler.go by
# Tasks 3-4) against a real obuspa instance: BEFORE a device's identity is
# pre-registered, cmd/uspc must refuse to reconcile it, and it must not
# have silently created a devices row for it anyway.
#
# Correction over this task's original brief (see task-5-report.md for the
# full rationale): obuspa's Device.DeviceInfo.SerialNumber is
# generated/persisted at its first boot, not a fixed, predictable value
# (only ManufacturerOUI/ProductClass are fixed compile-time defaults, and
# even those have no literal value recorded anywhere in this repo -- they
# live in obuspa's own vendor_defs.h). A second, deliberately-unregistered
# obuspa instance is therefore unnecessary: the SAME obuspa instance every
# other assert-*.sh script in this job later pre-registers and proves
# acceptance against is, before that pre-registration happens, itself an
# identity cmd/uspc has never seen before -- its very first connection
# attempt(s) are refused by the gate, and that refusal is exactly what
# this script checks for. The caller (ci.yml) is responsible for calling
# this BEFORE pre-registering the device and restarting the container,
# and for extracting endpoint_id/oui_serial via obuspa's own "-c get" CLI
# (the same out-of-band-oracle pattern assert-subscription.sh already
# established for confirming agent-side state independently of cmd/uspc's
# own opinion), not a hardcoded/predicted literal.
#
# Two things are asserted, in order:
#   1. cmd/uspc's own log shows a refusal for this endpoint: a
#      logReconcileFailure line (handler.go) whose wrapped error carries
#      devices.ErrUnknownDevice's exact sentinel text (usp.go) -- matched
#      on that sentinel text alone, not on which of the two identity paths
#      (primary OnBoardRequest vs. probe fallback) produced it, since
#      which one fires is agent/config-dependent and both wrap the same
#      sentinel.
#   2. No devices row was created for this identity anyway -- the refusal
#      must not have silently onboarded it under a different code path.
#
# Usage: assert-allowlist.sh <uspc_log_file> <postgres_dsn> <endpoint_id> <oui_serial>

set -euo pipefail

USPC_LOG="${1:?usage: assert-allowlist.sh <uspc_log_file> <postgres_dsn> <endpoint_id> <oui_serial>}"
POSTGRES_DSN="${2:?usage: assert-allowlist.sh <uspc_log_file> <postgres_dsn> <endpoint_id> <oui_serial>}"
ENDPOINT_ID="${3:?usage: assert-allowlist.sh <uspc_log_file> <postgres_dsn> <endpoint_id> <oui_serial>}"
OUI_SERIAL="${4:?usage: assert-allowlist.sh <uspc_log_file> <postgres_dsn> <endpoint_id> <oui_serial>}"

fail() {
  echo "FAIL: $1" >&2
  echo "--- uspc log tail ---" >&2
  tail -n 100 "$USPC_LOG" >&2 || true
  exit 1
}

# devices.ErrUnknownDevice's exact sentinel text (backend/internal/devices/
# usp.go), verified against that file, not guessed. handler.go's
# logReconcileFailure logs it wrapped as either "reconcile onboard: <this>"
# (handleOnBoardRequest) or "reconcile probe fallback: <this>"
# (handleProbeFallback) depending on which identity path fired -- grepping
# on the sentinel text alone makes this assertion independent of which
# path this obuspa build/config actually exercises.
REFUSAL_TEXT="no devices row exists for this identity -- pre-register the device before its first USP contact"

echo "waiting for cmd/uspc to log a refusal for $ENDPOINT_ID's not-yet-registered identity..."
found=0
matches=""
for i in $(seq 1 60); do
  if [ -f "$USPC_LOG" ]; then
    matches=$(grep -F "$REFUSAL_TEXT" "$USPC_LOG" 2>/dev/null | grep -F "endpoint=$ENDPOINT_ID" || true)
    if [ -n "$matches" ]; then
      found=1
      break
    fi
  fi
  sleep 1
done
if [ "$found" -ne 1 ]; then
  fail "cmd/uspc never logged an unknown-device refusal for endpoint=$ENDPOINT_ID within 60s (want a log line containing endpoint=$ENDPOINT_ID and \"$REFUSAL_TEXT\")"
fi
echo "OK: cmd/uspc logged a refusal for $ENDPOINT_ID's unregistered identity -- the identity gate is doing its job"
echo "$matches" | tail -3

echo "confirming the refusal did not silently create a devices row for oui_serial=$OUI_SERIAL anyway"
COUNT=$(psql "$POSTGRES_DSN" -tAc "SELECT count(*) FROM devices WHERE oui_serial = '${OUI_SERIAL//\'/\'\'}';")
COUNT=$(echo "$COUNT" | tr -d '[:space:]')
if [ "$COUNT" != "0" ]; then
  fail "devices row count for oui_serial=$OUI_SERIAL = $COUNT, want 0 (a refused reconciliation must not create a row)"
fi
echo "OK: no devices row exists yet for oui_serial=$OUI_SERIAL"

echo "assert-allowlist.sh: PASS"
