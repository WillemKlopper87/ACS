"""Security tests against a LOCAL, THROWAWAY ACS in the production profile, behind a TLS proxy.
Never point it at a real one: it creates operators, customers and clients, walks every
registered route, trips the sign-in throttles and deletes audit rows to test a privilege.

Operator sign-in (two-step), sessions and CSRF, role boundaries by walking every registered
route, the adapter's strict mode, source allowlist and order ceiling, server-side request
forgery, the CWMP parser, and the per-service database logins.

The stack it expects is described in docs/SECURITY-PROBE-2026-11-18.md section 7: the three
services with ACS_DEPLOYMENT_PROFILE=production on loopback ports 28080 (API), 28090 (adapter)
and 27547 (CWMP, TLS), a TLS proxy on 28443 and 29443 that overwrites X-Forwarded-For, a test
CA in certs/ca.crt next to this file, Postgres in a container named acs-test-pg with database
acs_prod and the per-service logins enabled, and ACS_BSS_MAX_REBOOTS_PER_HOUR=5. The
environment the services run with must be exported. Needs Python 3.11+ and httpx.
"""

from __future__ import annotations

import base64
import hashlib
import hmac
import json
import os
import re
import socket
import ssl
import struct
import subprocess
import time
import uuid

import httpx

P = os.path.dirname(os.path.abspath(__file__))
CA = os.path.join(P, "certs", "ca.crt")
API, BSS = "https://127.0.0.1:28443", "https://127.0.0.1:29443"
API_DIRECT, BSS_DIRECT = "http://127.0.0.1:28080", "http://127.0.0.1:28090"
CWMP = "https://127.0.0.1:27547/cwmp"
ORIGIN = "https://acs.test:28443"
ADMIN_PW = os.environ["ACS_BOOTSTRAP_ADMIN_PASSWORD"]
OUT: list[dict] = []


def check(area, name, ok, detail=""):
    OUT.append({"area": area, "name": name, "ok": bool(ok), "detail": str(detail)[:400]})
    print(("PASS " if ok else "FAIL ") + f"[{area}] {name}" + ("" if ok else f"  <- {str(detail)[:400]}"), flush=True)


def note(area, name, detail):
    OUT.append({"area": area, "name": name, "ok": None, "detail": str(detail)[:600]})
    print(f"NOTE [{area}] {name}: {str(detail)[:600]}", flush=True)


