"""A small simulated TR-069 router (TR-181 data model) for the live ACS qualification run.

It speaks CWMP to a real ACS: Inform, then answers whatever the ACS asks until the ACS ends the
session (204). It keeps a parameter tree, applies SetParameterValues, and records every RPC it was
sent, so the test can assert what really reached "the router".
"""

from __future__ import annotations

import re
import time
from xml.sax.saxutils import escape

import httpx

NS = (
    'xmlns:soap-env="http://schemas.xmlsoap.org/soap/envelope/" '
    'xmlns:soap-enc="http://schemas.xmlsoap.org/soap/encoding/" '
    'xmlns:xsd="http://www.w3.org/2001/XMLSchema" '
    'xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" '
    'xmlns:cwmp="urn:dslforum-org:cwmp-1-0"'
)


def envelope(body: str, cwmp_id: str) -> str:
    return (
        f'<?xml version="1.0" encoding="UTF-8"?><soap-env:Envelope {NS}><soap-env:Header>'
        f'<cwmp:ID soap-env:mustUnderstand="1">{escape(cwmp_id)}</cwmp:ID></soap-env:Header>'
        f"<soap-env:Body>{body}</soap-env:Body></soap-env:Envelope>"
    )


class SimCpe:
    def __init__(
        self,
        url: str,
        user: str,
        password: str,
        *,
        serial: str = "S230QE2E00001",
        oui: str = "001349",
        product_class: str = "NR5103",
        sim: tuple[str, str] | None = None,
    ):
        self.url = url
        self.auth = httpx.DigestAuth(user, password)
        self.oui, self.product_class, self.serial = oui, product_class, serial
        # (ICCID, IMSI) of the SIM in a cellular router; None for a router with no modem
        self.sim = sim
        self.booted_at = time.time() - 4242
        self.user = user
        self.own_credential = False  # True once the ACS has given this router its own
        self.rpcs: list[tuple[str, str]] = []  # (method, raw xml) in the order received
        self.pending_events: list[tuple[str, str]] = [("0 BOOTSTRAP", ""), ("1 BOOT", "")]
        self.factory_resets = 0
        self.reboots = 0
        self.params: dict[str, str] = {}
        self._factory_defaults()

    def _factory_defaults(self) -> None:
        self.params = {
            "Device.DeviceInfo.Manufacturer": "Zyxel",
            "Device.DeviceInfo.ManufacturerOUI": self.oui,
            "Device.DeviceInfo.ProductClass": self.product_class,
            "Device.DeviceInfo.SerialNumber": self.serial,
            "Device.DeviceInfo.ModelName": "NR5103",
            "Device.DeviceInfo.HardwareVersion": "1.0",
            "Device.DeviceInfo.SoftwareVersion": "2.3.1",
            "Device.DeviceInfo.UpTime": "0",
            "Device.Hosts.HostNumberOfEntries": "3",
            "Device.ManagementServer.ConnectionRequestURL": "http://127.0.0.1:1/unreachable",
            "Device.ManagementServer.ParameterKey": "",
            "Device.ManagementServer.URL": self.url,
            "Device.ManagementServer.Username": self.user,
            "Device.ManagementServer.Password": "",
            "Device.ManagementServer.PeriodicInformEnable": "true",
            "Device.ManagementServer.PeriodicInformInterval": "300",
            "Device.WiFi.SSIDNumberOfEntries": "1",
            "Device.WiFi.AccessPointNumberOfEntries": "1",
            "Device.WiFi.RadioNumberOfEntries": "1",
            "Device.WiFi.Radio.1.Enable": "true",
            "Device.WiFi.Radio.1.OperatingFrequencyBand": "2.4GHz",
            "Device.WiFi.SSID.1.Enable": "true",
            "Device.WiFi.SSID.1.SSID": "Factory-WiFi",
            "Device.WiFi.SSID.1.LowerLayers": "Device.WiFi.Radio.1",
            "Device.WiFi.AccessPoint.1.Enable": "true",
            "Device.WiFi.AccessPoint.1.SSIDReference": "Device.WiFi.SSID.1",
            "Device.WiFi.AccessPoint.1.Security.ModeEnabled": "WPA2-Personal",
            "Device.WiFi.AccessPoint.1.Security.KeyPassphrase": "factory-passphrase",
            "Device.WiFi.AccessPoint.1.AssociatedDeviceNumberOfEntries": "0",
        }
        if self.sim is not None:  # the SIM and the modem survive a factory reset
            iccid, imsi = self.sim
            self.params.update(
                {
                    "Device.Cellular.InterfaceNumberOfEntries": "1",
                    "Device.Cellular.Interface.1.Enable": "true",
                    "Device.Cellular.Interface.1.Status": "Up",
                    "Device.Cellular.Interface.1.IMEI": "356938035643809",
                    "Device.Cellular.Interface.1.USIM.Status": "Valid",
                    "Device.Cellular.Interface.1.USIM.ICCID": iccid,
                    "Device.Cellular.Interface.1.USIM.IMSI": imsi,
                    "Device.Cellular.Interface.1.RSRP": "-97",
                    "Device.Cellular.Interface.1.RSRQ": "-11",
                    "Device.Cellular.Interface.1.SINR": "14",
                    "Device.Cellular.Interface.1.RSSI": "-66",
                }
            )

    # -- values ---------------------------------------------------------------------------
    def value(self, name: str) -> str:
        if name == "Device.DeviceInfo.UpTime":
            return str(int(time.time() - self.booted_at))
        return self.params[name]

    def _matching(self, path: str) -> list[str]:
        if path == "" or path.endswith("."):
            return sorted(n for n in self.params if n.startswith(path))
        return [path] if path in self.params else []

    # -- one CWMP session -----------------------------------------------------------------
    def session(self) -> list[str]:
        """Inform, then serve the ACS until it has nothing more. Returns the methods served."""
        served: list[str] = []
        events = self.pending_events or [("2 PERIODIC", "")]
        self.pending_events = []
        ev = "".join(
            f"<EventStruct><EventCode>{escape(c)}</EventCode><CommandKey>{escape(k)}</CommandKey>"
            "</EventStruct>"
            for c, k in events
        )
        informed = (
            "Device.DeviceInfo.SoftwareVersion",
            "Device.DeviceInfo.HardwareVersion",
            "Device.ManagementServer.ConnectionRequestURL",
            "Device.ManagementServer.ParameterKey",
        )
        pl = "".join(
            f"<ParameterValueStruct><Name>{n}</Name>"
            f'<Value xsi:type="xsd:string">{escape(self.value(n))}</Value></ParameterValueStruct>'
            for n in informed
        )
        inform = (
            "<cwmp:Inform><DeviceId><Manufacturer>Zyxel</Manufacturer>"
            f"<OUI>{self.oui}</OUI><ProductClass>{self.product_class}</ProductClass>"
            f"<SerialNumber>{self.serial}</SerialNumber></DeviceId>"
            f'<Event soap-enc:arrayType="cwmp:EventStruct[{len(events)}]">{ev}</Event>'
            "<MaxEnvelopes>1</MaxEnvelopes>"
            f"<CurrentTime>{time.strftime('%Y-%m-%dT%H:%M:%SZ', time.gmtime())}</CurrentTime>"
            "<RetryCount>0</RetryCount>"
            f'<ParameterList soap-enc:arrayType="cwmp:ParameterValueStruct[{len(informed)}]">{pl}'
            "</ParameterList></cwmp:Inform>"
        )
        with httpx.Client(auth=self.auth, timeout=20) as http:
            r = http.post(self.url, content=envelope(inform, "inform-1"), headers=_XML)
            if r.status_code != 200 or "InformResponse" not in r.text:
                raise RuntimeError(f"inform refused: {r.status_code} {r.text[:300]}")
            body = ""
            for _ in range(200):
                r = http.post(self.url, content=body, headers=_XML if body else {})
                if r.status_code == 204 or not r.text.strip():
                    return served
                if r.status_code != 200:
                    raise RuntimeError(f"session broke: {r.status_code} {r.text[:300]}")
                method, body = self._answer(r.text)
                served.append(method)
        raise RuntimeError("the ACS never ended the session")

    def _answer(self, xml: str) -> tuple[str, str]:
        m = re.search(r"<(?:[\w-]+:)?Body[^>]*>\s*<(?:[\w-]+:)?(\w+)", xml)
        method = m.group(1) if m else "?"
        idm = re.search(r"<(?:[\w-]+:)?ID[^>]*>([^<]*)<", xml)
        cwmp_id = idm.group(1) if idm else "x"
        self.rpcs.append((method, xml))
        handler = getattr(self, f"_rpc_{method}", None)
        if handler is None:
            return method, envelope(_fault(9000, "Method not supported"), cwmp_id)
        return method, envelope(handler(xml), cwmp_id)

    # -- RPCs -----------------------------------------------------------------------------
    def _rpc_GetRPCMethods(self, _xml: str) -> str:
        methods = [n[5:] for n in dir(self) if n.startswith("_rpc_")]
        items = "".join(f"<string>{m}</string>" for m in methods)
        return (
            "<cwmp:GetRPCMethodsResponse><MethodList "
            f'soap-enc:arrayType="xsd:string[{len(methods)}]">{items}</MethodList>'
            "</cwmp:GetRPCMethodsResponse>"
        )

    def _rpc_GetParameterValues(self, xml: str) -> str:
        names: list[str] = []
        for asked in re.findall(r"<string[^>]*>([^<]*)</string>", xml):
            found = self._matching(asked)
            if not found:
                return _fault(9005, f"Invalid parameter name: {asked}")
            names.extend(found)
        items = "".join(
            f"<ParameterValueStruct><Name>{n}</Name>"
            f'<Value xsi:type="xsd:string">{escape(self.value(n))}</Value></ParameterValueStruct>'
            for n in names
        )
        return (
            "<cwmp:GetParameterValuesResponse><ParameterList "
            f'soap-enc:arrayType="cwmp:ParameterValueStruct[{len(names)}]">{items}'
            "</ParameterList></cwmp:GetParameterValuesResponse>"
        )

    def _rpc_GetParameterNames(self, xml: str) -> str:
        pm = re.search(r"<ParameterPath[^>]*>([^<]*)</ParameterPath>", xml)
        path = pm.group(1) if pm else ""
        next_level = re.search(r"<NextLevel[^>]*>\s*(1|true)\s*</NextLevel>", xml) is not None
        names: set[str] = set()
        for n in self.params:
            if not n.startswith(path):
                continue
            if next_level:
                rest = n[len(path) :]
                head = rest.split(".", 1)
                names.add(path + head[0] + ("." if len(head) > 1 else ""))
            else:
                names.add(n)
                parts = n.split(".")
                for i in range(1, len(parts)):
                    obj = ".".join(parts[:i]) + "."
                    if obj.startswith(path):
                        names.add(obj)
        if not names:
            return _fault(9005, f"Invalid parameter name: {path}")
        items = "".join(
            f"<ParameterInfoStruct><Name>{n}</Name><Writable>1</Writable></ParameterInfoStruct>"
            for n in sorted(names)
        )
        return (
            "<cwmp:GetParameterNamesResponse><ParameterList "
            f'soap-enc:arrayType="cwmp:ParameterInfoStruct[{len(names)}]">{items}'
            "</ParameterList></cwmp:GetParameterNamesResponse>"
        )

    def _rpc_SetParameterValues(self, xml: str) -> str:
        pairs = re.findall(
            r"<Name[^>]*>([^<]*)</Name>\s*<Value[^>]*>([^<]*)</Value>", xml, flags=re.S
        )
        for name, _ in pairs:
            if name not in self.params:
                return _fault(9005, f"Invalid parameter name: {name}")
        for name, value in pairs:
            self.params[name] = _unescape(value)
        # The ACS hands a router its own credential on first contact (it arrived with the
        # fleet's bootstrap one). From its next session the router signs in with that.
        names = dict(pairs)
        user = names.get("Device.ManagementServer.Username")
        password = names.get("Device.ManagementServer.Password")
        if user is not None and password is not None:
            self.user = _unescape(user)
            self.auth = httpx.DigestAuth(self.user, _unescape(password))
            self.own_credential = True
        key = re.search(r"<ParameterKey[^>]*>([^<]*)</ParameterKey>", xml)
        if key:
            self.params["Device.ManagementServer.ParameterKey"] = key.group(1)
        return (
            "<cwmp:SetParameterValuesResponse><Status>0</Status></cwmp:SetParameterValuesResponse>"
        )

    def _rpc_SetParameterAttributes(self, _xml: str) -> str:
        return "<cwmp:SetParameterAttributesResponse/>"

    def _rpc_Reboot(self, xml: str) -> str:
        key = re.search(r"<CommandKey[^>]*>([^<]*)</CommandKey>", xml)
        self.reboots += 1
        self.booted_at = time.time()
        self.pending_events = [("1 BOOT", ""), ("M Reboot", key.group(1) if key else "")]
        return "<cwmp:RebootResponse/>"

    def _rpc_FactoryReset(self, _xml: str) -> str:
        self.factory_resets += 1
        self.booted_at = time.time()
        user = self.user
        self._factory_defaults()
        self.params["Device.ManagementServer.Username"] = user
        self.pending_events = [("0 BOOTSTRAP", ""), ("1 BOOT", "")]
        return "<cwmp:FactoryResetResponse/>"


_XML = {"Content-Type": 'text/xml; charset="utf-8"', "SOAPAction": ""}


def _fault(code: int, text: str) -> str:
    return (
        "<soap-env:Fault><faultcode>Client</faultcode><faultstring>CWMP fault</faultstring>"
        f"<detail><cwmp:Fault><FaultCode>{code}</FaultCode>"
        f"<FaultString>{escape(text)}</FaultString></cwmp:Fault></detail></soap-env:Fault>"
    )


def _unescape(v: str) -> str:
    return v.replace("&lt;", "<").replace("&gt;", ">").replace("&quot;", '"').replace("&amp;", "&")
