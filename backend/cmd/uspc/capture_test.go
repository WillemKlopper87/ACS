package main

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"acs/internal/captures"
	"acs/internal/devices"
	"acs/internal/jobs"
	"acs/internal/observability"
	"acs/internal/parameters"
	"acs/internal/store"
	"acs/internal/subscriptions"
	"acs/internal/usp/mtp"
)

// newCaptureTestHandler builds a real-DB-backed handler+dispatcher pair
// wired with a real captures.Repository (design S5), mirroring
// handler_test.go's newNotifyTestHandler and cmd/acs/capture_test.go's
// newCaptureTestGateway -- neither existing helper wires captures
// (newNotifyTestHandler predates this task; newCaptureTestGateway is
// cmd/acs's own copy for the CWMP side), so this task's own tests need
// their own copy, same convention newCaptureTestGateway itself already
// establishes. Returns the handler and one pre-registered device's id
// and natural key (oui_serial) -- dispatcher.captureOutbound's own
// ActiveMatch lookup needs the latter, not the device id itself.
func newCaptureTestHandler(t *testing.T) (h *handler, deviceID, ouiSerial string) {
	t.Helper()
	dsn := os.Getenv("ACS_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("ACS_TEST_POSTGRES_DSN not set — skipping DB-backed integration test")
	}
	ctx := context.Background()
	db, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.ExecContext(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public`); err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(ctx, db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	devRepo := devices.NewRepository(db)
	dev, err := devRepo.PreRegister(ctx, "CAPTURE-TEST-01", "TestVendor", "001349", "NR7101", "SER1", nil, nil)
	if err != nil {
		t.Fatalf("pre-register device: %v", err)
	}

	capturesRepo := captures.NewRepository(db)
	registry := mtp.NewRegistry()
	disp := newDispatcher(jobs.NewRepository(db), devRepo, registry, ctrl, slog.Default())
	disp.captures = capturesRepo
	disp.devices = devRepo

	h = &handler{
		log:           slog.Default(),
		registry:      registry,
		probe:         newProbe(ctrl, slog.Default()),
		controllerID:  ctrl,
		metrics:       newUSPMetrics(observability.NewMetrics("uspc-test")),
		reconciler:    newReconciler(devRepo, slog.Default()),
		dispatcher:    disp,
		subscriptions: newSubscriptionReconciler(subscriptions.NewRepository(db), ctrl, slog.Default()),
		paramsRepo:    parameters.NewRepository(db),
		devicesRepo:   devRepo,
		captures:      capturesRepo,
	}
	return h, dev.ID, dev.OUISerial
}

// TestCaptureInboundRemoteIPRecordsEventAndBackfillsDeviceID drives a
// real (mock) USP message through OnRecord for an already-reconciled
// connection with a 'remote_ip'-mode capture session watching, and
// asserts exactly one capture_events row is recorded with the expected
// kind/summary, and that the session's device_id is backfilled by the
// same call (captureInbound's ResolveDeviceID branch).
//
// 'remote_ip' mode matches on c's own remote address (port-stripped via
// the remoteIP helper -- captureConn.RemoteAddr() returns a bare literal
// with no port, so remoteIP's no-op fallback applies here, but the
// stripping matters for a real wsConn/mqttConn whose RemoteAddr() is
// host:port). See TestCaptureInboundDeviceModeRecordsEvent below for the
// 'device'-mode counterpart, which matches on the reconciled connection's
// own natural key instead (set by resolveAndMarkReconciled in
// production, fix round 1).
func TestCaptureInboundRemoteIPRecordsEventAndBackfillsDeviceID(t *testing.T) {
	h, deviceID, _ := newCaptureTestHandler(t)
	ctx := context.Background()

	c := &captureConn{id: agent}
	session, err := h.captures.Start(ctx, captures.StartParams{
		MatchType:   captures.MatchRemoteIP,
		MatchValue:  remoteIP(c),
		Protocol:    "USP",
		StartedBy:   "test-operator",
		MaxDuration: 30 * time.Minute,
	})
	if err != nil {
		t.Fatalf("Start capture: %v", err)
	}
	if session.DeviceID != nil {
		t.Fatalf("newly-started remote_ip capture should have no device_id yet, got %v", *session.DeviceID)
	}

	// c is reconciled to deviceID as if an earlier OnBoardRequest had
	// already run -- same shortcut TestHandlerOperationCompleteSendsResp
	// (handler_test.go) uses, since reconciliation itself is exercised
	// elsewhere. Deliberately does NOT call h.setNaturalKey here (unlike
	// TestCaptureInboundDeviceModeRecordsEvent) -- this test's whole point
	// is that remote_ip-mode matching works via remote address alone, with
	// no natural key involved at all.
	h.markReconciled(c, deviceID)

	msg := valueChangeMsg("sub-cap-1", false, "Device.DeviceInfo.SoftwareVersion", "2.3.1")
	h.OnRecord(mtp.Inbound{Conn: c, Record: recordWire(t, agent, ctrl, msg)})

	updated, err := h.captures.Get(ctx, session.ID)
	if err != nil {
		t.Fatalf("Get capture session: %v", err)
	}
	if updated.DeviceID == nil {
		t.Fatal("capture session device_id was not backfilled by captureInbound")
	}
	if *updated.DeviceID != deviceID {
		t.Errorf("capture session device_id = %s, want %s", *updated.DeviceID, deviceID)
	}

	events, err := h.captures.ListEvents(ctx, session.ID)
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("capture_events for session = %d, want exactly 1: %+v", len(events), events)
	}
	ev := events[0]
	if ev.Kind != "USP message" {
		t.Errorf("event kind = %q, want %q", ev.Kind, "USP message")
	}
	if ev.Direction != "inbound" {
		t.Errorf("event direction = %q, want %q", ev.Direction, "inbound")
	}
	if !strings.Contains(ev.Summary, "NOTIFY") {
		t.Errorf("event summary = %q, want it to mention the NOTIFY msg type", ev.Summary)
	}
	if !strings.Contains(ev.Summary, "msg_id=") {
		t.Errorf("event summary = %q, want it to carry the msg_id", ev.Summary)
	}
	if ev.Body != nil {
		t.Errorf("event body = %v, want nil: per-message-type inbound body redaction is out of this task's first-cut scope", ev.Body)
	}
}

// TestCaptureInboundDeviceModeRecordsEvent is the brief's own Step 5
// acceptance case: a 'device'-mode capture session started for an
// already-known device, driving a real (mock) USP message through
// OnRecord, must produce exactly one capture_events row with the
// expected kind/summary.
//
// 'device'-mode matching needs a real natural key (ActiveMatch only
// matches 'device'/'identity' sessions against a non-empty one), which
// in production is resolved and recorded once per reconciliation by
// resolveAndMarkReconciled (handler.go, fix round 1) via
// h.setNaturalKey, not by markReconciled itself (a separate call, kept
// deliberately independent of markReconciled's own signature/call
// sites). This test bypasses resolveAndMarkReconciled the same way the
// rest of this package's tests bypass it for markReconciled alone
// (e.g. TestHandlerOperationCompleteSendsResp), so it calls both
// h.markReconciled and h.setNaturalKey directly to simulate what a real
// reconciliation would have already done.
func TestCaptureInboundDeviceModeRecordsEvent(t *testing.T) {
	h, deviceID, ouiSerial := newCaptureTestHandler(t)
	ctx := context.Background()

	session, err := h.captures.Start(ctx, captures.StartParams{
		DeviceID:    &deviceID,
		MatchType:   captures.MatchDevice,
		MatchValue:  ouiSerial,
		Protocol:    "USP",
		StartedBy:   "test-operator",
		MaxDuration: 30 * time.Minute,
	})
	if err != nil {
		t.Fatalf("Start capture: %v", err)
	}

	c := &captureConn{id: agent}
	h.markReconciled(c, deviceID)
	h.setNaturalKey(c, ouiSerial)

	msg := valueChangeMsg("sub-cap-2", false, "Device.DeviceInfo.SoftwareVersion", "2.3.1")
	h.OnRecord(mtp.Inbound{Conn: c, Record: recordWire(t, agent, ctrl, msg)})

	events, err := h.captures.ListEvents(ctx, session.ID)
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("capture_events for session = %d, want exactly 1: %+v", len(events), events)
	}
	ev := events[0]
	if ev.Kind != "USP message" {
		t.Errorf("event kind = %q, want %q", ev.Kind, "USP message")
	}
	if ev.Direction != "inbound" {
		t.Errorf("event direction = %q, want %q", ev.Direction, "inbound")
	}
	if !strings.Contains(ev.Summary, "NOTIFY") {
		t.Errorf("event summary = %q, want it to mention the NOTIFY msg type", ev.Summary)
	}
	if !strings.Contains(ev.Summary, "msg_id=") {
		t.Errorf("event summary = %q, want it to carry the msg_id", ev.Summary)
	}
}

// TestCaptureOutboundRecordsEvent drives dispatcher.captureOutbound
// directly (tryDispatch's own full lease/send path is already covered by
// dispatcher_test.go and isn't this task's concern) for a device-mode
// capture session watching the job's device, and asserts exactly one
// capture_events row is recorded with kind = job.Type and a summary
// mentioning the job's command key -- task-4 brief Step 4's own
// acceptance shape. No jobs.Repository row is created: captureOutbound
// itself never touches jobsRepo, only d.captures/d.devices, so a
// hand-built *jobs.Job is enough.
func TestCaptureOutboundRecordsEvent(t *testing.T) {
	h, deviceID, ouiSerial := newCaptureTestHandler(t)
	ctx := context.Background()

	session, err := h.captures.Start(ctx, captures.StartParams{
		DeviceID:    &deviceID,
		MatchType:   captures.MatchDevice,
		MatchValue:  ouiSerial,
		Protocol:    "USP",
		StartedBy:   "test-operator",
		MaxDuration: 30 * time.Minute,
	})
	if err != nil {
		t.Fatalf("Start capture: %v", err)
	}

	job := &jobs.Job{ID: "job-outbound-1", Type: jobs.TypeSetParameter, CommandKey: "ck-outbound-1", DeviceID: deviceID}
	h.dispatcher.captureOutbound(ctx, deviceID, job)

	events, err := h.captures.ListEvents(ctx, session.ID)
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("capture_events for session = %d, want exactly 1: %+v", len(events), events)
	}
	ev := events[0]
	if ev.Kind != jobs.TypeSetParameter {
		t.Errorf("event kind = %q, want %q", ev.Kind, jobs.TypeSetParameter)
	}
	if ev.Direction != "outbound" {
		t.Errorf("event direction = %q, want %q", ev.Direction, "outbound")
	}
	if !strings.Contains(ev.Summary, "ck-outbound-1") {
		t.Errorf("event summary = %q, want it to mention the job's command key", ev.Summary)
	}
	if ev.Body != nil {
		t.Errorf("event body = %v, want nil: outbound dispatch capture is summary-only in this task's first cut (see this task's report)", ev.Body)
	}
}

// TestCaptureOutboundNoActiveSessionNoOp confirms captureOutbound is a
// quiet no-op (no error, no event) when no capture session is watching
// this device -- the common-case path for every ordinary dispatch.
func TestCaptureOutboundNoActiveSessionNoOp(t *testing.T) {
	h, deviceID, _ := newCaptureTestHandler(t)
	ctx := context.Background()

	job := &jobs.Job{ID: "job-outbound-2", Type: jobs.TypeReboot, CommandKey: "ck-outbound-2", DeviceID: deviceID}
	h.dispatcher.captureOutbound(ctx, deviceID, job)

	sessions, err := h.captures.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(sessions) != 0 {
		t.Fatalf("expected no capture sessions to exist, got %+v", sessions)
	}
}
