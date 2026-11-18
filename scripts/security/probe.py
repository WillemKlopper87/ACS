"""Active security probe of a LOCAL, THROWAWAY ACS stack. Never point it at a real one: it
creates customers, clients and orders, restarts and resets its own simulated routers, and
sends bursts that trip the rate limits.

Two ISPs (customers A and B), each with one simulated router (simcpe.py), one customer-bound
client and one mapped account. Every check states what an attacker tries and what the ACS must
answer: one customer reaching another's devices, forged and misused tokens, the factory-reset
switch, the internal service token's reach, hostile input, and limits.

The stack it expects (see docs/SECURITY-PROBE-2026-11-18.md): cmd/api on 18080, cmd/bssadapter
on 18090 with ACS_BSS_ALLOW_FACTORY_RESET=true and ACS_BSS_SECRETS_BEHIND_TLS_PROXY=true, a
second cmd/bssadapter on 18091 WITHOUT factory reset, cmd/acs on 17547 with
ACS_CWMP_ALLOW_SHARED_ESTABLISHED=true, and ACS_SERVICE_CALLS_REQUIRE_CUSTOMER=true on the API.
Environment: ACS_INTERNAL_SERVICE_TOKEN, ACS_BOOTSTRAP_ADMIN_PASSWORD, ACS_DIGEST_USERNAME,
ACS_DIGEST_PASSWORD. Needs Python 3.11+ and httpx. Exit code 1 when a check fails.
"""

from __future__ import annotations

import base64
import json
import os
import sys
import time
import uuid

import httpx

sys.path.insert(0, os.path.dirname(__file__))
from simcpe import SimCpe  # noqa: E402

API, BSS, BSS_NORESET = "http://127.0.0.1:18080", "http://127.0.0.1:18090", "http://127.0.0.1:18091"
CWMP = "http://127.0.0.1:17547/cwmp"
SERVICE_TOKEN = os.environ["ACS_INTERNAL_SERVICE_TOKEN"]
OUT: list[dict] = []


def check(area: str, name: str, ok: bool, detail: object = "") -> None:
    OUT.append({"area": area, "name": name, "ok": bool(ok), "detail": str(detail)[:300]})
    print(("PASS " if ok else "FAIL ") + f"[{area}] {name}" + ("" if ok else f"  <- {str(detail)[:300]}"))


def note(area: str, name: str, detail: object) -> None:
    OUT.append({"area": area, "name": name, "ok": None, "detail": str(detail)[:400]})
    print(f"NOTE [{area}] {name}: {str(detail)[:400]}")


def bss(token: str | None, method: str, path: str, base: str = BSS, **kw) -> httpx.Response:
    headers = kw.pop("headers", {})
    no_wait = kw.pop("_no_wait", False)
    if token:
        headers["Authorization"] = "Bearer " + token
    for _ in range(6):
        r = httpx.request(method, base + path, headers=headers, timeout=20, **kw)
        if r.status_code != 429 or no_wait:
            return r
        time.sleep(float(r.headers.get("Retry-After") or 1))
    return r


def code(r: httpx.Response) -> str:
    try:
        return f"{r.status_code} {r.json().get('code') or r.json().get('error') or ''}".strip()
    except Exception:
        return f"{r.status_code} {r.text[:80]}"


