#!/bin/bash
# Generates ACS credentials ONCE and persists them to ~/.acs-secrets.env.
# Safe to source repeatedly — cmd/acs, cmd/api, cmd/bssadapter and cmd/uspc
# must see IDENTICAL values for shared credentials and stable service IDs.
# Re-running `openssl rand` on every source silently gives each process
# different secrets, breaking auth in a way that's hard to diagnose.
#
# Usage: source scripts/gen-env.sh   (from repo root, or any directory —
# path below is absolute)
set -e

SECRETS_FILE="$HOME/.acs-secrets.env"
SYSTEMD_FILE="$HOME/.acs-secrets.systemd.env"

if [ ! -f "$SECRETS_FILE" ]; then
  echo "Generating new ACS credentials -> $SECRETS_FILE"
  GENERATED_POSTGRES_PASSWORD="$(openssl rand -hex 24)"
  cat > "$SECRETS_FILE" <<EOF
export ACS_POSTGRES_PASSWORD="$GENERATED_POSTGRES_PASSWORD"
export ACS_POSTGRES_DSN="postgres://acs:$GENERATED_POSTGRES_PASSWORD@localhost:5432/acs?sslmode=disable"
export ACS_DEPLOYMENT_PROFILE="lab"
export ACS_CWMP_ALLOW_PLAINTEXT="true"

# Constrained first-contact identity. New CPEs should use this pair so ACS
# can graduate them to a unique device-bound CWMP_DIGEST credential.
export ACS_CWMP_BOOTSTRAP_USERNAME="acs-bootstrap"
export ACS_CWMP_BOOTSTRAP_PASSWORD="$(openssl rand -base64 24)"

# Legacy/shared Digest identity retained for controlled lab compatibility only.
# It is not the production onboarding or steady-state device identity.
export ACS_DIGEST_USERNAME="acs-device"
export ACS_DIGEST_PASSWORD="$(openssl rand -base64 16)"
export ACS_CWMP_ALLOW_SHARED_ESTABLISHED="false"

export ACS_CONNECTION_REQUEST_USERNAME="acs-connreq"
export ACS_CONNECTION_REQUEST_PASSWORD="$(openssl rand -base64 16)"

export ACS_BOOTSTRAP_ADMIN_USERNAME="admin"
export ACS_BOOTSTRAP_ADMIN_PASSWORD="$(openssl rand -base64 16)"

# Grafana's admin login, and the password for the SELECT-only Postgres
# role its Postgres-backed dashboards query through.
export GRAFANA_ADMIN_PASSWORD="$(openssl rand -base64 16)"
export ACS_GRAFANA_DB_PASSWORD="$(openssl rand -base64 24)"

export ACS_JWT_SIGNING_SECRET="$(openssl rand -base64 32)"
export ACS_CREDENTIAL_ENCRYPTION_KEY="$(openssl rand -base64 32)"
# Seals BSS secret handles (POST /bss/v1/secrets); cmd/bssadapter and
# cmd/acs both need it. Changing it invalidates handles not yet used.
export ACS_BSS_SECRET_KEY="$(openssl rand -base64 32)"
export ACS_INTERNAL_SERVICE_TOKEN="$(openssl rand -base64 32)"
export ACS_BSS_OAUTH_SIGNING_SECRET="$(openssl rand -base64 32)"

export ACS_ADDR=":7547"
export ACS_CWMP_ALLOWED_CIDRS=""
export ACS_API_ADDR=":8080"
export ACS_STUN_ADDR=":3478"
# Keep the BSS northbound port host-local in the quickstart. Put a TLS
# reverse proxy in front before exposing it beyond the host.
export ACS_BSS_ADDR="127.0.0.1:8090"
export ACS_INTERNAL_API_URL="http://127.0.0.1:8080"
export ACS_BSS_ADAPTER_URL="http://127.0.0.1:8090"

