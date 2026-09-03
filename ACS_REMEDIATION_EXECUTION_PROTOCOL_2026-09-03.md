# ACS Remediation & Production-Readiness Execution Protocol

**Version:** 2026-09-03  
**Repository:** `WillemKlopper87/ACS`  
**Baseline reviewed:** `main` at `7cf5768978ce2c0f6c9aacd10331a5229d310ee7`  
**Compatibility hardening branch:** `cpe-compatibility-hardening-2026-09-03`  
**Compatibility PR:** #12  
**Purpose:** turn the current ACS into a production-qualified, multi-tenant TR-069/CWMP control plane without regressing heterogeneous CPE interoperability.

---

## 1. How to use this document

This is an execution protocol, not a descriptive audit.

Every remediation item has:

- an invariant: what must always be true;
- implementation work;
- required tests/evidence;
- a completion gate.

An item is **not complete** because code exists. It is complete only when its tests pass and the required operational evidence is recorded.

Severity vocabulary:

- **P0 / Critical:** production release blocker; credible cross-tenant or fleet-wide control risk.
- **P1 / High:** production release blocker unless explicitly constrained by deployment architecture.
- **P2 / Medium:** hardening/reliability issue; schedule before broad production expansion.
- **Qualification gate:** cannot be closed by static code review; requires hardware, load or operational evidence.

---

# 2. Current state

The ACS is no longer a prototype. The current system already has substantial production-oriented foundations:

- PostgreSQL-backed devices, sessions and jobs;
- CWMP Inform/session processing and multiple RPCs;
- parameter cache/history;
- firmware upload/download and canary rollout logic;
- CPE-to-ACS Upload support;
- HTTP and Annex G Connection Request paths;
- STUN support;
- operator JWT authentication and RBAC;
- tenant/customer/region data structures;
- per-device authorization guard;
- BSS adapter and OAuth2 client credentials;
- per-device credentials and encryption at rest;
- object-store abstraction;
- migration locking/checksums;
- race-enabled backend tests;
- real PostgreSQL integration tests;
- OpenAPI validation and generated frontend types;
- frontend E2E/accessibility checks;
- SBOM generation and vulnerability scanning;
- backup/restore tooling.

The principal remaining architectural weakness is that **device authorization is stronger than control-object authorization**. Policies, templates, schedules, groups and rollouts can outlive the HTTP request that created them and later act asynchronously. Their authorization scope therefore has to be persisted and re-enforced at execution time.

---

# 3. Phase C0 — CPE compatibility-first transport baseline

## Objective

Maximize the probability that standards-compliant and common legacy CPE implementations can establish and maintain a CWMP session without weakening tenant or operator authorization.

## Status

**Implemented on compatibility branch / PR #12; automated CI and real-device qualification remain gates.**

## Implemented compatibility behavior

### C0.1 Flexible CWMP endpoint path

The ACS catch-all CWMP HTTP handler remains compatible with CPEs configured for `/`, `/cwmp`, `/acs` or other URL paths.

**Invariant**

> A valid CWMP POST must reach the CWMP handler independent of the path component of the configured ACS URL.

### C0.2 SOAP and namespace tolerance

Inbound parsing remains based on local XML element names, allowing vendor variation in SOAP prefixes. The CPE's CWMP namespace is detected and now persisted with each CWMP session.

ACS-initiated RPCs in that session are rewritten to the same negotiated namespace rather than silently falling back to `cwmp-1-0`.

**Invariant**

> Once a CPE opens a session, every ACS RPC in that session uses the session's negotiated CWMP namespace.

**Required tests**

- Inform in `cwmp-1-0`;
- Inform in `cwmp-1-1`;
- Inform in `cwmp-1-2`;
- fixture using `cwmp-1-4`/later namespace handling;
- ACS RPC after each Inform contains the expected namespace;
- invalid namespace strings cannot inject XML.

### C0.3 Compressed CWMP request support

The ACS now accepts:

- identity/no content encoding;
- gzip;
- `x-gzip`;
- zlib-wrapped `deflate`;
- raw DEFLATE used by some legacy embedded HTTP stacks.

