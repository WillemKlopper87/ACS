# Huawei N5368X secure qualification procedure

This procedure is the security-focused execution path for issue #54 and
Element 3 of issue #59. It supplements `HUAWEI-CPE-ONBOARDING.md`,
`FIELD-CPE-VALIDATION.md`, and `HUAWEI-CPE-QUALIFICATION-TEMPLATE.md` with the
production CWMP bootstrap and unique-credential graduation flow now implemented
by ACS.

The qualification target is one exact **Huawei N5368X hardware revision and
firmware build**. Do not generalize a result to the full N5368X family without
separate evidence.

## 1. Pin and preflight the ACS build

Use a clean checkout of `main` (or an explicitly approved release tag) on the
field test system. Before changing the CPE:

```bash
git fetch origin
git checkout main
git pull --ff-only origin main
git rev-parse HEAD
git status --short
./scripts/field-preflight.sh
```

Record the exact commit in the qualification evidence. A dirty worktree is not
a reproducible qualification baseline.

For the **production security path**, configure at minimum the normal
production TLS/secrets plus the dedicated CWMP bootstrap pair:

```text
ACS_DEPLOYMENT_PROFILE=production
ACS_CWMP_BOOTSTRAP_USERNAME=<dedicated bootstrap username>
ACS_CWMP_BOOTSTRAP_PASSWORD=<strong bootstrap secret, at least 16 bytes>
ACS_CREDENTIAL_ENCRYPTION_KEY=<strong independent encryption key>
```

Do not record the password or encryption-key values in field evidence.
`ACS_CWMP_BOOTSTRAP_USERNAME` must be distinct from any legacy
`ACS_DIGEST_USERNAME`. Production startup fails closed when the bootstrap pair
is incomplete, weak, or when generated per-device credentials cannot be
protected at rest.

Keep `ACS_AUTH_ALLOW_BASIC` disabled. Preserve TLS 1.2+ and Digest SHA-256.
Only use a lab profile for a deliberately isolated compatibility experiment,
and record that experiment separately from the production-path result.

## 2. Start capture before first contact

Start a bounded CWMP remote-IP capture before configuring/rebooting the CPE.
Record the capture ID. If the device has not authenticated before, identity
capture is unavailable until the first Inform is parsed.

Optionally use:

```text
ACS_ONBOARDING_LISTENER=once
```

for pre-auth reachability telemetry. It is not an authentication bypass.

The first-contact evidence must show:

1. request reaches ACS;
2. ACS issues a Digest challenge;
3. CPE retries using the dedicated bootstrap username;
4. ACS accepts only the constrained bootstrap Inform;
5. normal jobs/policies/firmware/diagnostics are not dispatched during the
   bootstrap exchange.

## 3. Prove constrained bootstrap

On the first authenticated bootstrap Inform, record the normalized identity:

- Manufacturer;
- OUI;
- ProductClass;
- SerialNumber (masked/hash in retained evidence);
- CWMP namespace;
- reported software/firmware version if present;
- data-model root evidence (`Device.` or `InternetGatewayDevice.`).

The bootstrap credential must not be accepted for an identity that ACS already
regards as established. If the CPE has already been qualified or enrolled,
reset/use a dedicated clean unit or explicitly remove only the test enrollment
state according to the test plan; do not weaken the bootstrap guard.

## 4. Observe unique credential installation

For a supported data-model root, ACS creates/reuses one device-bound
`PENDING` `CWMP_DIGEST` credential and the next permitted bootstrap operation
is only the credential-installation `SetParameterValues`.

Expected standard parameter paths are:

**TR-181 / Device:2**

```text
Device.ManagementServer.Username
Device.ManagementServer.Password
```

**TR-098 / InternetGatewayDevice:1**

```text
InternetGatewayDevice.ManagementServer.Username
InternetGatewayDevice.ManagementServer.Password
```

Record the paths, command key, response/fault and capture event IDs, but never
record the generated password. A `SetParameterValuesResponse` proves only that
the write was acknowledged; it does **not** activate the credential.

If ACS cannot determine a safe data-model root, or the Huawei firmware rejects
the credential write, classify automatic graduation as **BLOCKED** for that
firmware. The credential must remain `PENDING`. Do not fall back to a shared
steady-state credential. Perform an explicit operator/manual installation only
if the test plan allows it, then continue with the same bound reconnect proof.

## 5. Prove bound reconnect and graduation

After the credential-installation exchange ends, require the CPE to establish
a new CWMP session using the generated unique credential.

