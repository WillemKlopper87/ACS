#!/bin/bash
# Convert the stable host configuration into a fail-closed production
# device-plane profile. The operator must name the protected interface,
# certificate and permitted CPE networks; no Internet-wide defaults exist.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SECRETS_FILE="${HOME}/.acs-secrets.env"

: "${ACS_PRODUCTION_BIND_ADDRESS:?set ACS_PRODUCTION_BIND_ADDRESS to the protected device-plane IP}"
: "${ACS_PRODUCTION_ALLOWED_CIDRS:?set ACS_PRODUCTION_ALLOWED_CIDRS to the CPE management CIDRs}"
: "${ACS_PRODUCTION_TLS_CERT:?set ACS_PRODUCTION_TLS_CERT to the server full-chain PEM}"
: "${ACS_PRODUCTION_TLS_KEY:?set ACS_PRODUCTION_TLS_KEY to the server private-key PEM}"
: "${ACS_PRODUCTION_USP_CLIENT_CA_CERT:?set ACS_PRODUCTION_USP_CLIENT_CA_CERT to the USP agent client CA PEM}"

case "$ACS_PRODUCTION_BIND_ADDRESS" in
  0.0.0.0|::|"[::]"|"")
    echo "ACS_PRODUCTION_BIND_ADDRESS must name a protected interface, not a wildcard" >&2
    exit 1
    ;;
esac
for file in "$ACS_PRODUCTION_TLS_CERT" "$ACS_PRODUCTION_TLS_KEY" "$ACS_PRODUCTION_USP_CLIENT_CA_CERT"; do
  if [ ! -r "$file" ]; then
    echo "required TLS file is not readable: $file" >&2
    exit 1
  fi
done

# Generate stable application secrets first on a new host.
# shellcheck disable=SC1091
source "$ROOT/scripts/gen-env.sh" >/dev/null

upsert_export() {
  key="$1"
  value="$2"
  escaped="$(printf '%s' "$value" | sed 's/[&|]/\\&/g')"
  if grep -q "^export ${key}=" "$SECRETS_FILE"; then
    sed -i "s|^export ${key}=.*$|export ${key}=\"${escaped}\"|" "$SECRETS_FILE"
  else
    printf 'export %s="%s"\n' "$key" "$value" >> "$SECRETS_FILE"
  fi
}

upsert_export ACS_ADDR "${ACS_PRODUCTION_BIND_ADDRESS}:7547"
upsert_export ACS_DEPLOYMENT_PROFILE "production"
upsert_export ACS_CWMP_ALLOWED_CIDRS "$ACS_PRODUCTION_ALLOWED_CIDRS"
upsert_export ACS_USP_WS_ADDR "${ACS_PRODUCTION_BIND_ADDRESS}:9877"
upsert_export ACS_USP_MQTT_ADDR "${ACS_PRODUCTION_BIND_ADDRESS}:8883"
upsert_export ACS_TLS_CERT "$ACS_PRODUCTION_TLS_CERT"
upsert_export ACS_TLS_KEY "$ACS_PRODUCTION_TLS_KEY"
upsert_export ACS_TLS_MIN_VERSION "${ACS_PRODUCTION_TLS_MIN_VERSION:-1.2}"
upsert_export ACS_USP_TLS_CERT "$ACS_PRODUCTION_TLS_CERT"
upsert_export ACS_USP_TLS_KEY "$ACS_PRODUCTION_TLS_KEY"
upsert_export ACS_USP_CLIENT_CA_CERT "$ACS_PRODUCTION_USP_CLIENT_CA_CERT"
upsert_export ACS_USP_ALLOW_PLAINTEXT "false"
upsert_export ACS_USP_ALLOWED_CIDRS "$ACS_PRODUCTION_ALLOWED_CIDRS"
upsert_export ACS_AUTH_ALLOW_BASIC "false"
upsert_export ACS_CWMP_ALLOW_SHARED_ESTABLISHED "false"

chmod 600 "$SECRETS_FILE"
# Regenerate the systemd companion from the final persisted values.
# shellcheck disable=SC1091
source "$ROOT/scripts/gen-env.sh" >/dev/null

echo "Production device-plane profile written to $SECRETS_FILE"
echo "  bind address: $ACS_PRODUCTION_BIND_ADDRESS"
echo "  allowed CIDRs: $ACS_PRODUCTION_ALLOWED_CIDRS"
echo "  CWMP: https://${ACS_PRODUCTION_BIND_ADDRESS}:7547/cwmp"
echo "  USP WebSocket: wss://${ACS_PRODUCTION_BIND_ADDRESS}:9877/usp"
echo "  USP MQTT TLS: ${ACS_PRODUCTION_BIND_ADDRESS}:8883"
