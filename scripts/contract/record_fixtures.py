#!/usr/bin/env python3
"""Record integration contract fixtures from a running ACS.

Drives a real cmd/api + cmd/bssadapter + Postgres through the BSS flows an
ISP integration uses, and writes each request/response as a fixture in the
format the bss-oss-integration harness loads (name, description,
captured_from, exchanges[method, path, status, mutating, body]).

Run it against a throwaway stack only: it creates a customer, devices, an
OAuth client, sales and webhooks. With ACS_CWMP_URL set, CPEs check in to
cmd/acs with real CWMP Informs, which drives the onboarding events. A
first contact, a firmware change and an unreachable device are simulated
with SQL updates of the device row and the parameter cache; the event
triggers that fire on them are the real ones. Webhook bodies are recorded
from webhook_deliveries, which holds the exact body sent.

Environment:
  ACS_API_URL       operator API (cmd/api), e.g. http://127.0.0.1:18080
  ACS_ADMIN_TOKEN   superadmin bearer for ACS_API_URL ("" in insecure dev mode)
  ACS_BSS_URL       BSS adapter (cmd/bssadapter), e.g. http://127.0.0.1:18090
  ACS_DB_EXEC       psql command that runs SQL from stdin against the stack's DB,
                    e.g. "docker exec -i acs-pg psql -U acs -d acs -v ON_ERROR_STOP=1"
  ACS_CWMP_URL      optional: cmd/acs CWMP endpoint, e.g. http://127.0.0.1:17547/
  ACS_CWMP_USER, ACS_CWMP_PASSWORD   its shared Digest credential; run cmd/acs
                    with ACS_CWMP_ALLOW_SHARED_ESTABLISHED=true for the recording
  ACS_COMMIT        commit the stack was built from (recorded in captured_from)
Usage: record_fixtures.py <output-dir>
"""

import base64
import json
import os
import shlex
import subprocess
import sys
import time
import urllib.error
import urllib.parse
import urllib.request

# SIM identifiers in request examples are synthetic, but fixtures travel:
# they are redacted like secrets.
SECRET_KEYS = {"access_token", "client_secret", "secret", "iccid", "imsi"}
REDACTED = "***REDACTED***"

API = os.environ["ACS_API_URL"].rstrip("/")
BSS = os.environ["ACS_BSS_URL"].rstrip("/")
ADMIN = os.environ.get("ACS_ADMIN_TOKEN", "")
DB_EXEC = shlex.split(os.environ["ACS_DB_EXEC"])
COMMIT = os.environ.get("ACS_COMMIT", "unknown")
OUT = sys.argv[1]


def redact(value):
    if isinstance(value, dict):
        return {k: (REDACTED if k in SECRET_KEYS and isinstance(v, str) else redact(v)) for k, v in value.items()}
    if isinstance(value, list):
        return [redact(v) for v in value]
    return value


def call(base, method, path, body=None, token=None, form=None, headers=None):
    data, hdrs = None, dict(headers or {})
    if form is not None:
        data = urllib.parse.urlencode(form).encode()
        hdrs["Content-Type"] = "application/x-www-form-urlencoded"
    elif body is not None:
        data = json.dumps(body).encode()
        hdrs["Content-Type"] = "application/json"
    if token:
        hdrs["Authorization"] = "Bearer " + token
    for attempt in range(5):
        req = urllib.request.Request(base + path, data=data, method=method, headers=hdrs)
        try:
            with urllib.request.urlopen(req, timeout=30) as resp:
                status, raw = resp.status, resp.read()
        except urllib.error.HTTPError as err:
            status, raw = err.code, err.read()
            # Rate limited: wait as told, as an integration must.
            if status == 429 and attempt < 4:
                time.sleep(float(err.headers.get("Retry-After") or 1))
                continue
        break
    try:
        parsed = json.loads(raw) if raw else None
    except ValueError:
        parsed = raw.decode(errors="replace")
    return status, parsed


def sql(statement):
    subprocess.run(DB_EXEC, input=statement.encode(), check=True, capture_output=True)


