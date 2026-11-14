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
# The switch: is this ACS run on its own, or through the ISP NaaS platform?
# There is no default here: it decides what else is asked for and what the
# adapter will accept, so it is chosen, not inherited.
: "${ACS_PRODUCTION_INTEGRATION_MODE:?set ACS_PRODUCTION_INTEGRATION_MODE to standalone (the ACS on its own) or naas (through the NaaS platform)}"
case "$ACS_PRODUCTION_INTEGRATION_MODE" in
  standalone) ;;
  naas)
    # The networks the BSS/NaaS platform calls the adapter from. The adapter
    # answers its integration routes to these only.
    : "${ACS_PRODUCTION_BSS_ALLOWED_CIDRS:?set ACS_PRODUCTION_BSS_ALLOWED_CIDRS to the source CIDRs of the BSS/NaaS platform}"
    ;;
  *)
    echo "ACS_PRODUCTION_INTEGRATION_MODE must be standalone or naas (got '$ACS_PRODUCTION_INTEGRATION_MODE')" >&2
    exit 1
    ;;
esac

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

# Operator and integration plane (docs/SECURITY-ASSESSMENT-2026-11-14.md, O1).
# cmd/api and cmd/bssadapter refuse to start under the production profile
# without these.
#
# The adapter: customer-bound OAuth clients only, no shared token, calls
# accepted only from the platform's networks, listening on loopback behind
# the HTTPS ingress, which is the one proxy trusted to report the caller.
upsert_export ACS_INTEGRATION_MODE "$ACS_PRODUCTION_INTEGRATION_MODE"
upsert_export ACS_BSS_ADDR "127.0.0.1:8090"
upsert_export ACS_BSS_API_TOKEN ""
if [ "$ACS_PRODUCTION_INTEGRATION_MODE" = "naas" ]; then
  upsert_export ACS_BSS_REQUIRE_CUSTOMER_BOUND_CLIENTS "true"
  upsert_export ACS_BSS_ALLOWED_CIDRS "$ACS_PRODUCTION_BSS_ALLOWED_CIDRS"
  upsert_export ACS_TRUSTED_PROXY_CIDRS "${ACS_PRODUCTION_TRUSTED_PROXY_CIDRS:-127.0.0.1/32,::1/128}"
fi
# The console: two-step sign-in for every role that can change devices,
# and device web pages kept in their sandbox.
upsert_export ACS_MFA_REQUIRED_ROLES "${ACS_PRODUCTION_MFA_REQUIRED_ROLES:-superadmin,manager,noc}"
upsert_export ACS_WEBGUI_ALLOW_SAME_ORIGIN "false"

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
if [ "$ACS_PRODUCTION_INTEGRATION_MODE" = "naas" ]; then
  echo "  Integration: through the NaaS platform; customer-bound OAuth clients only, from $ACS_PRODUCTION_BSS_ALLOWED_CIDRS"
else
  echo "  Integration: standalone; the BSS/NaaS adapter is switched off"
fi
echo "  Operators: two-step sign-in required for ${ACS_PRODUCTION_MFA_REQUIRED_ROLES:-superadmin,manager,noc}"
