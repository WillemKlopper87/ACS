# USP interop CI fixtures

This directory backs the `usp-interop` job in `.github/workflows/ci.yml`:
it runs the Broadband Forum reference USP agent, [obuspa][obuspa], against
`cmd/uspc` over WebSocket, MQTT 5, and MQTT 3.1.1, and asserts the
controller logged a `GetResp` for `Device.DeviceInfo.` from the agent on
each transport. Spec §9: protocol code that has never met a real agent is
an intention, not a capability.

[obuspa]: https://github.com/BroadbandForum/obuspa

## Pinned version

obuspa is built from its own `ci/Dockerfile`, unmodified, at commit
`2d885f12d7fd345435b5db651b3111cb6d3a879e` (tag `v11.0.0-master`, the
latest master-branch release tag as of 2026-09-11) -- a pinned commit, not
a floating branch, so a change upstream cannot silently alter what this
job validates. That `ci/Dockerfile` builds obuspa via plain
`./configure && make install`; there is no `--disable-coap` flag at this
commit (CoAP is compiled in by default). CoAP being compiled in but never
configured or connected in these factory-reset files is harmless for this
job -- CoAP itself is out of scope (design §10).

To re-verify or move the pin:

```sh
git init -q /tmp/obuspa-check && cd /tmp/obuspa-check
git remote add origin https://github.com/BroadbandForum/obuspa.git
git fetch --depth 1 origin <commit-or-tag>
git checkout -q FETCH_HEAD && git rev-parse HEAD
```

then update `OBUSPA_COMMIT` in the `usp-interop` job and the comment
above it.

## Files

- `assert-getresp.sh` -- polls a `uspc` log for the probe's `GetResp`
  from a given agent over a given MTP; on timeout it prints both the
  `uspc` and obuspa logs and exits 1, so a red CI step shows what each
  side said. **Must stay committed with the executable bit** (`git
  ls-files -s ci/usp/assert-getresp.sh` must print `100755`; a plain
  `chmod +x` does not reach the git index on Windows -- use `git
  update-index --chmod=+x` after staging).
- `obuspa-websocket.txt` -- factory-reset config for the WebSocket run,
  derived from upstream's `ci/configs/WebSocket.txt`.
- `obuspa-mqtt-v5.txt` -- factory-reset config for the MQTT 5.0 run,
  derived from upstream's `ci/configs/MQTT.txt`.
- `obuspa-mqtt-v311.txt` -- factory-reset config for the MQTT 3.1.1 run
  (the reply-to topic-suffix path, since MQTT 3.1.1 has no Response
  Topic property). There is no stock MQTT 3.1.1 config upstream; this is
  `obuspa-mqtt-v5.txt`'s shape with `Device.MQTT.Client.1.ProtocolVersion`
  changed to `"3.1.1"`.
- `assert-allowlist.sh` -- proves the USP agent allowlist's identity-level
  gate (design docs/superpowers/specs/2026-09-12-usp-agent-allowlist-design.md
  S2.2) against a real obuspa instance: before its identity is
  pre-registered, `cmd/uspc` must refuse to reconcile it and must not
  silently create a `devices` row for it anyway. Usage: `assert-allowlist.sh
  <uspc_log_file> <postgres_dsn> <endpoint_id> <oui_serial>`.

Each config file's own header comment records exactly which upstream file
and commit it was derived from and which fields were overridden and why.

## Key fields every config gets right

- `Device.LocalAgent.EndpointID` -- a fixed test agent id
  (`os::012345-CIAGENT`), matching `AGENT_ENDPOINT_ID` in the CI job and
  the second argument to every `assert-getresp.sh` call.
- `Device.LocalAgent.Controller.1.EndpointID` -- must be exactly
  `self::` + `ACS_USP_CONTROLLER_ID` (`ci-obuspa-interop` in the CI job).
  `cmd/uspc` checks a record's `To` against its own configured controller
  id (design §3.4) and refuses/ignores anything else, so a typo here
  fails silently as "obuspa never replies" rather than loudly -- this is
  the single most important value to get right when touching these
  files.
- WebSocket: `Device.LocalAgent.Controller.1.MTP.1.WebSocket.Host/Port/Path`
  point at `cmd/uspc`'s WebSocket listener (`127.0.0.1:9877/usp`,
  reachable because the CI job runs the obuspa container with
  `--network host`) with `EnableEncryption "false"`.
- MQTT: `Device.MQTT.Client.1.BrokerAddress/BrokerPort` point at
  `cmd/uspc`'s embedded MQTT broker (`127.0.0.1:1883`);
  `Device.LocalAgent.Controller.1.MTP.1.MQTT.Topic` matches
  `ACS_USP_MQTT_CONTROLLER_TOPIC` (`/usp/controller`);
  `TransportProtocol "TCP/IP"` (plaintext, not TLS -- design §8: TLS is
  the default, plaintext only for CI).
  `Device.MQTT.Client.1.Username`/`.Password` are left as upstream's
  values: `cmd/uspc`'s embedded broker's `auth.AllowHook` accepts any
  credentials, so nothing on the controller side needs to match them.

