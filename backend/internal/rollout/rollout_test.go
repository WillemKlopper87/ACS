package rollout

import (
	"database/sql"
	"testing"
	"time"
)

func strp(s string) *string { return &s }

func TestInMaintenanceWindowUnconfiguredAlwaysTrue(t *testing.T) {
	now := time.Date(2026, 8, 11, 3, 0, 0, 0, time.UTC)
	if !InMaintenanceWindow(now, nil, nil) {
		t.Error("no window configured should always return true")
	}
}

func TestInMaintenanceWindowSameDay(t *testing.T) {
	start, end := strp("22:00:00"), strp("23:00:00")
	inside := time.Date(2026, 8, 11, 22, 30, 0, 0, time.UTC)
	before := time.Date(2026, 8, 11, 21, 59, 0, 0, time.UTC)
	atEnd := time.Date(2026, 8, 11, 23, 0, 0, 0, time.UTC) // end is exclusive

	if !InMaintenanceWindow(inside, start, end) {
		t.Error("22:30 should be inside a 22:00-23:00 window")
	}
	if InMaintenanceWindow(before, start, end) {
		t.Error("21:59 should be outside a 22:00-23:00 window")
	}
	if InMaintenanceWindow(atEnd, start, end) {
		t.Error("23:00 (the end boundary) should be excluded")
	}
}

// TestInMaintenanceWindowOvernightWrap is the case the doc comment calls
// out explicitly: start > end means the window wraps past midnight
// (e.g. 22:00-06:00 for an overnight maintenance slot).
func TestInMaintenanceWindowOvernightWrap(t *testing.T) {
	start, end := strp("22:00:00"), strp("06:00:00")

	lateNight := time.Date(2026, 8, 11, 23, 30, 0, 0, time.UTC)
	earlyMorning := time.Date(2026, 8, 11, 3, 0, 0, 0, time.UTC)
	midday := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)

	if !InMaintenanceWindow(lateNight, start, end) {
		t.Error("23:30 should be inside an overnight 22:00-06:00 window")
	}
	if !InMaintenanceWindow(earlyMorning, start, end) {
		t.Error("03:00 should be inside an overnight 22:00-06:00 window")
	}
	if InMaintenanceWindow(midday, start, end) {
		t.Error("12:00 should be outside an overnight 22:00-06:00 window")
	}
}

func TestInMaintenanceWindowUnparseableFailsOpen(t *testing.T) {
	start, end := strp("not-a-time"), strp("06:00:00")
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	if !InMaintenanceWindow(now, start, end) {
		t.Error("an unparseable configured window should fail open (true), not silently block every start")
	}
}

func TestDeriveState(t *testing.T) {
	cases := []struct {
		status sql.NullString
		want   string
	}{
		{sql.NullString{}, DeviceStateEligible},
		{sql.NullString{String: "QUEUED", Valid: true}, DeviceStateQueued},
		{sql.NullString{String: "RPC_SENT", Valid: true}, DeviceStateQueued},
		{sql.NullString{String: "AWAITING_TRANSFER_COMPLETE", Valid: true}, DeviceStateDownloading},
		{sql.NullString{String: "SUCCESS", Valid: true}, DeviceStateSuccess},
		{sql.NullString{String: "FAILED", Valid: true}, DeviceStateFailed},
		{sql.NullString{String: "TIMEOUT", Valid: true}, DeviceStateFailed},
		{sql.NullString{String: "IN_PROGRESS", Valid: true}, DeviceStateQueued},
	}
	for _, c := range cases {
		if got := deriveState(c.status); got != c.want {
			t.Errorf("deriveState(%+v) = %q, want %q", c.status, got, c.want)
		}
	}
}
