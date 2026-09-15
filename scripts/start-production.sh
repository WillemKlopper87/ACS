#!/bin/bash
# Production entry point for the host-based ACS stack.
#
# The device-facing CWMP/USP listeners terminate their own TLS. The operator
# console/API use the host quickstart's plain-HTTP servers only as loopback
# upstreams and MUST be published through one real HTTPS reverse proxy/load
# balancer origin. Configure the same public origin for both values before
# running this wrapper; the ingress routes API paths to 127.0.0.1:8080 and SPA
# traffic to 127.0.0.1:5173:
#
#   ACS_FRONTEND_BASE_URL=https://acs.example.com
#   ACS_API_PUBLIC_URL=https://acs.example.com
#
# A separate browser API origin is deliberately not supported by this host
# launcher because cmd/api does not currently define a reviewed CORS trust
# policy. Add that as a separate security-reviewed change rather than widening
# the production trust boundary implicitly.
#
# Also configure the CWMP/USP TLS certificate/key paths,
# ACS_USP_CLIENT_CA_CERT, and the restrictive USP CIDR allowlist in the
# environment or ~/.acs-secrets.env. Each production USP agent must have a
# pre-provisioned certificate-fingerprint -> device/EndpointID/MQTT-topic
# binding in usp_transport_principals. The normal scripts/start.sh remains the
# compatibility-first lab/field-test quickstart.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

export ACS_DEPLOYMENT_PROFILE=production

# gen-env.sh is intentionally authoritative for stable generated secrets, but
# it also contains lab-friendly transport defaults. Preserve production values
# that the operator explicitly exported before this wrapper was invoked, then
# restore them after sourcing the stable file. This makes both supported forms
# above real: edit ~/.acs-secrets.env OR supply environment overrides for a
# particular deployment.
declare -A production_overrides=()
for var in \
  ACS_TLS_CERT ACS_TLS_KEY ACS_TLS_MIN_VERSION ACS_MTLS_CA_CERT ACS_AUTH_ALLOW_BASIC \
  ACS_USP_TLS_CERT ACS_USP_TLS_KEY ACS_USP_CLIENT_CA_CERT ACS_USP_ALLOWED_CIDRS \
  ACS_USP_ALLOW_PLAINTEXT ACS_FRONTEND_BASE_URL ACS_API_PUBLIC_URL ACS_PUBLIC_IP \
  ACS_GRAFANA_PUBLIC ACS_PROMETHEUS_PUBLIC; do
  if [ "${!var+x}" = "x" ]; then
    production_overrides["$var"]="${!var}"
  fi
done

# Load/generate stable secrets exactly once. start.sh normally sources the same
# file itself, but that second source would overwrite the explicit production
# values restored below with persisted lab defaults. ACS_ENV_ALREADY_LOADED is
# the handoff contract telling start.sh to use this already-resolved environment.
# shellcheck disable=SC1091
source "$ROOT/scripts/gen-env.sh"

for var in "${!production_overrides[@]}"; do
  export "$var=${production_overrides[$var]}"
done

# The generated lab environment historically binds the API on :8080. Override
# that before the shared production preflight in start.sh so the host service is
# never itself the public TLS endpoint. The SPA server follows the same rule.
export ACS_API_ADDR="127.0.0.1:8080"
export ACS_FRONTEND_BIND="127.0.0.1"
export ACS_ENV_ALREADY_LOADED="1"

# ACS_DEPLOYMENT_PROFILE remains exported across exec; start.sh runs the shared
# production preflight, keeps console/API on loopback, and builds the browser
# bundle against the same-origin ACS_API_PUBLIC_URL.
exec "$ROOT/scripts/start.sh"