def totp(secret: str, at: float | None = None) -> str:
    key = base64.b32decode(secret.upper() + "=" * (-len(secret) % 8))
    counter = int((at or time.time()) // 30)
    digest = hmac.new(key, struct.pack(">Q", counter), hashlib.sha1).digest()
    off = digest[-1] & 0x0F
    return f"{(struct.unpack('>I', digest[off:off + 4])[0] & 0x7FFFFFFF) % 1000000:06d}"


def sql(statement: str, role: str | None = None, password: str | None = None) -> tuple[int, str]:
    if role:
        cmd = ["docker", "exec", "-e", f"PGPASSWORD={password}", "acs-test-pg", "psql", "-h", "127.0.0.1",
               "-U", role, "-d", "acs_prod", "-At", "-c", statement]
    else:
        cmd = ["docker", "exec", "acs-test-pg", "psql", "-U", "postgres", "-d", "acs_prod", "-At", "-c", statement]
    r = subprocess.run(cmd, capture_output=True, text=True)
    return r.returncode, (r.stdout + r.stderr).strip()


def client() -> httpx.Client:
    return httpx.Client(base_url=API, verify=CA, timeout=20, headers={"Origin": ORIGIN}, trust_env=False)


class Operator:
    """An operator signed in through the real two-step flow."""

    def __init__(self, username: str, password: str):
        self.username, self.password = username, password
        self.http = client()
        self.secret = None
        self.token = None
        self.login()

    def login(self):
        r = self.http.post("/api/v1/auth/login", json={"username": self.username, "password": self.password})
        assert r.status_code == 200, (r.status_code, r.text)
        body = r.json()
        self.first = body
        if body.get("mfa") == "enroll":
            e = self.http.post("/api/v1/auth/login/mfa/enroll", json={"mfa_token": body["mfa_token"]})
            assert e.status_code == 200, e.text
            self.secret = e.json()["secret"]
            self.enroll_token = body["mfa_token"]
            c = self.http.post("/api/v1/auth/login/mfa/confirm", json={"mfa_token": body["mfa_token"], "code": totp(self.secret)})
            assert c.status_code == 200, c.text
            self.session = c
            body = c.json()
        elif body.get("mfa"):
            self.verify_token = body["mfa_token"]
            c = self.http.post("/api/v1/auth/login/mfa", json={"mfa_token": body["mfa_token"], "code": totp(self.secret)})
            assert c.status_code == 200, c.text
            self.session = c
            body = c.json()
        else:
            self.session = r
        self.token = body.get("token")
        self.bearer = {"Authorization": "Bearer " + self.token} if self.token else {}


def main():
    admin = Operator("admin", ADMIN_PW)
    A = "signin"
    # --- two-step sign-in ------------------------------------------------------------------
    anon = client()
    r = anon.get("/api/v1/devices", headers={"Authorization": "Bearer " + admin.enroll_token})
    check(A, "the enrolment-step token is not a session", r.status_code == 401, r.status_code)
    r = anon.post("/api/v1/auth/login/mfa", json={"mfa_token": admin.enroll_token, "code": totp(admin.secret)})
    check(A, "an enrolment-step token is refused at the code step", r.status_code == 401, r.status_code)
    r = anon.post("/api/v1/auth/login/mfa/enroll", json={"mfa_token": admin.enroll_token})
    check(A, "enrolment cannot be started again once enabled (no new secret)", r.status_code in (401, 409) and "secret" not in r.text, (r.status_code, r.text[:80]))
    step = anon.post("/api/v1/auth/login", json={"username": "admin", "password": ADMIN_PW}).json()
    check(A, "a password alone gives no session token or cookie", "token" not in step and step.get("mfa") == "verify" and not anon.cookies, step.keys())
    r = anon.get("/api/v1/devices", headers={"Authorization": "Bearer " + step["mfa_token"]})
    check(A, "the code-step token is not a session", r.status_code == 401, r.status_code)
    codes = [anon.post("/api/v1/auth/login/mfa", json={"mfa_token": step["mfa_token"], "code": f"{i:06d}"}).status_code for i in range(12)]
    check(A, "guessing codes is throttled (429 within 12 tries)", 429 in codes, codes)
    note(A, "wrong codes allowed before the throttle", codes.index(429) if 429 in codes else "none")
    time.sleep(1)
    r = anon.post("/api/v1/auth/login/mfa", json={"mfa_token": step["mfa_token"], "code": totp(admin.secret)})
    note(A, "a correct code right after the throttle", r.status_code)
    # a used code again (replay within its 30 s)
    fresh = client()
    s2 = fresh.post("/api/v1/auth/login", json={"username": "admin", "password": ADMIN_PW}).json()
    t0 = time.time()
    while r.status_code == 429 and time.time() - t0 < 75:
        time.sleep(5)
        r = fresh.post("/api/v1/auth/login/mfa", json={"mfa_token": s2["mfa_token"], "code": totp(admin.secret)})
    code_used = totp(admin.secret)
    r1 = fresh.post("/api/v1/auth/login/mfa", json={"mfa_token": s2["mfa_token"], "code": code_used})
    s3 = fresh.post("/api/v1/auth/login", json={"username": "admin", "password": ADMIN_PW}).json()
    r2 = fresh.post("/api/v1/auth/login/mfa", json={"mfa_token": s3["mfa_token"], "code": code_used})
    check(A, "a one-time code cannot be used twice", r1.status_code in (200, 401) and r2.status_code != 200, (r1.status_code, r2.status_code))
    r = anon.post("/api/v1/auth/login", json={"username": "admin", "password": "x' OR '1'='1"})
    check(A, "SQL in the password is just a wrong password", r.status_code in (401, 429), r.status_code)
    r = anon.post("/api/v1/auth/login", json={"username": "nobody-" + uuid.uuid4().hex, "password": "wrong"})
    r_known = anon.post("/api/v1/auth/login", json={"username": "admin", "password": "wrong"})
    check(A, "unknown user and wrong password answer alike", (r.status_code, r.text) == (r_known.status_code, r_known.text) or 429 in (r.status_code, r_known.status_code), (r.text[:60], r_known.text[:60]))

    # --- session cookie and CSRF ---------------------------------------------------------
    S = "session"
    setc = admin.session.headers.get_list("set-cookie")
    flags = " ".join(setc).lower()
    check(S, "the session cookie is HttpOnly, Secure and SameSite=Strict", all(f in flags for f in ("httponly", "secure", "samesite=strict")), setc)
    note(S, "cookie attributes", [re.sub(r"=[^;]+", "=…", c, count=1) for c in setc])
    cookie_only = httpx.Client(base_url=API, verify=CA, timeout=20, cookies=admin.http.cookies, trust_env=False)
    r = cookie_only.get("/api/v1/devices", headers={"Origin": ORIGIN})
    check(S, "the cookie alone reads from the console's origin", r.status_code == 200, r.status_code)
    body = {"name": "csrf-" + uuid.uuid4().hex[:6]}
    for label, hdrs in (("no Origin, no CSRF header", {}), ("a foreign Origin", {"Origin": "https://evil.example"}),
                        ("a foreign Origin with the CSRF header", {"Origin": "https://evil.example", "X-ACS-CSRF": "1"}),
                        ("the console's Origin, no CSRF header", {"Origin": ORIGIN}),
                        ("a look-alike Origin", {"Origin": ORIGIN + ".evil.example", "X-ACS-CSRF": "1"}),
                        ("Origin: null", {"Origin": "null", "X-ACS-CSRF": "1"})):
        r = cookie_only.post("/api/v1/customers", json=body, headers=hdrs)
        check(S, f"a cookie-authenticated change with {label} is refused", r.status_code in (401, 403), r.status_code)
    n = sql(f"select count(*) from customers where name = '{body['name']}'")[1]
    check(S, "none of those created anything", n == "0", n)
    r = cookie_only.get("/api/v1/devices", headers={"Origin": "https://evil.example"})
    check(S, "a foreign origin is not allowed to read the answer (CORS)", r.headers.get("access-control-allow-origin") != "https://evil.example", r.headers.get("access-control-allow-origin"))
    # token tampering
    head, payload, sig = admin.token.split(".")
    claims = json.loads(base64.urlsafe_b64decode(payload + "=" * (-len(payload) % 4)))
    enc = lambda d: base64.urlsafe_b64encode(json.dumps(d, separators=(",", ":")).encode()).rstrip(b"=").decode()  # noqa: E731
    for label, tok in (("role edited", f"{head}.{enc(dict(claims, role='superadmin', sub='ghost'))}.{sig}"),
                       ("alg none", f"{enc({'alg': 'none', 'typ': 'JWT'})}.{payload}."),
                       ("expiry extended", f"{head}.{enc(dict(claims, exp=claims['exp'] + 99999))}.{sig}")):
        r = anon.get("/api/v1/devices", headers={"Authorization": "Bearer " + tok})
        check(S, f"console token with {label} -> 401", r.status_code == 401, r.status_code)

    # --- operators of lower rank, and every registered route --------------------------------
    R = "roles"
    pw = "Zx9-" + uuid.uuid4().hex + "-Kq"
    made = {}
    for role in ("readonly", "noc"):
        name = f"{role}-{uuid.uuid4().hex[:6]}"
        r = admin.http.post("/api/v1/auth/operators", json={"username": name, "password": pw, "role": role, "global_access": True}, headers=admin.bearer)
        assert r.status_code in (200, 201), r.text
        made[role] = Operator(name, pw)
    ro = made["readonly"]
    note(R, "readonly sign-in", {"mfa_required": ro.first.get("mfa"), "has_token": bool(ro.token)})
    r = admin.http.post("/api/v1/customers", json={"name": "Walk ISP"}, headers=admin.bearer)
    cust = r.json()["id"]
    dev = str(uuid.uuid4())
    sql(f"INSERT INTO devices (id, oui_serial, oui, product_class, serial_number, manufacturer, data_model_root, online_status, customer_id) "
        f"VALUES ('{dev}', 'A4FC77+PC+WALK01', 'A4FC77', 'PC', 'WALK01', 'Nokia', 'DEVICE2', 'OFFLINE', '{cust}')")
    routes_src = open(os.path.join(P, "..", "..", "backend", "cmd", "api", "routes.go")).read()
    routes = re.findall(r'route(?:Perm)?\(\s*"([A-Z]+)",\s*"([^"]+)"', routes_src)
    routes += [(m, p) for m, p in re.findall(r'mux\.HandleFunc\("([A-Z]+) ([^"]+)"', routes_src)]
    public = {"/api/v1/auth/login", "/api/v1/auth/login/mfa", "/api/v1/auth/login/mfa/enroll", "/api/v1/auth/login/mfa/confirm",
              "/healthz", "/readyz"}
    counts = {"anon_ok": [], "ro_mutations_ok": [], "server_errors": []}
    walked = 0
    for method, path in sorted(set(routes)):
        concrete = re.sub(r"\{(id|device_id)\}", dev, path)
        concrete = re.sub(r"\{[a-z_]+\.\.\.\}", "x", concrete)
        concrete = re.sub(r"\{[a-z_]+\}", str(uuid.uuid4()), concrete)
        kw = {"json": {}} if method in ("POST", "PUT", "PATCH") else {}
        walked += 1
        r = anon.request(method, concrete, **kw)
        if r.status_code < 400 and path not in public and not path.startswith("/api/v1/auth/password") and "public" not in path:
            counts["anon_ok"].append(f"{method} {path} -> {r.status_code}")
        if r.status_code >= 500:
            counts["server_errors"].append(f"anon {method} {path} -> {r.status_code}")
        if method != "GET":
            r = ro.http.request(method, concrete, headers={**ro.bearer}, **kw)
            if r.status_code < 400:
                counts["ro_mutations_ok"].append(f"{method} {path} -> {r.status_code}")
            if r.status_code >= 500:
                counts["server_errors"].append(f"readonly {method} {path} -> {r.status_code}")
        time.sleep(0.12)  # stay under the API's own rate limit
    check(R, f"no registered route answers without a session ({walked} routes walked)", not counts["anon_ok"], counts["anon_ok"][:12])
    check(R, "a read-only operator can change nothing on any registered route", not counts["ro_mutations_ok"], counts["ro_mutations_ok"][:12])
    check(R, "no route answers 5xx to an empty or unauthorised request", not counts["server_errors"], counts["server_errors"][:12])
    for o in (ro, made["noc"]):
        r = o.http.post("/api/v1/auth/operators", json={"username": "esc-" + uuid.uuid4().hex[:5], "password": pw, "role": "superadmin"}, headers=o.bearer)
        check(R, f"{o.username.split('-')[0]} cannot create a superadmin", r.status_code == 403, r.status_code)
    r = made["noc"].http.get("/api/v1/auth/operators", headers=made["noc"].bearer)
    check(R, "noc cannot list operators", r.status_code == 403, r.status_code)
    r = made["noc"].http.get("/api/v1/bss/oauth-clients", headers=made["noc"].bearer)
    check(R, "noc cannot list integration clients", r.status_code == 403, r.status_code)
    # a disabled operator's token stops at once
    victim = made["noc"]
    ops = admin.http.get("/api/v1/auth/operators", headers=admin.bearer).json()
    ops = ops.get("operators") or ops.get("items") or ops
    vid = [o for o in ops if o.get("username") == victim.username][0]["id"]
    d = admin.http.delete(f"/api/v1/auth/operators/{vid}", headers=admin.bearer)
    r = victim.http.get("/api/v1/devices", headers=victim.bearer)
    check(R, "a removed operator's session stops working at once", r.status_code == 401, (d.status_code, r.status_code))

    # --- the adapter in strict mode ---------------------------------------------------------
    B = "adapter"
    r = admin.http.post("/api/v1/bss/oauth-clients", json={"name": "glob", "scopes": ["tmf:read"], "global_access": True}, headers=admin.bearer)
    if r.status_code in (200, 201):
        g = r.json()
        t = httpx.post(BSS + "/bss/v1/oauth/token", verify=CA, trust_env=False, data={"grant_type": "client_credentials", "client_id": g["client"]["client_id"], "client_secret": g["client_secret"]})
        check(B, "a fleet-wide client gets no token in strict mode", t.status_code in (400, 401, 403), (t.status_code, t.text[:100]))
    else:
        check(B, "a fleet-wide client cannot be created in strict mode", r.status_code in (400, 403), r.status_code)
    r = admin.http.post("/api/v1/bss/oauth-clients", json={"name": "c1", "customer_id": cust, "scopes": ["tmf:read", "tmf:write", "tmf:execute"]}, headers=admin.bearer)
    c1 = r.json()
    tok = httpx.post(BSS + "/bss/v1/oauth/token", verify=CA, trust_env=False, data={"grant_type": "client_credentials", "client_id": c1["client"]["client_id"], "client_secret": c1["client_secret"]}).json()["access_token"]
    bh = {"Authorization": "Bearer " + tok}

    def bss(method, path, base=BSS, **kw):
        for _ in range(8):
            r = httpx.request(method, base + path, headers={**bh, **kw.pop("headers", {})}, verify=CA, trust_env=False, timeout=20, **kw)
            if r.status_code != 429 or "ceiling" in r.text:
                return r
            time.sleep(float(r.headers.get("Retry-After") or 1))
        return r
    # allocation rule: only a serial the operator allocated to this customer
    other = admin.http.post("/api/v1/customers", json={"name": "Other ISP"}, headers=admin.bearer).json()["id"]
    sql("INSERT INTO devices (id, oui_serial, oui, product_class, serial_number, manufacturer, data_model_root, online_status) VALUES "
        f"(gen_random_uuid(), 'A4FC77+PC+FREE01', 'A4FC77', 'PC', 'FREE01', 'Nokia', 'DEVICE2', 'OFFLINE'), "
        f"(gen_random_uuid(), 'A4FC77+PC+THEIRS01', 'A4FC77', 'PC', 'THEIRS01', 'Nokia', 'DEVICE2', 'OFFLINE')")
    sql(f"UPDATE devices SET customer_id = '{other}' WHERE serial_number = 'THEIRS01'")
    answers = {}
    for label, serial in (("unallocated", "FREE01"), ("another customer's", "THEIRS01"), ("unknown", "NOSUCH01")):
        r = bss("POST", "/bss/v1/mappings", json={"account_id": "acct-" + label[:4], "oui_serial": f"A4FC77+PC+{serial}", "service_plan": "P"})
        answers[label] = (r.status_code, r.json().get("code"))
        check(B, f"mapping an {label} serial is refused", r.status_code in (403, 404, 409), answers[label])
    check(B, "the three refusals are indistinguishable", len(set(answers.values())) == 1, answers)
    r = bss("POST", "/bss/v1/mappings", json={"account_id": "acct-walk", "oui_serial": "A4FC77+PC+WALK01", "service_plan": "P"})
    check(B, "mapping an allocated serial works", r.status_code == 200, (r.status_code, r.text[:120]))
    # source allowlist: the adapter listens on loopback behind the proxy; a caller it does not list
    r = httpx.get(BSS_DIRECT + "/bss/v1/mappings/acct-walk", headers={**bh, "X-Forwarded-For": "203.0.113.50"}, trust_env=False)
    check(B, "a call the proxy reports as coming from an unlisted network is refused", r.status_code == 403, (r.status_code, r.text[:100]))
    r = httpx.get(BSS + "/bss/v1/mappings/acct-walk", headers={**bh, "X-Forwarded-For": "203.0.113.50"}, verify=CA, trust_env=False)
    check(B, "a client cannot choose its own source address through the TLS proxy (X-Forwarded-For is overwritten)", r.status_code == 200, (r.status_code, r.text[:100]))
    # a wifi password in clear is refused (handles only), and http webhooks
    r = bss("POST", "/bss/v1/orders", json={"external_order_id": "clear-pw", "account_id": "acct-walk", "action": "MODIFY_WIFI", "parameters": {"wifi_password": "Clear-text-pass-1"}})
    check(B, "a Wi-Fi password sent in clear is refused", r.status_code == 400, (r.status_code, r.text[:100]))
    # the hourly ceiling (set to 5 restarts for this run)
    seen = []
    for i in range(7):
        r = bss("POST", "/bss/v1/orders", json={"external_order_id": f"ceil-{i}", "account_id": "acct-walk", "action": "REBOOT"})
        seen.append(r.status_code)
    check(B, "the 6th and 7th restart in an hour are refused with Retry-After", seen[:5] == [202] * 5 and seen[5:] == [429, 429] and r.headers.get("Retry-After"), (seen, r.headers.get("Retry-After")))
    r = bss("POST", "/bss/v1/orders", json={"external_order_id": "fr-1", "account_id": "acct-walk", "action": "FACTORY_RESET", "parameters": {"confirm": "FACTORY_RESET"}})
    check(B, "factory reset is off by default in production", r.status_code == 400, (r.status_code, r.text[:100]))

    # --- server-side request forgery ---------------------------------------------------------
    F = "ssrf"
    targets = ["http://203.0.113.9/h", "https://127.0.0.1:28080/api/v1/customers", "https://localhost/h", "https://[::1]/h",
               "https://169.254.169.254/latest/meta-data/", "https://2130706433/h", "https://0x7f.0.0.1/h", "https://10.0.0.5/h",
               "https://127.0.0.1.nip.io/h", "https://user:pw@127.0.0.1/h", "file:///etc/passwd", "gopher://127.0.0.1:55432/"]
    accepted = []
    for t in targets:
        r = bss("POST", "/bss/v1/webhooks", json={"account_id": "acct-walk", "target_url": t, "secret": "s" * 40, "event_types": ["DeviceSeen"]})
        if r.status_code in (200, 201):
            accepted.append(t)
    note(F, "webhook targets accepted at creation (delivery is guarded separately)", accepted or "none")
    # make an event and let the dispatcher try: nothing may reach an internal address
    hits = []
    srv = socket.socket()
    srv.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    srv.bind(("127.0.0.1", 28999))
    srv.listen(5)
    srv.settimeout(0.5)
    r = bss("POST", "/bss/v1/webhooks", json={"account_id": "acct-walk", "target_url": "https://127.0.0.1:28999/hook", "secret": "s" * 40, "event_types": ["DeviceSeen", "DeviceUnreachable"]})
    note(F, "webhook to the ACS host's own loopback, at creation", (r.status_code, r.text[:120]))
    sql(f"UPDATE devices SET online_status = 'ONLINE', last_inform_at = now() WHERE id = '{dev}'")
    sql(f"UPDATE devices SET online_status = 'OFFLINE' WHERE id = '{dev}'")
    t0 = time.time()
    while time.time() - t0 < 20:
        try:
            conn, _ = srv.accept()
            hits.append(conn.getpeername())
            conn.close()
        except TimeoutError:
            pass
    srv.close()
    check(F, "no webhook delivery reached a loopback address on the ACS host", not hits, hits)
    d = sql("select target_url, last_error from webhook_deliveries d join webhook_subscriptions s on s.id = d.subscription_id order by d.created_at desc limit 3")[1]
    note(F, "what the dispatcher recorded", d[:400])
    # the operator's device web-GUI proxy config
    for t in ("http://127.0.0.1:28080/", "http://169.254.169.254/", "http://localhost:55432/"):
        r = admin.http.put(f"/api/v1/devices/{dev}/webgui", json={"url": t, "base_url": t, "scheme": "http", "host": t.split("/")[2].split(":")[0], "port": 80}, headers=admin.bearer)
        note(F, f"device web-GUI target {t}", (r.status_code, r.text[:120]))

    # --- CWMP parser ------------------------------------------------------------------------
    W = "cwmp"
    ctx = ssl.create_default_context(cafile=CA)
    boot = httpx.DigestAuth(os.environ["ACS_CWMP_BOOTSTRAP_USERNAME"], os.environ["ACS_CWMP_BOOTSTRAP_PASSWORD"])
    env = lambda body, extra="": (  # noqa: E731
        f'<?xml version="1.0"?>{extra}<soap-env:Envelope xmlns:soap-env="http://schemas.xmlsoap.org/soap/envelope/" '
        'xmlns:cwmp="urn:dslforum-org:cwmp-1-0"><soap-env:Header><cwmp:ID soap-env:mustUnderstand="1">1</cwmp:ID></soap-env:Header>'
        f"<soap-env:Body>{body}</soap-env:Body></soap-env:Envelope>")
    inform = lambda serial, mfr="Nokia": (  # noqa: E731
        f"<cwmp:Inform><DeviceId><Manufacturer>{mfr}</Manufacturer><OUI>A4FC77</OUI><ProductClass>PC</ProductClass>"
        f"<SerialNumber>{serial}</SerialNumber></DeviceId><Event><EventStruct><EventCode>0 BOOTSTRAP</EventCode><CommandKey></CommandKey>"
        "</EventStruct></Event><MaxEnvelopes>1</MaxEnvelopes><CurrentTime>2026-11-18T10:00:00Z</CurrentTime><RetryCount>0</RetryCount>"
        "<ParameterList></ParameterList></cwmp:Inform>")
    with httpx.Client(verify=ctx, trust_env=False, timeout=20) as h:
        r = h.post(CWMP, content=env(inform("X")))
        check(W, "an Inform without credentials -> 401", r.status_code == 401, r.status_code)
        r = h.post(CWMP, content=env(inform("X")), auth=httpx.DigestAuth("acs-device", "wrong"))
        check(W, "a wrong device password -> 401", r.status_code == 401, r.status_code)
        r = h.post(CWMP, content=env(inform("X")), auth=(os.environ["ACS_CWMP_BOOTSTRAP_USERNAME"], os.environ["ACS_CWMP_BOOTSTRAP_PASSWORD"]))
        check(W, "Basic authentication is refused even with the right password", r.status_code == 401, r.status_code)
        r = h.post(CWMP, content=env(inform("WALK01")), auth=httpx.DigestAuth(os.environ["ACS_DIGEST_USERNAME"], os.environ["ACS_DIGEST_PASSWORD"]))
        note(W, "the shared device credential for a known serial (production forbids it for established devices)", r.status_code)
    xxe = '<!DOCTYPE x [<!ENTITY xxe SYSTEM "file:///etc/passwd">]>'
    with httpx.Client(verify=ctx, trust_env=False, timeout=20, auth=boot) as h:
        r = h.post(CWMP, content=env(inform("XXE01", "&xxe;"), xxe))
        leaked = sql("select manufacturer from devices where serial_number = 'XXE01'")[1]
        check(W, "an external entity in an Inform reads no file", "root:" not in r.text and "root:" not in leaked, (r.status_code, leaked[:60]))
    bomb = "<!DOCTYPE b [" + '<!ENTITY a "' + "A" * 1000 + '">' + "".join(
        f'<!ENTITY {chr(98 + i)} "{("&" + chr(97 + i) + ";") * 10}">' for i in range(8)) + "]>"
    with httpx.Client(verify=ctx, trust_env=False, timeout=30, auth=boot) as h:
        t0 = time.time()
        try:
            r = h.post(CWMP, content=env(inform("BOMB01", "&i;"), bomb))
            status = r.status_code
        except Exception as e:  # noqa: BLE001
            status = type(e).__name__
        took = time.time() - t0
        alive = httpx.get("https://127.0.0.1:27547/healthz", verify=ctx, trust_env=False).status_code
        check(W, "an entity-expansion bomb is rejected quickly and the gateway stays up", took < 5 and alive == 200, (status, f"{took:.1f}s", alive))
    with httpx.Client(verify=ctx, trust_env=False, timeout=30, auth=boot) as h:
        try:
            r = h.post(CWMP, content=env(inform("BIG01", "M" * (8 << 20))))
            status = r.status_code
        except Exception as e:  # noqa: BLE001
            status = type(e).__name__
        check(W, "an 8 MiB Inform is refused", status in (400, 413, "RemoteProtocolError", "WriteError", "ReadError") , status)
        r = h.post(CWMP, content=b"\x00\xff<<<not xml")
        check(W, "garbage instead of XML -> 400, no 5xx", r.status_code == 400, r.status_code)
        r = h.post(CWMP, content=env(inform("S2-" + uuid.uuid4().hex[:6])))
        note(W, "the bootstrap credential with a serial the ACS has never seen", r.status_code)
        n = sql("select count(*) from devices where serial_number like 'S2-%'")[1]
        note(W, "…and whether that created a device row", n)
    # a slow client: headers never finished
    s = ctx.wrap_socket(socket.create_connection(("127.0.0.1", 27547)), server_hostname="127.0.0.1")
    s.sendall(b"POST /cwmp HTTP/1.1\r\nHost: x\r\nX-A: ")
    s.settimeout(40)
    t0 = time.time()
    try:
        closed = s.recv(1) == b""
    except (TimeoutError, OSError):
        closed = False
    check(W, f"a connection that never finishes its headers is dropped (after {time.time() - t0:.0f}s)", closed and time.time() - t0 < 35, time.time() - t0)

    # --- per-service database logins ---------------------------------------------------------
    D = "database"
    creds = {}
    for var, role in (("ACS_BSS_POSTGRES_DSN", "acs_bss"), ("ACS_CWMP_POSTGRES_DSN", "acs_gateway"), ("ACS_API_POSTGRES_DSN", "acs_api")):
        m = re.match(r"postgres://([^:]+):([^@]+)@", os.environ[var])
        creds[role] = m.group(2)
    for role, statement, what in (
        ("acs_bss", "select password_hash from operators limit 1", "the adapter's login reads operator password hashes"),
        ("acs_bss", "select * from operator_mfa limit 1", "the adapter's login reads two-step sign-in secrets"),
        ("acs_bss", "select * from device_credentials limit 1", "the adapter's login reads device credentials"),
        ("acs_bss", "update operators set role = 'superadmin'", "the adapter's login changes operators"),
        ("acs_bss", "create table pwn (x int)", "the adapter's login creates tables"),
        ("acs_bss", "drop table devices", "the adapter's login drops tables"),
        ("acs_gateway", "select client_secret_hash from bss_oauth_clients limit 1", "the device gateway's login reads integration client secrets"),
        ("acs_gateway", "select password_hash from operators limit 1", "the device gateway's login reads operator password hashes"),
        ("acs_gateway", "delete from audit_log", "the device gateway's login deletes the audit log"),
        ("acs_api", "delete from audit_log", "the API's login deletes the audit log"),
        ("acs_api", "alter table operators add column x int", "the API's login alters the schema"),
        ("acs_api", "create role evil superuser", "the API's login creates roles"),
    ):
        rc, out = sql(statement, role, creds[role])
        check(D, f"{what} -> denied", rc != 0 and ("permission denied" in out or "must be" in out or "does not exist" in out), out[:140])
    rc, out = sql("select rolname, rolsuper, rolcreaterole, rolcreatedb, rolbypassrls from pg_roles where rolname like 'acs_%'")
    note(D, "service roles (name|super|createrole|createdb|bypassrls)", out)

    bad = [o for o in OUT if o["ok"] is False]
    print(f"\n{sum(1 for o in OUT if o['ok'])} passed, {len(bad)} failed, {sum(1 for o in OUT if o['ok'] is None)} notes")
    if bad:
        raise SystemExit(1)


if __name__ == "__main__":
    main()
