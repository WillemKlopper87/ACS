# ACS code review: 2026-10-30

> **Status at end of day 2026-10-30** (branch `fix/cwmp-cpe-initiated-rpcs`, PR #79). This review was written in the morning against `main`; the findings below are unchanged, so read this note for what was fixed since.
>
> **Fixed**
> - **C1:** diagnostics wait for a later session.
> - **C8:** typed diagnostic inputs.
> - **C4:** GetRPCMethods is answered; unknown methods get Fault 8000.
> - **C11, in part:**
>   - `HasEventCode` matches the whole event token.
>   - A lost RebootResponse is reconciled from `M Reboot`.
>   - Jobs from dropped sessions are recovered.
> - **D2, in part:** Go 1.26.10. Rerun `govulncheck` to confirm.
>
> **Partly fixed**
> - **S1:** secret-named parameters are now stored as `***REDACTED***` in the cache and its history (migration 0085 scrubs old rows). Still open:
>   - The SPV values remain in `jobs.payload`.
>   - Scheduled-job payloads are returned by `GET /schedules`.
>   - Digest rotation still goes through SPV, not the encrypted credential store.
>
> **Still open:** S2–S10, C2 (Download `Status=0` still waits for TransferComplete), C3, C5–C7, C9, C10, D3–D8.
>
> **D1 still applies.** On 2026-10-30 every CI job on PR #79 was refused with *"recent account payments have failed or your spending limit needs to be increased"*. No CI job has run, so the DB-backed tests have not run anywhere.

Reviewed `main` @ `8ed5d7f` (merge of PR #76, Nokia cloud qualification, 2026-10-20), read-only.
Context: preparing a lab field test of a Nokia FastMile 5G Gateway 2 (HW `3TG02241AAAA`, SW `1.2501.00.0798`) against ACS on AWS EC2 (af-south-1).

## Local checks

| Check | Result |
|---|---|
| `go build ./...`, `go vet ./...` (go1.26.6) | clean |
| `go test ./...` | 44 packages ok, 0 failures (DB-backed integration tests skip without Postgres) |
| Frontend `oxlint` / `tsc -b && vite build` / `vitest` | clean / ok / 65 of 65 pass |
| `npm audit --omit=dev` | 0 vulnerabilities |
| `govulncheck` v1.7.0 | **13 reachable**: Go stdlib (fixed in go1.26.9/1.26.10), `golang.org/x/net` (fixed in v0.60.0), `excelize` (pseudo-version fix) |
| Migrations | 0001–0078 contiguous, advisory-locked, checksummed; late-0062 upgrade path resolved |
| Secrets in history | none found (768 commits, all refs) |

## Verdict for the Nokia lab test: GO, with three caveats

The CWMP first-contact path is ready for a single-operator, single-CPE lab: bootstrap Digest, then credential graduation, then a bound reconnect, then read-only discovery. `scripts/quickstart.sh` is a working one-shot installer. Caveats:

1. **CPE-initiated `GetRPCMethods` is not answered (verified).** `internal/cwmp/envelope.go:25-63` has no field for an inbound `GetRPCMethods` request, so `IsEmpty()` treats it as an empty POST. During bootstrap the ACS would reply to the CPE's request with the credential-install SPV (`cmd/acs/bootstrap_graduation.go:153`) instead of a `GetRPCMethodsResponse`. Unknown CPE methods also get no Fault 8000. If the FastMile sends GetRPCMethods on `0 BOOTSTRAP`, first contact can loop. This is a small fix; do it before the test, or watch the capture for it.
2. **Firmware Download with `Status=0` is marked FAILED after 24h (verified).** `cmd/acs/dispatch.go:376-389` always waits for TransferComplete, which TR-069 doesn't send when the download completes synchronously. Don't trust Download job results on this CPE until this is fixed. (Firmware isn't in the first-test scope anyway.)
3. **Build on Go 1.26.10** (`GO_VERSION=1.26.10 ./scripts/quickstart.sh`). Port 7547 faces the internet, and govulncheck reports reachable net/http and crypto/tls issues fixed in 1.26.9/1.26.10.

## Findings

### Security

| # | Sev | Finding | Where | Verified |
|---|---|---|---|---|
| S1 | **High** | Per-device CWMP Digest (and CR, Wi-Fi) passwords written by SetParameterValues jobs land in plaintext in the parameter cache and `parameter_history`, readable by `readonly` operators. The leaked credential lets the reader impersonate the device. | `cmd/api/credential_handlers.go:81-93` → `cmd/acs/dispatch.go:329-337` → `GET /devices/{id}/parameters[/history]`; no redaction anywhere in `internal/parameters` | yes |
| S2 | **High** | Bootstrap graduation is trust-on-first-use. Anyone holding the fleet-wide bootstrap secret can Inform as any not-yet-established OUI/serial, including pre-registered devices, and receive its new per-device password. `EnsurePendingCWMPDigest` re-serves the same PENDING secret on every attempt. | `cmd/acs/bootstrap_graduation.go:173-256`, `internal/credentials/bootstrap.go:39-48` | yes |
| S3 | **High** | An account-scoped BSS OAuth client can map any device in the fleet to its account (lookup by `oui_serial`, no tenant check, no per-device uniqueness), then push orders such as `MODIFY_WIFI` to it. | `cmd/bssadapter/main.go:527-597`, `tmf641.go:~131`, `internal/bss/mapping.go:206`, migration 0052 | agent-traced |
| S4 | Med | The production guard's Digest username extractor takes the *first* `username` directive, case-insensitively. The real parser takes the *last*, case-sensitively. A duplicate `username` slips the shared credential past the production ban for first-contact Informs. | `cmd/acs/mux.go:307-316` vs `internal/auth/digest.go:497-510` | yes |
| S5 | Med | Legacy `/bss/v1/*` routes ignore OAuth scopes (account membership only). | `tmf_authz.go:89-112` | agent-traced |
| S6 | Med | A scoped manager can move their devices into another tenant: the target `customer_id` isn't scope-checked. Bulk import does check it. | `cmd/api/tenancy_handlers.go:161-183` | agent-traced |
| S7 | Med | The web-GUI proxy serves device HTML/JS without CSP or sandbox, and forwards the operator's reusable, not-device-bound browser ticket to the CPE. | `cmd/api/webgui_handlers.go:122-185`, `DeviceWebGUI.tsx:125` | agent-traced |
| S8 | Med | An mTLS client cert with an empty CN skips every device-binding check. | `cmd/acs/session.go:137` | agent-traced |
| S9 | Low–Med | Production doesn't require outbound CIDR allowlists for cmd/api and bssadapter, so RFC1918 is reachable via CR URL, web-GUI and webhooks (SSRF). | `cmd/api/main.go:201-226`, `cmd/bssadapter/main.go:149` | agent-traced |
| S10 | Low | Global legacy `FindOrder` (cross-account order lookup); tenant names visible to scoped operators; raw SOAP body (up to 4 MiB, may contain secrets) logged on parse error (`session.go:173`); OAuth account narrowing takes up to 1h to apply. | various | agent-traced |

### Correctness and reliability

| # | Sev | Finding | Where |
|---|---|---|---|
| C1 | **High** | CWMP diagnostics (IPPing, TraceRoute, TR-143) are re-leased and polled repeatedly *within the triggering session*, so they burn their attempts in seconds and end TIMEOUT before the CPE starts. `8 DIAGNOSTICS COMPLETE` is not handled. | `cmd/acs/dispatch.go:405-449, 626-643`, `session.go:519-537` |
| C2 | **High** | Download/Upload `Status=0` (completed synchronously) are marked FAILED `TRANSFER_TIMEOUT` after 24h, which skews rollout failure rates. **Verified.** | `cmd/acs/dispatch.go:376-403` |
| C3 | **High** | USP async Operate loses its lease after 15 minutes: `ExtendLease` has no callers, so long firmware downloads fail, and repeatable TR-143 runs get dispatched twice. | `cmd/uspc/dispatcher.go:711-717, 1052-1095`, `internal/jobs/lease.go` |
| C4 | Med | CPE-initiated RPCs (GetRPCMethods, AutonomousTransferComplete, RequestDownload, …) are unanswered, and unknown methods get no Fault 8000. **Verified.** | `internal/cwmp/envelope.go:25-63`, `cmd/acs/session.go:206-211` |
| C5 | Med | The lease reaper's unsafe-type pass and requeue pass are separate statements, leaving a narrow window to re-dispatch a Reboot, FactoryReset or Download. | `internal/jobs/lease.go:245-289` |
| C6 | Med | A stale USP reply can resolve a newer attempt (no attempts comparison); the USP single-flight guard is check-then-act. | `cmd/uspc/dispatcher.go:204-221, 985-1003` |
| C7 | Med | Device paging is ordered by `last_inform_at DESC` with no tiebreak, so TMF639 offset paging skips and duplicates devices on a live fleet. | `internal/devices/repository.go:265-269`, `window.go:52-54` |
| C8 | Med | Numeric diagnostic inputs are sent as `xsd:string`, so strict CPEs reply 9006. | `cmd/acs/dispatch.go:121-154`, `internal/cwmp/rpc.go:91-96` |
| C9 | Med | Webhook deliveries are not claimed (double-send across replicas) and are sent sequentially with a 10s timeout; the TMF event fan-out only looks back 24h. | `internal/bss/webhook.go:229-258`, `cmd/bssadapter/webhook_worker.go` |
| C10 | Med | The connection-request worker leases 1 job per second per replica (10k devices ≈ 2.8h). | `cmd/api/connreq_worker.go:111-129` |
| C11 | Low | CGNAT periodic-inform tightening never retries; CR confirmation compares clocks from two hosts; CWMP sessions never expire; lost RebootResponse isn't reconciled from `M Reboot`/`1 BOOT`; `HasEventCode` matches only the first character; terminal status writes have no status guard. | various |

### Build, CI and deploy

| # | Sev | Finding |
|---|---|---|
| D1 | **Critical** | **CI has been blocked by GitHub Actions billing since about 2026-09-23.** The last green `ci` run on main was 2026-09-17 (80 commits ago). The 10-08 run also hit "artifact storage quota". There is no branch protection (not available on the free private plan), so PR #76 merged with every job red. |
| D2 | High | 13 reachable vulns (see above); Dependabot's x/net PR only reaches v0.59 and doesn't bump excelize. The EC2 guide pins go1.26.6. |
| D3 | High | `.trivyignore.yaml` util-linux exceptions expired 2026-10-17, so the frontend image scan will fail once CI runs. The 10-08 run also showed 3 high npm **dev**-dependency vulns (brace-expansion, source-map-js, undici). |
| D4 | Med | The frontend image runs nginx as root. Backend images are distroless nonroot (good). Base images are pinned by tag only, not digest. |
| D5 | Med | The containerized compose path is lab-only out of the box: plaintext CWMP Digest on 0.0.0.0:7547, plaintext USP, `sslmode=disable`. |
| D6 | Med | **The repo is private, so the documented `curl raw.githubusercontent.com/... \| bash` quickstart returns 404** (confirmed on the EC2 box). Clone via deploy key or PAT, then run `./scripts/quickstart.sh`. |
| D7 | Med | `HANDOFF.md` is stale: it claims "checks on main pass" and "83 tests"; there are now 758 test functions. |
| D8 | Info | 15 non-Dependabot branches are all stale or superseded (content on main). Check `feat/public-monitoring-ingress` and `fix/field-rc-ci-baseline` before deleting. `__placeholder_do_not_use` is safe to delete. |

## What's solid

- **Auth:** JWT pinned to HS256 with constant-time compare, expiry and token-version revocation; secrets of 32+ bytes required. Digest has HMAC-signed nonces, Postgres-backed nc replay protection and MD5/SHA-256.
- **CWMP:** per-device session binding (C-1) on Inform, dispatch and TransferComplete. Production fails closed (`security-preflight.sh` forces TLS, loopback binds, no Basic, no plaintext USP).
- **Access control:** RBAC on every route; tenant re-checks on device, job, credential and capture handlers.
- **Jobs and BSS:** leases use `FOR UPDATE SKIP LOCKED`; destructive job types dead-letter instead of blind retry. The BSS order outbox and idempotency keys are correct under concurrency.
- **Parsing and files:** bounded bodies, `encoding/xml` (no XXE), no dynamic SQL from input; transfer files use HMAC single-use tokens.
- **CI design** (when it runs): SHA-pinned actions, `contents: read`, no `pull_request_target`; race tests, migration concurrency, obuspa USP interop, Trivy, SBOM and gitleaks.
- **Hygiene:** no secrets in git history.

## Priorities

1. **Before the Nokia test:** answer CPE-initiated `GetRPCMethods` (C4), and build with Go 1.26.10.
2. **Fix Actions billing** and clear artifact storage, so CI runs again.
3. Redact secret-class parameters and move Digest rotation into the encrypted credential store (S1).
4. Bootstrap TOFU: require operator approval or a per-device secret for pre-registered devices, and don't re-serve PENDING secrets (S2). Unify the Digest parser (S4).
5. Tenant-check BSS device mapping (S3, S5) and `assignDeviceCustomer` (S6).
6. Fix diagnostics polling (C1), Download `Status=0` (C2) and the USP lease extension (C3) before diagnostics or firmware qualification.
7. Bump x/net to v0.60+ and excelize; renew the Trivy exceptions or merge the nginx bump; update HANDOFF.md; prune stale branches.
