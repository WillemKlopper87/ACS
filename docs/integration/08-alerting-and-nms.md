# ACS alerting and external NMS integration

ACS has two outbound notification paths. Use the TMF event/alarm hub for tenant and device lifecycle notifications, and use Alertmanager for platform and fleet health alerts.

| Need | Path |
| --- | --- |
| CPE fault, recovery, or device condition | TMF688 event hub and TMF642 alarm records |
| BSS order completion | BSS webhook subscription (`JOB_COMPLETED`) |
| ACS service, fleet, job, lease, firmware, or database health | Prometheus → Alertmanager |

## Connecting a NetPod-style NMS

The generic HTTPS contract is vendor-neutral and works with an NMS webhook receiver or an integration relay. ACS does not claim a native NetPod wire protocol; if NetPod requires proprietary fields, put that mapping in the receiver or relay.

Create a TMF event hub on the BSS adapter. The callback may be fleet-wide or scoped to one account:

```powershell
$body = @{
  callback = "https://nms.example.net/acs/tmf-events"
  secret = $env:ACS_NMS_WEBHOOK_SECRET
  accountId = "tenant-001"
  eventTypes = @("DeviceFault", "JOB_COMPLETED")
} | ConvertTo-Json

Invoke-RestMethod -Method Post -Uri http://localhost:8090/tmf-api/eventManagement/v4/hub `
  -Headers @{ Authorization = "Bearer $env:ACS_BSS_API_TOKEN" } `
  -ContentType application/json -Body $body
```

Receivers must verify `Webhook-Signature: v1,<hex-hmac>` over `<Webhook-Id>.<Webhook-Timestamp>.<raw-body>`, reject stale timestamps, and deduplicate using `Webhook-Id`. The shared secret is never returned by the list-hub endpoint. Rotate it by creating a replacement hub, validating it, and deleting the old hub.

Device faults are persisted in TMF688 and the corresponding alarm is persisted in TMF642. A fault body includes the protocol, job, fault code, and message:

```json
{"protocol":"CWMP","jobId":"job-123","faultCode":"9002","message":"Download failed"}
```

Use the TMF representation's `accountId`, `deviceId`, `sourceKey`, alarm severity, state, `raisedAt`, and `clearedAt` to create and close an NMS incident. Recovery creates a `DeviceRecovered` TMF688 event and clears the durable TMF642 alarm. Do not use the body alone as a deduplication key.

## Alertmanager integration

Set `ACS_ALERT_WEBHOOK_URL` before starting the infrastructure stack. Compose substitutes it into Alertmanager at startup:

```powershell
$env:ACS_ALERT_WEBHOOK_URL = "https://nms.example.net/acs/alertmanager"
docker compose -f infra/docker-compose.yml up -d alertmanager prometheus
```

Alertmanager sends `send_resolved: true`. The receiver should inspect `status` (`firing` or `resolved`) and use `fingerprint` for correlation. Critical alerts repeat hourly; other alerts repeat every four hours until resolved. Current rules are in [`infra/alert_rules.yml`](../../infra/alert_rules.yml).

## Safeguards and rollout

Targets are checked against `ACS_BSS_WEBHOOK_ALLOWED_CIDRS` when saved and immediately before delivery. Redirects are not followed. Use HTTPS in production and restrict the allowlist to the NMS or relay network.

Deliveries are durable in Postgres, retried with exponential backoff, and marked `FAILED` after eight attempts. Monitor `webhook_deliveries` for failed or growing pending rows. For rollout, first verify signatures in a receiver without opening tickets, then trigger a lab CWMP/USP fault, confirm one event/alarm/delivery, test deduplication, verify recovery closes the NMS incident, and verify Alertmanager firing and resolved notifications.

## CPE escalation policies

Policies can apply to the whole fleet, a tenant, a device group, or one CPE. Resolution precedence is device, group, tenant, then fleet. A tenant policy may also specify `customer_tier`; the runtime uses the mapped service plan to select the premium policy. Fault codes support exact keys and suffix wildcards, and the special `offline` key controls silent-CPE incidents.

Create a policy with delays expressed as nanoseconds in the current Go JSON contract (the console converts seconds to nanoseconds):

```json
{
  "name": "Premium CPE escalation",
  "scope": "tenant",
  "tenant_id": "tenant-001",
  "customer_tier": "premium",
  "enabled": true,
  "fault_priorities": {"offline": "P1", "9002": "P2"},
  "offline_after": 180000000000,
  "steps": [
    {"after": 300000000000, "destination": "nms", "recipient": "netpod-premium"},
    {"after": 900000000000, "destination": "sms", "recipient": "+27115550123"}
  ]
}
```

Use `POST /api/v1/alert-policies` with the policy-management permission, list policies with `GET /api/v1/alert-policies`, and remove one with `DELETE /api/v1/alert-policies/{id}`. Incidents are available through `GET /api/v1/alert-incidents`; operators can acknowledge, suppress, recover, or close them with the protected PATCH endpoint.

Incident webhooks use the existing signed delivery outbox and can subscribe to `ALERT_OPENED`, `ALERT_ESCALATED`, and `ALERT_RECOVERED`. `ALERT_OPENED` is emitted once when a deduplicated condition first appears, escalation steps emit `ALERT_ESCALATED`, and a CPE check-in emits `ALERT_RECOVERED`. Receivers should correlate on `incident_id` and remain idempotent because delivery is retried after transport failures.