class Isp:
    def __init__(self, admin: httpx.Client, label: str, scopes: list[str] | None = None):
        self.cpe = SimCpe(CWMP, os.environ["ACS_DIGEST_USERNAME"], os.environ["ACS_DIGEST_PASSWORD"],
                          serial=f"SEC{label}{uuid.uuid4().hex[:6].upper()}")
        r = admin.post("/api/v1/customers", json={"name": f"ISP {label} {uuid.uuid4().hex[:5]}"})
        self.customer = r.json()["id"]
        self.cpe.session(), self.cpe.session()
        devs = admin.get("/api/v1/devices", params={"q": self.cpe.serial}).json()
        devs = devs.get("devices") or devs.get("items") or devs
        self.device = [d for d in devs if d.get("serial_number") == self.cpe.serial][0]["id"]
        admin.put(f"/api/v1/devices/{self.device}/customer", json={"customer_id": self.customer})
        self.client_id, self.secret, self.client_row = self.new_client(
            admin, scopes or ["tmf:read", "tmf:write", "tmf:execute"])
        self.token = self.fetch_token()
        self.account = f"acct-{label.lower()}-{uuid.uuid4().hex[:6]}"
        r = bss(self.token, "POST", "/bss/v1/mappings", json={
            "account_id": self.account, "service_plan": "P",
            "oui_serial": f"{self.cpe.oui}+{self.cpe.product_class}+{self.cpe.serial}"})
        assert r.status_code == 200, r.text

    def new_client(self, admin, scopes):
        r = admin.post("/api/v1/bss/oauth-clients",
                       json={"name": f"c-{uuid.uuid4().hex[:6]}", "customer_id": self.customer, "scopes": scopes})
        j = r.json()
        return j["client"]["client_id"], j["client_secret"], j["client"]

    def fetch_token(self, cid=None, secret=None) -> str:
        r = httpx.post(BSS + "/bss/v1/oauth/token", data={
            "grant_type": "client_credentials", "client_id": cid or self.client_id,
            "client_secret": secret or self.secret})
        return r.json()["access_token"]


def b64(d: dict | bytes) -> str:
    raw = d if isinstance(d, bytes) else json.dumps(d, separators=(",", ":")).encode()
    return base64.urlsafe_b64encode(raw).rstrip(b"=").decode()


def unb64(s: str) -> dict:
    return json.loads(base64.urlsafe_b64decode(s + "=" * (-len(s) % 4)))


