import importlib.util
import json
import pathlib
import tempfile
import unittest

ROOT = pathlib.Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location("export_discovery_library", ROOT / "export-discovery-library.py")
library = importlib.util.module_from_spec(spec)
spec.loader.exec_module(library)

SERIAL = "CSC25430018F"


class RoutedACS:
    """Answers by path prefix, so walk order does not matter."""

    def __init__(self, snapshots_by_call, skipped=()):
        self.snapshots = iter(snapshots_by_call)
        self.skipped = list(skipped)

    def request(self, path, body=None, method=None):
        if path.startswith("/api/v1/devices?"):
            return {"items": [{"id": "dev-1", "manufacturer": "Nokia", "product_class": "3TG02241", "oui": "A4FC77", "serial_number": SERIAL}], "total": 1}
        if path.endswith("/discovery-export"):
            return {"device_id": "dev-1", "snapshots": [next(self.snapshots)]}
        if "/parameters?paths=" in path:
            return {"parameters": {
                "Device.DeviceInfo.Manufacturer": {"value": "Nokia"},
                "Device.DeviceInfo.ModelName": {"value": "FastMile 5G Gateway 2"},
                "Device.DeviceInfo.ProductClass": {"value": "3TG02241"},
                "Device.DeviceInfo.HardwareVersion": {"value": "3TG02241AAAA"},
                "Device.DeviceInfo.SoftwareVersion": {"value": "1.2501.00.0798"},
                "Device.RootDataModelVersion": {"value": "2.12"},
                "Device.DeviceInfo.SerialNumber": {"value": SERIAL},
            }}
        if path.startswith("/api/v1/jobs/"):
            return {"result_detail": {"skipped": self.skipped}}
        raise AssertionError("unexpected request " + path)


def snapshot(key, names):
    return {"command_key": key, "protocol": "CWMP", "root": "Device.", "names": names,
            "capabilities": [{"path": "Device.DeviceInfo.ProvisioningCode", "value_type": "string"}],
            "evidence": {"manufacturer": "Nokia", "model": "3TG02241", "serial_number": SERIAL, "firmware": "1.2501.00.0798"},
            "discovered_at": "2026-10-30T10:24:37Z"}


class DiscoveryLibraryTests(unittest.TestCase):
    def test_walks_of_one_firmware_are_unioned_without_identifiers(self):
        first = snapshot("discover_a", {"Device.DeviceInfo.": False, "Device.DeviceInfo.ProvisioningCode": True, "Device.DynamicDNS.Client.": False})
        second = snapshot("discover_b", {"Device.DeviceInfo.": False, "Device.Ethernet.Interface.1.Enable": True})
        with tempfile.TemporaryDirectory() as tmp:
            lib = pathlib.Path(tmp)
            library.export(RoutedACS([first], skipped=["Device.Ethernet.Interface."]), lib, "abc1234")
            library.export(RoutedACS([second]), lib, "abc1234")

            entry = lib / "Nokia" / "3TG02241" / "1.2501.00.0798"
            meta = json.loads((entry / "meta.json").read_text(encoding="utf-8"))
            tsv = (entry / "parameters.tsv").read_text(encoding="utf-8")

            self.assertIn("Device.DeviceInfo.ProvisioningCode\tRW\tstring", tsv)
            self.assertIn("Device.Ethernet.Interface.1.Enable\tRW", tsv)  # from the second walk
            self.assertEqual(meta["snapshots_merged"], 2)
            self.assertEqual(meta["hardware_versions"], ["3TG02241AAAA"])
            self.assertEqual(meta["root_data_model_version"], "2.12")
            self.assertEqual(meta["never_observed"], ["Device.Ethernet.Interface."])
            for text in (tsv, json.dumps(meta), (lib / "INDEX.md").read_text(encoding="utf-8")):
                self.assertNotIn(SERIAL, text)
                self.assertNotIn("dev-1", text)

    def test_rerunning_the_same_walk_does_not_double_count(self):
        snap = snapshot("discover_a", {"Device.DeviceInfo.ProvisioningCode": True})
        with tempfile.TemporaryDirectory() as tmp:
            lib = pathlib.Path(tmp)
            library.export(RoutedACS([snap]), lib, "")
            library.export(RoutedACS([snap]), lib, "")
            meta = json.loads((lib / "Nokia" / "3TG02241" / "1.2501.00.0798" / "meta.json").read_text(encoding="utf-8"))
            self.assertEqual(meta["snapshots_merged"], 1)


if __name__ == "__main__":
    unittest.main()
