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
- A production deployment may run with device-specific CWMP Digest credentials only; it does not need to retain the fleet-wide Digest password. `ACS_CREDENTIAL_ENCRYPTION_KEY` remains required because it protects device credentials at rest and is also used as the Digest nonce-signing secret when no shared Digest password exists.
- USP plaintext is forbidden.
- `ACS_USP_TLS_CERT` and `ACS_USP_TLS_KEY` are required.
- `ACS_USP_CLIENT_CA_CERT` is required. Both USP WebSocket and MQTT require a client certificate chaining to this CA.
- `ACS_USP_ALLOWED_CIDRS` is required and may not contain `0.0.0.0/0` or `::/0`.
- Each production USP agent certificate must be pre-bound in `usp_transport_principals` to exactly one ACS `device_id`, expected USP EndpointID and exact MQTT response-topic root before that agent can connect successfully.
- `scripts/security-preflight.sh` must pass before a production field release is accepted.

CIDR filtering is defence in depth. It does not establish device identity.

## CWMP identity policy

A per-device `CWMP_DIGEST` credential is cryptographically bound by the ACS credential lookup to one `device_id`. The Inform OUI/ProductClass/Serial identity must resolve to that same device before normal management is accepted.

The shared fleet credential is retained only for controlled lab compatibility while the complete bootstrap/graduation flow is implemented and field-proven. Production rejects the shared username before the CWMP session handler, so a leaked fleet secret cannot be used there to claim another tenant's established device.

### Huawei N5368X field sequence

For Huawei N5368X qualification (#54):

1. Use the lab profile on a source-restricted field endpoint for first-contact capture if the device cannot yet be provisioned with a unique ACS credential.
2. Capture and verify Manufacturer, OUI, ProductClass, SerialNumber, firmware and observed CWMP data model.
3. Create a unique per-device `CWMP_DIGEST` credential for the registered ACS device.
4. Configure the Huawei ManagementServer username/password with that credential, retaining Digest SHA-256 compatibility.
5. Reconnect and prove the credential resolves to the intended `device_id` and the Inform OUI/ProductClass/Serial identity matches it.
6. Prove identity substitution is rejected before accepting the device as securely qualified.
7. Configure Connection Request credentials independently and uniquely for the device where supported.
8. Only after bound reconnect should normal provisioning, diagnostics, firmware or commercial fulfilment be treated as production-authorized.

The desired end state is an automated bootstrap-to-bound-credential graduation. Until that automation is complete, pre-provisioning or manual credential installation is safer than using a fleet secret in production.

## USP identity policy

Production USP uses **mutual TLS for both WebSocket and MQTT**. Network admission and caller-supplied USP metadata are not identity proofs.

The trust chain is:

1. The agent presents an individual client certificate during the TLS handshake.
2. `crypto/tls` verifies that certificate against `ACS_USP_CLIENT_CA_CERT` before the application accepts the connection.
3. The verified leaf certificate's SHA-256 fingerprint is resolved through `usp_transport_principals`.
4. That durable principal identifies exactly one ACS `device_id`, one expected USP EndpointID and one exact MQTT response-topic root.
5. WebSocket rejects an `eid` that differs from the certificate-bound EndpointID before upgrading the connection, and rejects a later USP Record whose `from_id` differs from the connection identity.
6. MQTT rejects a Record `from_id` that differs from the certificate-bound EndpointID. MQTT 5 Response Topic and MQTT 3.1.1 `reply-to` values must equal that principal's response-topic root.
7. MQTT publish/subscribe authorization is derived from the authenticated principal. An agent may publish only to the controller namespace required by its MTP binding and subscribe/read only within its own response-topic namespace; cross-agent topics are rejected.
8. During identity reconciliation, the agent's reported OUI/ProductClass/Serial must resolve to the same `device_id` already bound to the authenticated transport principal before live device state is mutated.

`usp_agents` remains live connection state and is deliberately **not** the trust authority for a connection. The pre-provisioned `usp_transport_principals` table is separate so a connection cannot authenticate itself by supplying the same EndpointID that would otherwise create or update its live-state row.

Certificate rotation replaces the device's previous fingerprint binding; disabling the principal prevents that binding from authenticating new sessions. Operators must distribute the replacement agent certificate and update the binding in a controlled sequence to avoid stranding the device.

The current security implementation provides the durable repository and runtime enforcement. Principal provisioning is an operational prerequisite; do not infer that a public operator REST endpoint exists unless one is explicitly added and reviewed.

The existing OB-USP-Agent interoperability, reconnect and subscription-persistence tests remain the compatibility gate. Lab mode keeps the explicit non-mTLS path used by those reference-agent and field-investigation tests; production does not inherit that relaxation.

## Production USP configuration

At minimum, production `cmd/uspc` needs:

```text
ACS_DEPLOYMENT_PROFILE=production
ACS_USP_TLS_CERT=/path/to/server-fullchain.pem
ACS_USP_TLS_KEY=/path/to/server-key.pem
ACS_USP_CLIENT_CA_CERT=/path/to/agent-client-ca.pem
ACS_USP_ALLOWED_CIDRS=<management-network-cidr[,more-cidrs]>
ACS_USP_POSTGRES_DSN=<postgres-dsn>
```

Before starting an agent, provision an `usp_transport_principals` row containing its ACS `device_id`, expected EndpointID, SHA-256 fingerprint of the client certificate DER, and exact MQTT response topic. The table enforces uniqueness for the EndpointID, fingerprint and MQTT topic, preventing two devices from sharing the same production transport identity.

## Release evidence

A production release is not accepted until:

- normal CI and field-RC workflows are green;
- `scripts/security-preflight.sh` passes for the target environment;
- a shared CWMP fleet credential is rejected in production;
- a per-device CWMP credential can reconnect and manage only its bound device;
- production USP rejects an unknown/disabled client certificate;
- WebSocket rejects EndpointID substitution;
- MQTT rejects Record From-ID substitution and cross-agent Response Topic / reply-to / publish-subscribe access;
- OB-USP-Agent WebSocket, MQTT 5 and MQTT 3.1.1 interoperability remains green in the controlled compatibility profile;
- the Huawei field evidence in #54 records the bound-credential reconnect and identity-substitution result;
- the security review is rerun and the findings tracked in #56 are either closed or explicitly proven unreachable in the supported production profile.
