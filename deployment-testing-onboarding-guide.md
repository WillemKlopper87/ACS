# Deployment, testing & device onboarding guide

Written for the first real-device test: a ZTE-family "ZOWEE 5G CPE Max 6"
(model H362-383), reachable only outbound (CGNAT), with developer/engineer
access to its TR-069 client settings. Everything here is grounded in what's
actually in the repo today (env vars, ports, endpoints) — nothing invented.
Where I can't verify something (the device's exact menu labels), I say so
rather than guess.

## 1. What you're deploying

Three Go binaries (`backend/cmd/...`), one Postgres database, one React
frontend:

| Component | Binary | Default port | Protocol | Who talks to it |
|---|---|---|---|---|
| CWMP gateway | `cmd/acs` | `:7547` (HTTP/HTTPS) | CWMP/SOAP | the CPE (outbound Inform) |
| STUN server | `cmd/acs` (same process) | `:3478` (UDP) | RFC 5389 STUN | the CPE's STUN client, if enabled |
| REST API | `cmd/api` | `:8080` | HTTP/JSON (put a TLS proxy in front) | the console (frontend), and operator tooling |
| BSS adapter | `cmd/bssadapter` | `ACS_BSS_ADDR` | HTTP/JSON | your external BSS, if wired up — not required for device testing |
| Console | `frontend` (Vite) | `:5173` dev / static build | HTTP (put a TLS proxy in front) | you, in a browser |
| Database | Postgres 18 | `:5432` | — | `cmd/acs`, `cmd/api` |

By default only Postgres/Prometheus/Grafana run in Docker
(`infra/docker-compose.yml`); `cmd/acs`, `cmd/api`, `cmd/bssadapter` run
directly on the host (`go run ./cmd/acs`, or a compiled binary in a real
deployment) — everything below assumes that workflow, unchanged.

**Container images now exist** (`backend/Dockerfile.acs`,
`Dockerfile.api`, `Dockerfile.bssadapter`, `frontend/Dockerfile` —
multi-stage builds, static Go binaries on a distroless base, nginx for
the frontend) as an alternative, opt-in via
`docker compose --profile containerized up -d --build` from `infra/`
(copy `infra/.env.containerized.example` to `infra/.env.containerized`
and fill in real secrets first — see that file for the minimum set).
Prometheus scrapes both the host-based and containerized target names,
so either mode works without editing `prometheus.yml`. Everything in
this guide from here on describes the host-based workflow, which is
still the default and the one most tested in practice.

## 2. The one decision that matters most: how does the CPE reach the ACS?

Your device is behind CGNAT, but that's actually the *easy* direction — a
CGNAT'd device can always dial **out**. What matters is that
`cmd/acs`'s `:7547` (and `:3478` UDP if you want STUN) must be reachable
from wherever the CPE's mobile/WAN connection can reach — i.e. the public
internet, not just your LAN. If you're running `cmd/acs` on this Windows
machine, pick one:

- **Public cloud VM (recommended for anything beyond a one-off test)** —
  run `cmd/acs`/`cmd/api`/Postgres on a small VM with a real public IP.
  Cleanest option, no NAT traversal needed on *your* side at all, and it's
  what you'd want for a persistent test fleet anyway.
- **Tunnel (fastest to get going today)** — keep running everything on
  this machine, expose `:7547` (and optionally `:3478` UDP) via a tunnel
  (e.g. Cloudflare Tunnel, ngrok TCP tunnel — ngrok's free tier doesn't do
  UDP, so STUN needs a paid tier or a different tunnel product if you go
  this route). The device's ACS URL becomes the tunnel's public hostname.
- **Router port-forward** — if you control the router/firewall in front of
  this machine and it has a public IP (not itself behind CGNAT), forward
  `7547/tcp` and `3478/udp` to this machine.

Whichever you pick, note the **public URL** you'll put in the device's ACS
URL field — everything below calls it `<ACS_URL>` (e.g.
`http://acs.example.com:7547/cwmp`) and `<STUN_HOST>` for the STUN address.

