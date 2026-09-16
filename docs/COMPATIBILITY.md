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
- inbound CPE authentication supports Digest with both MD5 and SHA-256 (RFC 7616 — challenged on separate `WWW-Authenticate` lines, MD5 first, so an MD5-only CPE is unaffected and a SHA-256-only one can answer), optional Basic fallback (`ACS_AUTH_ALLOW_BASIC=1`) in lab/compatibility use, unique per-device credentials, a constrained dedicated bootstrap identity, and optional mTLS;
- production first contact uses `ACS_CWMP_BOOTSTRAP_USERNAME/PASSWORD` only as a constrained onboarding identity: it cannot manage an established device or receive normal queued work, and a successful bootstrap graduates to a unique device-bound `CWMP_DIGEST` credential before normal management is enabled;
- a generated per-device credential remains `PENDING` after credential-installation `SetParameterValues`; it becomes `ACTIVE` only after a later Digest-authenticated Inform reports the natural identity bound to that credential's ACS DeviceID;
- the 401 challenge is kept small for embedded HTTP stacks: a 38-character nonce (several CPE clients allocate a fixed 64-byte nonce buffer and silently stop authenticating when it overflows) and `ACS_DIGEST_ALGORITHMS` to reduce a multi-line challenge to one line for a CPE that cannot parse several;
- TLS can be lowered as far as TLS 1.0 (`ACS_TLS_MIN_VERSION=1.0`) for controlled legacy-CPE lab qualification; the production profile requires TLS 1.2+;
- Connection Request treats any HTTP 2xx as accepted, prefers Digest authentication, accepts Digest qop lists/legacy no-qop/opaque and the MD5, MD5-sess, SHA-256 and SHA-256-sess algorithms (preferring SHA-256 when a CPE offers both), and falls back to Basic when Digest is unusable or Basic is the only challenge offered;
- direct IPv4/IPv6 Connection Request, STUN-learned addressing, and Annex G UDP Connection Request are available;
- both `Device.` (TR-181) and `InternetGatewayDevice.` (TR-098) management roots are recognized where the ACS needs to discover Connection Request/STUN state and where secure bootstrap graduation installs `ManagementServer.Username/Password`;
- the HTTP server timeouts are deliberately long enough for slow embedded CPE stacks and large Inform payloads.

Compatibility must not become a reason to disable authentication globally. Prefer a per-vendor/device compatibility knob, dedicated test tenant, or network-isolated lab profile when a legacy CPE needs weaker transport settings. Production shared fleet Digest identity is not a supported substitute for constrained bootstrap plus unique credentials.

## Important compatibility knobs

| Setting | Default / behaviour | Use when |
|---|---|---|
| `ACS_AUTH_ALLOW_BASIC` | off; forbidden by production profile | A legacy CPE cannot perform HTTP Digest during a controlled lab compatibility test. Use only over TLS or an isolated management network and do not treat the result as the production security path. |
| `ACS_TLS_MIN_VERSION` | lab compatibility can support `1.0`; production requires `1.2` or `1.3` | A legacy CPE cannot negotiate the production TLS floor. Test the exact firmware in isolation rather than lowering the production fleet floor. |
| `ACS_CWMP_EMPTY_RESPONSE_STATUS` | `204` | Set to `200` only for a CPE firmware that incorrectly requires an empty 200 response at session close. |
| `ACS_DIGEST_ALGORITHMS` | unset — challenges `MD5` then `SHA-256` | A CPE mishandles a multi-line 401. Set to one algorithm (for N5368X qualification, try `SHA-256`) to emit a single challenge line. Narrows what is *offered*, never what is verified. |
| `ACS_MTLS_CA_CERT` | unset | Enable certificate-authenticated CPEs while retaining device-bound Digest for the remainder of the fleet. |
| `ACS_CWMP_BOOTSTRAP_USERNAME` / `ACS_CWMP_BOOTSTRAP_PASSWORD` | unset; must be configured together, bootstrap password >=16 bytes, username distinct from legacy shared Digest username | Enable constrained first-contact enrollment. Bootstrap cannot manage established devices or consume normal work; use it only to install/graduated to a unique device-bound credential. |
| `ACS_CREDENTIAL_ENCRYPTION_KEY` | required for production bootstrap graduation and for per-device-only Digest operation without a shared password | Encrypt generated device credentials at rest and provide non-empty nonce-signing material where required. Never put the key in captures/evidence. |
| `ACS_DIGEST_USERNAME` / `ACS_DIGEST_PASSWORD` | optional legacy/lab compatibility source; production steady-state shared identity is rejected before normal CWMP handling | Controlled compatibility testing only. Do **not** configure this pair as the production bootstrap identity. |
| `ACS_STUN_ADDR` | `:3478` in the standard service configuration | Enable Annex G/NAT traversal workflows. |

