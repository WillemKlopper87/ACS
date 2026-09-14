# TMF / ODA Northbound Integration Programme — Master Design

**Date:** 2026-09-14  
**Status:** Programme design baseline  
**Tracking:** GitHub issue #21  
**Scope:** TMF639, TMF638, TMF640, TMF641, TMF688, TMF642, TMF656; TMF628/performance follows after assurance foundations

## 1. Purpose

Turn ACS from a protocol-centric device manager into a standards-based operational OSS integration platform without duplicating the business systems that already own commercial product/subscription truth.

The programme adds TM Forum northbound APIs over the existing ACS domain. The APIs are adapters over one set of internal state machines and repositories; they are not parallel implementations of ordering, activation, device management, event handling or alarm processing.

The target end-to-end path is:

```
TMF641 Service Order
        |
        v
TMF640 activation/configuration
        |
        v
existing ACS job/action engine
        |
   +----+----+
   |         |
 CWMP       USP
   |         |
   +----+----+
        |
        v
TMF639 resource state + TMF638 service state
        |
        v
TMF688 event -> TMF642 alarm -> TMF656 service problem
```

## 2. Non-negotiable ownership boundary

The existing fleet design correctly states that ACS must not become the source of truth for the subscriber's commercial subscription. That remains true.

The programme therefore distinguishes two concepts:

- **Commercial service/product intent** — owned by BSS/CRM/catalog/order systems outside ACS.
- **Operational service instance/projection** — owned by ACS only to describe what ACS is managing, which resources realize it, its operational state, and the execution/assurance context needed by the northbound APIs.

TMF638 exposes the second concept. It must never silently become a shadow billing/product inventory.

## 3. Existing foundations to reuse

The repository already contains the core capabilities the TMF adapters need:

- tenant/customer scoping and RBAC;
- temporal subscriber-to-device assignment with device roles;
- device registry and protocol-neutral device UUIDs;
- CWMP/TR-069 execution;
- USP/TR-369 execution over WebSocket/MQTT;
- parameter discovery/cache/history;
- job queue, command-key correlation, lease/reaper and dead-letter semantics;
- templates/policies/rollouts;
- durable BSS order outbox, idempotency and retry/dead-letter behavior;
- OAuth2 client credentials on the BSS adapter;
- signed webhook delivery;
- device events and USP Notify processing;
- Prometheus/Alertmanager and Grafana observability.

No TMF surface may bypass these foundations and write directly to protocol-specific execution code.

## 4. API/version strategy

TMF versions are pinned per surface, not globally.

Initial implementation targets are:

| API | Initial target | Rationale |
|---|---|---|
| TMF638 Service Inventory | v5.0 contract baseline | v5.0 is the current stable line; v5.1 remains a preview line. |
| TMF639 Resource Inventory | v5.0 candidate | v5.0 assets have a 2026 stable release; verify current CTK/metadata before freezing the contract because TM Forum directory metadata has shown mixed stable-version labels. |
| TMF640 Service Activation | v4.0 stable | v5.0 is preview; use stable contract first and isolate version-specific HTTP DTOs. |
| TMF641 Service Ordering | v4.2 stable | v4.2 became stable in August 2026; v5.0 is preview. |
| TMF688 Event Management | v4.0 | Current published Event Management surface. |
| TMF642 Alarm Management | v5.0 stable | Current stable alarm contract. |
| TMF656 Service Problem | v5.0 stable | v5.1 is preview; start from stable v5.0. |

Before each implementation milestone is merged, re-check the selected TM Forum OAS, conformance profile and CTK. Version-specific DTOs live at the HTTP boundary. Internal domain packages must not import generated TMF version packages.

## 5. Package and service architecture

The common implementation shape is:

```
backend/internal/tmf/
  common/           # href/id/external refs, errors, paging/filter helpers, correlation
  resource/         # protocol-neutral resource projection
  service/          # operational service projection
  activation/       # maps TMF640 intent to existing action/job engine
  ordering/         # maps TMF641 DTOs to existing durable BSS order core
  events/           # canonical durable event domain
  alarms/           # alarm lifecycle/domain
  problems/         # service-problem correlation/lifecycle

backend/cmd/bssadapter/
  tmf639_*.go
  tmf638_*.go
  tmf640_*.go
  tmf641_*.go
  tmf688_*.go
  tmf642_*.go
  tmf656_*.go
```

The external TMF APIs belong on the BSS/OSS integration boundary (`cmd/bssadapter`), not the internal operator API and not the CWMP/USP gateways.

A later extraction into a dedicated northbound service is allowed if scale or deployment isolation requires it, but the first implementation keeps the existing adapter boundary so auth, integration credentials, webhook delivery, deployment and operator expectations remain coherent.

### Dependency rule

`internal/tmf/*` may depend on stable ACS domain interfaces but must not depend on `cmd/acs`, `cmd/uspc` or HTTP handlers. Protocol-specific packages must never import TMF packages.

## 6. Common TMF foundation

Build once before individual surfaces.

### 6.1 Identity and references

Every exposed entity has:

- stable ACS-owned `id`;
- deterministic `href` generated by the northbound adapter;
- optional `externalId`/external references for BSS/OSS correlation;
- `@type`, `@baseType`, `@schemaLocation` handling at the contract boundary where required;
- correlation/request ID propagated into audit and downstream ACS jobs.

Do not use a device's OUI/serial, USP endpoint ID or CWMP session ID as the TMF resource primary ID. Device UUID remains the resource identity anchor.

### 6.2 HTTP conventions

Implement common handling for:

- `offset`/`limit` pagination and result-count headers required by the selected contract;
- `fields` projection;
- supported filter subset with explicit rejection of unsupported expressions rather than silent broad matches;
- RFC3339 timestamps;
- standard TMF error envelope;
- ETag/version semantics only where the chosen API requires/benefits from them;
- idempotency/correlation headers for mutating calls;
- listener/hub lifecycle and signed callback delivery through the existing webhook infrastructure where compatible.

### 6.3 Security

- OAuth2 client credentials remain the system-to-system authentication mechanism.
- Introduce explicit scopes per surface/action (for example `tmf639.read`, `tmf640.execute`, `tmf642.ack`).
- Every repository access remains tenant-scoped.
- Cross-tenant identifiers return not-found semantics rather than leaking existence.
- Write operations produce audit events containing actor/client, TMF surface, TMF resource/order ID, correlation ID, downstream job/order key and target resource/service.

## 7. TMF639 — Resource Inventory

TMF639 is implemented before TMF638 because the managed CPE is fundamentally an operational resource and services need stable resource relationships.

### Projection

A managed ACS device projects to a Resource with:

- `id` = ACS device UUID;
- name/label and description;
- category/role (`gateway`, `ont`, `extender`, `stb`, `ata`, `other`) from active assignment context where applicable;
- administrative/operational state derived from ACS liveness/management state;
- manufacturer, OUI, product class, serial, software/firmware version;
- management protocols (`CWMP`, `USP`, or both);
- site/location reference without exposing secrets;
- resource relationships for topology/assignment links;
- characteristics sourced from a deliberately allow-listed set of canonical device facts, not a dump of the complete parameter cache.

### Rules

TMF639 is a projection over `devices` plus related operational tables. It must not create a second device inventory table merely to satisfy the API shape.

Create/update/delete semantics from the TMF contract are supported only where they map to truthful ACS behavior. A northbound DELETE must not physically erase an auditable device merely because the TMF schema has a delete operation; use supported lifecycle semantics or return an explicit unsupported/conflict response according to the selected contract.

## 8. TMF638 — Service Inventory

TMF638 exposes **operational service instances**.

Examples:

- broadband access;
- managed Wi-Fi;
- voice/ATA service;
- IPTV/STB service.

A service has a stable service-instance ID, subscriber/account external reference, tenant, service type, operational state and one or more relationships to TMF639 resources.

### Persistence

Introduce an ACS-owned operational service table only for state ACS can truthfully maintain:

- service instance ID;
- tenant/customer;
- external account/service references;
- service type;
- lifecycle/operational state;
- characteristics required for activation/assurance but not secrets;
- timestamps/version;
- relationship to active device assignment(s).

Do not model catalog pricing, contracts, invoices or product entitlement.

### Relationship to the existing fleet model

The subscriber-to-device assignment remains the authoritative answer to "which device currently serves this account in role X." A service-resource relationship may reference that assignment but does not replace it.

## 9. TMF640 — Service Activation & Configuration

TMF640 is an adapter over existing ACS execution.

```
TMF640 request
  -> validate service/resource and tenant
  -> translate characteristics/action
  -> create/use existing ACS job(s)
  -> CWMP or USP dispatcher
  -> completion/fault
  -> TMF monitor / service state update / event
```

The adapter must reuse:

- action registry;
- parameter/data-model adapters;
- templates;
- jobs and command keys;
- CWMP/USP dispatch;
- existing retry and terminal failure semantics.

It must not issue SOAP RPCs or USP protobuf messages directly.

Async monitor IDs must correlate durably to the underlying ACS job/order keys.

## 10. TMF641 — Service Ordering

TMF641 is a second HTTP representation of the **existing durable BSS order core**, not a second ordering engine.

Both paths converge:

```
/bss/v1/orders ----+
                    +--> canonical order/application service --> activation/jobs
TMF641 ------------+
```

Required properties:

- reuse external-order idempotency;
- preserve write-ahead/outbox behavior;
- preserve retry/dead-letter semantics;
- map service-order items to operational service intent and then TMF640/activation operations;
- preserve role-aware resource targeting;
- expose state transitions and notifications using the TMF641 contract;
- retain `/bss/v1/*` unchanged for existing integrators.

Never create a second `service_orders` state machine whose status can diverge from `bss_orders`; if additional TMF fields need persistence, attach a projection/extension keyed to the canonical order identity.

## 11. Canonical event backbone and TMF688

Before TMF688, normalize internal events into a durable protocol-neutral domain event.

Sources include:

- CWMP Inform event codes;
- USP Notify variants;
- device liveness/reachability transitions;
- job state/completion/fault;
- firmware/transfer lifecycle;
- diagnostics;
- policy/compliance changes;
- operational service/resource transitions;
- selected monitoring/health signals.

Canonical event minimum fields:

- event ID and event type;
- occurred/observed timestamps;
- tenant;
- source resource ID;
- affected service IDs where known;
- severity/category;
- correlation/causation IDs;
- source protocol and source record reference;
- sanitized payload/attributes;
- deduplication key.

TMF688 exposes this domain. It does not expose arbitrary application log lines.

## 12. TMF642 — Alarm Management

An alarm is durable lifecycle state, not a Prometheus alert notification.

Minimum domain model:

- alarm ID;
- source resource and affected service relationships;
- alarm type / probable cause;
- perceived severity;
- first raised, last changed and cleared timestamps;
- acknowledgement state, actor and time;
- state (`raised`, `updated`, `cleared` or selected TMF-equivalent mapping);
- related canonical event IDs;
- correlation/dedup key.

Prometheus/Alertmanager, CWMP/USP/device events and job failures may raise/update/clear alarms through an alarm evaluator. They are sources, not the source of truth.

Alarm acknowledge/clear operations must be audited and tenant-scoped. Manual clear must not erase the history of the underlying condition.

## 13. TMF656 — Service Problem Management

TMF656 follows TMF638 + TMF688 + TMF642 because it needs their stable identities and relationships.

A service problem groups/correlates one or more alarms/events into customer-service impact. It records:

- affected operational services;
- affected resources;
- originating and related alarms/events;
- impact/severity/priority;
- status lifecycle;
- root-cause candidate/reference;
- resolution summary and timestamps;
- external ticket/problem references.

Initial correlation may be deterministic/rule-based. Machine-learning correlation is explicitly not required for the first conformant implementation.

## 14. Performance management follow-up

TMF628 or the selected contemporary performance-management surface comes after the service/resource/event/assurance graph is stable.

