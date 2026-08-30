# CPE compatibility matrix

Status of each vendor/model profile against the ACS. "Mock" means the
behaviour is exercised by the mock-CPE harness in CI
(`backend/cmd/acs/session_integration_test.go`, `TestIntegration_CPE*`);
"Real device" means a recorded result against hardware — none exist yet,
which is the honest state of this project and the single most valuable
thing to add before a production rollout. Fill the real-device columns
in per firmware version as devices are tested, and keep the vendor
catalogs (`backend/internal/devices/adapters/catalogs/*.xml`) in sync.

## Vendor profiles

| Vendor | Model (catalog) | Data model | Mock: Inform/session | Mock: SPV / fault | Mock: Download / TransferComplete | Real device | Firmware tested | Notes |
|---|---|---|---|---|---|---|---|---|
| Huawei | 5G CPE Pro | TR-181 (expected) | ✅ | ✅ (shared path) | ✅ (shared path) | not yet | — | Some firmwares default to HTTP Basic (`ACS_AUTH_ALLOW_BASIC`); TLS 1.0 floor may be needed |
| Nokia | FastMile 5G | TR-181 | ✅ | ✅ | ✅ | not yet | — | |
| Teltonika | RUTX50 | TR-181 | ✅ | ✅ | ✅ | not yet | — | |
| Zyxel | NR7101 / NR5103 | TR-181 | ✅ (primary mock profile) | ✅ | ✅ | not yet | — | |

## Protocol capabilities

| Capability | Implementation | Mock coverage | Real-device validation |
|---|---|---|---|
| Inform (BOOTSTRAP, BOOT, PERIODIC, VALUE CHANGE, CONNECTION REQUEST) | `cmd/acs/session.go` | periodic + bootstrap fixtures | not yet |
| Digest auth (qop=auth, nonce expiry, replay rejection), Basic fallback | `internal/auth/digest.go` | ✅ incl. replay | not yet |
| Per-device Digest credentials (`CWMP_DIGEST` rotation, self-activating) | `internal/credentials`, `cmd/acs/main.go` | unit | not yet |
| mTLS client certificates | `ACS_MTLS_CA_CERT` | — | not yet |
| SetParameterValues / GetParameterValues / GetParameterNames | `cmd/acs/dispatch.go` | ✅ SPV success + 9005 fault | not yet |
| AddObject / DeleteObject / Reboot / FactoryReset / ScheduleInform / *Attributes | `cmd/acs/dispatch.go` | render unit tests | not yet |
| Download + TransferComplete (delayed, duplicate, stale fault) | `cmd/acs/session.go` | ✅ | not yet |
| Upload + receipt endpoint (signed URL, size cap, single use) | `cmd/api/upload_handlers.go` | ✅ (API suite) | not yet |
| IPPing / TraceRoute diagnostics (trigger + poll) | `cmd/acs/dispatch.go` | — | not yet |
| Connection Request over HTTP (direct IPv4/IPv6) | `internal/connreq` | unit | not yet |
| Connection Request over UDP (Annex G, STUN-learned address) | `internal/connreq/annexg.go` | unit (datagram shape, HMAC) | **not validated — implemented from the spec text** |
| STUN server (RFC 5389 binding) | `internal/stun` | unit | not yet |
| XMPP connection requests, TR-098 writes | — | — | not implemented |
| Malformed XML / oversized body handling | `cmd/acs/session.go` | ✅ 400 | n/a |

## Load evidence

`ACS_TEST_LOAD_DEVICES=N go test -run TestIntegration_CPELoad ./cmd/acs/`
runs N concurrent mock CPEs (one Inform + close each) against a real
Postgres and logs sessions/second. Record results here per environment:

| Date | Environment (CPU / RAM / Postgres) | Devices | Sessions/s | Notes |
|---|---|---|---|---|
| — | — | — | — | no recorded run yet |

## How to record a real-device result

1. Point the device's `ManagementServer.URL` at a test ACS with
   `ACS_DEBUG=1` so the full CWMP exchange is logged.
2. Walk: bootstrap Inform → SPV of `PeriodicInformInterval` → GPV of
   `DeviceInfo.` → Download of a known-good image → Connection Request
   (HTTP, then Annex G if the device sits behind NAT).
3. Add a row above with the firmware version and anything that needed a
   compatibility knob (`ACS_AUTH_ALLOW_BASIC`, `ACS_TLS_MIN_VERSION`,
   catalog path overrides), and commit the sanitized log under
   `backend/test/fixtures/` if it exposed a new parsing case.
