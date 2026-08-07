# Enterprise TR-069 ACS Platform — Application Design v3

Status: Draft v3  
Date: 2026-08-04  
Replaces: v2 application design  
Scope: Implementation-ready design for an enterprise TR-069 / CWMP ACS platform managing Huawei 5G CPE, Nokia FastMile, Teltonika 5G CPE, and Zyxel 5G CPE devices.

---

## 0. v3 Change Log

This version incorporates the technical review feedback and corrects several protocol-state and operational gaps.

Major changes from v2:

1. **Corrected RPC dispatch model**
   - RPCs inside a CWMP session are now explicitly modeled as serial request/response operations.
   - The ACS must not fire multiple RPCs without waiting for the CPE response.
   - The design now uses a single in-flight RPC per session.

2. **Added session timeout and stale-session handling**
   - Added explicit session states for timeout, abort, and recovery.
   - Added timer definitions for Inform response, empty request, RPC response, session idle, and TransferComplete waiting.

3. **Clarified SOAP payload storage**
   - PostgreSQL has a native `xml` type, but for operational querying and API exposure, this design now recommends:
     - raw SOAP XML stored as `TEXT` or `XML`
     - parsed RPC content stored as `JSONB`
   - Preferred operational pattern: `payload_xml TEXT + payload_json JSONB`.

4. **Added credential rotation model**
   - Separates CPE-to-ACS credentials, ACS-to-CPE Connection Request credentials, firmware transfer credentials, API credentials, and service-to-service credentials.
   - Adds credential versioning and grace-period rotation.
   - Explicitly notes that CPE-to-ACS Digest credential rotation is device-capability-dependent.

5. **Improved CGNAT / Connection Request reachability model**
   - Because the fleet is cellular 5G CPE, CGNAT should be assumed unless the operator confirms public/static IP APNs.
   - Adds device reachability modes:
     - `DIRECT_IPV4`
     - `DIRECT_IPV6`
     - `STUN_ANNEX_G`
     - `PERIODIC_FALLBACK_ONLY`
     - `UNKNOWN`

6. **Tightened Phase 0**
   - Phase 0 is now explicitly a lab/probe harness, not a partial production ACS.
   - Its purpose is to verify real device behavior before building OTA, diagnostics, and fleet rollout features.

7. **Aligned with prerequisite document**
   - This design remains gated by unresolved items P1–P5:
     - P1: Device:2 vs IGD:1 data model support
     - P2: CWMP Amendment support
     - P3: mTLS support
     - P4: NAT/CGNAT and Connection Request reachability
     - P5: Third-party ACS license verification

---

## 1. Executive Summary

The platform should be built as an **event-driven TR-069/CWMP ACS**, not as a synchronous CRUD application.

The core architectural rule is:

> REST write operations queue work.  
> CWMP sessions execute work.  
> Connection Request only attempts to wake the CPE.  
> Firmware transfer happens CPE-side using `Download()`.

The system must assume:

```text
CPEs are not always reachable.
Sessions are CPE-initiated.
RPCs are asynchronous from the operator's perspective.
Firmware transfer is CPE-pull, not ACS-push.
Data model roots may differ per device.
CGNAT may prevent direct Connection Request.
Vendor behavior must be verified on real units.
```

The application should therefore consist of:

1. **CWMP Gateway**
   - HTTPS/SOAP endpoint for CPE sessions.
   - Handles `Inform`, RPC responses, `TransferComplete`, faults.

2. **Session State Machine**
   - Tracks each CWMP session.
   - Enforces serial RPC dispatch.
   - Handles timeouts and stale sessions.

3. **Job Orchestrator**
   - Durable queue of ACS-initiated operations.
   - Uses `CommandKey` for correlation.
   - Handles retry, timeout, and audit.

4. **REST API Gateway**
   - Queues work.
   - Returns `202 Accepted`.
   - Exposes job status and device state.

5. **Device/Data Model Adapter Layer**
   - Supports TR-181 Device:2 and legacy TR-098 IGD:1.
   - Isolates vendor-specific behavior.

6. **Firmware Repository**
   - Stores firmware images separately from the main database.
   - Serves images over HTTPS.
   - Uses checksum verification at ingestion.

7. **Security Layer**
   - TLS everywhere.
   - mTLS target, Digest fallback.
   - Secrets manager.
   - Credential versioning and rotation.
   - Append-only audit log.

8. **Observability Layer**
   - Metrics, tracing, alerting.
   - Correlation across async CWMP operations.

---

## 2. Governing Protocol Rules

### 2.1 Correct protocol documents

The platform must be built against:

| Topic | Correct reference |
|---|---|
| CWMP protocol | TR-069 |
| Legacy data model | TR-098, `InternetGatewayDevice.` root |
| Current data model | TR-181 Issue 2, `Device.` root / Device:2 |

Do not build against TR-068 or TR-179.

The design reference may be TR-069 Amendment 6, but the deployed CPE fleet’s actual amendment support is still unverified. Therefore:

> The ACS must perform runtime capability detection using `GetRPCMethods` and must not assume Amendment 4+ or Amendment 6-only RPCs are universally available.

---

### 2.2 Session initiation

The CPE opens a CWMP session by sending `Inform`.

Typical Inform event codes:

```text
0  BOOTSTRAP
1  BOOT
2  PERIODIC
4  VALUE CHANGE
6  CONNECTION REQUEST
```

The ACS may attempt to wake a CPE using Connection Request:

```text
ACS -> CPE: HTTP GET to ConnectionRequestURL
CPE -> ACS: New Inform session, ideally EventCode 6
```

Connection Request does not carry provisioning payload.

---

### 2.3 RPC execution is serial inside a session

A CWMP session must be treated as a state machine.

Correct model:

```text
ACS sends one RPC.
ACS waits for the CPE response.
ACS processes the response or fault.
Only then does ACS send the next RPC.
```

Incorrect model:

```text
ACS pops all queued RPCs and sends them without waiting.
```

The v3 design enforces:

```text
One in-flight RPC per session.
```

---

### 2.4 Firmware updates use Download()

Correct OTA flow:

```text
ACS sends Download(FileType, URL, FileSize, Credentials, ...)
CPE fetches firmware over HTTP/HTTPS/FTP
CPE flashes and reboots if required
CPE sends TransferComplete
ACS optionally verifies SoftwareVersion
```

Never send firmware bytes inside SOAP parameter writes.

---

### 2.5 Security target

For a new enterprise platform:

```text
Target: mutual TLS (mTLS)
Fallback: HTTP Digest authentication
```

However, prerequisite P3 is unresolved. Therefore the implementation must support:

```text
MTLS
DIGEST
UNKNOWN
BOOTSTRAP_ENROLLMENT
```

and must not make mTLS mandatory until confirmed on real devices.

---

## 3. Recommended Technology Stack

| Layer | Recommended choice | Reason |
|---|---|---|
| Core CWMP gateway | Go | Strong TLS, HTTP, concurrency, simple deployment |
| REST API | Go, or Python/FastAPI if team prefers | One language reduces operational complexity |
| Primary database | PostgreSQL | Devices, jobs, credentials, audit, firmware metadata |
| Cache/session store | Redis | Active sessions, dispatch locks, pub/sub |
| Async queue | RabbitMQ or Kafka | Bulk provisioning, OTA rollouts, retries |
| Firmware storage | S3-compatible storage / CDN / HTTPS static host | CPE pulls firmware image directly |
| Metrics | Prometheus + Grafana | Fleet health, session throughput, failure rates |
| Tracing | OpenTelemetry | Correlate RPC request and later TransferComplete |
| Secrets | Vault / AWS Secrets Manager / GCP Secret Manager | Device secrets, firmware transfer credentials, keys |

Production-oriented stack:

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

Prototype-oriented stack:

```text
Python/FastAPI
SQLAlchemy
PostgreSQL
Redis
Celery/RQ
S3-compatible storage
```

The protocol/state machine design matters more than language choice.

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
                   │ devices/jobs/   │                        │ sessions/locks/ │
                   │ audit/firmware  │                        │ pubsub/cache    │
                   └─────────────────┘                        └─────────────────┘

                   ┌─────────────────┐
                   │ Firmware/Config │
                   │ HTTPS file host │
                   └─────────────────┘