Unknown content encoding returns HTTP `415 Unsupported Media Type` instead of being misclassified as malformed SOAP.

Both the encoded and decompressed bodies are bounded.

**Invariant**

> Compression compatibility may not create an unbounded-memory or decompression-bomb path.

### C0.4 Empty session response compatibility

The standards-oriented default for no additional ACS RPCs is now:

`204 No Content`

A vendor escape hatch exists:

`ACS_CWMP_EMPTY_RESPONSE_STATUS=200`

for a device firmware that incorrectly requires the previous empty-200 behavior.

**Rule**

Do not set the compatibility override fleet-wide unless a real hardware matrix proves it is necessary.

### C0.5 Authentication compatibility

Existing inbound support remains:

- HTTP Digest;
- legacy non-qop Digest responses;
- optional Basic fallback via `ACS_AUTH_ALLOW_BASIC=1`;
- per-device credentials;
- optional client-certificate authentication.

**Security rule**

Basic authentication is only acceptable over TLS or an isolated management network.

### C0.6 TLS compatibility

The ACS already supports a configurable TLS minimum as low as TLS 1.0 for legacy hardware.

**Rule**

Compatibility does not mean the production default must remain TLS 1.0. Determine the highest common floor from the hardware qualification matrix and use vendor/network exceptions only where required.

### C0.7 Connection Request interoperability

PR #12 broadens the ACS-to-CPE HTTP Connection Request path:

- any HTTP 2xx is treated as an accepted wake request;
- Digest remains preferred;
- Digest challenge scheme parsing is case-insensitive;
- qop lists containing `auth` are supported;
- legacy no-qop Digest remains supported;
- MD5 and MD5-sess challenges are supported;
- `opaque` is echoed;
- Basic is used when it is the only supported challenge.

A successful HTTP response still does not mean the CWMP session opened: Event `6 CONNECTION REQUEST` Inform remains the real success signal.

### C0.8 NAT traversal paths

Keep all three reachability modes available:

1. direct HTTP Connection Request over IPv4/IPv6;
2. STUN-discovered UDP address;
3. Annex G UDP Connection Request.

Do not assume a successful lab direct-IPv4 test proves residential CGNAT compatibility.

---

# 4. CPE compatibility completion gate

The compatibility branch may be merged after automated CI passes except for already-known unrelated baseline failures, but **production compatibility is not complete until real devices are qualified**.

At minimum qualify these current catalog targets:

- Huawei 5G CPE Pro;
- Nokia FastMile 5G;
- Teltonika RUTX50;
- Zyxel NR7101 / NR5103;
- any actual operator-deployed models not yet represented by the catalogs.

Qualification is **per firmware version**, not merely per model.

For every firmware record:

1. TLS negotiation and certificate behavior;
2. first unauthenticated request/challenge/retry;
3. Inform acceptance and ID echo;
4. namespace behavior;
5. empty poll/end-session exchange;
6. GPV/GPN;
7. SPV of a reversible parameter;
8. correct handling of 900x faults;
9. Connection Request and Event 6 Inform;
10. STUN/Annex G where NAT applies;
11. diagnostics where supported;
12. upload/download and TransferComplete where supported;
13. reboot on an isolated unit;
14. interrupted-session/retry behavior;
15. sanitized packet/CWMP fixture retained for every newly discovered valid vendor variation.

**Completion criteria**

- exact firmware appears in `docs/COMPATIBILITY.md`;
- no undocumented global compatibility weakening is required;
- a reproducible automated fixture exists for every newly encountered protocol variation.

---

# 5. Phase P0 — Repair multi-tenant authorization semantics

This phase is the highest-priority production security work.

## P0.1 Zero scopes must mean zero access

### Problem

For a normal operator, the absence of `operator_scopes` currently behaves as unrestricted access. Creating a manager/NOC operator without scopes, or removing the final scope, can therefore produce fleet-wide access.

### Required invariant

> A non-superadmin with zero explicit scopes has zero tenant/device access unless an explicit platform-global entitlement is present.

### Implementation

Introduce an explicit access mode, e.g.:

- `SUPERADMIN_GLOBAL`;
- `OPERATOR_GLOBAL` only when explicitly assigned;
- `SCOPED`;
- `NO_SCOPE`.

Do not infer `GLOBAL` from an empty SQL result.

Operator creation should preferably be transactional with initial scope assignment when a scoped role is being created.

### Required tests

- `test_new_manager_has_no_devices_until_scope_assigned`;
- `test_new_noc_has_no_devices_until_scope_assigned`;
- `test_removing_last_scope_does_not_grant_global_access`;
- `test_explicit_global_operator_can_access_all_customers`;
- `test_superadmin_remains_global`.

### Completion gate

Two-tenant DB-backed integration tests demonstrate fail-closed behavior for new and de-scoped operators.

---

## P0.2 Make control-plane resources tenant-aware

### Problem

The following objects are currently effectively global even when created by a scoped operator:

- device groups;
- configuration templates;
- continuous policies;
- scheduled jobs;
- firmware rollouts.

Those resources can later mutate devices asynchronously.

### Required invariant

> A control object can only target devices contained in its persisted authorization scope.

### Data model

Every applicable object must carry one of:

- `customer_id`;
- `region_id`;
- normalized `scope_id` / scope expression;
- explicit `platform_global` ownership restricted to suitable administrators.

Also persist:

- `created_by_operator_id`;
- authorization/scope revision or snapshot as needed for traceability.

### Execution rule

Authorization must be checked:

1. when the object is created/modified;
2. again when it executes.

Rechecking at execution protects against a device moving from one customer to another after a schedule/policy was created.

---

## P0.3 Device groups

### Required work

- scope group ownership;
- validate every device on add/remove;
- filter list/get/member IDs by caller scope;
- prevent group use from becoming a cross-tenant bulk-action bypass.

### Tests

- scoped manager cannot add other-customer device;
- scoped readonly cannot enumerate foreign member UUIDs;
- group target fan-out includes only authorized devices;
- device customer reassignment invalidates/removes unauthorized target at execution.

---

## P0.4 Configuration templates

### Required work

- scope template ownership;
- validate arbitrary `device_ids` before job creation;
- validate `group_id` and every resolved member;
- make auto-apply templates scope-aware in the CWMP gateway.

### Critical invariant

> A tenant-created auto-apply template may never apply to another tenant merely because manufacturer/model matches.

---

## P0.5 Continuous policies

### Required work

Policies must include scope. `ForDevice()` or the enforcement layer must filter by both model and tenant ownership.

### Critical invariant

> A matching model is never sufficient authorization to mutate a device.

### Required test

Create same-model CPEs in Customer A and Customer B; create policy as A-scoped manager; Inform both devices; only A may receive a corrective job.

---

## P0.6 Scheduled jobs

### Problem

Target authorization is lost after the HTTP request and the scheduler later directly creates jobs.

### Required work

Persist target authorization and validate at create plus fire time.

### Tests

- foreign device UUID target rejected;
- foreign group target rejected;
- group membership changed after schedule creation is re-evaluated;
- device moved customer before fire is skipped/blocked and audited.

---

## P0.7 Firmware rollouts

### Required work

Rollout eligibility must be computed inside rollout scope, never against the global fleet for a scoped operator.

Scope both:

- rollout metadata visibility;
- candidate-device selection;
- detail/status rows;
- rollback target selection.

### Completion gate for Phase P0

A two-tenant integration suite must attempt every orchestration path as a scoped manager and prove no read or write escapes its customer.

---

# 6. Phase P1 — CWMP and control-plane integrity

## P1.1 Bind TransferComplete to the authenticated/current CPE

### Problem

`TransferComplete` is correlated by `CommandKey`, but the sending CPE is not proven to equal `job.DeviceID` before the job is completed.

### Invariant

> `TransferComplete.CommandKey` identifies a job **and** the current authenticated/session device equals the job device.

### Required work

Derive trusted current device identity for asynchronous CPE-originated RPCs and reject/audit mismatches.

### Tests

- correct device succeeds;
- second device with valid-but-foreign command key cannot alter job;
- delayed duplicate from correct device remains idempotent.

