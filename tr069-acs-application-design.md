# Enterprise TR-069 ACS Platform — Application Design

> **Historical document.** Written before the 2026-08-30 audit hardening pass; the code, the root README, and `ACS_CODEBASE_AUDIT_2026-08-28.md` are the source of truth where this differs.

Status: Draft  
Date: 2026-08-04  
Scope: Implementation design for an enterprise TR-069 / CWMP ACS platform managing Huawei 5G CPE, Nokia FastMile, Teltonika 5G CPE, and Zyxel 5G CPE devices.

---

## 1. Summary

I would write this application as an **event-driven TR-069/CWMP ACS platform**, not as a traditional CRUD web application.

The core constraint is:

> The ACS cannot push arbitrary work to a CPE whenever it wants.  
> Most work must be queued and executed when the CPE has an open CWMP session, or after the ACS successfully wakes the CPE using Connection Request.

Therefore, the application should be built around:

1. A **CWMP/SOAP HTTPS endpoint** that receives CPE sessions.
2. A **device/session state machine**.
3. An **asynchronous job queue** for all ACS-initiated actions.
4. A **REST API gateway** that queues work and returns `202 Accepted`.
5. A **firmware/file service** used by the CPE during `Download()`.
6. A **security layer** with TLS, mTLS where possible, Digest fallback, audit, and secrets management.
7. A **data-model abstraction layer** supporting both TR-181 `Device.` and legacy TR-098 `InternetGatewayDevice.` roots.

---

## 2. Correct TR-069 Assumptions

The implementation must follow the corrected architecture assumptions.

### 2.1 Protocol documents

The relevant documents are:

| Topic | Correct reference |
|---|---|
| CWMP protocol | TR-069 |
| Legacy data model | TR-098, `InternetGatewayDevice.` root |
| Current data model | TR-181 Issue 2, `Device.` root / Device:2 |

Do not build against TR-068 or TR-179 for this platform.

### 2.2 Session initiation

The CPE opens the CWMP session by sending `Inform`.

Typical Inform event codes:

```text
0  BOOTSTRAP
1  BOOT
2  PERIODIC
4  VALUE CHANGE
6  CONNECTION REQUEST
```

Connection Request is **ACS-initiated**, but it does not carry configuration. It only asks the CPE to start a new session.

Correct conceptual flow:

```text
CPE -> ACS: HTTPS POST Inform
ACS -> CPE: InformResponse
ACS -> CPE: Optional RPCs, e.g. GetParameterValues, SetParameterValues, Download
CPE -> ACS: RPC responses
ACS -> CPE: Empty HTTP body to end session, or additional RPCs
```

Out-of-band wake:

```text
ACS -> CPE: HTTP GET to ConnectionRequestURL
CPE -> ACS: New Inform session with EventCode 6 CONNECTION REQUEST
```

### 2.3 Firmware updates

Firmware updates must use the TR-069 `Download()` RPC.

Do not attempt to send firmware bytes inside SOAP parameter writes.

Correct OTA flow:

```text
ACS sends Download(FileType, URL, FileSize, Credentials, ...)
CPE fetches firmware over HTTP/HTTPS/FTP
CPE flashes and reboots if required
CPE sends TransferComplete
ACS optionally verifies SoftwareVersion afterwards
```

### 2.4 Security target

For a new enterprise deployment:

```text
Target: mutual TLS (mTLS)
Fallback: HTTP Digest authentication
```

Because the actual CPE fleet’s mTLS capability is not yet confirmed, the implementation should support both and not make mTLS mandatory until verified on real units.

---

## 3. Recommended Technology Stack

For a production enterprise ACS, I would use:

| Layer | Recommended choice | Reason |
|---|---|---|
| Core CWMP gateway | Go | Strong TLS, HTTP, concurrency, simple deployment |
| REST API | Go, or Python/FastAPI if team prefers | Same language reduces operational complexity |
| Primary database | PostgreSQL | Devices, jobs, users, audit, firmware metadata |
| Cache/session store | Redis | Active sessions, pending RPC queues, pub/sub |
| Async queue | RabbitMQ or Kafka | Bulk provisioning, OTA rollouts, retries |
| Firmware storage | S3-compatible storage / CDN / HTTPS static host | CPE pulls firmware image directly |
| Metrics | Prometheus + Grafana | Fleet health, session throughput, failure rates |
| Tracing | OpenTelemetry | Correlate RPC request and later TransferComplete |
| Secrets | Vault / AWS Secrets Manager / GCP Secret Manager | Digest secrets, firmware transfer credentials, keys |

A practical production architecture:

```text
Go CWMP gateway
Go REST API
PostgreSQL
Redis
Kafka or RabbitMQ
S3-compatible firmware storage
Prometheus/Grafana
OpenTelemetry
```

If the team is smaller or Python-focused:

```text
Python/FastAPI
SQLAlchemy
PostgreSQL
Redis
Celery/RQ
S3-compatible storage
```

The language is less important than correctly implementing the session/job model.

---

## 4. High-Level Architecture

