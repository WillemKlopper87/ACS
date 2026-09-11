package devices

import (
	"errors"
	"testing"
)

func TestLinkUspAgentReconnect(t *testing.T) {
	ctx, r := newDevicesTestRepo(t)
	d, err := r.UpsertFromOnBoard(ctx, "001122", "Router", "ABC123")
	if err != nil {
		t.Fatalf("seed device: %v", err)
	}

	if err := r.LinkUspAgent(ctx, d.ID, "os::001122-ABC123", "WebSocket"); err != nil {
		t.Fatalf("first link: %v", err)
	}
	agent, err := r.GetUspAgentByEndpointID(ctx, "os::001122-ABC123")
	if err != nil {
		t.Fatalf("get after first link: %v", err)
	}
	if agent.DeviceID != d.ID || agent.MTPKind != "WebSocket" || !agent.Connected {
		t.Errorf("agent after first link = %+v, want device %s, WebSocket, connected", agent, d.ID)
	}

	// Reconnect: same device, new endpoint id and MTP kind — same row,
	// updated in place, not a second row.
	if err := r.LinkUspAgent(ctx, d.ID, "os::001122-ABC123-v2", "MQTT"); err != nil {
		t.Fatalf("reconnect link: %v", err)
	}
	agent, err = r.GetUspAgentByEndpointID(ctx, "os::001122-ABC123-v2")
	if err != nil {
		t.Fatalf("get after reconnect: %v", err)
	}
	if agent.DeviceID != d.ID || agent.MTPKind != "MQTT" || !agent.Connected {
		t.Errorf("agent after reconnect = %+v, want device %s, MQTT, connected", agent, d.ID)
	}

	// The old endpoint id must no longer resolve — it was overwritten in
	// place, not left as a stale second row.
	if _, err := r.GetUspAgentByEndpointID(ctx, "os::001122-ABC123"); !errors.Is(err, ErrUspAgentNotFound) {
		t.Errorf("old endpoint id still resolves: %v, want ErrUspAgentNotFound", err)
	}
}

func TestLinkUspAgentEndpointCollision(t *testing.T) {
	ctx, r := newDevicesTestRepo(t)
	dA, err := r.UpsertFromOnBoard(ctx, "001122", "Router", "ABC123")
	if err != nil {
		t.Fatalf("seed device A: %v", err)
	}
	dB, err := r.UpsertFromOnBoard(ctx, "001122", "Router", "XYZ789")
	if err != nil {
		t.Fatalf("seed device B: %v", err)
	}

	if err := r.LinkUspAgent(ctx, dA.ID, "shared-endpoint", "WebSocket"); err != nil {
		t.Fatalf("link A: %v", err)
	}
	err = r.LinkUspAgent(ctx, dB.ID, "shared-endpoint", "WebSocket")
	if !errors.Is(err, ErrEndpointIDInUse) {
		t.Errorf("link B with A's endpoint id returned %v, want ErrEndpointIDInUse", err)
	}

	// A's row must be untouched by B's failed attempt.
	agent, err := r.GetUspAgentByEndpointID(ctx, "shared-endpoint")
	if err != nil || agent.DeviceID != dA.ID {
		t.Errorf("after collision, shared-endpoint resolves to %+v (err %v), want device A %s", agent, err, dA.ID)
	}
}

func TestMarkUspAgentDisconnected(t *testing.T) {
	ctx, r := newDevicesTestRepo(t)
	d, err := r.UpsertFromOnBoard(ctx, "001122", "Router", "ABC123")
	if err != nil {
		t.Fatalf("seed device: %v", err)
	}
	if err := r.LinkUspAgent(ctx, d.ID, "os::endpoint", "WebSocket"); err != nil {
		t.Fatalf("link: %v", err)
	}

	if err := r.MarkUspAgentDisconnected(ctx, d.ID); err != nil {
		t.Fatalf("mark disconnected: %v", err)
	}
	agent, err := r.GetUspAgentByEndpointID(ctx, "os::endpoint")
	if err != nil {
		t.Fatalf("get after disconnect: %v", err)
	}
	if agent.Connected {
		t.Errorf("agent.Connected = true after MarkUspAgentDisconnected, want false")
	}
}

func TestMarkUspAgentDisconnectedUnknownDevice(t *testing.T) {
	ctx, r := newDevicesTestRepo(t)
	if err := r.MarkUspAgentDisconnected(ctx, "00000000-0000-0000-0000-000000000000"); err != nil {
		t.Errorf("MarkUspAgentDisconnected for unknown device = %v, want nil (no-op)", err)
	}
}

func TestGetUspAgentByEndpointID(t *testing.T) {
	ctx, r := newDevicesTestRepo(t)
	d, err := r.UpsertFromOnBoard(ctx, "001122", "Router", "ABC123")
	if err != nil {
		t.Fatalf("seed device: %v", err)
	}
	if err := r.LinkUspAgent(ctx, d.ID, "os::endpoint", "MQTT"); err != nil {
		t.Fatalf("link: %v", err)
	}

	agent, err := r.GetUspAgentByEndpointID(ctx, "os::endpoint")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if agent.DeviceID != d.ID || agent.EndpointID != "os::endpoint" || agent.MTPKind != "MQTT" {
		t.Errorf("agent = %+v, want device %s, endpoint os::endpoint, MQTT", agent, d.ID)
	}
}

func TestGetUspAgentByEndpointIDNotFound(t *testing.T) {
	ctx, r := newDevicesTestRepo(t)
	_, err := r.GetUspAgentByEndpointID(ctx, "no-such-endpoint")
	if !errors.Is(err, ErrUspAgentNotFound) {
		t.Errorf("got %v, want ErrUspAgentNotFound", err)
	}
}
