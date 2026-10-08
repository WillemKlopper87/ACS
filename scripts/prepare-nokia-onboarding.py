#!/usr/bin/env python3
"""Prepare a redacted Nokia onboarding handoff without contacting a device or ACS.

Read credentials from process environment only. This checks preparation, not
server reachability, TLS trust, migration state or physical CPE compatibility.
"""
import argparse
import ipaddress
import json
import os
from pathlib import Path
import urllib.parse

IDENTITY = {"manufacturer_reported": "Nokia", "model": None, "serial": None,
            "hardware_version": "3TG02241AAAA", "software_version": "1.2501.00.0798",
            "software_build": "5GGW2-OMNI-2_R250100BiwT0301E0798", "country_id": "eu"}


def prepare(acs_url, environment, management_ip="192.168.1.1"):
    ipaddress.ip_address(management_ip)
    checks = []
    def record(name, ready, action):
        checks.append({"check": name, "state": "prepared" if ready else "pending", "action": action})
    url = urllib.parse.urlsplit(acs_url or "")
    try:
        port = url.port or (443 if url.scheme == "https" else 80)
        valid = (url.scheme in ("https", "http") and bool(url.hostname) and not url.username
                 and not url.password and not url.query and not url.fragment)
    except ValueError:
        valid, port = False, None
    production = environment.get("ACS_DEPLOYMENT_PROFILE") == "production"
    record("deployment_profile", environment.get("ACS_DEPLOYMENT_PROFILE") in ("lab", "production"),
           "Select lab or production explicitly; run the existing security preflight for production.")
    record("acs_url", valid and (not production or url.scheme == "https"),
           "Provide the exact CPE ACS URL; production requires HTTPS and a trusted certificate.")
    for name in ("NOKIA_ACS_USERNAME", "NOKIA_ACS_PASSWORD", "NOKIA_ACSCR_USERNAME", "NOKIA_ACSCR_PASSWORD"):
        value = environment.get(name, "")
        record(name, bool(value.strip()) and not value.lower().startswith(("change-me", "placeholder")),
               "Install through the existing device credential workflow; no secret is exported.")
    record("database_configured", bool(environment.get("ACS_POSTGRES_DSN")),
           "Run the read-only migration check on the target database; apply pending migrations separately.")
    record("credential_encryption_configured", len(environment.get("ACS_CREDENTIAL_ENCRYPTION_KEY", "").encode()) >= 16,
           "Use the same credential encryption key on the API and CWMP services.")
    scenario = {"name": "nokia-reported-firmware-read-only", "identity_fields":
                ["Manufacturer", "ModelName", "ProductClass", "HardwareVersion", "SoftwareVersion"],
                "expected_identity": {"HardwareVersion": [IDENTITY["hardware_version"]],
                                      "SoftwareVersion": [IDENTITY["software_version"], IDENTITY["software_build"]]},
                "reads": [], "read_discovered_5g": True, "writes": [], "reboot": False}
    report = {"identity_reported": dict(IDENTITY), "management_ip": management_ip,
              "prepared": all(c["state"] == "prepared" for c in checks), "checks": checks,
              "hardware_qualified": False, "device_contacted": False,
              "network": {"cwmp_destination_port": port if valid else None,
                          "cwmp_transport": "TCP", "optional_stun_destination_port": 3478,
                          "connection_request_port": "pending device discovery"},
              "acceptance_pending": ["Target server readiness and migrations", "TLS and firewall validation",
                                     "Confirmed model, serial and OUI", "First authenticated Inform",
                                     "Discovered paths and exact-firmware physical qualification"],
              "scope": "Offline preparation only; prepared does not mean deployed or hardware qualified."}
    return report, scenario


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--acs-url", default=os.environ.get("NOKIA_ACS_URL", ""))
    parser.add_argument("--management-ip", default="192.168.1.1")
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    report, scenario = prepare(args.acs_url, os.environ, args.management_ip)
    args.output.mkdir(parents=True, exist_ok=True)
    for name, content in (("readiness.json", report), ("scenario.json", scenario)):
        (args.output / name).write_text(json.dumps(content, indent=2) + "\n", encoding="utf-8")
    print("Preparation report saved. Outstanding checks: " + str(sum(c["state"] == "pending" for c in report["checks"])))
    return 0 if report["prepared"] else 1


if __name__ == "__main__":
    raise SystemExit(main())