## Vendor profiles

| Vendor | Model (catalog) | Data model | Mock: Inform/session | Mock: SPV / fault | Mock: Download / TransferComplete | Real device | Firmware tested | Notes |
|---|---|---|---|---|---|---|---|---|
| Huawei | 5G CPE Pro, N5368X 5G Outdoor CPE | TR-181 (expected) | ✅ | ✅ (shared path) | ✅ (shared path) | not yet | — | The N5368X (V200R001C00SPC340T) offers a `Connection request Authentication: Digest-SHA256` setting; both auth directions now implement SHA-256, so it needs no weakening of that setting. The secure field path must additionally prove constrained bootstrap -> credential-install SPV -> unique bound reconnect before this row is marked qualified; use `HUAWEI-N5368X-SECURE-QUALIFICATION.md`. Some firmware may block remote `ManagementServer.Username/Password` rewrite; record that as firmware-scoped BLOCKED/manual graduation rather than falling back to a shared normal-management identity. Bare `WLANConfiguration.{i}.KeyPassphrase` is advertised but non-writable on EchoLife ONTs (HG8546M, HG8145V5, HG8245H, EG8141A5); writes must target `PreSharedKey.1.KeyPassphrase`, which is now the preferred TR-098 candidate. A TR-098 device implementing only the bare form is a known unqualified case until writability-aware candidate selection lands. |
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
| Digest auth (qop=auth, MD5 + SHA-256, nonce expiry, replay rejection — Postgres-backed, cross-replica, audit P1.6), Basic fallback | `internal/auth/digest.go`, `internal/auth/replay_postgres.go` | ✅ incl. cross-replica replay, algorithm mismatch rejection | not yet |
| Constrained CWMP bootstrap identity isolation | `cmd/acs/bootstrap.go`, production CWMP guard | ✅ established-device rejection, cross-tenant/natural-identity isolation, no normal job/session dispatch | not yet |
| Bootstrap -> unique device-bound `CWMP_DIGEST` graduation | `cmd/acs/bootstrap_graduation.go`, `internal/credentials`, `internal/devices` | ✅ PENDING creation/reuse, TR-181/TR-098 credential SPV, unknown-root fail-closed, SPV response does not activate, bound-Inform activation, post-graduation bootstrap rejection | not yet — N5368X is first target |
| Per-device Digest credentials (`CWMP_DIGEST`) | `internal/credentials`, `cmd/acs/main.go`, `cmd/acs/session.go` | ✅ Digest proof is side-effect-free and lifecycle activation occurs only after credential-bound Inform identity match | not yet |
| mTLS client certificates | `ACS_MTLS_CA_CERT` | — | not yet |
| SetParameterValues / GetParameterValues / GetParameterNames | `cmd/acs/dispatch.go` | ✅ SPV success + 9005 fault | not yet |
| AddObject / DeleteObject / Reboot / FactoryReset / ScheduleInform / *Attributes | `cmd/acs/dispatch.go` | render unit tests | not yet |
| Download + TransferComplete (delayed, duplicate, stale fault) | `cmd/acs/session.go` | ✅ | not yet |
| Upload + receipt endpoint (signed URL, size cap, single use) | `cmd/api/upload_handlers.go` | ✅ (API suite) | not yet |
| IPPing / TraceRoute diagnostics (trigger + poll) | `cmd/acs/dispatch.go` | — | not yet |
| Connection Request HTTP success variants + Digest (MD5/SHA-256, both `-sess`)/Basic auth | `internal/connreq` | unit incl. 200/204/202, Digest qop list, SHA-256 preference, unsupported-algorithm fallback, Basic | not yet |
| Connection Request over UDP (Annex G, STUN-learned address) | `internal/connreq/annexg.go` | unit (datagram shape, HMAC) | **not validated — implemented from the spec text** |
| STUN server (RFC 5389 binding) | `internal/stun` | unit | not yet |
| XMPP connection requests | — | — | not implemented |
| Full TR-098 write catalog | partial root compatibility | partial | not qualified |
| Malformed XML / oversized or invalidly compressed body handling | `cmd/acs/session.go` | unit | n/a |
| USP (TR-369) Get/GetResp over WebSocket + MQTT 5 + MQTT 3.1.1 | `cmd/uspc`, `internal/usp`, `internal/usp/mtp` | ✅ unit + pinned obuspa reference-agent interop over WebSocket, MQTT 5 and MQTT 3.1.1, including Get/GetResp, job dispatch, allowlist and subscription/restart checks (`usp-interop`) | physical-CPE qualification deferred; reference-agent qualification is the current software acceptance gate |

