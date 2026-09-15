# Roadmap

The current implementation status is maintained in [TMF API status](../TMF-API-STATUS.md). This roadmap describes the next expansion work; it no longer lists the delivered TMF surfaces as pending.

## Available now

The ACS exposes the existing `/bss/v1` integration contract alongside the TMF northbound APIs. TMF638/639 provide operational inventory, TMF640 provides activation/configuration, TMF641 provides service ordering, TMF688 and TMF642 provide durable events and alarms, and TMF656 provides service-problem management.

See [TMF API status](../TMF-API-STATUS.md) for routes and behavior, and the machine-readable adapter contract in [`backend/openapi-bssadapter.yaml`](../../backend/openapi-bssadapter.yaml) where applicable.

## Next priorities

- Add official TM Forum contract and CTK validation for each selected API version.
- Expand notification delivery so order, service, alarm, and problem lifecycle changes are published consistently through the shared hub/webhook abstraction.
- Add richer service/resource relationship projections and operational service persistence where ACS can truthfully own the state.
- Add low-cardinality TMF request, outcome, delivery-backlog, and lifecycle metrics.
- Evaluate TMF628 performance management after the service/resource/event/assurance graph is stable.

The BSS-to-assurance correlation path is covered by the DB-backed
`TestBSSOrderToTMFAssuranceFlow` integration test. It verifies order dispatch
through the ACS client, fault publication, durable TMF event and alarm
creation, and service-problem correlation. The test uses the same emulator/
mock-CPE qualification harness described in [COMPATIBILITY](../COMPATIBILITY.md);
physical-device qualification remains deferred.

## Boundaries

The BSS remains the system of record for catalog, billing, contracts, entitlements, and commercial subscription truth. ACS stores operational state only. Wi-Fi passwords remain write-only, and TMF event responses do not expose raw protocol payloads or secrets.

Existing `/bss/v1` behavior remains supported while the TMF surfaces mature.

