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

require_nonempty ACS_TLS_CERT
require_nonempty ACS_TLS_KEY
require_nonempty ACS_USP_TLS_CERT
require_nonempty ACS_USP_TLS_KEY
require_nonempty ACS_USP_ALLOWED_CIDRS

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

if [ "$errors" -ne 0 ]; then
  echo "Security preflight FAILED with $errors problem(s)." >&2
  exit 1
fi

echo "Security preflight passed: production CWMP/USP transport requirements are configured."
echo "Note: production CWMP rejects the shared fleet Digest username; provision per-device Digest credentials or mTLS before connecting established CPEs."
