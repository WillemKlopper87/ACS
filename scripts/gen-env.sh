#!/bin/bash
# Generates ACS credentials ONCE and persists them to ~/.acs-secrets.env.
# Safe to source repeatedly — cmd/acs and cmd/api are separate processes
# that must see IDENTICAL values (Digest password, JWT signing secret,
# etc). Re-running `openssl rand` on every source (the old approach)
# silently gives each process different secrets the moment they're
# started from different shells, breaking auth in a way that's hard to
# diagnose. This script generates once and reuses from then on.
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

export ACS_JWT_SIGNING_SECRET="$(openssl rand -base64 32)"
export ACS_CREDENTIAL_ENCRYPTION_KEY="$(openssl rand -base64 32)"
export ACS_INTERNAL_SERVICE_TOKEN="$(openssl rand -base64 32)"
export ACS_BSS_OAUTH_SIGNING_SECRET="$(openssl rand -base64 32)"

export ACS_ADDR=":7547"
export ACS_API_ADDR=":8080"
export ACS_STUN_ADDR=":3478"
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
  # Backfill secrets added after this file was first generated — the
  # services fail closed without them since the P0.1 hardening.
  for var in ACS_INTERNAL_SERVICE_TOKEN ACS_BSS_OAUTH_SIGNING_SECRET; do
    if ! grep -q "^export $var=" "$SECRETS_FILE"; then
      echo "Backfilling $var into $SECRETS_FILE"
      echo "export $var=\"$(openssl rand -base64 32)\"" >> "$SECRETS_FILE"
    fi
  done
fi

source "$SECRETS_FILE"

# systemd's EnvironmentFile= wants plain KEY=value lines — no `export`,
# no quotes, no command substitution. Regenerate this companion file
# from the already-resolved values every time this script runs, so it's
# always in sync with .acs-secrets.env even if you delete/regenerate.
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
echo "======================================================="
echo ""
