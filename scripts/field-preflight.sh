#!/bin/bash
# Preflight for a controlled real-CPE qualification run.
#
# This does not prove a device is compatible. It proves the deployed ACS
# is healthy, records the exact build under test, and blocks a few unsafe
# mistakes before hardware is pointed at it.
#
# Plaintext USP is allowed only for an explicitly acknowledged isolated
# field/lab run:
#   ACS_FIELD_TEST_ACK=1 ./scripts/field-preflight.sh
# Production-facing USP should use TLS instead of this acknowledgement.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SECRETS_FILE="${ACS_SECRETS_FILE:-$HOME/.acs-secrets.env}"
FAILURES=0
WARNINGS=0

fail() {
  echo "FAIL: $*" >&2
  FAILURES=$((FAILURES + 1))
}

warn() {
  echo "WARN: $*" >&2
  WARNINGS=$((WARNINGS + 1))
}

ok() {
  echo "OK:   $*"
}

is_loopback_bind() {
  local addr="${1:-}"
  case "$addr" in
    127.*:*|localhost:*|\[::1\]:*) return 0 ;;
    *) return 1 ;;
  esac
}

bind_port() {
  local addr="${1:-}"
  # Works for host:port and [IPv6]:port. Callers only use this for
  # configured listener strings, never untrusted input.
  printf '%s\n' "${addr##*:}" | tr -d ']'
}

echo "=== ACS real-CPE field preflight ==="

# Record the exact software under test. A dirty worktree makes evidence
# irreproducible, so require an explicit escape hatch rather than silently
# qualifying hardware against uncommitted code.
if command -v git >/dev/null 2>&1 && git -C "$ROOT" rev-parse --is-inside-work-tree >/dev/null 2>&1; then
  COMMIT="$(git -C "$ROOT" rev-parse HEAD)"
  echo "Commit: $COMMIT"
  if [ -n "$(git -C "$ROOT" status --porcelain --untracked-files=normal)" ]; then
    if [ "${ACS_FIELD_ALLOW_DIRTY:-0}" = "1" ]; then
      warn "repository has uncommitted changes; ACS_FIELD_ALLOW_DIRTY=1 explicitly allowed this non-reproducible run"
    else
      fail "repository has uncommitted changes; commit/stash them or set ACS_FIELD_ALLOW_DIRTY=1 and record why"
    fi
  else
    ok "repository is clean"
  fi
else
  warn "git metadata unavailable; record the deployed release/commit manually"
fi

# Load the standard persisted runtime settings only after checking that the
# credential file is not group/world-accessible. We never print secrets.
if [ -f "$SECRETS_FILE" ]; then
  if command -v stat >/dev/null 2>&1; then
    MODE="$(stat -c '%a' "$SECRETS_FILE" 2>/dev/null || true)"
    if [ -n "$MODE" ]; then
      GROUP_OTHER="${MODE: -2}"
      if [ "$GROUP_OTHER" != "00" ]; then
        fail "$SECRETS_FILE permissions are $MODE; credential material must not be accessible to group/other users"
      else
        ok "$SECRETS_FILE permissions are restricted ($MODE)"
      fi
    fi
  fi
  # shellcheck disable=SC1090
  source "$SECRETS_FILE"
else
  warn "$SECRETS_FILE not found; using the current process environment"
fi

# The northbound BSS surface and the USP health/management endpoint are
# intentionally host-local in the supported quickstart. Exposing either
# directly is an avoidable management-plane risk.
BSS_BIND="${ACS_BSS_ADDR:-127.0.0.1:8090}"
USP_HTTP_BIND="${ACS_USP_HTTP_ADDR:-127.0.0.1:8092}"
if is_loopback_bind "$BSS_BIND"; then
  ok "BSS/TMF adapter management bind is loopback ($BSS_BIND)"
else
  fail "BSS/TMF adapter bind is not loopback ($BSS_BIND); put northbound access behind the controlled TLS/auth path"
fi
if is_loopback_bind "$USP_HTTP_BIND"; then
  ok "USP management/health bind is loopback ($USP_HTTP_BIND)"
