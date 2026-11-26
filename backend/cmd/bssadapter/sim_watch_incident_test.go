package main

import (
	"net/http"
	"strings"
	"testing"

	"acs/internal/alerting"
	"acs/internal/bss"
)

type simIncidentEnv struct {
	*bindingEnv
	devA, devB string
	acct       string
}

func (e *simIncidentEnv) setRow(t *testing.T, deviceID, state string) {
	t.Helper()
	if _, err := e.db.ExecContext(e.ctx, `INSERT INTO device_sim_watch (device_id, state, network, iccid_hint, imsi_hint, previous_iccid_hint, previous_imsi_hint, detected_at)
		VALUES ($1, $2, '65501', '…0022', '65501…0022', '…0011', '65501…0011', now())
		ON CONFLICT (device_id) DO UPDATE SET state = EXCLUDED.state`, deviceID, state); err != nil {
		t.Fatal(err)
	}
}

func (e *simIncidentEnv) deliveries(t *testing.T, eventType string) int {
	t.Helper()
	var n int
	if err := e.db.QueryRowContext(e.ctx, `SELECT count(*) FROM webhook_deliveries WHERE event_type = $1`, eventType).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func (e *simIncidentEnv) incident(t *testing.T, deviceID string) (state, priority, summary, details string, count int) {
	t.Helper()
	if err := e.db.QueryRowContext(e.ctx, `SELECT count(*) FROM alert_incidents WHERE device_id = $1 AND condition_key = $2`, deviceID, alerting.SIMChangeConditionKey).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count == 0 {
		return "", "", "", "", 0
	}
	if err := e.db.QueryRowContext(e.ctx, `SELECT state, priority, summary, details::text FROM alert_incidents WHERE device_id = $1 AND condition_key = $2`, deviceID, alerting.SIMChangeConditionKey).Scan(&state, &priority, &summary, &details); err != nil {
		t.Fatal(err)
	}
	return state, priority, summary, details, count
}

func newSIMIncidentEnv(t *testing.T, withPolicy bool) *simIncidentEnv {
	t.Helper()
	b := newBindingEnv(t)
	b.h.alertPolicies = alerting.NewRepository(b.db)
	b.h.alertIncidents = alerting.NewIncidentRepository(b.db)
	b.h.webhooks = bss.NewWebhookRepository(b.db)
	e := &simIncidentEnv{bindingEnv: b, acct: "A.1"}
	tok := bindingToken(t, b.custA, false)
	if rec := b.call(t, b.h.createMapping, http.MethodPost, "/bss/v1/mappings", tok, map[string]any{"account_id": e.acct, "oui_serial": "001122-DEV-A"}); rec.Code != http.StatusOK {
		t.Fatalf("mapping = %d %s", rec.Code, rec.Body)
	}
	if err := b.db.QueryRowContext(b.ctx, `SELECT id::text FROM devices WHERE oui_serial = '001122-DEV-A'`).Scan(&e.devA); err != nil {
		t.Fatal(err)
	}
	if err := b.db.QueryRowContext(b.ctx, `SELECT id::text FROM devices WHERE oui_serial = '001122-DEV-B'`).Scan(&e.devB); err != nil {
		t.Fatal(err)
	}
	if withPolicy {
		if _, err := b.h.alertPolicies.Create(b.ctx, alerting.Policy{Name: "fleet", Scope: alerting.ScopeFleet, Enabled: true}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := b.h.webhooks.CreateSubscription(b.ctx, &e.acct, "https://203.0.113.10/hook", "secret-0123456789abcdef", []string{"ALERT_OPENED", "ALERT_RECOVERED"}); err != nil {
		t.Fatal(err)
	}
	return e
}

func TestIngestSIMWatchOpensUpgradesRecoversAndReopens(t *testing.T) {
	e := newSIMIncidentEnv(t, true)

	e.setRow(t, e.devA, "notice")
	e.h.ingestSIMWatch(e.ctx)
	e.h.ingestSIMWatch(e.ctx) // a second cycle changes nothing
	state, prio, summary, details, n := e.incident(t, e.devA)
	if n != 1 || state != "open" || prio != "P4" || !strings.Contains(summary, "65501") {
		t.Fatalf("notice: count=%d state=%s priority=%s summary=%q", n, state, prio, summary)
	}
	if e.deliveries(t, "ALERT_OPENED") != 1 {
		t.Fatalf("ALERT_OPENED deliveries = %d, want 1", e.deliveries(t, "ALERT_OPENED"))
	}
	for _, full := range []string{"89270000", "655010000"} {
		if strings.Contains(summary+details, full) {
			t.Fatalf("incident carries an identifier: %s %s", summary, details)
		}
	}
	if !strings.Contains(details, "…0022") {
		t.Fatalf("details lack the masked hint: %s", details)
	}

	// The SIM is swapped again, onto a network the plan does not allow: the
	// priority rises, the escalation restarts and subscribers are told.
	if _, err := e.db.ExecContext(e.ctx, `UPDATE alert_incidents SET escalation_stage = 2, next_escalation_at = now() + interval '30 days' WHERE device_id = $1`, e.devA); err != nil {
		t.Fatal(err)
	}
	e.setRow(t, e.devA, "alert")
	e.h.ingestSIMWatch(e.ctx)
	if state, prio, _, _, _ = e.incident(t, e.devA); state != "open" || prio != "P2" {
		t.Fatalf("alert: state=%s priority=%s, want open P2", state, prio)
	}
	if e.deliveries(t, "ALERT_OPENED") != 2 {
		t.Fatalf("a rise in priority must notify, deliveries = %d, want 2", e.deliveries(t, "ALERT_OPENED"))
	}
	var stage int
	var overdue bool
	if err := e.db.QueryRowContext(e.ctx, `SELECT escalation_stage, next_escalation_at < now() + interval '1 day' FROM alert_incidents WHERE device_id = $1`, e.devA).Scan(&stage, &overdue); err != nil || stage != 0 || !overdue {
		t.Fatalf("escalation not restarted: stage=%d soon=%v err=%v", stage, overdue, err)
	}
	e.h.ingestSIMWatch(e.ctx) // the same verdict again changes nothing
	if e.deliveries(t, "ALERT_OPENED") != 2 {
		t.Fatalf("an unchanged verdict notified again: %d", e.deliveries(t, "ALERT_OPENED"))
	}
	// A different SIM again while the incident is open is a new verdict.
	if _, err := e.db.ExecContext(e.ctx, `UPDATE device_sim_watch SET detected_at = now() + interval '1 minute', imsi_hint = '65501…0033' WHERE device_id = $1`, e.devA); err != nil {
		t.Fatal(err)
	}
	e.h.ingestSIMWatch(e.ctx)
	if e.deliveries(t, "ALERT_OPENED") != 3 {
		t.Fatalf("a new verdict on an open incident must notify, deliveries = %d, want 3", e.deliveries(t, "ALERT_OPENED"))
	}

	e.setRow(t, e.devA, "ok")
	e.h.ingestSIMWatch(e.ctx)
	if state, _, _, _, _ = e.incident(t, e.devA); state != "recovered" {
		t.Fatalf("after approval: state=%s, want recovered", state)
	}
	if e.deliveries(t, "ALERT_RECOVERED") != 1 {
		t.Fatalf("ALERT_RECOVERED deliveries = %d, want 1", e.deliveries(t, "ALERT_RECOVERED"))
	}

	e.setRow(t, e.devA, "notice")
	e.h.ingestSIMWatch(e.ctx)
	if state, _, _, _, n = e.incident(t, e.devA); n != 1 || state != "open" {
		t.Fatalf("a later change: count=%d state=%s, want the incident reopened", n, state)
	}
	if e.deliveries(t, "ALERT_OPENED") != 4 {
		t.Fatalf("a reopened incident must notify, ALERT_OPENED deliveries = %d, want 4", e.deliveries(t, "ALERT_OPENED"))
	}
}

func TestIngestSIMWatchIgnoresUnmappedDevicesAndMissingPolicies(t *testing.T) {
	e := newSIMIncidentEnv(t, true)
	e.setRow(t, e.devB, "alert") // DEV-B has no account mapping
	e.h.ingestSIMWatch(e.ctx)
	if _, _, _, _, n := e.incident(t, e.devB); n != 0 {
		t.Fatalf("an unmapped device got %d incidents", n)
	}

	noPolicy := newSIMIncidentEnv(t, false)
	noPolicy.setRow(t, noPolicy.devA, "alert")
	noPolicy.h.ingestSIMWatch(noPolicy.ctx)
	if _, _, _, _, n := noPolicy.incident(t, noPolicy.devA); n != 0 {
		t.Fatalf("with no matching policy %d incidents were opened", n)
	}
}
