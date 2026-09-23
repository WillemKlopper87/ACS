# TMF, TR Standards, and BSS/OSS Completion Plan

**Status:** Proposed implementation plan; no new code work is authorized by this document alone.
**Prepared:** 2026-09-23
**Baseline:** Repository commit `beb915a` plus local uncommitted changes. Release claims must be rechecked against a clean merged commit.

## Objective

Define, implement, test, document, and qualify an explicit set of TM Forum Open APIs, Broadband Forum TR protocols/data models, and BSS/OSS integrations. The completion claim will apply only to the named API versions, protocol profiles, and device/partner matrix approved in scope. “All TR-xxx” cannot be the acceptance criterion: the TR series is a large and evolving family, and ACS is not expected to implement every report or data model.

## Current position

- The TMF status page lists seven implemented API families: TMF638, TMF639, TMF640, TMF641, TMF642, TMF656, and TMF688. They expose selected operations and are not certified/conformance-tested as a complete set of TM Forum APIs.
- The current API routes use TMF638 v4, TMF639 v5, TMF640 v4, TMF641 v4, TMF642 v4, TMF656 v4, and TMF688 v4. The 2026-09-14 northbound programme design proposes v5 for TMF638, TMF642, and TMF656. These conflicts require an explicit version decision before contract work.
- Only TMF639 currently has its own OpenAPI file. `openapi-bssadapter.yaml` covers `/bss/v1`, not all TMF APIs.
- Protocol code covers a subset of CWMP/TR-069 and USP/TR-369. Current docs explicitly list XMPP Connection Requests as not implemented, the full TR-098 write catalog as partial, Annex G as implemented but not field-validated, and physical-device qualification as outstanding.
- The current source checkout includes uncommitted TMF641 recovery work (migration `0073`); confirm review, merge, and database-backed CI before treating it as a release baseline.
- BSS idempotency closes duplicate submission/concurrent-claim cases, but a crash after internal dispatch succeeds and before success is recorded can still cause a retry. This is not an end-to-end exactly-once guarantee.

## Completion definition

A feature/API/profile is **complete for scope** only when all of the following are true:

1. Its exact normative document, API/data-model version, required/optional feature set, and exclusions are recorded.
2. Runtime routes/operations and DTOs match the accepted contract; unsupported operations fail explicitly and safely.
3. Automated contract, auth/tenant, persistence/recovery, and failure-path tests pass in CI against the release commit.
4. Real CPE qualification passes for every included model/firmware/protocol/network profile; simulators and reference agents are supplemental evidence only.
5. BSS/OSS partner flows pass in a representative partner test environment, or the integration is clearly classified as API-contract tested only.
6. Operations, security, integration, compatibility, and release documentation match the same commit and list known limitations.

No generic “TMF-complete,” “TR-complete,” “rock-solid,” or vendor-support claim should be made beyond that approved matrix.

## Workstream 0 — Freeze baseline and scope

**Owners:** Product/solution architect, ACS engineering lead, operations, security.
**Dependencies:** None.

### Actions

- Merge/review the pending CWMP identity, BSS ownership, TMF641 recovery, compose, credential, limiter, and CI changes, or choose a different clean release candidate. Capture commit and passing CI URLs.
- Select the BSS/OSS use cases that must ship: onboarding/assignment, Wi-Fi/service changes, unassign/swap, service activation, order lifecycle, alarms/events/problems, retries, and operational reporting.
- Confirm exact target devices/APNs and partner platforms. Link [P1–P5 evidence plan](../../tr069-acs-prerequisites.md); unresolved fleet/legal gates remain separate.
- Approve service-level objectives and error budgets for API latency/availability, order execution age, outbox backlog/dead-letter age, callback delivery, recovery time, and database restore. Product/operations must set numerical thresholds from service requirements; do not invent them in engineering docs.

### Deliverables / gate

- Signed scope and support matrix, with `in scope`, `excluded`, and `future` decisions.
- Clean, reproducible release baseline and passing required CI artifacts.
- Owners and dependency dates for vendor, carrier, legal, and partner work.

## Workstream 1 — Normative contract and version audit

**Owners:** TMF/API lead, protocol lead, QA/conformance lead.
**Dependencies:** Workstream 0 decisions.

### TMF

1. Reconcile each current route with the 2026-09-14 northbound programme proposal. For TMF638/642/656 choose whether to maintain v4 for compatibility, migrate to v5, or expose an explicitly versioned second path. Document consumer impact, migration plan, and deprecation policy.
2. For each selected API/version, obtain the official TM Forum OpenAPI, schema, conformance profile, error/paging/filter rules, lifecycle semantics, and any officially available CTK. Record artifact revision/hash and redistribution/licensing constraints.
3. Build a per-operation gap matrix: normative operation, ACS operation, mapping to domain behavior, request/response/error contract, auth/tenant rule, persistence/event behavior, test, status, and deliberate exclusion.
4. Decide any claim of formal TM Forum conformance/certification separately from internal contract compatibility. A passing route test or generated OpenAPI is not certification.