Prometheus remains the metrics engine. The TMF performance domain models measurement jobs/collections/results and references stable services/resources; it must not simply proxy arbitrary PromQL.

## 15. Delivery sequence and gates

### Phase 0 — baseline

- fix current OpenAPI validation failure;
- update vulnerable Gorilla WebSocket dependency;
- obtain a fully green CI run, including generated API drift, browser E2E, npm audit and USP interop.

### Phase 1 — architecture consolidation

- reconcile this master design with the existing local TMF638/TMF expansion designs;
- retain useful detailed decisions from those specs;
- resolve conflicts explicitly rather than silently replacing them.

### Phase 2 — common foundation

- common types/helpers;
- OAuth scopes;
- TMF error/paging/filtering conventions;
- correlation/audit;
- hub/notification abstraction and contract-test harness.

### Phase 3 — TMF639

Resource projection and conformance tests.

### Phase 4 — TMF638

Operational service persistence/projection and resource relationships.

### Phase 5 — TMF640

Activation/configuration adapter to the existing job engine.

### Phase 6 — TMF641

Service-order adapter to the existing BSS order core.

### Phase 7 — events/TMF688

Canonical event store and northbound event management.

### Phase 8 — alarms/TMF642

Durable alarm lifecycle and event/alarm correlation.

### Phase 9 — TMF656

Service-impact problem lifecycle and correlation.

### Phase 10 — performance

TMF performance-management expansion.

## 16. Testing and conformance

Every surface requires:

1. pure unit tests for mapping/domain logic;
2. DB-backed repository/tenant-isolation tests;
3. handler tests for filtering, pagination, errors and auth scopes;
4. OpenAPI lint and generated-contract drift gate;
5. selected official TM Forum CTK/conformance profile in CI where licensing/assets permit automated use;
6. backward-compatibility tests for existing `/bss/v1/*` behavior;
7. trace test proving northbound correlation ID -> canonical order/activation -> ACS job -> CWMP/USP result -> northbound state/event.

No API is called implemented merely because the endpoint serializes the expected JSON shape.

## 17. Observability

Add low-cardinality metrics by TMF surface and operation:

- request count/latency/status;
- rejected auth/scope/tenant accesses;
- order/activation terminal outcomes;
- event ingest/dedup/drop counts;
- alarm counts by lifecycle/severity;
- notification delivery backlog/failures;
- service-problem counts by state/severity.

Correlation IDs must be present in structured logs and audit events, but not Prometheus labels.

## 18. Migration and compatibility policy

- Forward-only DB migrations.
- Existing custom BSS APIs remain supported.
- No destructive rewrite of `devices`, assignments, jobs or BSS orders solely to mirror TMF schemas.
- TMF-specific persistence stores only data that is genuinely additional domain state, not duplicate copies of existing authoritative rows.
- Existing CWMP/USP behavior must remain unchanged when the TMF adapter is unused.

## 19. Definition of done for the programme

The programme is complete when a test environment demonstrates:

1. an authenticated TMF641 Service Order is accepted idempotently;
2. its item resolves to the correct TMF638 operational service and TMF639 resource(s);
3. TMF640 activation creates canonical ACS work without bypassing the job engine;
4. that work executes over CWMP or USP according to device capability;
5. completion/fault updates order/activation/service state with end-to-end correlation;
6. a canonical TMF688 event is emitted;
7. a qualifying condition creates/updates/clears a TMF642 alarm;
8. service-impacting alarms/events correlate into a TMF656 service problem;
9. all reads/writes are tenant-isolated and scope-authorized;
10. official contract/conformance checks and repository CI are green.

## 20. Explicit non-goals

- ACS does not become product catalog, billing or commercial subscription authority.
- No duplicate service-order or activation state machines.
- No raw CWMP/USP payloads in TMF responses.
- No secret parameter values in service/resource characteristics or events.
- No generic log-export API masquerading as TMF688.
- No Prometheus-alert passthrough masquerading as TMF642.
- No ML dependency for initial service-problem correlation.
