# TLS for the lab (Caddy + Let's Encrypt)

The lab serves the console (`:5173`), the operator API (`:8080`) and the BSS
adapter (`:8090`) over plain HTTP on the instance's IP. This puts HTTPS in
front of them: a Caddy container on the instance gets and renews a Let's
Encrypt certificate for three names and forwards to the loopback services.
TLS ends on your own instance; no third party reads the traffic.

It covers the **operator surface only** (console, API, BSS adapter). CPE
traffic (CWMP, USP, XMPP, STUN) and the CPEs' ACS URLs are unchanged.

## What it unlocks

TLS on the BSS adapter is what the NaaS integration needs for sealed Wi-Fi
secret handles (`POST /bss/v1/secrets` refuses plain HTTP), webhooks and event
replay, and a Grafana gate. No Wi-Fi passphrase goes to the lab before this is
verified.

## Names

Three names under a zone you control. The examples use `example.com`:

| Name | Forwards to | Used by |
|---|---|---|
| `lab.example.com` | `127.0.0.1:5173` | operators (console) |
| `lab-api.example.com` | `127.0.0.1:8080` | the console's API calls |
| `lab-bss.example.com` | `127.0.0.1:8090` | the NaaS / BSS integration |

If the zone already serves another app (a wildcard record, names such as
`www` or `admin`), use names that app does not own. An explicit record beats a
wildcard. Browsers also send a cookie that app set for the whole zone
(`Domain=.example.com`) to these names; have it use host-only cookies, or give
the lab a zone of its own, if that matters.

## Steps

**1. Cloudflare DNS.** Add three `A` records (`lab`, `lab-api`, `lab-bss`) to the
instance's public IP, each set to **DNS only** (grey cloud, not proxied). A
proxied record would make Cloudflare terminate TLS and read the traffic, and
Caddy could not get its own certificate the usual way. Use an Elastic IP: if
the instance's address changes, the names break.

**2. Security group.** Add inbound `80/tcp` and `443/tcp` from anywhere (the
certificate check and the redirect need 80). Opening them blocks nothing.

**3. Check nothing else owns 80 or 443** on the instance:
`ss -ltn | grep -E ':(80|443) '` should print nothing. Caddy uses the host
network so it can reach the loopback services and bind both.

**4. Configure.** Add to the lab's secrets env file (the one
`scripts/start.sh` sources):

```sh
export ACS_LAB_PUBLIC_FRONTEND_URL="https://lab.example.com"
export ACS_LAB_PUBLIC_API_URL="https://lab-api.example.com"
export ACS_LAB_PUBLIC_BSS_URL="https://lab-bss.example.com"
export ACS_LAB_TLS_EMAIL="you@example.com"      # Let's Encrypt expiry notices
export ACS_TRUSTED_PROXY_CIDRS="127.0.0.1,::1"
export ACS_BSS_SECRETS_BEHIND_TLS_PROXY="true"
```

- With all four `ACS_LAB_*` set, `start.sh` also starts the Caddy container
  (compose profile `tls`, `infra/ingress/Caddyfile.lab`). With only the first
  two it just uses the https names in the console and CORS.
- `ACS_TRUSTED_PROXY_CIDRS` lets the API and the BSS adapter read the real
  client address from `X-Forwarded-For`, but only from a connection that comes
  from these ranges (Caddy on the same host). Without it every user looks like
  one client: one shared sign-in throttle and rate-limit bucket. Never list a
  range an outside party can connect from.
- `ACS_BSS_SECRETS_BEHIND_TLS_PROXY=true` tells the adapter TLS ends at the
  proxy, so it accepts secret handles. The adapter cannot check this, so close
  port 8090 to the internet (step 6).

Then run `./scripts/start.sh`. The first start asks Let's Encrypt for the three
certificates, which takes under a minute once DNS has propagated.

**5. Verify.**

```sh
curl -sI https://lab.example.com/ | head -1
curl -s -o /dev/null -w '%{http_code}\n' https://lab-api.example.com/readyz   # 200
curl -s -o /dev/null -w '%{http_code}\n' https://lab-bss.example.com/readyz   # 200
curl -sI http://lab-bss.example.com/ | head -1                                # a redirect to https
```

Sign in at the console. Create a BSS client and `POST
https://lab-bss.example.com/bss/v1/secrets` with a throwaway value: it should
return 201, not `ErrTLSRequired`. Fail a sign-in five times from your own
connection: you are throttled, and a sign-in from another connection is not.

**6. Close what CPEs do not use, in this order.**

1. `8090` (BSS adapter) and `5173` (console) from the internet. No CPE uses
   them, and with the secrets flag on, an open 8090 would accept a passphrase in
   clear. Also `3000` and `9090` if they were public.
2. `8080` (API) **last, and only after** CPE transfers are off it. CPEs fetch
   firmware from `<ACS_FIRMWARE_BASE_URL>/api/v1/firmware/images/<id>/file` and
   upload config backups and logs to `<ACS_UPLOAD_BASE_URL>/api/v1/bulk-data/…`;
   both are on the API port. Set both variables to `https://lab-api.example.com`,
   restart, and confirm a real CPE completes a download and an upload over
   https. Older CPE firmware may not trust the Let's Encrypt root; if one
   does not, keep 8080 open for it (restrict the source addresses if you
   can) and keep operators on https.

Keep open throughout: the CPE ports (`7547/tcp` CWMP, `3478/udp` STUN,
`9877/tcp` USP WebSocket, `1883/tcp` USP MQTT, `5222/tcp` XMPP if enabled) and
SSH from your own address.

## Roll back

Remove the `ACS_LAB_*` lines and restart, and stop the proxy
(`cd infra && docker compose --profile tls stop caddy`). The IP and port
addresses work again as long as their security-group rules are still there.

## Not covered

- CPE-facing TLS: the CWMP listener keeps its own certificate
  (`scripts/acs-cert.sh`).
- A production ingress: the production profile expects your own proxy and
  refuses to start without https origins.
- Grafana behind the console's gate over https (`ACS_GRAFANA_URL`): add a
  fourth name the same way, then set it.