```text
                         ┌────────────────────────────┐
                         │        REST API            │
                         │ /devices /jobs /firmware   │
                         └──────────────┬─────────────┘
                                        │ queue jobs
                                        ▼
┌────────────┐      HTTPS/SOAP     ┌────────────────────────────┐
│  CPE Fleet │◀───────────────────▶│       CWMP Gateway         │
│ TR-069     │                     │  Inform / RPC / Session    │
└─────┬──────┘                     └──────────────┬─────────────┘
      │                                           │
      │ Connection Request                        │ session events
      │ HTTP GET                                  ▼
      │                              ┌────────────────────────────┐
      └──────────────────────────────│   Session/Job Orchestrator │
                                     └──────┬──────────┬──────────┘
                                            │          │
                            ┌───────────────┘          └───────────────┐
                            ▼                                          ▼
                   ┌─────────────────┐                        ┌─────────────────┐
                   │ PostgreSQL      │                        │ Redis           │
                   │ devices/jobs/   │                        │ sessions/queues/│
                   │ audit/firmware  │                        │ pubsub/cache    │
                   └─────────────────┘                        └─────────────────┘

                   ┌─────────────────┐
                   │ Firmware/Config │
                   │ HTTPS file host │
                   └─────────────────┘
```

Key rule:

> REST write operations do not directly talk to the CPE.  
> They create jobs. The CWMP session worker executes those jobs when a session is available.

---

## 5. Core Application Components

### 5.1 CWMP Gateway

Responsibilities:

- Accept HTTPS POST requests from CPEs.
- Parse SOAP/XML CWMP messages.
- Handle `Inform`.
- Return `InformResponse`.
- Handle empty HTTP requests.
- Dispatch queued RPCs.
- Parse RPC responses.
- Handle `TransferComplete`.
- Emit session events to the orchestrator.

Important RPCs:

```text
Inform
InformResponse
GetRPCMethods
GetParameterValues
SetParameterValues
GetParameterNames
GetParameterAttributes
SetParameterAttributes
AddObject
DeleteObject
Download
Upload
TransferComplete
Reboot
FactoryReset
ScheduleInform
```

Example pseudocode:

```python
def handle_cwmp_post(request):
    soap_message = parse_soap(request.body)

    if soap_message.is_inform():
        device = authenticate_device(request)
        device_state = upsert_device_from_inform(
            device=device,
            event_codes=soap_message.event_codes,
            device_id=soap_message.device_id,
            parameters=soap_message.parameter_list,
        )

        session = open_session(device_state)
        store_inform_event(session, soap_message)

        return render_inform_response(session)

    elif soap_message.is_empty_http_request():
        session = get_active_session(request)
        pending_rpc = fetch_next_pending_rpc(session.device_id)

        if pending_rpc is None:
            return empty_http_response()

        send_rpc_to_cpe(session, pending_rpc)
        return render_rpc_request(pending_rpc)

    elif soap_message.is_rpc_response():
        session = get_active_session(request)
        result = parse_rpc_response(soap_message)

        complete_rpc_step(session, result)

        if result.is_fault():
            record_job_failure(result)

        return continue_session_or_close(session)

    elif soap_message.is_transfer_complete():
        handle_transfer_complete(soap_message)
        return render_transfer_complete_response()
```

---

### 5.2 Device Authentication Module

Authentication should support multiple modes.

Preferred:

```text
Mutual TLS
```

Fallback:

```text
HTTP Digest auth
```

Device natural key:

```text
OUI + SerialNumber
```

or:

```text
OUI + ProductClass + SerialNumber
```

Internal device ID should be a UUID.

Example:

```python
def authenticate_device(request, inform):
    if request.client_certificate:
        return authenticate_by_mtls(request.client_certificate)

    if request.digest_auth:
        return authenticate_by_digest(request.digest_auth)

    raise Unauthorized()
```

Because mTLS support is not yet confirmed for the fleet, the system should support:

```text
MTLS
DIGEST
BOOTSTRAP_ENROLLMENT
UNKNOWN
```

---

### 5.3 Session Manager

CWMP sessions may span multiple HTTP requests.

Track session state per device:

```text
SESSION_OPENED
INFORM_RECEIVED
INFORM_RESPONSE_SENT
WAITING_FOR_EMPTY_REQUEST
RPC_DISPATCHED
RPC_RESPONSE_RECEIVED
SESSION_CLOSING
SESSION_CLOSED
```

Store active session state in Redis:

```text
session:{device_id}
```

Example Redis session object:

```json
{
  "session_id": "uuid",
  "device_id": "uuid",
  "state": "RPC_DISPATCHED",
  "opened_at": "2026-08-04T10:15:00Z",
  "last_inform_event_codes": ["2 PERIODIC"],
  "pending_rpc_queue": "device:{device_id}:pending",
  "current_rpc_id": "rpc_123",
  "last_activity_at": "2026-08-04T10:15:07Z"
}
```

---

### 5.4 Job Orchestrator

Every ACS-initiated action becomes a job.

Job types:

```text
SET_PARAMETER
GET_PARAMETER
GET_PARAMETER_NAMES
REBOOT
FACTORY_RESET
FIRMWARE_DOWNLOAD
DIAGNOSTICS_PING
DIAGNOSTICS_TRACEROUTE
CONNECTION_REQUEST
SET_PARAMETER_ATTRIBUTES
```

Job states:

```text
QUEUED
SCHEDULED
SESSION_AVAILABLE
RPC_SENT
RPC_ACKED
IN_PROGRESS
SUCCESS
FAILED
TIMEOUT
CANCELLED
```

Each job should have a unique `CommandKey`.

Example job:

```json
{
  "job_id": "6c8f4a5e-8a2f-4a1c-9d21-2f2c9b0d1e22",
  "command_key": "fw-2026-08-04-0001",
  "device_id": "d1a2...",
  "type": "FIRMWARE_DOWNLOAD",
  "status": "QUEUED",
  "payload": {
    "file_type": "1 Firmware Upgrade Image",
    "url": "https://firmware.example.com/nr5103/v2.3.1.bin",
    "file_size": 52428800,
    "target_filename": "firmware.bin",
    "delay_seconds": 0
  }
}
```

---

### 5.5 Pending RPC Queue