# USP controller identity must be stable across restarts because agents
# retain it as the controller endpoint ID. The quickstart explicitly opts
# into plaintext WebSocket/MQTT for lab/field-test compatibility. Production
# requires ACS_USP_TLS_CERT/KEY, ACS_USP_CLIENT_CA_CERT, a restrictive CIDR
# allowlist, and a pre-provisioned per-agent certificate principal.
export ACS_USP_CONTROLLER_ID="acs-controller-$(openssl rand -hex 8)"
export ACS_USP_POSTGRES_DSN="\$ACS_POSTGRES_DSN"
export ACS_USP_WS_ADDR=":9877"
export ACS_USP_MQTT_ADDR=":1883"
export ACS_USP_HTTP_ADDR="127.0.0.1:8092"
export ACS_USP_ALLOW_PLAINTEXT="true"
export ACS_USP_TLS_CERT=""
export ACS_USP_TLS_KEY=""
export ACS_USP_CLIENT_CA_CERT=""
export ACS_USP_ALLOWED_CIDRS=""

export ACS_DEBUG=""

# --- CPE compatibility knobs (optional, safe defaults) ---
# Set to "1" to also accept HTTP Basic auth on the CWMP endpoint, for CPE
# firmwares that only implement Basic (some Huawei/ZTE defaults). Basic
# sends the password in cleartext — combine with TLS in production.
export ACS_AUTH_ALLOW_BASIC=""
# TLS floor for the CWMP listener when ACS_TLS_CERT/KEY are set. Empty
# means "1.0" (permissive, legacy CBC/RSA-kex ciphers enabled — many
# deployed CPEs can't do TLS 1.2). Set "1.2" to harden a modern fleet.
export ACS_TLS_MIN_VERSION=""
# Paths to the CWMP TLS certificate + key (use the FULL CHAIN, e.g.
# Let's Encrypt fullchain.pem — old CPEs won't fetch intermediates).
# Empty means the CWMP endpoint runs plain HTTP.
export ACS_TLS_CERT=""
export ACS_TLS_KEY=""
# Lab TLS migration: a second CWMP listener serving TLS (e.g. ":7548") while
# ACS_ADDR (:7547) stays plain HTTP as the recovery path. Needs
# ACS_TLS_CERT/KEY (scripts/acs-cert.sh). Empty means one listener.
export ACS_TLS_ADDR=""

# --- XMPP Connection Request (TR-069 Annex K) ---
# docs/XMPP-CONNECTION-REQUEST.md. Off until set to "1": scripts/start.sh
# then starts the Prosody container (TCP 5222, open it to CPEs) and cmd/api
# connects to it. The domain is only a name inside JIDs; CPEs dial
# ACS_XMPP_CPE_SERVER_ADDRESS (empty = the detected public IP).
export ACS_XMPP_ENABLED="0"
export ACS_XMPP_DOMAIN="xmpp.acs.lab"
export ACS_XMPP_JID="acs@xmpp.acs.lab"
export ACS_XMPP_PASSWORD="$(openssl rand -hex 24)"
export ACS_XMPP_CPE_SERVER_ADDRESS=""
EOF
  chmod 600 "$SECRETS_FILE"
