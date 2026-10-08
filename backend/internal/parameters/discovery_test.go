package parameters

import (
	"errors"
	"testing"
	"time"
)

func TestDiscoveryEvidenceAndFirmwareInvalidation(t *testing.T) {
	ctx, r, id := newParametersTestRepo(t)
	versionPath := "Device.DeviceInfo.SoftwareVersion"
	if err := r.Upsert(ctx, id, map[string]CachedValue{versionPath: {Value: "1.0", UpdatedAt: time.Now(), Source: SourceInform}}); err != nil {
		t.Fatal(err)
	}
	s := DiscoverySnapshot{CommandKey: "test-discovery", Protocol: "CWMP", Root: "Device.", ExpectedFirmware: "1.0", Names: map[string]bool{"Device.WiFi.SSID.1.SSID": true}}
	if err := r.SaveDiscovery(ctx, id, s); err != nil {
		t.Fatal(err)
	}
	snapshots, err := r.DiscoverySnapshots(ctx, id)
	if err != nil || len(snapshots) != 1 || snapshots[0].Evidence["firmware"] != "1.0" {
		t.Fatal(snapshots, err)
	}
	if err := r.Upsert(ctx, id, map[string]CachedValue{versionPath: {Value: "2.0", UpdatedAt: time.Now(), Source: SourceInform}}); err != nil {
		t.Fatal(err)
	}
	names, err := r.GetNames(ctx, id)
	if err != nil || !names.Stale || len(names.Names) != 0 {
		t.Fatal(names, err)
	}
	var count int
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM jobs WHERE device_id=$1 AND type='PARAMETER_DISCOVERY' AND payload->>'firmware'='2.0'`, id).Scan(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
	if err := r.SaveDiscovery(ctx, id, s); !errors.Is(err, ErrFirmwareChanged) {
		t.Fatalf("old firmware tree published: %v", err)
	}
	snapshots, err = r.DiscoverySnapshots(ctx, id)
	if err != nil || len(snapshots) != 1 {
		t.Fatal("previous evidence lost", err)
	}
}
