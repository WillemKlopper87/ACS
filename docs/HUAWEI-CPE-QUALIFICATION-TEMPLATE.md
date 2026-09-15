# Huawei CPE qualification evidence template

Copy this template into the physical-device qualification issue or retained
field notes. Do not commit subscriber secrets, full serial numbers, passwords,
Authorization headers, Wi-Fi PSKs or private management addresses where those
are sensitive.

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
- Compatibility overrides used (exact values and justification):

## First contact

- Remote-IP/identity capture ID:
- ACS URL reachable: PASS / FAIL
- `AuthenticationFailure` observed: yes / no
- Digest algorithm observed:
- Authenticated Inform: PASS / FAIL
- CWMP namespace:
- BOOTSTRAP/BOOT/PERIODIC behavior:
- Empty session close behavior:
- Notes:

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
- Command key:
- HTTP result:
- Event 6 Inform observed: PASS / FAIL
- Capture ID:

## Diagnostics and recovery

- IPPing: PASS / FAIL / N/A
- TraceRoute: PASS / FAIL / N/A
- Reboot + reconnect: PASS / FAIL / N/A
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
- Secrets redacted: PASS / FAIL
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
