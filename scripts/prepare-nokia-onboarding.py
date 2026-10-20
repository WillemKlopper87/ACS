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


def _present(environment, name):
    value = environment.get(name, "").strip()
    return bool(value) and not value.lower().startswith(("change-me", "placeholder"))


def prepare(acs_url, environment, management_ip="192.168.1.1"):
    ipaddress.ip_address(management_ip)
    checks = []

    def record(name, ready, action, blocking=True):
        checks.append({"check": name, "state": "prepared" if ready else "pending",
                       "blocking": blocking, "action": action})

    url = urllib.parse.urlsplit(acs_url or "")
    try:
        port = url.port or (443 if url.scheme == "https" else 80)
        valid = (url.scheme in ("https", "http") and bool(url.hostname) and not url.username
                 and not url.password and not url.query and not url.fragment)
    except ValueError:
        valid, port = False, None

    profile = environment.get("ACS_DEPLOYMENT_PROFILE")
    production = profile == "production"
    lab = profile == "lab"
    record("deployment_profile", profile in ("lab", "production"),
           "Select lab or production explicitly; use lab for the first controlled capture if Nokia TLS behavior is still unknown.")

    record("acs_url", valid and (not production or url.scheme == "https"),
           "Provide the exact CPE ACS URL. Production requires HTTPS with a certificate the Nokia trusts.")

    bootstrap_ready = _present(environment, "ACS_CWMP_BOOTSTRAP_USERNAME") and _present(environment, "ACS_CWMP_BOOTSTRAP_PASSWORD")
    lab_legacy_ready = _present(environment, "NOKIA_ACS_USERNAME") and _present(environment, "NOKIA_ACS_PASSWORD")
    if production:
        credential_mode = "production_bootstrap" if bootstrap_ready else "missing"
        record("cwmp_first_contact_credentials", bootstrap_ready,
               "Set ACS_CWMP_BOOTSTRAP_USERNAME/PASSWORD. Production first contact must use the constrained bootstrap identity and then graduate to a unique device-bound credential.")
    elif lab:
        if bootstrap_ready:
            credential_mode = "constrained_bootstrap"
        elif lab_legacy_ready:
            credential_mode = "lab_compatibility"
        else:
            credential_mode = "missing"
        record("cwmp_first_contact_credentials", bootstrap_ready or lab_legacy_ready,
               "Prefer ACS_CWMP_BOOTSTRAP_USERNAME/PASSWORD. NOKIA_ACS_USERNAME/PASSWORD is accepted only as a lab compatibility input, never as the production steady-state identity.")
    else:
        credential_mode = "missing"
        record("cwmp_first_contact_credentials", False,
               "Choose the deployment profile first, then configure the corresponding first-contact credentials.")

    cr_user = _present(environment, "NOKIA_ACSCR_USERNAME")
    cr_password = _present(environment, "NOKIA_ACSCR_PASSWORD")
    record("connection_request_credentials", cr_user and cr_password,
           "Optional for first contact. Configure device-specific Connection Request credentials after the Nokia advertises its ConnectionRequestURL/auth behavior.",
           blocking=False)

    record("database_configured", bool(environment.get("ACS_POSTGRES_DSN")),
           "Run the read-only migration check on the target database; apply pending migrations separately.")

    encryption_ready = len(environment.get("ACS_CREDENTIAL_ENCRYPTION_KEY", "").encode()) >= 16
    record("credential_encryption_configured", encryption_ready,
           "Use the same credential encryption key on the API and CWMP services. It is required for secure per-device credential graduation.")

    scenario = {"name": "nokia-reported-firmware-read-only", "identity_fields":
                ["Manufacturer", "ModelName", "ProductClass", "HardwareVersion", "SoftwareVersion"],
                "expected_identity": {"HardwareVersion": [IDENTITY["hardware_version"]],
                                      "SoftwareVersion": [IDENTITY["software_version"], IDENTITY["software_build"]]},
                "reads": [], "read_discovered_5g": True, "writes": [], "reboot": False}

    blocking_pending = [c for c in checks if c["blocking"] and c["state"] != "prepared"]
    report = {"identity_reported": dict(IDENTITY), "management_ip": management_ip,
              "prepared": not blocking_pending, "first_contact_prepared": not blocking_pending,
              "checks": checks, "credential_mode": credential_mode,
              "hardware_qualified": False, "device_contacted": False,
              "network": {"cwmp_destination_port": port if valid else None,
                          "cwmp_transport": "TCP", "optional_stun_destination_port": 3478,
                          "connection_request_port": "pending device discovery"},
              "acceptance_pending": ["Target server readiness and migrations", "TLS and firewall validation",
                                     "Confirmed model, serial and OUI", "First authenticated Inform",
                                     "Bootstrap-to-device-bound credential graduation where applicable",
                                     "Connection Request behavior", "Discovered paths and exact-firmware physical qualification"],
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
    for name, document in (("readiness.json", report), ("scenario.json", scenario)):
        (args.output / name).write_text(json.dumps(document, indent=2) + "\n", encoding="utf-8")
    print("Preparation report saved. Outstanding blocking checks: " +
          str(sum(c["state"] == "pending" and c["blocking"] for c in report["checks"])))
    return 0 if report["prepared"] else 1


if __name__ == "__main__":
    raise SystemExit(main())
