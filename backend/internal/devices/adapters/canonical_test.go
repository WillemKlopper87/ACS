package adapters

import (
	"testing"

	"acs/internal/devices"
)

func TestResolvePathDevice2AndUnknownDefaultToTR181(t *testing.T) {
	for _, root := range []string{devices.DataModelRootDevice2, devices.DataModelRootUnknown, ""} {
		path, ok := ResolvePath(root, DeviceInfoSoftwareVersion)
		if !ok || path != "Device.DeviceInfo.SoftwareVersion" {
			t.Errorf("root %q: ResolvePath(DeviceInfoSoftwareVersion) = (%q, %v), want (Device.DeviceInfo.SoftwareVersion, true)", root, path, ok)
		}
	}
}

func TestResolvePathIGD1(t *testing.T) {
	cases := map[CanonicalParameter]string{
		DeviceInfoSoftwareVersion:             "InternetGatewayDevice.DeviceInfo.SoftwareVersion",
		ManagementServerConnectionRequestURL:  "InternetGatewayDevice.ManagementServer.ConnectionRequestURL",
		ManagementServerConnectionRequestUser: "InternetGatewayDevice.ManagementServer.ConnectionRequestUsername",
		ManagementServerConnectionRequestPass: "InternetGatewayDevice.ManagementServer.ConnectionRequestPassword",
		WiFiSSID:                              "InternetGatewayDevice.LANDevice.1.WLANConfiguration.1.SSID",
		WiFiKeyPassphrase:                     "InternetGatewayDevice.LANDevice.1.WLANConfiguration.1.KeyPassphrase",
	}
	for canonical, want := range cases {
		got, ok := ResolvePath(devices.DataModelRootIGD1, canonical)
		if !ok || got != want {
			t.Errorf("ResolvePath(IGD1, %q) = (%q, %v), want (%q, true)", canonical, got, ok, want)
		}
	}
}

func TestResolvePathUnknownCanonicalParameter(t *testing.T) {
	if _, ok := ResolvePath(devices.DataModelRootDevice2, CanonicalParameter("no_such_parameter")); ok {
		t.Error("ResolvePath with an unregistered CanonicalParameter should return ok=false")
	}
	if _, ok := ResolvePath(devices.DataModelRootIGD1, CanonicalParameter("no_such_parameter")); ok {
		t.Error("ResolvePath with an unregistered CanonicalParameter should return ok=false")
	}
}

func TestDiagnosticsPrefix(t *testing.T) {
	cases := []struct {
		root, kind, want string
	}{
		{devices.DataModelRootDevice2, DiagnosticPing, "Device.IP.Diagnostics.IPPing."},
		{devices.DataModelRootDevice2, DiagnosticTraceroute, "Device.IP.Diagnostics.TraceRoute."},
		{devices.DataModelRootUnknown, DiagnosticPing, "Device.IP.Diagnostics.IPPing."},
		{devices.DataModelRootIGD1, DiagnosticPing, "InternetGatewayDevice.IPPingDiagnostics."},
		{devices.DataModelRootIGD1, DiagnosticTraceroute, "InternetGatewayDevice.TraceRouteDiagnostics."},
	}
	for _, c := range cases {
		if got := DiagnosticsPrefix(c.root, c.kind); got != c.want {
			t.Errorf("DiagnosticsPrefix(%q, %q) = %q, want %q", c.root, c.kind, got, c.want)
		}
	}
}

func TestWiFiAssociatedDevicesPrefix(t *testing.T) {
	if got := WiFiAssociatedDevicesPrefix(devices.DataModelRootDevice2); got != "Device.WiFi.AccessPoint." {
		t.Errorf("Device2 prefix = %q, want Device.WiFi.AccessPoint.", got)
	}
	if got := WiFiAssociatedDevicesPrefix(devices.DataModelRootIGD1); got != "InternetGatewayDevice.LANDevice.1.WLANConfiguration." {
		t.Errorf("IGD1 prefix = %q, want InternetGatewayDevice.LANDevice.1.WLANConfiguration.", got)
	}
}
