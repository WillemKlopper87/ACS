# ACS on AWS EC2 — Complete Deployment Guide

This guide walks through deploying the TR-069 ACS system on an Ubuntu EC2 instance, from instance launch through running all services with real devices.

## Quick start (recommended — single SSH session, no tmux needed)

### Brand new instance, nothing installed yet

Once you've done §1 (launched the instance and opened the security group
ports), SSH in and run one command — it installs every package in §2,
clones the repo (§3), and builds + starts the whole stack (§4-§6):

```bash
curl -fsSL https://raw.githubusercontent.com/WillemKlopper87/ACS/main/scripts/quickstart.sh | bash
```

Safe to rerun (e.g. after a `git pull`) — every step skips work that's
already done, and it ends by calling `scripts/start.sh`, which always
stops whatever was running first. See the comment block at the top of
`scripts/quickstart.sh` for the environment variables that control it
(`REPO_URL`, `GIT_REF`, `GO_VERSION`, `ACS_PUBLIC_IP`, etc.) — in
particular, if the repo is private, set `REPO_URL` per §3 before running
this.

If you'd rather run the steps yourself (or the one-liner above doesn't
fit your setup), §2 and §3 below cover the same ground manually.

### Already cloned, packages already installed

```bash
cd ~/ACS
./scripts/start.sh
```

This builds real binaries (not `go run`), backgrounds all three services
(cmd/acs, cmd/api, frontend) with `nohup` + PID files, and prints the
console URL and login credentials at the end. Safe to rerun any time —
it stops whatever was running first. If you pull a fresh `git clone`,
run `chmod +x scripts/*.sh` once (Windows git doesn't always preserve
the executable bit across the repo).

```bash
./scripts/logs.sh    # tail all three logs together — watch for a device's first Inform here
./scripts/stop.sh     # stop everything cleanly
```

