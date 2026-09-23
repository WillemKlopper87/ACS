# ACS P1–P5 Evidence Request Pack

**Purpose:** Reusable request and inventory templates for resolving the ACS fleet prerequisites. This is a planning artifact; no request has been sent and no vendor, carrier, or legal response is implied.

Use the exact SKU and firmware from the fleet inventory. Send vendor questions to the vendor's ACS/technical-support channel, carrier questions to the mobile-core/APN owner, and P5 to legal/compliance. Save replies and sanitized test artifacts in the organization's controlled evidence store; link identifiers here without committing credentials, private keys, subscriber data, or restricted vendor documents.

## Starting candidate list from the current compatibility catalog

These are catalog entries, not confirmed deployed inventory. Confirm which are actually deployed/planned and add exact hardware revision, firmware, carrier profile, and variant before outreach.

| Vendor | Current catalog candidate | Exact deployed SKU / revision | Firmware | Carrier / APN | Sample available? | Qualification state |
|---|---|---|---|---|---|---|
| Huawei | 5G CPE Pro; N5368X 5G Outdoor CPE | UNKNOWN | UNKNOWN | UNKNOWN | UNKNOWN | Mock/catalog only; real-device result not recorded |
| Nokia | FastMile 5G | UNKNOWN | UNKNOWN | UNKNOWN | UNKNOWN | Mock/catalog only; real-device result not recorded |
| Teltonika | RUTX50 | UNKNOWN | UNKNOWN | UNKNOWN | UNKNOWN | Mock/catalog only; real-device result not recorded |
| Zyxel | NR7303-EU01V1F | UNKNOWN | UNKNOWN | UNKNOWN | UNKNOWN | Field validation pending |

Source of candidate names/status: [`COMPATIBILITY.md`](COMPATIBILITY.md). The broader [`5G-CPE-PROFILES.md`](5G-CPE-PROFILES.md) contains additional potential vendors/models; add those only if the fleet owner confirms they are in scope. Do not carry the older Huawei H312/H122 or Zyxel NR5103 examples forward as deployed inventory without confirmation.

## Vendor questionnaire — P1, P2, P3

