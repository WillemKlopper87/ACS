package bss

import (
	"testing"
	"time"
)

func order(externalID, accountID, commandKey string, createdAt time.Time, params ...ParameterWrite) OrderRecord {
	return OrderRecord{
		ExternalOrderID: externalID, AccountID: accountID, CommandKey: commandKey,
		CreatedAt: createdAt, Parameters: params,
	}
}

var t0 = time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC)

func TestComputeReconciliationMatch(t *testing.T) {
	orders := []OrderRecord{
		order("ORD-1", "ACC-1", "ck-1", t0, ParameterWrite{Name: "Device.WiFi.SSID.1.SSID", Value: "HomeNet"}),
	}
	confirmed := map[string]bool{"ck-1": true}
	current := map[string]CachedParameter{"Device.WiFi.SSID.1.SSID": {Value: "HomeNet", UpdatedAt: "2026-09-18T10:05:00Z"}}

	got := ComputeReconciliation("dev-1", orders, confirmed, current, t0.Add(time.Hour))
	if len(got.Fields) != 1 || got.Fields[0].Status != ReconcileMatch {
		t.Fatalf("fields = %+v, want a single match", got.Fields)
	}
	if got.AccountID != "ACC-1" {
		t.Errorf("account = %q, want ACC-1", got.AccountID)
	}
}

func TestComputeReconciliationDrift(t *testing.T) {
	orders := []OrderRecord{
		order("ORD-1", "ACC-1", "ck-1", t0, ParameterWrite{Name: "Device.WiFi.SSID.1.SSID", Value: "HomeNet"}),
	}
	confirmed := map[string]bool{"ck-1": true}
	current := map[string]CachedParameter{"Device.WiFi.SSID.1.SSID": {Value: "SomeoneChangedThis"}}

	got := ComputeReconciliation("dev-1", orders, confirmed, current, t0.Add(time.Hour))
	if len(got.Fields) != 1 || got.Fields[0].Status != ReconcileDrift {
		t.Fatalf("fields = %+v, want a single drift", got.Fields)
	}
	if got.Fields[0].ActualValue != "SomeoneChangedThis" {
		t.Errorf("actual value = %q, want the device's own reported value", got.Fields[0].ActualValue)
	}
}

func TestComputeReconciliationUnknownWhenNeverCached(t *testing.T) {
	orders := []OrderRecord{
		order("ORD-1", "ACC-1", "ck-1", t0, ParameterWrite{Name: "Device.WiFi.SSID.1.SSID", Value: "HomeNet"}),
	}
	got := ComputeReconciliation("dev-1", orders, map[string]bool{"ck-1": true}, map[string]CachedParameter{}, t0)
	if len(got.Fields) != 1 || got.Fields[0].Status != ReconcileUnknown {
		t.Fatalf("fields = %+v, want unknown when the device never reported this path", got.Fields)
	}
}

// TestComputeReconciliationUnconfirmedOrderIsNeverCompared is the core
// safety property: an order whose job hasn't (yet) succeeded must not be
// compared against the device's current state at all -- there is no
// confirmed BSS intent yet, so "match" or "drift" would both be a
// fabricated claim.
func TestComputeReconciliationUnconfirmedOrderIsNeverCompared(t *testing.T) {
	orders := []OrderRecord{
		order("ORD-1", "ACC-1", "ck-1", t0, ParameterWrite{Name: "Device.WiFi.SSID.1.SSID", Value: "HomeNet"}),
	}
	// The device happens to already report exactly this value -- perhaps
	// by coincidence, perhaps from an even earlier order -- but the job
	// backing THIS order was never confirmed, so it must read unconfirmed
	// regardless of what current says.
	current := map[string]CachedParameter{"Device.WiFi.SSID.1.SSID": {Value: "HomeNet"}}
	got := ComputeReconciliation("dev-1", orders, map[string]bool{}, current, t0)
	if len(got.Fields) != 1 || got.Fields[0].Status != ReconcileUnconfirmed {
		t.Fatalf("fields = %+v, want unconfirmed", got.Fields)
	}
	if got.Fields[0].ActualValue != "" {
		t.Errorf("actual value = %q, want empty -- an unconfirmed field must not report a comparison result", got.Fields[0].ActualValue)
	}
}

