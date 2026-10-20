#!/bin/bash
# Print the initial CPE/TR-069 settings for a controlled onboarding session.
#
# By default secrets are redacted. Use --show-secrets interactively when you
# are physically configuring the CPE. Do not paste the secret-bearing output
# into tickets, screenshots or qualification evidence.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SECRETS_FILE="${ACS_SECRETS_FILE:-$HOME/.acs-secrets.env}"
SHOW_SECRETS=0

if [ "${1:-}" = "--show-secrets" ]; then
  SHOW_SECRETS=1
elif [ -n "${1:-}" ]; then
  echo "Usage: $0 [--show-secrets]" >&2
  exit 2
fi

if [ ! -f "$SECRETS_FILE" ]; then
  echo "ERROR: $SECRETS_FILE not found. Run/source scripts/gen-env.sh first." >&2
  exit 1
fi

# shellcheck disable=SC1090
source "$SECRETS_FILE"

detect_public_ip() {
  if [ -n "${ACS_PUBLIC_IP:-}" ]; then
    printf '%s\n' "$ACS_PUBLIC_IP"
    return
  fi
  local token
  token="$(curl -s -m 2 -X PUT "http://169.254.169.254/latest/api/token"     -H "X-aws-ec2-metadata-token-ttl-seconds: 21600" 2>/dev/null || true)"
  if [ -n "$token" ]; then
    curl -s -m 2 -H "X-aws-ec2-metadata-token: $token"       http://169.254.169.254/latest/meta-data/public-ipv4 2>/dev/null || true
  fi
}

PUBLIC_IP="$(detect_public_ip)"
if [ -z "$PUBLIC_IP" ]; then
  PUBLIC_IP="<CURRENT_PUBLIC_IP>"
fi

CWMP_SCHEME="http"
if [ -n "${ACS_TLS_CERT:-}" ] && [ -n "${ACS_TLS_KEY:-}" ]; then
  CWMP_SCHEME="https"
fi

secret_value() {
  local value="$1"
  if [ "$SHOW_SECRETS" = "1" ]; then
    printf '%s' "$value"
  else
    printf '%s' "<redacted: rerun with --show-secrets>"
  fi
}

echo "=== Initial CPE / TR-069 setup ==="
echo "Deployment profile: ${ACS_DEPLOYMENT_PROFILE:-lab}"
echo
echo "Periodic Inform enabled:  yes"
echo "Periodic Inform interval: 60 seconds (qualification)"
echo
echo "ACS URL:                   $CWMP_SCHEME://$PUBLIC_IP:7547/cwmp"
echo "ACS Username (ACSUsername): ${ACS_CWMP_BOOTSTRAP_USERNAME:-${ACS_DIGEST_USERNAME:-<unset>}}"
printf 'ACS Password (ACSPassword): '
secret_value "${ACS_CWMP_BOOTSTRAP_PASSWORD:-${ACS_DIGEST_PASSWORD:-}}"
echo
echo
echo "Connection Request URL:    leave device-generated / observed initially"
echo "CR Username (ACSCRUsername): ${ACS_CONNECTION_REQUEST_USERNAME:-<unset>}"
printf 'CR Password (ACSCRPassword): '
secret_value "${ACS_CONNECTION_REQUEST_PASSWORD:-}"
echo
echo
echo "Notes:"
echo "  - ACSUsername/ACSPassword are for CPE -> ACS authentication."
echo "  - ACSCRUsername/ACSCRPassword are for ACS -> CPE Connection Request authentication."
echo "  - Do not replace the device's Connection Request URL with the ACS URL."
echo "  - Verify credential installation, matching bound reconnect and ACTIVE state before normal management."
echo "  - If this EC2 instance uses an auto-assigned public IP, rerun this helper after any stop/start."
if [ "$SHOW_SECRETS" = "1" ]; then
  echo
  echo "WARNING: secret values were displayed. Do not capture/share this terminal output."
fi