## Running locally

Requires a running Docker daemon and, since Task 5 (USP agent allowlist),
a running/migrated Postgres reachable at `ACS_USP_POSTGRES_DSN`:
`cmd/uspc` now opens a database connection at startup for identity
reconciliation, and the identity gate refuses any agent whose
OUI+ProductClass+SerialNumber has no `devices` row -- obuspa's own
identity is only known once extracted (its `SerialNumber` is
generated/persisted at first boot, not predictable ahead of time), so a
first connection attempt is expected to be refused until it is
pre-registered and the container restarted. See the `usp-interop` job's
"usp-interop: websocket" step in `.github/workflows/ci.yml` for the full
extract-prove-register-restart sequence this snippet abbreviates.

```sh
cd backend && go build -o /tmp/uspc ./cmd/uspc && go build -o /tmp/migrate ./cmd/migrate && cd ..

git init -q /tmp/obuspa-src
( cd /tmp/obuspa-src \
  && git remote add origin https://github.com/BroadbandForum/obuspa.git \
  && git fetch --depth 1 origin 2d885f12d7fd345435b5db651b3111cb6d3a879e \
  && git checkout -q FETCH_HEAD )
docker build -f /tmp/obuspa-src/ci/Dockerfile -t obuspa:ci /tmp/obuspa-src

export ACS_USP_CONTROLLER_ID=ci-obuspa-interop ACS_USP_WS_ADDR=:9877 \
  ACS_USP_WS_PATH=/usp ACS_USP_MQTT_ADDR=:1883 \
  ACS_USP_MQTT_CONTROLLER_TOPIC=/usp/controller ACS_USP_ALLOW_PLAINTEXT=true \
  ACS_USP_HTTP_ADDR=:8092 \
  ACS_USP_POSTGRES_DSN=postgres://acs:acs@localhost:5432/acs?sslmode=disable \
  ACS_POSTGRES_DSN=postgres://acs:acs@localhost:5432/acs?sslmode=disable

/tmp/migrate

/tmp/uspc > /tmp/uspc-ws.log 2>&1 &
docker run -d --name obuspa-ws --network host \
  -v "$PWD/ci/usp:/ci/usp:ro" \
  obuspa:ci -p -v4 -r /ci/usp/obuspa-websocket.txt -t /etc/obuspa/certs -f /tmp/usp-ws.db
docker logs -f obuspa-ws > /tmp/obuspa-ws.log 2>&1 &

# obuspa-ws's first connection is refused (identity gate, Task 5) --
# extract its real identity, prove the refusal, then pre-register it.
oui=$(docker exec obuspa-ws obuspa -c get "Device.DeviceInfo.ManufacturerOUI" | sed -n 's/.*=> //p' | tr -d '[:space:]')
product_class=$(docker exec obuspa-ws obuspa -c get "Device.DeviceInfo.ProductClass" | sed -n 's/.*=> //p' | sed -e 's/^ *//' -e 's/ *$//')
serial_number=$(docker exec obuspa-ws obuspa -c get "Device.DeviceInfo.SerialNumber" | sed -n 's/.*=> //p' | tr -d '[:space:]')
oui_serial="$oui+$product_class+$serial_number"  # only safe when none of the three contain \ or + -- see ci.yml's esc_natural_key for the general case

ci/usp/assert-allowlist.sh /tmp/uspc-ws.log "$ACS_USP_POSTGRES_DSN" os::012345-CIAGENT "$oui_serial"

psql "$ACS_USP_POSTGRES_DSN" -c "
  INSERT INTO devices (id, oui_serial, manufacturer, oui, product_class, serial_number, online_status, first_seen_at, last_updated_at, customer_id, tags)
  VALUES (gen_random_uuid(), '$oui_serial', 'obuspa-ci', '$oui', '$product_class', '$serial_number', 'OFFLINE', now(), now(), NULL, '{}')
  ON CONFLICT (oui_serial) DO NOTHING;
"
docker restart obuspa-ws

ci/usp/assert-getresp.sh /tmp/uspc-ws.log os::012345-CIAGENT WebSocket /tmp/obuspa-ws.log

docker rm -f obuspa-ws
kill %1 %2 2>/dev/null || true
```

`--network host` only works this way on Linux; on macOS/Windows Docker
Desktop, publish `cmd/uspc`'s ports and point the factory-reset file's
`Host`/`BrokerAddress` at `host.docker.internal` instead of `127.0.0.1`.
The MQTT 5 and MQTT 3.1.1 runs follow the same shape with
`obuspa-mqtt-v5.txt`/`obuspa-mqtt-v311.txt`, `mtp=MQTT` in
`assert-getresp.sh`'s third argument, and distinct log/db paths.