else
  fail "USP management/health bind is not loopback ($USP_HTTP_BIND)"
fi

# The reachability probe never bypasses authentication, but leaving it on
# indefinitely adds noisy request telemetry. Prefer once for a single
# problem device and record the mode in field evidence.
ONBOARDING_MODE="${ACS_ONBOARDING_LISTENER:-off}"
case "$ONBOARDING_MODE" in
  off)
    ok "CWMP onboarding reachability probe is off"
    ;;
  once)
    ok "CWMP onboarding reachability probe is armed for one successful Inform"
    ;;
  on)
    warn "ACS_ONBOARDING_LISTENER=on remains enabled after onboarding; prefer 'once' for a bounded field test"
    ;;
  *)
    fail "ACS_ONBOARDING_LISTENER must be off, on or once (got '$ONBOARDING_MODE')"
    ;;
esac

# Plaintext USP can be useful when qualifying unknown devices in an
# isolated lab, but it must be a conscious field-test choice.
if [ "${ACS_USP_ALLOW_PLAINTEXT:-false}" = "true" ] && { [ -z "${ACS_USP_TLS_CERT:-}" ] || [ -z "${ACS_USP_TLS_KEY:-}" ]; }; then
  if [ "${ACS_FIELD_TEST_ACK:-0}" = "1" ]; then
    warn "USP WebSocket/MQTT is plaintext; this run is explicitly acknowledged as an isolated field/lab test"
  else
    fail "USP is plaintext. For an isolated test rerun with ACS_FIELD_TEST_ACK=1; otherwise configure ACS_USP_TLS_CERT/KEY and disable plaintext"
  fi
else
  ok "USP transport is not relying on the plaintext field-test exception"
fi

# A field environment may deliberately publish Grafana/Prometheus for an
# operator CIDR. That is a warning rather than a hard failure because the
# host cannot prove the cloud/security-group source restrictions itself.
if [ "${ACS_GRAFANA_PUBLIC:-0}" = "1" ]; then
  warn "Grafana public bind is enabled; confirm port 3000 is restricted to the operator/test CIDR"
fi
if [ "${ACS_PROMETHEUS_PUBLIC:-0}" = "1" ]; then
  warn "Prometheus public bind is enabled; confirm port 9090 is restricted to the operator/test CIDR"
fi

# Also inspect live listeners when ss is available; this catches a stale
# process/configuration whose current environment no longer reflects how
# it was started. Use the configured ports rather than assuming defaults.
if command -v ss >/dev/null 2>&1; then
  LISTENERS="$(ss -H -lnt 2>/dev/null || true)"
  BSS_PORT="$(bind_port "$BSS_BIND")"
  USP_HTTP_PORT="$(bind_port "$USP_HTTP_BIND")"
  if printf '%s\n' "$LISTENERS" | grep -Eq "(^|[[:space:]])(0\\.0\\.0\\.0|\\*|\\[::\\]):${BSS_PORT}([[:space:]]|$)"; then
    fail "live BSS adapter port $BSS_PORT appears bound on all interfaces"
  fi
  if printf '%s\n' "$LISTENERS" | grep -Eq "(^|[[:space:]])(0\\.0\\.0\\.0|\\*|\\[::\\]):${USP_HTTP_PORT}([[:space:]]|$)"; then
    fail "live USP health port $USP_HTTP_PORT appears bound on all interfaces"
  fi
fi

# Device captures are the evidence source for the qualification run. The
# health check verifies the same services the operator will use before a
# device is reconfigured to point at this ACS.
if bash "$ROOT/scripts/healthcheck.sh"; then
  ok "whole-stack readiness passed"
else
  fail "whole-stack readiness failed"
fi

if [ "$FAILURES" -ne 0 ]; then
  echo ""
  echo "Preflight failed: $FAILURES blocking issue(s), $WARNINGS warning(s)." >&2
  exit 1
fi

echo ""
echo "Preflight passed with $WARNINGS warning(s)."
echo "Record the commit above, device firmware, capture IDs and job command keys in the field evidence."