```

Key rule:

> REST write operations do not directly talk to the CPE.  
> They create durable jobs. The CWMP session worker executes those jobs when a session is available.

---

## 5. CWMP Session State Machine

This is the corrected core of the platform.

### 5.1 Session states

```text
IDLE
INFORM_RECEIVED
INFORM_RESPONSE_SENT
WAITING_FOR_EMPTY_REQUEST
READY_FOR_RPC_DISPATCH
RPC_DISPATCHED
AWAITING_RPC_RESPONSE
RPC_RESPONSE_RECEIVED
RPC_FAULT_RECEIVED
SESSION_CLOSING
SESSION_CLOSED
SESSION_TIMEOUT
SESSION_ABORTED
```

Optional operational state:

```text
AWAITING_TRANSFER_COMPLETE
```

This is better modeled as a job state than a session state because `TransferComplete` may arrive in a later session.

---

### 5.2 Session timers

| Timer | Purpose | Suggested default |
|---|---|---:|
| `inform_response_timeout` | Time allowed to respond to Inform | 10 seconds |
| `empty_request_timeout` | Time to wait for next CPE request after InformResponse | 30–60 seconds |
| `rpc_response_timeout` | Time to wait for RPC response | 30–120 seconds |
| `session_idle_timeout` | Maximum idle time for an open session | 300 seconds |
| `transfer_complete_timeout` | Time allowed for CPE to complete Download | minutes to hours, depending on firmware size |
| `connection_request_inform_wait_timeout` | Time to wait for Inform after Connection Request | 30–120 seconds |

Timeouts should be configurable per device class or vendor adapter.

---

### 5.3 State transitions

Simplified transition model:

```text
IDLE
  -> INFORM_RECEIVED
  -> INFORM_RESPONSE_SENT
  -> WAITING_FOR_EMPTY_REQUEST
  -> READY_FOR_RPC_DISPATCH
  -> RPC_DISPATCHED
  -> AWAITING_RPC_RESPONSE
  -> RPC_RESPONSE_RECEIVED
  -> READY_FOR_RPC_DISPATCH
  -> SESSION_CLOSING
  -> SESSION_CLOSED
```

Failure transitions:

```text
AWAITING_RPC_RESPONSE
  -> SESSION_TIMEOUT
  -> SESSION_ABORTED

RPC_RESPONSE_RECEIVED with FaultStruct
  -> RPC_FAULT_RECEIVED
  -> READY_FOR_RPC_DISPATCH or SESSION_ABORTED
```

Special cases:

```text
Reboot() acknowledged:
  CPE may reset immediately.
  Do not treat abrupt session loss as a fatal error if RebootResponse was received.

Download() acknowledged:
  Transfer may continue outside the session.
  Job enters AWAITING_TRANSFER_COMPLETE.
  TransferComplete may arrive later.
```

---

### 5.4 Serial RPC dispatch pseudocode

```python
def on_inform(device_id, inform_message):
    device = authenticate_device(inform_message)
    session = open_session(device_id)

    session.state = "INFORM_RECEIVED"
    store_inform_event(session, inform_message)

    response = render_inform_response(session)
    session.state = "INFORM_RESPONSE_SENT"

    return response


def on_empty_request(session):
    session.state = "READY_FOR_RPC_DISPATCH"
    dispatch_next_rpc(session)


def dispatch_next_rpc(session):
    if session.in_flight_rpc is not None:
        # Never send another RPC while one is awaiting response.
        return

    rpc = peek_next_pending_rpc(session.device_id)

    if rpc is None:
        close_session(session, reason="NO_PENDING_RPCS")
        return

    session.in_flight_rpc = rpc
    session.state = "RPC_DISPATCHED"
    lease_rpc(rpc)

    send_rpc_to_cpe(session, rpc)
    session.state = "AWAITING_RPC_RESPONSE"


def on_rpc_response(session, response):
    rpc = session.in_flight_rpc

    if response.is_fault():
        session.state = "RPC_FAULT_RECEIVED"
        handle_rpc_fault(session, rpc, response)
        fail_or_retry_rpc(rpc, response)
    else:
        session.state = "RPC_RESPONSE_RECEIVED"
        complete_rpc(session, rpc, response)

    session.in_flight_rpc = None
    session.state = "READY_FOR_RPC_DISPATCH"

    dispatch_next_rpc(session)


def on_timeout(session):
    if session.state == "AWAITING_RPC_RESPONSE":
        rpc = session.in_flight_rpc
        mark_rpc_timeout(rpc)
        fail_or_retry_rpc(rpc, reason="RPC_RESPONSE_TIMEOUT")

    session.state = "SESSION_TIMEOUT"
    close_session(session, reason="TIMEOUT")
```

The important behavior is:

```text
Peek next RPC.
Dispatch one RPC.
Wait for response.
Complete or fail that RPC.
Then dispatch the next RPC.
```

Do not do this:

```python
while rpc := redis.lpop(queue):
    send_rpc(rpc)
```

That can mislead implementers into firing RPCs without waiting for CPE responses.

---

### 5.5 Durable job queue versus Redis dispatch queue

PostgreSQL should be the durable source of truth for jobs.

Redis can be used for fast dispatch, but it should not be the only record of pending work.

Recommended model:

```text
PostgreSQL:
  jobs table is authoritative

Redis:
  device:{device_id}:dispatch_queue
  device:{device_id}:in_flight_rpc
  session:{device_id}
