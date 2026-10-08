# Device compatibility lab

The existing mock-CPE session tests cover CWMP over real HTTP. `ci/usp` runs a pinned BBF obuspa agent over WebSocket, MQTT 5 and MQTT 3.1.1. The database CI gate now includes discovery, firmware evidence, readback recovery and BulkData persistence tests. Tests recreate the public schema, so run sequentially against a disposable database (`go test -p 1`).

Run `python -m unittest discover -s scripts/tests` for the operator-runner regression checks. Run `python scripts/qualify-device.py --help` for the API qualification runner. Set `ACS_API_TOKEN`, use an authenticated ACS origin, specify the device UUID and a scenario file, and export a JSON report. `scenario.json` performs portable identity reads only. Configure additional instance paths and known writable settings for your designated lab device. Writes and reboot require `--lab-actions`; the default does neither.

For each manufacturer/model/firmware combination, record these cases:

| Case | Expected evidence |
|---|---|
| Onboarding | Device-bound authentication and correct identity |
| Root/model discovery | Complete bounded snapshot; unsupported roots fail safely |
| USP instances | Concrete rows and unique model access evidence |
| Read/write | Returned readable values; verified readback; write-only secrets explicitly excluded |
| Diagnostics | Accepted request, delayed completion, error and timeout |
| Reconnect | Re-established transport and successful new read after interruption |
| Restart recovery | Durable readback/discovery phase; destructive jobs never blindly repeated |
| Firmware upgrade | Changed SoftwareVersion invalidates previous evidence and queues discovery |
| BulkData | Authorized/gzip report, deduplication, malformed report, expiry, renewal and revocation |

The runner collects scenario evidence. It does not certify an ACS or automatically qualify firmware. Hardware reboot/reconnect and firmware upgrade require supervised tests and recorded physical-device evidence. Store the report reference in the catalog's `qualificationEvidence` and the exact tested versions in `firmwares`; never use the simulator report as hardware qualification evidence.

`nokia-read-only.json` is prepared for the reported Nokia hardware and firmware. It verifies DeviceInfo identity, selects the discovered root and reads confirmed concrete RF paths. No Nokia model or vendor path is inferred from the firmware build. See `docs/NOKIA-ONBOARDING-HANDOFF.md` for offline preparation and deployment checks.

BBF baselines: TP-181 for CWMP interoperability, applicable TP-469 agent cases for USP, and the device's selected TR-181 revision for mappings. Network architecture documents such as TR-470 and TR-521 do not replace device parameter definitions.