Each device has a pending RPC queue.

Redis key:

```text
device:{device_id}:pending_rpcs
```

When REST API receives a write request:

```python
def queue_rpc(device_id, rpc):
    redis.rpush(f"device:{device_id}:pending_rpcs", serialize(rpc))
    job_store.update_status(rpc.job_id, "QUEUED")
```

When the device opens a session:

```python
def on_inform(device_id):
    mark_device_online(device_id)

    while rpc := redis.lpop(f"device:{device_id}:pending_rpcs"):
        execute_rpc_in_session(device_id, rpc)
```

If immediate execution is requested:

```python
def queue_rpc_and_try_wake(device_id, rpc):
    queue_rpc(device_id, rpc)

    if device_is_reachable(device_id):
        send_connection_request(device_id)
```

---

### 5.6 Connection Request Service

Responsibilities:

- Read `ConnectionRequestURL` from the device record.
- Send HTTP GET to the CPE.
- Use Digest auth if required.
- Wait for a new Inform, preferably with EventCode `6 CONNECTION REQUEST`.
- Record success, timeout, or failure.

Example:

```python
def connection_request(device_id, timeout_seconds=30):
    device = get_device(device_id)

    if not device.connection_request_url:
        return ConnectionRequestResult.UNAVAILABLE

    response = http_get_with_digest(
        url=device.connection_request_url,
        username=device.connection_request_username,
        password=get_secret(device.connection_request_secret_ref),
        timeout=10,
    )

    if response.status_code != 200:
        return ConnectionRequestResult.HTTP_FAILURE

    return wait_for_new_inform(device_id, timeout_seconds)
```

Important:

> A successful Connection Request HTTP 200 does not necessarily mean the CPE session has opened yet.

Because the fleet consists of cellular 5G CPEs, assume CGNAT may be present. Connection Request may fail for devices behind carrier-grade NAT unless STUN/Annex G, IPv6 reachability, or a public IP/APN is available.

Fallback:

```text
Wait for next periodic Inform.
```

---

## 6. Data Model Strategy

### 6.1 Support both roots

The platform should support:

```text
TR-181 Device:2 root:
  Device.

TR-098 IGD:1 root:
  InternetGatewayDevice.
```

Do not assume all CPEs use the same root.

Example:

```text
Device.DeviceInfo.SoftwareVersion
InternetGatewayDevice.DeviceInfo.SoftwareVersion
```

Detect root during first `Inform` or `GetParameterNames` exchange.

Store the result:

```text
data_model_root = DEVICE2 | IGD1 | UNKNOWN
```

---

### 6.2 Canonical parameter mapping

Do not hardcode TR-181 paths everywhere.

Use canonical names:

```python
class CanonicalParameter:
    DEVICE_INFO_SERIAL_NUMBER = "device_info.serial_number"
    DEVICE_INFO_SOFTWARE_VERSION = "device_info.software_version"
    WIFI_SSID = "wifi.ssid"
    WIFI_ASSOCIATED_CLIENTS = "wifi.associated_clients"
    MANAGEMENT_SERVER_CONNECTION_REQUEST_URL = "management_server.connection_request_url"
```

Map them to actual paths:

```python
DEVICE2_PATHS = {
    "device_info.serial_number": "Device.DeviceInfo.SerialNumber",
    "device_info.software_version": "Device.DeviceInfo.SoftwareVersion",
    "management_server.connection_request_url": "Device.ManagementServer.ConnectionRequestURL",
}

IGD1_PATHS = {
    "device_info.serial_number": "InternetGatewayDevice.DeviceInfo.SerialNumber",
    "device_info.software_version": "InternetGatewayDevice.DeviceInfo.SoftwareVersion",
}
```

Resolver:

```python
def resolve_path(device, canonical_parameter):
    if device.data_model_root == "DEVICE2":
        return DEVICE2_PATHS[canonical_parameter]

    if device.data_model_root == "IGD1":
        return IGD1_PATHS[canonical_parameter]

    raise UnknownDataModelRoot(device.id)
```

---

### 6.3 Vendor adapters

Vendor-specific behavior should be isolated in adapters.

Example adapters:

```text
TR181Adapter
TR098Adapter
HuaweiAdapter
NokiaAdapter
TeltonikaAdapter
ZyxelAdapter
```

Adapter responsibilities:

```text
Resolve data model paths
Handle vendor extensions
Normalize diagnostics objects
Handle firmware profile quirks
Detect unsupported features
```

Vendor extension namespace pattern:

```text
Device.X_<VENDOR-OUI>_<ParameterName>
```

Examples:

```text
Device.X_HUAWEI_...
Device.X_NOKIA_...
Device.X_TELTONIKA_...
Device.ZYXEL_...
```

---

## 7. Database Design

### 7.1 Devices table

```sql
CREATE TABLE devices (
    id UUID PRIMARY KEY,
    oui_serial TEXT UNIQUE NOT NULL,
    manufacturer TEXT,
    oui TEXT,
    product_class TEXT,
    serial_number TEXT,
    model_name TEXT,
    hardware_version TEXT,
    software_version TEXT,
    data_model_root TEXT CHECK (data_model_root IN ('DEVICE2', 'IGD1', 'UNKNOWN')),
    cwmp_amendment TEXT,
    connection_request_url TEXT,
    periodic_inform_enable BOOLEAN,
    periodic_inform_interval INTEGER,
    auth_mode TEXT CHECK (auth_mode IN ('MTLS', 'DIGEST', 'UNKNOWN')),
    online_status TEXT CHECK (online_status IN ('ONLINE', 'OFFLINE', 'UNREACHABLE')),
    last_inform_at TIMESTAMPTZ,
    last_inform_event_codes TEXT[],
    first_seen_at TIMESTAMPTZ DEFAULT now(),
    last_updated_at TIMESTAMPTZ DEFAULT now()
);
```