Credentials are generated once and persisted to `~/.acs-secrets.env` —
rerunning `start.sh` reuses them rather than regenerating (regenerating
on every run would give `cmd/acs` and `cmd/api` different secrets if
they're ever started from different shells, silently breaking auth).
Delete that file and rerun `start.sh` if you want fresh credentials.

The rest of this guide explains what the script does and how to
customize/harden it — read on if you want the manual steps or you're
troubleshooting something the script doesn't cover.

## 1. EC2 Instance Setup

### 1.1 Launch Instance

- **AMI**: Ubuntu 22.04 LTS or 24.04 LTS (free tier eligible)
- **Instance type**: `t3.medium` or larger (2vCPU, 4GB RAM minimum)
  - `t3.medium` is sufficient for development and small test fleets
  - `t3.large` (2vCPU, 8GB) for real fleets or production evaluation
- **Storage**: 20 GB root volume minimum, 50+ GB recommended if storing device logs/firmware
- **Network**: Public IP (enable automatically) or Elastic IP for stability

### 1.2 Security Group — Inbound Rules

**Open these ports** to allow CPE devices, the console, and operators to reach the ACS:

| Port | Protocol | CIDR | Purpose |
|---|---|---|---|
| `7547` | TCP | `0.0.0.0/0` | **CWMP gateway** — CPE devices send Inform and RPC over this port. Must be reachable from the internet. |
| `3478` | UDP | `0.0.0.0/0` | **STUN server** — CPE devices use this for NAT traversal and UDP Connection Request binding. Optional if STUN is disabled on devices. |
| `8080` | TCP | `0.0.0.0/0` | **REST API** — the console's backend calls and direct operator API access. If behind a corporate VPN or single office, restrict to that CIDR instead. |
| `5173` | TCP | `0.0.0.0/0` | **Console (frontend)** — the actual web UI you open in a browser. Easy to miss since it's not mentioned anywhere else this early — if you can't load the console after deploying, this is the first thing to check. |
| `5432` | TCP | (none) | **PostgreSQL** — only for local connections; never expose to the internet. Leave closed. |
| `22` | TCP | `<your-ip>/32` or `0.0.0.0/0` | **SSH** — your management access. Restrict to your IP or VPN. |

**Summary**:
```
Inbound: 7547/tcp from 0.0.0.0/0  (CWMP)
Inbound: 3478/udp from 0.0.0.0/0  (STUN)
Inbound: 8080/tcp from 0.0.0.0/0  (API)
Inbound: 5173/tcp from 0.0.0.0/0  (console — restrict if you prefer)
Inbound: 22/tcp from <your-cidr>   (SSH)
Outbound: allow all (default)
```

## 2. System Setup — Ubuntu Packages

SSH into the instance and run:

```bash
#!/bin/bash
set -e

# Update package lists
sudo apt-get update

# Install system dependencies
sudo apt-get install -y \
  build-essential \
  curl \
  wget \
  git \
  ca-certificates \
  gnupg \
  lsb-release

# Install Go 1.22+
curl -fsSL https://go.dev/dl/go1.22.0.linux-amd64.tar.gz -o /tmp/go.tar.gz
sudo rm -rf /usr/local/go
sudo tar -C /usr/local -xzf /tmp/go.tar.gz
echo 'export PATH=$PATH:/usr/local/go/bin' >> ~/.bashrc
source ~/.bashrc

# Install Node.js 20 LTS (for frontend build)
curl -fsSL https://deb.nodesource.com/setup_20.x | sudo -E bash -
sudo apt-get install -y nodejs

# Install Docker (for PostgreSQL container)
curl -fsSL https://download.docker.com/linux/ubuntu/gpg | sudo gpg --dearmor -o /usr/share/keyrings/docker-archive-keyring.gpg
echo "deb [arch=$(dpkg --print-architecture) signed-by=/usr/share/keyrings/docker-archive-keyring.gpg] https://download.docker.com/linux/ubuntu $(lsb_release -cs) stable" | sudo tee /etc/apt/sources.list.d/docker.list > /dev/null
sudo apt-get update
sudo apt-get install -y docker-ce docker-ce-cli containerd.io docker-compose-plugin

# Add your user to docker group (so you don't need sudo)
sudo usermod -aG docker $USER
newgrp docker

# Install PostgreSQL client (for remote connections to the container)
sudo apt-get install -y postgresql-client

echo "System setup complete. Open a new shell or run: source ~/.bashrc"
```

Copy the script to the instance and run it, or paste the commands one by one.

## 3. Clone the Repository from GitHub

The repo (`WillemKlopper87/ACS`) is **private**, so plain `git clone` over
HTTPS with no credentials will fail with a 404/permission error. Use one of
the two options below.

### Option A: SSH deploy key (recommended)

A deploy key is a repo-scoped SSH key with no expiry, so it's the best fit
for a long-lived EC2 instance — you don't need a personal token sitting on
the box.

1. Generate a key pair on the instance (no passphrase, so it can be used
   non-interactively):
   ```bash
   ssh-keygen -t ed25519 -f ~/.ssh/acs_deploy_key -N ""
   cat ~/.ssh/acs_deploy_key.pub
   ```
2. Copy the printed public key. On GitHub, go to the `ACS` repo →
   **Settings → Deploy keys → Add deploy key**, paste it in, leave
   "Allow write access" unchecked (read-only is enough to pull), and save.
3. Tell SSH to use that key for GitHub:
   ```bash
   cat >> ~/.ssh/config <<'EOF'
   Host github.com
     IdentityFile ~/.ssh/acs_deploy_key
     IdentitiesOnly yes
   EOF
   chmod 600 ~/.ssh/config
   ssh -T git@github.com   # should say "Hi WillemKlopper87/ACS! You've successfully authenticated"
   ```
4. Clone:
   ```bash
   cd ~
   git clone git@github.com:WillemKlopper87/ACS.git
   cd ACS
   ```

### Option B: HTTPS with a personal access token

Simpler to set up but the token needs to be rotated/renewed and grants
whatever scope you gave it, so prefer Option A for anything long-lived.

1. Create a fine-grained PAT at github.com → **Settings → Developer
   settings → Personal access tokens**, scoped to just the `ACS` repo with
   read-only Contents access.
2. Clone using the token in place of a password:
   ```bash
   cd ~
   git clone https://<token>@github.com/WillemKlopper87/ACS.git
   cd ACS
   ```
   (Or clone with the plain HTTPS URL and let `git` prompt — enter your
   GitHub username and the token as the password.)

Either way, verify the clone:
```bash
ls -la
# Should show: backend/, frontend/, infra/, deployment-testing-onboarding-guide.md, etc.
```

## 4. Start PostgreSQL (Docker)

From the repo root:

```bash
cd infra
docker compose up -d postgres

# Wait a few seconds for it to initialize, then verify:
docker compose logs postgres
# Look for "database system is ready to accept connections"

# Test the connection:
psql -h localhost -U acs -d acs -c "SELECT 1"
# Password: acs (or whatever you configured in docker-compose.yml)
```

## 5. Backend Build & Setup

### 5.1 Build the Go binaries

**`scripts/start.sh` does this for you.** If building manually, always
pass an explicit `-o` — `go build ./cmd/acs` with no `-o`, run from
`backend/`, produces a binary literally named `acs` in the *current*
directory (`backend/acs`), not inside `cmd/acs/`. Running `./cmd/acs`
afterward hits the source directory instead of the binary and fails
with `Is a directory`.

```bash
cd ~/ACS/backend
mkdir -p bin
go build -o bin/acs ./cmd/acs
go build -o bin/api ./cmd/api
go build -o bin/bssadapter ./cmd/bssadapter  # optional, only for BSS integration testing
go build -o bin/probe ./cmd/probe            # optional: CLI diagnostics tool
```

Binaries land in `backend/bin/` — run them as `./bin/acs`, `./bin/api`, etc.

### 5.2 Set environment variables

Use `scripts/gen-env.sh` (in the repo root) rather than a hand-rolled
heredoc — it generates every credential **once** and persists them to
`~/.acs-secrets.env`, then prints them. This matters more than it looks:
`cmd/acs` and `cmd/api` are separate processes, and if each one sources
a script that calls `openssl rand` fresh, they end up with *different*
Digest passwords / JWT secrets the moment they're started from different
shells or at different times — auth breaks silently and it's a genuinely
confusing thing to debug. `gen-env.sh` avoids that by generating once and
reusing on every subsequent source.

```bash
source ~/ACS/scripts/gen-env.sh
```

First run generates and prints credentials; every run after that loads
the same ones from `~/.acs-secrets.env` and prints them again (handy —
you don't have to write them down, just re-source this file whenever you
need to see them).

**IMPORTANT**: `~/.acs-secrets.env` is created with `chmod 600` (owner-only),
but it's still plaintext on disk — treat it like any other credentials
file (back it up somewhere safe if you'll need it later, don't commit it
to git).

### 5.3 Run cmd/acs and cmd/api

**If you're using `scripts/start.sh` (recommended, see Quick Start at the
top of this guide), this is already done for you** — it builds and
backgrounds both processes with `nohup` so one SSH session is enough.