**Subject:** ACS interoperability evidence request — `<vendor> <exact model> / hardware `<revision>` / firmware `<version>`

Please answer for the exact operator/carrier firmware build identified above. If a capability varies by hardware or firmware, list each supported combination and the applicable operator profile.

1. Please provide the CWMP/TR-069 and/or USP/TR-369 conformance statement, supported protocol version/amendment, and any operator integration guide applicable to this firmware.
2. Which CWMP data model roots are implemented: TR-181 Device:2 (`Device.`), TR-098 IGD:1 (`InternetGatewayDevice.`), or both? Which USP data model version/profile applies? Are vendor-specific objects required for provisioning?
3. Can you provide a sanitized sample `GetParameterNames` result for each supported root and a `GetRPCMethods` result? Please identify writable paths and known 900x faults/limitations relevant to Wi-Fi, cellular WAN, diagnostics, reboot, and firmware transfer.
4. Which required CWMP RPCs are supported on this firmware: `Inform`, `GetParameterNames`, `GetParameterValues`, `SetParameterValues`, `GetRPCMethods`, `Reboot`, `Download`, and `TransferComplete`? Note optional or vendor-specific behavior, limits, and known interoperability settings.
5. What TLS versions and cipher suites can the CPE negotiate when connecting to the ACS? Does it validate the ACS certificate and hostname? Can it present a client certificate for mutual TLS?
6. If client certificates are supported, describe initial enrollment/provisioning, private-key generation/storage, trust-anchor updates, certificate renewal/rotation, revocation, failure recovery, and whether this is available in the exact carrier firmware without a field technician.
7. If client certificates are not supported, what per-device CWMP authentication methods are supported? Can the device use unique Digest credentials and safely accept ACS-side updates to `ManagementServer.Username` and `ManagementServer.Password`?
8. What STUN/Annex G behavior is supported? Please list exact parameter paths for STUN server address/port, credentials, keepalive interval, UDP Connection Request address, and NAT detection; identify default and operator-configurable values.
9. Can firmware/software transfer be performed with an operator-approved image and rollback procedure? What URL schemes, image formats, size limits, and completion/failure reports apply?
10. Please confirm the model and firmware identifiers and the date/version of the documents supplied. May these documents and sanitized captures be retained in a restricted engineering/compliance evidence store?

**Vendor response tracker:** Contact / ticket: `UNKNOWN`; request date: `UNKNOWN`; response date: `UNKNOWN`; attached document IDs: `UNKNOWN`; discrepancies needing follow-up: `UNKNOWN`.

## Carrier / APN questionnaire — P4

**Subject:** CPE management-plane reachability — `<APN>` / `<device models>`

For each APN and relevant network segment, please confirm:

1. Addressing assigned to CPEs (private IPv4, public IPv4, IPv6, static/dynamic) and whether CGNAT or another NAT layer is applied.
2. Whether inbound TCP to the CPE's Connection Request listener can be reached from the ACS; filtering, ACL, firewall, or port restrictions; any supported private interconnect/VPN route.
3. UDP behavior relevant to STUN/Annex G: filtering, mapping behavior, idle timeout, endpoint-dependent mapping/filtering where known, and whether bindings are preserved at the device's configured keepalive interval.
4. Approved STUN server address/port and any required authentication/configuration parameters. Confirm whether the carrier permits the device to send STUN traffic to that endpoint.
5. Whether a public/static-IP APN or managed VPN is available, its scope/cost/lead time, and security requirements.
6. Any CGNAT, APN, or firewall changes planned during the qualification window.

**Carrier response tracker:** Network owner / ticket: `UNKNOWN`; APN: `UNKNOWN`; response date: `UNKNOWN`; design document ID: `UNKNOWN`; lab SIM available: `UNKNOWN`.

## P5 legal/compliance intake

**Proposed review scope:** intended hosted/commercial ACS deployment and distribution; source and binary distribution; repository provenance; third-party and generated code; dependency/license notices; and patent/IPR considerations.

Please review these concrete inputs and advise if additional materials are required:

- Current release commit and repository history/provenance statement.
- `backend/go.mod` and `backend/go.sum`, frontend lockfile and applicable license files/notices.
- CI-generated SPDX SBOM artifacts for the backend and frontend images, including the artifact/run identifier and image digest for the intended release.
- `backend/internal/usp/proto/LICENSE.txt`, both vendored USP `.proto` schemas, and both generated `backend/internal/usp/uspproto/*.pb.go` bindings. The source/generated headers identify Broadband Forum and ARRIS contributions. The included license permits specified source/binary redistribution subject to conditions and expressly points to the Broadband Forum IPR Policy; request advice on the implications for the intended use.
- Any other copied, adapted, generated, or vendored source identified by repository/provenance review.

**Requested disposition:** approved for the stated use / approved subject to listed notices or changes / further information required / not approved. Please identify copyright notice obligations, attribution/endorsement restrictions, source/binary distribution obligations, patent/IPR review needed, unresolved provenance questions, approver, date, and the deployment/distribution scope covered. This questionnaire does not interpret the license or substitute for counsel's decision.

## Qualification record pointer

Use the per-device evidence and PASS/FAIL/BLOCKED/N/A classifications in [`FIELD-CPE-VALIDATION.md`](FIELD-CPE-VALIDATION.md). For Huawei use [`HUAWEI-CPE-QUALIFICATION-TEMPLATE.md`](HUAWEI-CPE-QUALIFICATION-TEMPLATE.md); the N5368X secure-bootstrap path has its own [`HUAWEI-N5368X-SECURE-QUALIFICATION.md`](HUAWEI-N5368X-SECURE-QUALIFICATION.md). Update [`COMPATIBILITY.md`](COMPATIBILITY.md) only after exact-firmware evidence meets its qualification rule.