```

When a session opens:

```text
1. Load QUEUED jobs for device from PostgreSQL.
2. Optionally mirror them into Redis dispatch queue.
3. Peek one job.
4. Lease it.
5. Send RPC.
6. Update PostgreSQL after response.
```

If the ACS process restarts:

```text
1. Recover active sessions from Redis/PostgreSQL.
2. Mark expired in-flight RPCs as TIMEOUT.
3. Requeue retryable jobs.
4. Fail non-retryable jobs.
```

---

## 6. Device Identity and Data Model Strategy

### 6.1 Device natural key

External CWMP identity:

```text
OUI + SerialNumber
```

or, where needed:

```text
OUI + ProductClass + SerialNumber
```

Internal primary key:

```text
device.id UUID
```

---

### 6.2 Data model roots

The platform must support:

```text
TR-181 Device:2 root:
  Device.

TR-098 IGD:1 root:
  InternetGatewayDevice.
```

Do not assume all devices use the same root.

Store:

```text
data_model_root = DEVICE2 | IGD1 | UNKNOWN
```

Detect root during:

```text
First Inform
GetParameterNames
GetRPCMethods
Initial capability probe
```

---

### 6.3 Canonical parameter mapping

Do not hardcode TR-181 paths everywhere.

Use canonical names:

```python
class CanonicalParameter:
    DEVICE_INFO_SERIAL_NUMBER = "device_info.serial_number"
    DEVICE_INFO_SOFTWARE_VERSION = "device_info.software_version"
    MANAGEMENT_SERVER_CONNECTION_REQUEST_URL = "management_server.connection_request_url"
    WIFI_SSID = "wifi.ssid"
    WIFI_ASSOCIATED_CLIENTS = "wifi.associated_clients"
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

### 6.4 Vendor adapters

Vendor differences should be isolated in adapters.

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
Resolve canonical parameter paths
Detect unsupported objects
Normalize diagnostics paths
Handle vendor extensions
Handle firmware profile quirks
Interpret vendor-specific fault behavior
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

### 7.1 Devices

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
    auth_mode TEXT CHECK (auth_mode IN ('MTLS', 'DIGEST', 'UNKNOWN')),
    online_status TEXT CHECK (online_status IN ('ONLINE', 'OFFLINE', 'UNREACHABLE')),
    connection_request_url TEXT,
    connection_request_mode TEXT CHECK (connection_request_mode IN (
        'DIRECT_IPV4',
        'DIRECT_IPV6',
        'STUN_ANNEX_G',
        'PERIODIC_FALLBACK_ONLY',
        'UNKNOWN'
    )),
    last_connection_request_at TIMESTAMPTZ,
    last_connection_request_status TEXT,
    last_inform_after_connection_request_at TIMESTAMPTZ,
    periodic_inform_enable BOOLEAN,
    periodic_inform_interval INTEGER,
    last_inform_at TIMESTAMPTZ,
    last_inform_event_codes TEXT[],
    first_seen_at TIMESTAMPTZ DEFAULT now(),
    last_updated_at TIMESTAMPTZ DEFAULT now()
);
```

---

### 7.2 Device credentials

```sql
CREATE TABLE device_credentials (
    id UUID PRIMARY KEY,
    device_id UUID NOT NULL REFERENCES devices(id),
    credential_type TEXT NOT NULL CHECK (credential_type IN (
        'CPE_TO_ACS_DIGEST',
        'ACS_TO_CPE_CONNECTION_REQUEST',
        'FIRMWARE_TRANSFER',
        'MTLS_CERT'
    )),
    credential_version INTEGER NOT NULL,
    secret_ref TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN (
        'PENDING',
        'ACTIVE',
        'GRACE',
        'REVOKED',
        'EXPIRED'
    )),
    activated_at TIMESTAMPTZ,
    expires_at TIMESTAMPTZ,
    rotated_at TIMESTAMPTZ,
    rotation_reason TEXT,
    created_at TIMESTAMPTZ DEFAULT now(),
    UNIQUE (device_id, credential_type, credential_version)
);
```

Do not store actual secrets in this table. Store references to a secrets manager.

---

### 7.3 Sessions

```sql
CREATE TABLE cwmp_sessions (
    id UUID PRIMARY KEY,
    device_id UUID NOT NULL REFERENCES devices(id),
    state TEXT NOT NULL,
    inform_event_codes TEXT[],
    opened_at TIMESTAMPTZ DEFAULT now(),
    last_activity_at TIMESTAMPTZ DEFAULT now(),
    timeout_at TIMESTAMPTZ,
    closed_at TIMESTAMPTZ,
    close_reason TEXT
);
```

Active session state can be mirrored in Redis for fast access.

---

### 7.4 Jobs

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
    timeout_at TIMESTAMPTZ,
    fault_code TEXT,
    fault_string TEXT,
    lease_owner TEXT,
    lease_expires_at TIMESTAMPTZ
);
```