else
  echo "Using existing credentials from $SECRETS_FILE (delete this file and rerun to regenerate)"
  # Repair early quickstart files that declared the CWMP credentials but
  # left them empty. Existing non-empty values are preserved so rerunning
  # this script cannot silently strand an already-provisioned fleet.
  if grep -q '^export ACS_DIGEST_USERNAME=""$' "$SECRETS_FILE"; then
    sed -i 's/^export ACS_DIGEST_USERNAME=""$/export ACS_DIGEST_USERNAME="acs-device"/' "$SECRETS_FILE"
  elif ! grep -q '^export ACS_DIGEST_USERNAME=' "$SECRETS_FILE"; then
    echo 'export ACS_DIGEST_USERNAME="acs-device"' >> "$SECRETS_FILE"
  fi
  if grep -q '^export ACS_DIGEST_PASSWORD=""$' "$SECRETS_FILE"; then
    sed -i "s|^export ACS_DIGEST_PASSWORD=\"\"$|export ACS_DIGEST_PASSWORD=\"$(openssl rand -base64 16)\"|" "$SECRETS_FILE"
  elif ! grep -q '^export ACS_DIGEST_PASSWORD=' "$SECRETS_FILE"; then
    echo "export ACS_DIGEST_PASSWORD=\"$(openssl rand -base64 16)\"" >> "$SECRETS_FILE"
  fi
  if grep -q '^export ACS_CONNECTION_REQUEST_USERNAME=""$' "$SECRETS_FILE"; then
    sed -i 's/^export ACS_CONNECTION_REQUEST_USERNAME=""$/export ACS_CONNECTION_REQUEST_USERNAME="acs-connreq"/' "$SECRETS_FILE"
  elif ! grep -q '^export ACS_CONNECTION_REQUEST_USERNAME=' "$SECRETS_FILE"; then
    echo 'export ACS_CONNECTION_REQUEST_USERNAME="acs-connreq"' >> "$SECRETS_FILE"
  fi
  # Backfill the constrained bootstrap identity added after the original
  # quickstart. Generate it once and preserve it across restarts so a CPE
  # midway through bootstrap/graduation is not stranded.
  if ! grep -q '^export ACS_CWMP_BOOTSTRAP_USERNAME=' "$SECRETS_FILE"; then
    echo 'export ACS_CWMP_BOOTSTRAP_USERNAME="acs-bootstrap"' >> "$SECRETS_FILE"
  fi
  if ! grep -q '^export ACS_CWMP_BOOTSTRAP_PASSWORD=' "$SECRETS_FILE"; then
    echo "export ACS_CWMP_BOOTSTRAP_PASSWORD=\"$(openssl rand -base64 24)\"" >> "$SECRETS_FILE"
  fi
  if ! grep -q '^export ACS_CWMP_ALLOW_SHARED_ESTABLISHED=' "$SECRETS_FILE"; then
    echo 'export ACS_CWMP_ALLOW_SHARED_ESTABLISHED="false"' >> "$SECRETS_FILE"
  fi
  if ! grep -q '^export ACS_DEPLOYMENT_PROFILE=' "$SECRETS_FILE"; then
    echo 'export ACS_DEPLOYMENT_PROFILE="lab"' >> "$SECRETS_FILE"
  fi
  if ! grep -q '^export ACS_POSTGRES_PASSWORD=' "$SECRETS_FILE"; then
    # Older quickstart files used the literal `acs` password in their DSN.
    # Preserve that existing database credential; new installs above get a
    # random password.
    LEGACY_POSTGRES_PASSWORD="$(sed -n 's#^export ACS_POSTGRES_DSN="postgres://[^:]*:\([^@]*\)@.*#\1#p' "$SECRETS_FILE" | head -n 1)"
    if [ -z "$LEGACY_POSTGRES_PASSWORD" ]; then
      echo "Cannot infer ACS_POSTGRES_PASSWORD from $SECRETS_FILE's ACS_POSTGRES_DSN" >&2
      exit 1
    fi
    echo "export ACS_POSTGRES_PASSWORD=\"$LEGACY_POSTGRES_PASSWORD\"" >> "$SECRETS_FILE"
  fi
  if ! grep -q '^export ACS_CWMP_ALLOWED_CIDRS=' "$SECRETS_FILE"; then
    echo 'export ACS_CWMP_ALLOWED_CIDRS=""' >> "$SECRETS_FILE"
  fi
  if grep -q '^export ACS_CONNECTION_REQUEST_PASSWORD=""$' "$SECRETS_FILE"; then
    sed -i "s|^export ACS_CONNECTION_REQUEST_PASSWORD=\"\"$|export ACS_CONNECTION_REQUEST_PASSWORD=\"$(openssl rand -base64 16)\"|" "$SECRETS_FILE"
  elif ! grep -q '^export ACS_CONNECTION_REQUEST_PASSWORD=' "$SECRETS_FILE"; then
    echo "export ACS_CONNECTION_REQUEST_PASSWORD=\"$(openssl rand -base64 16)\"" >> "$SECRETS_FILE"
  fi

  # Backfill secrets added after this file was first generated — the
  # services fail closed without them since the P0.1 hardening.
  for var in ACS_INTERNAL_SERVICE_TOKEN ACS_BSS_OAUTH_SIGNING_SECRET GRAFANA_ADMIN_PASSWORD ACS_GRAFANA_DB_PASSWORD; do
    if ! grep -q "^export $var=" "$SECRETS_FILE"; then
      echo "Backfilling $var into $SECRETS_FILE"
      echo "export $var=\"$(openssl rand -base64 32)\"" >> "$SECRETS_FILE"
    fi
  done

  # Backfill the services that were added after the original host
  # quickstart. The generated controller ID is intentionally written only
  # once so a rerun cannot change the USP controller identity underneath
  # already-provisioned agents.
  if ! grep -q '^export ACS_BSS_ADDR=' "$SECRETS_FILE"; then
    echo 'export ACS_BSS_ADDR="127.0.0.1:8090"' >> "$SECRETS_FILE"
  fi
  if ! grep -q '^export ACS_INTERNAL_API_URL=' "$SECRETS_FILE"; then
    echo 'export ACS_INTERNAL_API_URL="http://127.0.0.1:8080"' >> "$SECRETS_FILE"
  fi
  if ! grep -q '^export ACS_BSS_ADAPTER_URL=' "$SECRETS_FILE"; then
    echo 'export ACS_BSS_ADAPTER_URL="http://127.0.0.1:8090"' >> "$SECRETS_FILE"
  fi
  if ! grep -q '^export ACS_USP_CONTROLLER_ID=' "$SECRETS_FILE"; then
    echo "export ACS_USP_CONTROLLER_ID=\"acs-controller-$(openssl rand -hex 8)\"" >> "$SECRETS_FILE"
  fi
  if ! grep -q '^export ACS_USP_POSTGRES_DSN=' "$SECRETS_FILE"; then
    echo 'export ACS_USP_POSTGRES_DSN="$ACS_POSTGRES_DSN"' >> "$SECRETS_FILE"
  fi
  if ! grep -q '^export ACS_USP_WS_ADDR=' "$SECRETS_FILE"; then
    echo 'export ACS_USP_WS_ADDR=":9877"' >> "$SECRETS_FILE"
  fi
  if ! grep -q '^export ACS_USP_MQTT_ADDR=' "$SECRETS_FILE"; then
    echo 'export ACS_USP_MQTT_ADDR=":1883"' >> "$SECRETS_FILE"
  fi
  if ! grep -q '^export ACS_USP_HTTP_ADDR=' "$SECRETS_FILE"; then
    echo 'export ACS_USP_HTTP_ADDR="127.0.0.1:8092"' >> "$SECRETS_FILE"
  fi
  if ! grep -q '^export ACS_USP_ALLOW_PLAINTEXT=' "$SECRETS_FILE"; then
    echo 'export ACS_USP_ALLOW_PLAINTEXT="true"' >> "$SECRETS_FILE"
  fi
  for var in ACS_USP_TLS_CERT ACS_USP_TLS_KEY ACS_USP_CLIENT_CA_CERT ACS_USP_ALLOWED_CIDRS; do
    if ! grep -q "^export $var=" "$SECRETS_FILE"; then
      echo "export $var=\"\"" >> "$SECRETS_FILE"
    fi
  done

  # Backfill XMPP Connection Request (TR-069 Annex K) settings, disabled by
  # default. The ACS account password is generated once and kept, since
  # cmd/api and the Prosody container must agree on it.
  if ! grep -q '^export ACS_XMPP_ENABLED=' "$SECRETS_FILE"; then
    echo 'export ACS_XMPP_ENABLED="0"' >> "$SECRETS_FILE"
  fi
  if ! grep -q '^export ACS_XMPP_DOMAIN=' "$SECRETS_FILE"; then
    echo 'export ACS_XMPP_DOMAIN="xmpp.acs.lab"' >> "$SECRETS_FILE"
  fi
  if ! grep -q '^export ACS_XMPP_JID=' "$SECRETS_FILE"; then
    echo 'export ACS_XMPP_JID="acs@$ACS_XMPP_DOMAIN"' >> "$SECRETS_FILE"
  fi
  if ! grep -q '^export ACS_XMPP_PASSWORD=' "$SECRETS_FILE"; then
    echo "export ACS_XMPP_PASSWORD=\"$(openssl rand -hex 24)\"" >> "$SECRETS_FILE"
  fi
  if ! grep -q '^export ACS_XMPP_CPE_SERVER_ADDRESS=' "$SECRETS_FILE"; then
    echo 'export ACS_XMPP_CPE_SERVER_ADDRESS=""' >> "$SECRETS_FILE"
  fi
