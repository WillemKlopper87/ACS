package devices

import (
	"testing"
)

// TestRecordEventAndEvents covers the basic round trip and Events'
// most-recent-first ordering.
func TestRecordEventAndEvents(t *testing.T) {
	ctx, r := newDevicesTestRepo(t)

	d, err := r.UpsertFromOnBoard(ctx, "001349", "NR7101", "EVENTS-01")
	if err != nil {
		t.Fatalf("UpsertFromOnBoard: %v", err)
	}

	if err := r.RecordEvent(ctx, d.ID, "msg-1", "Device.WiFi.SSID.2.", "ObjectCreation", map[string]string{"UniqueKeys": "SSID.2"}); err != nil {
		t.Fatalf("RecordEvent msg-1: %v", err)
	}
	if err := r.RecordEvent(ctx, d.ID, "msg-2", "Device.WiFi.SSID.3.", "ObjectDeletion", nil); err != nil {
		t.Fatalf("RecordEvent msg-2: %v", err)
	}
	if err := r.RecordEvent(ctx, d.ID, "msg-3", "Device.", "Boot", map[string]string{"CommandKey": "abc"}); err != nil {
		t.Fatalf("RecordEvent msg-3: %v", err)
	}

	events, err := r.Events(ctx, d.ID, 10)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("got %d events, want 3", len(events))
	}

	// recorded_at DESC -- most recently inserted (msg-3) first.
	if events[0].MsgID != "msg-3" {
		t.Errorf("events[0].MsgID = %q, want %q (most recent first)", events[0].MsgID, "msg-3")
	}
	if events[0].EventName != "Boot" || events[0].ObjPath != "Device." {
		t.Errorf("events[0] = %+v, want EventName=Boot ObjPath=Device.", events[0])
	}
	if events[0].Params["CommandKey"] != "abc" {
		t.Errorf("events[0].Params[CommandKey] = %q, want %q", events[0].Params["CommandKey"], "abc")
	}

	if events[2].MsgID != "msg-1" {
		t.Errorf("events[2].MsgID = %q, want %q (oldest last)", events[2].MsgID, "msg-1")
	}
	if events[2].Params["UniqueKeys"] != "SSID.2" {
		t.Errorf("events[2].Params[UniqueKeys] = %q, want %q", events[2].Params["UniqueKeys"], "SSID.2")
	}
	if events[2].DeviceID != d.ID {
		t.Errorf("events[2].DeviceID = %q, want %q", events[2].DeviceID, d.ID)
	}

	// msg-2's params were passed as nil -- must round-trip as an empty
	// map, not nil, and not error.
	if events[1].MsgID != "msg-2" {
		t.Errorf("events[1].MsgID = %q, want %q", events[1].MsgID, "msg-2")
	}
	if len(events[1].Params) != 0 {
		t.Errorf("events[1].Params = %v, want empty", events[1].Params)
	}
}

// TestRecordEventDedup covers device_events' ON CONFLICT (device_id,
// msg_id) DO NOTHING through the Go method: a second RecordEvent call with
// the same (deviceID, msgID) is a true no-op -- no second row, and the
// first insert's values survive untouched.
func TestRecordEventDedup(t *testing.T) {
	ctx, r := newDevicesTestRepo(t)

	d, err := r.UpsertFromOnBoard(ctx, "001349", "NR7101", "EVENTS-DEDUP-01")
	if err != nil {
		t.Fatalf("UpsertFromOnBoard: %v", err)
	}

	if err := r.RecordEvent(ctx, d.ID, "msg-dup", "Device.WiFi.", "ObjectCreation", map[string]string{"UniqueKeys": "first"}); err != nil {
		t.Fatalf("first RecordEvent: %v", err)
	}
	if err := r.RecordEvent(ctx, d.ID, "msg-dup", "Device.Other.", "ObjectDeletion", map[string]string{"UniqueKeys": "second"}); err != nil {
		t.Fatalf("second RecordEvent (duplicate msg_id): %v", err)
	}

	events, err := r.Events(ctx, d.ID, 10)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("got %d events after duplicate RecordEvent, want 1", len(events))
	}
	if events[0].ObjPath != "Device.WiFi." {
		t.Errorf("ObjPath = %q, want the first insert's %q (conflict was not a no-op)", events[0].ObjPath, "Device.WiFi.")
	}
	if events[0].EventName != "ObjectCreation" {
		t.Errorf("EventName = %q, want the first insert's %q (conflict was not a no-op)", events[0].EventName, "ObjectCreation")
	}
	if events[0].Params["UniqueKeys"] != "first" {
		t.Errorf("Params[UniqueKeys] = %q, want the first insert's %q (conflict was not a no-op)", events[0].Params["UniqueKeys"], "first")
	}
}