---

### 7.2 Jobs table

```sql
CREATE TABLE jobs (
    id UUID PRIMARY KEY,
    command_key TEXT UNIQUE NOT NULL,
    device_id UUID NOT NULL REFERENCES devices(id),
    type TEXT NOT NULL,
    status TEXT NOT NULL,
    payload JSONB NOT NULL,
    attempts INTEGER DEFAULT 0,
    max_attempts INTEGER DEFAULT 3,
    created_by TEXT,
    created_at TIMESTAMPTZ DEFAULT now(),
    updated_at TIMESTAMPTZ DEFAULT now(),
    scheduled_at TIMESTAMPTZ,
    started_at TIMESTAMPTZ,
    completed_at TIMESTAMPTZ,
    fault_code TEXT,
    fault_string TEXT
);
```

---

### 7.3 RPC message log

```sql
CREATE TABLE rpc_messages (
    id UUID PRIMARY KEY,
    job_id UUID REFERENCES jobs(id),
    device_id UUID NOT NULL,
    session_id UUID,
    direction TEXT NOT NULL,
    rpc_method TEXT NOT NULL,
    soap_message_id TEXT,
    payload XML,
    created_at TIMESTAMPTZ DEFAULT now()
);
```

Direction values:

```text
ACS_TO_CPE
CPE_TO_ACS
```

---

### 7.4 Firmware images table

```sql
CREATE TABLE firmware_images (
    id UUID PRIMARY KEY,
    vendor TEXT NOT NULL,
    model TEXT NOT NULL,
    version TEXT NOT NULL,
    channel TEXT NOT NULL,
    url TEXT NOT NULL,
    file_size_bytes BIGINT NOT NULL,
    sha256 TEXT NOT NULL,
    released_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ DEFAULT now(),
    UNIQUE (vendor, model, version, channel)
);
```

---

### 7.5 Parameter cache

Start with PostgreSQL JSONB:

```sql
CREATE TABLE device_parameter_cache (
    device_id UUID PRIMARY KEY REFERENCES devices(id),
    parameters JSONB NOT NULL DEFAULT '{}',
    updated_at TIMESTAMPTZ DEFAULT now()
);
```

Example:

```json
{
  "Device.DeviceInfo.SoftwareVersion": {
    "value": "2.3.1",
    "type": "string",
    "updated_at": "2026-08-04T10:15:00Z",
    "source": "INFORM"
  },
  "Device.WiFi.SSID.1.SSID": {
    "value": "CorpWiFi",
    "type": "string",
    "updated_at": "2026-08-04T10:15:00Z",
    "source": "GET_PARAMETER_VALUES"
  }
}
```

If parameter history is required:

```sql
CREATE TABLE parameter_history (
    id BIGSERIAL PRIMARY KEY,
    device_id UUID NOT NULL,
    parameter_path TEXT NOT NULL,
    value TEXT,
    source TEXT,
    session_id UUID,
    recorded_at TIMESTAMPTZ DEFAULT now()
);
```

For very large fleets, move high-volume telemetry to a dedicated log, document, or time-series store later.

---

## 8. REST API Design

All write endpoints should queue work and return `202 Accepted`.

### 8.1 Devices

```http
GET /api/v1/devices
```

Filters:

```text
?status=online
&vendor=Zyxel
&model=NR5103
&data_model=device2
&firmware_version=2.3.1
&page=1
&page_size=100
```

Example response:

```json
{
  "items": [
    {
      "id": "5b1c...",
      "oui_serial": "001122334455",
      "manufacturer": "Zyxel",
      "model_name": "NR5103",
      "software_version": "2.3.1",
      "data_model_root": "DEVICE2",
      "online_status": "ONLINE",
      "last_inform_at": "2026-08-04T09:55:00Z"
    }
  ],
  "page": 1,
  "page_size": 100,
  "total": 18432
}
```

---

### 8.2 Device detail

```http
GET /api/v1/devices/{id}
```

Example response:

```json
{
  "id": "5b1c...",
  "oui_serial": "001122334455",
  "manufacturer": "Zyxel",
  "model_name": "NR5103",
  "data_model_root": "DEVICE2",
  "online_status": "ONLINE",
  "last_inform_at": "2026-08-04T09:55:00Z",
  "connection_request_url": "https://10.20.30.40:7547/cwmp",
  "parameter_cache": {
    "Device.DeviceInfo.SoftwareVersion": {
      "value": "2.3.1",
      "updated_at": "2026-08-04T09:55:00Z"
    }
  }
}
```

---

### 8.3 Parameters

Read cached parameters:

```http
GET /api/v1/devices/{id}/parameters?paths=Device.DeviceInfo.SoftwareVersion
```

Queue parameter write:

```http
PUT /api/v1/devices/{id}/parameters
```

Body:

```json
{
  "parameters": [
    {
      "name": "Device.WiFi.SSID.1.SSID",
      "value": "CorpWiFi",
      "type": "string"
    }
  ]
}
```

Response:

```json
{
  "command_key": "setparam_20260804_0001",
  "status": "QUEUED"
}
```

---

### 8.4 Connection Request

```http
POST /api/v1/devices/{id}/connection-request
```

Body:

```json
{
  "timeout_seconds": 30
}
```

Response:

```json
{
  "command_key": "cr_20260804_0001",
  "status": "QUEUED"
}
```

---

### 8.5 Firmware

```http
POST /api/v1/devices/{id}/firmware
```

