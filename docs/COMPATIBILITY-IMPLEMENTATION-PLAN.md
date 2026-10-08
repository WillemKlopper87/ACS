# Compatibility implementation plan

This extends the standards/discovery implementation already in this checkout. Each item delivers code, regression coverage and operator documentation. Hardware qualification is recorded only after tests on the exact firmware; simulators do not certify vendor devices.

1. **Interoperability lab:** add a repeatable simulator qualification runner and scenario matrix; exercise onboarding/discovery/read/write/reboot/reconnect through existing ACS APIs, export evidence and provide a disposable PostgreSQL integration workflow.
2. **USP instances:** implement a durable GetInstances job, scoped queue/read APIs, firmware-bound instance evidence, and concrete-instance checks for provisioning.
3. **Connection diagnostics:** classify observed authentication, reachability and connection-request failures; expose stale contact, transport and remediation evidence without arbitrary network probing.
4. **Firmware profiles:** persist firmware-scoped read/write verification results, expose capability changes and qualification evidence, and display separate compatibility stages.
5. **Provisioning:** expand standard range/enum validation; add durable readback verification with mismatch, write-only and partial-result handling. Never automatically roll back credentials or connectivity settings.
6. **5G mapping:** expose discovered registration/session/slice/QoS/SA-NSA candidates with source, units, freshness and unavailable markers; preserve raw values and avoid guessing vendor meanings.
7. **Resilience:** test duplicate/late responses, persisted discovery progress, restart recovery and destructive-operation retry protection; fix defects found.
8. **Telemetry lifecycle:** scoped receiver renewal, health/missing-report evaluation and freshness metadata, reusing existing alert surfaces where possible.

Validation: targeted tests during changes, full backend tests and vet, frontend build/lint/tests, OpenAPI generation/drift validation, Python runner tests. Run database suites only against an explicitly disposable database because existing test harnesses recreate the public schema. Physical-device cases remain pending until hardware and credentials are available.

## Delivery status

All eight software items are implemented in this checkout. The operator reference is `docs/DEVICE-STANDARDS-AND-DISCOVERY.md`; repeatable lab scenarios and remaining hardware cases are documented in `ci/compatibility/README.md`.

Validated with a disposable PostgreSQL 18 database: all backend packages were exercised, the corrected USP response fixture was rechecked with the entire USP controller suite, and BulkData renewal plus missing-report incident recovery passed. Go vet passed. Frontend build/lint and 62 tests passed; seven Python runner tests passed; OpenAPI validation and route drift checks passed. The local Go race detector was unavailable; CI retains the race gate.

Deployment requires migrations 0074–0077. No running ACS deployment or physical CPE fleet was changed. Firmware qualification, real reboot/reconnect, firmware upgrades and vendor interoperability remain lab acceptance tasks. BBF.0281 is not treated as a verified dictionary. The implementation uses the bundled official TR-181 revisions; renewal is operator-triggered and OID fallback remains a qualified read-only standalone probe.

Follow-up on 8 October 2026: Linux container race checks passed for every backend package against disposable PostgreSQL 18, superseding the earlier local race-detector limitation. Nokia onboarding preparation adds read-only migration status, offline readiness export, firmware identity guards and a read-only discovered-RF scenario; see `docs/NOKIA-ONBOARDING-READINESS-PLAN.md`.