### Broadband Forum TR scope

1. Pin named protocol/data-model profiles from fleet needs, initially considering CWMP/TR-069 Amendment 6; TR-181 Device:2 plus only needed TR-098 compatibility; selected TR-143 diagnostics; USP/TR-369 with an explicitly selected message version and MTPs; and the exact applicable NAT/Connection Request behavior. Confirm against the current official documents before freezing.
2. Decide whether to support USP 1.3 only or a newer version; current generated protobuf source is `usp-msg-1-3`/`usp-record-1-3`. Define supported WebSocket/MQTT versions and security profiles individually.
3. Inventory related TRs/features (for example TR-106 data-model rules, TR-111 remote management, TR-157, TR-143, and TR-181 profiles) and classify each as required, optional, unsupported, or not applicable. Do not add features just because their TR number exists.
4. Pin the Broadband Forum data-model release used for schema/catalog validation and document how older CPE-declared model versions/vendor extensions are handled. Do not promise implementation of the entire Device:2 model unless that is an explicit product requirement.

### Deliverables / gate

- Versioned TMF API contract matrix and BBF protocol/data-model support matrix approved by product and engineering.
- Explicit changes required to code, deployment configuration, and migration strategy.
- No standard/API scope remains described only as “all” or “latest.”

## Workstream 2 — TMF API completeness for approved scope

**Owners:** TMF/API lead, domain owners, QA.
**Dependencies:** Workstream 1 contract decisions.

### Implementation sequence

1. Generate or author versioned OpenAPI contracts for all selected TMF routes: TMF638/639/640/641/642/656/688. Keep TMF639 separate if useful; ensure no API accidentally inherits the `/bss/v1` spec.
2. Compare each operation and field against the approved official spec. Implement only required in-scope operations; for deliberate exclusions, return documented, contract-compatible errors and state them in the capability matrix.
3. Complete shared rules: TMF error envelopes, identifiers/hrefs, correlation, paging/count headers, field selection, supported filter grammar, timestamp and enum semantics, auth scopes, tenant hiding, idempotency, ETag/version behavior where required.
4. Close lifecycle gaps: service/resource relationship semantics; TMF640/641 work-to-job mapping; order item restart/recovery; TMF642 alarm state transitions; TMF656 problem state/correlation; TMF688 hub subscription, signature, retries, ordering, and event fanout. Keep BSS commercial catalog/entitlements out of ACS.
5. Add database-backed tests per contract operation, including malformed schema, unsupported values, cross-tenant IDs, concurrent mutations, retries, restart, migration, and version-specific serialization.

### Deliverables / gate

- Versioned OpenAPI and conformance/gap reports for each selected API.
- Automated OAS lint, route-to-spec coverage, schema contract tests, CTK run/equivalent evidence, and full DB-backed regression pass.
- One documented consumer migration/compatibility plan for any API version changes.

## Workstream 3 — TR protocol/profile support

**Owners:** CWMP/USP leads, device integration, security, QA.
**Dependencies:** Approved protocol matrix and P1–P4 evidence.

### Implementation sequence

1. Create a testable capability matrix by protocol/profile: RPC/message, data-model object/profile, auth/security mode, transport, automated evidence, physical-device evidence, and known limits.
2. Fill required CWMP RPC/message-path gaps for the selected Amendment; harden parsing, faults, session behavior, transfer/recovery, and vendor-specific compatibility without weakening fleet-wide identity or security.
3. Implement the selected TR-181/TR-098 paths through discovered capabilities and writable-state checks. Expand full-tree/model validation only where needed by approved use cases; retain XMPP or other unsupported features as explicit exclusions unless selected.
4. Complete selected TR-143 diagnostics and result handling for real supported device profiles; test allowlists/SSRF protections and unsupported/faulted diagnostics.
5. Validate USP version/MTP/security scope against the selected TR-369 release. Keep reference-agent CI for controller interoperability and add physical-agent tests for the actual vendor fleet. Upgrade protocol/schema generation only after forward/backward interop and migration analysis.
6. For STUN/Annex G, prove device-side configuration and end-to-end Connection Request behavior per APN; server code tests alone do not close NAT readiness.

### Deliverables / gate

- Versioned capability matrix that explicitly marks every in-scope feature supported/partial/not supported/N/A.
- Automated conformance/interoperability regression for selected CWMP/USP exchanges.
- Sanitized fixtures and regression tests for every accepted vendor-specific wire behavior.
- No production support status until exact firmware has passed [`FIELD-CPE-VALIDATION.md`](FIELD-CPE-VALIDATION.md) and appears as qualified in [`COMPATIBILITY.md`](COMPATIBILITY.md).

## Workstream 4 — BSS/OSS reliability and partner qualification

**Owners:** BSS integration lead, API/domain engineers, SRE, partner integration owner.
**Dependencies:** Workstream 0 SLOs; Workstream 2 contracts; test partner access.

### Implementation sequence