Body:

```json
{
  "firmware_profile_id": "zyxel-nr5103-2.3.1",
  "delay_seconds": 0
}
```

Response:

```json
{
  "command_key": "fw_20260804_0001",
  "status": "QUEUED"
}
```

Job status:

```http
GET /api/v1/jobs/{command_key}
```

Response:

```json
{
  "command_key": "fw_20260804_0001",
  "device_id": "5b1c...",
  "type": "FIRMWARE_DOWNLOAD",
  "status": "IN_PROGRESS",
  "created_at": "2026-08-04T10:00:00Z",
  "updated_at": "2026-08-04T10:02:11Z"
}
```

---

### 8.6 Reboot

```http
POST /api/v1/devices/{id}/reboot
```

Response:

```json
{
  "command_key": "reboot_20260804_0001",
  "status": "QUEUED"
}
```

---

### 8.7 Diagnostics

```http
POST /api/v1/devices/{id}/diagnostics
```

Body:

```json
{
  "type": "PING",
  "host": "8.8.8.8",
  "number_of_repetitions": 5
}
```

Response:

```json
{
  "command_key": "diag_20260804_0001",
  "status": "QUEUED"
}
```

---

## 9. Firmware OTA Design

### 9.1 Correct OTA flow

```text
Operator selects firmware profile via REST
ACS creates OTA job
ACS queues Download RPC
CPE opens session or is woken
ACS sends Download RPC
CPE fetches image from HTTPS URL
CPE sends TransferComplete
ACS updates job
ACS optionally reads SoftwareVersion
```

### 9.2 Download RPC

Example ACS-side RPC:

```text
Download(
  CommandKey="fw_20260804_0001",
  FileType="1 Firmware Upgrade Image",
  URL="https://firmware.example.com/zyxel/nr5103/v2.3.1.bin",
  Username="cpe-download",
  Password="********",
  FileSize=52428800,
  TargetFileName="firmware.bin",
  DelaySeconds=0
)
```

### 9.3 TransferComplete handling

```python
def on_transfer_complete(device_id, command_key, fault):
    if fault is None:
        job_store.update_status(command_key, "SUCCESS")

        queue_rpc(device_id, GetParameterValues([
            resolve_path(device_id, "device_info.software_version")
        ]))
    else:
        job_store.update_status(
            command_key,
            "FAILED",
            fault_code=fault.code,
            fault_string=fault.string,
        )
```

### 9.4 Firmware repository rules

Firmware images should be stored outside the main relational database.

Use:

```text
S3
MinIO
CDN
HTTPS static file host
```

Before publishing an image:

```text
1. Upload image to staging storage
2. Validate SHA256
3. Validate vendor/model compatibility metadata
4. Store metadata in PostgreSQL
5. Publish to HTTPS file endpoint
```

For fleet rollouts, add:

```text
firmware_rollout
firmware_rollout_device
```

Rollout states:

```text
PENDING
ELIGIBLE
QUEUED
DOWNLOADING
SUCCESS
FAILED
SKIPPED
BLOCKED
```

Rollout controls:

```text
canary_percentage
maintenance_window
maximum_failure_rate
rollback_version
model_filter
current_version_filter
```

---

## 10. Diagnostics Design

Use TR-181 diagnostics objects where available.

### 10.1 Ping

Object:

```text
Device.IP.Diagnostics.IPPing.
```

ACS writes:

```text
Host
NumberOfRepetitions
Timeout
DataBlockSize
DSCP
DiagnosticsState = Requested
```

ACS later reads:

```text
DiagnosticsState
SuccessCount
FailureCount
AverageResponseTime
MinimumResponseTime
MaximumResponseTime
```

---

### 10.2 Traceroute

Object:

```text
Device.IP.Diagnostics.TraceRoute.
```

Same request/poll pattern.

---

### 10.3 WiFi client stats

Use:

```text
Device.WiFi.AccessPoint.{i}.AssociatedDevice.{i}
```

Useful parameters:

```text
MACAddress
SignalStrength
AuthenticationState
LastDataDownlinkRate
LastDataUplinkRate
```

---

### 10.4 Active Notification

Where supported, use:

```text
SetParameterAttributes(Notification=2)
```

This tells the CPE to include changed parameters in future Inform messages.

Example:

```python
queue_rpc(device_id, SetParameterAttributes([
    {
        "Name": "Device.WiFi.AccessPoint.1.AssociatedDevice.1.SignalStrength",
        "Notification": 2,
    }
]))
```

This reduces polling for high-value metrics.

---

## 11. Security Design

### 11.1 Transport

All endpoints must use TLS.

```text
CWMP endpoint: HTTPS only
REST endpoint: HTTPS only
Firmware endpoint: HTTPS only
Connection Request: HTTPS where possible
```

Target:

```text
TLS 1.3
```

No plaintext CWMP in production.

---

### 11.2 CPE authentication

Preferred:

```text
Mutual TLS
```

Fallback:

```text
HTTP Digest authentication
```

Because fleet mTLS support is not confirmed, the implementation should support both and record per-device auth mode.

---

### 11.3 Secrets management

Do not store sensitive values in normal device or parameter tables.

Sensitive values include:

```text
Digest passwords
Firmware download credentials
Certificate private keys
Connection Request secrets
```

Use:

```text
Vault
AWS Secrets Manager
GCP Secret Manager
Azure Key Vault
```

Reference secrets by ID:

```json
{
  "auth_secret_ref": "vault:secret/data/cpe/001122334455"
}
```

---

### 11.4 Audit logging

Audit these events:

