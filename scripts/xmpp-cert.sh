#!/bin/bash
# Creates the lab XMPP server's self-signed TLS certificate once, in
# infra/prosody/certs/ (git-ignored), for the Prosody container
# (docker-compose.yml, profile "xmpp"). The ACS pins exactly this
# certificate (ACS_XMPP_TLS_CA_FILE) instead of skipping verification. A CPE
# cannot be given this CA over TR-069, so the field test either relaxes the
# CPE's certificate check explicitly (provisioning flag
# relax_certificate_verification) or replaces these two files with a
# CA-issued certificate for the XMPP domain (same paths, then restart the
# container).
#
# Usage: scripts/xmpp-cert.sh [extra-SAN-IP-or-host ...]
#   ACS_XMPP_DOMAIN (required) becomes the CN and first DNS SAN; the public
#   IP is added as an IP SAN when given, for CPEs that check the address
#   they dialled. Existing files are left alone; delete them to regenerate.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CERT_DIR="${ACS_XMPP_CERT_DIR:-$ROOT/infra/prosody/certs}"
: "${ACS_XMPP_DOMAIN:?ACS_XMPP_DOMAIN must be set (source scripts/gen-env.sh)}"

if [ -s "$CERT_DIR/xmpp.crt" ] && [ -s "$CERT_DIR/xmpp.key" ]; then
  echo "XMPP TLS certificate already present in $CERT_DIR (delete xmpp.crt/xmpp.key to regenerate)"
  exit 0
fi

SAN="DNS:$ACS_XMPP_DOMAIN"
for extra in "$@"; do
  [ -z "$extra" ] && continue
  if [[ "$extra" =~ ^[0-9.]+$ || "$extra" == *:* ]]; then
    SAN="$SAN,IP:$extra"
  else
    SAN="$SAN,DNS:$extra"
  fi
done

mkdir -p "$CERT_DIR"
chmod 700 "$CERT_DIR"
# RSA 2048 rather than ECDSA: embedded CPE TLS stacks support it universally.
openssl req -x509 -newkey rsa:2048 -nodes -days 825 -sha256 \
  -subj "/CN=$ACS_XMPP_DOMAIN" \
  -addext "subjectAltName=$SAN" \
  -addext "extendedKeyUsage=serverAuth" \
  -keyout "$CERT_DIR/xmpp.key" -out "$CERT_DIR/xmpp.crt" >/dev/null 2>&1
chmod 600 "$CERT_DIR/xmpp.key"
chmod 644 "$CERT_DIR/xmpp.crt"
echo "Created self-signed XMPP certificate for $SAN in $CERT_DIR"
