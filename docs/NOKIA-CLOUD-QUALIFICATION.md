# Nokia cloud qualification

This runbook is the execution path for issue #75: deploy ACS to a controlled cloud host, receive the physical Nokia CPE's first CWMP session, capture its real identity/data model, and qualify that exact firmware without guessing vendor paths.

The current reported Nokia identifiers are:

- hardware: `3TG02241AAAA`
- software: `1.2501.00.0798`
- build: `5GGW2-OMNI-2_R250100BiwT0301E0798`
- country: `eu`
- local management address: `192.168.1.1`

Those values do **not** prove the final model/ProductClass/OUI. The first authenticated Inform and discovery export are authoritative.

## 1. Cloud topology

Provider-neutral requirements:

- public/reachable TCP endpoint for CWMP;
- persistent Postgres;
- operator console/API not exposed as unauthenticated cleartext management services;
- optional UDP 3478 only when STUN/Annex-G testing is in scope;
- enough storage for captures/evidence and DB backup;
- stable clock/DNS.

### AWS EC2 example

A practical test host is Ubuntu 24.04 LTS on EC2 in `af-south-1`. An Elastic IP is **not required** for the first controlled Nokia lab. The instance's current auto-assigned public IPv4 is acceptable as long as it is recorded at the start of the test and the operator understands that stopping/starting the instance can change it. Keep the instance running during an active qualification session, or update the Nokia ACS URL if the public IPv4 changes. For a persistent test fleet or production-like qualification, prefer DNS backed by a stable address.

Recommended security-group policy for the first Nokia CWMP run:

| Port | Direction | Source | Purpose |
|---|---|---|---|
| 22/tcp | inbound | operator/admin CIDR only | SSH administration |
| 443/tcp | inbound | operator/admin CIDR or reviewed public ingress | HTTPS operator UI/API when production ingress is configured |
| 7547/tcp | inbound | Nokia WAN/NAT source policy | CWMP CPE -> ACS |
| 3478/udp | inbound | Nokia WAN/NAT source policy | optional STUN |
| 5173/tcp | inbound | operator/admin CIDR only | lab operator console |
| 8080/tcp | inbound | operator/admin CIDR only | lab REST API used by the console/qualification tooling |
| 3000/9090 | inbound | **none by default** | Grafana/Prometheus stay loopback unless deliberately published for a restricted lab |
| 8090/8092/5432 | inbound | **none** | BSS/USP health/Postgres remain host-local |

Do not use the operator UI port as the CPE ACS URL. For example, `http://CURRENT_PUBLIC_IP:5173` is the browser UI; the default CWMP URL is `http://CURRENT_PUBLIC_IP:7547/cwmp` in a lab profile.

At the beginning of every lab session, obtain and record the instance's **current** public IPv4 from the cloud console (or equivalent instance metadata) and use that value consistently in the CPE configuration and qualification evidence. Never assume the previous session's address is still valid.

If the Nokia source address is unknown/dynamic behind a mobile carrier or CGNAT, prefer a short controlled observation window with the constrained bootstrap identity and rate limits rather than leaving 7547 globally exposed. Tighten the source policy as soon as the real egress range is known.

## 2. Choose the first-contact profile

### Pass A — controlled lab

Use this first if the Nokia's TLS trust/cipher behavior is still unknown.

```
export ACS_DEPLOYMENT_PROFILE=lab
export ACS_CWMP_BOOTSTRAP_USERNAME="nokia-bootstrap"
export ACS_CWMP_BOOTSTRAP_PASSWORD="<strong-random-secret>"
export ACS_CREDENTIAL_ENCRYPTION_KEY="<strong-stable-secret>"
```

For a lab host using its current auto-assigned public IP `CURRENT_PUBLIC_IP`:

```
export NOKIA_ACS_URL="http://CURRENT_PUBLIC_IP:7547/cwmp"
```

This is for capture-first compatibility qualification only. Keep the firewall restricted and do not call this the production security path.

