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
import re
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
# Response headers an integration acts on, kept in the exchange.
KEPT_HEADERS = ("Location", "Idempotent-Replayed", "Content-Type", "Retry-After", "WWW-Authenticate")
LAST_HEADERS = {}


def redact(value):
    if isinstance(value, dict):
        return {k: (REDACTED if k in SECRET_KEYS and isinstance(v, str) else redact(v)) for k, v in value.items()}
    if isinstance(value, list):
        return [redact(v) for v in value]
    return value


def call(base, method, path, body=None, token=None, form=None, headers=None, retry=True):
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
                status, raw, got = resp.status, resp.read(), resp.headers
        except urllib.error.HTTPError as err:
            status, raw, got = err.code, err.read(), err.headers
        LAST_HEADERS.clear()
        LAST_HEADERS.update({k: got[k] for k in KEPT_HEADERS if got.get(k)})
        # Rate limited: wait as told, as an integration must.
        if status == 429 and retry and attempt < 4:
            time.sleep(float(got.get("Retry-After") or 1))
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


def cwmp_opener():
    url = os.environ["ACS_CWMP_URL"]
    passwords = urllib.request.HTTPPasswordMgrWithDefaultRealm()
    passwords.add_password(None, url, os.environ["ACS_CWMP_USER"], os.environ["ACS_CWMP_PASSWORD"])
    return url, urllib.request.build_opener(urllib.request.HTTPDigestAuthHandler(passwords), urllib.request.HTTPCookieProcessor())


def cwmp_post(url, opener, body):
    req = urllib.request.Request(url, data=body, method="POST", headers={"Content-Type": "text/xml; charset=utf-8"})
    try:
        with opener.open(req, timeout=30) as resp:
            return resp.status, resp.read().decode(errors="replace")
    except urllib.error.HTTPError as err:
        return err.code, err.read().decode(errors="replace")


def inform_body(serial, params, event):
    values = "".join(f'<ParameterValueStruct><Name>{k}</Name><Value xsi:type="xsd:string">{v}</Value></ParameterValueStruct>'
                     for k, v in params.items())
    return INFORM.format(serial=serial, event=event, n=len(params), params=values).encode()


def inform(serial, params, event="2 PERIODIC"):
    """One CWMP Inform to cmd/acs with Digest auth; returns the HTTP status."""
    url, opener = cwmp_opener()
    return cwmp_post(url, opener, inform_body(serial, params, event))[0]


ENVELOPE = """<?xml version="1.0" encoding="UTF-8"?>
<soap-env:Envelope xmlns:soap-env="http://schemas.xmlsoap.org/soap/envelope/" xmlns:soap-enc="http://schemas.xmlsoap.org/soap/encoding/" xmlns:xsd="http://www.w3.org/2001/XMLSchema" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" xmlns:cwmp="urn:dslforum-org:cwmp-1-0">
<soap-env:Header><cwmp:ID soap-env:mustUnderstand="1">{id}</cwmp:ID></soap-env:Header>
<soap-env:Body>{body}</soap-env:Body></soap-env:Envelope>"""


def cpe_answer(rpc, text, fail_writes, values):
    """What the simulated CPE answers to one ACS RPC."""
    if rpc == "SetParameterValues" and fail_writes:
        names = re.findall(r"<Name>([^<]+)</Name>", text)
        detail = "".join(f"<SetParameterValuesFault><ParameterName>{n}</ParameterName><FaultCode>9007</FaultCode>"
                         f"<FaultString>Invalid parameter value</FaultString></SetParameterValuesFault>" for n in names)
        return ("<soap-env:Fault><faultcode>Client</faultcode><faultstring>CWMP fault</faultstring><detail><cwmp:Fault>"
                f"<FaultCode>9007</FaultCode><FaultString>Invalid parameter value</FaultString>{detail}</cwmp:Fault></detail></soap-env:Fault>")
    if rpc == "SetParameterValues":
        for n, v in re.findall(r"<Name>([^<]+)</Name>\s*<Value[^>]*>([^<]*)</Value>", text):
            values[n] = v
        return "<cwmp:SetParameterValuesResponse><Status>0</Status></cwmp:SetParameterValuesResponse>"
    if rpc == "GetParameterValues":
        names = re.findall(r"<string>([^<]+)</string>", text)
        found = [(n, values[n]) for n in names if n in values] or [(n, v) for n, v in values.items() if any(n.startswith(p) for p in names)]
        items = "".join(f'<ParameterValueStruct><Name>{n}</Name><Value xsi:type="xsd:string">{v}</Value></ParameterValueStruct>' for n, v in found)
        return (f'<cwmp:GetParameterValuesResponse><ParameterList soap-enc:arrayType="cwmp:ParameterValueStruct[{len(found)}]">'
                f"{items}</ParameterList></cwmp:GetParameterValuesResponse>")
    if rpc == "GetParameterNames":
        items = "".join(f"<ParameterInfoStruct><Name>{n}</Name><Writable>{'1' if 'WiFi' in n else '0'}</Writable></ParameterInfoStruct>"
                        for n in values)
        return (f'<cwmp:GetParameterNamesResponse><ParameterList soap-enc:arrayType="cwmp:ParameterInfoStruct[{len(values)}]">'
                f"{items}</ParameterList></cwmp:GetParameterNamesResponse>")
    return f"<cwmp:{rpc}Response/>"


