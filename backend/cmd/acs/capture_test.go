package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"acs/internal/auth"
	"acs/internal/captures"
	"acs/internal/cwmp"
	"acs/internal/devices"
	"acs/internal/jobs"
	"acs/internal/observability"
	"acs/internal/parameters"
	"acs/internal/policy"
	"acs/internal/ratelimit"
	"acs/internal/sessions"
	"acs/internal/store"
	"acs/internal/templates"
)

// TestCaptureDispatchBodyRedactsKeyPassphrase is the one hard
// requirement Step 4 calls out explicitly: whatever captureDispatchBody
// implementation is used, a KeyPassphrase value must never appear in
// its output, and the parameter must come through as the fixed
// ***REDACTED*** marker. requestBody is built via a real call to
// cwmp.RenderSetParameterValues (rpc.go), not hand-written XML, so this
// test exercises captureDispatchBody against the exact wire shape the
// real dispatch path renders.
func TestCaptureDispatchBodyRedactsKeyPassphrase(t *testing.T) {
	const secret = "the-real-secret-value"
	requestBody := cwmp.RenderSetParameterValues("1", []cwmp.ParameterValueStruct{
		{Name: "Device.WiFi.AccessPoint.1.Security.KeyPassphrase", Value: secret},
		{Name: "Device.ManagementServer.PeriodicInformInterval", Value: "300"},
	}, "job-command-key")

	got := captureDispatchBody(requestBody)
	if got == nil {
		t.Fatal("captureDispatchBody returned nil")
	}
	if strings.Contains(*got, secret) {
		t.Fatalf("captureDispatchBody output leaked the KeyPassphrase secret: %s", *got)
	}
	if !strings.Contains(*got, "***REDACTED***") {
		t.Fatalf("captureDispatchBody output missing the redaction marker: %s", *got)
	}
	// The non-sensitive parameter must still be readable -- only secret
	// parameters are masked, everything else stays diagnostically useful.
	if !strings.Contains(*got, "300") {
		t.Errorf("captureDispatchBody redacted a non-sensitive value too: %s", *got)
	}
	if !strings.Contains(*got, "Device.WiFi.AccessPoint.1.Security.KeyPassphrase") {
		t.Errorf("captureDispatchBody must keep the parameter name visible, only mask its value: %s", *got)
	}
}

// TestCaptureDispatchBodyPassesThroughNonValueRPC confirms an RPC body
// with no <Name>/<Value> pairs (GetParameterValues here, standing in
// for Reboot/GetParameterNames/etc.) is returned unchanged -- there is
// nothing in it for captureDispatchBody to find or redact.
func TestCaptureDispatchBodyPassesThroughNonValueRPC(t *testing.T) {
	requestBody := cwmp.RenderGetParameterValues("1", []string{"Device.DeviceInfo.SoftwareVersion"})
	got := captureDispatchBody(requestBody)
	if got == nil || *got != string(requestBody) {
		t.Fatalf("captureDispatchBody changed a non-value-carrying RPC body:\n got:  %s\n want: %s", stringOrNil(got), requestBody)
	}
}

func stringOrNil(s *string) string {
	if s == nil {
		return "<nil>"
	}
	return *s
}