## Real-device qualification matrix

Every model must be qualified **per firmware release**, because CWMP behaviour frequently changes between vendor firmware builds. Execute the generic qualification using [`FIELD-CPE-VALIDATION.md`](FIELD-CPE-VALIDATION.md); for the N5368X secure release gate, also follow [`HUAWEI-N5368X-SECURE-QUALIFICATION.md`](HUAWEI-N5368X-SECURE-QUALIFICATION.md). This matrix records the resulting support decision rather than replacing the evidence runbook.

Record at least:

| Area | Required evidence |
|---|---|
| Transport | DNS, IPv4/IPv6 as applicable, TLS version/cipher, certificate behaviour, keep-alive |
| Authentication | dedicated bootstrap Digest first challenge/retry, credential-install SPV, unique device-bound Digest reconnect/graduation, post-graduation bootstrap rejection; Basic only if an isolated firmware-specific lab test requires it; mTLS if used |
| Session start | Inform accepted, cwmp:ID echoed, namespace preserved, repeated BOOT/PERIODIC behaviour |
| Empty exchange | CPE accepts standards-aligned 204 or documented 200 override |
| Compression | identity plus any compression the CPE advertises/uses |
| Parameter model | `Device.` or `InternetGatewayDevice.`, discovery depth, vendor extension paths |
| Provisioning | GPV/GPN/SPV, value types, 900x faults, writable/non-writable paths |
| Connection Request | direct 2xx result, device-specific Digest/Basic challenge behaviour, Event 6 Inform observed |
| NAT traversal | STUN address, NATDetected, Annex G where required |
| Diagnostics | IPPing/TraceRoute trigger, polling and terminal states |
| File transfer | Download, redirect/range behaviour if used, TransferComplete, post-upgrade version confirmation |
| Reboot/reset | Reboot and FactoryReset behaviour on an isolated test unit |
| Recovery | retry after ACS restart/network loss and duplicate/delayed messages while stable bound identity is preserved |

## Load evidence

`ACS_TEST_LOAD_DEVICES=N go test -run TestIntegration_CPELoad ./cmd/acs/`
runs N concurrent mock CPEs (one Inform + close each) against a real Postgres and logs sessions/second. Record results here per environment:

| Date | Environment (CPU / RAM / Postgres) | Devices | Sessions/s | Notes |
|---|---|---|---|---|
| 2026-09-15 | Windows development host / Docker PostgreSQL 18 test container | 100 mock CPE sessions | 637.8 | `TestIntegration_CPELoad`; 0 failures, 100 registered; emulator/load-harness evidence, not physical-device qualification |

## How to record a real-device result

1. Start from the release/commit, preflight, capture-first sequence, result classifications and evidence template in [`FIELD-CPE-VALIDATION.md`](FIELD-CPE-VALIDATION.md). For N5368X, use [`HUAWEI-N5368X-SECURE-QUALIFICATION.md`](HUAWEI-N5368X-SECURE-QUALIFICATION.md) as the security-specific execution order.
2. Point the device's `ManagementServer.URL` at the controlled test ACS with capture enabled before the qualifying reconnect. Supplementary debug logs may be used when needed, but sanitize secrets before retaining them.
3. Record exact vendor, model, hardware revision and firmware version.
4. For the production bootstrap path, walk: constrained bootstrap Inform -> device-bound PENDING credential -> credential-install SPV -> new Digest reconnect with matching OUI/ProductClass/Serial -> ACTIVE credential -> post-graduation bootstrap rejection. If remote credential rewrite is unsupported, retain PENDING and document an explicit manual installation step instead of weakening steady-state authentication.
5. After graduation, walk: empty/session-close behavior -> GPN/GPV -> SPV of a reversible test parameter -> Connection Request -> diagnostics -> upload/download where supported.
6. Test direct Connection Request first, then STUN/Annex G if the device sits behind NAT.
7. Where safe on a dedicated test unit, test reboot, approved firmware download/TransferComplete and recovery after interrupted connectivity.
8. Add/update the vendor row with every compatibility knob required. Do not silently make a weak vendor-specific setting the fleet-wide default.
9. Commit a sanitized fixture under `backend/test/fixtures/` whenever a device exposes a new valid SOAP, namespace, HTTP-auth, compression, fault or USP message shape.

## Production compatibility rule

A CPE is **supported** only when its exact firmware appears in this matrix with a reproducible successful onboarding/session test. For production CWMP onboarding, that evidence includes constrained bootstrap and successful unique bound-credential graduation (or a documented secure manual installation path when the exact firmware blocks remote credential rewrite). The generic parser, mock harness and reference-agent interop maximize the chance that an unknown CPE connects; they do not replace hardware qualification.
