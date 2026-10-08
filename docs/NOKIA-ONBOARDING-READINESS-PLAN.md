# Nokia onboarding software plan

Prepare the existing ACS for the supplied Nokia hardware `3TG02241AAAA`, software version `1.2501.00.0798`, software build `5GGW2-OMNI-2_R250100BiwT0301E0798`, and local address `192.168.1.1`. Model, serial, OUI, protocol revision and vendor parameters remain unconfirmed.

1. Add a read-only migration check that reports pending, unverified and mismatched embedded migrations without changing the database.
2. Add an offline onboarding preparation command that validates an ACS URL, transport/credential prerequisites and the reported device identity. Export a redacted handoff checklist and read-only qualification scenario. Unknown deployment settings must remain pending.
3. Extend the qualification runner to read identity before any write/reboot, reject a different hardware/firmware, choose Device or InternetGatewayDevice from discovery, and select optional reads only from confirmed readable leaf paths.
4. Capture a discovered Nokia capability inventory for later profile authoring. Keep vendor mappings unqualified until exact model/firmware evidence is available.
5. Add regression coverage and run software validation, including a Linux container race check where available. Reuse the existing secure deployment preflight and reference-agent CI gates.

This stage prepares software and operators; it does not configure the CPE, migrate a live deployment, install credentials, alter firewall rules or qualify physical hardware. Mesh steering, a fleet SNMP scheduler and an independent LMAP scheduler are broader features, not prerequisites for Nokia CWMP onboarding. They remain separate work rather than being presented as delivered here.

## Completed software delivery — 8 October 2026

All five preparation items are delivered. Use `docs/NOKIA-ONBOARDING-HANDOFF.md`, `scripts/prepare-nokia-onboarding.py`, `ci/compatibility/nokia-read-only.json`, and `go run ./cmd/migrate -check`. The migration check is also included in CI.

Validation: 13 Python tests, frontend build/lint and 62 tests, full Go vet, and race-enabled tests for every backend package against disposable PostgreSQL 18 in Linux containers. The store suite was rerun with dashboard fixtures mounted after the initial container omitted them; it passed. The migration tests prove the checker does not create tracking tables, apply migrations or backfill checksums and detects modified/unknown migrations. No physical Nokia connection or live deployment change was performed. Reference-agent interoperability CI remains a required remote release gate.

The repository's default branch is `main`. Pull `main` for the new cloud instance, review the deployment handoff, and complete schema/security/service checks before connecting the device. Local readiness/qualification reports are ignored by Git.