The reconnect passes only when all of the following are observed:

1. HTTP Digest proof succeeds with the unique per-device username/secret;
2. the subsequent Inform reports the same normalized OUI/ProductClass/Serial
   bound to that credential's ACS DeviceID;
3. the credential lifecycle moves from `PENDING` to `ACTIVE` only after that
   identity check;
4. normal CWMP management becomes available only after this reconnect;
5. another identity cannot use the credential to activate or manage a device.

Retain only credential ID/status/username metadata when needed for evidence;
do not retain the generated password or Authorization header.

## 6. Prove bootstrap is closed after graduation

After the unique credential is ACTIVE, attempt a controlled reconnect using
the bootstrap identity against the same established CPE identity. The request
must be rejected and must not consume queued work or create a second device.

Record the rejection in the bounded capture. This is the physical counterpart
to the automated established-device bootstrap regression.

## 7. Continue normal Huawei CWMP qualification

Only after secure graduation is proven, continue the generic real-device
sequence:

1. observe BOOTSTRAP / BOOT / PERIODIC / session-close behavior;
2. run `GetParameterNames` discovery;
3. run representative `GetParameterValues` reads;
4. perform one reversible, non-service-impacting `SetParameterValues`, read it
   back, and restore the original value;
5. test ACS -> CPE Connection Request and require the following Event 6
   CONNECTION REQUEST Inform;
6. preserve Digest-SHA256 for the N5368X Connection Request path;
7. test STUN / Annex G only when the deployment topology requires it;
8. run IPPing / TraceRoute where exposed;
9. reboot the dedicated test unit and prove stable identity/reconnect;
10. test Upload/Download/TransferComplete only when explicitly approved.

For every operation, record the command key, capture ID and final result/fault.
A UI success notification without protocol/device evidence is insufficient.

## 8. Identity-substitution evidence

The production security result must include evidence that device identity is
not caller-controlled after authentication.

At minimum retain:

- the successful unique-credential reconnect for the intended identity;
- the automated wrong-device regression from the exact ACS build's CI; and
- where safely reproducible in the lab, a controlled mismatched-identity
  attempt showing rejection before credential activation/normal management.

Do not modify a production/customer CPE serial/OUI merely to manufacture this
case. Automated evidence is acceptable for the destructive/impractical
negative case when the physical positive binding is proven.

## 9. Northbound and operational verification

After normal management is enabled, verify as applicable:

- TMF639 resource projection contains the intended device and no credentials;
- TMF638 service mapping references the correct resource when service intent
  exists;
- TMF640/641 activation/order correlation uses the existing ACS job path;
- TMF688/642/656 event/alarm/problem correlation for a safe applicable
  scenario;
- operator UI/API shows one stable device identity through reboot/reconnect.

Use least-privilege OAuth/account scope for northbound tests.

## 10. Redaction and compatibility output

Before retaining any capture/export:

- remove HTTP Authorization values and all passwords/secrets;
- mask/hash serials when required;
- verify no Wi-Fi, SIP, PPP or subscriber secrets remain;
- keep enough SOAP/auth/fault shape to reproduce vendor behavior.

If the N5368X exposes a new valid SOAP namespace, Digest shape, compression
form, fault, or vendor parameter path, add a sanitized fixture/regression test
and record the exact firmware constraint.

Update `COMPATIBILITY.md` only after the mandatory qualification steps are
supported by retained evidence. Use PASS / FAIL / BLOCKED / N/A explicitly.

## 11. Exit evidence for #54 / #59 Element 3

Element 3 is complete only when the issue contains or links evidence for:

- exact tested `main` SHA and successful field preflight;
- exact N5368X model/ProductClass, hardware revision, OUI and firmware;
- Digest-SHA256 first contact and authenticated bootstrap Inform;
- constrained bootstrap isolation;
- device-bound PENDING credential creation and credential-install SPV;
- successful unique-credential reconnect and `PENDING -> ACTIVE` graduation;
- post-graduation bootstrap rejection;
- identity-substitution rejection evidence;
- BOOTSTRAP/BOOT/PERIODIC/session close;
- GPN, representative GPV and reversible SPV/read-back/restore;
- device-specific Connection Request and following Event 6 Inform;
- reboot/reconnect/recovery;
- sanitized captures/fixtures and `COMPATIBILITY.md` update.

Any unsupported firmware behavior remains explicit as BLOCKED and must not be
converted into a fleet-wide security downgrade.