def cwmp_session(serial, params, fail_writes, values, event="2 PERIODIC"):
    """A whole CWMP session: Inform, then answer every RPC the ACS sends
    until it ends the session. Returns the RPC names seen."""
    url, opener = cwmp_opener()
    status, _ = cwmp_post(url, opener, inform_body(serial, params, event))
    assert status == 200, status
    seen, body = [], b""
    for _ in range(20):
        status, text = cwmp_post(url, opener, body)
        if status == 204 or not text.strip():
            return seen
        rpc = re.search(r"<soap(?:-env)?:Body>\s*<cwmp:(\w+)", text)
        rpc_id = re.search(r"<cwmp:ID[^>]*>([^<]*)</cwmp:ID>", text)
        if not rpc:
            return seen
        seen.append(rpc.group(1))
        body = ENVELOPE.format(id=rpc_id.group(1) if rpc_id else "", body=cpe_answer(rpc.group(1), text, fail_writes, values)).encode()
    return seen


fixtures = {}


def record(name, description, exchanges):
    fixtures[name] = {
        "name": "acs/" + name,
        "description": description,
        "captured_from": f"RECORDED from a running ACS (cmd/api + cmd/bssadapter + Postgres) at commit {COMMIT}, "
        f"{time.strftime('%Y-%m-%d')}. Secrets and tokens redacted.",
        "exchanges": exchanges,
    }


