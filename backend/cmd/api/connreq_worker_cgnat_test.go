package main

import (
	"encoding/json"
	"testing"

	"acs/internal/devices"
	"acs/internal/jobs"
)

// TestTightenPeriodicInformQueuesSetParameter covers the CGNAT fallback
// lever: once both direct and Annex G Connection Request have failed to
// raise a session, the worker's only remaining option is to ask the device
// to check back in on its own sooner. This proves the queued job targets
// the right TR-181 path for a Device:2 root and carries the documented
// target interval.
func TestTightenPeriodicInformQueuesSetParameter(t *testing.T) {
	e := newTestEnv(t)
	deviceID := e.device("CGNAT-1", nil)
	w := &connectionRequestWorker{logger: e.h.logger, jobs: e.h.jobs, devices: e.h.devices}

	device, err := e.h.devices.Get(e.ctx, deviceID)
	if err != nil {
		t.Fatal(err)
	}
	w.tightenPeriodicInform(e.ctx, device)

	list, err := e.h.jobs.List(e.ctx, deviceID, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Type != jobs.TypeSetParameter {
		t.Fatalf("jobs = %+v, want exactly one SET_PARAMETER job", list)
	}
	var payload jobs.SetParameterPayload
	if err := json.Unmarshal(list[0].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Parameters) != 1 ||
		payload.Parameters[0].Name != "Device.ManagementServer.PeriodicInformInterval" ||
		payload.Parameters[0].Value != "300" {
		t.Fatalf("payload = %+v, want Device.ManagementServer.PeriodicInformInterval=300", payload)
	}
}

// TestTightenPeriodicInformUsesIGD1PathForLegacyRoot proves the path
// switches for a TR-098 device -- the same root-awareness every other
// canonical-path resolver in this codebase already requires.
func TestTightenPeriodicInformUsesIGD1PathForLegacyRoot(t *testing.T) {
	e := newTestEnv(t)
	deviceID := e.device("CGNAT-2", nil)
	if err := e.h.devices.UpdateDataModelRoot(e.ctx, deviceID, devices.DataModelRootIGD1); err != nil {
		t.Fatal(err)
	}
	w := &connectionRequestWorker{logger: e.h.logger, jobs: e.h.jobs, devices: e.h.devices}

	device, err := e.h.devices.Get(e.ctx, deviceID)
	if err != nil {
		t.Fatal(err)
	}
	w.tightenPeriodicInform(e.ctx, device)

	list, err := e.h.jobs.List(e.ctx, deviceID, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	var payload jobs.SetParameterPayload
	if err := json.Unmarshal(list[0].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Parameters[0].Name != "InternetGatewayDevice.ManagementServer.PeriodicInformInterval" {
		t.Fatalf("path = %q, want the IGD1 equivalent", payload.Parameters[0].Name)
	}
}

// TestTightenPeriodicInformIsIdempotentPerDevice proves repeated failed
// connection-request attempts (the realistic call pattern -- a
// permanently-CGNAT'd device fails Connection Request every time it's
// tried) don't pile up duplicate SetParameterValues jobs for the same
// device.
func TestTightenPeriodicInformIsIdempotentPerDevice(t *testing.T) {
	e := newTestEnv(t)
	deviceID := e.device("CGNAT-3", nil)
	w := &connectionRequestWorker{logger: e.h.logger, jobs: e.h.jobs, devices: e.h.devices}

	device, err := e.h.devices.Get(e.ctx, deviceID)
	if err != nil {
		t.Fatal(err)
	}
	w.tightenPeriodicInform(e.ctx, device)
	w.tightenPeriodicInform(e.ctx, device)
	w.tightenPeriodicInform(e.ctx, device)

	list, err := e.h.jobs.List(e.ctx, deviceID, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("jobs = %+v, want exactly one job despite three calls", list)
	}
}