def query(statement):
    """Rows of a query as lists of fields (psql unaligned, tuples only)."""
    out = subprocess.run(DB_EXEC + ["-t", "-A", "-F", "\t"], input=statement.encode(), check=True, capture_output=True)
    return [line.split("\t") for line in out.stdout.decode().splitlines() if line.strip()]


INFORM = """<?xml version="1.0" encoding="UTF-8"?>
<soap-env:Envelope xmlns:soap-env="http://schemas.xmlsoap.org/soap/envelope/" xmlns:soap-enc="http://schemas.xmlsoap.org/soap/encoding/" xmlns:xsd="http://www.w3.org/2001/XMLSchema" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" xmlns:cwmp="urn:dslforum-org:cwmp-1-0">
<soap-env:Header><cwmp:ID soap-env:mustUnderstand="1">rec-{serial}</cwmp:ID></soap-env:Header>
<soap-env:Body><cwmp:Inform>
<DeviceId><Manufacturer>Nokia</Manufacturer><OUI>A4FC77</OUI><ProductClass>3TG02241</ProductClass><SerialNumber>{serial}</SerialNumber></DeviceId>
<Event soap-enc:arrayType="cwmp:EventStruct[1]"><EventStruct><EventCode>{event}</EventCode><CommandKey></CommandKey></EventStruct></Event>
<MaxEnvelopes>1</MaxEnvelopes><CurrentTime>2026-10-31T08:00:00Z</CurrentTime><RetryCount>0</RetryCount>
<ParameterList soap-enc:arrayType="cwmp:ParameterValueStruct[{n}]">{params}</ParameterList>
</cwmp:Inform></soap-env:Body></soap-env:Envelope>"""


def inform(serial, params, event="2 PERIODIC"):
    """One CWMP Inform to cmd/acs with Digest auth; returns the HTTP status."""
    values = "".join(f'<ParameterValueStruct><Name>{k}</Name><Value xsi:type="xsd:string">{v}</Value></ParameterValueStruct>'
                     for k, v in params.items())
    body = INFORM.format(serial=serial, event=event, n=len(params), params=values).encode()
    url = os.environ["ACS_CWMP_URL"]
    passwords = urllib.request.HTTPPasswordMgrWithDefaultRealm()
    passwords.add_password(None, url, os.environ["ACS_CWMP_USER"], os.environ["ACS_CWMP_PASSWORD"])
    opener = urllib.request.build_opener(urllib.request.HTTPDigestAuthHandler(passwords), urllib.request.HTTPCookieProcessor())
    req = urllib.request.Request(url, data=body, method="POST", headers={"Content-Type": "text/xml; charset=utf-8"})
    try:
        with opener.open(req, timeout=30) as resp:
            return resp.status
    except urllib.error.HTTPError as err:
        return err.code


fixtures = {}


def record(name, description, exchanges):
    fixtures[name] = {
        "name": "acs/" + name,
        "description": description,
        "captured_from": f"RECORDED from a running ACS (cmd/api + cmd/bssadapter + Postgres) at commit {COMMIT}, "
        f"{time.strftime('%Y-%m-%d')}. Secrets and tokens redacted.",
        "exchanges": exchanges,
    }


def exchange(method, path, status, body, mutating, request=None):
    out = {"method": method, "path": path, "status": status, "mutating": mutating, "body": redact(body)}
    if request is not None:
        out["request"] = redact(request)
    return out


