package main

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
	"time"

	"acs/internal/cwmp"
	"acs/internal/devices"
	"acs/internal/jobs"
	"acs/internal/parameters"
	"acs/internal/store"
	"acs/internal/wifiinsights"
)

func TestInformHasEventMatchesWholeCode(t *testing.T) {
	inform := &cwmp.Inform{Event: []cwmp.EventStruct{{EventCode: "10 AUTONOMOUS TRANSFER COMPLETE"}}}
	if informHasEvent(inform, "1 BOOT") {
		t.Fatal(`"10 AUTONOMOUS TRANSFER COMPLETE" must not match "1 BOOT"`)
	}
	inform.Event = append(inform.Event, cwmp.EventStruct{EventCode: "1 BOOT"})
	if !informHasEvent(inform, "1 BOOT") {
		t.Fatal(`"1 BOOT" not matched`)
	}
}

func TestHasGatewayInfo(t *testing.T) {
	if hasGatewayInfo([]cwmp.ParameterValueStruct{{Name: "Device.DeviceInfo.SoftwareVersion"}}) {
		t.Fatal("non-GatewayInfo list reported as GatewayInfo")
	}
	if !hasGatewayInfo([]cwmp.ParameterValueStruct{{Name: "Device.DeviceInfo.SoftwareVersion"}, {Name: devices.GatewayInfoSerialNumber}}) {
		t.Fatal("GatewayInfo leaf not detected")
	}
}