### Pass B — production-like qualification

Use a DNS name and a certificate the CPE can validate:

```
export ACS_DEPLOYMENT_PROFILE=production
export NOKIA_ACS_URL="https://acs.example.com:7547/cwmp"
export ACS_CWMP_BOOTSTRAP_USERNAME="nokia-bootstrap"
export ACS_CWMP_BOOTSTRAP_PASSWORD="<strong-random-secret>"
export ACS_CREDENTIAL_ENCRYPTION_KEY="<strong-stable-secret>"
export ACS_TLS_CERT="/path/to/cwmp-fullchain.pem"
export ACS_TLS_KEY="/path/to/cwmp-private-key.pem"
export ACS_TLS_MIN_VERSION="1.2"
```

The supported full production launcher additionally requires the USP production TLS/client-CA/CIDR controls documented in `docs/security/device-authentication-and-deployment-profiles.md`, even if the first Nokia test is CWMP-only.

## 3. Build the cloud server from the exact commit under test

```
git clone <repo>
cd ACS
git checkout <recorded-commit>
```

Do not qualify hardware against an uncommitted worktree.

Generate/load stable secrets and dependencies using the existing deployment path, then apply migrations.

From `backend/`:

```
go run ./cmd/migrate
go run ./cmd/migrate -check
```

The `-check` command must return success before the Nokia is pointed at the server.

For a controlled lab launch:

```
./scripts/start.sh
```

For the supported production host profile:

```
./scripts/start-production.sh
```

Then run the applicable preflight:

```
./scripts/field-preflight.sh
# or
./scripts/security-preflight.sh
```

Record the exact commit and preflight output in the qualification evidence.

## 4. Prepare the Nokia readiness report

Before touching the CPE:

```
export NOKIA_ACS_URL="http://CURRENT_PUBLIC_IP:7547/cwmp"   # lab example
export ACS_DEPLOYMENT_PROFILE=lab
export ACS_CWMP_BOOTSTRAP_USERNAME="nokia-bootstrap"
export ACS_CWMP_BOOTSTRAP_PASSWORD="<secret>"
export ACS_CREDENTIAL_ENCRYPTION_KEY="<same key used by ACS/API>"
export ACS_POSTGRES_DSN="<target DSN>"

python scripts/prepare-nokia-onboarding.py \
  --output .local/nokia-onboarding
```

The exported report is redacted. It must never contain the bootstrap password, DB password or encryption key.

## 5. Start capture before configuring the CPE

Use the ACS Capture workbench and start a bounded CWMP remote-IP capture for the Nokia's public/NAT address when known.

If the source address is not yet known, use the normal CWMP logs/onboarding reachability telemetry long enough to learn it, then move to a bounded capture.

The first useful distinction is:

```
no request
  -> network/DNS/firewall/ACS URL problem

request + repeated 401
  -> Digest/bootstrap credential/HTTP parser problem

authenticated Inform
  -> transport + auth + CWMP session established

later 900x fault
  -> data-model/path/value problem, not first-contact authentication
```

## 6. Configure the Nokia CPE

In the Nokia TR-069/CWMP/Management Server page, configure at minimum:

| CPE field | First-contact value |
|---|---|
| ACS URL | `NOKIA_ACS_URL` |
| ACS username | `ACS_CWMP_BOOTSTRAP_USERNAME` |
| ACS password | `ACS_CWMP_BOOTSTRAP_PASSWORD` |
| Periodic Inform | enabled |
| Periodic Inform interval | 60–300 seconds for qualification |

Do not guess Connection Request settings until the device's actual ManagementServer tree and WebUI options are observed.

Save/apply the settings or reboot if the firmware requires it.

## 7. Evidence expected from the first Inform

Record:

- Manufacturer;
- OUI;
- ProductClass;
- SerialNumber;
- HardwareVersion;
- SoftwareVersion/build;
- CWMP namespace;
- `Device.` vs `InternetGatewayDevice.` root;
- advertised ConnectionRequestURL;
- PeriodicInform values;
- STUN/NAT parameters if present.

With constrained bootstrap, credential installation and a matching unique-credential reconnect must complete before normal discovery/jobs can run. The bootstrap Inform is not a normal management session. Do not add Nokia vendor mappings until subsequent discovery has proved the paths exist on this exact firmware.

## 8. Run the read-only Nokia scenario

Once the Nokia has an ACS device UUID and has completed the bound-credential reconnect in section 9:

```
export ACS_API_TOKEN="<operator token>"

python scripts/qualify-device.py \
  --base-url https://YOUR-API-ORIGIN \
  --device DEVICE_UUID \
  --scenario ci/compatibility/nokia-read-only.json \
  --output .local/nokia-onboarding/qualification.json
```

For a lab where the API is reachable only through an SSH tunnel or loopback proxy, use that protected API origin instead.

The read-only scenario:

- checks reported hardware/software identity;
- stops on mismatch;
- discovers the real parameter root/tree;
- reads only discovered safe 5G/cellular candidates;
- excludes secrets/IMSI/IMEI/ICCID-like paths;
- performs no write and no reboot.

A PASS means the read-only compatibility stage passed. It does not make the firmware production-qualified.

## 9. Credential graduation

When the constrained bootstrap pair is configured, both lab and production use the bootstrap graduation handler. ACS creates a unique per-device `CWMP_DIGEST` credential and attempts installation through the standard ManagementServer username/password paths for the root inferred from Inform. This happens before parameter discovery; it does not establish that those paths are writable on this Nokia firmware. An unknown root blocks installation.

Expected lifecycle:

```
bootstrap Inform
  -> exact identity validated
  -> generated credential stored encrypted
  -> credential state PENDING
  -> credential installation SPV
  -> Nokia reconnects using unique credential
  -> Inform identity matches bound ACS device
  -> credential becomes ACTIVE
  -> bootstrap identity is no longer accepted for normal management of that device
```

If the Nokia firmware refuses remote ManagementServer credential rewrite, record the exact fault and mark automatic graduation BLOCKED for that firmware. Do not fall back to a permanent shared fleet credential.

An SPV acknowledgement alone does not activate the credential. If installation faults or reconnect fails, retain the capture/credential ID, keep normal management blocked, and investigate the CPE configuration and root/path evidence. Use the reviewed operator credential workflow to recover the matching pending identity; do not repeatedly rotate credentials or delete device registration to bypass the check.

## 10. Connection Request qualification

After normal Inform/discovery is stable:

1. read the actual ConnectionRequestURL;
2. determine the Nokia's supported authentication mode;
3. configure unique Connection Request credentials;
4. trigger a Connection Request from ACS;
5. require the following Inform to contain Event 6 CONNECTION REQUEST;
6. record whether direct HTTP reachability, NAT, STUN or Annex-G is required.

A private `192.168.1.1` URL is not a cloud-reachable Connection Request endpoint by itself.

## 11. Controlled writes

Only after the read-only stage and secure reconnect pass:

1. choose a harmless discovered writable parameter;
2. read and record the original value;
3. set a temporary safe value;
4. read it back;
5. restore the original value;
6. read back the restored value.

Do not start with APN, WAN authentication, cellular band lock, factory reset or firmware operations.

## 12. Exit evidence

Retain:

- exact ACS commit;
- cloud host/profile;
- firewall/source policy;
- Nokia OUI/ProductClass/serial/hardware/software;
- capture IDs;
- discovery export;
- qualification JSON;
- command keys and 900x faults;
- credential-graduation state transitions;
- Connection Request/Event 6 result;
- reversible write/read-back/restore result;
- reboot/reconnect result;
- sanitized Nokia-specific parameter mappings.

Only then update `docs/COMPATIBILITY.md` with the exact firmware result.