## 3. Backend deployment

```bash
cd ACS/infra
docker compose up -d postgres        # add prometheus/grafana too if you want metrics dashboards

cd ../backend
export ACS_POSTGRES_DSN="postgres://acs:acs@localhost:5432/acs?sslmode=disable"
```

Set these before starting `cmd/acs` and `cmd/api` — this is the realistic
minimum for a device test, not the full production list (see §7 for
everything else that's available):

```bash
# Auth for the CPE-to-ACS CWMP channel (Digest — set a real value; TLS/mTLS
# is the stronger option but Digest is enough to get a first device talking)
export ACS_DIGEST_USERNAME="acs-device"
export ACS_DIGEST_PASSWORD="<pick something>"

# Auth for ACS-to-CPE Connection Requests (separate credential pair)
export ACS_CONNECTION_REQUEST_USERNAME="acs-connreq"
export ACS_CONNECTION_REQUEST_PASSWORD="<pick something>"

# First operator account for the console/API
export ACS_BOOTSTRAP_ADMIN_USERNAME="admin"
export ACS_BOOTSTRAP_ADMIN_PASSWORD="<pick something>"

# Signs operator JWTs — required, cmd/api refuses to start meaningfully secure without it
export ACS_JWT_SIGNING_SECRET="<random 32+ byte string>"

# Encrypts device_credentials.password at rest (AES-256-GCM) — optional but recommended
export ACS_CREDENTIAL_ENCRYPTION_KEY="<any passphrase>"
```

Then:

```bash
go run ./cmd/acs    # CWMP gateway + STUN server, :7547 and :3478
go run ./cmd/api    # REST API, :8080
```

Watch the startup logs — every security-relevant thing that's off gets a
loud `WARN` (this project's convention: silent-by-default gates always log
when they're disabled, e.g. "CWMP gateway listening WITHOUT TLS"). If you
skipped any of the vars above, you'll see it immediately.

**If you're not fronting this with TLS yet** (`ACS_TLS_CERT`/`ACS_TLS_KEY`
unset): fine for a first test, but Digest auth over plain HTTP means
credentials cross the CGNAT/internet path unencrypted. Get a cert (even a
free Let's Encrypt one on a real domain) before anything beyond a quick
smoke test — genuinely easy to add once you're on a public host with a
domain name.

## 4. Frontend deployment

```bash
cd ACS/frontend
echo "VITE_API_BASE_URL=http://localhost:8080" > .env.local   # point at wherever cmd/api actually runs
npm install
npm run dev       # or `npm run build` + serve the static output for anything persistent
```

Log in with the `ACS_BOOTSTRAP_ADMIN_USERNAME`/`PASSWORD` pair from §3.

## 5. Configuring the device

TR-069 client settings on consumer/field CPE are almost always tucked under
an "Advanced" or "Remote Management" section, sometimes gated behind an
engineer/developer login distinct from the normal admin login — you
mentioned you already have that access, which is exactly what's needed
here. **I don't have verified documentation for this specific ZOWEE/ZTE
H362-383 rebrand's exact menu labels** — rather than guess a path that
might be wrong for your firmware, here's what to look for by the
*parameter names themselves* (these are standardized TR-069 field names,
so most vendor UIs either show them directly or use very recognizable
labels for them):

| What to set | Standardized name | Value |
|---|---|---|
| ACS URL | `ManagementServer.URL` | `<ACS_URL>` from §2, e.g. `http://acs.example.com:7547/cwmp` |
| ACS username | `ManagementServer.Username` | `ACS_DIGEST_USERNAME` from §3 |
| ACS password | `ManagementServer.Password` | `ACS_DIGEST_PASSWORD` from §3 |
| Periodic Inform | `ManagementServer.PeriodicInformEnable` | enabled |
| Periodic Inform interval | `ManagementServer.PeriodicInformInterval` | **60–300 seconds for testing** (see §8 on why this matters right now) |
| Connection Request username | `ManagementServer.ConnectionRequestUsername` | `ACS_CONNECTION_REQUEST_USERNAME` from §3 |
| Connection Request password | `ManagementServer.ConnectionRequestPassword` | `ACS_CONNECTION_REQUEST_PASSWORD` from §3 |
| STUN enable (if offered) | `ManagementServer.STUNEnable` | enabled |
| STUN server address (if offered) | `ManagementServer.STUNServerAddress` | `<STUN_HOST>` from §2 |
| STUN server port (if offered) | `ManagementServer.STUNServerPort` | `3478` (or whatever you exposed) |

If the device's UI doesn't expose STUN settings at all, that's fine —
leave it off. Periodic Inform alone is enough for everything in §8's
testing flow; STUN only affects *instant* on-demand actions, which aren't
fully wired up on the ACS side yet either (see §9).

Saving these settings should make the CPE send a **BOOTSTRAP** Inform
(TR-069's own trigger: "the ACS URL changed") within moments — that's the
single event that kicks off both auto-provisioning and parameter discovery
on the ACS side, so it's the moment to watch the logs.

## 6. Verifying it worked

Watch `cmd/acs`'s log output the moment you save the device's settings.
You're looking for, in order:

1. `"Inform received"` with your device's real manufacturer/serial —
   confirms the CPE reached the ACS at all (the network path from §2
   works).
2. `"parameter discovery queued on BOOTSTRAP"` then shortly after
   `"parameter discovery completed"` with a real `parameter_count` — this
   is the feature that auto-detects whether your device speaks TR-181
   (`Device.`) or TR-098 (`InternetGatewayDevice.`); either is fine, it's
   just good to know which for later template/policy work.

Then from the console (or `curl`):

```bash
curl -s http://localhost:8080/api/v1/devices?page_size=50 -H "Authorization: Bearer <token>" | jq
```

Confirm: `online_status: "ONLINE"`, `data_model_root` is `DEVICE2` or
`IGD1` (not `UNKNOWN`), and `last_inform_at` is recent. If STUN was
configured, also check `udp_connection_request_address` and
`nat_detected` — a real value there (not `null`) means the device actually
completed a STUN binding, which is worth knowing even before instant push
works, since it tells you whether Annex G is realistically usable on this
hardware at all.

Then queue a harmless real action from the console (Device Detail →
Diagnostics → Ping, or the raw API `POST
/devices/{id}/diagnostics/ping`) and watch it complete on the device's
*next* periodic Inform (see §8) — that closes the loop end to end.

## 7. Full environment variable reference

Updated 2026-08-30 for the audit hardening pass (see the `# hardening` block at the end, and note that every service now **fails closed** on missing/placeholder secrets unless `ACS_INSECURE_DEV_MODE=true`). The 2026-08-11 list predated the admin-platform backlog (RBAC, tenancy, VPN/CLI/web-GUI, BSS OAuth2, mailer) and was missing everything it added. This list is grepped directly from every `os.Getenv`/`envOr*` call in `cmd/acs`, `cmd/api`, and `cmd/bssadapter` as of this date, grouped by which binary reads it:

```
# cmd/acs (CWMP gateway + STUN server)
ACS_POSTGRES_DSN, ACS_ADDR, ACS_STUN_ADDR, ACS_TLS_CERT, ACS_TLS_KEY,
ACS_TLS_MIN_VERSION, ACS_MTLS_CA_CERT, ACS_DIGEST_USERNAME,
ACS_DIGEST_PASSWORD, ACS_AUTH_ALLOW_BASIC, ACS_CONNECTION_REQUEST_USERNAME,
ACS_CONNECTION_REQUEST_PASSWORD, ACS_RATE_LIMIT_IP_PER_SECOND,
ACS_RATE_LIMIT_IP_BURST, ACS_RATE_LIMIT_DEVICE_PER_SECOND,
ACS_RATE_LIMIT_DEVICE_BURST, ACS_WALLED_GARDEN_PARAMETER,
ACS_WALLED_GARDEN_ACTIVE_VALUE, ACS_WALLED_GARDEN_SUSPEND_VALUE,
ACS_ONBOARDING_LISTENER, ACS_DEBUG

# ACS_ONBOARDING_LISTENER: off (default), on, or once. When on/once, the
# ACS logs each request that reaches the real CWMP endpoint. once stops the
# extra reachability logging after the first successful Inform. This is an
# observer for fault-finding; it does not block or alter normal CWMP traffic.

# cmd/api (REST API — operators, tenancy, dashboards, BSS admin panel, CLI/VPN/webgui)
ACS_API_ADDR, ACS_API_CORS_ORIGIN, ACS_API_RATE_LIMIT_PER_SECOND,
ACS_API_RATE_LIMIT_BURST, ACS_BOOTSTRAP_ADMIN_USERNAME,
ACS_BOOTSTRAP_ADMIN_PASSWORD, ACS_JWT_SIGNING_SECRET,
ACS_INTERNAL_SERVICE_TOKEN (bridges cmd/bssadapter's calls into this API —
  set identically on both processes, see §9 of the build plan; without it,
  bssadapter gets 401'd once ACS_JWT_SIGNING_SECRET is set),
ACS_CREDENTIAL_ENCRYPTION_KEY, ACS_FIRMWARE_STORAGE_ROOT,
ACS_FIRMWARE_BASE_URL, ACS_UPLOAD_STORAGE_ROOT, ACS_UPLOAD_BASE_URL,
ACS_FRONTEND_BASE_URL (default http://localhost:5173 — used to build the
  link in password-reset emails), ACS_BSS_ADAPTER_URL (default
  http://localhost:8090 — cmd/api's BSS admin panel proxies troubleshooting
  calls to bssadapter through this), ACS_BSS_API_TOKEN (shared with
  bssadapter, used by the admin panel proxy),
ACS_SMTP_HOST, ACS_SMTP_PORT, ACS_SMTP_USERNAME, ACS_SMTP_PASSWORD,
ACS_SMTP_FROM (all optional — unset means password-reset emails are
  logged, not sent; see internal/mailer),
ACS_VPN_OVERLAY_SUBNET, ACS_VPN_SERVER_ENDPOINT, ACS_VPN_SERVER_PUBLIC_KEY
  (VPN concentrator peer config — see build plan §9; no OS-level tunnel
  process consumes these yet)

# cmd/bssadapter (BSS/CRM adapter)
ACS_BSS_ADDR, ACS_BSS_API_TOKEN (legacy shared token, deprecated),
ACS_BSS_OAUTH_SIGNING_SECRET (enables the OAuth2 client-credentials token
  endpoint — recommended over the shared token, see bss-integration-guide.md §3),
ACS_BSS_TLS_CERT, ACS_BSS_TLS_KEY, ACS_BSS_MTLS_CA_CERT,
ACS_BSS_RATE_LIMIT_PER_SECOND, ACS_BSS_RATE_LIMIT_BURST,
ACS_INTERNAL_API_URL (where cmd/api lives, default http://localhost:8080),
ACS_INTERNAL_SERVICE_TOKEN (same value as cmd/api's, above)

# cmd/probe only
ACS_RESULTS_FILE

# hardening (added 2026-08-30 — ACS_CODEBASE_AUDIT_2026-08-28.md)
ACS_INSECURE_DEV_MODE (literal "true" disables secret enforcement — isolated
  local development ONLY; otherwise JWT/encryption/service-token secrets are
  required on cmd/api, Digest password or mTLS CA on cmd/acs, OAuth secret or
  shared token plus service token on cmd/bssadapter; placeholders like
  change-me and too-short values are refused at startup),
ACS_DEVICE_NET_ALLOWED_CIDRS (cmd/api — comma-separated device networks the
  web-GUI proxy and SSH/Telnet bridge may dial; loopback/link-local/metadata
  are always refused),
ACS_CREDENTIAL_ENCRYPTION_KEY is now also read by cmd/acs (decrypts per-device
  CWMP_DIGEST credentials — set the same value on both processes),
ACS_UPLOAD_MAX_BYTES (cmd/api — CPE upload receipt ceiling, default 256 MiB),
ACS_OBJECT_STORE (cmd/api — local [default] or s3), ACS_S3_BUCKET, ACS_S3_REGION,
  ACS_S3_ENDPOINT (MinIO/other S3-compatible), ACS_S3_PATH_STYLE=true (endpoints
  without bucket DNS); credentials via the standard AWS chain (env/shared
  config/instance role). Firmware lives under firmware/, uploads under uploads/;
  with s3 the backup script's file-store tarballs are unnecessary — use bucket
  versioning/lifecycle instead,
ACS_DB_MAX_OPEN_CONNS, ACS_DB_MAX_IDLE_CONNS, ACS_DB_CONN_MAX_LIFETIME,
ACS_DB_CONN_MAX_IDLE_TIME (all services — pool limits; defaults 20/5/30m/5m),
ACS_RETENTION_SESSIONS_DAYS, ACS_RETENTION_AUDIT_LOG_DAYS,
ACS_RETENTION_PARAMETER_HISTORY_DAYS, ACS_RETENTION_WEBHOOK_DELIVERIES_DAYS,
ACS_RETENTION_FINISHED_JOBS_DAYS, ACS_RETENTION_STALE_UPLOAD_SLOTS_DAYS,
ACS_RETENTION_RESET_TOKENS_DAYS (cmd/api — pruning windows, 0 disables;
  defaults 30/365/90/30/90/7/1),
ACS_API_CORS_ORIGIN now defaults to ACS_FRONTEND_BASE_URL rather than "*"

# docker compose (infra/docker-compose.yml) — shell/.env, not the app env file
GRAFANA_ADMIN_PASSWORD (required), ACS_POSTGRES_PASSWORD (default acs),
ACS_ALERT_WEBHOOK_URL (Alertmanager receiver)
```

## 8. Why periodic interval matters right now

Every ACS-initiated action (a queued job — set a parameter, reboot,
request diagnostics) sits as `QUEUED` until the device's *next* Inform,
then dispatches inside that session. With CGNAT and no working instant
push yet (§9), that means **your periodic Inform interval is your action
latency** — set it short (60–300s) for testing so you're not waiting 15+
minutes to see whether something worked. Once you're done testing, raise
it back to something reasonable (the TR-069 default is often 3600s) —
frequent Informs are needless load on both the device and the ACS at
fleet scale.

## 9. What's still missing for instant (non-periodic) actions

The ACS side of STUN is real and verified — the STUN server, and Inform
capture of `NATDetected`/`UDPConnectionRequestAddress`. What's **not**
built yet is the actual UDP Connection Request datagram the ACS would send
to reach a NAT'd device instantly: TR-069 Annex G's exact signature format
couldn't be sourced from an authoritative spec (every mirror of the real
document is blocked/paywalled), and shipping a guessed HMAC scheme would
look done while silently failing against your real hardware. Once your
device is on the network and reporting `udp_connection_request_address`
(§6), the plan is to determine the real format from a packet capture of
its actual STUN/keep-alive traffic rather than continue guessing — that's
the next step after this onboarding, not before it.

## 10. Troubleshooting

- **No Inform ever arrives**: check the device's ACS URL is exactly right
  (including `/cwmp`), and that `<ACS_URL>`'s port is actually reachable
  from the device's network — test with `curl <ACS_URL>` from *outside*
  your LAN (e.g. your phone on mobile data, not wifi) to rule out a
  firewall/NAT issue independent of the device itself.
- **Inform arrives but gets a 401 and never retries successfully**: Digest
  credentials on the device don't match `ACS_DIGEST_USERNAME`/`PASSWORD` —
  check for typos, and that the device isn't caching an old ACS
  password from a previous provider.
- **Device shows OFFLINE despite recent Informs**: check `cmd/acs`
  logs for errors around session handling for that device's `oui_serial`
  — an unhandled error there can leave the online-status flag stale even
  though Informs are landing.
- **Queued jobs never complete**: confirm the periodic interval (§8) isn't
  set to something very long — this is the most common "nothing is
  happening" cause and isn't a bug.