// TestIntegration_MeshSatelliteLinksToGateway walks the field-test pair: a
// Beacon 3.1 (ALCL) whose discovered tree has TR-111 GatewayInfo gets a
// targeted read queued, and the values it returns link it to the FastMile.
func TestIntegration_MeshSatelliteLinksToGateway(t *testing.T) {
	h, _, ctx := newTestGateway(t)
	gateway, err := h.devices.UpsertFromInform(ctx, cwmp.DeviceID{Manufacturer: "Nokia", OUI: "A4FC77", ProductClass: "3TG02241", SerialNumber: "CSC25430018F"}, []string{"0 BOOTSTRAP"})
	if err != nil {
		t.Fatal(err)
	}
	beacon, err := h.devices.UpsertFromInform(ctx, cwmp.DeviceID{Manufacturer: "ALCL", OUI: "F89B6E", ProductClass: "Beacon 3.1", SerialNumber: "ALCLB0000001"}, []string{"0 BOOTSTRAP"})
	if err != nil {
		t.Fatal(err)
	}

	h.queueGatewayInfoRead(ctx, beacon.ID, map[string]bool{
		"Device.GatewayInfo.": false, devices.GatewayInfoManufacturerOUI: false,
		devices.GatewayInfoProductClass: false, devices.GatewayInfoSerialNumber: false,
		"Device.X_ALU-COM_Wifi.WorkRole": false,
	})
	queued, err := h.jobs.List(ctx, beacon.ID, nil, false)
	if err != nil || len(queued) != 1 || queued[0].Type != jobs.TypeGetParameter || queued[0].CreatedBy != gatewayInfoJobCreator {
		t.Fatalf("queued jobs = %+v, %v; want one gateway-info GET", queued, err)
	}
	var payload jobs.GetParameterPayload
	if err := json.Unmarshal(queued[0].Payload, &payload); err != nil || !reflect.DeepEqual(payload.Paths, devices.GatewayInfoPaths) {
		t.Fatalf("gateway-info read paths = %v, %v", payload.Paths, err)
	}

	// No GatewayInfo in the gateway's own tree: nothing queued.
	h.queueGatewayInfoRead(ctx, gateway.ID, map[string]bool{"Device.WiFi.SSID.1.SSID": true})
	if gq, _ := h.jobs.List(ctx, gateway.ID, nil, false); len(gq) != 0 {
		t.Fatalf("gateway without GatewayInfo got jobs: %+v", gq)
	}

	h.cacheParameterValues(ctx, beacon.ID, []cwmp.ParameterValueStruct{
		{Name: devices.GatewayInfoManufacturerOUI, Value: "A4FC77"},
		{Name: devices.GatewayInfoProductClass, Value: "3TG02241"},
		{Name: devices.GatewayInfoSerialNumber, Value: "CSC25430018F"},
	}, parameters.SourceGetValues)
	got, err := h.devices.Get(ctx, beacon.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.GatewayDeviceID == nil || *got.GatewayDeviceID != gateway.ID {
		t.Fatalf("beacon gateway_device_id = %v, want %s", got.GatewayDeviceID, gateway.ID)
	}

	// The satellite later reports no upstream gateway: link cleared.
	h.cacheParameterValues(ctx, beacon.ID, []cwmp.ParameterValueStruct{
		{Name: devices.GatewayInfoManufacturerOUI, Value: ""},
		{Name: devices.GatewayInfoProductClass, Value: ""},
		{Name: devices.GatewayInfoSerialNumber, Value: ""},
	}, parameters.SourceGetValues)
	if got, _ = h.devices.Get(ctx, beacon.ID); got.GatewayDeviceID != nil {
		t.Fatalf("link not cleared: %v", *got.GatewayDeviceID)
	}
}

// TestIntegration_WiFiInsightsExcludesLinkedSatellite: field data
// 2026-10-30 listed the Beacon in the FastMile's Hosts table as a client.
// A linked satellite's own interface MACs (from its parameter cache) are
// left out of the gateway's clients even when the EasyMesh table does not
// carry them.
func TestIntegration_WiFiInsightsExcludesLinkedSatellite(t *testing.T) {
	h, _, ctx := newTestGateway(t)
	db, err := store.Open(ctx, os.Getenv("ACS_TEST_POSTGRES_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	repo := wifiinsights.NewRepository(db, nil)
	h.wifiInsights = &wifiInsightsSampler{repo: repo, interval: time.Minute}

	gateway, err := h.devices.UpsertFromInform(ctx, cwmp.DeviceID{Manufacturer: "Nokia", OUI: "A4FC77", ProductClass: "3TG02241", SerialNumber: "CSC25430018F"}, []string{"0 BOOTSTRAP"})
	if err != nil {
		t.Fatal(err)
	}
	beacon, err := h.devices.UpsertFromInform(ctx, cwmp.DeviceID{Manufacturer: "ALCL", OUI: "F89B6E", ProductClass: "Beacon 3.1", SerialNumber: "ALCLB0000001"}, []string{"0 BOOTSTRAP"})
	if err != nil {
		t.Fatal(err)
	}
	h.cacheParameterValues(ctx, beacon.ID, []cwmp.ParameterValueStruct{
		{Name: devices.GatewayInfoManufacturerOUI, Value: "A4FC77"},
		{Name: devices.GatewayInfoProductClass, Value: "3TG02241"},
		{Name: devices.GatewayInfoSerialNumber, Value: "CSC25430018F"},
		{Name: "Device.Ethernet.Link.1.MACAddress", Value: "BC:51:5F:5E:7C:D3"},
	}, parameters.SourceGetValues)

	h.recordWiFiInsights(ctx, gateway.ID, []cwmp.ParameterValueStruct{
		{Name: "Device.Hosts.Host.1.PhysAddress", Value: "bc:51:5f:5e:7c:d3"},
		{Name: "Device.Hosts.Host.1.Active", Value: "1"},
		{Name: "Device.Hosts.Host.1.InterfaceType", Value: "802.11"},
		{Name: "Device.Hosts.Host.2.PhysAddress", Value: "32:a9:5c:44:3e:9a"},
		{Name: "Device.Hosts.Host.2.Active", Value: "1"},
		{Name: "Device.Hosts.Host.2.InterfaceType", Value: "802.11"},
	})
	ov, err := repo.Overview(ctx, gateway.ID, wifiinsights.Windows["2h"], time.Now().UTC().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(ov.Clients) != 1 || ov.Clients[0].MAC != "32:a9:5c:44:3e:9a" {
		t.Fatalf("gateway clients = %+v, want only the phone", ov.Clients)
	}
}
