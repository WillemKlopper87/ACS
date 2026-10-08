#!/usr/bin/env python3
"""Run an explicit lab scenario through ACS and export firmware qualification evidence.

Uses ACS_API_TOKEN; scenario writes and reboot are opt-in and require --lab-actions.
Reports contain names/statuses and explicitly selected DeviceInfo identity,
never operational parameter values or credentials.
"""
import argparse
import importlib.util
import json
import os
from pathlib import Path
import time
import uuid
import urllib.parse

spec = importlib.util.spec_from_file_location("discovery", Path(__file__).with_name("discover-devices.py"))
discovery = importlib.util.module_from_spec(spec)
spec.loader.exec_module(discovery)


def wait_job(acs, key, timeout, poll=2):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        job = acs.request("/api/v1/jobs/" + key)
        if job["status"] == "SUCCESS":
            return {"command_key": key, "status": "SUCCESS"}
        if job["status"] in ("FAILED", "TIMEOUT", "CANCELLED"):
            return {"command_key": key, "status": job["status"], "fault_code": job.get("fault_code")}
        time.sleep(poll)
    return {"command_key": key, "status": "WAIT_TIMEOUT"}


def qualify(acs, device, scenario, timeout, lab_actions=False):
    uuid.UUID(device)
    if (scenario.get("writes") or scenario.get("reboot")) and not lab_actions:
        raise ValueError("writes/reboot require --lab-actions and a designated lab device")
    prefix = "/api/v1/devices/" + device
    report = {"device_id": device, "scenario": scenario.get("name", "unnamed"),
              "captured_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()), "tests": []}
    exported = discovery.discover(acs, device, timeout, 2)
    report["discovery"] = exported["snapshot"]
    names = exported["snapshot"].get("names", {})
    root = exported["snapshot"].get("root", "Device.")
    if root not in ("Device.", "InternetGatewayDevice."):
        raise ValueError("unsupported discovery root")
    identity = scenario.get("identity_fields", [])
    expected = scenario.get("expected_identity", {})
    identity = list(dict.fromkeys(identity + list(expected)))
    allowed_identity = {"Manufacturer", "ModelName", "ProductClass", "HardwareVersion", "SoftwareVersion", "SerialNumber"}
    if any(field not in allowed_identity for field in identity):
        raise ValueError("identity fields must be portable DeviceInfo metadata")
    if identity:
        paths = [root + "DeviceInfo." + field for field in identity if root + "DeviceInfo." + field in names]
        if not paths or any(root + "DeviceInfo." + field not in names for field in expected):
            report.update(passed=False, identity_check={"status": "MISSING_CAPABILITY"})
            return report
        queued = acs.request(prefix + "/parameters/get", {"paths": paths})
        outcome = wait_job(acs, queued["command_key"], timeout)
        report["tests"].append({"operation": "identity_read", "paths": paths, **outcome})
        if outcome["status"] != "SUCCESS":
            report.update(passed=False, identity_check={"status": "READ_FAILED"})
            return report
        cached = acs.request(prefix + "/parameters?paths=" + urllib.parse.quote(",".join(paths), safe=""))["parameters"]
        actual = {field: cached.get(root + "DeviceInfo." + field, {}).get("value") for field in identity}
        mismatched = [field for field, accepted in expected.items() if not isinstance(accepted, list) or actual.get(field) not in accepted]
        report["identity_check"] = {"status": "MISMATCH" if mismatched else "SUCCESS", "mismatched_fields": mismatched}
        report["identity_observed"] = actual
        if mismatched:
            report["passed"] = False
            return report
    if scenario.get("read_discovered_5g"):
        write_only = {c.get("path") for c in exported["snapshot"].get("capabilities", []) if c.get("access") == "PARAM_WRITE_ONLY"}
        sensitive = ("password", "passphrase", "secret", "token", "key", "imsi", "imei", "iccid")
        keywords = ("rsrp", "rsrq", "sinr", "rssi", "rat", "registration", "networktype", "band", "cellid", "arfcn", "beam", "qos", "slice")
        candidates = sorted(path for path in names if not path.endswith(".") and not any(c in path for c in "{*[()")
                          and path not in write_only and any(k in path.lower() for k in ("cellular", "5g", "sessionmanagement"))
                          and not any(k in path.lower() for k in sensitive)
                          and any(k in path.rsplit(".", 1)[-1].lower() for k in keywords))
        selected = candidates[:128]
        report["fiveg_inventory"] = {"paths": selected, "candidate_count": len(candidates), "truncated": len(candidates) > 128, "scope": "Discovered readable candidates; units require dictionary/vendor evidence."}
        if selected:
            queued = acs.request(prefix + "/parameters/get", {"paths": selected})
            report["tests"].append({"operation": "discovered_5g_read", "paths": selected, **wait_job(acs, queued["command_key"], timeout)})
    tables = scenario.get("instances", [])
    if tables:
        queued = acs.request(prefix + "/discover-instances", {"paths": tables})
        report["tests"].append({"operation": "instances", **wait_job(acs, queued["command_key"], timeout)})
    if scenario.get("reads"):
        queued = acs.request(prefix + "/parameters/get", {"paths": scenario["reads"]})
        report["tests"].append({"operation": "read", "paths": scenario["reads"], **wait_job(acs, queued["command_key"], timeout)})
    if scenario.get("writes"):
        if any(t["status"] != "SUCCESS" for t in report["tests"]):
            report.update(passed=False, writes_skipped="Read/instance prerequisites failed")
            return report
        queued = acs.request(prefix + "/parameters", {"parameters": scenario["writes"]}, method="PUT")
        report["tests"].append({"operation": "write_readback", "paths": [p["name"] for p in scenario["writes"]], **wait_job(acs, queued["command_key"], timeout)})
    if scenario.get("reboot"):
        queued = acs.request(prefix + "/reboot", {})
        report["tests"].append({"operation": "reboot", **wait_job(acs, queued["command_key"], timeout)})
        # A read proves continued communication, but cannot witness the reboot itself.
        queued = acs.request(prefix + "/parameters/get", {"paths": ["Device.DeviceInfo.SoftwareVersion"]})
        report["tests"].append({"operation": "post_reboot_read", **wait_job(acs, queued["command_key"], timeout)})
        report["reboot_witness"] = "Physical restart and transport reconnection require independent observation."
    report["compatibility"] = acs.request(prefix + "/compatibility")
    report["passed"] = bool(report["tests"]) and all(t["status"] == "SUCCESS" for t in report["tests"])
    report["scope"] = "Scenario evidence only. Does not automatically mark firmware qualified or claim BBF certification."
    return report


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--base-url", required=True)
    parser.add_argument("--device", required=True)
    parser.add_argument("--scenario", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--timeout", type=int, default=600)
    parser.add_argument("--lab-actions", action="store_true")
    args = parser.parse_args()
    if args.timeout <= 0:
        parser.error("timeout must be positive")
    token = os.environ.get("ACS_API_TOKEN")
    if not token:
        parser.error("set ACS_API_TOKEN")
    scenario = json.loads(args.scenario.read_text(encoding="utf-8"))
    report = qualify(discovery.ACS(args.base_url, token), str(uuid.UUID(args.device)), scenario, args.timeout, args.lab_actions)
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(json.dumps(report, indent=2) + "\n", encoding="utf-8")
    return 0 if report["passed"] else 1


if __name__ == "__main__":
    raise SystemExit(main())
