package main

import (
	"encoding/json"
	"reflect"
	"testing"

	"acs/internal/cwmp"
	"acs/internal/devices"
	"acs/internal/jobs"
	"acs/internal/parameters"
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
