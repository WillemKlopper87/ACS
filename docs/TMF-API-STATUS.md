# TMF API status

**Reviewed:** 2026-09-23 against repository baseline `beb915a` plus local uncommitted working-tree changes.

This page describes the implemented TMF API subset, not complete TM Forum Open API conformance. The running implementation is the source of truth; versioned design notes under `docs/superpowers/` preserve delivery decisions and may describe earlier implementation stages. Release claims must be checked against the merged commit and its test artifacts.

For the sequenced implementation and qualification plan, see [`TMF-TR-BSS-OSS-COMPLETION-PLAN.md`](TMF-TR-BSS-OSS-COMPLETION-PLAN.md).

## Available surfaces

| Surface | Base path | Current behavior |
|---|---|---|
| TMF638 Service Inventory | `/tmf-api/serviceInventoryManagement/v4/service` | Account-scoped service reads with filtering, paging, and field selection. |
| TMF639 Resource Inventory | `/tmf-api/resourceInventoryManagement/v5/resource` | Account-scoped managed-device projection; no duplicate device inventory is created. |
| TMF640 Service Activation | `/tmf-api/serviceActivationAndConfiguration/v4/` | `GET /service`, `GET /service/{id}`, `PATCH /service/{id}` and `GET /monitor/{id}` through the existing ACS job engine. PATCH needs `X-Idempotency-Key` (it becomes the order id and the Monitor id; a retry dispatches nothing more) and takes one shape: `state` active/inactive (ACTIVATE/SUSPEND) or `serviceCharacteristic` SSID/WiFiPassword (MODIFY_WIFI). It answers 202 with a Monitor (`InProgress`, `Completed`, `InError`). `Service.state` is `reserved` while a pre-provisioned CPE is onboarding, and such a service cannot be patched until it is active. `GET /monitor` (list) is not built. |
| TMF641 Service Ordering | `/tmf-api/serviceOrdering/v4/serviceOrder` | Idempotent order submit/read/list, add/delete/modify/no-change items, sequential halt/skip, cancellation, and same-role swaps. An `add` for a CPE the ACS has not seen yet pre-provisions it (item `AWAITING_DEVICE` until the onboarding reaches IN_SERVICE; optional `iccid`, `imsi`, `place`); see [ONBOARDING.md](ONBOARDING.md). Current uncommitted working tree adds recovery for abandoned items through migration `0073`; confirm merge and DB-backed CI before release. |
| TMF688 Event Management | `/tmf-api/eventManagement/v4/` | Durable event ingest/read/list plus event hub registration and signed delivery. ACS CWMP faults and qualifying USP events are recorded automatically. |
| TMF642 Alarm Management | `/tmf-api/alarmManagement/v4/alarm` | Durable alarm create/read/list, acknowledgement, clearing, deduplication, and recovery-driven clearing. |
| TMF656 Service Problem | `/tmf-api/serviceProblemManagement/v4/serviceProblem` | Tenant-scoped problem create/read/list/update with alarm, event, resource, impact, severity, and root-cause correlation. |

## Shared behavior

- The BSS adapter is the northbound boundary for authentication and tenant/account scoping.
- TMF641 and TMF640 converge on the existing durable ACS order, outbox, job, retry, and CWMP/USP execution paths.
- TMF688 events, TMF642 alarms, and TMF656 problems are durable PostgreSQL records.
- External IDs and source keys provide idempotency where the surface accepts them.
- Collections support the implemented account/state filters, `offset`/`limit`, result-count headers, and `fields` projection where applicable.
- Existing `/bss/v1/*` integrations remain supported.
- Commercial catalog, billing, entitlement, and contract data remain owned by the BSS.

## Authentication and deployment

System-to-system callers use the BSS adapter's OAuth2 client-credentials flow
or the configured legacy shared token. Configure the adapter and database
using the variables documented in the deployment guide. Do not expose the
adapter or callback endpoints without TLS, authentication, and the required
network policy.

## Current boundaries

TMF639 is currently exposed through the existing managed-resource projection
rather than a separate CRUD resource store. TMF641 order notifications use
the shared event/webhook infrastructure. Prometheus and Alertmanager remain
the infrastructure alerting path; TMF642 is the durable domain-alarm path.

## Conformance and reliability limits

- These are implemented northbound surfaces and selected operations, not a claim that every required/optional operation, schema rule, extension, or lifecycle in the corresponding TM Forum API is implemented.
- Official TM Forum contract/CTK validation for each selected API version remains outstanding; broader lifecycle notification coverage, richer service/resource relationships, persistence, and TMF metrics remain roadmap items in [`integration/04-roadmap.md`](integration/04-roadmap.md).
- Only TMF639 currently has a separate OpenAPI document (`backend/openapi-tmf639.yaml`). The BSS adapter OpenAPI covers `/bss/v1`; it is not a complete TMF contract. Handler-route tests do not establish full schema/conformance coverage.
- BSS outbox/idempotency prevents duplicate logical submissions and concurrent claims. It does not currently guarantee end-to-end exactly-once side effects across a process crash after dispatch succeeds but before dispatch success is committed; see [`integration/APPENDIX-internal.md`](integration/APPENDIX-internal.md) for the internal caveat and `integration/02-integration-model.md` for the integrator-safe limitations.
- BSS/OSS integration tests use database-backed ACS clients and mock CPE/emulator flows. They do not prove interoperability with a production BSS/OSS product or qualify a physical CPE fleet.