If you want to run them manually instead (e.g. for `screen`/`tmux`, or to
watch one in the foreground while debugging):

**Terminal 1 — CWMP gateway + STUN:**
```bash
cd ~/ACS/backend
source ~/ACS/scripts/gen-env.sh
go build -o bin/acs ./cmd/acs && ./bin/acs
```

Watch for logs like:
```
msg="CWMP gateway listening" addr=:7547
msg="stun server listening" addr=:3478
```

**Terminal 2 — REST API:**
```bash
cd ~/ACS/backend
source ~/ACS/scripts/gen-env.sh
go build -o bin/api ./cmd/api && ./bin/api
```

Watch for:
```
msg="REST API listening" addr=:8080
```

If both start cleanly (no error logs), you're ready for the next step.

Prefer a built binary (`go build` then run it) over `go run` for
anything you intend to leave running: `go run` wraps the real binary in
a subprocess, and a stray Ctrl+Z or dropped SSH session can leave that
subprocess running and holding its port (e.g. `:3478` STUN) even after
the wrapper you see in your terminal is gone — which then blocks the
next start attempt with "address already in use." A built binary has no
wrapper to lose track of.

## 6. Frontend Build & Deployment

### 6.1 Build the static site

**`scripts/start.sh` does this correctly already — read this section if
you're building manually, or if the console loads but shows "Failed to
reach the API".**

The frontend needs to know where `cmd/api` is *before* it's built —
`VITE_API_BASE_URL` gets compiled into the JS bundle at build time and
evaluated later in whoever's browser loads the page. Set it to
`localhost` and it silently means "the visitor's own machine," not this
server — every API call fails with no useful error beyond "Failed to
reach the API." Set it to your EC2 instance's actual public IP instead:

```bash
cd ~/ACS/frontend

# Replace with your instance's public IP (or domain, once you have one)
echo "VITE_API_BASE_URL=http://<ec2-public-ip>:8080" > .env.local

npm install
npm run build

# Output is in ./dist/ — a static HTML/JS/CSS bundle ready to serve
ls -la dist/
```

If you already built without this and are seeing "Failed to reach the
API" in the console: fix `.env.local` and rerun `npm run build` — the
old bundle in `dist/` has the wrong URL baked in and needs replacing,
restarting the static server alone won't fix it.

