#!/bin/bash
# One-shot deployment: turns a bare Ubuntu 22.04/24.04 EC2 instance into a
# running ACS stack. Covers everything in EC2-DEPLOYMENT-GUIDE.md §2-§7
# except the AWS-console steps (launching the instance, opening security
# group ports) — those can't be done from inside the instance.
#
# The host deployment starts the complete control plane: CWMP/STUN,
# operator API, BSS/TMF adapter, USP controller, frontend, PostgreSQL,
# Prometheus, Alertmanager and Grafana. The quickstart publishes the
# monitoring UIs directly by default; set ACS_MONITORING_PUBLIC=0 to keep
# Grafana and Prometheus localhost-only.
#
# Usage (on a fresh instance, logged in as the `ubuntu` user):
#   curl -fsSL https://raw.githubusercontent.com/WillemKlopper87/ACS/main/scripts/quickstart.sh | bash
# or, if you've already cloned:
#   ./scripts/quickstart.sh
#
# Safe to rerun: every step is idempotent and scripts/start.sh stops the
# previous application processes before starting the new revision.
#
# Override via environment variables:
#   REPO_URL     git URL to clone
#   GIT_REF      branch/tag/commit to check out (default: main)
#   INSTALL_DIR  where to clone/find the repo (default: ~/ACS)
#   GO_VERSION   Go toolchain to install (default: matches backend/go.mod)
#   ACS_PUBLIC_IP  passed through to scripts/start.sh — set this if the
#                instance isn't on EC2 or IMDS is blocked
#   ACS_MONITORING_PUBLIC  1 (default) publishes Grafana :3000 and
#                Prometheus :9090; 0 keeps them localhost-only
set -e

if [ "$(id -u)" -eq 0 ]; then
  echo "Run this as a regular sudo-capable user (e.g. 'ubuntu'), not as root/sudo." >&2
  echo "It calls sudo itself for the steps that need it." >&2
  exit 1
fi

REPO_URL="${REPO_URL:-https://github.com/WillemKlopper87/ACS.git}"
GIT_REF="${GIT_REF:-main}"
INSTALL_DIR="${INSTALL_DIR:-$HOME/ACS}"
GO_VERSION="${GO_VERSION:-1.26.6}"
ACS_MONITORING_PUBLIC="${ACS_MONITORING_PUBLIC:-1}"

case "$(uname -m)" in
  x86_64) GO_ARCH=amd64 ;;
  aarch64) GO_ARCH=arm64 ;;
  *) echo "Unsupported architecture: $(uname -m)" >&2; exit 1 ;;
esac

echo "=================================================="
echo "  ACS Quick Start — full EC2 deployment"
echo "=================================================="

echo ""
echo "=== 1/6: Base system packages ==="
sudo apt-get update
sudo apt-get install -y \
  build-essential \
  curl \
  wget \
  git \
  ca-certificates \
  gnupg \
  lsb-release \
  postgresql-client

echo ""
echo "=== 2/6: Go $GO_VERSION ==="
CURRENT_GO="$(/usr/local/go/bin/go version 2>/dev/null | awk '{print $3}' | sed 's/^go//')"
if [ "$CURRENT_GO" = "$GO_VERSION" ]; then
  echo "Go $GO_VERSION already installed, skipping."
else
  curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-${GO_ARCH}.tar.gz" -o /tmp/go.tar.gz
  sudo rm -rf /usr/local/go
  sudo tar -C /usr/local -xzf /tmp/go.tar.gz
  rm -f /tmp/go.tar.gz
fi
grep -q '/usr/local/go/bin' ~/.bashrc || echo 'export PATH=$PATH:/usr/local/go/bin' >> ~/.bashrc
export PATH="$PATH:/usr/local/go/bin"

echo ""
echo "=== 3/6: Node.js 22 LTS ==="
if command -v node >/dev/null && [ "$(node --version | cut -d. -f1)" = "v22" ]; then
  echo "Node 22 already installed, skipping."
else
  curl -fsSL https://deb.nodesource.com/setup_22.x | sudo -E bash -
  sudo apt-get install -y nodejs
fi

echo ""
echo "=== 4/6: Docker ==="
if command -v docker >/dev/null; then
  echo "Docker already installed, skipping."
else
  curl -fsSL https://download.docker.com/linux/ubuntu/gpg | sudo gpg --dearmor -o /usr/share/keyrings/docker-archive-keyring.gpg
  echo "deb [arch=$(dpkg --print-architecture) signed-by=/usr/share/keyrings/docker-archive-keyring.gpg] https://download.docker.com/linux/ubuntu $(lsb_release -cs) stable" | sudo tee /etc/apt/sources.list.d/docker.list > /dev/null
  sudo apt-get update
  sudo apt-get install -y docker-ce docker-ce-cli containerd.io docker-compose-plugin
