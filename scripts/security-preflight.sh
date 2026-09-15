#!/bin/bash
# Validate the management-plane security settings before a production start.
# The host quickstart intentionally remains a lab workflow; operators choosing
# ACS_DEPLOYMENT_PROFILE=production must satisfy these fail-closed checks.
set -euo pipefail

profile="${ACS_DEPLOYMENT_PROFILE:-lab}"

if [ "$profile" = "lab" ]; then
  echo "Security preflight: LAB profile — plaintext/compatibility mechanisms may be enabled."
  echo "Do not treat this profile as production-ready."
  exit 0
fi

if [ "$profile" != "production" ]; then
  echo "ERROR: ACS_DEPLOYMENT_PROFILE must be 'lab' or 'production' (got '$profile')." >&2
  exit 1
fi

errors=0
fail() {
  echo "ERROR: $*" >&2
  errors=$((errors + 1))
}

require_nonempty() {
  local name="$1" value="${!1:-}"
  if [ -z "$value" ]; then
    fail "$name is required when ACS_DEPLOYMENT_PROFILE=production"
  fi
}

require_https_origin() {
  local name="$1" value="${!1:-}"
  if [ -z "$value" ]; then
    fail "$name is required when ACS_DEPLOYMENT_PROFILE=production"
    return
  fi
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
    fail "$name must be a valid HTTPS origin with no path/query/fragment (got '$value')"
  fi
}

require_loopback_socket() {
  local name="$1" value="${!1:-}"
  case "$value" in
    127.0.0.1:*|localhost:*|'[::1]':*) ;;
    *) fail "$name must bind loopback in production (got '${value:-<unset>}')" ;;
  esac
}

require_nonempty ACS_TLS_CERT
require_nonempty ACS_TLS_KEY
require_nonempty ACS_USP_TLS_CERT
require_nonempty ACS_USP_TLS_KEY
require_nonempty ACS_USP_CLIENT_CA_CERT
require_nonempty ACS_USP_ALLOWED_CIDRS
require_https_origin ACS_FRONTEND_BASE_URL
require_https_origin ACS_API_PUBLIC_URL

# The current host console/API do not implement a browser CORS trust layer.
# Keep the supported production topology same-origin and let one HTTPS ingress
# route API paths to the loopback API upstream and all other console traffic to
# the loopback SPA upstream. A separate API origin requires its own reviewed
# CORS design and is deliberately not implied by this launcher.
frontend_origin="${ACS_FRONTEND_BASE_URL%/}"
api_origin="${ACS_API_PUBLIC_URL%/}"
if [ -n "$frontend_origin" ] && [ -n "$api_origin" ] && [ "$frontend_origin" != "$api_origin" ]; then
  fail "ACS_API_PUBLIC_URL must equal ACS_FRONTEND_BASE_URL in the supported host production profile (same-origin HTTPS ingress required)"
fi

# Every plain-HTTP management/control listener started by the host launcher is
# an upstream, never an externally reachable production endpoint. Enforce this
# even if ~/.acs-secrets.env was manually edited away from its safe defaults.
require_loopback_socket ACS_API_ADDR
require_loopback_socket ACS_BSS_ADDR
require_loopback_socket ACS_USP_HTTP_ADDR
case "${ACS_FRONTEND_BIND:-}" in
  127.0.0.1|localhost|::1) ;;
  *) fail "ACS_FRONTEND_BIND must be loopback in production (got '${ACS_FRONTEND_BIND:-<unset>}')" ;;
esac

# Monitoring is intentionally host-local in this production launcher. Grafana
# and especially Prometheus must not be turned into additional cleartext public
# management surfaces by compatibility flags.
if [ "${ACS_GRAFANA_PUBLIC:-0}" = "1" ]; then
  fail "ACS_GRAFANA_PUBLIC=1 is forbidden in production; publish it only through separately hardened HTTPS ingress"
fi
if [ "${ACS_PROMETHEUS_PUBLIC:-0}" = "1" ]; then
  fail "ACS_PROMETHEUS_PUBLIC=1 is forbidden in production; Prometheus has no login in the host quickstart"
fi

# cmd/acs normalizes an unset production floor to TLS 1.2. Surface the same
# rule here so field preflight cannot report success for an explicitly weak
# 1.0/1.1 compatibility setting. Those versions remain available in lab.
cwmp_tls_min="${ACS_TLS_MIN_VERSION:-1.2}"
case "$cwmp_tls_min" in
  1.2|1.3) ;;
  *) fail "ACS_TLS_MIN_VERSION must be 1.2 or 1.3 in production (effective value: $cwmp_tls_min)" ;;
esac

if [ "${ACS_USP_ALLOW_PLAINTEXT:-false}" = "true" ]; then
  fail "ACS_USP_ALLOW_PLAINTEXT=true is forbidden in production"
fi

case "${ACS_AUTH_ALLOW_BASIC:-}" in
  1|true|TRUE|yes|on)
    fail "ACS_AUTH_ALLOW_BASIC is forbidden in production"
    ;;
esac

if [ -n "${ACS_USP_ALLOWED_CIDRS:-}" ]; then
  IFS=',' read -r -a cidrs <<< "$ACS_USP_ALLOWED_CIDRS"
  for raw in "${cidrs[@]}"; do
    cidr="${raw//[[:space:]]/}"
    case "$cidr" in
      0.0.0.0/0|::/0)
        fail "ACS_USP_ALLOWED_CIDRS must not contain universal network $cidr in production"
        ;;
    esac
  done
fi

for pair in "ACS_TLS_CERT ACS_TLS_KEY" "ACS_USP_TLS_CERT ACS_USP_TLS_KEY"; do
  read -r cert_var key_var <<< "$pair"
  cert="${!cert_var:-}"
  key="${!key_var:-}"
  if [ -n "$cert" ] && [ ! -r "$cert" ]; then
    fail "$cert_var points to an unreadable file: $cert"
  fi
  if [ -n "$key" ] && [ ! -r "$key" ]; then
    fail "$key_var points to an unreadable file: $key"
  fi
done

if [ -n "${ACS_USP_CLIENT_CA_CERT:-}" ] && [ ! -r "$ACS_USP_CLIENT_CA_CERT" ]; then
  fail "ACS_USP_CLIENT_CA_CERT points to an unreadable file: $ACS_USP_CLIENT_CA_CERT"
fi

if [ "$errors" -ne 0 ]; then
  echo "Security preflight FAILED with $errors problem(s)." >&2
  exit 1
fi

echo "Security preflight passed: production device transports and operator ingress requirements are configured."
echo "Operator console/API: one HTTPS public origin -> loopback-only host listeners."
echo "BSS adapter, USP health/metrics, Grafana and Prometheus remain host-local in this production profile."
echo "Note: production CWMP rejects the shared fleet Digest username; provision per-device Digest credentials or mTLS before connecting established CPEs."
echo "Note: production USP requires each agent certificate to be pre-bound to its device, EndpointID, and MQTT response topic in usp_transport_principals."
