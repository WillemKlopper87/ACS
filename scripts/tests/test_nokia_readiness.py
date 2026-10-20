import importlib.util
import pathlib
import unittest

from test_compatibility import FakeACS, DEVICE, qualification

spec = importlib.util.spec_from_file_location("prepare_nokia", pathlib.Path(__file__).resolve().parents[1] / "prepare-nokia-onboarding.py")
preparation = importlib.util.module_from_spec(spec)
spec.loader.exec_module(preparation)


class NokiaReadinessTests(unittest.TestCase):
    def test_invalid_configured_bootstrap_cannot_fall_back_to_legacy(self):
        env = {'ACS_DEPLOYMENT_PROFILE': 'lab', 'ACS_POSTGRES_DSN': 'postgres://host/db',
               'ACS_CREDENTIAL_ENCRYPTION_KEY': 'stable-key-material-for-testing',
               'ACS_CWMP_BOOTSTRAP_USERNAME': 'bootstrap', 'ACS_CWMP_BOOTSTRAP_PASSWORD': 'short',
               'NOKIA_ACS_USERNAME': 'legacy', 'NOKIA_ACS_PASSWORD': 'strong-password-material'}
        report, _ = preparation.prepare('http://acs.example:7547/cwmp', env)
        self.assertFalse(report['first_contact_prepared'])

    def test_runtime_invalid_bootstrap_secrets_remain_pending(self):
        for password in ('short', 'x' * 32, 'change_me       ', 'password', 'CHANGE ME'):
            with self.subTest(password=password):
                env = {'ACS_DEPLOYMENT_PROFILE': 'lab', 'ACS_POSTGRES_DSN': 'postgres://host/db',
                       'ACS_CREDENTIAL_ENCRYPTION_KEY': 'stable-key-material-for-testing',
                       'ACS_CWMP_BOOTSTRAP_USERNAME': 'bootstrap',
                       'ACS_CWMP_BOOTSTRAP_PASSWORD': password}
                report, _ = preparation.prepare('http://acs.example:7547/cwmp', env)
                self.assertFalse(report['first_contact_prepared'])

    def test_bootstrap_username_must_differ_from_shared_identity(self):
        env = {'ACS_DEPLOYMENT_PROFILE': 'lab', 'ACS_POSTGRES_DSN': 'postgres://host/db',
               'ACS_CREDENTIAL_ENCRYPTION_KEY': 'stable-key-material-for-testing',
               'ACS_CWMP_BOOTSTRAP_USERNAME': 'same-user', 'ACS_DIGEST_USERNAME': 'same-user',
               'ACS_CWMP_BOOTSTRAP_PASSWORD': 'strong-password-material'}
        report, _ = preparation.prepare('http://acs.example:7547/cwmp', env)
        self.assertFalse(report['first_contact_prepared'])

    def test_placeholder_encryption_key_remains_pending(self):
        env = {'ACS_DEPLOYMENT_PROFILE': 'lab', 'ACS_POSTGRES_DSN': 'postgres://host/db',
               'ACS_CREDENTIAL_ENCRYPTION_KEY': 'x' * 32,
               'ACS_CWMP_BOOTSTRAP_USERNAME': 'bootstrap',
               'ACS_CWMP_BOOTSTRAP_PASSWORD': 'strong-password-material'}
        report, _ = preparation.prepare('http://acs.example:7547/cwmp', env)
        self.assertFalse(report['first_contact_prepared'])

    def test_missing_settings_remain_pending(self):
        report, scenario = preparation.prepare("", {})
        self.assertFalse(report["prepared"])
        self.assertFalse(report["hardware_qualified"])
        self.assertIsNone(report["identity_reported"]["model"])
        self.assertFalse(scenario["writes"])
        self.assertFalse(scenario["reboot"])

    def test_production_uses_constrained_bootstrap_without_exporting_secret(self):
        secret = "never-export-this-password"
        env = {
            "ACS_DEPLOYMENT_PROFILE": "production",
            "ACS_POSTGRES_DSN": "postgres://user:" + secret + "@host/db",
            "ACS_CREDENTIAL_ENCRYPTION_KEY": secret,
            "ACS_CWMP_BOOTSTRAP_USERNAME": "nokia-bootstrap",
            "ACS_CWMP_BOOTSTRAP_PASSWORD": secret,
        }
        report, _ = preparation.prepare("https://acs.example:7547/cwmp", env)
        self.assertTrue(report["prepared"])
        self.assertEqual(report["credential_mode"], "production_bootstrap")
        self.assertEqual(report["network"]["cwmp_destination_port"], 7547)
        self.assertNotIn(secret, str(report))

        report, _ = preparation.prepare("https://user:" + secret + "@acs.example/cwmp", env)
        self.assertFalse(report["prepared"])
        self.assertNotIn(secret, str(report))

    def test_production_rejects_legacy_nokia_credentials_as_first_contact(self):
        secret = "never-export-this-password"
        env = {
            "ACS_DEPLOYMENT_PROFILE": "production",
            "ACS_POSTGRES_DSN": "postgres://host/db",
            "ACS_CREDENTIAL_ENCRYPTION_KEY": secret,
            "NOKIA_ACS_USERNAME": "legacy-user",
            "NOKIA_ACS_PASSWORD": secret,
        }
        report, _ = preparation.prepare("https://acs.example:7547/cwmp", env)
        self.assertFalse(report["prepared"])
        self.assertEqual(report["credential_mode"], "missing")
        check = next(c for c in report["checks"] if c["check"] == "cwmp_first_contact_credentials")
        self.assertEqual(check["state"], "pending")

    def test_lab_legacy_credentials_are_explicitly_compatibility_only(self):
        secret = "never-export-this-password"
        env = {
            "ACS_DEPLOYMENT_PROFILE": "lab",
            "ACS_POSTGRES_DSN": "postgres://host/db",
            "ACS_CREDENTIAL_ENCRYPTION_KEY": secret,
            "NOKIA_ACS_USERNAME": "lab-user",
            "NOKIA_ACS_PASSWORD": secret,
        }
        report, _ = preparation.prepare("http://acs.example:7547/cwmp", env)
        self.assertTrue(report["prepared"])
        self.assertEqual(report["credential_mode"], "lab_compatibility")

    def test_connection_request_credentials_are_not_required_for_first_contact(self):
        secret = "never-export-this-password"
        env = {
            "ACS_DEPLOYMENT_PROFILE": "lab",
            "ACS_POSTGRES_DSN": "postgres://host/db",
            "ACS_CREDENTIAL_ENCRYPTION_KEY": secret,
            "ACS_CWMP_BOOTSTRAP_USERNAME": "bootstrap",
            "ACS_CWMP_BOOTSTRAP_PASSWORD": secret,
        }
        report, _ = preparation.prepare("http://acs.example:7547/cwmp", env)
        self.assertTrue(report["first_contact_prepared"])
        check = next(c for c in report["checks"] if c["check"] == "connection_request_credentials")
        self.assertEqual(check["state"], "pending")
        self.assertFalse(check["blocking"])

    def test_production_plaintext_is_pending(self):
        report, _ = preparation.prepare("http://acs.example:7547", {"ACS_DEPLOYMENT_PROFILE": "production"})
        self.assertEqual(next(c for c in report["checks"] if c["check"] == "acs_url")["state"], "pending")

    def test_wrong_firmware_stops_before_write(self):
        path = "Device.DeviceInfo.SoftwareVersion"
        acs = FakeACS([{"command_key": "discovery"}, {"status": "SUCCESS"},
                       {"snapshots": [{"command_key": "discovery", "root": "Device.", "names": {path: False}}]},
                       {"command_key": "identity"}, {"status": "SUCCESS"},
                       {"parameters": {path: {"value": "different"}}}])
        scenario = {"expected_identity": {"SoftwareVersion": ["1.2501.00.0798"]}, "writes": [{"name": "Device.WiFi.SSID.1.SSID", "value": "lab"}]}
        report = qualification.qualify(acs, DEVICE, scenario, 10, lab_actions=True)
        self.assertFalse(report["passed"])
        self.assertEqual(report["identity_check"]["status"], "MISMATCH")
        self.assertFalse(any(method == "PUT" for _, _, method in acs.calls))

    def test_legacy_root_and_discovered_rf_reads(self):
        identity = "InternetGatewayDevice.DeviceInfo.SoftwareVersion"
        signal = "InternetGatewayDevice.X_NOKIA_5G.RSRP"
        secret = "InternetGatewayDevice.X_NOKIA_5G.PasswordRSRP"
        acs = FakeACS([{"command_key": "discovery"}, {"status": "SUCCESS"},
                       {"snapshots": [{"command_key": "discovery", "root": "InternetGatewayDevice.", "names": {identity: False, signal: False, secret: True}}]},
                       {"command_key": "identity"}, {"status": "SUCCESS"},
                       {"parameters": {identity: {"value": "firmware"}}},
                       {"command_key": "rf"}, {"status": "SUCCESS"}, {}])
        report = qualification.qualify(acs, DEVICE, {"expected_identity": {"SoftwareVersion": ["firmware"]}, "read_discovered_5g": True}, 10)
        self.assertTrue(report["passed"])
        self.assertEqual(report["fiveg_inventory"]["paths"], [signal])

    def test_missing_identity_does_not_queue_mutation(self):
        acs = FakeACS([{"command_key": "discovery"}, {"status": "SUCCESS"}, {"snapshots": [{"command_key": "discovery", "names": {}}]}])
        report = qualification.qualify(acs, DEVICE, {"expected_identity": {"HardwareVersion": ["known"]}, "reboot": True}, 10, lab_actions=True)
        self.assertFalse(report["passed"])
        self.assertEqual(len(acs.calls), 3)
