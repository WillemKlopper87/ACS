# XMPP Connection Request (TR-069 Annex K) — design and field-test runbook

## Why

Field test 2026-10-30: the Nokia FastMile 5G Gateway 2 (ProductClass
`3TG02241`, firmware `1.2501.00.0798`, TR-181 2.12) sits behind carrier CGNAT.
Its `ConnectionRequestURL` is `http://10.10.178.14:7547/` — a private carrier
address the ACS can never dial — and it has no STUN / Annex G parameters, so
every queued job waits for the next periodic Inform (60 s). It does report
`Device.ManagementServer.SupportedConnReqMethods = "XMPP,HTTP"`,
`X_ALU_COM_XMPP_Enable = "1"` and a writable, empty `Device.XMPP.Connection.`
table.

With XMPP the CPE keeps an *outbound* TLS session to an XMPP server the ACS
also logs into. A Connection Request is then an iq stanza routed through
that server:

```xml
<iq type="get" id="…" to="xmpp-…@xmpp.acs.lab/cwmp">
  <connectionRequest xmlns="urn:broadband-forum-org:cwmp:xmppConnReq-1-0">
    <username>…ConnectionRequestUsername…</username>
    <password>…ConnectionRequestPassword…</password>
  </connectionRequest>
</iq>
```

The CPE answers with an iq `result` (or `error`) and opens a CWMP session
with Inform event `6 CONNECTION REQUEST` — seconds instead of a minute.

## What was built

| Piece | Where |
|---|---|
| XMPP client (STARTTLS, SASL SCRAM-SHA-256/SHA-1/PLAIN, bind, XEP-0199 keepalive, reconnect, iq matching, Annex K stanza, XEP-0133 account admin) | `backend/internal/xmpp` |
| Worker path: XMPP first when configured and the CPE reported `ConnReqJabberID`; confirm via Inform event 6; fall back otherwise | `backend/cmd/api/connreq_xmpp.go`, `connreq_worker.go` |
| Outcomes `XMPP_SENT_INFORM_RECEIVED`, `XMPP_SENT_NO_INFORM`, `XMPP_ERROR`, `XMPP_UNAVAILABLE`; mode `XMPP` | `internal/connreq`, `internal/devices` |
| Migration 0081: `XMPP` mode, `XMPP` credential type, `connection_request_behind_nat`, `device_xmpp_provisioning` | `internal/store/migrations/0081_xmpp_connection_request.sql` |
| Zero-touch provisioning state machine + API | `internal/xmppprov`, `cmd/api/xmpp_provisioner.go`, `cmd/api/xmpp_handlers.go` |
| Subtree discovery (`PARAMETER_DISCOVERY` with `merge: true`) | `internal/jobs`, `internal/parameters`, `cmd/acs/discovery.go` |
| Lab Prosody server (opt-in compose profile `xmpp`) | `infra/docker-compose.yml`, `infra/prosody/`, `scripts/xmpp-cert.sh`, `scripts/gen-env.sh`, `scripts/start.sh` |

### Design choices

- **Hand-written client, no new dependency.** The ACS needs a narrow client
  subset (log in over STARTTLS, send iqs, match replies by id and sender,
  keep the session alive, run two admin commands). The maintained Go
  libraries are either large (mellium.im/xmpp pulls a sizeable dependency
  tree) or expose the subset awkwardly (xmppo/go-xmpp's synchronous `Recv`
  loop, no iq correlation). ~1,300 lines (heavily commented) on the standard library keep
  `govulncheck` unchanged, are covered end to end against an in-process fake
  server, and SCRAM is checked against the RFC 5802 / RFC 7677 vectors.
  TLS is mandatory: the client refuses a server that does not offer
  STARTTLS, because the Annex K stanza carries the CPE's Connection Request
  password.
- **Prosody 0.12** (official `prosodyim/prosody` image) as the lab server:
  small, packaged, and ships XEP-0133 (`mod_admin_adhoc`). s2s, in-band
  registration, offline storage and HTTP are disabled.
- **Per-device accounts created by the ACS over XMPP (XEP-0133).** The ACS
  account is Prosody's only admin, so provisioning creates
  `xmpp-<64 random bits>@<domain>` with a 192-bit password over the session it
  already holds. Rejected alternatives: `docker exec prosodyctl` from the API
  (needs the docker socket — root-equivalent — and only works on the same
  host), a community HTTP admin module (extra exposed surface, not packaged),
  open in-band registration (anyone could register), one shared CPE account
  (a compromised CPE could impersonate every other one). The ACS account
  itself is (re)created by the container entrypoint from the generated
  secret on every start.
