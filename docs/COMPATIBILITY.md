# CPE compatibility matrix

This document is the compatibility contract for onboarding heterogeneous TR-069/CWMP CPEs to ACS. "Mock" means the behaviour is exercised by automated tests; "Real device" means a recorded result against physical hardware. Real-device rows are deliberately not inferred from mocks: firmware-specific hardware qualification remains mandatory before production rollout.

## Compatibility-first ACS baseline

The CWMP ingress is intentionally tolerant where doing so does not weaken identity or authorization:

- any HTTP path can carry CWMP POSTs, so CPEs configured for `/`, `/cwmp`, `/acs`, vendor-specific paths, etc. can reach the same handler;
- SOAP element parsing is prefix-insensitive and CWMP namespace tolerant;
- whitespace-only empty POSTs are accepted;
- the CPE's CWMP namespace is detected on Inform, persisted with the session, echoed on CPE-initiated responses, and used for later ACS-initiated RPCs in the same session;
- request bodies accept identity, gzip, `x-gzip`, zlib-wrapped deflate and legacy raw-deflate; unsupported encodings receive HTTP 415 so a capable CPE can retry without compression;
- both encoded and decompressed CWMP bodies are bounded to prevent compression bombs;
- the standards-aligned empty end-of-session response is HTTP 204; `ACS_CWMP_EMPTY_RESPONSE_STATUS=200` restores the historical empty-200 behaviour for a vendor that requires it;
- inbound CPE authentication supports Digest, optional Basic fallback (`ACS_AUTH_ALLOW_BASIC=1`), per-device credentials, and optional mTLS;
- TLS can be lowered as far as TLS 1.0 (`ACS_TLS_MIN_VERSION=1.0`) for legacy CPEs; production should use the highest floor supported by the deployed fleet;
- Connection Request treats any HTTP 2xx as accepted, prefers Digest authentication, accepts Digest qop lists/legacy no-qop/MD5-sess/opaque, and falls back to Basic when that is the only challenge offered;
- direct IPv4/IPv6 Connection Request, STUN-learned addressing, and Annex G UDP Connection Request are available;
- both `Device.` (TR-181) and `InternetGatewayDevice.` (TR-098) management roots are recognized where the ACS needs to discover Connection Request/STUN state;
- the HTTP server timeouts are deliberately long enough for slow embedded CPE stacks and large Inform payloads.

Compatibility must not become a reason to disable authentication globally. Prefer a per-vendor/device compatibility knob, dedicated test tenant, or network-isolated onboarding profile when a legacy CPE needs weaker transport settings.

## Important compatibility knobs

| Setting | Default / behaviour | Use when |
|---|---|---|
| `ACS_AUTH_ALLOW_BASIC` | off | A legacy CPE cannot perform HTTP Digest. Use only over TLS or an isolated management network. |
| `ACS_TLS_MIN_VERSION` | compatibility-oriented; supports `1.0` through modern TLS | A legacy CPE cannot negotiate the production TLS floor. Raise the floor whenever the fleet permits it. |
| `ACS_CWMP_EMPTY_RESPONSE_STATUS` | `204` | Set to `200` only for a CPE firmware that incorrectly requires an empty 200 response at session close. |
| `ACS_MTLS_CA_CERT` | unset | Enable certificate-authenticated CPEs while retaining Digest fallback for the remainder of the fleet. |
| `ACS_DIGEST_USERNAME` / `ACS_DIGEST_PASSWORD` | deployment supplied | Shared bootstrap credentials; migrate devices to unique per-device credentials. |
| `ACS_STUN_ADDR` | `:3478` in the standard service configuration | Enable Annex G/NAT traversal workflows. |

## Vendor profiles

| Vendor | Model (catalog) | Data model | Mock: Inform/session | Mock: SPV / fault | Mock: Download / TransferComplete | Real device | Firmware tested | Notes |
|---|---|---|---|---|---|---|---|---|
| Huawei | 5G CPE Pro | TR-181 (expected) | ✅ | ✅ (shared path) | ✅ (shared path) | not yet | — | Some firmwares may require Basic fallback or an older TLS floor; record exact firmware behaviour rather than enabling either fleet-wide. |
| Nokia | FastMile 5G | TR-181 | ✅ | ✅ | ✅ | not yet | — | |
| Teltonika | RUTX50 | TR-181 | ✅ | ✅ | ✅ | not yet | — | |
| Zyxel | NR7101 / NR5103 | TR-181 | ✅ (primary mock profile) | ✅ | ✅ | not yet | — | |

## Protocol capabilities

