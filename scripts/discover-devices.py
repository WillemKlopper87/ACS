#!/usr/bin/env python3
"""Queue ACS discovery, wait for completion, and export names/access evidence.

Credentials are read from ACS_API_TOKEN; cached parameter values are not exported.
Uses only the Python standard library and the existing authenticated ACS API.
"""
import argparse
import json
import os
from pathlib import Path
import sys
import time
import urllib.error
import urllib.parse
import urllib.request


class ACS:
    def __init__(self, base, token):
        parsed = urllib.parse.urlsplit(base)
        if parsed.scheme not in ("http", "https") or not parsed.netloc or parsed.username:
            raise ValueError("base URL must be an HTTP(S) ACS origin")
        if parsed.scheme == "http" and parsed.hostname not in ("localhost", "127.0.0.1", "::1"):
            raise ValueError("use HTTPS for remote ACS authentication")
        self.base, self.token = base.rstrip("/"), token

    def request(self, path, body=None, method=None):
        data = None if body is None else json.dumps(body).encode()
        request = urllib.request.Request(self.base + path, data=data, method=method,
            headers={"Authorization": "Bearer " + self.token, "Content-Type": "application/json"})
        # Refuse redirects so an API credential cannot leak to another origin.
        class NoRedirect(urllib.request.HTTPRedirectHandler):
            def redirect_request(self, req, fp, code, msg, headers, newurl):
                return None
        opener = urllib.request.build_opener(NoRedirect)
        with opener.open(request, timeout=30) as response:
            return json.load(response)


def discover(acs, device_id, timeout, interval):
    escaped = urllib.parse.quote(device_id, safe="")
    prefix = "/api/v1/devices/" + escaped
    queued = acs.request(prefix + "/discover-parameters", {"incremental": True})
    deadline = time.monotonic() + timeout
    key = queued["command_key"]
    # CWMP has one documented root fallback job. Follow only a newly queued
    # fallback for this device rather than treating the first root fault as final.
    followed_fallback = False
    fallback_waits = 0
    while time.monotonic() < deadline:
        job = acs.request("/api/v1/jobs/" + urllib.parse.quote(key, safe=""))
        status = job["status"]
        if status == "SUCCESS":
            export = acs.request(prefix + "/discovery-export")
            snapshots = [s for s in export["snapshots"] if s["command_key"] == key]
            if not snapshots:
                raise RuntimeError("job completed without a persisted discovery snapshot")
            return {"device_id": device_id, "snapshot": snapshots[0]}
        if status in ("FAILED", "TIMEOUT", "CANCELLED"):
            if status == "FAILED" and not followed_fallback:
                fallback_key = (job.get("result_detail") or {}).get("fallback_command_key")
                if fallback_key:
                    key = fallback_key
                    followed_fallback = True
                elif fallback_waits < 2:
                    fallback_waits += 1
                    time.sleep(interval)
                else:
                    raise RuntimeError("discovery failed: " + str(job.get("fault_string", status)))
                continue
            raise RuntimeError("discovery failed: " + str(job.get("fault_string", status)))
        time.sleep(interval)
    raise TimeoutError("device did not complete discovery before timeout; queued job remains available in ACS")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--base-url", required=True)
    parser.add_argument("--device", action="append", required=True, help="device UUID; repeat for multiple devices")
    parser.add_argument("--output", type=Path, default=Path("discovery-output"))
    parser.add_argument("--timeout", type=float, default=1800)
    parser.add_argument("--poll-interval", type=float, default=5)
    args = parser.parse_args()
    if args.timeout <= 0 or args.poll_interval < 1:
        parser.error("timeout must be positive and poll interval at least one second")
    token = os.environ.get("ACS_API_TOKEN", "")
    if not token:
        parser.error("set ACS_API_TOKEN to an operator token with diagnostics.run permission")
    acs = ACS(args.base_url, token)
    args.output.mkdir(parents=True, exist_ok=True)
    failures = []
    for device_id in dict.fromkeys(args.device):
        try:
            # A filename is derived only from a validated UUID, never a device label.
            import uuid
            filename = str(uuid.UUID(device_id)) + ".json"
            result = discover(acs, device_id, args.timeout, args.poll_interval)
            target = args.output / filename
            temporary = target.with_suffix(".tmp")
            temporary.write_text(json.dumps(result, indent=2, sort_keys=True), encoding="utf-8")
            temporary.replace(target)
            print(f"{device_id}: saved {target}")
        except (ValueError, RuntimeError, TimeoutError, urllib.error.URLError, OSError) as exc:
            # Do not print request headers, tokens, or raw HTTP response bodies.
            failures.append(device_id)
            print(f"{device_id}: {exc}", file=sys.stderr)
    return 1 if failures else 0


if __name__ == "__main__":
    sys.exit(main())