- **Per-device credentials** are a new `XMPP` credential type in
  `device_credentials` (encrypted at rest with
  `ACS_CREDENTIAL_ENCRYPTION_KEY`, versioned). It becomes ACTIVE only when the
  CPE reports a `ConnReqJabberID` belonging to that account — proof it logged
  in. The Annex K stanza itself carries the device's existing
  CONNECTION_REQUEST credential (or the fleet fallback), exactly as the HTTP
  path does.
- **Provisioning is a persisted state machine**, because the CPE chooses the
  instance numbers and only a GetParameterNames after AddObject proves which
  leaves exist and are writable:

  `ADD_CONNECTION → DISCOVER_CONNECTION → [ADD_SERVER → DISCOVER_SERVER] →
  CONFIGURE → BIND → VERIFY → DONE | FAILED`

  Each state waits on one persisted job id (`device_xmpp_provisioning.job_id`),
  so an API restart resumes exactly where it stopped and never repeats an
  AddObject. Discovery of a new instance is a single
  `GetParameterNames(<instance>., NextLevel=false)` merged into the existing
  tree (a full FastMile re-walk takes many sessions). CONFIGURE writes only
  discovered writable leaves (`Username`, `Password`, `Domain` and
  `Server.{i}.ServerAddress` required; `Resource`, `UseTLS=true`,
  `KeepAliveInterval`, `ServerConnectAlgorithm=ServerTable`, `Server.{i}.Port`,
  `Server.{i}.Enable`, `Enable` when present). BIND sets
  `ManagementServer.ConnReqXMPPConnection = Device.XMPP.Connection.<n>` (path
  reference without the trailing dot), `ConnReqAllowedJabberIDs = <ACS bare
  JID>` and the vendor `X_*_XMPP_Enable` when writable. VERIFY reads
  `ConnReqJabberID` (+ the connection's `Status`/`JabberID`) up to 8 times, 20 s
  apart at least (each read also waits for the CPE's next CWMP session).
- **Certificate checks on the CPE are relaxed only on request**
  (`relax_certificate_verification` → `Device.XMPP.X_*_CertificateVerificationRequired=false`,
  `bypass_hostname_check` → `X_*_BypassHostnameCheck=true`). Asking for it on a
  CPE without the knob fails provisioning instead of silently continuing.
  The ACS side never skips verification by default: it pins the lab
  certificate via `ACS_XMPP_TLS_CA_FILE`.
- **Worker order.** XMPP goes first whenever ACS XMPP is configured and the
  cached `ConnReqJabberID` is a valid full JID — no 10 s HTTP dial to a CGNAT
  address. `XMPP_ERROR` (iq error/timeout) and `XMPP_UNAVAILABLE` (ACS not
  connected) are audited with `transport: XMPP, fallback: true`, then the
  unchanged HTTP / Annex G / BEHIND_NAT logic runs. An acknowledged request
  with no Inform is `XMPP_SENT_NO_INFORM` (TIMEOUT, no fallback, mode not
  downgraded). Inform-time NAT classification and STUN status never downgrade
  an `XMPP` device; the NAT judgement is kept in
  `connection_request_behind_nat` so an XMPP-mode CGNAT device still gets the
  instant BEHIND_NAT skip when XMPP is down.
- **Unconfigured = unchanged.** With `ACS_XMPP_SERVER` unset nothing starts,
  the worker never consults the JID, and the provision endpoint returns 503.

### Configuration (cmd/api)

| Variable | Meaning |
|---|---|
| `ACS_XMPP_SERVER` | host[:port] the ACS dials (`127.0.0.1:5222` on the lab host). Unset = feature off. |
| `ACS_XMPP_JID` / `ACS_XMPP_PASSWORD` | ACS account (bare JID) and password (≥ 16 bytes). |
| `ACS_XMPP_DOMAIN` | XMPP domain (defaults to the JID's). |
| `ACS_XMPP_RESOURCE` | ACS resource, default `acs`. |
| `ACS_XMPP_TLS_CA_FILE` | PEM to trust for the server certificate (pin the lab cert). |
| `ACS_XMPP_TLS_SERVER_NAME` | Name checked in the certificate (default: domain). |
| `ACS_XMPP_TLS_INSECURE_SKIP_VERIFY=true` | Lab escape hatch, logged as a warning. |
| `ACS_XMPP_KEEPALIVE` | Ping interval to the server, default `60s`. |
| `ACS_XMPP_CPE_SERVER_ADDRESS` / `_PORT` | What CPEs are told to dial (public IP/name; port default 5222). Provisioning is refused while unset. |
| `ACS_XMPP_CPE_RESOURCE` | CPE resource, default `cwmp` (keeps the CPE's full JID stable). |
| `ACS_XMPP_CPE_KEEPALIVE_SECONDS` | `Device.XMPP.Connection.{i}.KeepAliveInterval`, default 60. |

`scripts/gen-env.sh` generates `ACS_XMPP_DOMAIN` (`xmpp.acs.lab`),
`ACS_XMPP_JID`, `ACS_XMPP_PASSWORD` and `ACS_XMPP_ENABLED="0"` once (and
backfills them into an existing `~/.acs-secrets.env`). `ACS_XMPP_SERVER` and
`ACS_XMPP_TLS_CA_FILE` are derived only when `ACS_XMPP_ENABLED=1`.

## Field-test runbook (EC2 lab, Nokia FastMile behind CGNAT)

### 1. Security group

Add an inbound rule: **TCP 5222 from the CPE's public egress address**
(`66.18.65.65/32` in the lab). Nothing else changes; the ACS reaches Prosody
on loopback.

### 2. Enable and deploy

```bash
cd <acs-checkout> && git fetch && git checkout feat/xmpp-connection-request
sed -i 's/^export ACS_XMPP_ENABLED=.*/export ACS_XMPP_ENABLED="1"/' ~/.acs-secrets.env
./scripts/start.sh        # migrates (0081), creates infra/prosody/certs/, starts Prosody, cmd/api
docker compose -f infra/docker-compose.yml --profile xmpp logs prosody | tail   # "ACS account acs@xmpp.acs.lab ready"
grep -i xmpp ~/acs-logs/api.log | tail     # "xmpp session established" jid=acs@xmpp.acs.lab/acs
```

`start.sh` sets `ACS_XMPP_CPE_SERVER_ADDRESS` to the detected public IP unless
it is set in `~/.acs-secrets.env`. The self-signed certificate names
`xmpp.acs.lab` and the public IP.

### 3. Preconditions on the device

- It has informed and has a completed parameter discovery (the FastMile
  does: `Device.XMPP.Connection.` is in the discovered tree as writable).
- Optional but recommended: rotate its Connection Request credential first
  (`POST /api/v1/devices/{id}/credentials/rotate`, then activate) so the
  Annex K stanza carries a per-device secret.

### 4. Provision

```bash
API=http://127.0.0.1:8080; DEV=<device-uuid>; TOKEN=<operator JWT with credential.manage>
curl -s -X POST "$API/api/v1/devices/$DEV/xmpp/provision" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"relax_certificate_verification": true, "bypass_hostname_check": true}'
```

Both relaxations are needed with the lab's self-signed certificate (the CPE
cannot be given the CA over TR-069 and it connects by IP). Omit them when
Prosody has a CA-issued certificate for a name the CPE dials.

Watch progress (each state needs one CWMP session — about a minute per step on
the FastMile's 60 s periodic Inform, so ~6–10 minutes overall; a Connection
Request job speeds nothing up yet because XMPP is not live):

```bash
watch -n 10 "curl -s -H 'Authorization: Bearer $TOKEN' $API/api/v1/devices/$DEV/xmpp | jq '.provisioning | {state, connection_path, server_path, attempts, last_error, jabber_id}'"
```

### 5. Verify

1. **CPE logged in**: Prosody log shows an authenticated c2s session for
   `xmpp-…@xmpp.acs.lab/cwmp` from the CPE's public address.
2. **ConnReqJabberID populated**: `GET /api/v1/devices/$DEV/xmpp` →
   `provisioning.state = DONE`, `cpe.conn_req_jabber_id` is the full JID,
   `cpe.conn_req_jabber_id_valid = true`; the XMPP credential is ACTIVE in
   `GET /api/v1/devices/$DEV/credentials`.
3. **Connection Request within seconds**:
   ```bash
   curl -s -X POST "$API/api/v1/devices/$DEV/connection-request" -H "Authorization: Bearer $TOKEN" \
     -H 'Content-Type: application/json' -d '{"timeout_seconds": 30}'
   ```
   The job becomes SUCCESS within a few seconds; the device shows
   `connection_request_mode = XMPP`, `last_connection_request_status =
   XMPP_SENT_INFORM_RECEIVED`, its last Inform carries `6 CONNECTION REQUEST`,
   and `api.log` has `xmpp connection request succeeded … elapsed=…`.
4. Queue a GET (`POST /api/v1/devices/$DEV/parameters/get`) followed by a
   Connection Request and confirm the read completes in seconds, not at the
   next periodic Inform.

### 6. Troubleshooting

| Symptom | Check |
|---|---|
| `provision` → 503 | `ACS_XMPP_ENABLED=1`? `api.log` "xmpp connect failed" (certificate pin, password mismatch → restart Prosody to re-sync the ACS account). |
| `provision` → 502 | Prosody refused the XEP-0133 command: is the ACS JID in `admins` (domain/user match `ACS_XMPP_DOMAIN`/`ACS_XMPP_JID`)? |
| FAILED in `CONFIGURE`/`BIND` with a 9xxx fault | Firmware rejected a value; `last_error` has the fault. Check the leaf in the discovered tree and the job in `GET /api/v1/devices/$DEV/jobs`. |
| VERIFY retries, `connection_status` `Connecting`/`Error` | CPE cannot reach/auth to 5222: security group, certificate relaxations, `Server.1.ServerAddress`. `tcpdump -i any port 5222` on the host. |
| Connection Request → `XMPP_ERROR` (`service-unavailable`) | CPE's XMPP session is down (Prosody shows no session), or it rejects the request (wrong Connection Request credential → `not-authorized`). The job then falls back (BEHIND_NAT for this device). |
| `XMPP_SENT_NO_INFORM` | CPE acked but did not inform: check its Connection Request credential and Annex K rate limiting. |

### 7. Rollback

1. Stop using XMPP on the CPE (it falls back to HTTP/periodic):
   ```bash
   curl -s -X PUT "$API/api/v1/devices/$DEV/parameters" -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
     -d '{"parameters":[{"name":"Device.ManagementServer.ConnReqXMPPConnection","value":"","type":"xsd:string"},
                        {"name":"Device.XMPP.Connection.1.Enable","value":"false","type":"xsd:boolean"}]}'
   ```
   (Use the instance from `provisioning.connection_path`; optionally
   `POST /api/v1/devices/$DEV/objects/delete` with `{"object_path":"Device.XMPP.Connection.1."}`,
   and restore `X_ALU-COM_CertificateVerificationRequired=true` /
   `X_ALU-COM_BypassHostnameCheck=false` if they were relaxed.)
2. Disable on the ACS: set `ACS_XMPP_ENABLED="0"` in `~/.acs-secrets.env`,
   rerun `./scripts/start.sh` (cmd/api starts without XMPP), then
   `cd infra && docker compose --profile xmpp stop prosody`, and remove the
   5222 security-group rule.
3. The device's mode stays `XMPP` until the next Inform-time classification
   or Connection Request outcome changes it; with XMPP off the worker
   ignores the JID and the NAT judgement still yields the BEHIND_NAT skip.

## Known limits / not verified

- **Not yet run against the FastMile or a real Prosody.** Tests cover the
  client against an in-process fake server, the stanza shapes, SCRAM vectors,
  the worker's path selection and outcomes (DB-backed tests need Postgres),
  and every provisioning transition with in-memory fakes. Docker/Prosody were
  not started in development.
- Nokia specifics to confirm on the device: whether AddObject on
  `Device.XMPP.Connection.` auto-creates `Server.1`; whether
  `ConnReqXMPPConnection` wants the reference with or without the trailing dot
  (`Device.XMPP.Connection.1` is written); whether `ConnReqAllowedJabberIDs`
  is matched against the bare or full ACS JID (the bare JID is written); which
  SASL mechanism its client uses (Prosody's `internal_hashed` offers
  SCRAM-SHA-1 and PLAIN — DIGEST-MD5-only clients would fail); TLS versions
  (Prosody 0.12 defaults to TLS 1.2+).
- **CGNAT keepalive**: the CPE is told `KeepAliveInterval=60`. Carrier NAT
  idle timers for TCP vary (minutes to hours); if sessions silently die,
  lower it (`ACS_XMPP_CPE_KEEPALIVE_SECONDS`) and re-provision with
  `restart: true`.
- **Known issue S1 (unchanged, not made worse)**: like credential rotation,
  the CONFIGURE SetParameterValues job payload — and the parameter cache
  until the confirmation read returns the CPE's masked value — holds the XMPP
  password in plaintext. Logs and audit records never contain it.
- Old XMPP accounts are not deleted from Prosody on re-provisioning; the
  superseded PENDING credential is revoked.
- Provisioning is API-only (no console button yet).
