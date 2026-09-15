#!/bin/bash
# Production entry point for the host-based ACS stack.
#
# The device-facing CWMP/USP listeners terminate their own TLS. The operator
# console/API use the host quickstart's plain-HTTP servers only as loopback
# upstreams and MUST be published through a real HTTPS reverse proxy/load
# balancer. Configure these public origins before running this wrapper:
#
#   ACS_FRONTEND_BASE_URL=https://acs.example.com
#   ACS_API_PUBLIC_URL=https://acs.example.com        # same-origin proxy, or
#   ACS_API_PUBLIC_URL=https://api.acs.example.com    # separate API origin
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
# in the comment above real: edit ~/.acs-secrets.env OR supply environment
# overrides for a particular deployment.
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

# Load the same stable credentials/settings used by scripts/start.sh so the
# preflight validates the effective environment that the services will see.
# shellcheck disable=SC1091
source "$ROOT/scripts/gen-env.sh"

for var in "${!production_overrides[@]}"; do
  export "$var=${production_overrides[$var]}"
done

# The generated lab environment historically binds the API on :8080. Override
# that before preflight and again inside start.sh's production branch so the
# host service is never itself the public TLS endpoint. The SPA server follows
# the same rule through ACS_FRONTEND_BIND.
export ACS_API_ADDR="127.0.0.1:8080"
export ACS_FRONTEND_BIND="127.0.0.1"

"$ROOT/scripts/security-preflight.sh"

# ACS_DEPLOYMENT_PROFILE remains exported across exec; cmd/acs and cmd/uspc
# therefore enforce their production request/config boundaries too. start.sh
# additionally keeps console/API on loopback and builds the browser bundle
# against ACS_API_PUBLIC_URL.
exec "$ROOT/scripts/start.sh"
