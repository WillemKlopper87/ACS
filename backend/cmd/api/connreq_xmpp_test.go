package main

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"acs/internal/connreq"
	"acs/internal/devices"
	"acs/internal/jobs"
	"acs/internal/parameters"
	"acs/internal/xmpp"
)

// TestSelectConnReqPath pins the worker's path order: XMPP first whenever
// the ACS has XMPP and the CPE reported a JID (no 10 s HTTP dial for a
// NAT'd device), and otherwise exactly the pre-XMPP behaviour.
func TestSelectConnReqPath(t *testing.T) {
	const jid = "xmpp-1@xmpp.acs.lab/cwmp"
	cases := []struct {
		name string
		in   connReqInputs
		want string
	}{
		{"xmpp first for a NAT'd device", connReqInputs{HasURL: true, Mode: devices.ModePeriodicFallback, JabberID: jid, XMPPConfigured: true}, connReqPathXMPP},
		{"xmpp even without a URL", connReqInputs{JabberID: jid, XMPPConfigured: true}, connReqPathXMPP},
		{"xmpp before direct HTTP", connReqInputs{HasURL: true, Mode: devices.ModeDirectIPv4, JabberID: jid, XMPPConfigured: true}, connReqPathXMPP},
		{"no JID: unchanged HTTP", connReqInputs{HasURL: true, Mode: devices.ModeUnknown, XMPPConfigured: true}, connReqPathHTTP},
		{"xmpp unconfigured: JID ignored", connReqInputs{HasURL: true, Mode: devices.ModeUnknown, JabberID: jid}, connReqPathHTTP},
		{"behind NAT without STUN", connReqInputs{HasURL: true, Mode: devices.ModePeriodicFallback}, connReqPathBehindNAT},
		{"behind NAT with STUN goes HTTP then Annex G", connReqInputs{HasURL: true, Mode: devices.ModePeriodicFallback, UDPAddr: "1.2.3.4:5"}, connReqPathHTTP},
		{"xmpp-mode NAT'd device after XMPP failed", connReqInputs{HasURL: true, Mode: devices.ModeXMPP, BehindNAT: true}, connReqPathBehindNAT},
		{"xmpp-mode direct device after XMPP failed", connReqInputs{HasURL: true, Mode: devices.ModeXMPP}, connReqPathHTTP},
		{"no URL, STUN address", connReqInputs{UDPAddr: "1.2.3.4:5"}, connReqPathAnnexG},
		{"no URL, nothing", connReqInputs{}, connReqPathUnavailable},
	}
	for _, c := range cases {
		if got := selectConnReqPath(c.in); got != c.want {
			t.Errorf("%s: selectConnReqPath(%+v) = %s, want %s", c.name, c.in, got, c.want)
		}
	}
}

type fakeXMPP struct {
	mu        sync.Mutex
	connected bool
	err       error
	onSend    func()
	sent      []string // "to user"
}

func (f *fakeXMPP) Connected() bool { return f.connected }

func (f *fakeXMPP) ConnectionRequest(_ context.Context, to, username, _ string) error {
	f.mu.Lock()
	f.sent = append(f.sent, to+" "+username)
	f.mu.Unlock()
	if f.err == nil && f.onSend != nil {
		f.onSend()
	}
	return f.err
}

// newXMPPWorkerEnv is a DB-backed worker with a NAT'd FastMile-like device
// that has reported ConnReqJabberID.
func newXMPPWorkerEnv(t *testing.T, fake *fakeXMPP) (*testEnv, *connectionRequestWorker, string) {
	e := newTestEnv(t)
	id := e.device("XMPP-1", nil)
	if err := e.h.devices.UpdateConnectionRequestURL(e.ctx, id, "http://10.10.178.14:7547/"); err != nil {
		t.Fatal(err)
	}
	if err := e.h.devices.ClassifyConnectionRequestNAT(e.ctx, id, true); err != nil {
		t.Fatal(err)
	}
	if err := e.h.params.Upsert(e.ctx, id, map[string]parameters.CachedValue{
		"Device.ManagementServer.ConnReqJabberID": {Value: "xmpp-abc@xmpp.acs.lab/cwmp", UpdatedAt: time.Now().UTC(), Source: parameters.SourceGetValues},
	}); err != nil {
		t.Fatal(err)
	}
	w := &connectionRequestWorker{logger: e.h.logger, jobs: e.h.jobs, devices: e.h.devices, auditor: e.h.auditor, params: e.h.params, username: "cr-user", password: "cr-pass", xmpp: fake}
	return e, w, id
}

func (e *testEnv) connReqJob(deviceID string, timeout int) *jobs.Job {
	e.t.Helper()
	job, err := e.h.jobs.Create(e.ctx, deviceID, jobs.TypeConnectionRequest, jobs.ConnectionRequestPayload{TimeoutSeconds: timeout}, "test")
	if err != nil {
		e.t.Fatal(err)
	}
	return job
}