// TestComputeReconciliationNewestOrderWinsPerPath is the ordering
// contract: a newer, unconfirmed order for a path must NOT be skipped in
// favor of an older confirmed order for the same path -- the newest order
// is the actual current BSS intent, confirmed or not.
func TestComputeReconciliationNewestOrderWinsPerPath(t *testing.T) {
	older := order("ORD-1", "ACC-1", "ck-old", t0, ParameterWrite{Name: "Device.WiFi.SSID.1.SSID", Value: "OldNet"})
	newer := order("ORD-2", "ACC-1", "ck-new", t0.Add(time.Minute), ParameterWrite{Name: "Device.WiFi.SSID.1.SSID", Value: "NewNet"})
	// OrdersForDevice's contract: most-recent-first.
	orders := []OrderRecord{newer, older}
	confirmed := map[string]bool{"ck-old": true} // only the OLDER order's job succeeded
	current := map[string]CachedParameter{"Device.WiFi.SSID.1.SSID": {Value: "OldNet"}}

	got := ComputeReconciliation("dev-1", orders, confirmed, current, t0.Add(time.Hour))
	if len(got.Fields) != 1 {
		t.Fatalf("fields = %+v, want exactly one field (the path is claimed once)", got.Fields)
	}
	if got.Fields[0].ExternalOrderID != "ORD-2" || got.Fields[0].Status != ReconcileUnconfirmed {
		t.Fatalf("field = %+v, want ORD-2/unconfirmed -- the newest order must win even though it's the unconfirmed one", got.Fields[0])
	}
}

// TestComputeReconciliationSecretPathIsUnverifiable proves a password-
// shaped path never gets compared, even when confirmed and even when the
// device happens to report a cached value for it.
func TestComputeReconciliationSecretPathIsUnverifiable(t *testing.T) {
	orders := []OrderRecord{
		order("ORD-1", "ACC-1", "ck-1", t0,
			ParameterWrite{Name: "Device.WiFi.AccessPoint.1.Security.KeyPassphrase", Value: "hunter2"}),
	}
	current := map[string]CachedParameter{"Device.WiFi.AccessPoint.1.Security.KeyPassphrase": {Value: ""}}
	got := ComputeReconciliation("dev-1", orders, map[string]bool{"ck-1": true}, current, t0)
	if len(got.Fields) != 1 || got.Fields[0].Status != ReconcileUnverifiable {
		t.Fatalf("fields = %+v, want unverifiable for a passphrase path", got.Fields)
	}
	if got.Fields[0].ActualValue != "" {
		t.Errorf("actual value = %q, want empty for an unverifiable secret field", got.Fields[0].ActualValue)
	}
}

func TestReconciliationPathsAndCommandKeysDeduplicate(t *testing.T) {
	orders := []OrderRecord{
		order("ORD-2", "ACC-1", "ck-2", t0.Add(time.Minute), ParameterWrite{Name: "Device.WiFi.SSID.1.SSID", Value: "NewNet"}),
		order("ORD-1", "ACC-1", "ck-1", t0,
			ParameterWrite{Name: "Device.WiFi.SSID.1.SSID", Value: "OldNet"},
			ParameterWrite{Name: "Device.WiFi.SSID.1.Enable", Value: "1"}),
	}
	paths := ReconciliationPaths(orders)
	if len(paths) != 2 {
		t.Fatalf("paths = %v, want 2 distinct paths", paths)
	}
	keys := ReconciliationCommandKeys(orders)
	if len(keys) != 2 {
		t.Fatalf("command keys = %v, want 2 distinct keys", keys)
	}
}
