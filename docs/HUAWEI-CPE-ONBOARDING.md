# Huawei CPE onboarding and troubleshooting

This runbook is the Huawei-specific companion to
[`COMPATIBILITY.md`](COMPATIBILITY.md) and
[`FIELD-CPE-VALIDATION.md`](FIELD-CPE-VALIDATION.md). It is deliberately
firmware-oriented: **do not create one fleet-wide "Huawei mode"**. A workaround
that is necessary for one model or firmware must be recorded against that
exact device profile and kept as narrow as possible.

The first target is the **Huawei N5368X / 5G CPE family**, followed by EchoLife
TR-098 ONTs. The goal is to make every failure answerable as one of:

1. the CPE did not reach ACS;
2. it reached ACS but did not authenticate;
3. authentication succeeded but CWMP session startup failed;
4. CWMP works but the assumed data-model path/value is wrong;
5. the ACS RPC works but Connection Request/NAT reachability is wrong; or
6. the exact firmware has a vendor-specific behavior that needs a recorded
   compatibility mapping or parser/auth regression fixture.

## Safety rules

- Run `./scripts/field-preflight.sh` before a physical-device qualification.
- Start a bounded capture **before** reproducing a problem.
- Keep Digest authentication enabled whenever the device supports it.
- Do not enable Basic authentication or lower TLS fleet-wide to make one CPE
  connect. Use an isolated test profile and capture evidence first.
- Do not mark a model supported from mock/reference-agent evidence. The exact
  physical firmware must be recorded in `COMPATIBILITY.md`.
- Never retain raw passwords, Wi-Fi keys, PPP credentials, tokens or private
  subscriber data in a committed capture/fixture.

## Phase 1 — Prove first contact before changing settings

### 1. Start a remote-IP capture

For a CPE that has never authenticated, identity matching is unavailable.
Open **Captures → Huawei N5368X / 5G CPE** (or Huawei EchoLife / TR-098) and
start a **CWMP remote-IP capture** for the device's source IP.

The capture diagnosis is intentionally staged:

| Evidence | Meaning | Next action |
|---|---|---|
| No events | No matching CWMP evidence reached ACS | Verify ACS URL, route, DNS, firewall/NAT and TLS before touching credentials. |
| `AuthenticationFailure` | The request reached ACS but failed authentication | Inspect Digest challenge/retry and credentials. Do not debug parameter paths yet. |
| `Inform` | Authentication and CWMP session start succeeded | Record identity/firmware and discover the data model. |
| Outbound RPC + 900x fault | Transport/auth/session work; the operation/model/value is the problem | Diagnose the exact fault and discovered parameter tree. |

### 2. Optional host-side reachability probe

For a controlled field test you can set:

```bash
ACS_ONBOARDING_LISTENER=once
```

This adds **reachability logging on the normal CWMP endpoint before
authentication**. It does not create an alternate ACS endpoint and it does
not bypass authentication. `once` disables the probe after the first
successful Inform; prefer it over indefinite `on`.

Use it when the CPE's HTTP client gives little/no useful local logging and
you need to prove a POST is arriving at the host.

## Phase 2 — Huawei N5368X authentication

The known N5368X firmware profile exposes **Connection request Authentication:
Digest-SHA256**. Keep that enabled. ACS supports SHA-256 for both directions:

- CPE → ACS CWMP HTTP authentication; and
- ACS → CPE HTTP Connection Request authentication.

### Expected sequence

1. CPE POSTs to ACS.
2. ACS returns HTTP 401 with Digest challenge(s).
3. CPE retries with a Digest `Authorization` header.
4. ACS accepts the Inform and returns `InformResponse`.
5. The device completes the empty/session-close exchange.

If the capture shows repeated `AuthenticationFailure` and no Inform:

1. confirm the configured ACS username/password on both sides;
2. confirm the CPE actually retries after the first 401;
3. keep the capture running for one clean reproduction;
4. in an **isolated test profile**, try:

```bash
ACS_DIGEST_ALGORITHMS=SHA-256
```

This narrows the challenge to one supported algorithm and is useful for
embedded HTTP parsers that mishandle multiple `WWW-Authenticate` lines.
It does not weaken verification to an unsupported algorithm.

Only if captured evidence demonstrates that the firmware cannot perform
Digest at all should `ACS_AUTH_ALLOW_BASIC=1` be considered, and then only on
TLS or an isolated management network. Record that as an exact
model/firmware limitation.

### What not to do

Do not simultaneously change Digest algorithm, username/password, TLS floor,
CWMP empty-response status and NAT settings. That destroys the evidence needed
to know which compatibility knob was actually required.

## Phase 3 — Record exact identity and firmware

After the first successful Inform, record:

- Manufacturer
- OUI
- ProductClass
- SerialNumber
- hardware revision if exposed
- software/firmware version
- CWMP namespace/version
- data-model root (`Device.` or `InternetGatewayDevice.`)
- source/NAT topology used during the test
- capture session ID

