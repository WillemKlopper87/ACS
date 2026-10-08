package bulkdata

import "testing"

func TestBulkDataRejectsUnconfiguredAndNestedMetrics(t *testing.T) {
	allowed := []string{"Device.Cellular.Interface.1.RSRP"}
	for _, raw := range []string{`{"Report":[{"Device.Password":"secret"}]}`, `{"Report":[{"Device.Cellular.Interface.1.RSRP":{"value":1}}]}`, `{"Report":[]}`, `{"Report":[{"CollectionTime":"2026-10-05T00:00:00Z"}]}`} {
		if err := ValidateReport([]byte(raw), allowed); err == nil {
			t.Errorf("accepted %s", raw)
		}
	}
	if err := ValidateReport([]byte(`{"Report":[{"CollectionTime":"2026-10-05T00:00:00Z","Device.Cellular.Interface.1.RSRP":-90}]}`), allowed); err != nil {
		t.Fatal(err)
	}
}