1. Close the dispatch crash window: add an idempotency/correlation key to internal ACS job creation and make dispatch retry return/reconcile the original job. Test process termination immediately before/after external dispatch and DB commit. If any operation cannot be made idempotent, define at-least-once semantics and per-action duplicate safety explicitly.
2. Test outbox/reconciler behavior across duplicate submissions, simultaneous adapter replicas, network timeouts, PostgreSQL restart/failover, process kill at each transition, expired leases, dead-letter/replay, and operator recovery.
3. Add contract and end-to-end partner tests for OAuth token issuance/revocation, tenant/account/role isolation, mapping/unassign/swap, each supported action, TMF workflows, reconciliation, HMAC callbacks, retries, rate limits, and partial downstream failure. Include at least one representative BSS/OSS product sandbox before the “partner qualified” claim.
4. Exercise long-running CPE availability and completion: a `202` order can remain queued until device check-in; monitor age, terminal outcomes, retries and callbacks against agreed SLOs.
5. Complete load, soak, failover, restore, and security-negative campaigns at target scale. Retain run artifacts and publish service-specific limits and dashboards.

### Deliverables / gate

- Documented and tested delivery semantics (idempotent exactly-once where achievable; otherwise at-least-once plus dedupe).
- Partner contract-test pack and passing sandbox evidence for each supported partner integration.
- SLO/error-budget report, load/soak results, failure-injection report, backup/restore evidence, and on-call runbooks.

## Workstream 5 — Documentation and release controls

**Owners:** Each API/protocol owner, technical writer, release manager.
**Dependencies:** Every workstream; documentation ships with each implementation slice.

- Make one generated/reviewed source of truth for API versions and routes. Generate or validate OpenAPI against handlers and add a CI check for missing/stale contracts.
- Keep [`TMF-API-STATUS.md`](TMF-API-STATUS.md), [`COMPATIBILITY.md`](COMPATIBILITY.md), [`FIELD-CPE-VALIDATION.md`](FIELD-CPE-VALIDATION.md), BSS integration docs, security/deployment guides, migrations, and UAT assessment tied to a named commit/date.
- Mark design specifications as historical decisions with implementation status links; do not silently rewrite their historical assumptions as current behavior.
- Add a docs consistency checklist to release: route/version table; OAS validation; operation examples; error/retry semantics; security/tenant boundaries; known limitations; test/CTK/device/partner evidence links; supported matrix; changelog.
- Keep test artifacts, vendor approvals, legal dispositions, and real-device captures in access-controlled evidence storage; public docs contain sanitized references only.

## Suggested delivery order

1. **Scope freeze + release baseline:** merge/review current changes, decide use cases/SLOs, identify partner and exact fleet scope.
2. **Contract freeze:** reconcile TMF API versions and select the TR/protocol/data-model profiles; acquire official contract/test artifacts.
3. **TMF/API implementation and reliability:** close operation/schema gaps, add specs and contracts, fix exactly-once/at-least-once semantics.
4. **Protocol and device qualification:** validate required RPCs/data-model profiles/USP MTPs/TLS/STUN on physical CPE and APNs; resolve P1–P4.
5. **Partner/operational campaigns:** BSS/OSS sandbox, scale/soak, chaos, restore, security, SLO acceptance.
6. **Release/documentation gate:** P5 sign-off; passing CI/contract suites; updated matrices and runbooks; explicit limitations; approval for the bounded supported scope.

Vendor procurement, carrier access, partner sandboxes, CTK availability/licensing, and legal sign-off are external dependencies. Track them as blockers with an owner and due date; do not replace missing evidence with a code-only completion claim.

## Current scope decisions needed before implementation starts

| Decision | Current state | Required owner |
|---|---|---|
| Which TMF API versions to support (especially 638, 642, 656 v4 vs proposed v5) | Conflicting route and programme-design versions | Product/API owner |
| Whether formal TM Forum conformance/certification is required or internal contract compatibility is sufficient | Not recorded | Product/compliance |
| Exact TR/CWMP/USP standards and profiles needed for launch | Broad families named; bounded feature matrix incomplete | Product/protocol owner |
| Whether to implement any XMPP Connection Request or full TR-098 breadth | Current docs mark unsupported/partial | Product/network owner |
| USP protocol/message version target | Current generated bindings are USP 1.3; target policy not approved | Protocol owner |
| Numerical availability, latency, backlog, recovery, and scale SLOs | Not recorded | Product/SRE |
| Named BSS/OSS partner sandbox and supported release | No qualified external product listed | Integration owner |
| Legal treatment of source/dependencies/BBF notices and IPR | P5 open | Legal/compliance |

## Reference material

- [TM Forum API versions and official conformance material](https://www.tmforum.org/oda/open-apis/)
- [Broadband Forum CWMP data-model source and releases](https://github.com/BroadbandForum/cwmp-data-models)
- [Broadband Forum TR-181 Device Data Model](https://github.com/BroadbandForum/device-data-model)
- [Broadband Forum USP agent conformance plan TP-469](https://www.broadband-forum.org/pdfs/tp-469-1-2-0.pdf) — agent certification scope, not ACS controller certification.