Do not infer the firmware from marketing model names. Huawei often ships
behavior changes under closely related product labels.

## Phase 4 — Discover before writing

Run **Discover model** / `GetParameterNames` before a SetParameterValues test.
Then perform a small live `GetParameterValues` against paths that were actually
discovered.

Treat **9005** as a model/path mismatch until proven otherwise. A working
session that returns 9005 is not an authentication failure.

For a write test:

1. choose a reversible, non-service-impacting parameter;
2. read the original value;
3. write the test value;
4. read it back;
5. restore the original value; and
6. keep the capture/job command keys as evidence.

Do not start qualification with APN, WAN credentials, radio-locking, factory
reset, or another setting that can remove management connectivity.

## Phase 5 — Huawei EchoLife / TR-098 trap

Several EchoLife firmwares (including families around HG8546M, HG8145V5,
HG8245H and EG8141A5) may advertise a bare path such as:

```text
InternetGatewayDevice.LANDevice.{i}.WLANConfiguration.{i}.KeyPassphrase
```

but reject writes to it. Where the discovered model exposes it, prefer the
writable pre-shared-key object, for example:

```text
...WLANConfiguration.{i}.PreSharedKey.1.KeyPassphrase
```

The exact path is firmware-dependent. Discovery/writability evidence wins
over assumptions or another Huawei model's mapping.

If the CPE returns 9005 or 9007:

- 9005: re-discover the exact path and object instance;
- 9007: verify value type/range/encoding and read the current value first.

When a valid vendor-specific path is confirmed, add it to the vendor mapping
with the model/firmware constraints instead of changing the generic standard
mapping.

## Phase 6 — Prove Connection Request independently

Once normal Inform/session traffic works:

1. verify the CPE supplied a Connection Request URL;
2. start a CWMP capture;
3. issue **Test Connection Request**;
4. record the HTTP result/auth behavior; and
5. require a following **Event 6 CONNECTION REQUEST** Inform.

For N5368X, preserve Digest-SHA256. If the outbound challenge is the problem,
capture the CPE's challenge and compare the advertised algorithm/qop/opaque
before altering credentials.

If the Connection Request URL is private/unreachable from ACS, only then move
to NAT traversal diagnosis:

- STUN-learned address / `NATDetected`;
- Annex G UDP Connection Request where required and supported; or
- the deployment's intended routed/VPN management path.

Do not diagnose NAT while the CPE has not yet supplied a valid Connection
Request URL.

## Phase 7 — Diagnostics, reboot and recovery

After the basic management path is proven, qualify:

- IPPing and TraceRoute trigger + terminal state;
- reboot on an isolated test unit;
- ACS restart or temporary network loss during a queued operation;
- repeated/delayed Inform behavior;
- upload/download and TransferComplete only where the device/profile requires
  it;
- firmware upgrade only with an approved image and recovery path.

The CPE must reconnect and return to a coherent job/device state after the
recovery test.

## Fault-oriented triage

| Symptom / fault | Interpretation | Huawei-first response |
|---|---|---|
| Nothing in capture | Reachability/TLS path | Verify URL, route/firewall/DNS/TLS; optionally arm `ACS_ONBOARDING_LISTENER=once`. |
| AuthenticationFailure only | HTTP reaches ACS, auth does not complete | Verify credentials and Digest retry; on N5368X try a single SHA-256 challenge in isolated testing before Basic. |
| Repeated 401 loop | Embedded Digest/parser/credential mismatch | Preserve capture/log evidence; change one auth variable at a time. |
| Inform works, 9005 | Wrong/unsupported parameter path | Re-run discovery; build model/firmware-specific mapping. |
| Inform works, 9007 | Correct-ish path, invalid value/type | Read original/type/constraints, then retry reversible value. |
| Connection Request fails | Separate outbound reachability/auth problem | Verify URL then CPE Digest challenge; require Event 6 after success. |
| Direct CR impossible behind NAT | NAT topology | Validate STUN/Annex G/routed management only after normal Inform works. |
| CPE stops responding after RPC | Firmware/RPC interoperability | Preserve full bounded capture, exact firmware and command key; reproduce with one minimal RPC. |

## Evidence required before marking a Huawei firmware supported

A PASS requires all applicable evidence below:

- field preflight passed and exact ACS commit recorded;
- exact Huawei model, hardware revision and firmware recorded;
- authenticated Inform and session close proven;
- Digest mode documented (and any compatibility override justified);
- parameter discovery completed;
- live read completed;
- reversible write + read-back + restore completed;
- Connection Request + Event 6 completed;
- NAT traversal completed where required;
- diagnostics/reboot/recovery completed where applicable;
- bounded capture reviewed for redaction;
- any new SOAP/auth/fault shape converted into a sanitized fixture;
- any Huawei-specific path converted into a constrained vendor mapping;
- `COMPATIBILITY.md` updated with the exact firmware result.

Until that evidence exists, the model remains **unqualified**, even if the
generic parser or another Huawei firmware works.
