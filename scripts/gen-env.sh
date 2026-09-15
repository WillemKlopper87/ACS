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
  cat > "$SECRETS_FILE" <<EOF
export ACS_POSTGRES_DSN="postgres://acs:acs@localhost:5432/acs?sslmode=disable"

export ACS_DIGEST_USERNAME="acs-device"
export ACS_DIGEST_PASSWORD="$(openssl rand -base64 16)"

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
export ACS_INTERNAL_SERVICE_TOKEN="$(openssl rand -base64 32)"
export ACS_BSS_OAUTH_SIGNING_SECRET="$(openssl rand -base64 32)"

export ACS_ADDR=":7547"
export ACS_API_ADDR=":8080"
export ACS_STUN_ADDR=":3478"
# Keep the BSS northbound port host-local in the quickstart. Put a TLS
# reverse proxy in front before exposing it beyond the host.
export ACS_BSS_ADDR="127.0.0.1:8090"
export ACS_INTERNAL_API_URL="http://127.0.0.1:8080"
export ACS_BSS_ADAPTER_URL="http://127.0.0.1:8090"

# USP controller identity must be stable across restarts because agents
# retain it as the controller endpoint ID. The quickstart explicitly opts
# into plaintext WebSocket/MQTT for lab/field-test compatibility; replace
# this with ACS_USP_TLS_CERT/KEY and set ALLOW_PLAINTEXT=false for a
# production-facing deployment.
export ACS_USP_CONTROLLER_ID="acs-controller-$(openssl rand -hex 8)"
export ACS_USP_POSTGRES_DSN="\$ACS_POSTGRES_DSN"
export ACS_USP_WS_ADDR=":9877"
export ACS_USP_MQTT_ADDR=":1883"
export ACS_USP_HTTP_ADDR="127.0.0.1:8092"
export ACS_USP_ALLOW_PLAINTEXT="true"
export ACS_USP_TLS_CERT=""
export ACS_USP_TLS_KEY=""
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
  for var in ACS_USP_TLS_CERT ACS_USP_TLS_KEY ACS_USP_ALLOWED_CIDRS; do
    if ! grep -q "^export $var=" "$SECRETS_FILE"; then
      echo "export $var=\"\"" >> "$SECRETS_FILE"
    fi
  done
fi

source "$SECRETS_FILE"

# docker compose reads variables from a `.env` next to the compose file,
# not from this shell's exports when compose is invoked from somewhere
# else. Writing it here keeps the monitoring stack consistent.
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
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
} > "$COMPOSE_ENV"
chmod 600 "$COMPOSE_ENV"

# systemd's EnvironmentFile= wants plain KEY=value lines — no `export`,
# no quotes, no command substitution. Regenerate this companion file
# from the already-resolved values every time this script runs.
{
  echo "ACS_POSTGRES_DSN=$ACS_POSTGRES_DSN"
  echo "ACS_DIGEST_USERNAME=$ACS_DIGEST_USERNAME"
  echo "ACS_DIGEST_PASSWORD=$ACS_DIGEST_PASSWORD"
  echo "ACS_CONNECTION_REQUEST_USERNAME=$ACS_CONNECTION_REQUEST_USERNAME"
  echo "ACS_CONNECTION_REQUEST_PASSWORD=$ACS_CONNECTION_REQUEST_PASSWORD"
  echo "ACS_BOOTSTRAP_ADMIN_USERNAME=$ACS_BOOTSTRAP_ADMIN_USERNAME"
  echo "ACS_BOOTSTRAP_ADMIN_PASSWORD=$ACS_BOOTSTRAP_ADMIN_PASSWORD"
  echo "ACS_JWT_SIGNING_SECRET=$ACS_JWT_SIGNING_SECRET"
  echo "ACS_CREDENTIAL_ENCRYPTION_KEY=$ACS_CREDENTIAL_ENCRYPTION_KEY"
  echo "ACS_INTERNAL_SERVICE_TOKEN=$ACS_INTERNAL_SERVICE_TOKEN"
  echo "ACS_BSS_OAUTH_SIGNING_SECRET=$ACS_BSS_OAUTH_SIGNING_SECRET"
  echo "ACS_ADDR=$ACS_ADDR"
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
  echo "ACS_USP_ALLOWED_CIDRS=$ACS_USP_ALLOWED_CIDRS"
  echo "ACS_AUTH_ALLOW_BASIC=$ACS_AUTH_ALLOW_BASIC"
  echo "ACS_TLS_MIN_VERSION=$ACS_TLS_MIN_VERSION"
  echo "ACS_TLS_CERT=$ACS_TLS_CERT"
  echo "ACS_TLS_KEY=$ACS_TLS_KEY"
} > "$SYSTEMD_FILE"
chmod 600 "$SYSTEMD_FILE"

echo ""
echo "=== ACS credentials (also saved in $SECRETS_FILE) ==="
echo "Console / API login:"
echo "  Username: $ACS_BOOTSTRAP_ADMIN_USERNAME"
echo "  Password: $ACS_BOOTSTRAP_ADMIN_PASSWORD"
echo ""
echo "CWMP Digest (device ManagementServer.Username / .Password):"
echo "  Username: $ACS_DIGEST_USERNAME"
echo "  Password: $ACS_DIGEST_PASSWORD"
echo ""
echo "Connection Request (device .ConnectionRequestUsername / .Password):"
echo "  Username: $ACS_CONNECTION_REQUEST_USERNAME"
echo "  Password: $ACS_CONNECTION_REQUEST_PASSWORD"
echo ""
echo "USP controller ID: $ACS_USP_CONTROLLER_ID"
echo "Grafana (dashboards, :3000):"
echo "  Username: admin"
echo "  Password: $GRAFANA_ADMIN_PASSWORD"
echo "======================================================="
echo ""
