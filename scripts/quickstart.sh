#!/bin/bash
# One-shot deployment: turns a bare Ubuntu 22.04/24.04 EC2 instance into a
# running ACS stack. Covers everything in EC2-DEPLOYMENT-GUIDE.md §2-§7
# except the AWS-console steps (launching the instance, opening security
# group ports) — those can't be done from inside the instance.
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
#                instance isn't on EC2 or IMDS is blocked (start.sh will
#                tell you if it can't auto-detect one)
set -e

if [ "$(id -u)" -eq 0 ]; then
  echo "Run this as a regular sudo-capable user (e.g. 'ubuntu'), not as root/sudo." >&2
  echo "It calls sudo itself for the steps that need it." >&2
  exit 1
fi

REPO_URL="${REPO_URL:-https://github.com/WillemKlopper87/ACS.git}"
GIT_REF="${GIT_REF:-main}"
INSTALL_DIR="${INSTALL_DIR:-$HOME/ACS}"
GO_VERSION="${GO_VERSION:-1.26.5}"

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
# go.mod pins a specific version (currently 1.26.5) — install that exact
# toolchain rather than an older "1.22+" minimum. Go's automatic toolchain
# switching (GOTOOLCHAIN=auto, the default since 1.21) *would* fetch the
# right version on first build even if this installed an older one, but
# that means the first `go build` silently downloads a second toolchain
# over the network — installing the pinned version up front avoids that
# surprise and matches exactly what the repo was built/tested against.
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
echo "=== 3/6: Node.js 20 LTS ==="
if command -v node >/dev/null && [ "$(node --version | cut -d. -f1)" = "v20" ]; then
  echo "Node 20 already installed, skipping."
else
  curl -fsSL https://deb.nodesource.com/setup_20.x | sudo -E bash -
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

echo ""
echo "=== 6/6: Build and start the stack ==="
# The docker group membership added in step 4 doesn't apply to this
# already-running shell (that normally needs a fresh login) — `sg`
# runs start.sh as if that login already happened, so a freshly
# provisioned instance can go from zero to running in one pass with no
# manual re-login step in between.
sg docker -c "cd '$INSTALL_DIR' && ACS_PUBLIC_IP='$ACS_PUBLIC_IP' ./scripts/start.sh"

echo ""
echo "=================================================="
echo "  Quick start complete."
echo "=================================================="
echo "Reminder — this script cannot open EC2 security group ports for you."
echo "Make sure inbound 7547/tcp, 3478/udp, 8080/tcp, 5173/tcp are open"
echo "(see EC2-DEPLOYMENT-GUIDE.md §1.2) or devices/console/API won't be reachable."