| Capability | Implementation | Automated coverage | Real-device validation |
|---|---|---|---|
| Inform (BOOTSTRAP, BOOT, PERIODIC, VALUE CHANGE, CONNECTION REQUEST) | `cmd/acs/session.go` | periodic + bootstrap fixtures | not yet |
| CWMP 1.x namespace detection/echo + session persistence | `internal/cwmp`, `internal/sessions`, migration `0045` | namespace and renderer regression tests | not yet |
| Identity/gzip/x-gzip/zlib-deflate/raw-deflate CWMP bodies | `cmd/acs/session.go` | HTTP compatibility unit tests | not yet |
| Empty session POST / session close | `cmd/acs/session.go` | whitespace empty body + 204/legacy-200 tests | not yet |
| Digest auth (qop=auth, nonce expiry, replay rejection — Postgres-backed, cross-replica, audit P1.6), Basic fallback | `internal/auth/digest.go`, `internal/auth/replay_postgres.go` | ✅ incl. cross-replica replay | not yet |
| Per-device Digest credentials (`CWMP_DIGEST` rotation, self-activating) | `internal/credentials`, `cmd/acs/main.go` | unit | not yet |
| mTLS client certificates | `ACS_MTLS_CA_CERT` | — | not yet |
| SetParameterValues / GetParameterValues / GetParameterNames | `cmd/acs/dispatch.go` | ✅ SPV success + 9005 fault | not yet |
| AddObject / DeleteObject / Reboot / FactoryReset / ScheduleInform / *Attributes | `cmd/acs/dispatch.go` | render unit tests | not yet |
| Download + TransferComplete (delayed, duplicate, stale fault) | `cmd/acs/session.go` | ✅ | not yet |
| Upload + receipt endpoint (signed URL, size cap, single use) | `cmd/api/upload_handlers.go` | ✅ (API suite) | not yet |
| IPPing / TraceRoute diagnostics (trigger + poll) | `cmd/acs/dispatch.go` | — | not yet |
| Connection Request HTTP success variants + Digest/Basic auth | `internal/connreq` | unit incl. 200/204/202, Digest qop list and Basic | not yet |
| Connection Request over UDP (Annex G, STUN-learned address) | `internal/connreq/annexg.go` | unit (datagram shape, HMAC) | **not validated — implemented from the spec text** |
| STUN server (RFC 5389 binding) | `internal/stun` | unit | not yet |
| XMPP connection requests | — | — | not implemented |
| Full TR-098 write catalog | partial root compatibility | partial | not qualified |
| Malformed XML / oversized or invalidly compressed body handling | `cmd/acs/session.go` | unit | n/a |

## Real-device qualification matrix

Every model must be qualified **per firmware release**, because CWMP behaviour frequently changes between vendor firmware builds.

Record at least:

| Area | Required evidence |
|---|---|
| Transport | DNS, IPv4/IPv6 as applicable, TLS version/cipher, certificate behaviour, keep-alive |
| Authentication | Digest first challenge/retry, per-device credential rotation, Basic only if required, mTLS if used |
| Session start | Inform accepted, cwmp:ID echoed, namespace preserved, repeated BOOT/PERIODIC behaviour |
| Empty exchange | CPE accepts standards-aligned 204 or documented 200 override |
| Compression | identity plus any compression the CPE advertises/uses |
| Parameter model | `Device.` or `InternetGatewayDevice.`, discovery depth, vendor extension paths |
| Provisioning | GPV/GPN/SPV, value types, 900x faults, writable/non-writable paths |
| Connection Request | direct 2xx result, Digest/Basic challenge behaviour, Event 6 Inform observed |
| NAT traversal | STUN address, NATDetected, Annex G where required |
| Diagnostics | IPPing/TraceRoute trigger, polling and terminal states |
| File transfer | Download, redirect/range behaviour if used, TransferComplete, post-upgrade version confirmation |
| Reboot/reset | Reboot and FactoryReset behaviour on an isolated test unit |
| Recovery | retry after ACS restart/network loss and duplicate/delayed messages |

## Load evidence

`ACS_TEST_LOAD_DEVICES=N go test -run TestIntegration_CPELoad ./cmd/acs/`
runs N concurrent mock CPEs (one Inform + close each) against a real Postgres and logs sessions/second. Record results here per environment:

| Date | Environment (CPU / RAM / Postgres) | Devices | Sessions/s | Notes |
|---|---|---|---|---|
| — | — | — | — | no recorded run yet |

## How to record a real-device result

1. Point the device's `ManagementServer.URL` at a test ACS with `ACS_DEBUG=1` so the CWMP exchange is observable. Sanitize secrets before retaining logs.
2. Record exact vendor, model, hardware revision and firmware version.
3. Walk: bootstrap Inform → empty session exchange → GPN/GPV → SPV of a reversible test parameter → Connection Request → diagnostics → upload/download where supported.
4. Test direct Connection Request first, then STUN/Annex G if the device sits behind NAT.
5. Where safe on a dedicated test unit, test reboot, firmware download/TransferComplete and recovery after interrupted connectivity.
6. Add a vendor row with every compatibility knob required. Do not silently make a weak vendor-specific setting the fleet-wide default.
7. Commit a sanitized fixture under `backend/test/fixtures/` whenever a device exposes a new valid SOAP, namespace, HTTP-auth, compression or fault shape.

## Production compatibility rule

A CPE is **supported** only when its exact firmware appears in this matrix with a reproducible successful onboarding/session test. The generic parser and mock harness maximize the chance that an unknown CPE connects; they do not replace hardware qualification.