Job types:

```text
GET_PARAMETER
SET_PARAMETER
GET_PARAMETER_NAMES
GET_RPC_METHODS
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
LEASED
RPC_SENT
RPC_ACKED
IN_PROGRESS
AWAITING_TRANSFER_COMPLETE
SUCCESS
FAILED
TIMEOUT
CANCELLED
```

---

### 7.5 RPC message log

Recommended operational schema:

```sql
CREATE TABLE rpc_messages (
    id UUID PRIMARY KEY,
    job_id UUID REFERENCES jobs(id),
    device_id UUID NOT NULL,
    session_id UUID REFERENCES cwmp_sessions(id),
    direction TEXT NOT NULL CHECK (direction IN ('ACS_TO_CPE', 'CPE_TO_ACS')),
    rpc_method TEXT NOT NULL,
    soap_message_id TEXT,
    payload_xml TEXT NOT NULL,
    payload_json JSONB,
    fault_json JSONB,
    created_at TIMESTAMPTZ DEFAULT now()
);
```

Why `TEXT + JSONB`?

```text
TEXT preserves the raw SOAP/XML payload.
JSONB allows efficient querying by parsed fields.
JSONB is easier to expose through REST APIs and dashboards.
XML is useful for protocol fidelity but less ergonomic for operational querying.
```

If strict XML validation or XPath querying is required, use:

```sql
payload_xml XML NOT NULL
```

But for most ACS operational needs, `TEXT + JSONB` is preferable.

---

### 7.6 Firmware images

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

### 7.7 Parameter cache

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

For parameter history:

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