---

## P1.2 ConnectionRequestURL is a controlled outbound-network boundary

### Problem

A CPE supplies `ManagementServer.ConnectionRequestURL`; the ACS later requests it. This is necessary functionality but creates an SSRF-capable trust boundary.

### Compatibility constraint

Do **not** solve this by globally blocking RFC1918 addresses: operator-managed CPEs commonly have private management addresses.

### Required design

Use an operator-configurable network policy:

- permitted schemes;
- allowed management CIDRs/networks;
- explicit loopback/link-local/metadata denial unless deliberately approved;
- DNS resolution validation;
- redirect revalidation;
- port policy;
- timeouts.

The policy should be topology-aware rather than internet-browser-style.

---

## P1.3 Fix simultaneous CPE Upload object race

### Problem

Two PUTs can write the same object key; the losing DB transition removes the shared object and can delete the winner's recorded file.

### Invariant

> A successful `RECEIVED` database row always has exactly one corresponding object whose digest matches the row.

### Preferred implementation

Either:

- atomically claim `PENDING -> RECEIVING` before storing; or
- write each request to a unique temporary key and atomically promote only the winner.

### Test

Launch genuinely simultaneous PUT requests to one slot and verify one winner, one conflict and one valid retained object.

---

## P1.4 Firmware upload total-size limit

Apply an explicit total request/file limit using `http.MaxBytesReader` or equivalent.

Add deployment setting such as:

`ACS_FIRMWARE_MAX_BYTES`

`ParseMultipartForm(maxMemory)` is not a total file-size limit.

---

## P1.5 Browser ticket token-version propagation

Browser tickets for CLI/WebGUI must carry the authenticated operator's current token version.

### Tests

- login -> ticket works;
- logout -> old ticket invalid;
- login again -> new ticket works;
- password reset -> old ticket invalid;
- login again -> new ticket works.

---

## P1.6 Distributed Digest replay protection

The current nonce/replay cache is process-local.

For multiple CWMP replicas, choose and document one architecture:

- shared replay state in Redis/Postgres;
- cryptographic/stateless design with equivalent replay guarantees;
- or sticky routing whose security assumptions are explicit and tested.

Do not claim multi-replica replay protection while replay state is local to one process.

---

## P1.7 Command-aware stale-job recovery

### Problem

Generic lease expiry/requeue is not equally safe for all CWMP methods.

### Classify job types

- `IDEMPOTENT` — safe to retry;
- `RECONCILABLE` — inspect device state before retry;
- `NON_REPEATABLE` — require manual/explicit recovery if delivery is uncertain.

Particular attention:

- AddObject;
- DeleteObject;
- FactoryReset;
- Reboot;
- Download/firmware;
- Upload.

### Invariant

> Process failure after a CPE performed an action may not cause blind repetition of a destructive RPC.

---

## P1.8 Device liveness state machine

Derive online/offline from evidence rather than a one-way online update.

Inputs should include:

- `last_inform_at`;
- PeriodicInform interval when known;
- configurable grace multiplier;
- last Connection Request outcome;
- boot/reboot behavior.

### Tests

- missed intervals transition offline;
- late Inform restores online;
- grace window prevents flapping;
- devices with no known interval use documented fallback.

---

## P1.9 CWMP session cookie policy

Make cookie properties explicit and interoperability-tested.

Consider configurable:

- `Secure` when HTTPS is used;
- SameSite behavior;
- lifecycle/expiry semantics.

Do not blindly apply browser-cookie assumptions without testing real CPE cookie implementations.

---

# 7. Phase P2 — confidentiality, revocation and supply chain

## P2.1 Tenant-scope audit log reads

Audit entries containing device IDs, parameter values, command keys or actions must be filtered by authorization scope unless the caller has an explicit platform audit permission.

Test cross-tenant audit enumeration directly.

---

## P2.2 Classify every API resource by tenancy model

Maintain a table in code/docs declaring every REST resource as:

- platform-global;
- region-owned;
- customer-owned;
- device-derived.

No new endpoint may be merged without a tenancy classification.

---

## P2.3 BSS OAuth revocation semantics

Revoking a BSS OAuth client prevents new tokens, but already issued self-contained JWTs can remain valid until expiry.

Choose explicitly:

- accept bounded one-hour residual lifetime;
- shorten token TTL;
- add client token-version/revocation epoch;
- or perform active-client validation for high-risk endpoints.

Document emergency credential-compromise procedure.

---

## P2.4 Browser operator token storage

The frontend currently uses browser local storage for the bearer token.

Long-term options:

- BFF/HttpOnly session cookie;
- short-lived access + protected refresh mechanism;
- or strong CSP plus carefully minimized token lifetime if local storage remains.

Treat XSS impact as fleet-control impact, not merely UI compromise.

---

## P2.5 CI immutability

Pin:

- GitHub Actions to immutable commit SHAs;
- security/tooling versions;
- Syft installation artifact/version;
- avoid `@latest` in a release gate.

Maintain Dependabot/Renovate-style controlled update flow rather than mutable execution at CI runtime.

---

## P2.6 Scan every production image

Trivy or equivalent must gate:

- `acs-acs`;
- `acs-api`;
- `acs-bssadapter`;
- `acs-frontend`.

SBOM generation already covers all four and should remain tied to the exact release digest.

---

# 8. Current dependency/CI blocker

At the reviewed baseline, `backend/go.mod` uses:

`golang.org/x/crypto v0.54.0`

The existing container vulnerability gate reports a fixed Critical advisory with `v0.55.0` as the fixed version.

The reported affected symbol is SSH server-side functionality (`ssh.NewServerConn`), while ACS primarily uses SSH as a client. That distinction matters for exploitability analysis, but it does **not** justify leaving the release branch red.

### Required work

- upgrade to the fixed dependency version or newer compatible release;
- update `go.sum` through normal Go tooling;
- run unit/race/integration tests;
- rerun all image scans;
- document reachability assessment if the vulnerability scanner and call-graph scanner differ.

**Release invariant**

> Production release commits must have a green required CI status; scanner suppression requires a reviewed, documented exception with expiry.

---

# 9. Protocol compatibility work still requiring hardware evidence

The compatibility hardening intentionally does not pretend to solve vendor behavior that cannot be proven without CPEs.

## Explicit CWMP 1.4+ version negotiation

The parser tolerates later namespace variants and the compatibility branch preserves the CPE namespace. Explicit `SupportedCWMPVersions` / `UseCWMPVersion` SOAP negotiation can be added if the product declares itself a CWMP 1.4+ ACS.

Do not advertise a higher protocol version merely for marketing/compatibility. First verify every feature the declared version requires and add negotiation/fault tests.

## SOAPAction/content-type quirks

The ACS currently does not depend on a specific SOAPAction value, which is compatibility-friendly. Preserve that tolerance unless a security requirement demands stricter validation.

## Parameter value typing

Real-device qualification must verify that SPV serialization uses types accepted by each model, particularly integer, boolean, unsigned and date/time parameters. If any CPE rejects string-typed values where a concrete type is required, promote typed SPV rendering to a compatibility remediation with fixtures.

## Vendor extension paths

Do not hard-code a global Huawei/Nokia/Zyxel/Teltonika assumption. Capture vendor catalogs and discovered data-model roots per hardware/firmware.

---

# 10. Required invariant test suite

Create/maintain a dedicated suite whose names describe platform guarantees.

## Tenancy

```text
test_new_non_superadmin_has_zero_device_access
test_removing_last_operator_scope_does_not_grant_global_access
test_scoped_manager_cannot_target_other_customer_device_in_schedule
test_scoped_manager_cannot_apply_template_to_other_customer
test_scoped_manager_cannot_add_other_customer_device_to_group
test_scoped_manager_policy_never_applies_to_other_customer
test_scoped_manager_rollout_contains_only_authorized_devices
test_scoped_readonly_cannot_read_cross_customer_audit_events
```

## CPE compatibility