fi

source "$SECRETS_FILE"

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

# XMPP Connection Request: cmd/api enables its XMPP client only when
# ACS_XMPP_SERVER is set, so it stays unset unless explicitly enabled. The
# ACS pins the lab server's self-signed certificate rather than skipping
# verification (scripts/xmpp-cert.sh creates it).
export ACS_XMPP_USER="${ACS_XMPP_JID%%@*}"
if [ "${ACS_XMPP_ENABLED:-0}" = "1" ]; then
  export ACS_XMPP_SERVER="${ACS_XMPP_SERVER:-127.0.0.1:5222}"
  export ACS_XMPP_TLS_CA_FILE="${ACS_XMPP_TLS_CA_FILE:-${ACS_XMPP_CERT_DIR:-$REPO_ROOT/infra/prosody/certs}/xmpp.crt}"
else
  unset ACS_XMPP_SERVER
fi

# docker compose reads variables from a `.env` next to the compose file,
# not from this shell's exports when compose is invoked from somewhere
# else. Writing it here keeps the monitoring stack consistent.
COMPOSE_ENV="$REPO_ROOT/infra/.env"
{
  echo "# Generated by scripts/gen-env.sh — do not edit, do not commit."
  echo "GRAFANA_ADMIN_PASSWORD=$GRAFANA_ADMIN_PASSWORD"
  echo "ACS_GRAFANA_DB_PASSWORD=$ACS_GRAFANA_DB_PASSWORD"
  echo "GRAFANA_COOKIE_SECURE=${GRAFANA_COOKIE_SECURE:-false}"
  echo "GRAFANA_BIND=${GRAFANA_BIND:-127.0.0.1}"
  echo "GRAFANA_ROOT_URL=${GRAFANA_ROOT_URL:-http://localhost:3000}"
  echo "ACS_POSTGRES_PASSWORD=${ACS_POSTGRES_PASSWORD:-acs}"
  echo "ACS_ALERT_WEBHOOK_URL=${ACS_ALERT_WEBHOOK_URL:-http://host.docker.internal:9999/alerts}"
  echo "ACS_XMPP_DOMAIN=$ACS_XMPP_DOMAIN"
  echo "ACS_XMPP_USER=$ACS_XMPP_USER"
  echo "ACS_XMPP_PASSWORD=$ACS_XMPP_PASSWORD"
  if [ -n "${ACS_XMPP_CERT_DIR:-}" ]; then
    echo "ACS_XMPP_CERT_DIR=$ACS_XMPP_CERT_DIR"
  fi
} > "$COMPOSE_ENV"
chmod 600 "$COMPOSE_ENV"

