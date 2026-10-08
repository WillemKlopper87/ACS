package bulkdata

import (
	"testing"
	"time"
)

func TestHealthTransitions(t *testing.T) {
	now := time.Now()
	h := Health{IntervalSeconds: 60, RenewedAt: now, ExpiresAt: now.Add(48 * time.Hour)}
	if EvaluateHealth(h, now).State != "awaiting_first_report" {
		t.Fatal("initial state")
	}
	if EvaluateHealth(h, now.Add(5*time.Minute)).State != "missing_reports" {
		t.Fatal("missing reports not flagged")
	}
	stamp := now.Add(5 * time.Minute)
	h.LastReportAt = &stamp
	if EvaluateHealth(h, stamp).State != "receiving" {
		t.Fatal("report did not recover")
	}
	if EvaluateHealth(h, now.Add(49*time.Hour)).State != "expired" {
		t.Fatal("expired receiver not flagged")
	}
	h.Revoked = true
	if EvaluateHealth(h, now).State != "revoked" {
		t.Fatal("revoked receiver is active")
	}
}
