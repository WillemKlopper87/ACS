# Nokia onboarding handoff

The reported identifiers are hardware `3TG02241AAAA`, software version `1.2501.00.0798`, software build `5GGW2-OMNI-2_R250100BiwT0301E0798`, country `eu`, and local management address `192.168.1.1`. The build string is not a confirmed model. Model, serial and OUI are pending device evidence. SSH is optional for investigation and is not required for CWMP.

## Prepare before connecting the device

Run from the repository root:

```
python scripts/prepare-nokia-onboarding.py --output .local/nokia-onboarding
```

The tool is offline. It reports pending settings with exit code 1 and produces `readiness.json` and a read-only `scenario.json`. It never contacts the CPE, changes a server or exports passwords. Set `NOKIA_ACS_URL` to the actual CPE management URL; specify the deployment profile and supply `NOKIA_ACS_USERNAME`, `NOKIA_ACS_PASSWORD`, `NOKIA_ACSCR_USERNAME`, `NOKIA_ACSCR_PASSWORD`, `ACS_POSTGRES_DSN`, and `ACS_CREDENTIAL_ENCRYPTION_KEY` securely in the invoking process. These are preparation inputs, not automatic credential installation. The Nokia password meanings and usernames must be confirmed when configuring the device. API and CWMP services must share the credential encryption key.

Use the existing production launcher and `scripts/security-preflight.sh` for the full production security check. The offline checklist does not replace it or prove reachability. Apply standard TLS, source CIDR and device authentication configuration documented in `docs/security/device-authentication-and-deployment-profiles.md`. Keep the API/console private behind the configured authenticated HTTPS ingress.

## Check deployment schema

On the intended server, export `ACS_POSTGRES_DSN` securely. From `backend`, run:

```
go run ./cmd/migrate -check
```

This opens a read-only database snapshot. Exit 0 means every migration embedded in that binary has a matching applied checksum. Pending, unverified, modified or unknown migrations return exit 1; an absent tracking table remains absent. Do not run migration integration tests on this server because their harness recreates the public schema.

After reviewing pending changes and taking the normal database backup, apply migrations through the regular deployment procedure (`go run ./cmd/migrate`, which writes to the database). Migrations 0074–0077 support discovery evidence, BulkData, instance/operation evidence and telemetry health. Start the new services and verify their readiness endpoints. A schema check does not prove service health or network readiness.

## Network handoff

The default CWMP listener is TCP 7547; a TLS ingress can expose a different external destination port. The preparation report derives the firewall destination port from the supplied ACS URL. Permit CPE-initiated sessions from approved CPE source/NAT ranges to that destination with stateful return traffic. Optional STUN uses UDP 3478. ACS-to-CPE connection requests use the CPE-advertised port and need a separate rule when enabled. Verify trusted TLS, DNS resolution, the final ingress port and time synchronization. The private local address `192.168.1.1` is not a cloud-reachable management address by itself.

## Once the device is available

Install CPE and ACS credentials using the existing secure workflows, verify authenticated Inform, and obtain its ACS device UUID. Set `ACS_API_TOKEN` securely and run:

```
python scripts/qualify-device.py --base-url https://YOUR-API-ORIGIN --device DEVICE_UUID --scenario ci/compatibility/nokia-read-only.json --output .local/nokia-onboarding/qualification.json
```

The runner discovers the root, reads portable identity and rejects hardware/firmware mismatches before writes or reboot. It then reads up to 128 discovered concrete cellular/5G candidates, excluding sensitive and known write-only parameters. It never guesses Nokia vendor paths. The report records identity, path inventory and job outcomes, without operational values. Unsupported required identity fields fail the scenario rather than weakening its checks. If the actual device reports a different firmware identifier, reconcile it with the captured evidence and review the scenario explicitly.

Review `5g-telemetry`, connection diagnostics, discovery export and compatibility in the API/console. Do not set firmware qualification from a read-only report. Safe writes/readback, diagnostics, real reconnect, restart recovery, BulkData and upgrades still require supervised lab acceptance on the exact hardware and firmware. The existing Nokia catalog is a candidate mapping source; its guessed vendor paths are not asserted to exist on this device.

Renewal remains operator-triggered. Mesh steering, server-scheduled SNMP fallback and independent LMAP scheduling remain broader feature work; none is needed to receive the first Nokia CWMP session.