```text
test_inform_accepts_standard_and_vendor_paths
test_inform_namespace_is_persisted_for_session
test_outbound_rpc_uses_session_namespace
test_gzip_inform
test_xgzip_inform
test_zlib_deflate_inform
test_raw_deflate_inform
test_unsupported_content_encoding_returns_415
test_empty_session_defaults_204
test_empty_session_vendor_override_200
test_connection_request_accepts_200_202_204
test_connection_request_digest_qop_list
test_connection_request_md5_sess
test_connection_request_basic_fallback
```

## CWMP integrity

```text
test_transfer_complete_must_match_job_device
test_value_change_command_key_must_match_current_device
test_connection_request_network_policy_rejects_unapproved_loopback
test_connection_request_redirect_is_revalidated
test_digest_replay_rejected_across_replicas
```

## Concurrency/recovery

```text
test_parallel_upload_puts_leave_exactly_one_valid_object
test_stale_add_object_job_is_not_blindly_reexecuted
test_stale_factory_reset_requires_safe_reconciliation
```

## Authentication

```text
test_browser_ticket_uses_current_token_version
test_browser_ticket_works_after_logout_and_relogin
test_browser_ticket_works_after_password_reset_and_relogin
```

## Liveness

```text
test_missed_periodic_informs_transition_device_offline
test_late_inform_returns_device_online
test_liveness_grace_avoids_flapping
```

---

# 11. Real-device qualification protocol

For each vendor/model/firmware combination:

## Stage A — passive onboarding

- packet capture/sanitized HTTP trace;
- DNS/TCP/TLS negotiation;
- authentication challenge/retry;
- Inform only;
- no writes.

## Stage B — read operations

- GetRPCMethods;
- GetParameterNames at correct root;
- GetParameterValues for DeviceInfo/ManagementServer;
- parameter discovery pagination/depth as relevant.

## Stage C — reversible provisioning

- SetParameterValues on a known reversible test parameter;
- read-back confirmation;
- VALUE CHANGE behavior;
- correct value type and fault handling.

## Stage D — reachability

- HTTP Connection Request;
- IPv6 where applicable;
- STUN/NAT status;
- Annex G behind NAT/CGNAT topology.

## Stage E — diagnostics

- IPPing;
- TraceRoute;
- state polling to terminal result;
- timeout/fault paths.

## Stage F — disruptive operations on test hardware only

- reboot;
- firmware download;
- TransferComplete;
- software-version confirmation;
- rollback exercise where vendor image allows;
- FactoryReset only when re-onboarding recovery is prepared.

## Stage G — failure injection

- close TCP connection mid-session;
- ACS restart while RPC is in flight;
- duplicate CPE response;
- delayed TransferComplete;
- expired credentials/token;
- NAT address change;
- DNS failure/object-store failure where relevant.

Store only sanitized fixtures; never commit CPE passwords, private keys or live subscriber identifiers.

---

# 12. Fleet-scale qualification

Static correctness is not enough for ACS production.

Run at least:

- 1k concurrent mock CPEs;
- 10k session burst model if target deployment warrants it;
- realistic PeriodicInform distribution/jitter;
- queued job fan-out;
- connection-request storm bounded by rate controls;
- firmware canary wave load;
- DB restart/failover;
- object-store latency/failure;
- multi-replica gateway if HA is intended;
- 24–72 hour soak.

Record:

- sessions/sec;
- p50/p95/p99 request latency;
- PostgreSQL connections/query latency;
- queue depth/lease age;
- job retry counts;
- goroutines/memory/CPU;
- dropped/failed Informs;
- rate-limit rejects;
- firmware/object-store throughput.

Define target SLOs before calling results acceptable.

---

# 13. Disaster recovery and upgrade qualification

Production gate requires measured evidence for:

- Postgres backup restore;
- object-store restore/consistency;
- credential/encryption-key recovery procedure;
- migration from N-1 release to N;
- rollback procedure when schema permits;
- rolling deployment while CPE sessions are active;
- stale lease/session recovery after service restart;
- Alertmanager/notification delivery;
- measured RTO/RPO.

A backup script existing in the repository is not equivalent to a restore drill.

---

# 14. Recommended execution order

## Wave 0 — compatibility branch