```text
SetParameterValues
Download
Upload
Reboot
FactoryReset
AddObject
DeleteObject
ConnectionRequest
OperatorLogin
RoleChange
FirmwarePublish
RolloutApproval
```

Example audit record:

```json
{
  "event_id": "audit_123",
  "timestamp": "2026-08-04T10:15:00Z",
  "operator_id": "user@example.com",
  "device_id": "5b1c...",
  "action": "SetParameterValues",
  "command_key": "setparam_20260804_0001",
  "before": {
    "Device.WiFi.SSID.1.SSID": "OldWiFi"
  },
  "after": {
    "Device.WiFi.SSID.1.SSID": "CorpWiFi"
  }
}
```

Audit storage should be append-only.

---

### 11.5 REST authentication

Use:

```text
OAuth2 / OIDC
JWT access tokens
Short-lived tokens
Refresh tokens
RBAC
```

Suggested roles:

```text
read_only
operator
fleet_admin
firmware_manager
auditor
```

---

## 12. Observability

### 12.1 Metrics

Important metrics:

```text
cwmp_inform_total
cwmp_inform_by_event_code
cwmp_session_open_total
cwmp_session_close_total
cwmp_rpc_sent_total
cwmp_rpc_fault_total
cwmp_transfer_complete_total
connection_request_total
connection_request_success_total
job_created_total
job_success_total
job_failure_total
device_online_count
device_offline_count
firmware_download_success_rate
periodic_inform_delay_seconds
```

### 12.2 Alerts

Important alerts:

```text
Fleet heartbeat loss
Sudden drop in Inform rate
High SOAP fault rate
High Download failure rate
Redis queue backlog growing
Connection Request failure spike
Database replication lag
Certificate expiry approaching
```

### 12.3 Tracing

Use OpenTelemetry to correlate:

```text
REST request
job creation
queue dispatch
CWMP RPC
TransferComplete
job completion
```

This is especially important for OTA jobs because start and completion may be separated by minutes or hours.

---

## 13. Fleet-Specific Considerations

### 13.1 Zyxel NR5103

Vendor documentation suggests TR-181/Device:2 support.

Verify on real firmware:

```text
Device. root
Connection Request behavior
Download support
Diagnostics object support
Vendor extensions
```

Expected paths:

```text
Device.DeviceInfo.SoftwareVersion
Device.ManagementServer.ConnectionRequestURL
Device.WiFi.SSID.{i}.SSID
```

---

### 13.2 Nokia FastMile

Vendor documentation suggests TR-069, TR-181, and possibly TR-369/USP.

For this platform, target TR-069 first.

Verify:

```text
CWMP amendment
mTLS or Digest behavior
Diagnostics object support
Vendor-specific parameter names
Firmware Download behavior
```

---

### 13.3 Teltonika RUTX/RUTM

TR-069 may be an installable package on RutOS and may not be factory-enabled.

Verify:

```text
Is TR-069 package installed?
Is it enabled?
Does it use Device. root?
Which parameters are writable?
Does firmware Download work reliably?
```

Teltonika may require more onboarding logic than the other vendors.

---

### 13.4 Huawei 5G CPE

Public documentation may not confirm Device:2 vs IGD:1.

Verify:

```text
Data model root
Available RPC methods
Connection Request behavior
CGNAT reachability
Firmware Download support
Vendor-specific parameters
```

Huawei behavior may differ by market, operator SKU, and firmware version.

---

## 14. Prerequisite Verification Before Production Build

Do not finalize production implementation until these items are confirmed on real devices.

### P1. Data model support

Question:

```text
Does each CPE model implement Device:2, IGD:1, or a mixed data model?
```

Verification:

```text
GetParameterNames against Device.
GetParameterNames against InternetGatewayDevice.
```

Gates:

```text
Data model layer
Root detection logic
REST parameter translation
```

---

### P2. CWMP Amendment

Question:

```text
Which TR-069 Amendment does each CPE model support?
```

Verification:

```text
Vendor conformance statement
GetRPCMethods on live device
```

Gates:

```text
RPC support matrix
OTA flow
Optional Amendment 4+ features
```

---

### P3. mTLS support

Question:

```text
Can each CPE model present a client certificate?
```

Verification:

```text
Vendor integration guide
Live TLS handshake test
Certificate enrollment workflow review
```

Fallback:

```text
HTTP Digest auth
```

Gates:

```text
Security infrastructure
Certificate issuance pipeline
Device onboarding
```

---

### P4. NAT topology

Question:

```text
Are CPEs directly reachable via ConnectionRequestURL?
```

Because these are cellular 5G CPEs, assume CGNAT unless the mobile operator confirms public/static IP APNs.

Verification:

```text
Network topology review
Carrier/APN confirmation
Live Connection Request tests
IPv6 reachability tests
```

Fallback:

```text
STUN/Annex G support
Periodic Inform fallback
```

Gates:

```text
Connection Request service
NAT traversal design
Operational reachability expectations
```

---

### P5. Third-party ACS license

Question:

```text
If using an existing ACS codebase, what license applies?
```

Verification:

```text
Check LICENSE file in the actual source repository
Do not rely on secondary summaries
```

Fallback:

```text
Do not embed/fork third-party ACS code until license is confirmed.
```

---

## 15. Phased Delivery Plan

### Phase 0: Lab Harness and Device Probes

Goal:

```text
Learn actual behavior of the fleet.
```

Build:

```text
Minimal CWMP listener
SOAP logger
Inform parser
GetRPCMethods client
GetParameterNames client
```

Capture:

```text
Inform structure
Event codes
Authentication behavior
Data model root
Parameter tree
RPC methods
Connection Request URL format
Firmware Download behavior
```

Deliverable:

```text
Device compatibility matrix
```

Example:

| Vendor/Model | Root | Amendment | mTLS | Digest | Download | Diagnostics | Notes |
|---|---:|---:|---:|---:|---:|---:|---|
| Zyxel NR5103 | Device.2 likely | TBD | TBD | likely | TBD | TBD | Needs live test |
| Nokia FastMile | Device.2 likely | TBD | TBD | likely | TBD | TBD | Needs live test |
| Teltonika RUTX/RUTM | Device.2 likely | TBD | TBD | likely | TBD | TBD | TR-069 package may need install |
| Huawei 5G CPE | TBD | TBD | TBD | likely | TBD | TBD | Needs live test |

---

### Phase 1: Minimal CWMP ACS

Goal:

```text
Safely accept CPE sessions.
```

Implement:

```text
HTTPS CWMP endpoint
Inform parsing
InformResponse
Device registry upsert
Session tracking
SOAP fault handling
Basic audit logging
```

Acceptance criteria:

```text
A CPE can send Inform.
ACS records device.
ACS responds correctly.
Session closes cleanly.
Device appears in REST API.
```

---

### Phase 2: Read/Write Parameters

Goal:

```text
Basic provisioning.
```

Implement:

```text
GetParameterValues
SetParameterValues
GetParameterNames
Pending RPC queue
CommandKey/job tracking
Parameter cache
REST read/write endpoints
```

Acceptance criteria:

```text
Operator can queue a parameter write.
Write is applied on next session.
Job status becomes SUCCESS.
Parameter cache updates.
SOAP faults are visible in job record.
```

---

### Phase 3: Connection Request

Goal:

```text
Wake devices outside periodic Inform.
```

Implement:

```text
ConnectionRequestURL storage
Digest-authenticated HTTP GET
Connection Request job
Wait-for-Inform logic
REST trigger endpoint
```

Acceptance criteria:

```text
POST /connection-request causes CPE to send Inform with EventCode 6.
API reports whether session opened within timeout.
Failures are recorded.
```

---

### Phase 4: Firmware OTA

Goal:

```text
Reliable firmware updates.
```

Implement:

```text
Firmware image repository
Checksum verification at ingestion
Download RPC job
TransferComplete handling
Post-update SoftwareVersion check
Rollout jobs
Maintenance windows
Failure rate throttling
```

Acceptance criteria:

```text
ACS can queue firmware update.
CPE downloads image from HTTPS URL.
TransferComplete updates job.
ACS confirms new SoftwareVersion.
Failed download creates FAILED job with fault.
```

---

### Phase 5: Diagnostics

Goal:

```text
Operational troubleshooting.
```

Implement:

```text
Ping
Traceroute
WiFi client stats
Diagnostics job model
WebSocket or polling for results
```

Acceptance criteria:

```text
Operator can request ping.
CPE executes diagnostics object.
ACS retrieves result.
Result is tied to command_key.
```

---

### Phase 6: Security Hardening

Goal:

```text
Production-ready security.
```

Implement:

```text
mTLS where supported
Digest fallback
Certificate rotation
Secrets manager integration
RBAC
Audit log completeness
TLS policy enforcement
Rate limiting
SOAP/XML hardening
```

---

### Phase 7: Fleet Operations

Goal:

```text
Manage large numbers of devices.
```

Implement:

```text
Device groups
Tags
Bulk operations
Canary rollouts
Policy engine
Scheduled jobs
Dashboards
Alerting
```

---

## 16. Example Repository Layout

### Go-oriented layout

```text
/cmd
  acs/
    main.go
  api/
    main.go
  worker/
    main.go

/internal
  cwmp/
    soap.go
    inform.go
    rpc.go
    fault.go
    xml.go
    session.go
  auth/
    mtls.go
    digest.go
    jwt.go
  devices/
    device.go
    repository.go
    adapter.go
    adapters/
      tr181.go
      tr098.go
      huawei.go
      nokia.go
      teltonika.go
      zyxel.go
  jobs/
    job.go
    queue.go
    worker.go
    retry.go
  firmware/
    image.go
    repository.go
    download.go
  api/
    devices.go
    jobs.go
    firmware.go
    diagnostics.go
    middleware.go
  observability/
    metrics.go
    tracing.go
    audit.go
  store/
    postgres.go
    redis.go
  config/
    config.go
```

### Python-oriented layout

```text
/app
  main.py
  api/
    devices.py
    jobs.py
    firmware.py
    diagnostics.py
  cwmp/
    server.py
    soap.py
    inform.py
    rpc.py
    fault.py
  devices/
    models.py
    adapters.py
    tr181.py
    tr098.py
    vendors/
      huawei.py
      nokia.py
      teltonika.py
      zyxel.py
  jobs/
    models.py
    queue.py
    worker.py
  firmware/
    models.py
    service.py
  security/
    mtls.py
    digest.py
    audit.py
  db/
    models.py
    migrations/
```

---

## 17. Testing Strategy

### 17.1 Unit tests

Test:

```text
SOAP parsing
Inform parsing
Fault parsing
CommandKey generation
Data model path mapping
Job state transitions
```

---

### 17.2 Protocol tests

Use golden XML fixtures for:

```text
Inform
InformResponse
GetParameterValues
SetParameterValues
GetParameterNames
Download
DownloadResponse
TransferComplete
Fault
```

---

### 17.3 Mock CPE emulator

Build a mock CPE that can simulate:

```text
Boot Inform
Periodic Inform
Connection Request wake
GetParameterValues response
SetParameterValues response
Download acceptance
TransferComplete success
TransferComplete failure
SOAP faults
```

This allows development before real devices are available.

---

