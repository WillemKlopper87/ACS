#!/bin/bash
# Entrypoint for the lab Prosody container (infra/docker-compose.yml,
# profile "xmpp"). Runs as root only long enough to:
#   1. install the TLS certificate/key from the read-only /certs mount where
#      the prosody user can read them, and
#   2. (re)create the ACS account from ACS_XMPP_USER/ACS_XMPP_PASSWORD, so
#      the password in ~/.acs-secrets.env is always the one that works,
# then drops to the prosody user. Per-CPE accounts are created later by the
# ACS itself over XMPP (XEP-0133), not here.
set -euo pipefail

: "${ACS_XMPP_DOMAIN:?ACS_XMPP_DOMAIN must be set (run scripts/gen-env.sh)}"
: "${ACS_XMPP_PASSWORD:?ACS_XMPP_PASSWORD must be set (run scripts/gen-env.sh)}"
ACS_XMPP_USER="${ACS_XMPP_USER:-acs}"

if [ ! -s /certs/xmpp.crt ] || [ ! -s /certs/xmpp.key ]; then
  echo "prosody-entrypoint: /certs/xmpp.crt and /certs/xmpp.key are required (run scripts/xmpp-cert.sh)" >&2
  exit 1
fi
install -d -o prosody -g prosody -m 0750 /var/lib/prosody/acs-certs
install -o prosody -g prosody -m 0644 /certs/xmpp.crt /var/lib/prosody/acs-certs/xmpp.crt
install -o prosody -g prosody -m 0600 /certs/xmpp.key /var/lib/prosody/acs-certs/xmpp.key
chown -R prosody:prosody /var/lib/prosody

# prosodyctl register refuses an existing account, so drop and recreate the
# ACS account: it holds no state worth keeping (no roster, no messages).
prosodyctl deluser "$ACS_XMPP_USER@$ACS_XMPP_DOMAIN" >/dev/null 2>&1 || true
prosodyctl register "$ACS_XMPP_USER" "$ACS_XMPP_DOMAIN" "$ACS_XMPP_PASSWORD"
echo "prosody-entrypoint: ACS account $ACS_XMPP_USER@$ACS_XMPP_DOMAIN ready"

exec runuser -u prosody -- prosody