def exchange(method, path, status, body, mutating, request=None, headers=None, request_headers=None):
    out = {"method": method, "path": path, "status": status, "mutating": mutating, "body": redact(body)}
    if request is not None:
        out["request"] = redact(request)
    if request_headers:
        out["request_headers"] = request_headers
    if headers:
        out["headers"] = dict(headers)
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
    key = {"Idempotency-Key": "sale-contract-0002"}
    status, resp = call(BSS, "POST", "/bss/v1/mappings", body, token, headers=key)
    first = exchange("POST", "/bss/v1/mappings", status, resp, True, body, dict(LAST_HEADERS), key)
    sale_href = resp.get("href", "")
    record("mapping-pre-provision", "pre_provision for a CPE the ACS has not seen: 202, a sale waits for the device. href and "
           "Location point to the sale read; plan_matched says the plan code names an ACS plan.", [first])
    status, replay = call(BSS, "POST", "/bss/v1/mappings", body, token, headers=key)
    replayed = exchange("POST", "/bss/v1/mappings", status, replay, False, body, dict(LAST_HEADERS), key)
    other = dict(body, service_plan="HOME-5G-PLUS")
    status, clash = call(BSS, "POST", "/bss/v1/mappings", other, token, headers=key)
    record("mapping-idempotency-key", "Idempotency-Key on a mapping: the first answer, its replay (Idempotent-Replayed, the same "
           "body and Location, nothing done again), and the same key with another body (409 ErrIdempotencyKeyReused).",
           [first, replayed, exchange("POST", "/bss/v1/mappings", status, clash, False, other, dict(LAST_HEADERS), key)])

    status, sale = call(BSS, "GET", sale_href, token=token)
    unknown = "/bss/v1/onboardings/00000000-0000-0000-0000-000000000000"
    accept = {"Accept": "application/problem+json"}
    status2, missing = call(BSS, "GET", unknown, token=token, headers=accept)
    record("sale-read", "The sale read (GET href from the 202): state, reason_code, plan_matched, expires_at while PLANNED, "
           "UTC timestamps; then an unknown sale as a problem (404, Accept: application/problem+json).",
           [exchange("GET", sale_href, status, sale, False),
            exchange("GET", unknown, status2, missing, False, headers=dict(LAST_HEADERS), request_headers=accept)])

    body = {"account_id": "contract.0006", "oui_serial": "A4FC77+3TG02241+CONTRACT06", "service_plan": "NO-SUCH-PLAN", "pre_provision": True}
    status, resp = call(BSS, "POST", "/bss/v1/mappings", body, token)
    record("mapping-unknown-plan", "A plan code that matches no ACS plan: accepted, with plan_matched false and a PLAN_NOT_FOUND warning.",
           [exchange("POST", "/bss/v1/mappings", status, resp, True, body, dict(LAST_HEADERS))])

    # A mapped CPE, deleted over TMF641 without an unassignReason, the order
    # repeated, reused for another order, and a delete of the now empty role.
    sql("""INSERT INTO devices (id, oui_serial, oui, product_class, serial_number, manufacturer, data_model_root, online_status)
            VALUES (gen_random_uuid(), 'A4FC77+3TG02241+CONTRACT05', 'A4FC77', '3TG02241', 'CONTRACT05', 'Nokia', 'DEVICE2', 'OFFLINE');""")
    body = {"account_id": "contract.0005", "oui_serial": "A4FC77+3TG02241+CONTRACT05", "service_plan": "HOME-5G"}
    status, _ = call(BSS, "POST", "/bss/v1/mappings", body, token)
    assert status == 200, status
    delete = {"externalId": "SO-CONTRACT-DEL-1", "accountId": "contract.0005", "orderItem": [{"id": "1", "action": "delete", "role": "gateway"}]}
    path = "/tmf-api/serviceOrdering/v4/serviceOrder"
    status, created = call(BSS, "POST", path, delete, token)
    created_x = exchange("POST", path, status, created, True, delete)
    status, read = call(BSS, "GET", path + "/SO-CONTRACT-DEL-1", token=token)
    read_x = exchange("GET", path + "/SO-CONTRACT-DEL-1", status, read, False)
    status, mapped = call(BSS, "GET", "/bss/v1/mappings/contract.0005", token=token)
    mapped_x = exchange("GET", "/bss/v1/mappings/contract.0005", status, mapped, False)
    record("tmf641-delete-order", "TMF641 delete without unassignReason: completed at once (the reason recorded is return); "
           "the account then lists no active mapping.", [created_x, read_x, mapped_x])
    status, again = call(BSS, "POST", path, delete, token)
    repeat_x = exchange("POST", path, status, again, False, delete, dict(LAST_HEADERS))
    reused = dict(delete, orderItem=[{"id": "1", "action": "delete", "role": "extender"}])
    status, clash = call(BSS, "POST", path, reused, token)
    record("tmf641-repeat-external-id", "The same externalId again: the identical order answers 200 with the order as it is "
           "(Idempotent-Replayed, nothing done again); another order under it is 409 ErrIdempotencyKeyReused.",
           [repeat_x, exchange("POST", path, status, clash, False, reused, dict(LAST_HEADERS))])
    empty = {"externalId": "SO-CONTRACT-DEL-2", "accountId": "contract.0005", "orderItem": [{"id": "1", "action": "delete", "role": "gateway"}]}
    status, done = call(BSS, "POST", path, empty, token)
    record("tmf641-delete-unmapped", "A delete of a role with no device: completed, since what it asks for is already true.",
           [exchange("POST", path, status, done, True, empty)])

    sql("""INSERT INTO devices (id, oui_serial, oui, product_class, serial_number, manufacturer, data_model_root, online_status, customer_id)
            VALUES (gen_random_uuid(), 'A4FC77+3TG02241+CONTRACT07', 'A4FC77', '3TG02241', 'CONTRACT07', 'Nokia', 'DEVICE2', 'OFFLINE', '%s');""" % cust)
    status, page1 = call(BSS, "GET", "/bss/v1/devices?limit=2", token=token)
    nxt = page1.get("next_cursor", "") if isinstance(page1, dict) else ""
    status2, page2 = call(BSS, "GET", f"/bss/v1/devices?limit=2&cursor={nxt}", token=token)
    record("devices-page", "The customer's devices with their account, role, mapping status and reachability, two per page; "
           "next_cursor is absent on the last page.",
           [exchange("GET", "/bss/v1/devices?limit=2", status, page1, False),
            exchange("GET", f"/bss/v1/devices?limit=2&cursor={nxt}", status2, page2, False)])

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

    record_edges(token, cust)

    if os.environ.get("ACS_CWMP_URL"):
        record_webhook_bodies(token)

    for name, fixture in fixtures.items():
        with open(os.path.join(OUT, name + ".json"), "w", encoding="utf-8", newline="\n") as f:
            json.dump(fixture, f, indent=2, sort_keys=False, default=str)
            f.write("\n")
    print(f"wrote {len(fixtures)} fixtures to {OUT}")