def main():
    os.makedirs(OUT, exist_ok=True)
    status, customer = call(API, "POST", "/api/v1/customers", {"name": "Contract ISP"}, ADMIN)
    assert status in (200, 201), (status, customer)
    cust = customer["id"]
    sql(f"""INSERT INTO devices (id, oui_serial, oui, product_class, serial_number, manufacturer, data_model_root, online_status)
            VALUES (gen_random_uuid(), 'A4FC77+3TG02241+CONTRACT01', 'A4FC77', '3TG02241', 'CONTRACT01', 'Nokia', 'DEVICE2', 'OFFLINE');""")
    status, plan = call(API, "POST", "/api/v1/service-plans",
                        {"code": "HOME-5G", "name": "Home 5G", "customer_id": cust, "allowed_plmns": ["65506"]}, ADMIN)
    assert status == 201, (status, plan)
    status, created = call(API, "POST", "/api/v1/bss/oauth-clients",
                           {"name": "contract-isp", "scopes": ["tmf:read", "tmf:write", "tmf:execute"], "customer_id": cust}, ADMIN)
    assert status in (200, 201), (status, created)
    client_id, client_secret = created["client"]["client_id"], created["client_secret"]

    status, tok = call(BSS, "POST", "/bss/v1/oauth/token",
                       form={"grant_type": "client_credentials", "client_id": client_id, "client_secret": client_secret})
    assert status == 200, (status, tok)
    token = tok["access_token"]
    record("oauth-token-customer-bound", "Token for a customer-bound client: customer_id names the ACS customer (ISP).",
           [exchange("POST", "/bss/v1/oauth/token", status, tok, False)])

    acct = "contract.0001"
    body = {"account_id": acct, "oui_serial": "A4FC77+3TG02241+CONTRACT01", "service_plan": "HOME-5G"}
    status, resp = call(BSS, "POST", "/bss/v1/mappings", body, token)
    record("mapping-create", "First write to a new account claims it for the client's customer; the device joins that customer.",
           [exchange("POST", "/bss/v1/mappings", status, resp, True, body)])

    body = {"account_id": "contract.0002", "oui_serial": "A4FC77+3TG02241+CONTRACT02", "service_plan": "HOME-5G",
            "pre_provision": True, "iccid": "89270000000000001234",
            "place": {"id": "PREM-CONTRACT-1", "name": "14 Oak Ave", "city": "Embalenhle", "country": "ZA"}}
    status, resp = call(BSS, "POST", "/bss/v1/mappings", body, token)
    record("mapping-pre-provision", "pre_provision for a CPE the ACS has not seen: 202, a sale waits for the device.",
           [exchange("POST", "/bss/v1/mappings", status, resp, True, body)])

    order = {"externalId": "SO-CONTRACT-1", "accountId": "contract.0003", "orderItem": [
        {"id": "1", "action": "add", "role": "gateway", "ouiSerial": "A4FC77+3TG02241+CONTRACT03", "servicePlan": "HOME-5G",
         "imsi": "655060000000013", "place": {"id": "PREM-CONTRACT-2", "name": "9 Elm St"}},
        {"id": "2", "action": "modify", "role": "gateway", "parameters": {"wifi_ssid": "Elm"}}]}
    status, created_order = call(BSS, "POST", "/tmf-api/serviceOrdering/v4/serviceOrder", order, token)
    status2, read_order = call(BSS, "GET", "/tmf-api/serviceOrdering/v4/serviceOrder/SO-CONTRACT-1", token=token)
    record("tmf641-add-awaiting-device", "TMF641 add for an unseen CPE: the item is AWAITING_DEVICE and the modify behind it stays PENDING.",
           [exchange("POST", "/tmf-api/serviceOrdering/v4/serviceOrder", status, created_order, True, order),
            exchange("GET", "/tmf-api/serviceOrdering/v4/serviceOrder/SO-CONTRACT-1", status2, read_order, False)])

    status, hook = call(BSS, "POST", "/bss/v1/webhooks",
                        {"account_id": acct, "target_url": "https://203.0.113.10/acs-events",
                         "secret": "whsec_" + base64.b64encode(os.urandom(24)).decode(),
                         "event_types": ["DeviceSeen", "FirmwareChanged", "OnboardingStateChangeEvent"]}, token)
    assert status in (200, 201), (status, hook)
    record("webhook-create", "Account-scoped webhook subscription. The subscriber supplies the secret; it is never returned (rotate-secret returns a new one once).",
           [exchange("POST", "/bss/v1/webhooks", status, hook, True)])

    # Device events, from the real triggers on simulated state changes.
    sql("""UPDATE devices SET last_inform_at = now(), online_status = 'ONLINE' WHERE oui_serial = 'A4FC77+3TG02241+CONTRACT01';
           INSERT INTO device_parameter_cache (device_id, parameters)
             SELECT id, '{"Device.DeviceInfo.SoftwareVersion": {"value": "1.2501.00.0798"}}' FROM devices WHERE oui_serial = 'A4FC77+3TG02241+CONTRACT01';
           UPDATE device_parameter_cache SET parameters = '{"Device.DeviceInfo.SoftwareVersion": {"value": "1.2601.00.0101"}}'
             WHERE device_id = (SELECT id FROM devices WHERE oui_serial = 'A4FC77+3TG02241+CONTRACT01');""")
    status, page = call(BSS, "GET", "/bss/v1/events?after=0", token=token)
    status2, page2 = call(BSS, "GET", f"/bss/v1/events?after={page.get('next_after', 0)}", token=token)
    record("events-replay", "Replay of the customer's event stream: envelope, per-customer sequence, next_after cursor, then an empty page.",
           [exchange("GET", "/bss/v1/events?after=0", status, page, False),
            exchange("GET", f"/bss/v1/events?after={page.get('next_after', 0)}", status2, page2, False)])

    # Let the adapter fan the events out and attempt delivery (the target is
    # a documentation address, so attempts fail and say why).
    deadline = time.time() + 90
    deliveries = None
    while time.time() < deadline:
        status, deliveries = call(BSS, "GET", f"/bss/v1/webhooks/{hook['id']}/deliveries", token=token)
        if status == 200 and any(d.get("attempts", 0) > 0 for d in deliveries.get("items", [])):
            break
        time.sleep(5)
    record("webhook-deliveries", "Delivery view: status, attempts and the last error of each delivery.",
           [exchange("GET", f"/bss/v1/webhooks/{hook['id']}/deliveries", status, deliveries, False)])

    status, rotated = call(BSS, "POST", f"/bss/v1/webhooks/{hook['id']}/rotate-secret", {"overlap_seconds": 3600}, token)
    record("webhook-rotate-secret", "Secret rotation: both secrets sign until previous_secret_valid_until.",
           [exchange("POST", f"/bss/v1/webhooks/{hook['id']}/rotate-secret", status, rotated, True, {"overlap_seconds": 3600})])

    status, services = call(BSS, "GET", f"/tmf-api/serviceActivationAndConfiguration/v4/service?accountId={acct}", token=token)
    service_id = services[0]["id"] if status == 200 and services else ""
    headers = {"X-Idempotency-Key": "intent-contract-1"}
    status, mon = call(BSS, "PATCH", f"/tmf-api/serviceActivationAndConfiguration/v4/service/{service_id}", {"state": "inactive"}, token,
                       headers=headers)
    status2, mon2 = call(BSS, "PATCH", f"/tmf-api/serviceActivationAndConfiguration/v4/service/{service_id}", {"state": "inactive"}, token,
                         headers=headers)
    status3, mon3 = call(BSS, "GET", "/tmf-api/serviceActivationAndConfiguration/v4/monitor/intent-contract-1", token=token)
    status4, missing = call(BSS, "GET", "/tmf-api/serviceActivationAndConfiguration/v4/monitor/never-issued", token=token)
    record("tmf640-patch-monitor", "TMF640 PATCH with X-Idempotency-Key, its replay (same Monitor, nothing dispatched again), the Monitor, and an unknown Monitor (404).",
           [exchange("PATCH", f"/tmf-api/serviceActivationAndConfiguration/v4/service/{service_id}", status, mon, True, {"state": "inactive"}),
            exchange("PATCH", f"/tmf-api/serviceActivationAndConfiguration/v4/service/{service_id}", status2, mon2, False, {"state": "inactive"}),
            exchange("GET", "/tmf-api/serviceActivationAndConfiguration/v4/monitor/intent-contract-1", status3, mon3, False),
            exchange("GET", "/tmf-api/serviceActivationAndConfiguration/v4/monitor/never-issued", status4, missing, False)])

    status, other = call(BSS, "GET", "/tmf-api/serviceOrdering/v4/serviceOrder?accountId=someone-else.0001", token=token)
    record("not-found-other-account", "An account this customer does not own reads as 404, never 403.",
           [exchange("GET", "/tmf-api/serviceOrdering/v4/serviceOrder?accountId=someone-else.0001", status, other, False)])

    if os.environ.get("ACS_CWMP_URL"):
        record_webhook_bodies(token)

    for name, fixture in fixtures.items():
        with open(os.path.join(OUT, name + ".json"), "w", encoding="utf-8", newline="\n") as f:
            json.dump(fixture, f, indent=2, sort_keys=False, default=str)
            f.write("\n")
    print(f"wrote {len(fixtures)} fixtures to {OUT}")