### 6.2 Serve the frontend (simple option)

**If you're using `scripts/start.sh`, this is already running.** It uses
Python's built-in server:

```bash
cd ~/ACS/frontend/dist
python3 -m http.server 5173
```

deliberately instead of `npm install -g http-server` — the global npm
install needs `sudo` on a stock Ubuntu instance (npm's default global
prefix is root-owned), which is one more permission hurdle for no real
benefit here. `python3` is already on every stock Ubuntu AMI, needs no
install, and is entirely sufficient for serving a static build.

Then open `http://<ec2-public-ip>:5173` in your browser and log in with
the admin username/password `scripts/gen-env.sh` printed.

### 6.2 Alternative: Serve with Nginx (production)

For a more robust setup:

```bash
sudo apt-get install -y nginx

# Create Nginx config
sudo tee /etc/nginx/sites-available/acs > /dev/null <<EOF
server {
    listen 5173;
    server_name _;

    location / {
        root /home/ubuntu/ACS/frontend/dist;
        try_files \$uri \$uri/ /index.html;
    }

    location /api/ {
        proxy_pass http://localhost:8080;
        proxy_http_version 1.1;
        proxy_set_header Upgrade \$http_upgrade;
        proxy_set_header Connection 'upgrade';
        proxy_set_header Host \$host;
        proxy_cache_bypass \$http_upgrade;
    }
}
EOF

# Enable the site
sudo ln -sf /etc/nginx/sites-available/acs /etc/nginx/sites-enabled/
sudo nginx -t
sudo systemctl restart nginx
```

## 7. Verify the Deployment

### 7.1 Check services are running

```bash
# CWMP gateway should respond to a ping-like request
curl -v http://localhost:7547/cwmp 2>&1 | head -20
# Expected: 405 Method Not Allowed or similar (CWMP expects SOAP, not GET)

# API should return health/metrics
curl http://localhost:8080/metrics | head
# Expected: Prometheus metrics

# Frontend should serve HTML
curl http://localhost:5173 | head
# Expected: HTML doctype and content
```

### 7.2 Log into the console

Open `http://<ec2-public-ip>:5173` in a browser.

**Login**: admin / `<ACS_BOOTSTRAP_ADMIN_PASSWORD>`

You should see the Dashboard with 0 devices (fleet is empty initially).

## 8. Configure a CPE Device to Connect

On your CPE (e.g., ZTE ZOWEE 5G CPE Max 6), set:

| TR-069 Parameter | Value |
|---|---|
| `ManagementServer.URL` | `http://<ec2-public-ip>:7547/cwmp` |
| `ManagementServer.Username` | `acs-device` (from `ACS_DIGEST_USERNAME`) |
| `ManagementServer.Password` | the value you stored from `ACS_DIGEST_PASSWORD` |
| `ManagementServer.PeriodicInformEnable` | enabled |
| `ManagementServer.PeriodicInformInterval` | 60 (seconds, for testing) |
| `ManagementServer.ConnectionRequestUsername` | `acs-connreq` |
| `ManagementServer.ConnectionRequestPassword` | the value you stored from `ACS_CONNECTION_REQUEST_PASSWORD` |
| `ManagementServer.STUNEnable` | enabled (optional) |
| `ManagementServer.STUNServerAddress` | `<ec2-public-ip>` |
| `ManagementServer.STUNServerPort` | `3478` |

Save the settings. The CPE should send a **BOOTSTRAP** Inform within moments.

### 7.3 Verify device appears in console

Watch the `cmd/acs` logs for:
```
msg="Inform received" oui_serial="..." manufacturer="..."
msg="parameter discovery queued on BOOTSTRAP"
msg="parameter discovery completed" parameter_count=N
```

Then refresh the console Dashboard — the device should appear in the "DEVICES" count and "DEVICES BY ONLINE STATUS" section.

## 9. Systemd Services (Optional — for persistence)

`scripts/start.sh` (Quick Start, top of this guide) already gives you
processes that survive a dropped SSH session — systemd is a further
step up (auto-start on instance reboot, `systemctl` control, journal
logging) once you're past initial testing.

Two things below are easy to get wrong, so this section builds them
explicitly rather than by analogy to the manual steps in §5:

1. **Binary path**: `go build -o bin/acs ./cmd/acs` (with `-o`, as used
   throughout this guide) puts the binary at `backend/bin/acs`. Without
   `-o`, `go build ./cmd/acs` from the `backend/` directory puts it at
   `backend/acs` — *not* `backend/cmd/acs/acs`, which is a directory,
   not a binary, and will fail to start with a confusing error. Always
   build with an explicit `-o` for anything systemd will run.
