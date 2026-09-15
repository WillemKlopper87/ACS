# Device Authentication and Deployment Profiles

This document implements the security-remediation policy tracked in #56. It separates controlled CPE investigation from production device management and defines the identity guarantees required for CWMP and USP.

## Profiles

### `ACS_DEPLOYMENT_PROFILE=lab`

The lab profile exists for controlled hardware qualification, packet/session capture and compatibility testing. It may use plaintext management transports and the historical fleet-wide CWMP Digest credential when the surrounding network is explicitly restricted.

A lab deployment is **not** a production security baseline. Restrict inbound management ports to the test source CIDR/security group, retain capture redaction, and avoid placing unrelated tenant devices behind the same exposed lab endpoint.

### `ACS_DEPLOYMENT_PROFILE=production`

Production is fail-closed:

- CWMP requests must arrive over TLS.
- HTTP Basic is rejected for CWMP.
- The configured fleet-wide `ACS_DIGEST_USERNAME` is rejected for CWMP. Established CPEs must use a per-device Digest credential resolved from `device_credentials`, or verified mTLS.
- USP plaintext is forbidden.
- `ACS_USP_TLS_CERT` and `ACS_USP_TLS_KEY` are required.
- `ACS_USP_ALLOWED_CIDRS` is required and may not contain `0.0.0.0/0` or `::/0`.
- `scripts/security-preflight.sh` must pass before a production field release is accepted.

CIDR filtering is defence in depth. It does not establish device identity.

## CWMP identity policy

A per-device `CWMP_DIGEST` credential is cryptographically bound by the ACS credential lookup to one `device_id`. The Inform OUI/serial identity must resolve to that same device before normal management is accepted.

The shared fleet credential is retained only for controlled lab compatibility while the complete bootstrap/graduation flow is implemented and field-proven. Production rejects the shared username before the CWMP session handler, so a leaked fleet secret cannot be used there to claim another tenant's established device.

### Huawei N5368X field sequence

For Huawei N5368X qualification (#54):

1. Use the lab profile on a source-restricted field endpoint for first-contact capture if the device cannot yet be provisioned with a unique ACS credential.
2. Capture and verify Manufacturer, OUI, ProductClass, SerialNumber, firmware and observed CWMP data model.
3. Create a unique per-device `CWMP_DIGEST` credential for the registered ACS device.
4. Configure the Huawei ManagementServer username/password with that credential, retaining Digest SHA-256 compatibility.
5. Reconnect and prove the credential resolves to the intended `device_id` and the Inform OUI/serial matches it.
6. Prove identity substitution is rejected before accepting the device as securely qualified.
7. Configure Connection Request credentials independently and uniquely for the device where supported.
8. Only after bound reconnect should normal provisioning, diagnostics, firmware or commercial fulfilment be treated as production-authorized.

The desired end state is an automated bootstrap-to-bound-credential graduation. Until that automation is complete, pre-provisioning or manual credential installation is safer than using a fleet secret in production.

## USP identity policy

USP must move from network admission to authenticated agent identity:

- each agent receives an individual authenticated transport principal (MQTT credential and/or client certificate);
- that principal maps to exactly one expected USP EndpointID and ACS device identity;
- an inbound USP Record `from_id` must equal the endpoint bound to the authenticated principal;
- MQTT publish/subscribe ACLs are least-privilege per agent;
- WebSocket must enforce the same principal-to-EndpointID binding;
- CIDR allowlists remain an additional network control, never the primary identity proof.

The existing OB-USP-Agent interoperability, reconnect and subscription-persistence tests must remain intact while these controls are added.

## Release evidence

A production release is not accepted until:

- normal CI and field-RC workflows are green;
- `scripts/security-preflight.sh` passes for the target environment;
- a shared CWMP fleet credential is rejected in production;
- a per-device CWMP credential can reconnect and manage only its bound device;
- USP impersonation and cross-topic attempts are rejected;
- the Huawei field evidence in #54 records the bound-credential reconnect and identity-substitution result;
- the security review is rerun and the findings tracked in #56 are either closed or explicitly proven unreachable in the supported production profile.
