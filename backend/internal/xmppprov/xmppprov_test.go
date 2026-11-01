package xmppprov

import (
	"strings"
	"testing"
)

func TestFirstInstance(t *testing.T) {
	names := map[string]bool{
		"Device.XMPP.Connection.1.Server.":                true,
		"Device.XMPP.Connection.1.Server.3.":              true,
		"Device.XMPP.Connection.1.Server.2.":              true,
		"Device.XMPP.Connection.1.Server.2.Port":          true,
		"Device.XMPP.Connection.1.ServerConnectAlgorithm": true,
	}
	if got := FirstInstance(names, "Device.XMPP.Connection.1.Server."); got != "Device.XMPP.Connection.1.Server.2." {
		t.Fatalf("FirstInstance = %q", got)
	}
	if got := FirstInstance(map[string]bool{"Device.XMPP.Connection.": true}, "Device.XMPP.Connection."); got != "" {
		t.Fatalf("empty table: %q", got)
	}
}

func TestConnectionReference(t *testing.T) {
	if got := ConnectionReference("Device.XMPP.Connection.1."); got != "Device.XMPP.Connection.1" {
		t.Fatal(got)
	}
}

// TestConfigureWritesOnlyDiscoveredWritableLeaves: optional leaves the CPE
// does not expose (or exposes read-only) are skipped; required ones fail
// the step with their names.
func TestConfigureWritesOnlyDiscoveredWritableLeaves(t *testing.T) {
	p := &Provisioning{ConnectionPath: "Device.XMPP.Connection.1.", ServerPath: "Device.XMPP.Connection.1.Server.1.",
		Domain: "d", Resource: "cwmp", ServerAddress: "1.2.3.4", ServerPort: 5222, KeepAliveInterval: 60}
	names := map[string]bool{
		"Device.XMPP.Connection.1.Username": true, "Device.XMPP.Connection.1.Password": true,
		"Device.XMPP.Connection.1.Domain": true, "Device.XMPP.Connection.1.UseTLS": false,
		"Device.XMPP.Connection.1.Server.1.ServerAddress": true,
	}
	writes, err := ConfigureWrites(names, p, "u", "pw")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, w := range writes {
		got[w.Name] = w.Value
	}
	if len(got) != 4 || got["Device.XMPP.Connection.1.Server.1.ServerAddress"] != "1.2.3.4" {
		t.Fatalf("writes = %v (UseTLS is read-only and must be skipped)", got)
	}

	delete(names, "Device.XMPP.Connection.1.Domain")
	if _, err := ConfigureWrites(names, p, "u", "pw"); err == nil || !strings.Contains(err.Error(), "Domain") {
		t.Fatalf("missing Domain: err = %v", err)
	}
	p.ServerPath = ""
	names["Device.XMPP.Connection.1.Domain"] = true
	if _, err := ConfigureWrites(names, p, "u", "pw"); err == nil {
		t.Fatal("no server path must be an error")
	}
}

func TestBindWritesRequiresConnReqXMPPConnection(t *testing.T) {
	p := &Provisioning{ConnectionPath: "Device.XMPP.Connection.1.", ACSJID: "acs@d"}
	if _, err := BindWrites(map[string]bool{"Device.ManagementServer.ConnReqAllowedJabberIDs": true}, p); err == nil {
		t.Fatal("bind without ConnReqXMPPConnection must fail")
	}
	writes, err := BindWrites(map[string]bool{"Device.ManagementServer.ConnReqXMPPConnection": true}, p)
	if err != nil || len(writes) != 1 {
		t.Fatalf("writes = %v err = %v", writes, err)
	}
}

func TestVendorParamMatchesVendorPrefixes(t *testing.T) {
	names := map[string]bool{
		"Device.ManagementServer.X_ALU_COM_XMPP_Enable": true,
		"Device.ManagementServer.ConnReqJabberID":       false,
	}
	if got := vendorParam(names, ManagementServer, "XMPP_Enable"); got != "Device.ManagementServer.X_ALU_COM_XMPP_Enable" {
		t.Fatalf("got %q", got)
	}
	if got := vendorParam(names, xmppRoot, "BypassHostnameCheck"); got != "" {
		t.Fatalf("got %q", got)
	}
}
