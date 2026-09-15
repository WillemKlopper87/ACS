# TMF API status

This page describes the TMF surfaces currently present on `main`. The
running implementation is the source of truth; the versioned design notes
under `docs/superpowers/` preserve the decisions made during delivery.

## Available surfaces

| Surface | Base path | Current behavior |
|---|---|---|
| TMF638 Service Inventory | `/tmf-api/serviceInventoryManagement/v4/service` | Account-scoped service reads with filtering, paging, and field selection. |
| TMF639 Resource Inventory | `/tmf-api/resourceInventoryManagement/v5/resource` | Account-scoped managed-device projection; no duplicate device inventory is created. |
| TMF640 Service Activation | `/tmf-api/serviceActivationAndConfiguration/v4/` | Service reads, configuration actions, and monitor state through the existing ACS job engine. |
| TMF641 Service Ordering | `/tmf-api/serviceOrdering/v4/serviceOrder` | Idempotent order submit/read/list, add/delete/modify/no-change items, sequential halt/skip, cancellation, and same-role swaps. |
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