def record_webhook_bodies(token):
    """Real CWMP Informs for two pre-provisioned sales and a mapped CPE, then
    one stored webhook body per event type."""
    body = {"account_id": "contract.0004", "oui_serial": "A4FC77+3TG02241+CONTRACT04", "service_plan": "HOME-5G", "pre_provision": True}
    status, _ = call(BSS, "POST", "/bss/v1/mappings", body, token)
    assert status == 202, status
    every = ["DeviceSeen", "DeviceUnreachable", "FirmwareChanged", "CellularStateChanged", "DeviceFault", "DeviceRecovered",
             "OnboardingStateChangeEvent", "IdentityMismatch"]
    for account in ("contract.0001", "contract.0002", "contract.0004"):
        status, _ = call(BSS, "POST", "/bss/v1/webhooks", {"account_id": account, "target_url": "https://203.0.113.10/acs-events",
                                                           "secret": "whsec_" + base64.b64encode(os.urandom(24)).decode(),
                                                           "event_types": every}, token)
        assert status in (200, 201), status
    # The sale for CONTRACT02 expects another ICCID: held, IdentityMismatch.
    assert inform("CONTRACT02", {"Device.DeviceInfo.SoftwareVersion": "1.2501.00.0798",
                                 "Device.Cellular.Interface.1.USIM.ICCID": "89270000000000009999"}, "0 BOOTSTRAP") == 200
    # The sale for CONTRACT04 passes the SIM check and waits for its own credential.
    assert inform("CONTRACT04", {"Device.DeviceInfo.SoftwareVersion": "1.2501.00.0798",
                                 "Device.Cellular.Interface.1.USIM.IMSI": "655060000000013"}, "0 BOOTSTRAP") == 200
    # The mapped CPE reports its cellular state, then goes quiet.
    assert inform("CONTRACT01", {"Device.DeviceInfo.SoftwareVersion": "1.2601.00.0101",
                                 "Device.Cellular.Interface.1.Status": "Up"}) == 200
    sql("UPDATE devices SET online_status = 'UNREACHABLE' WHERE oui_serial = 'A4FC77+3TG02241+CONTRACT01';")

    deadline, rows = time.time() + 60, []
    while time.time() < deadline:
        rows = query("""SELECT DISTINCT ON (event_type, payload->'data'->>'state') event_type, payload::text
                          FROM webhook_deliveries WHERE payload ? 'sequence'
                         ORDER BY event_type, payload->'data'->>'state', created_at;""")
        if len({r[0] for r in rows}) >= 6:
            break
        time.sleep(5)
    exchanges = [exchange("POST", "/acs-events (subscriber's target_url)", 0, json.loads(r[1]), False) for r in rows]
    for x in exchanges:
        x["note"] = ("The webhook body as sent, with headers Webhook-Id (the delivery id), Webhook-Timestamp, "
                     "Webhook-Signature and X-Webhook-Event. status is 0: the recording target is not reachable.")
    record("webhook-bodies", "One delivered webhook body per event type: the envelope plus the older TMF688 keys. "
           "Onboarding events come from real CWMP Informs to cmd/acs; DeviceFault and DeviceRecovered are not produced by this "
           "recording (see docs/integration/09-events.md for their data).", exchanges)


if __name__ == "__main__":
    main()
