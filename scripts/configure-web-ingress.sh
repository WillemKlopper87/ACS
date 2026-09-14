#!/bin/bash
# Installs the repo's Nginx site configuration and reloads Nginx.
#
# This deliberately keeps Grafana and Prometheus bound to 127.0.0.1;
# Nginx is the only public operator ingress. Prometheus has no native
# login, so it is protected with HTTP Basic Auth. To avoid creating yet
# another secret, the Basic Auth credentials are the same as Grafana's
# generated admin login: admin / $GRAFANA_ADMIN_PASSWORD.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SITE_SOURCE="$ROOT/infra/nginx/acs.conf"
SITE_AVAILABLE="/etc/nginx/sites-available/acs"
SITE_ENABLED="/etc/nginx/sites-enabled/acs"
HTPASSWD_FILE="/etc/nginx/acs-prometheus.htpasswd"

if ! command -v nginx >/dev/null 2>&1; then
  echo "nginx is not installed. Run scripts/quickstart.sh or install nginx first." >&2
  exit 1
fi
if ! command -v openssl >/dev/null 2>&1; then
  echo "openssl is required to generate the Prometheus Basic Auth hash." >&2
  exit 1
fi
if [ -z "${GRAFANA_ADMIN_PASSWORD:-}" ]; then
  echo "GRAFANA_ADMIN_PASSWORD is not set. Source scripts/gen-env.sh first." >&2
  exit 1
fi

# Nginx accepts Apache apr1 hashes in auth_basic_user_file. Generate from
# stdin so the clear-text password is not placed in the process argv.
PROM_HASH="$(printf '%s' "$GRAFANA_ADMIN_PASSWORD" | openssl passwd -apr1 -stdin)"
printf 'admin:%s\n' "$PROM_HASH" | sudo tee "$HTPASSWD_FILE" >/dev/null
sudo chown root:www-data "$HTPASSWD_FILE"
sudo chmod 640 "$HTPASSWD_FILE"

sudo install -o root -g root -m 0644 "$SITE_SOURCE" "$SITE_AVAILABLE"
sudo ln -sfn "$SITE_AVAILABLE" "$SITE_ENABLED"
# The Ubuntu package enables a default_server site which would collide
# with our own default_server listener on a fresh instance.
sudo rm -f /etc/nginx/sites-enabled/default

sudo nginx -t
sudo systemctl enable nginx >/dev/null 2>&1 || true
sudo systemctl restart nginx

echo "Nginx operator ingress is configured on port 80."