// TestConnReqWorkerXMPPSuccessSetsMode: the CPE acknowledges the iq and
// informs with event 6 -> SUCCESS, mode XMPP, outcome
// XMPP_SENT_INFORM_RECEIVED; no HTTP dial happened (the URL is private).
func TestConnReqWorkerXMPPSuccessSetsMode(t *testing.T) {
	fake := &fakeXMPP{connected: true}
	e, w, id := newXMPPWorkerEnv(t, fake)
	fake.onSend = func() {
		if _, err := e.db.ExecContext(e.ctx, `UPDATE devices SET last_inform_at = now() + interval '1 second', last_inform_event_codes = '{"6 CONNECTION REQUEST"}' WHERE id = $1`, id); err != nil {
			t.Error(err)
		}
	}
	job := e.connReqJob(id, 5)
	w.process(e.ctx, job)

	got, _ := e.h.jobs.ByID(e.ctx, job.ID)
	device, _ := e.h.devices.Get(e.ctx, id)
	if got.Status != jobs.StatusSuccess || device.ConnectionRequestMode != devices.ModeXMPP ||
		device.LastConnectionRequestStatus == nil || *device.LastConnectionRequestStatus != connreq.OutcomeXMPPInformReceived {
		t.Fatalf("job=%s mode=%s status=%v", got.Status, device.ConnectionRequestMode, device.LastConnectionRequestStatus)
	}
	if len(fake.sent) != 1 || fake.sent[0] != "xmpp-abc@xmpp.acs.lab/cwmp cr-user" {
		t.Fatalf("sent = %v", fake.sent)
	}
	// A later NAT judgement on Inform must not downgrade the XMPP mode.
	if err := e.h.devices.ClassifyConnectionRequestNAT(e.ctx, id, true); err != nil {
		t.Fatal(err)
	}
	if device, _ = e.h.devices.Get(e.ctx, id); device.ConnectionRequestMode != devices.ModeXMPP {
		t.Fatalf("mode after NAT classification = %s, want XMPP", device.ConnectionRequestMode)
	}
}

// TestConnReqWorkerXMPPErrorFallsBackToBehindNAT: an iq error (CPE
// offline) falls back to the pre-existing path, which for a NAT'd device
// without STUN is the immediate BEHIND_NAT skip — not a 10 s HTTP dial.
func TestConnReqWorkerXMPPErrorFallsBackToBehindNAT(t *testing.T) {
	fake := &fakeXMPP{connected: true, err: &xmpp.StanzaError{Type: "cancel", Condition: "service-unavailable"}}
	e, w, id := newXMPPWorkerEnv(t, fake)
	job := e.connReqJob(id, 5)
	start := time.Now()
	w.process(e.ctx, job)
	if time.Since(start) > 3*time.Second {
		t.Fatalf("fallback took %s — it should not have dialled HTTP", time.Since(start))
	}
	got, _ := e.h.jobs.ByID(e.ctx, job.ID)
	if got.Status != jobs.StatusFailed || got.FaultCode == nil || *got.FaultCode != connreq.OutcomeBehindNAT {
		t.Fatalf("job = %+v, want FAILED/BEHIND_NAT", got)
	}
	assertXMPPAudit(t, e, id, connreq.OutcomeXMPPError)
}

// TestConnReqWorkerXMPPUnavailableFallsBack: the ACS's own XMPP session
// is down — XMPP_UNAVAILABLE is audited, the CPE is not addressed.
func TestConnReqWorkerXMPPUnavailableFallsBack(t *testing.T) {
	fake := &fakeXMPP{connected: false}
	e, w, id := newXMPPWorkerEnv(t, fake)
	job := e.connReqJob(id, 5)
	w.process(e.ctx, job)
	if len(fake.sent) != 0 {
		t.Fatalf("sent while disconnected: %v", fake.sent)
	}
	got, _ := e.h.jobs.ByID(e.ctx, job.ID)
	if got.Status != jobs.StatusFailed || got.FaultCode == nil || *got.FaultCode != connreq.OutcomeBehindNAT {
		t.Fatalf("job = %+v, want FAILED/BEHIND_NAT", got)
	}
	assertXMPPAudit(t, e, id, connreq.OutcomeXMPPUnavailable)
}

// TestConnReqWorkerXMPPNoInformTimesOut: acknowledged but no session —
// TIMEOUT with XMPP_SENT_NO_INFORM, and the mode is not downgraded.
func TestConnReqWorkerXMPPNoInformTimesOut(t *testing.T) {
	fake := &fakeXMPP{connected: true}
	e, w, id := newXMPPWorkerEnv(t, fake)
	job := e.connReqJob(id, 1)
	w.process(e.ctx, job)
	got, _ := e.h.jobs.ByID(e.ctx, job.ID)
	device, _ := e.h.devices.Get(e.ctx, id)
	if got.Status != jobs.StatusTimeout || device.LastConnectionRequestStatus == nil || *device.LastConnectionRequestStatus != connreq.OutcomeXMPPNoInform {
		t.Fatalf("job=%s status=%v", got.Status, device.LastConnectionRequestStatus)
	}
	if device.ConnectionRequestMode != devices.ModePeriodicFallback {
		t.Fatalf("mode = %s, want it left at PERIODIC_FALLBACK_ONLY", device.ConnectionRequestMode)
	}
}

func assertXMPPAudit(t *testing.T, e *testEnv, deviceID, outcome string) {
	t.Helper()
	var detail []byte
	err := e.db.QueryRowContext(e.ctx, `SELECT details FROM audit_log WHERE device_id = $1 AND action = 'ConnectionRequest' AND details->>'transport' = 'XMPP' ORDER BY occurred_at LIMIT 1`, deviceID).Scan(&detail)
	if err != nil {
		t.Fatalf("no XMPP audit entry: %v", err)
	}
	var d map[string]any
	_ = json.Unmarshal(detail, &d)
	if d["outcome"] != outcome || d["fallback"] != true {
		t.Fatalf("audit detail = %v, want outcome %s with fallback", d, outcome)
	}
}