def main() -> None:
    admin = httpx.Client(base_url=API, timeout=20)
    r = admin.post("/api/v1/auth/login", json={"username": "admin", "password": os.environ["ACS_BOOTSTRAP_ADMIN_PASSWORD"]})
    admin.headers["Authorization"] = "Bearer " + r.json()["token"]
    a, b = Isp(admin, "A"), Isp(admin, "B")

    # B places real orders so there is something of B's to go after
    order_b = bss(b.token, "POST", "/bss/v1/orders", json={
        "external_order_id": "B-ORDER-1", "account_id": b.account, "action": "REFRESH_STATUS"}).json()
    b.cpe.session()
    key_b = order_b["command_key"]

    # ---------------------------------------------------------------- cross-tenant
    T = "cross-tenant"
    for path in (f"/bss/v1/mappings/{b.account}", f"/bss/v1/mappings/{b.account}/status",
                 f"/bss/v1/mappings/{b.account}/capabilities", f"/bss/v1/jobs/{key_b}",
                 f"/bss/v1/devices/{b.device}/reconciliation",
                 f"/tmf-api/serviceInventoryManagement/v4/service/{b.account}"):
        r = bss(a.token, "GET", path)
        check(T, f"A reads B's {path.split('/')[3] if 'bss' in path else 'tmf service'} ({path.rsplit('/', 1)[-1][:14]}) -> 404",
              r.status_code == 404 and b.device not in r.text and b.cpe.serial not in r.text, code(r))
    for action, params in (("REBOOT", None), ("REFRESH_STATUS", None),
                           ("FACTORY_RESET", {"confirm": "FACTORY_RESET"}),
                           ("MODIFY_WIFI", {"wifi_ssid": "pwned"})):
        body = {"external_order_id": f"A-ATTACK-{action}", "account_id": b.account, "action": action}
        if params:
            body["parameters"] = params
        r = bss(a.token, "POST", "/bss/v1/orders", json=body)
        check(T, f"A orders {action} on B's account -> refused", r.status_code in (403, 404), code(r))
        r = bss(a.token, "POST", "/bss/v1/actions/preflight", json={k: v for k, v in body.items() if k != "external_order_id"})
        check(T, f"A preflights {action} on B's account -> refused", r.status_code in (403, 404), code(r))
    n = len(b.cpe.rpcs)
    b.cpe.session()
    got = [m for m, _ in b.cpe.rpcs[n:]]
    check(T, "B's router received nothing from A's attempts", not {"Reboot", "FactoryReset", "SetParameterValues"} & set(got), got)
    r = bss(a.token, "GET", "/bss/v1/devices")
    check(T, "A's device list holds only A's device", b.device not in r.text and b.cpe.serial not in r.text, r.text[:200])
    r = bss(a.token, "GET", "/bss/v1/events?after=0&limit=500")
    check(T, "A's event replay holds nothing of B's", b.device not in r.text and b.account not in r.text, code(r))
    r = bss(a.token, "POST", "/bss/v1/mappings", json={
        "account_id": "a-steal", "service_plan": "P",
        "oui_serial": f"{b.cpe.oui}+{b.cpe.product_class}+{b.cpe.serial}"})
    check(T, "A maps B's router by serial -> refused", r.status_code in (403, 404, 409), code(r))
    # the same external_order_id and account id are each customer's own (2.0)
    r = bss(a.token, "POST", "/bss/v1/orders", json={"external_order_id": "B-ORDER-1", "account_id": a.account, "action": "REFRESH_STATUS"})
    check(T, "B's order id reused by A is A's own order, not a replay of B's",
          r.status_code == 202 and r.json()["command_key"] != key_b, code(r))
    a.cpe.session()
    # a secret handle is the customer's own
    h = bss(b.token, "POST", "/bss/v1/secrets", json={"value": "B-secret-passphrase-1"}).json().get("handle")
    r = bss(a.token, "POST", "/bss/v1/orders", json={"external_order_id": "A-HANDLE", "account_id": a.account,
            "action": "MODIFY_WIFI", "parameters": {"wifi_password_handle": h}})
    check(T, "A uses B's sealed secret handle -> refused", r.status_code in (400, 403, 404), code(r))
    r = bss(a.token, "DELETE", f"/bss/v1/secrets/{h}")
    check(T, "A deletes B's secret handle -> 404", r.status_code == 404, code(r))
    r = bss(a.token, "GET", "/bss/v1/webhooks")
    wb = bss(b.token, "POST", "/bss/v1/webhooks", json={"account_id": b.account, "target_url": "https://203.0.113.9/h",
             "secret": "s" * 40, "event_types": ["DeviceSeen"]})
    if wb.status_code in (200, 201):
        wid = wb.json().get("id")
        for m, p in (("DELETE", f"/bss/v1/webhooks/{wid}"), ("GET", f"/bss/v1/webhooks/{wid}/deliveries"),
                     ("POST", f"/bss/v1/webhooks/{wid}/rotate-secret")):
            r = bss(a.token, m, p, **({"json": {"secret": "x" * 40}} if m == "POST" else {}))
            check(T, f"A {m} on B's webhook -> 404", r.status_code in (403, 404), code(r))
    else:
        note(T, "webhook create", code(wb))

    # ---------------------------------------------------------------- tokens
    T = "tokens"
    r = bss(None, "GET", f"/bss/v1/mappings/{a.account}")
    check(T, "no token -> 401", r.status_code == 401, code(r))
    r = bss("garbage", "GET", f"/bss/v1/mappings/{a.account}")
    check(T, "garbage token -> 401", r.status_code == 401, code(r))
    head, payload, sig = a.token.split(".")
    claims = unb64(payload)
    note(T, "token claims (names only)", sorted(claims))
    forged = dict(claims)
    for k in list(forged):
        if forged[k] == a.customer:
            forged[k] = b.customer
    r = bss(f"{head}.{b64(forged)}.{sig}", "GET", f"/bss/v1/mappings/{b.account}")
    check(T, "payload swapped to B's customer, old signature -> 401", r.status_code == 401, code(r))
    none_head = b64({"alg": "none", "typ": "JWT"})
    for label, tok in (("alg none, no signature", f"{none_head}.{b64(forged)}."),
                       ("alg none, old signature", f"{none_head}.{b64(forged)}.{sig}"),
                       ("signed with a guessed secret", None)):
        if tok is None:
            import hashlib
            import hmac
            msg = f"{head}.{b64(forged)}".encode()
            tok = msg.decode() + "." + b64(hmac.new(b"secret", msg, hashlib.sha256).digest())
        r = bss(tok, "GET", f"/bss/v1/mappings/{b.account}")
        check(T, f"{label} -> 401", r.status_code == 401, code(r))
    expired = dict(claims, exp=int(time.time()) - 10)
    r = bss(f"{head}.{b64(expired)}.{sig}", "GET", f"/bss/v1/mappings/{a.account}")
    check(T, "expiry edited -> 401", r.status_code == 401, code(r))
    # the operator's console token and the internal service token are not adapter tokens
    r = bss(admin.headers["Authorization"].split()[1], "GET", f"/bss/v1/mappings/{a.account}")
    check(T, "operator console token on the adapter -> 401", r.status_code == 401, code(r))
    r = bss(SERVICE_TOKEN, "GET", f"/bss/v1/mappings/{a.account}")
    check(T, "internal service token on the adapter -> 401", r.status_code == 401, code(r))
    # and an adapter token is not an operator token
    r = httpx.get(API + "/api/v1/devices", headers={"Authorization": "Bearer " + a.token})
    check(T, "adapter token on the operator API -> 401", r.status_code in (401, 403), code(r))
    r = httpx.post(BSS + "/bss/v1/oauth/token", data={"grant_type": "client_credentials", "client_id": a.client_id, "client_secret": "wrong"})
    check(T, "wrong client secret -> 401, no detail", r.status_code == 401 and a.client_id not in r.text, code(r))
    # scopes
    ro_id, ro_secret, _ = a.new_client(admin, ["tmf:read"])
    ro = a.fetch_token(ro_id, ro_secret)
    r = bss(ro, "POST", "/bss/v1/orders", json={"external_order_id": "RO-1", "account_id": a.account, "action": "REBOOT"})
    check(T, "read-only client orders a restart -> 403", r.status_code == 403, code(r))
    r = bss(ro, "POST", "/bss/v1/mappings", json={"account_id": "ro-acct", "oui_serial": "x+y+z", "service_plan": "P"})
    check(T, "read-only client writes a mapping -> 403", r.status_code == 403, code(r))
    r = bss(ro, "POST", "/bss/v1/secrets", json={"value": "some-passphrase-123"})
    check(T, "read-only client stores a secret -> 403", r.status_code == 403, code(r))
    # revocation
    rv_id, rv_secret, rv_row = a.new_client(admin, ["tmf:read"])
    rv = a.fetch_token(rv_id, rv_secret)
    assert bss(rv, "GET", f"/bss/v1/mappings/{a.account}").status_code == 200
    d = admin.delete(f"/api/v1/bss/oauth-clients/{rv_row['id']}")
    r = httpx.post(BSS + "/bss/v1/oauth/token", data={"grant_type": "client_credentials", "client_id": rv_id, "client_secret": rv_secret})
    check(T, "revoked client gets no new token", r.status_code == 401, (d.status_code, code(r)))
    t0 = time.time()
    while time.time() - t0 < 25:
        r = bss(rv, "GET", f"/bss/v1/mappings/{a.account}")
        if r.status_code == 401:
            break
        time.sleep(1)
    check(T, f"an issued token of a revoked client stops working (took {time.time() - t0:.0f}s, documented bound 15s)",
          r.status_code == 401 and time.time() - t0 <= 17, code(r))

    # ---------------------------------------------------------------- factory reset gate
    T = "factory-reset"
    base = {"account_id": a.account, "action": "FACTORY_RESET"}
    for label, body in (
        ("no confirmation", dict(base)),
        ("confirmation in lower case", dict(base, parameters={"confirm": "factory_reset"})),
        ("confirmation true", dict(base, parameters={"confirm": True})),
        ("an extra parameter", dict(base, parameters={"confirm": "FACTORY_RESET", "x": "1"})),
    ):
        r = bss(a.token, "POST", "/bss/v1/orders", json=dict(body, external_order_id="FR-" + uuid.uuid4().hex[:6]))
        check(T, f"switched on, {label} -> 400", r.status_code == 400, code(r))
    for label, body in (
        ("with the confirmation", dict(base, parameters={"confirm": "FACTORY_RESET"})),
        ("action in lower case", dict(base, action="factory_reset", parameters={"confirm": "FACTORY_RESET"})),
        ("action padded", dict(base, action=" FACTORY_RESET ", parameters={"confirm": "FACTORY_RESET"})),
    ):
        r = bss(a.token, "POST", "/bss/v1/orders", base=BSS_NORESET, json=dict(body, external_order_id="FR-" + uuid.uuid4().hex[:6]))
        check(T, f"switched off, {label} -> 400", r.status_code == 400, code(r))
        r = bss(a.token, "POST", "/bss/v1/actions/preflight", base=BSS_NORESET, json=body)
        check(T, f"switched off, preflight {label} -> refused", r.status_code == 400, code(r))
    tmf = {"externalId": "FR-TMF-" + uuid.uuid4().hex[:6], "serviceOrderItem": [{
        "id": "1", "action": "modify", "service": {"id": a.account, "serviceCharacteristic": [
            {"name": "factory_reset", "value": True}, {"name": "action", "value": "FACTORY_RESET"},
            {"name": "confirm", "value": "FACTORY_RESET"}]}}]}
    r = bss(a.token, "POST", "/tmf-api/serviceOrdering/v4/serviceOrder", base=BSS_NORESET, json=tmf)
    n = len(a.cpe.rpcs)
    a.cpe.session(), a.cpe.session()
    check(T, "no reset reaches the router by any refused route (incl. a TMF641 try)",
          a.cpe.factory_resets == 0 and "FactoryReset" not in [m for m, _ in a.cpe.rpcs[n:]], (code(r), a.cpe.factory_resets))

    # ---------------------------------------------------------------- service token on the operator API
    T = "service-token"
    svc = {"Authorization": "Bearer " + SERVICE_TOKEN}
    for m, p in (("GET", "/api/v1/customers"), ("GET", "/api/v1/devices"), ("GET", "/api/v1/bss/oauth-clients"),
                 ("POST", "/api/v1/bss/oauth-clients"), ("GET", "/api/v1/audit-log"), ("GET", "/api/v1/operators"),
                 ("DELETE", f"/api/v1/devices/{b.device}"), ("PUT", f"/api/v1/devices/{b.device}/customer"),
                 ("POST", f"/api/v1/devices/{b.device}/reboot/"), ("POST", f"/api/v1/devices/{b.device}/reboot/x"),
                 ("POST", f"/api/v1/devices/{b.device}/firmware"), ("POST", f"/api/v1/devices/{b.device}/parameters/refresh-status/x"),
                 ("POST", f"/api/v1/devices/../customers/x/reboot"), ("POST", f"/api/v1/devices/{b.device}/%2e%2e/{b.device}/factory-reset"),
                 ("GET", f"/api/v1/devices/{b.device}/parameters"), ("POST", f"/api/v1/devices/{b.device}/cli/ticket")):
        r = httpx.request(m, API + p, headers=svc, json={} if m in ("POST", "PUT") else None)
        check(T, f"service token {m} {p.replace(b.device, '{id}')} -> refused", r.status_code in (400, 401, 403, 404, 405), code(r))
    # with production's rule on (service calls must name the customer): no customer, and the wrong one
    for label, extra in (("names no customer", {}), ("names customer A", {"X-ACS-Acting-Customer": a.customer})):
        for op in ("reboot", "factory-reset", "parameters/refresh-status"):
            r = httpx.post(API + f"/api/v1/devices/{b.device}/{op}", headers={**svc, **extra, "Idempotency-Key": uuid.uuid4().hex})
            check(T, f"service call that {label}: {op} on B's device -> refused", r.status_code in (400, 401, 403, 404), code(r))
    n = len(b.cpe.rpcs)
    b.cpe.session()
    got = [m for m, _ in b.cpe.rpcs[n:]]
    check(T, "B's router received no restart or reset from those calls", not {"Reboot", "FactoryReset"} & set(got), got)

    # ---------------------------------------------------------------- input handling
    T = "input"
    r = bss(a.token, "POST", "/bss/v1/orders", content=b'{"external_order_id":"big","pad":"' + b"A" * (2 << 20) + b'"}',
            headers={"Content-Type": "application/json"})
    check(T, "2 MiB order body -> 400/413", r.status_code in (400, 413), code(r))
    for label, acct in (("traversal", "../../etc/passwd"), ("encoded traversal", "%2e%2e%2f%2e%2e"), ("quote", "x' OR '1'='1"),
                        ("null byte", "a%00b"), ("10k chars", "A" * 10000), ("unicode", "ａｃｃｔ‮")):
        r = bss(a.token, "GET", f"/bss/v1/mappings/{acct}/status")
        check(T, f"status read with {label} account id -> 4xx, no server error", 400 <= r.status_code < 500, code(r))
    for key in ("x' OR 1=1--", "../../jobs", "A" * 5000, "%00"):
        r = bss(a.token, "GET", f"/bss/v1/jobs/{key}")
        check(T, f"job read with hostile key ({key[:12]}) -> 4xx", 400 <= r.status_code < 500, code(r))
    deep = "[" * 100000 + "]" * 100000
    r = bss(a.token, "POST", "/bss/v1/orders", content=deep.encode(), headers={"Content-Type": "application/json"})
    check(T, "deeply nested JSON -> 400, adapter still answers", r.status_code == 400 and bss(a.token, "GET", f"/bss/v1/mappings/{a.account}").status_code == 200, code(r))
    r = bss(a.token, "POST", "/bss/v1/orders", json={"external_order_id": "X" * 5000, "account_id": a.account, "action": "REBOOT"})
    check(T, "5000-character order id -> 400", r.status_code == 400, code(r))
    r = bss(a.token, "POST", "/bss/v1/orders", json={"external_order_id": "inj-1", "account_id": a.account, "action": "MODIFY_WIFI",
            "parameters": {"wifi_ssid": "a</Value><Name>Device.ManagementServer.URL</Name><Value>http://evil"}})
    n = len(a.cpe.rpcs)
    a.cpe.session(), a.cpe.session()
    sets = [x for m, x in a.cpe.rpcs[n:] if m == "SetParameterValues"]
    check(T, "an SSID carrying XML never becomes a second parameter on the router",
          not any("<Name>Device.ManagementServer.URL</Name>" in x for x in sets)
          and a.cpe.params.get("Device.ManagementServer.URL") is None, (code(r), len(sets)))
    r = bss(a.token, "GET", "/bss/v1/mappings/nope/status")
    check(T, "errors carry no stack trace, SQL or file path", not any(s in r.text for s in ("goroutine", "pq:", "SQLSTATE", ".go:", "/home/")), r.text[:200])

    # ---------------------------------------------------------------- limits and headers
    T = "limits"
    codes = [bss(a.token, "GET", f"/bss/v1/mappings/{a.account}", _no_wait=True).status_code for _ in range(40)]
    check(T, "40 rapid calls by one client are rate limited (429 seen)", 429 in codes, {c: codes.count(c) for c in set(codes)})
    r = bss(b.token, "GET", f"/bss/v1/mappings/{b.account}", _no_wait=True)
    check(T, "another client is not slowed by it", r.status_code == 200, code(r))
    fails = [httpx.post(API + "/api/v1/auth/login", json={"username": "admin", "password": f"wrong-{i}"}).status_code for i in range(15)]
    check(T, "15 wrong operator passwords are throttled (429 seen)", 429 in fails, {c: fails.count(c) for c in set(fails)})
    T = "headers"
    r = httpx.get(API + "/api/v1/devices", headers={"Origin": "https://evil.example", "Authorization": admin.headers["Authorization"]})
    check(T, "operator API does not allow a foreign web origin", r.headers.get("access-control-allow-origin") not in ("*", "https://evil.example"),
          r.headers.get("access-control-allow-origin"))
    for base, label in ((API, "API"), (BSS, "adapter")):
        r = httpx.get(base + "/metrics")
        note(T, f"/metrics on the {label} without a token", f"{r.status_code} (lab profile; production requires ACS_METRICS_TOKEN)")
    r = httpx.get(API + "/api/v1/devices", headers={"Authorization": admin.headers["Authorization"]})
    note(T, "security headers on the API", {k: v for k, v in r.headers.items() if k.lower() in (
        "x-content-type-options", "x-frame-options", "content-security-policy", "strict-transport-security", "cache-control", "referrer-policy")})
    T = "cwmp"
    r = httpx.post(CWMP, content="<x/>")
    check(T, "an Inform with no credentials -> 401", r.status_code == 401, r.status_code)
    r = httpx.post(CWMP, content="<x/>", auth=httpx.DigestAuth("acs-device", "wrong-password"))
    check(T, "an Inform with a wrong password -> 401", r.status_code == 401, r.status_code)

    bad = [o for o in OUT if o["ok"] is False]
    print(f"\n{sum(1 for o in OUT if o['ok'])} passed, {len(bad)} failed, {sum(1 for o in OUT if o['ok'] is None)} notes")
    if bad:
        raise SystemExit(1)


if __name__ == "__main__":
    main()