// newCaptureTestGateway builds a full cmd/acs handler wired with a real
// captures.Repository against a fresh schema on ACS_TEST_POSTGRES_DSN --
// session_integration_test.go's newTestGateway doesn't set the captures
// field (it predates this task), so capture-specific tests need their
// own copy of that harness rather than editing the shared one.
func newCaptureTestGateway(t *testing.T) (*handler, *httptest.Server, context.Context) {
	t.Helper()
	dsn := os.Getenv("ACS_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("ACS_TEST_POSTGRES_DSN not set; skipping DB-backed integration test")
	}
	ctx := context.Background()
	db, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.ExecContext(ctx, "DROP SCHEMA public CASCADE; CREATE SCHEMA public"); err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	h := &handler{
		logger:             slog.New(slog.NewTextHandler(io.Discard, nil)),
		auth:               auth.DigestAuthenticator{Username: cpeUser, Password: cpePass},
		devices:            devices.NewRepository(db),
		sessions:           sessions.NewRepository(db),
		jobs:               jobs.NewRepository(db),
		params:             parameters.NewRepository(db),
		auditor:            observability.NewAuditor(db),
		metrics:            observability.NewMetrics("acs-test"),
		policies:           policy.NewRepository(db),
		templates:          templates.NewRepository(db),
		captures:           captures.NewRepository(db),
		captureMaxDuration: 30 * time.Minute,
		ipLimiter:          ratelimit.New(100000, 100000, time.Minute),
		deviceLimiter:      ratelimit.New(100000, 100000, time.Minute),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", h.handleCWMP)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return h, srv, ctx
}

// TestIntegration_CaptureIdentityInformBackfillsDeviceID is the design
// §4 acceptance case: starting an 'identity'-mode capture for a
// not-yet-onboarded device's natural key, then driving a real Inform
// through handleInform for that identity, must produce exactly one
// capture_events row (kind="Inform") and backfill the session's
// device_id once the device actually authenticates.
func TestIntegration_CaptureIdentityInformBackfillsDeviceID(t *testing.T) {
	h, srv, ctx := newCaptureTestGateway(t)

	// The periodic Inform fixture (see session_integration_test.go's
	// periodicInform/fixture helpers) is Zyxel OUI=001349
	// ProductClass=NR5103 SerialNumber=S230Q12345678, which is not yet
	// registered as a device -- exactly the "not-yet-onboarded" case.
	naturalKey := cwmp.DeviceID{OUI: "001349", ProductClass: "NR5103", SerialNumber: "S230Q12345678"}.NaturalKey()

	session, err := h.captures.Start(ctx, captures.StartParams{
		MatchType:   captures.MatchIdentity,
		MatchValue:  naturalKey,
		Protocol:    "CWMP",
		StartedBy:   "test-operator",
		MaxDuration: h.captureMaxDuration,
	})
	if err != nil {
		t.Fatalf("Start capture: %v", err)
	}
	if session.DeviceID != nil {
		t.Fatalf("newly-started identity capture should have no device_id yet, got %v", *session.DeviceID)
	}

	cpe := &mockCPE{t: t, client: srv.Client(), url: srv.URL + "/cwmp"}
	code, body := cpe.post(periodicInform(t))
	if code != 200 || !strings.Contains(body, "InformResponse") {
		t.Fatalf("Inform -> %d %s", code, body)
	}

	device, err := h.devices.GetByOUIserial(ctx, naturalKey)
	if err != nil {
		t.Fatalf("device not onboarded from Inform: %v", err)
	}

	updated, err := h.captures.Get(ctx, session.ID)
	if err != nil {
		t.Fatalf("Get capture session: %v", err)
	}
	if updated.DeviceID == nil {
		t.Fatal("capture session device_id was not backfilled after the device's Inform")
	}
	if *updated.DeviceID != device.ID {
		t.Errorf("capture session device_id = %s, want %s", *updated.DeviceID, device.ID)
	}

	events, err := h.captures.ListEvents(ctx, session.ID)
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("capture_events for session = %d, want exactly 1: %+v", len(events), events)
	}
	if events[0].Kind != "Inform" {
		t.Errorf("event kind = %q, want %q", events[0].Kind, "Inform")
	}
	if events[0].Direction != "inbound" {
		t.Errorf("event direction = %q, want %q", events[0].Direction, "inbound")
	}
	if events[0].Body == nil || !strings.Contains(*events[0].Body, "Device.DeviceInfo.SoftwareVersion") {
		t.Errorf("event body missing expected Inform parameter content: %v", events[0].Body)
	}
}