- [x] session CWMP namespace persistence;
- [x] ACS outbound namespace rewrite;
- [x] gzip/x-gzip/deflate request handling;
- [x] decompressed body cap;
- [x] 204 empty response + 200 escape hatch;
- [x] Connection Request 2xx normalization;
- [x] Connection Request Digest variants + Basic fallback;
- [x] compatibility regression tests added;
- [x] compatibility matrix updated;
- [ ] PR CI backend/migrations/integration green;
- [ ] real hardware matrix started.

## Wave 1 — immediate containment

- [ ] zero-scope deny-by-default;
- [ ] temporarily restrict global policy/template/schedule/rollout/group administration to superadmin until scoped ownership lands;
- [ ] repair current dependency/CI vulnerability gate.

## Wave 2 — proper tenant-aware orchestration

- [ ] scoped groups;
- [ ] scoped templates and auto-apply;
- [ ] scoped policies/enforcement;
- [ ] scoped scheduled jobs/worker;
- [ ] scoped rollouts/rollback;
- [ ] scoped audit views.

## Wave 3 — protocol/control integrity

- [ ] TransferComplete device binding;
- [ ] topology-aware Connection Request outbound network policy;
- [ ] CPE upload race repair;
- [ ] firmware upload hard limit;
- [ ] browser-ticket token version;
- [ ] HA Digest replay design;
- [ ] command-aware retry semantics;
- [ ] liveness state machine.

## Wave 4 — release hardening

- [ ] scan all images;
- [ ] immutable CI action/tool versions;
- [ ] operator browser-session hardening decision;
- [ ] BSS revocation policy;
- [ ] explicit resource tenancy classification.

## Wave 5 — qualification

- [ ] hardware interoperability matrix;
- [ ] NAT/Annex G field validation;
- [ ] load/soak;
- [ ] restore/failover;
- [ ] rolling upgrade;
- [ ] alert delivery;
- [ ] RTO/RPO evidence.

---

# 15. Release decision matrix

## Lab / developer environment

Allowed when:

- migrations apply;
- backend compiles/tests;
- test credentials only;
- no real subscriber fleet at risk.

## Single-operator controlled staging

Allowed when:

- compatibility branch is merged and automated tests are green;
- authentication is fail-closed;
- target CPE firmware is hardware-qualified;
- current critical dependency gate is resolved.

## Single-tenant production pilot

Requires:

- P1 transfer/recovery integrity issues addressed or explicitly operationally constrained;
- real CPE matrix;
- load/soak evidence;
- restore drill;
- green CI.

## Multi-tenant production

**Blocked until all P0 tenancy work is complete.**

No exception should allow a scoped tenant operator to create a control object capable of changing another customer's CPE.

---

# 16. Definition of production-ready

ACS may be called production-ready only when all of the following are true:

1. no open P0 finding;
2. required P1 findings are closed or have narrowly documented architecture constraints;
3. CI is green on the exact release commit;
4. all production images pass vulnerability gates;
5. target CPE models/firmware are in the real compatibility matrix;
6. tenant-isolation integration tests cover synchronous **and asynchronous** paths;
7. destructive RPC retry semantics are explicit;
8. DB/object-store restore has been demonstrated;
9. target fleet load and soak tests meet defined SLOs;
10. HA assumptions are tested if more than one replica is deployed;
11. release/rollback procedure is rehearsed;
12. monitoring and alert delivery are demonstrated.

---

# 17. Architectural principle to preserve

The key design principle for the next phase is:

> **Authorize intent, not only endpoints.**

A CPE management platform does not stop acting when the operator's HTTP request ends. Policies, templates, schedules, groups and rollouts continue to act later, often in a different process and with no live JWT request context.

Therefore authorization has to travel with the control object and be enforced again when that object acts.

The compatibility principle is complementary:

> **Be tolerant in wire-format interoperability, strict at identity and tenant boundaries.**

Accept harmless CPE variation in paths, SOAP prefixes, CWMP namespaces, compression, HTTP success codes and legacy authentication modes where explicitly configured. Never trade that tolerance for cross-tenant access, unauthenticated production control or unbounded input handling.