# systemd's EnvironmentFile= wants plain KEY=value lines — no `export`,
# no quotes, no command substitution. Regenerate this companion file
# from the already-resolved values every time this script runs.
{
  echo "ACS_POSTGRES_DSN=$ACS_POSTGRES_DSN"
  echo "ACS_DEPLOYMENT_PROFILE=${ACS_DEPLOYMENT_PROFILE:-lab}"
  echo "ACS_CWMP_BOOTSTRAP_USERNAME=$ACS_CWMP_BOOTSTRAP_USERNAME"
  echo "ACS_CWMP_BOOTSTRAP_PASSWORD=$ACS_CWMP_BOOTSTRAP_PASSWORD"
  echo "ACS_DIGEST_USERNAME=$ACS_DIGEST_USERNAME"
  echo "ACS_DIGEST_PASSWORD=$ACS_DIGEST_PASSWORD"
  echo "ACS_CWMP_ALLOW_SHARED_ESTABLISHED=${ACS_CWMP_ALLOW_SHARED_ESTABLISHED:-false}"
  echo "ACS_CONNECTION_REQUEST_USERNAME=$ACS_CONNECTION_REQUEST_USERNAME"
  echo "ACS_CONNECTION_REQUEST_PASSWORD=$ACS_CONNECTION_REQUEST_PASSWORD"
  echo "ACS_BOOTSTRAP_ADMIN_USERNAME=$ACS_BOOTSTRAP_ADMIN_USERNAME"
  echo "ACS_BOOTSTRAP_ADMIN_PASSWORD=$ACS_BOOTSTRAP_ADMIN_PASSWORD"
  echo "ACS_JWT_SIGNING_SECRET=$ACS_JWT_SIGNING_SECRET"
  echo "ACS_CREDENTIAL_ENCRYPTION_KEY=$ACS_CREDENTIAL_ENCRYPTION_KEY"
  echo "ACS_BSS_SECRET_KEY=$ACS_BSS_SECRET_KEY"
  echo "ACS_INTERNAL_SERVICE_TOKEN=$ACS_INTERNAL_SERVICE_TOKEN"
  echo "ACS_BSS_OAUTH_SIGNING_SECRET=$ACS_BSS_OAUTH_SIGNING_SECRET"
  echo "ACS_ADDR=$ACS_ADDR"
  echo "ACS_CWMP_ALLOWED_CIDRS=${ACS_CWMP_ALLOWED_CIDRS:-}"
  echo "ACS_API_ADDR=$ACS_API_ADDR"
  echo "ACS_STUN_ADDR=$ACS_STUN_ADDR"
  echo "ACS_BSS_ADDR=$ACS_BSS_ADDR"
  echo "ACS_INTERNAL_API_URL=$ACS_INTERNAL_API_URL"
  echo "ACS_BSS_ADAPTER_URL=$ACS_BSS_ADAPTER_URL"
  echo "ACS_USP_CONTROLLER_ID=$ACS_USP_CONTROLLER_ID"
  echo "ACS_USP_POSTGRES_DSN=$ACS_USP_POSTGRES_DSN"
  echo "ACS_USP_WS_ADDR=$ACS_USP_WS_ADDR"
  echo "ACS_USP_MQTT_ADDR=$ACS_USP_MQTT_ADDR"
  echo "ACS_USP_HTTP_ADDR=$ACS_USP_HTTP_ADDR"
  echo "ACS_USP_ALLOW_PLAINTEXT=$ACS_USP_ALLOW_PLAINTEXT"
  echo "ACS_USP_TLS_CERT=$ACS_USP_TLS_CERT"
  echo "ACS_USP_TLS_KEY=$ACS_USP_TLS_KEY"
  echo "ACS_USP_CLIENT_CA_CERT=$ACS_USP_CLIENT_CA_CERT"
  echo "ACS_USP_ALLOWED_CIDRS=$ACS_USP_ALLOWED_CIDRS"
  echo "ACS_AUTH_ALLOW_BASIC=$ACS_AUTH_ALLOW_BASIC"
  echo "ACS_TLS_MIN_VERSION=$ACS_TLS_MIN_VERSION"
  echo "ACS_TLS_CERT=$ACS_TLS_CERT"
  echo "ACS_TLS_KEY=$ACS_TLS_KEY"
  echo "ACS_TLS_ADDR=${ACS_TLS_ADDR:-}"
  echo "ACS_XMPP_SERVER=${ACS_XMPP_SERVER:-}"
  echo "ACS_XMPP_DOMAIN=$ACS_XMPP_DOMAIN"
  echo "ACS_XMPP_JID=$ACS_XMPP_JID"
  echo "ACS_XMPP_PASSWORD=$ACS_XMPP_PASSWORD"
  echo "ACS_XMPP_TLS_CA_FILE=${ACS_XMPP_TLS_CA_FILE:-}"
  echo "ACS_XMPP_CPE_SERVER_ADDRESS=${ACS_XMPP_CPE_SERVER_ADDRESS:-}"
} > "$SYSTEMD_FILE"
chmod 600 "$SYSTEMD_FILE"

# This script is sourced by quickstart/startup and CI. Do not emit credentials
# into terminal history, EC2 serial logs, session recordings, or support logs.
# Use scripts/show-cpe-setup.sh --show-secrets only for a deliberate,
# interactive CPE configuration session.
echo ""
echo "ACS credentials loaded from $SECRETS_FILE (file mode 600)."
echo "CWMP first-contact username: $ACS_CWMP_BOOTSTRAP_USERNAME"
echo "Legacy fleet Digest credential: lab compatibility only."
echo "Connection Request username: $ACS_CONNECTION_REQUEST_USERNAME"
if [ "${ACS_XMPP_ENABLED:-0}" = "1" ]; then
  echo "XMPP Connection Request: enabled (ACS JID $ACS_XMPP_JID)"
else
  echo "XMPP Connection Request: disabled (set ACS_XMPP_ENABLED=\"1\" in $SECRETS_FILE to enable)"
fi
echo "Passwords and API/admin secrets are redacted."
echo "For CPE setup, run: $REPO_ROOT/scripts/show-cpe-setup.sh"
echo "To inspect CPE secrets interactively: $REPO_ROOT/scripts/show-cpe-setup.sh --show-secrets"
echo ""
