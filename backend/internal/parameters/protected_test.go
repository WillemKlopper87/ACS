package parameters

import (
	"errors"
	"testing"
)

// Paths from the discovered trees of the field FastMile (1.2501.00.0798)
// and Beacon 3.1, all writable on the device, plus their TR-098 forms.
func TestProtectedParameters(t *testing.T) {
	protected := []string{
		"Device.ManagementServer.URL",
		"Device.ManagementServer.Username",
		"Device.ManagementServer.Password",
		"Device.ManagementServer.ConnectionRequestUsername",
		"Device.ManagementServer.ConnectionRequestPassword",
		"Device.ManagementServer.EnableCWMP",
		"Device.ManagementServer.PeriodicInformEnable",
		"Device.ManagementServer.UpgradesManaged",
		"Device.ManagementServer.ConnReqXMPPConnection",
		"Device.ManagementServer.ConnReqAllowedJabberIDs",
		"Device.ManagementServer.X_ALU_COM_XMPP_Enable",
		"Device.ManagementServer.X_ALU-COM_ACSIPv4List",
		"Device.IP.Interface.2.X_ALU-COM_WanAccessCfg.Tr69Disabled",
		"Device.X_ALU-COM_LanAccessCfg.Tr69Disabled",
		"Device.X_ALU-COM_ModemDeviceInfo.X_ALU-COM_NokiaServerURL",
		"Device.X_ALU-COM_ModemDeviceInfo.X_ALU-COM_BlockingTrafficByOperatorLock",
		"Device.X_ALU-COM_ModemDeviceInfo.X_ALU-COM_QueryBOENGServerInOperatorLock",
		"Device.X_ALU-COM_ModemDeviceInfo.X_ALU-COM_ObtainOPIDFromPLMN",
		"Device.X_ALU-COM_ModemDeviceInfo.X_ALU-COM_ForceDownloadPreconfigFromServer",
		"Device.X_ALU-COM_ModemDeviceInfo.X_ALU-COM_ForceDownloadDeltaConfigFromServer",
		"Device.X_ALU-COM_ModemDeviceInfo.X_ALU-COM_DeltaConfig_Flag",
		"Device.X_ALU-COM_ModemDeviceInfo.X_ALU-COM_ImageDownloadTimer",
		"Device.X_ALU-COM_NWCC.L1Server.URL",
		"Device.X_ALU-COM_NWCC.RemoteMobileAccess.URL",
		"Device.X_ALU-COM_FastmileController.URL",
		"Device.X_ALU-COM_RemoteWebDebug.Enable",
		"Device.X_ALU-COM_RemoteWebDebug.DebugServerURL",
		"Device.WiFi.DataElements.Network.ControllerID",
		"Device.WiFi.Radio.1.RegulatoryDomain",
		"InternetGatewayDevice.ManagementServer.URL",
		"InternetGatewayDevice.ManagementServer.Password",
		"InternetGatewayDevice.LANDevice.1.WLANConfiguration.1.RegulatoryDomain",
	}
	for _, name := range protected {
		if reason, ok := ProtectedReason(name); !ok || reason == "" {
			t.Errorf("%s not protected", name)
		}
	}
	allowed := []string{
		"Device.WiFi.SSID.1.SSID",
		"Device.WiFi.AccessPoint.1.Security.KeyPassphrase",
		"Device.ManagementServer.PeriodicInformInterval", // tightened by the CGNAT fallback, set by operators
		"Device.ManagementServer.ParameterKey",
		"Device.IP.Interface.2.X_ALU-COM_WanAccessCfg.HttpsDisabled",
		"Device.Cellular.AccessPoint.1.APN",
		"Device.ManagementServer.URLExtra", // whole-segment match only
	}
	for _, name := range allowed {
		if _, ok := ProtectedReason(name); ok {
			t.Errorf("%s wrongly protected", name)
		}
	}
}

func TestCheckProtected(t *testing.T) {
	if err := CheckProtected("Device.WiFi.SSID.1.SSID", "Device.ManagementServer.PeriodicInformInterval"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	err := CheckProtected("Device.WiFi.SSID.1.SSID", "Device.ManagementServer.URL")
	var pe *ProtectedParameterError
	if !errors.As(err, &pe) || pe.Name != "Device.ManagementServer.URL" {
		t.Fatalf("err = %v, want ProtectedParameterError for the ACS URL", err)
	}
	if !errors.Is(err, ErrProtectedParameter) {
		t.Fatal("not matchable with errors.Is(ErrProtectedParameter)")
	}
}