2. **Env file format**: systemd's `EnvironmentFile=` only understands
   plain `KEY=value` lines — no `export`, no quotes, no `$(...)` command
   substitution. `scripts/gen-env.sh` already generates a
   systemd-compatible companion file for exactly this reason:
   `~/.acs-secrets.systemd.env`. Use that one here, not
   `~/.acs-secrets.env` (which is for `source`-ing in bash and uses
   `export`/quotes that systemd won't parse correctly).

First, build the binaries with explicit output paths:
```bash
cd ~/ACS/backend
go build -o bin/acs ./cmd/acs
go build -o bin/api ./cmd/api
source ~/ACS/scripts/gen-env.sh   # also regenerates ~/.acs-secrets.systemd.env
```

### cmd/acs service

```bash
sudo tee /etc/systemd/system/acs-cwmp.service > /dev/null <<'EOF'
[Unit]
Description=TR-069 CWMP Gateway & STUN Server
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=ubuntu
WorkingDirectory=/home/ubuntu/ACS/backend
EnvironmentFile=/home/ubuntu/.acs-secrets.systemd.env
ExecStart=/home/ubuntu/ACS/backend/bin/acs
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
EOF

sudo systemctl daemon-reload
sudo systemctl enable acs-cwmp
sudo systemctl start acs-cwmp
sudo systemctl status acs-cwmp
```

### cmd/api service

```bash
sudo tee /etc/systemd/system/acs-api.service > /dev/null <<'EOF'
[Unit]
Description=TR-069 ACS REST API
After=network-online.target acs-cwmp.service
Wants=network-online.target

[Service]
Type=simple
User=ubuntu
WorkingDirectory=/home/ubuntu/ACS/backend
EnvironmentFile=/home/ubuntu/.acs-secrets.systemd.env
ExecStart=/home/ubuntu/ACS/backend/bin/api
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
EOF

sudo systemctl daemon-reload
sudo systemctl enable acs-api
sudo systemctl start acs-api
sudo systemctl status acs-api
```

If you switch to systemd, stop anything `scripts/start.sh` left running
first (`scripts/stop.sh`) — otherwise both will fight over the same
ports.

View logs:
```bash
sudo journalctl -u acs-cwmp -f
sudo journalctl -u acs-api -f
```

## 10. Troubleshooting

### Device Inform never arrives

- **Check network**: from your laptop (not on the same LAN as the device), run `curl http://<ec2-public-ip>:7547` — if it times out, the security group isn't open or the instance isn't running.
- **Check device config**: verify the URL, username, password are exact. Device logs often show the ACS URL being used — screenshot it.
- **Check ACS logs**: watch `cmd/acs` logs for connection attempts. A 401 Unauthorized means credentials don't match.
- **Check the path**: the gateway now accepts CWMP POSTs on *any* path (a device provisioned with `http://host:7547/` or `/acs` still works), but run with `ACS_DEBUG=1` to see non-standard paths logged.
- **Elastic IP**: without one, the public IP changes on every stop/start and every provisioned CPE keeps calling the old address. Allocate an Elastic IP before rolling out devices.
- **NAT/CGNAT**: the Inform is CPE→ACS (outbound from the CPE), so it works through NAT — but if Informs don't arrive at all, check whether the ISP blocks outbound 7547 or a middlebox intercepts it; try `ACS_ADDR=:443`-style alternate ports as a diagnostic.

### Device connects but the session dies after the first Inform

CPE compatibility knobs on `cmd/acs` (all optional env vars):

| Variable | Default | When to change it |
|---|---|---|
| `ACS_AUTH_ALLOW_BASIC` | off | Set to `1` for CPE firmwares that only implement HTTP **Basic** auth (some Huawei/ZTE defaults). Combine with TLS — Basic is cleartext. |
| `ACS_TLS_MIN_VERSION` | `1.0` | The CWMP TLS listener accepts TLS 1.0+ with legacy (CBC / RSA-key-exchange) cipher suites, because many deployed CPEs cannot do TLS 1.2. Raise to `1.2` once your fleet is known-modern. |
| `ACS_RATE_LIMIT_IP_PER_SECOND` / `_BURST` | 20 / 40 | Raise if many CPEs sit behind one CGNAT IP — they share a per-IP budget and can rate-limit each other out (HTTP 429 in device logs). |

The gateway also (as of this revision) echoes the CPE's `cwmp:ID` header and CWMP namespace version (`cwmp-1-0`…`cwmp-1-4`) in InformResponse/TransferCompleteResponse, and accepts whitespace-only empty POSTs — all three were previously spec deviations that made strict CPE stacks (Huawei, ZTE, MikroTik, Zyxel among others) drop the session right after the Inform.

### TLS-specific failures

- Old CPEs rarely ship your CA roots: use a **full-chain** certificate (Let's Encrypt `fullchain.pem`, not `cert.pem`), and expect some devices to need certificate validation disabled or your CA pushed to the device.
- Test the handshake the way a legacy CPE would: `openssl s_client -connect <ip>:7547 -tls1` (and `-tls1_2`). A `handshake failure` at `-tls1` with default settings means `ACS_TLS_MIN_VERSION` was raised.
- Devices that fail TLS silently often fall back to retrying forever with no server-side log — packet capture (`sudo tcpdump -i any port 7547 -w cwmp.pcap`) shows the handshake reset.

### Device shows OFFLINE in console despite Informs arriving

- Check `cmd/acs` logs for session errors.
- Verify the database connection: `psql -h localhost -U acs -d acs -c "SELECT COUNT(*) FROM devices"`

### Console won't load

- Check `cmd/api` is running: `curl http://localhost:8080/metrics`
- Check frontend build: `ls ~/ACS/frontend/dist/index.html`
- Check Nginx (if using it): `sudo nginx -t` and `sudo systemctl status nginx`

## 11. Updating from GitHub

**If you're using `scripts/start.sh`:**

```bash
cd ~/ACS
git pull origin main
./scripts/start.sh   # rebuilds binaries + frontend, restarts everything
```

**If you set up systemd services (§9) instead:**

```bash
cd ~/ACS
git pull origin main

# Rebuild binaries
cd backend
go build -o bin/acs ./cmd/acs
go build -o bin/api ./cmd/api

# Restart services
sudo systemctl restart acs-cwmp acs-api

# For frontend changes
cd ../frontend
npm install && npm run build
sudo systemctl restart nginx  # or restart your HTTP server
```

## 12. Production Hardening Checklist

Before going to production:

- [ ] Enable TLS on CWMP (set `ACS_TLS_CERT` and `ACS_TLS_KEY` to valid certificates — Let's Encrypt works)
- [ ] Enable JWT signing (`ACS_JWT_SIGNING_SECRET` is set)
- [ ] **If running `cmd/bssadapter`, set `ACS_INTERNAL_SERVICE_TOKEN`** — the same value on both `cmd/api` and `cmd/bssadapter`. Enabling `ACS_JWT_SIGNING_SECRET` (above) without this breaks BSS order dispatch and job-status lookups with `401`s the moment bssadapter calls back into the API — both processes log a `WARN` at startup if this is unset, easy to miss if you're not watching logs on first deploy.
- [ ] If running `cmd/bssadapter` for real BSS/CRM integrations, set `ACS_BSS_OAUTH_SIGNING_SECRET` and move integrations onto OAuth2 client-credentials (`bss-integration-guide.md` §3) rather than the legacy shared `ACS_BSS_API_TOKEN`
- [ ] Enable credential encryption (`ACS_CREDENTIAL_ENCRYPTION_KEY` is set) — this also covers device CLI/SSH credentials and VPN peer private keys, not just Connection Request credentials
- [ ] If using self-service password reset, configure `ACS_SMTP_HOST`/`_PORT`/`_USERNAME`/`_PASSWORD`/`_FROM` — unset, reset links are only ever written to the `cmd/api` log, not emailed
- [ ] Rotate the `ACS_BOOTSTRAP_ADMIN_PASSWORD` after initial login
- [ ] Restrict `:8080` security group to your operator IPs (not `0.0.0.0/0`)
- [ ] Set up CloudWatch logs or syslog forwarding for audit trail
- [ ] Configure RDS PostgreSQL instead of local Docker (for HA)
- [ ] Set up auto-scaling or a second instance as warm standby
- [ ] Document your deployment, credentials handling, and backup strategy

See `deployment-testing-onboarding-guide.md` §7 for the full environment variable reference (all three binaries) — this checklist only calls out the ones easy to miss.

---

**Questions?** Refer to `deployment-testing-onboarding-guide.md` in the repo for device-specific configuration details, or `bss-integration-guide.md` if integrating with a BSS/CRM system.
