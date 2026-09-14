package main

import (
	"context"
	"encoding/base64"
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

	got := captureDispatchBody(jobs.TypeSetParameter, requestBody)
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
	got := captureDispatchBody(jobs.TypeGetParameter, requestBody)
	if got == nil || *got != string(requestBody) {
		t.Fatalf("captureDispatchBody changed a non-value-carrying RPC body:\n got:  %s\n want: %s", stringOrNil(got), requestBody)
	}
}

// TestCaptureDispatchBodyRedactsDownloadPassword is Finding 1's fix: a
// Download RPC's <Password> (internal/cwmp/download.go's RenderDownload)
// is a live file-server credential, not diagnostic data, and must never
// reach capture storage -- captureDispatchBody's original implementation
// only matched SetParameterValues' <Name>/<Value> shape and left this
// leaking verbatim.
func TestCaptureDispatchBodyRedactsDownloadPassword(t *testing.T) {
	const secret = "fw-secret"
	requestBody := cwmp.RenderDownload("1", "cmd-key", "1 Firmware Upgrade Image",
		"http://fileserver.test/fw.bin", "fw-user", secret, 1234, "fw.bin", 0)

	got := captureDispatchBody(jobs.TypeFirmwareDownload, requestBody)
	if got == nil {
		t.Fatal("captureDispatchBody returned nil")
	}
	if strings.Contains(*got, secret) {
		t.Fatalf("captureDispatchBody leaked the Download RPC's Password: %s", *got)
	}
	if !strings.Contains(*got, "***REDACTED***") {
		t.Fatalf("captureDispatchBody output missing the redaction marker: %s", *got)
	}
	if !strings.Contains(*got, "fw-user") {
		t.Errorf("captureDispatchBody should not touch Username, only Password: %s", *got)
	}
}

// TestCaptureDispatchBodyRedactsUploadPassword is Upload's mirror of
// TestCaptureDispatchBodyRedactsDownloadPassword (internal/cwmp/upload.go's
// RenderUpload emits the same <Username>/<Password> shape as Download).
func TestCaptureDispatchBodyRedactsUploadPassword(t *testing.T) {
	const secret = "upload-secret"
	requestBody := cwmp.RenderUpload("1", "cmd-key", "1 Vendor Configuration File",
		"http://fileserver.test/upload", "up-user", secret, 0)

	got := captureDispatchBody(jobs.TypeUpload, requestBody)
	if got == nil {
		t.Fatal("captureDispatchBody returned nil")
	}
	if strings.Contains(*got, secret) {
		t.Fatalf("captureDispatchBody leaked the Upload RPC's Password: %s", *got)
	}
	if !strings.Contains(*got, "***REDACTED***") {
		t.Fatalf("captureDispatchBody output missing the redaction marker: %s", *got)
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

// TestIntegration_CaptureInboundRedactsInformSecrets is Finding 5's
// coverage gap: nothing previously tested that the inbound hook
// (captureInbound -> captureInformBody) actually calls
// captures.RedactParamValue/RedactAuthHeader on a real Inform driven
// through handleInform -- only the redaction helpers themselves (Task 2)
// and the outbound dispatch body-builder (this task) were tested. This
// seeds an Inform whose ParameterList carries a KeyPassphrase-named
// parameter and whose Authorization header is a Basic credential, drives
// it through handleInform with an active capture session watching, and
// asserts the resulting capture_events row's body redacts both: the
// parameter's real value never appears (replaced by ***REDACTED***), and
// neither the raw Basic credential string nor its base64 form appears
// (RedactAuthHeader keeps only the username).
func TestIntegration_CaptureInboundRedactsInformSecrets(t *testing.T) {
	h, _, ctx := newCaptureTestGateway(t)

	naturalKey := cwmp.DeviceID{OUI: "001349", ProductClass: "NR5103", SerialNumber: "S230Q99999999"}.NaturalKey()
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

	const wifiSecret = "wifi-passphrase-xyz"
	const basicUser, basicPass = "someuser", "basic-credential-secret"
	basicHeader := "Basic " + base64.StdEncoding.EncodeToString([]byte(basicUser+":"+basicPass))

	inform := &cwmp.Inform{
		DeviceId: cwmp.DeviceID{OUI: "001349", ProductClass: "NR5103", SerialNumber: "S230Q99999999"},
		Event:    []cwmp.EventStruct{{EventCode: "2 PERIODIC"}},
		ParameterList: []cwmp.ParameterValueStruct{
			{Name: "Device.WiFi.AccessPoint.1.Security.KeyPassphrase", Value: wifiSecret},
			{Name: "Device.DeviceInfo.SoftwareVersion", Value: "2.3.1"},
		},
	}

	r := httptest.NewRequest(http.MethodPost, "/cwmp", nil)
	r.Header.Set("Authorization", basicHeader)
	w := httptest.NewRecorder()
	h.handleInform(ctx, w, r, inform, devices.AuthModeNone, inboundIdentity{}, "test-id", cwmp.DefaultCWMPNamespace)
	if w.Code != http.StatusOK {
		t.Fatalf("handleInform -> %d %s", w.Code, w.Body.String())
	}

	if _, err := h.devices.GetByOUIserial(ctx, naturalKey); err != nil {
		t.Fatalf("device not onboarded from Inform: %v", err)
	}

	events, err := h.captures.ListEvents(ctx, session.ID)
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(events) != 1 || events[0].Body == nil {
		t.Fatalf("capture_events for session = %+v, want exactly 1 event with a body", events)
	}
	body := *events[0].Body

	if strings.Contains(body, wifiSecret) {
		t.Fatalf("captured Inform body leaked the KeyPassphrase value: %s", body)
	}
	if !strings.Contains(body, "***REDACTED***") {
		t.Fatalf("captured Inform body missing the redaction marker for KeyPassphrase: %s", body)
	}
	if strings.Contains(body, basicPass) {
		t.Fatalf("captured Inform body leaked the raw Basic auth password: %s", body)
	}
	if strings.Contains(body, base64.StdEncoding.EncodeToString([]byte(basicUser+":"+basicPass))) {
		t.Fatalf("captured Inform body leaked the base64-encoded Basic credential: %s", body)
	}
	if !strings.Contains(body, "username="+basicUser) {
		t.Errorf("captured Inform body should keep the Basic username visible: %s", body)
	}
	if !strings.Contains(body, "Device.DeviceInfo.SoftwareVersion") {
		t.Errorf("captured Inform body should keep non-sensitive parameters visible: %s", body)
	}
}