For high-volume fleets, partition history tables or move telemetry to a dedicated log/document/time-series store.

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
  "connection_request_mode": "UNKNOWN",
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
  "timeout_seconds": 60
}
```

Response:

```json
{
  "command_key": "cr_20260804_0001",
  "status": "QUEUED",
  "reachability": {
    "connection_request_mode": "UNKNOWN",
    "recommendation": "Device may rely on periodic Inform until reachability is verified."
  }
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

Example response:

```json
{
  "command_key": "fw_20260804_0001",
  "device_id": "5b1c...",
  "type": "FIRMWARE_DOWNLOAD",
  "status": "AWAITING_TRANSFER_COMPLETE",
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
CPE acknowledges Download
CPE fetches image from HTTPS URL
CPE sends TransferComplete
ACS updates job
ACS optionally reads SoftwareVersion
```

---

### 9.2 Download RPC

Example:

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

Important:

> A successful `DownloadResponse` means the CPE accepted the Download request.  
> It does not mean the firmware transfer has completed.

The job should move to:

```text
AWAITING_TRANSFER_COMPLETE
```

---

### 9.3 TransferComplete handling

```python
def on_transfer_complete(device_id, command_key, fault):
    if fault is None:
        update_job(command_key, status="SUCCESS")

        queue_rpc(device_id, GetParameterValues([
            resolve_path(device_id, "device_info.software_version")
        ]))
    else:
        update_job(
            command_key,
            status="FAILED",
            fault_code=fault.code,
            fault_string=fault.string,
        )
```

---

### 9.4 Firmware repository

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

Firmware endpoint considerations:

```text
HTTPS required
Range requests recommended
Signed URLs where supported
Basic auth if CPE supports it
Avoid custom HTTP headers unless CPE support is confirmed
```

---

### 9.5 Rollout controls

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

### 10.1 Ping

Use:

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

Use:

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

Because prerequisite P3 is unresolved, the implementation must support both and record per-device auth mode.

---

### 11.3 Credential classes

The design must separate these credential types:

```text
1. CPE-to-ACS Digest credentials
2. ACS-to-CPE Connection Request credentials
3. Firmware transfer credentials
4. REST/API operator credentials
5. Internal service-to-service credentials
```

Each class has different rotation behavior.

---

### 11.4 Credential rotation policy

| Credential type | Rotation owner | Rotation method | Notes |
|---|---|---|---|
| CPE-to-ACS Digest | Device capability dependent | Manual reprovisioning or vendor-specific remote update | Do not assume remote rotation is possible |
| ACS-to-CPE Connection Request | ACS platform | Remote parameter update if supported | Use canary rotation and grace period |
| Firmware transfer | Firmware service | Short-lived signed URLs or per-campaign credentials | Prefer temporary credentials |
| REST/API operator | Identity provider | OIDC/JWT rotation, password/MFA policies | Short-lived tokens |
| Service-to-service | Platform PKI | Short-lived certificates/JWT/SPIFFE-style identity | Automate rotation |

---

### 11.5 CPE-to-ACS Digest rotation limitations

Rotating the credential the CPE uses to authenticate to the ACS may require:

```text
Vendor-specific provisioning parameters
Bootstrap/re-enrollment
Manual device reprovisioning
Factory-provisioned identity
```

Therefore:

> Do not assume automatic remote rotation of CPE-to-ACS Digest credentials for all fleet models.

Add this as a prerequisite question for each vendor:

```text
Can the CPE-to-ACS HTTP Digest credential be updated remotely?
If yes, by which standard or vendor-specific parameter?
Is there a graceful dual-credential period?
```

---

### 11.6 ACS-to-CPE Connection Request credential rotation

Possible rotation flow:

```text
1. Generate new credential version in Vault.
2. Queue SetParameterValues for ConnectionRequestUsername/Password or vendor equivalent.
3. Confirm RPC success.
4. Switch ACS Connection Request client to new credential.
5. Keep old credential valid for grace period if rollback is possible.
6. Audit old credential revocation.
```

Do not rotate all devices at once.

Use:

```text
Canary group
Maintenance window
Automatic rollback on failure
Audit trail
```

---

### 11.7 Secrets storage

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

### 11.8 Audit logging

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
CredentialRotation
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

Secret values must be masked in audit logs.

---

## 12. Connection Request, NAT, and CGNAT

### 12.1 Assumption for this fleet

The fleet consists of cellular 5G CPEs.

Therefore:

> Assume carrier-grade NAT (CGNAT) is present unless the mobile operator confirms public/static IP APNs.

This means Connection Request may not work directly for many devices.

---

### 12.2 Reachability modes

Store a reachability mode per device:

```text
DIRECT_IPV4
DIRECT_IPV6
STUN_ANNEX_G
PERIODIC_FALLBACK_ONLY
UNKNOWN
```

---

### 12.3 Connection Request result tracking

Track:

```text
last_connection_request_at
last_connection_request_status
last_connection_request_failure_reason
last_inform_after_connection_request_at
connection_request_success_rate
```

Example statuses:

```text
HTTP_200_NO_INFORM
HTTP_200_INFORM_RECEIVED
HTTP_401
HTTP_404
TIMEOUT
DNS_FAILURE
TCP_FAILURE
TLS_FAILURE
UNREACHABLE
```

---

### 12.4 Fallback behavior

If Connection Request fails:

```text
Queue remains pending.
Job executes on next periodic Inform.
REST API should not claim immediate reachability.
```

If a device is repeatedly unreachable:

```text
Mark online_status = UNREACHABLE
Raise operational alert if fleet-wide pattern appears
Recommend APN/topology review
```

---

### 12.5 STUN / Annex G

If CGNAT is confirmed and direct Connection Request fails, evaluate:

```text
TR-069 Annex G / STUN-based NAT traversal
IPv6 reachability
Operator-provided public APN
Shorter periodic Inform intervals for critical devices
```

Do not retrofit STUN support after assuming direct reachability.

---

## 13. Prerequisite Verification Gates

This design remains gated by the prerequisite document.

### P1. CPE Data Model Support

Question:

```text
Does each CPE model implement Device:2, IGD:1, or a mixed data model?
```

Verification:

```text
GetParameterNames against Device.
GetParameterNames against InternetGatewayDevice.
Vendor conformance documentation.
```

Gates:

```text
Data model layer
Root detection logic
REST parameter translation
```

---

### P2. CWMP Amendment Version

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

### P3. mTLS Support

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

### P4. NAT Topology / STUN Requirement

Question:

```text
Are CPEs directly reachable via ConnectionRequestURL?
```

Verification:

```text
Network topology review
Carrier/APN confirmation
Live Connection Request tests
IPv6 reachability tests
```

Provisional decision:

```text
Assume CGNAT present.
Build STUN/Annex G support if direct reachability is not confirmed.
```

Gates:

```text
Connection Request service
NAT traversal design
Operational reachability expectations
```

---

### P5. Third-Party ACS Codebase License

Question:

```text
If using an existing ACS codebase, what license applies?
```

Verification:

```text
Check LICENSE file in the actual source repository.
Do not rely on secondary summaries.
```

Fallback:

```text
Do not embed/fork third-party ACS code until license is confirmed.
```

---

### Additional v3 open item: credential rotation capability

This should be added to vendor verification:

```text
Can CPE-to-ACS Digest credentials be rotated remotely?
Can ACS-to-CPE Connection Request credentials be rotated remotely?
Does the CPE support grace-period dual credentials?
```

This is especially important for long-lived Digest deployments.

---

## 14. Phased Delivery Plan

### Phase 0: Lab Harness and Device Probes

Goal:

```text
Learn actual behavior of the fleet.
```

Build only:

```text
Minimal CWMP listener
SOAP logger
Inform parser
InformResponse renderer
GetRPCMethods probe
GetParameterNames probe
Device identity extraction
SOAP fault logger
Connection Request probe, optional
```

Do not build yet:

```text
Full OTA rollout
Full diagnostics UI
Bulk operations
Fleet policy engine
Advanced RBAC
```

Phase 0 acceptance criteria:

```text
Can receive Inform from a real CPE.
Can authenticate or log authentication failure.
Can respond with InformResponse.
Can call GetRPCMethods.
Can call GetParameterNames.
Can detect Device. vs InternetGatewayDevice.
Can log SOAP faults cleanly.
Can produce a device compatibility matrix.
```

Phase 0 deliverable:

```text
Device compatibility matrix
```

Example:

| Vendor/Model | Root | Amendment | mTLS | Digest | Download | Diagnostics | Connection Request | Notes |
|---|---:|---:|---:|---:|---:|---:|---:|---|
| Zyxel NR5103 | Device.2 likely | TBD | TBD | likely | TBD | TBD | TBD | Needs live test |
| Nokia FastMile | Device.2 likely | TBD | TBD | likely | TBD | TBD | TBD | Needs live test |
| Teltonika RUTX/RUTM | Device.2 likely | TBD | TBD | likely | TBD | TBD | TBD | TR-069 package may need install |
| Huawei 5G CPE | TBD | TBD | TBD | likely | TBD | TBD | TBD | Needs live test |

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
Serial RPC dispatch
```

Acceptance criteria:

```text
Operator can queue a parameter write.
Write is applied on next session.
Only one RPC is in flight per session.
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
Reachability mode tracking
REST trigger endpoint
```

Acceptance criteria:

```text
POST /connection-request attempts to wake CPE.
ACS detects whether Inform arrives with EventCode 6.
API reports success, timeout, or unreachable.
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
DownloadResponse handling
TransferComplete handling
Post-update SoftwareVersion check
Rollout jobs
Maintenance windows
Failure rate throttling
```

Acceptance criteria:

```text
ACS can queue firmware update.
CPE accepts Download.
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
Credential versioning
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

## 15. Testing Strategy

### 15.1 Unit tests

Test:

```text
SOAP parsing
Inform parsing
Fault parsing
CommandKey generation
Data model path mapping
Job state transitions
Session state transitions
Timeout handling
```

---

### 15.2 Protocol tests

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

### 15.3 Mock CPE emulator

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
Silent session timeout
Abrupt disconnect after Reboot
```

This allows development before real devices are available.

---

### 15.4 Real-device lab tests

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

### 15.5 Chaos tests

Test:

```text
CPE disconnects mid-session
CPE goes silent after RPC dispatch
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
Connection Request returns 200 but no Inform arrives
```

---

## 16. Observability

### 16.1 Metrics

Important metrics:

```text
cwmp_inform_total
cwmp_inform_by_event_code
cwmp_session_open_total
cwmp_session_close_total
cwmp_session_timeout_total
cwmp_rpc_sent_total
cwmp_rpc_fault_total
cwmp_rpc_timeout_total
cwmp_transfer_complete_total
connection_request_total
connection_request_success_total
connection_request_failure_total
job_created_total
job_success_total
job_failure_total
device_online_count
device_offline_count
device_unreachable_count
firmware_download_success_rate
periodic_inform_delay_seconds
```

---

### 16.2 Alerts

Important alerts:

```text
Fleet heartbeat loss
Sudden drop in Inform rate
High SOAP fault rate
High RPC timeout rate
High Download failure rate
Redis queue backlog growing
Connection Request failure spike
Database replication lag
Certificate expiry approaching
Secret rotation failure
```

---

### 16.3 Tracing

Use OpenTelemetry to correlate:

```text
REST request
job creation
queue dispatch
CWMP RPC
RPC response
TransferComplete
job completion
```

This is especially important for OTA jobs because start and completion may be separated by minutes or hours.

---

## 17. MVP Definition

### Must have

```text
1. HTTPS CWMP endpoint
2. Inform handling
3. Device registry
4. Session state tracking
5. Serial RPC dispatch
6. GetParameterValues
7. SetParameterValues
8. Pending RPC queue
9. REST device API
10. REST job API
11. Audit log
12. Digest authentication
13. Firmware Download via URL
14. TransferComplete handling
15. Session timeout handling
16. Basic Prometheus metrics
```

### Should have

```text
1. Connection Request
2. Reachability mode tracking
3. WebSocket job updates
4. Diagnostics ping/traceroute
5. Dual TR-181/TR-098 support
6. Vendor adapters
7. Bulk operations
8. mTLS support
9. Credential versioning
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

## 18. Example Repository Layout

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
    timeout.go
  auth/
    mtls.go
    digest.go
    jwt.go
    credentials.go
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
    lease.go
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
    session.py
    timeout.py
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
    lease.py
  firmware/
    models.py
    service.py
  security/
    mtls.py
    digest.py
    credentials.py
    audit.py
  db/
    models.py
    migrations/
```

---

## 19. Common Pitfalls to Avoid

### 19.1 Do not fire RPCs in parallel inside one CWMP session

Bad:

```text
Send multiple RPCs without waiting for responses.
```

Correct:

```text
One in-flight RPC per session.
```

---

### 19.2 Do not assume synchronous CPE reachability

Bad:

```text
REST PUT parameter -> immediately call CPE -> return result
```

Correct:

```text
REST PUT parameter -> create job -> return 202 -> execute when session exists
```

---

### 19.3 Do not hardcode one data model

Bad:

```text
Device.DeviceInfo.SoftwareVersion everywhere
```

Correct:

```text
Canonical parameter -> adapter -> actual CPE path
```

---

### 19.4 Do not store firmware in database rows

Bad:

```text
SetParameterValues(Device.Flash.ImageData, firmware.hex())
```

Correct:

```text
Download(URL)
```

---

### 19.5 Do not forget TransferComplete correlation

Use:

```text
CommandKey
```

Without it, OTA jobs become hard to track.

---

### 19.6 Do not rely only on Connection Request

For cellular CPEs behind CGNAT, Connection Request may not work directly.

Always have:

```text
Periodic Inform fallback
```

---

### 19.7 Do not treat DownloadResponse as transfer completion

```text
DownloadResponse = CPE accepted the Download request
TransferComplete = CPE reports transfer outcome
```

---

### 19.8 Do not trust cached parameters as live state

Parameter cache should show freshness:

```json
{
  "value": "CorpWiFi",
  "as_of": "2026-08-04T08:00:00Z",
  "source": "INFORM"
}
```

---

### 19.9 Do not ignore SOAP faults

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

### Step 1: Build Phase 0 lab harness

Goal:

```text
Receive Inform from one real CPE
Log it
Respond with InformResponse
```

No full database or OTA logic needed initially.

---

### Step 2: Add capability probes

Implement:

```text
GetRPCMethods
GetParameterNames
Device. vs InternetGatewayDevice. detection
SOAP fault logging
```

---

### Step 3: Add device persistence

Store:

```text
OUI
SerialNumber
ProductClass
EventCodes
Last Inform time
ConnectionRequestURL
Data model root
CWMP amendment if discoverable
```

---

### Step 4: Add serial job dispatch

Implement:

```text
SetParameterValues queued by REST
Applied on next Inform/session
One RPC in flight
Job status tracking
```

---

### Step 5: Add Connection Request and reachability tracking

Implement:

```text
Wake device
Wait for Inform
Record EventCode 6
Record timeout/failure
```

---

### Step 6: Add OTA

Implement:

```text
Firmware metadata
Download RPC
DownloadResponse handling
TransferComplete
Post-update version check
```

---

### Step 7: Harden security and operations

Add:

```text
mTLS/Digest auth
secrets manager
credential versioning
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

## 22. Final Implementation Rules

The implementation must enforce:

```text
1. CWMP endpoint handles SOAP sessions only.
2. REST API only queues jobs.
3. Jobs execute when the CPE has an open session or after Connection Request.
4. RPC dispatch inside a session is serial.
5. One RPC is in flight per session.
6. Firmware updates use Download(), not parameter writes.
7. Device identity is OUI + SerialNumber.
8. Data model root is detected per device.
9. Vendor differences are isolated in adapters.
10. All write RPCs are audited.
11. TLS is mandatory.
12. mTLS is the target, Digest is fallback.
13. Credential rotation is capability-aware and versioned.
14. Redis tracks sessions and dispatch state.
15. PostgreSQL tracks durable state.
16. Firmware is served over HTTPS from a separate file store.
17. Session timeouts and stale sessions are handled explicitly.
18. CGNAT fallback is assumed for cellular CPEs.
19. Observability is built in from the beginning.
20. Real-device lab verification gates production rollout.
```