def map_device(token, n):
    serial = f"CONTRACT{n:02d}"
    sql(f"""INSERT INTO devices (id, oui_serial, oui, product_class, serial_number, manufacturer, data_model_root, online_status)
            VALUES (gen_random_uuid(), 'A4FC77+3TG02241+{serial}', 'A4FC77', '3TG02241', '{serial}', 'Nokia', 'DEVICE2', 'OFFLINE');""")
    status, _ = call(BSS, "POST", "/bss/v1/mappings", {"account_id": f"contract.00{n:02d}", "oui_serial": f"A4FC77+3TG02241+{serial}",
                                                      "service_plan": "HOME-5G"}, token)
    assert status == 200, status


def record_edges(token, cust):
    """Order outcomes, sale failure and expiry, and the error bodies."""
    path = "/tmf-api/serviceOrdering/v4/serviceOrder"

    map_device(token, 8)
    stale = {"externalId": "SO-CONTRACT-STALE", "accountId": "contract.0008", "orderItem": [
        {"id": "1", "action": "delete", "role": "gateway", "ouiSerial": "A4FC77+3TG02241+CONTRACT99", "unassignReason": "return"}]}
    status, failed = call(BSS, "POST", path, stale, token)
    record("tmf641-order-failed", "A delete naming a device that is not the one in the role: the item FAILS and nothing is "
           "ended (the order state is failed). TMF641 order states are acknowledged (work outstanding; see "
           "tmf641-add-awaiting-device), completed, failed, partial and cancelled; there is no inProgress.",
           [exchange("POST", path, status, failed, True, stale)])
    mixed = {"externalId": "SO-CONTRACT-PARTIAL", "accountId": "contract.0008", "orderItem": [
        {"id": "1", "action": "delete", "role": "gateway", "ouiSerial": "A4FC77-CONTRACT08"},
        {"id": "2", "action": "modify", "role": "gateway", "parameters": {"wifi_ssid": "Gone"}}]}
    status, partial = call(BSS, "POST", path, mixed, token)
    record("tmf641-order-partial", "A delete that completes (ouiSerial in the Annex form matches), then a modify of the role it "
           "emptied, which fails: the order is partial. Items after a failed one are SKIPPED and do not make an order partial.",
           [exchange("POST", path, status, partial, True, mixed)])

    body = {"account_id": "contract.0009", "oui_serial": "A4FC77+3TG02241+CONTRACT09", "service_plan": "HOME-5G", "pre_provision": True}
    status, sale = call(BSS, "POST", "/bss/v1/mappings", body, token)
    assert status == 202, status
    # FAILED comes from configuration jobs a CPE refuses; set here in the
    # database, as the reconciler records it.
    sql(f"""UPDATE onboardings SET state = 'FAILED', state_code = 'CONFIG_FAILED',
              state_reason = 'configuration failed: 9007 Invalid parameter value' WHERE id = '{sale['onboarding_id']}';""")
    status, failed_sale = call(BSS, "GET", sale["href"], token=token)
    status2, retried = call(BSS, "POST", sale["href"] + "/retry", None, token)
    record("sale-failed-retry", "A FAILED sale (CONFIG_FAILED; the state is set in the database as the reconciler records it) "
           "read, then retried by the BSS: back to PLANNED, as its CPE has not connected.",
           [exchange("GET", sale["href"], status, failed_sale, False),
            exchange("POST", sale["href"] + "/retry", status2, retried, True)])

    order = {"externalId": "SO-CONTRACT-TTL", "accountId": "contract.0010", "orderItem": [
        {"id": "1", "action": "add", "role": "gateway", "ouiSerial": "A4FC77+3TG02241+CONTRACT10", "servicePlan": "HOME-5G"}]}
    status, _ = call(BSS, "POST", path, order, token)
    assert status == 201, status
    ttl_sale = query("SELECT id FROM onboardings WHERE expected_serial = 'CONTRACT10';")[0][0]
    # Age the sale past the TTL; the adapter's sync loop expires it.
    sql(f"UPDATE onboardings SET created_at = now() - interval '31 days', updated_at = now() - interval '31 days' WHERE id = '{ttl_sale}';")
    deadline, expired = time.time() + 60, None
    while time.time() < deadline:
        status, expired = call(BSS, "GET", f"/bss/v1/onboardings/{ttl_sale}", token=token)
        if expired.get("reason_code") == "TTL_EXPIRED":
            break
        time.sleep(3)
    time.sleep(16)  # one more sync pass fails the waiting item
    status2, ttl_order = call(BSS, "GET", path + "/SO-CONTRACT-TTL", token=token)
    status3, page = call(BSS, "GET", "/bss/v1/events?after=0&limit=500", token=token)
    event = next(e for e in page["events"] if e.get("data", {}).get("reasonCode") == "TTL_EXPIRED")
    record("sale-ttl-expired", "A sale no CPE contacted within the sale TTL (aged in the database; the adapter's own sync expired "
           "it): CANCELLED with TTL_EXPIRED, its TMF641 add FAILED, and the OnboardingStateChangeEvent as replayed.",
           [exchange("GET", f"/bss/v1/onboardings/{ttl_sale}", status, expired, False),
            exchange("GET", path + "/SO-CONTRACT-TTL", status2, ttl_order, False),
            exchange("GET", "/bss/v1/events (the TTL_EXPIRED event)", status3, event, False)])

    key = {"Idempotency-Key": "inflight-contract-0011"}
    body = {"account_id": "contract.0011", "oui_serial": "A4FC77+3TG02241+CONTRACT11", "pre_provision": True}
    status, _ = call(BSS, "POST", "/bss/v1/mappings", body, token, headers=key)
    assert status == 202, status
    # The first request still running is a window of milliseconds; the key
    # is put back in that state in the database to record the answer.
    sql("UPDATE bss_idempotency_keys SET status = NULL, response = NULL, completed_at = NULL WHERE key = 'inflight-contract-0011';")
    status, busy = call(BSS, "POST", "/bss/v1/mappings", body, token, headers=key)
    record("idempotency-key-in-flight", "A retry while the first request with the key is still running (that state is set in the "
           "database): 409 ErrIdempotencyKeyInFlight with Retry-After.",
           [exchange("POST", "/bss/v1/mappings", status, busy, False, body, dict(LAST_HEADERS), key)])

    errors = []
    bad = {"account_id": "contract.0012"}
    status, resp = call(BSS, "POST", "/bss/v1/mappings", bad, token)
    errors.append(exchange("POST", "/bss/v1/mappings", status, resp, False, bad, dict(LAST_HEADERS)))
    status, created = call(API, "POST", "/api/v1/bss/oauth-clients",
                           {"name": "contract-isp-readonly", "scopes": ["tmf:read"], "customer_id": cust}, ADMIN)
    assert status in (200, 201), (status, created)
    status, tok = call(BSS, "POST", "/bss/v1/oauth/token", form={"grant_type": "client_credentials",
                       "client_id": created["client"]["client_id"], "client_secret": created["client_secret"]})
    reader = tok["access_token"]
    write = {"account_id": "contract.0001", "oui_serial": "A4FC77+3TG02241+CONTRACT01"}
    status, resp = call(BSS, "POST", "/bss/v1/mappings", write, reader)
    errors.append(exchange("POST", "/bss/v1/mappings", status, resp, False, write, dict(LAST_HEADERS)))
    status, resp = call(BSS, "GET", "/bss/v1/events", token="not-a-token")
    errors.append(exchange("GET", "/bss/v1/events", status, resp, False, headers=dict(LAST_HEADERS)))
    for _ in range(40):
        status, resp = call(BSS, "GET", "/bss/v1/devices?limit=1", token=token, retry=False)
        if status == 429:
            errors.append(exchange("GET", "/bss/v1/devices?limit=1", status, resp, False, headers=dict(LAST_HEADERS)))
            break
    time.sleep(3)
    record("errors", "Problem bodies: 400 (a mapping without oui_serial), 403 insufficient_scope (a tmf:read client writing, "
           "with WWW-Authenticate), 401 (a bad token), 429 (over the rate, with Retry-After).", errors)


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
    # The mapped CPE reports its cellular state.
    assert inform("CONTRACT01", {"Device.DeviceInfo.SoftwareVersion": "1.2601.00.0101",
                                 "Device.Cellular.Interface.1.Status": "Up"}) == 200

    # A parameter write the CPE refuses (CWMP fault 9007: DeviceFault), then
    # one it accepts (the fault clears: DeviceRecovered).
    device_id = query("SELECT id FROM devices WHERE oui_serial = 'A4FC77+3TG02241+CONTRACT01';")[0][0]
    values = {"Device.DeviceInfo.SoftwareVersion": "1.2601.00.0101", "Device.WiFi.SSID.1.SSID": "Contract"}
    write = {"parameters": [{"name": "Device.WiFi.SSID.1.SSID", "value": "Contract Home", "type": "xsd:string"}]}
    status, _ = call(API, "PUT", f"/api/v1/devices/{device_id}/parameters", write, ADMIN)
    assert status == 202, status
    seen_fault = cwmp_session("CONTRACT01", {"Device.DeviceInfo.SoftwareVersion": "1.2601.00.0101"}, True, values)
    status, _ = call(API, "PUT", f"/api/v1/devices/{device_id}/parameters", write, ADMIN)
    assert status == 202, status
    seen_ok = cwmp_session("CONTRACT01", {"Device.DeviceInfo.SoftwareVersion": "1.2601.00.0101"}, False, values)
    print("CWMP RPCs answered: fault session", seen_fault, "success session", seen_ok)
    sql("UPDATE devices SET online_status = 'UNREACHABLE' WHERE oui_serial = 'A4FC77+3TG02241+CONTRACT01';")

    # The held sale (CONTRACT02, SIM mismatch): the BSS retries it, then
    # cancels it.
    sale = query("SELECT id, state FROM onboardings WHERE expected_serial = 'CONTRACT02';")[0]
    exchanges = []
    for action in ("retry", "cancel"):
        status, resp = call(BSS, "POST", f"/bss/v1/onboardings/{sale[0]}/{action}", None, token)
        exchanges.append(exchange("POST", f"/bss/v1/onboardings/{sale[0]}/{action}", status, resp, True))
    status, resp = call(BSS, "POST", f"/bss/v1/onboardings/{sale[0]}/cancel", None, token)
    exchanges.append(exchange("POST", f"/bss/v1/onboardings/{sale[0]}/cancel", status, resp, False, headers=dict(LAST_HEADERS)))
    record("sale-retry-cancel", f"A held sale (was {sale[1]}, SIM mismatch) retried by the BSS, then cancelled; cancelling it "
           "again is 409 ErrSaleWrongState.", exchanges)

    deadline, rows = time.time() + 60, []
    while time.time() < deadline:
        rows = query("""SELECT DISTINCT ON (event_type, payload->'data'->>'state') event_type, payload::text
                          FROM webhook_deliveries WHERE payload ? 'sequence'
                         ORDER BY event_type, payload->'data'->>'state', created_at;""")
        if len({r[0] for r in rows}) >= 8:
            break
        time.sleep(5)
    exchanges = [exchange("POST", "/acs-events (subscriber's target_url)", 0, json.loads(r[1]), False) for r in rows]
    for x in exchanges:
        x["note"] = ("The webhook body as sent, with headers Webhook-Id (the delivery id), Webhook-Timestamp, "
                     "Webhook-Signature and X-Webhook-Event. status is 0: the recording target is not reachable.")
    record("webhook-bodies", "One delivered webhook body per event type: the envelope plus the older TMF688 keys. "
           "All come from real CWMP sessions with cmd/acs: Informs drive the onboarding events, a parameter write the CPE "
           "refuses with fault 9007 raises DeviceFault, and the next write it accepts clears it (DeviceRecovered).", exchanges)


if __name__ == "__main__":
    main()