fi
if ! groups "$USER" | grep -q docker; then
  sudo usermod -aG docker "$USER"
fi

echo ""
echo "=== 5/6: Clone/update the repository ==="
if [ -d "$INSTALL_DIR/.git" ]; then
  echo "Existing clone found at $INSTALL_DIR, pulling latest $GIT_REF."
  git -C "$INSTALL_DIR" fetch origin "$GIT_REF"
  git -C "$INSTALL_DIR" checkout "$GIT_REF"
  git -C "$INSTALL_DIR" pull origin "$GIT_REF"
else
  git clone --branch "$GIT_REF" "$REPO_URL" "$INSTALL_DIR"
fi
chmod +x "$INSTALL_DIR"/scripts/*.sh
# Load the persisted credentials/settings in this shell as well as in
# start.sh so the final summary can show the exact values in use.
# shellcheck disable=SC1090
source "$INSTALL_DIR/scripts/gen-env.sh"

GOMOD_GO="$(awk '/^go /{print $2; exit}' "$INSTALL_DIR/backend/go.mod" 2>/dev/null || true)"
if [ -n "$GOMOD_GO" ] && [ "$GOMOD_GO" != "$GO_VERSION" ]; then
  echo ""
  echo "NOTE: this script installed Go $GO_VERSION, but backend/go.mod asks for $GOMOD_GO."
  echo "      GOTOOLCHAIN=auto can fetch $GOMOD_GO on demand, but align GO_VERSION"
  echo "      with backend/go.mod to avoid the extra download."
fi

echo ""
echo "=== 6/6: Build, start and verify the stack ==="
# The docker group membership added in step 4 doesn't apply to this
# already-running shell. `sg` starts the stack with that group active.
sg docker -c "cd '$INSTALL_DIR' && ACS_PUBLIC_IP='${ACS_PUBLIC_IP:-}' ACS_GRAFANA_PUBLIC='$ACS_MONITORING_PUBLIC' ACS_PROMETHEUS_PUBLIC='$ACS_MONITORING_PUBLIC' ./scripts/start.sh"

echo ""
echo "=================================================="
echo "  Quick start complete — readiness gate passed."
echo "=================================================="
if [ "$ACS_MONITORING_PUBLIC" = "1" ]; then
  echo "Grafana and Prometheus were published directly on the ACS public IP:"
  echo "  Grafana:    http://<public-ip>:3000"
  echo "  Prometheus: http://<public-ip>:9090"
  echo "Restrict those ports to your operator/test CIDR."
else
  echo "Grafana and Prometheus were kept on localhost only."
fi

echo ""
echo "CPE management endpoints:"
echo "  CWMP ACS URL: http://<public-ip>:7547/cwmp"
echo "  STUN:         <public-ip>:3478/udp"
echo "  USP WebSocket: ws://<public-ip>:9877/usp"
echo "  USP MQTT:      <public-ip>:1883"
echo "  USP controller ID: $ACS_USP_CONTROLLER_ID"
echo ""
echo "CWMP credentials:"
echo "  Username: $ACS_DIGEST_USERNAME"
echo "  Password: $ACS_DIGEST_PASSWORD"
echo "  Connection-request username: $ACS_CONNECTION_REQUEST_USERNAME"
echo "  Connection-request password: $ACS_CONNECTION_REQUEST_PASSWORD"
echo "  Credentials/settings file: ~/.acs-secrets.env"
echo ""
echo "The BSS/TMF adapter binds to 127.0.0.1:8090 by default. Expose it only"
echo "through an authenticated TLS reverse proxy or similarly controlled"
echo "northbound path. Its health endpoint is checked automatically."
echo ""
echo "SECURITY: this quickstart intentionally opts USP into plaintext for"
echo "controlled lab/field testing. For production, set ACS_USP_TLS_CERT and"
echo "ACS_USP_TLS_KEY, set ACS_USP_ALLOW_PLAINTEXT=false, restrict"
echo "ACS_USP_ALLOWED_CIDRS, and terminate operator/API/BSS traffic behind TLS."
echo ""
echo "Reminder — this script cannot open EC2 security group ports for you."
echo "For a controlled CPE test, allow only the required source CIDRs to"
echo "7547/tcp, 3478/udp, 9877/tcp and/or 1883/tcp. The dev UI/API ports"
echo "5173/8080 and monitoring 3000/9090 should be operator-CIDR restricted;"
echo "8090 and 8092 are host-local by default."
echo ""
echo "Re-run '$INSTALL_DIR/scripts/healthcheck.sh' at any time to verify the stack."