### 17.4 Real-device lab tests

For each vendor model:

```text
Onboard device
Read DeviceInfo
Read WiFi parameters
Set one safe parameter
Trigger Connection Request
Run ping diagnostics
Perform firmware Download
Reboot
Factory reset in isolated lab
```

---

### 17.5 Chaos tests

Test:

```text
CPE disconnects mid-session
SOAP timeout
Duplicate Inform
Repeated empty requests
Invalid auth
Oversized XML
Malformed XML
Redis outage
Postgres failover
Firmware URL unreachable
TransferComplete with unknown CommandKey
```

---

## 18. MVP Definition

### Must have

```text
1. HTTPS CWMP endpoint
2. Inform handling
3. Device registry
4. Session state tracking
5. GetParameterValues
6. SetParameterValues
7. Pending RPC queue
8. REST device API
9. REST job API
10. Audit log
11. Digest authentication
12. Firmware Download via URL
13. TransferComplete handling
14. Basic Prometheus metrics
```

### Should have

```text
1. Connection Request
2. WebSocket job updates
3. Diagnostics ping/traceroute
4. Dual TR-181/TR-098 support
5. Vendor adapters
6. Bulk operations
7. mTLS support
```

### Later

```text
1. Full rollout/canary engine
2. Advanced policy engine
3. Full STUN/Annex G NAT traversal
4. TR-369/USP bridge
5. Multi-tenant SaaS features
6. Advanced device telemetry analytics
```

---

## 19. Common Pitfalls to Avoid

### 19.1 Do not assume synchronous CPE reachability

Bad:

```text
REST PUT parameter -> immediately call CPE -> return result
```

Correct:

```text
REST PUT parameter -> create job -> return 202 -> execute when session exists
```

---

### 19.2 Do not hardcode one data model

Bad:

```text
Device.DeviceInfo.SoftwareVersion everywhere
```

Correct:

```text
Canonical parameter -> adapter -> actual CPE path
```

---

### 19.3 Do not store firmware in database rows

Bad:

```text
SetParameterValues(Device.Flash.ImageData, firmware.hex())
```

Correct:

```text
Download(URL)
```

---

### 19.4 Do not forget TransferComplete correlation

Use:

```text
CommandKey
```

Without it, OTA jobs become hard to track.

---

### 19.5 Do not rely only on Connection Request

For cellular CPEs behind CGNAT, Connection Request may not work directly.

Always have:

```text
Periodic Inform fallback
```

---

### 19.6 Do not trust cached parameters as live state

Parameter cache should show freshness:

```json
{
  "value": "CorpWiFi",
  "as_of": "2026-08-04T08:00:00Z",
  "source": "INFORM"
}
```

---

### 19.7 Do not ignore SOAP faults

Common fault causes:

```text
Invalid parameter name
Invalid parameter value
Read-only parameter
Resource exceeded
Internal error
```

Every fault should map to a job failure with readable operator context.

---

## 20. Recommended First Actions

If starting now, do this in order:

### Step 1: Build a SOAP/CWMP spike

Goal:

```text
Receive Inform from one real CPE
Log it
Respond with InformResponse
```

No database needed initially.

---

### Step 2: Add device persistence

Store:

```text
OUI
SerialNumber
ProductClass
EventCodes
Last Inform time
ConnectionRequestURL
```

---

### Step 3: Add pending job queue

Implement:

```text
SetParameterValues queued by REST
Applied on next Inform/session
```

---

### Step 4: Add Connection Request

Implement:

```text
Wake device
Wait for Inform
```

---

### Step 5: Add OTA

Implement:

```text
Firmware metadata
Download RPC
TransferComplete
Post-update version check
```

---

### Step 6: Harden security and operations

Add:

```text
mTLS/Digest auth
secrets manager
audit
metrics
tracing
alerting
```

---

## 21. Build vs Fork Decision

### Option A: Build the core ACS in-house

Pros:

```text
Full control
Cleaner integration with internal REST/API
No third-party license risk
Can optimize for your specific fleet
```

Cons:

```text
CWMP is fiddly
SOAP/XML handling takes effort
Need protocol expertise
Longer time to first working version
```

Choose this if:

```text
This is a strategic enterprise platform
You need custom workflows
You have engineering capacity
You need strict compliance/audit control
```

---

### Option B: Use an existing ACS engine

Pros:

```text
Faster initial deployment
Mature protocol handling
Existing CPE compatibility
```

Cons:

```text
License risk
Operational coupling
May need adaptation for enterprise REST layer
May not fit your job/rollout model
```

If using an existing ACS, still build:

```text
device lifecycle API
firmware rollout engine
audit layer
RBAC
fleet policy engine
vendor adapters
```

Do not embed or fork any third-party ACS until its license is verified directly from the source repository.

---

## 22. Final Design Principle

The system should be designed around:

> Queued asynchronous device operations.

TR-069 CPEs are not always reachable and cannot be treated like ordinary HTTP microservices.

The final implementation should enforce:

```text
1. CWMP endpoint handles SOAP sessions only.
2. REST API only queues jobs.
3. Jobs execute when the CPE has an open session or after Connection Request.
4. Firmware updates use Download(), not parameter writes.
5. Device identity is OUI + SerialNumber.
6. Data model root is detected per device.
7. Vendor differences are isolated in adapters.
8. All write RPCs are audited.
9. TLS is mandatory.
10. mTLS is the target, Digest is fallback.
11. Redis tracks sessions and queues.
12. PostgreSQL tracks durable state.
13. Firmware is served over HTTPS from a separate file store.
14. Observability is built in from the beginning.
15. Real-device lab verification gates production rollout.
```