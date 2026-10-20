import importlib.util
import pathlib
import unittest
from unittest.mock import patch

ROOT = pathlib.Path(__file__).resolve().parents[1]


def load(name):
    spec = importlib.util.spec_from_file_location(name, ROOT / (name + ".py"))
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


discovery = load("discover-devices")
qualification = load("qualify-device")
DEVICE = "11111111-1111-1111-1111-111111111111"


class FakeACS:
    def __init__(self, responses):
        self.responses = iter(responses)
        self.calls = []

    def request(self, path, body=None, method=None):
        self.calls.append((path, body, method))
        return next(self.responses)


class CompatibilityTests(unittest.TestCase):
    def test_failed_read_prevents_reboot(self):
        acs = FakeACS([{'command_key': 'discovery'}, {'status': 'SUCCESS'},
                       {'snapshots': [{'command_key': 'discovery', 'names': {}}]},
                       {'command_key': 'read'}, {'status': 'FAILED'},
                       {'command_key': 'reboot'}, {'status': 'SUCCESS'},
                       {'command_key': 'post'}, {'status': 'SUCCESS'}, {}])
        report = qualification.qualify(acs, DEVICE, {'reads': ['Device.DeviceInfo.SoftwareVersion'], 'reboot': True}, 10, True)
        self.assertFalse(report['passed'])
        self.assertFalse(any(path.endswith('/reboot') for path, _, _ in acs.calls))

    def test_post_reboot_read_uses_discovered_legacy_root(self):
        acs = FakeACS([{'command_key': 'discovery'}, {'status': 'SUCCESS'},
                       {'snapshots': [{'command_key': 'discovery', 'root': 'InternetGatewayDevice.', 'names': {}}]},
                       {'command_key': 'reboot'}, {'status': 'SUCCESS'},
                       {'command_key': 'post'}, {'status': 'SUCCESS'}, {}])
        qualification.qualify(acs, DEVICE, {'reboot': True}, 10, True)
        reads = [body['paths'] for path, body, _ in acs.calls if path.endswith('/parameters/get')]
        self.assertEqual(reads, [['InternetGatewayDevice.DeviceInfo.SoftwareVersion']])

    def test_remote_credentials_require_https(self):
        with self.assertRaises(ValueError):
            discovery.ACS("http://acs.example", "secret")

    def test_discovery_follows_only_its_recorded_fallback(self):
        acs = FakeACS([{"command_key": "first"},
                       {"status": "FAILED", "result_detail": {"fallback_command_key": "fallback"}},
                       {"status": "SUCCESS"},
                       {"snapshots": [{"command_key": "unrelated"}, {"command_key": "fallback", "names": {"Device.Enable": True}}]}])
        result = discovery.discover(acs, DEVICE, 10, 1)
        self.assertEqual(result["snapshot"]["command_key"], "fallback")
        self.assertTrue(acs.calls[2][0].endswith("/fallback"))

    def test_completed_job_requires_matching_snapshot(self):
        acs = FakeACS([{"command_key": "first"}, {"status": "SUCCESS"}, {"snapshots": []}])
        with self.assertRaises(RuntimeError):
            discovery.discover(acs, DEVICE, 10, 1)

    def test_destructive_scenario_needs_lab_flag(self):
        with self.assertRaises(ValueError):
            qualification.qualify(FakeACS([]), DEVICE, {"reboot": True}, 10)

    def test_read_only_qualification_exports_no_values(self):
        acs = FakeACS([{"command_key": "discovery"}, {"status": "SUCCESS"},
                       {"snapshots": [{"command_key": "discovery", "names": {"Device.DeviceInfo.SoftwareVersion": False}}]},
                       {"command_key": "read"}, {"status": "SUCCESS", "result_detail": {"params": {"secret": "never-export"}}},
                       {"firmware": "1.0", "read_tested": True}])
        report = qualification.qualify(acs, DEVICE, {"reads": ["Device.DeviceInfo.SoftwareVersion"]}, 10)
        self.assertTrue(report["passed"])
        self.assertNotIn("never-export", str(report))

    def test_failed_job_is_not_qualified(self):
        result = qualification.wait_job(FakeACS([{"status": "FAILED", "fault_code": "READBACK_MISMATCH"}]), "write", 10)
        self.assertEqual(result["status"], "FAILED")

    def test_post_reboot_read_does_not_claim_observed_reconnection(self):
        acs = FakeACS([{"command_key": "discovery"}, {"status": "SUCCESS"},
                       {"snapshots": [{"command_key": "discovery", "names": {}}]},
                       {"command_key": "reboot"}, {"status": "SUCCESS"},
                       {"command_key": "read"}, {"status": "SUCCESS"}, {}])
        report = qualification.qualify(acs, DEVICE, {"reboot": True}, 10, lab_actions=True)
        self.assertEqual(report["tests"][1]["operation"], "post_reboot_read")
        self.assertIn("independent observation", report["reboot_witness"])


if __name__ == "__main__":
    unittest.main()
