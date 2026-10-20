# Nokia onboarding handoff

The reported identifiers are hardware `3TG02241AAAA`, software version `1.2501.00.0798`, software build `5GGW2-OMNI-2_R250100BiwT0301E0798`, country `eu`, and local management address `192.168.1.1`. The build string is not a confirmed model. Model, serial, OUI, exact ProductClass and vendor paths remain pending device evidence. SSH is optional for investigation and is not required for CWMP.

Tracking goal: #75.

## Recommended sequence

Use two passes:

1. **Controlled cloud lab first contact** — capture-first, read-only, no guessed Nokia writes.
2. **Production-like secure management** — TLS, constrained bootstrap, automatic unique device-bound Digest graduation, Connection Request and reversible operations.

Do not mark the device qualified from emulator/mock evidence.

## Prepare before connecting the device

Run from the repository root:

```
python scripts/prepare-nokia-onboarding.py --output .local/nokia-onboarding
```

The tool is offline. It never contacts the CPE or ACS, changes a server, or exports passwords. It writes a redacted `readiness.json` and read-only `scenario.json`.

Set:

```
NOKIA_ACS_URL=<exact URL to configure on the CPE>
ACS_DEPLOYMENT_PROFILE=lab|production
ACS_POSTGRES_DSN=<target database DSN>
ACS_CREDENTIAL_ENCRYPTION_KEY=<same key used by API and CWMP services>
```

### Production first contact

Production must use the constrained bootstrap path:

```
ACS_CWMP_BOOTSTRAP_USERNAME=<bootstrap username>
ACS_CWMP_BOOTSTRAP_PASSWORD=<strong bootstrap password>
```

The bootstrap identity is not a normal fleet credential. It cannot manage an already-established device or consume that device's queued work. After a valid first-contact Inform, ACS installs a generated per-device `CWMP_DIGEST` credential through the device's discovered TR-181/TR-098 ManagementServer path. That credential stays `PENDING` until the Nokia reconnects with it and reports the same natural identity, then becomes `ACTIVE`.

### Lab first contact

For a controlled compatibility lab, the same constrained bootstrap pair is preferred. The legacy preparation variables:

```
NOKIA_ACS_USERNAME
NOKIA_ACS_PASSWORD
```

remain accepted by the preparation script only as a **lab compatibility input**. They are not a production steady-state identity.

Connection Request credentials are deliberately not required for first contact. When the Nokia advertises its ConnectionRequestURL/authentication behavior, configure device-specific values and record them securely:

```
NOKIA_ACSCR_USERNAME
NOKIA_ACSCR_PASSWORD
```

## Cloud deployment

For the first cloud run, use the current `main` release candidate and the supported deployment scripts. See `docs/NOKIA-CLOUD-QUALIFICATION.md` for the concrete cloud sequence.

Before changing the CPE:

1. apply migrations;
2. run `go run ./cmd/migrate -check`;
3. run `scripts/security-preflight.sh` for production or `scripts/field-preflight.sh` for a controlled field/lab run;
4. verify the ACS/API/BSS/USP readiness endpoints;
5. start a bounded remote-IP CWMP capture for the Nokia source address when practical.

Keep API/console/BSS/USP health/Grafana/Prometheus off the public management plane except through the documented protected ingress.

## Network handoff

The default CWMP listener is TCP 7547; a TLS ingress can expose a different external destination port. Permit CPE-initiated sessions from the approved Nokia WAN/NAT source policy with stateful return traffic.

Optional STUN uses UDP 3478. Do not expose it until STUN/Annex-G qualification is actually needed.

ACS-to-CPE Connection Request uses the CPE-advertised URL/port and must be qualified separately. The Nokia's private local address `192.168.1.1` is not directly cloud-routable and should never be assumed to be its Connection Request path.

## First-contact evidence

On the first authenticated Inform, record:

- Manufacturer;
- OUI;
- ProductClass;
- SerialNumber;
- HardwareVersion;
- SoftwareVersion/build;
- CWMP namespace;
- Device/TR-181 vs InternetGatewayDevice/TR-098 root;
- ConnectionRequestURL if present;
- NAT/STUN state if present.

Do not create vendor-specific parameter mappings from the reported build string alone.

## Read-only qualification

After the device exists in ACS and you have its device UUID, set `ACS_API_TOKEN` securely and run:

```
python scripts/qualify-device.py \
  --base-url https://YOUR-API-ORIGIN \
  --device DEVICE_UUID \
  --scenario ci/compatibility/nokia-read-only.json \
  --output .local/nokia-onboarding/qualification.json
```

The runner:

- discovers the actual root and parameter tree first;
- reads portable DeviceInfo identity;
- rejects a hardware/firmware mismatch before writes or reboot;
- reads only discovered concrete 5G/cellular candidates;
- excludes sensitive/write-only candidates;
- never guesses Nokia vendor paths;
- records path/job evidence without exporting operational values or secrets.

Review the Device Compatibility panel, discovery export, connection diagnostics, 5G telemetry and captured CWMP session together.

## After read-only PASS

Only after read-only qualification succeeds:

1. prove the unique bound Digest reconnect / credential activation;
2. verify post-graduation bootstrap rejection;
3. test Connection Request and require Event 6 CONNECTION REQUEST;
4. identify one harmless writable parameter from discovery;
5. perform read -> SPV -> read-back -> restore;
6. run IPPing/TraceRoute if supported;
7. test reboot/reconnect with independent observation;
8. test firmware Download/TransferComplete only with an approved image for the exact device/firmware.

Any Nokia-specific SOAP, authentication, data-model or fault behavior must be captured, sanitized and converted into firmware-scoped fixtures/tests before being promoted into a reusable compatibility profile.
