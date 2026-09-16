# Huawei CPE qualification evidence template

Copy this template into the physical-device qualification issue or retained
field notes. Do not commit subscriber secrets, full serial numbers, passwords,
Authorization headers, Wi-Fi PSKs or private management addresses where those
are sensitive.

For the N5368X security qualification, follow
`HUAWEI-N5368X-SECURE-QUALIFICATION.md` before continuing with the generic
parameter, Connection Request, diagnostics and northbound checks below.

## Device and build

- ACS commit/tag:
- Field preflight: PASS / FAIL
- Test date/time UTC:
- Operator:
- Huawei model / ProductClass:
- Hardware revision:
- Firmware/software version:
- OUI:
- Serial (masked/hash):
- Data-model root:
- Network topology: direct / NAT / APN / VPN / lab LAN
- Protocol: CWMP / USP / both
- ACS deployment profile: lab / production
- Dedicated CWMP bootstrap configured: yes / no
- Credential encryption configured: yes / no (never record the key)
- Compatibility overrides used (exact non-secret values and justification):

## First contact

- Remote-IP/identity capture ID:
- ACS URL reachable: PASS / FAIL
- `AuthenticationFailure` observed: yes / no
- Digest algorithm observed:
- Bootstrap username identifier (username only; no password):
- Authenticated bootstrap Inform: PASS / FAIL / N/A
- CWMP namespace:
- BOOTSTRAP/BOOT/PERIODIC behavior:
- Empty session close behavior:
- Notes:

## Secure bootstrap and credential graduation

- Established-device bootstrap rejection checked: PASS / FAIL / N/A
- Normal jobs/policies/firmware/diagnostics withheld during bootstrap: PASS / FAIL
- Pending CWMP credential ID/version (non-secret metadata only):
- Initial credential state: PENDING / other:
- Credential-installation command key:
- Username parameter path:
- Password parameter path:
- Credential-install SPV result/fault:
- Generated password absent from retained evidence: PASS / FAIL
- Unique-credential reconnect observed: PASS / FAIL / BLOCKED
- Bound OUI/ProductClass/Serial matched on reconnect: PASS / FAIL / BLOCKED
- Credential state after bound reconnect: ACTIVE / PENDING / other:
- `PENDING -> ACTIVE` occurred only after bound Inform: PASS / FAIL / BLOCKED
- Wrong-device/identity-substitution rejection evidence: PASS / FAIL / automated-only / N/A
- Post-graduation bootstrap rejected: PASS / FAIL / BLOCKED
- Manual credential-installation step required by firmware: yes / no
- Graduation capture/evidence IDs:
- Notes / BLOCKED reason:

## Parameter model

- Discovery command key:
- Discovery result: PASS / FAIL
- Standard root confirmed:
- Vendor extension paths of interest:
- Writable/non-writable surprises:
- 900x faults observed:
- Sanitized fixture added (if new protocol shape):

## Reversible read/write

- Parameter:
- Original value (sanitize if sensitive):
- GPV command key/result:
- SPV command key/result:
- Read-back command key/result:
- Original restored: PASS / FAIL

## Connection Request

- Connection Request URL supplied: yes / no
- Mode: direct / STUN / Annex G / routed-VPN
- Digest challenge algorithm/qop:
- Device-specific Connection Request credential used: PASS / FAIL / N/A
- Command key:
- HTTP result:
- Event 6 Inform observed: PASS / FAIL
- Capture ID:

## Diagnostics and recovery

- IPPing: PASS / FAIL / N/A
- TraceRoute: PASS / FAIL / N/A
- Reboot + unique-credential reconnect: PASS / FAIL / N/A
- ACS/network interruption recovery: PASS / FAIL / N/A
- Upload/Download/TransferComplete: PASS / FAIL / N/A
- Firmware upgrade: PASS / FAIL / N/A

## Northbound projection

- TMF639 resource: PASS / FAIL / N/A
- TMF638 service mapping: PASS / FAIL / N/A
- TMF640/641 activation/order trace: PASS / FAIL / N/A
- TMF688/642/656 assurance trace: PASS / FAIL / N/A
- Correlation/external IDs:

## Capture/redaction

- Capture export reviewed: PASS / FAIL
- Authorization values redacted: PASS / FAIL
- Generated CWMP password absent: PASS / FAIL
- Other secrets redacted: PASS / FAIL
- Evidence retained at / linked issue:

## Vendor-profile output

- Canonical concept → observed Huawei path mappings:
- Mapping source/confidence:
- Compatibility knob(s) required:
- Reproduced on second clean session/unit: yes / no
- Regression fixture/test added:

## Qualification decision

- Result: PASS / FAIL / BLOCKED
- Blocking defects:
- Known limitations / N/A capabilities:
- `COMPATIBILITY.md` updated: yes / no
- Approved by / date:
