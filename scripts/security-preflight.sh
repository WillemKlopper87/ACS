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

# --- Operator and integration plane (docs/SECURITY-ASSESSMENT-2026-11-14.md, O1).
# cmd/api and cmd/bssadapter enforce the same rules at startup
# (backend/internal/config/production.go); checking them here reports every
# problem at once, before anything is started.

if [ "${ACS_INSECURE_DEV_MODE:-}" = "true" ]; then
  fail "ACS_INSECURE_DEV_MODE=true is forbidden in production"
fi

# The switch between running on its own and through the NaaS platform.
# Unset means naas, as before the switch existed. Standalone asks for none
# of the adapter's settings: it answers 503 and runs no workers.
integration_mode="$(printf '%s' "${ACS_INTEGRATION_MODE:-naas}" | tr -d '[:space:]' | tr '[:upper:]' '[:lower:]')"
case "$integration_mode" in
  naas|standalone) ;;
  *) fail "ACS_INTEGRATION_MODE must be 'naas' or 'standalone' (got '${ACS_INTEGRATION_MODE:-}')" ;;
esac

if [ "$integration_mode" = "naas" ]; then
# The BSS adapter: customer-bound OAuth clients only.
require_nonempty ACS_BSS_OAUTH_SIGNING_SECRET
if [ -n "${ACS_BSS_API_TOKEN:-}" ]; then
  fail "ACS_BSS_API_TOKEN must be unset in production: the shared token grants every scope on every account"
fi
if [ "${ACS_BSS_REQUIRE_CUSTOMER_BOUND_CLIENTS:-}" != "true" ]; then
  fail "ACS_BSS_REQUIRE_CUSTOMER_BOUND_CLIENTS=true is required in production"
fi

# ...reachable only from the integration platform's networks.
if [ -z "${ACS_BSS_ALLOWED_CIDRS:-}" ]; then
  fail "ACS_BSS_ALLOWED_CIDRS is required in production: list the networks the BSS/NaaS platform calls from"
else
  IFS=',' read -r -a bss_cidrs <<< "$ACS_BSS_ALLOWED_CIDRS"
  for raw in "${bss_cidrs[@]}"; do
    cidr="${raw//[[:space:]]/}"
    case "$cidr" in
      */0)
        fail "ACS_BSS_ALLOWED_CIDRS must not contain universal network $cidr in production"
        ;;
      */*) ;;
      *)
        fail "ACS_BSS_ALLOWED_CIDRS entry '$cidr' is not a CIDR (address/prefix)"
        ;;
    esac
  done
fi
# The adapter sits on loopback behind the HTTPS ingress (checked above), so
# the ingress must be trusted to report the caller, or every caller looks
# like the ingress and the allowlist cannot be applied.
if [ -z "${ACS_TRUSTED_PROXY_CIDRS:-}" ]; then
  fail "ACS_TRUSTED_PROXY_CIDRS is required in production: name the ingress proxy so the adapter sees the real caller address"
fi
fi # integration_mode = naas

# Operators: two-step sign-in for every role that can change devices.
mfa_roles=",$(printf '%s' "${ACS_MFA_REQUIRED_ROLES:-}" | tr -d '[:space:]' | tr '[:upper:]' '[:lower:]'),"
case "$mfa_roles" in
  *,all,*) ;;
  *)
    for role in superadmin manager noc; do
      case "$mfa_roles" in
        *",$role,"*) ;;
        *) fail "ACS_MFA_REQUIRED_ROLES must include $role in production (set it to superadmin,manager,noc or all)" ;;
      esac
    done
    ;;
esac
require_nonempty ACS_CREDENTIAL_ENCRYPTION_KEY
if [ "${ACS_WEBGUI_ALLOW_SAME_ORIGIN:-}" = "true" ]; then
  fail "ACS_WEBGUI_ALLOW_SAME_ORIGIN=true is forbidden in production: it lets a device's web pages run on the console's origin"
fi

if [ "$errors" -ne 0 ]; then
  echo "Security preflight FAILED with $errors problem(s)." >&2
  exit 1
fi

echo "Security preflight passed: production device transports and operator ingress requirements are configured."
echo "Operator console/API: one HTTPS public origin -> loopback-only host listeners."
echo "BSS adapter, USP health/metrics, Grafana and Prometheus remain host-local in this production profile."
if [ "$integration_mode" = "naas" ]; then
  echo "BSS adapter: customer-bound OAuth clients only, no shared token, calls accepted from ACS_BSS_ALLOWED_CIDRS only."
else
  echo "Integration mode: standalone. The BSS/NaaS adapter is switched off (503 on every integration route)."
fi
echo "Operators: two-step sign-in required for ${ACS_MFA_REQUIRED_ROLES}."
echo "Note: production CWMP rejects the shared fleet Digest username; provision per-device Digest credentials or mTLS before connecting established CPEs."
echo "Note: production USP requires each agent certificate to be pre-bound to its device, EndpointID, and MQTT response topic in usp_transport_principals."
