#!/bin/bash
# One-shot deployment: turns a bare Ubuntu 22.04/24.04 EC2 instance into a
# running ACS stack. Covers everything in EC2-DEPLOYMENT-GUIDE.md §2-§7
# except the AWS-console steps (launching the instance, opening security
# group ports) — those can't be done from inside the instance.
#
# Dev quickstart publishes the monitoring UIs directly on the same public
# IP as ACS, using their normal ports:
#   http://<public-ip>:3000  Grafana
#   http://<public-ip>:9090  Prometheus
# Set ACS_MONITORING_PUBLIC=0 before invoking this script to keep both on
# localhost instead. Prometheus has no login in this direct dev mode, so
# restrict 9090/tcp to your own test IP/CIDR in the EC2 security group.
#
# Usage (on a fresh instance, logged in as the `ubuntu` user):
#   curl -fsSL https://raw.githubusercontent.com/WillemKlopper87/ACS/main/scripts/quickstart.sh | bash
# or, if you've already cloned:
#   ./scripts/quickstart.sh
#
# Safe to rerun: every step is idempotent (skips work that's already done),
# and it ends by calling scripts/start.sh, which itself always stops any
# previous run first. Rerunning this after `git pull` is the supported way
# to pick up a new commit (see §11 "Updating from GitHub" in the guide).
#
# Override via environment variables:
#   REPO_URL     git URL to clone (default: the public HTTPS URL below —
#                if the repo is private, set this to the SSH form
#                (git@github.com:WillemKlopper87/ACS.git) with a deploy
#                key already installed, or an HTTPS URL with a PAT
#                embedded — see EC2-DEPLOYMENT-GUIDE.md §3)
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

GOMOD_GO="$(awk '/^go /{print $2; exit}' "$INSTALL_DIR/backend/go.mod" 2>/dev/null || true)"
if [ -n "$GOMOD_GO" ] && [ "$GOMOD_GO" != "$GO_VERSION" ]; then
  echo ""
  echo "NOTE: this script installed Go $GO_VERSION, but backend/go.mod asks for $GOMOD_GO."
  echo "      The build still works — GOTOOLCHAIN=auto fetches $GOMOD_GO on demand — but the"
  echo "      first 'go build' will download a second toolchain. Update GO_VERSION in this"
  echo "      script to $GOMOD_GO to avoid that."
fi

echo ""
echo "=== 6/6: Build and start the stack ==="
# The docker group membership added in step 4 doesn't apply to this
# already-running shell. `sg` starts the stack with that group active.
sg docker -c "cd '$INSTALL_DIR' && ACS_PUBLIC_IP='$ACS_PUBLIC_IP' ACS_GRAFANA_PUBLIC='$ACS_MONITORING_PUBLIC' ACS_PROMETHEUS_PUBLIC='$ACS_MONITORING_PUBLIC' ./scripts/start.sh"

echo ""
echo "=================================================="
echo "  Quick start complete."
echo "=================================================="
if [ "$ACS_MONITORING_PUBLIC" = "1" ]; then
  echo "Grafana and Prometheus were published directly on the ACS public IP:"
  echo "  Grafana:    http://<public-ip>:3000"
  echo "  Prometheus: http://<public-ip>:9090"
  echo "The exact detected URLs and Grafana credentials were printed above."
else
  echo "Grafana and Prometheus were kept on localhost only."
fi
echo ""
echo "Reminder — this script cannot open EC2 security group ports for you."
echo "For the dev quickstart, allow inbound 7547/tcp, 3478/udp, 8080/tcp,"
echo "5173/tcp, 3000/tcp and 9090/tcp. Restrict 3000/9090 to your test"
echo "IP/CIDR where possible; Prometheus :9090 is unauthenticated in this mode."
