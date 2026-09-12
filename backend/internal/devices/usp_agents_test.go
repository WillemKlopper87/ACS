package devices

import (
	"errors"
	"testing"
)

func TestLinkUspAgentReconnect(t *testing.T) {
	ctx, r := newDevicesTestRepo(t)
	d := seedUSPDevice(t, ctx, r, "001122", "Router", "ABC123")

	if err := r.LinkUspAgent(ctx, d.ID, "os::001122-ABC123", "WebSocket", nil); err != nil {
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
	if err := r.LinkUspAgent(ctx, d.ID, "os::001122-ABC123-v2", "MQTT", nil); err != nil {
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
	dA := seedUSPDevice(t, ctx, r, "001122", "Router", "ABC123")
	dB := seedUSPDevice(t, ctx, r, "001122", "Router", "XYZ789")

	if err := r.LinkUspAgent(ctx, dA.ID, "shared-endpoint", "WebSocket", nil); err != nil {
		t.Fatalf("link A: %v", err)
	}
	err := r.LinkUspAgent(ctx, dB.ID, "shared-endpoint", "WebSocket", nil)
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
	d := seedUSPDevice(t, ctx, r, "001122", "Router", "ABC123")
	if err := r.LinkUspAgent(ctx, d.ID, "os::endpoint", "WebSocket", nil); err != nil {
		t.Fatalf("link: %v", err)
	}

	if err := r.MarkUspAgentDisconnected(ctx, d.ID, "os::endpoint"); err != nil {
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
	if err := r.MarkUspAgentDisconnected(ctx, "00000000-0000-0000-0000-000000000000", "no-such-endpoint"); err != nil {
		t.Errorf("MarkUspAgentDisconnected for unknown device = %v, want nil (no-op)", err)
	}
}

// TestMarkUspAgentDisconnectedStaleEndpointIsNoOp proves the
// endpoint-aware guard (final-review finding 3): a device reconnects
// under a NEW endpoint id (LinkUspAgent retargets the single per-device
// row in place, as TestLinkUspAgentReconnect covers), and only afterward
// does the OLD endpoint's connection get around to disconnecting -- e.g. a
// delayed teardown from the connection that was superseded. That stale
// disconnect must be a no-op: the row's endpoint_id is now the NEW
// endpoint id, not the old one, so `connected` must stay true.
func TestMarkUspAgentDisconnectedStaleEndpointIsNoOp(t *testing.T) {
	ctx, r := newDevicesTestRepo(t)
	d := seedUSPDevice(t, ctx, r, "001122", "Router", "ABC123")
	if err := r.LinkUspAgent(ctx, d.ID, "endpoint-old", "WebSocket", nil); err != nil {
		t.Fatalf("link old endpoint: %v", err)
	}

	// Device reconnects under a NEW endpoint id -- same device_id row,
	// retargeted in place.
	if err := r.LinkUspAgent(ctx, d.ID, "endpoint-new", "MQTT", nil); err != nil {
		t.Fatalf("link new endpoint: %v", err)
	}

	// The OLD endpoint's connection disconnects afterward. Must be a
	// no-op: the row's endpoint_id is "endpoint-new" now, not
	// "endpoint-old".
	if err := r.MarkUspAgentDisconnected(ctx, d.ID, "endpoint-old"); err != nil {
		t.Fatalf("mark disconnected on stale endpoint: %v", err)
	}

	agent, err := r.GetUspAgentByEndpointID(ctx, "endpoint-new")
	if err != nil {
		t.Fatalf("get after stale disconnect: %v", err)
	}
	if !agent.Connected {
		t.Error("agent.Connected = false after a stale (superseded-endpoint) disconnect, want true: the new endpoint's session is still live")
	}

	// A genuine disconnect for the CURRENT endpoint id still works.
	if err := r.MarkUspAgentDisconnected(ctx, d.ID, "endpoint-new"); err != nil {
		t.Fatalf("mark disconnected on current endpoint: %v", err)
	}
	agent, err = r.GetUspAgentByEndpointID(ctx, "endpoint-new")
	if err != nil {
		t.Fatalf("get after genuine disconnect: %v", err)
	}
	if agent.Connected {
		t.Error("agent.Connected = true after a genuine disconnect on the current endpoint, want false")
	}
}

func TestGetUspAgentByEndpointID(t *testing.T) {
	ctx, r := newDevicesTestRepo(t)
	d := seedUSPDevice(t, ctx, r, "001122", "Router", "ABC123")
	if err := r.LinkUspAgent(ctx, d.ID, "os::endpoint", "MQTT", nil); err != nil {
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

// TestGetUspAgentByDeviceID covers the reverse of
// TestGetUspAgentByEndpointID: dispatch needs device_id -> endpoint_id ->
// live mtp.Conn, and this is the first hop.
func TestGetUspAgentByDeviceID(t *testing.T) {
	ctx, r := newDevicesTestRepo(t)
	d := seedUSPDevice(t, ctx, r, "001122", "Router", "ABC123")
	if err := r.LinkUspAgent(ctx, d.ID, "os::endpoint", "MQTT", nil); err != nil {
		t.Fatalf("link: %v", err)
	}

	agent, err := r.GetUspAgentByDeviceID(ctx, d.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if agent.DeviceID != d.ID || agent.EndpointID != "os::endpoint" || agent.MTPKind != "MQTT" {
		t.Errorf("agent = %+v, want device %s, endpoint os::endpoint, MQTT", agent, d.ID)
	}
}

func TestGetUspAgentByDeviceIDNotFound(t *testing.T) {
	ctx, r := newDevicesTestRepo(t)
	d := seedUSPDevice(t, ctx, r, "001122", "Router", "ABC123")
	_, err := r.GetUspAgentByDeviceID(ctx, d.ID)
	if !errors.Is(err, ErrUspAgentNotFound) {
		t.Errorf("got %v, want ErrUspAgentNotFound", err)
	}
}
