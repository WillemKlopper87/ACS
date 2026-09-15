#!/bin/bash
# Production entry point for the host-based ACS stack.
#
# Before invoking this wrapper, configure the CWMP/USP TLS certificate and key
# paths, ACS_USP_CLIENT_CA_CERT, and the restrictive USP CIDR allowlist in the
# environment or ~/.acs-secrets.env. Each production USP agent must also have
# a pre-provisioned certificate-fingerprint -> device/EndpointID/MQTT-topic
# binding in usp_transport_principals. The normal scripts/start.sh remains the
# compatibility-first lab/field-test quickstart.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

export ACS_DEPLOYMENT_PROFILE=production

# Load the same stable credentials/settings used by scripts/start.sh so the
# preflight validates the effective environment that the services will see.
# shellcheck disable=SC1091
source "$ROOT/scripts/gen-env.sh"

"$ROOT/scripts/security-preflight.sh"

# ACS_DEPLOYMENT_PROFILE remains exported across exec; cmd/acs and cmd/uspc
# therefore enforce their production request/config boundaries too.
exec "$ROOT/scripts/start.sh"
