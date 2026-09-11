package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"strings"
	"testing"

	"acs/internal/devices"
	"acs/internal/jobs"
	"acs/internal/store"
	"acs/internal/usp"
	"acs/internal/usp/mtp"
	"acs/internal/usp/uspproto"
)

// newDispatcherTestDB opens a real Postgres connection (gated on
// ACS_TEST_POSTGRES_DSN, matching every other DB-backed suite in this
// codebase -- see internal/jobs/lease_test.go), migrates a clean schema,
// and pre-registers one real devices row so jobs created against its id
// satisfy jobs.device_id's foreign key. dispatcher.go's own devicesRepo
// dependency (identityStore) is deliberately NOT this real
// *devices.Repository in the tests below -- a fakeIdentityStore stands
// in for it, matching the rest of cmd/uspc's own test suite -- only
// jobsRepo needs the real thing, since internal/jobs.Repository is a
// concrete type wrapping *sql.DB with no interface seam to fake (per the
// task brief: follow the codebase's existing convention rather than
// inventing one internal/jobs doesn't have).
func newDispatcherTestDB(t *testing.T) (*jobs.Repository, string) {
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
	dev, err := devRepo.PreRegister(ctx, "DISPATCH-TEST-01", "TestVendor", "001349", "NR7101", "SER1", nil, nil)
	if err != nil {
		t.Fatalf("pre-register device: %v", err)
	}
	return jobs.NewRepository(db), dev.ID
}

// registerAgent seeds store's in-memory agentsByEndpointID so
// dispatcher.tryDispatch's device_id -> endpoint_id lookup succeeds, and
// registers conn in registry so the endpoint_id -> live mtp.Conn lookup
// also succeeds -- the two-hop resolution tryDispatch performs before it
// ever looks at jobsRepo.
func registerAgent(store *fakeIdentityStore, registry *mtp.Registry, deviceID string, conn mtp.Conn) {
	store.agentsByEndpointID[string(conn.Endpoint())] = &devices.UspAgent{
		DeviceID:   deviceID,
		EndpointID: string(conn.Endpoint()),
		MTPKind:    string(conn.Kind()),
		Connected:  true,
	}
	registry.Add(conn)
}

func TestTryDispatchNoLiveConnection(t *testing.T) {
	jobsRepo, deviceID := newDispatcherTestDB(t)
	ctx := context.Background()
	if _, err := jobsRepo.Create(ctx, deviceID, jobs.TypeGetParameter,
		jobs.GetParameterPayload{Paths: []string{"Device.DeviceInfo."}}, "test"); err != nil {
		t.Fatalf("create job: %v", err)
	}

	store := newFakeIdentityStore() // no agent registered for deviceID
	registry := mtp.NewRegistry()
	d := newDispatcher(jobsRepo, store, registry, ctrl, slog.Default())

	if err := d.tryDispatch(ctx, deviceID); err != nil {
		t.Fatalf("tryDispatch() = %v, want nil (no live connection is a no-op, not an error)", err)
	}
	if len(d.pending) != 0 {
		t.Errorf("pending = %+v, want none", d.pending)
	}
}

func TestTryDispatchNoJob(t *testing.T) {
	jobsRepo, deviceID := newDispatcherTestDB(t)
	ctx := context.Background()

	store := newFakeIdentityStore()
	registry := mtp.NewRegistry()
	c := &captureConn{id: agent}
	registerAgent(store, registry, deviceID, c)
	d := newDispatcher(jobsRepo, store, registry, ctrl, slog.Default())

	if err := d.tryDispatch(ctx, deviceID); err != nil {
		t.Fatalf("tryDispatch() = %v, want nil (no leasable job is a no-op)", err)
	}
	if len(c.sent) != 0 {
		t.Errorf("c.sent = %d records, want 0", len(c.sent))
	}
	if len(d.pending) != 0 {
		t.Errorf("pending = %+v, want none", d.pending)
	}
}

func TestTryDispatchSendsAndTracks(t *testing.T) {
	jobsRepo, deviceID := newDispatcherTestDB(t)
	ctx := context.Background()
	job, err := jobsRepo.Create(ctx, deviceID, jobs.TypeGetParameter,
		jobs.GetParameterPayload{Paths: []string{"Device.DeviceInfo."}}, "test")
	if err != nil {
		t.Fatalf("create job: %v", err)
	}

	store := newFakeIdentityStore()
	registry := mtp.NewRegistry()
	c := &captureConn{id: agent}
	registerAgent(store, registry, deviceID, c)
	d := newDispatcher(jobsRepo, store, registry, ctrl, slog.Default())

	if err := d.tryDispatch(ctx, deviceID); err != nil {
		t.Fatalf("tryDispatch() = %v, want nil", err)
	}
	if len(c.sent) != 1 {
		t.Fatalf("c.sent = %d records, want exactly 1", len(c.sent))
	}

	rec, err := usp.DecodeRecord(c.sent[0], agent)
	if err != nil {
		t.Fatalf("decode dispatched record: %v", err)
	}
	if rec.From != ctrl {
		t.Errorf("record From = %q, want %q", rec.From, ctrl)
	}
	msg, err := usp.DecodeMsg(rec.Payload)
	if err != nil {
		t.Fatalf("decode dispatched msg: %v", err)
	}
	get := msg.GetBody().GetRequest().GetGet()
	if get == nil {
		t.Fatal("dispatched message is not a Get")
	}
	if paths := get.GetParamPaths(); len(paths) != 1 || paths[0] != "Device.DeviceInfo." {
		t.Errorf("Get param_paths = %v, want [Device.DeviceInfo.]", paths)
	}

	msgID := msg.GetHeader().GetMsgId()
	d.mu.Lock()
	entry, ok := d.pending[msgID]
	d.mu.Unlock()
	if !ok {
		t.Fatalf("pending = %+v, want an entry keyed by %q", d.pending, msgID)
	}
	if entry.job.ID != job.ID {
		t.Errorf("pending entry job id = %q, want %q", entry.job.ID, job.ID)
	}
	if entry.conn != mtp.Conn(c) {
		t.Errorf("pending entry conn = %v, want %v", entry.conn, c)
	}

	gotJob, err := jobsRepo.ByID(ctx, job.ID)
	if err != nil {
		t.Fatalf("ByID: %v", err)
	}
	if gotJob.Status != jobs.StatusRPCSent {
		t.Errorf("job status = %s, want RPC_SENT", gotJob.Status)
	}
}

// TestTryDispatchNeverLeasesFirmwareDownload covers uspDispatchableTypes'
// deliberate exclusion of FIRMWARE_DOWNLOAD (fix round 1, Important 2):
// buildUSPRequest can never render it over USP today (see its own case's
// doc comment in dispatch.go), so leasing for it would only ever repeat
// a lease-then-requeue cycle forever, starving every genuinely
// dispatchable job queued behind it -- see uspDispatchableTypes' own doc
// comment for the full rationale. This confirms the exclusion actually
// holds: a FIRMWARE_DOWNLOAD job is never leased, so it can never even
// reach buildUSPRequest in the first place, and is left untouched
// (QUEUED, zero attempts) rather than cycling.
func TestTryDispatchNeverLeasesFirmwareDownload(t *testing.T) {
	jobsRepo, deviceID := newDispatcherTestDB(t)
	ctx := context.Background()
	job, err := jobsRepo.Create(ctx, deviceID, jobs.TypeFirmwareDownload,
		jobs.FirmwareDownloadPayload{FirmwareImageID: "fw-1", URL: "https://example.test/fw.bin", FileSize: 100}, "test")
	if err != nil {
		t.Fatalf("create job: %v", err)
	}

	store := newFakeIdentityStore()
	registry := mtp.NewRegistry()
	c := &captureConn{id: agent}
	registerAgent(store, registry, deviceID, c)
	d := newDispatcher(jobsRepo, store, registry, ctrl, slog.Default())

	if err := d.tryDispatch(ctx, deviceID); err != nil {
		t.Fatalf("tryDispatch() = %v, want nil", err)
	}
	if len(c.sent) != 0 {
		t.Errorf("c.sent = %d records, want 0: FIRMWARE_DOWNLOAD must never be leased, let alone sent", len(c.sent))
	}
	if len(d.pending) != 0 {
		t.Errorf("pending = %+v, want none", d.pending)
	}

	gotJob, err := jobsRepo.ByID(ctx, job.ID)
	if err != nil {
		t.Fatalf("ByID: %v", err)
	}
	if gotJob.Status != jobs.StatusQueued {
		t.Errorf("job status = %s, want QUEUED (never leased, so never touched)", gotJob.Status)
	}
	if gotJob.Attempts != 0 {
		t.Errorf("job attempts = %d, want 0 (never leased)", gotJob.Attempts)
	}
}

// TestTryDispatchRequeuesUnsupportedType covers tryDispatch's
// ErrUnsupportedOverUSP handling as the defensive guard it now is (fix
// round 1, Important 2): with FIRMWARE_DOWNLOAD removed from
// uspDispatchableTypes, no currently-leasable type can actually produce
// ErrUnsupportedOverUSP from buildUSPRequest, so this test exercises the
// guard the way a future accidental edit to uspDispatchableTypes would --
// by temporarily reinstating FIRMWARE_DOWNLOAD onto the leasable list for
// the duration of this test only, restored via t.Cleanup, and confirming
// the lease is still cleanly requeued rather than left stranded RPC_SENT.
func TestTryDispatchRequeuesUnsupportedType(t *testing.T) {
	origTypes := uspDispatchableTypes
	uspDispatchableTypes = append(append([]string{}, origTypes...), jobs.TypeFirmwareDownload)
	t.Cleanup(func() { uspDispatchableTypes = origTypes })

	jobsRepo, deviceID := newDispatcherTestDB(t)
	ctx := context.Background()
	job, err := jobsRepo.Create(ctx, deviceID, jobs.TypeFirmwareDownload,
		jobs.FirmwareDownloadPayload{FirmwareImageID: "fw-1", URL: "https://example.test/fw.bin", FileSize: 100}, "test")
	if err != nil {
		t.Fatalf("create job: %v", err)
	}

	store := newFakeIdentityStore()
	registry := mtp.NewRegistry()
	c := &captureConn{id: agent}
	registerAgent(store, registry, deviceID, c)
	d := newDispatcher(jobsRepo, store, registry, ctrl, slog.Default())

	if err := d.tryDispatch(ctx, deviceID); err != nil {
		t.Fatalf("tryDispatch() = %v, want nil", err)
	}
	if len(c.sent) != 0 {
		t.Errorf("c.sent = %d records, want 0: an unsupported job type must never be sent", len(c.sent))
	}
	if len(d.pending) != 0 {
		t.Errorf("pending = %+v, want none", d.pending)
	}

	gotJob, err := jobsRepo.ByID(ctx, job.ID)
	if err != nil {
		t.Fatalf("ByID: %v", err)
	}
	if gotJob.Status != jobs.StatusQueued {
		t.Errorf("job status = %s, want QUEUED (requeued after an unsupported-type dispatch attempt, not stranded RPC_SENT)", gotJob.Status)
	}
}

// dispatchTestSetup builds a dispatcher with one leased-and-sent job
// outstanding, returning the pieces TestHandleResponse* need to build and
// deliver a matching response. An optional logger may be passed (used by
// the error-mapping tests that assert on log content); it defaults to
// slog.Default() when omitted.
func dispatchTestSetup(t *testing.T, log ...*slog.Logger) (d *dispatcher, jobsRepo *jobs.Repository, jobID string, msgID string, c *captureConn) {
	t.Helper()
	l := slog.Default()
	if len(log) > 0 {
		l = log[0]
	}
	jobsRepo, deviceID := newDispatcherTestDB(t)
	ctx := context.Background()
	job, err := jobsRepo.Create(ctx, deviceID, jobs.TypeGetParameter,
		jobs.GetParameterPayload{Paths: []string{"Device.DeviceInfo."}}, "test")
	if err != nil {
		t.Fatalf("create job: %v", err)
	}

	store := newFakeIdentityStore()
	registry := mtp.NewRegistry()
	c = &captureConn{id: agent}
	registerAgent(store, registry, deviceID, c)
	d = newDispatcher(jobsRepo, store, registry, ctrl, l)

	if err := d.tryDispatch(ctx, deviceID); err != nil {
		t.Fatalf("tryDispatch() = %v, want nil", err)
	}
	if len(c.sent) != 1 {
		t.Fatalf("c.sent = %d records, want exactly 1", len(c.sent))
	}
	rec, err := usp.DecodeRecord(c.sent[0], agent)
	if err != nil {
		t.Fatalf("decode dispatched record: %v", err)
	}
	sentMsg, err := usp.DecodeMsg(rec.Payload)
	if err != nil {
		t.Fatalf("decode dispatched msg: %v", err)
	}
	return d, jobsRepo, job.ID, sentMsg.GetHeader().GetMsgId(), c
}

func TestHandleResponseResolvesSuccess(t *testing.T) {
	d, jobsRepo, jobID, msgID, _ := dispatchTestSetup(t)

	resp := getResp(msgID, map[string]string{"SoftwareVersion": "11.0.7"})
	if matched := d.handleResponse(agent, resp); !matched {
		t.Fatal("handleResponse() = false, want true for the dispatched job's own msg_id")
	}
	if len(d.pending) != 0 {
		t.Errorf("pending = %+v, want none after a matched response", d.pending)
	}

	gotJob, err := jobsRepo.ByID(context.Background(), jobID)
	if err != nil {
		t.Fatalf("ByID: %v", err)
	}
	if gotJob.Status != jobs.StatusSuccess {
		t.Errorf("job status = %s, want SUCCESS", gotJob.Status)
	}
	if len(gotJob.ResultDetail) == 0 {
		t.Error("job result_detail is empty, want a JSON summary of the response")
	}
}

func TestHandleResponseResolvesFailure(t *testing.T) {
	d, jobsRepo, jobID, msgID, _ := dispatchTestSetup(t)

	errMsg := &uspproto.Msg{
		Header: &uspproto.Header{MsgId: msgID, MsgType: uspproto.Header_ERROR},
		Body:   &uspproto.Body{MsgBody: &uspproto.Body_Error{Error: &uspproto.Error{ErrCode: 7012, ErrMsg: "invalid path"}}},
	}
	if matched := d.handleResponse(agent, errMsg); !matched {
		t.Fatal("handleResponse() = false, want true for the dispatched job's own msg_id")
	}
	if len(d.pending) != 0 {
		t.Errorf("pending = %+v, want none after a matched response", d.pending)
	}

	gotJob, err := jobsRepo.ByID(context.Background(), jobID)
	if err != nil {
		t.Fatalf("ByID: %v", err)
	}
	if gotJob.Status != jobs.StatusFailed {
		t.Errorf("job status = %s, want FAILED", gotJob.Status)
	}
	if gotJob.FaultCode == nil || *gotJob.FaultCode != "7012" {
		t.Errorf("job fault_code = %v, want 7012", gotJob.FaultCode)
	}
	if gotJob.FaultString == nil || *gotJob.FaultString != "invalid path" {
		t.Errorf("job fault_string = %v, want %q", gotJob.FaultString, "invalid path")
	}
}

// uspErrorMsg builds an ERROR-typed uspproto.Msg answering msgID, mirroring
// the literal shape TestHandleResponseResolvesFailure already used inline --
// factored out here since the error-mapping tests below all need the same
// shape with different codes/messages.
func uspErrorMsg(msgID string, code uint32, msg string) *uspproto.Msg {
	return &uspproto.Msg{
		Header: &uspproto.Header{MsgId: msgID, MsgType: uspproto.Header_ERROR},
		Body:   &uspproto.Body{MsgBody: &uspproto.Body_Error{Error: &uspproto.Error{ErrCode: code, ErrMsg: msg}}},
	}
}

// TestErrorMappingNotWriteable covers design §6.4's first special case
// (USP 7013): the Huawei-class trap that resolves, sends and silently
// fails on CWMP must instead fail typed and diagnosable -- the job's
// FaultString gets a class-identifying prefix, not just the agent's raw
// message.
func TestErrorMappingNotWriteable(t *testing.T) {
	d, jobsRepo, jobID, msgID, _ := dispatchTestSetup(t)

	if matched := d.handleResponse(agent, uspErrorMsg(msgID, 7013, "Device.WiFi.SSID is read-only")); !matched {
		t.Fatal("handleResponse() = false, want true")
	}

	gotJob, err := jobsRepo.ByID(context.Background(), jobID)
	if err != nil {
		t.Fatalf("ByID: %v", err)
	}
	if gotJob.Status != jobs.StatusFailed {
		t.Errorf("job status = %s, want FAILED", gotJob.Status)
	}
	if gotJob.FaultCode == nil || *gotJob.FaultCode != "7013" {
		t.Errorf("job fault_code = %v, want 7013", gotJob.FaultCode)
	}
	wantFaultString := "parameter is not writeable: Device.WiFi.SSID is read-only"
	if gotJob.FaultString == nil || *gotJob.FaultString != wantFaultString {
		t.Errorf("job fault_string = %v, want %q", gotJob.FaultString, wantFaultString)
	}
}

// TestErrorMappingObjectDoesNotExist covers design §6.4's second special
// case (USP 7016). The brief's binding decision: log distinctly so an
// operator/future automation can act on it, but do NOT auto-queue a
// rediscovery job -- auto-queuing from inside error handling risks a
// retry storm against a persistently stale data model. This test asserts
// both halves: a distinct log line, and that no second job was created
// for the device.
func TestErrorMappingObjectDoesNotExist(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	d, jobsRepo, jobID, msgID, _ := dispatchTestSetup(t, log)

	if matched := d.handleResponse(agent, uspErrorMsg(msgID, 7016, "Device.WiFi.AccessPoint.5. does not exist")); !matched {
		t.Fatal("handleResponse() = false, want true")
	}

	gotJob, err := jobsRepo.ByID(context.Background(), jobID)
	if err != nil {
		t.Fatalf("ByID: %v", err)
	}
	if gotJob.Status != jobs.StatusFailed {
		t.Errorf("job status = %s, want FAILED", gotJob.Status)
	}
	if gotJob.FaultCode == nil || *gotJob.FaultCode != "7016" {
		t.Errorf("job fault_code = %v, want 7016", gotJob.FaultCode)
	}

	logged := buf.String()
	if !strings.Contains(logged, "does not exist") {
		t.Errorf("logged output should distinctly call out the object-does-not-exist case, got: %s", logged)
	}
	if !strings.Contains(strings.ToLower(logged), "discovery") && !strings.Contains(strings.ToLower(logged), "stale") {
		t.Errorf("logged output should be actionable for an operator/future rediscovery automation (mention discovery/stale data model), got: %s", logged)
	}

	gotJobs, err := jobsRepo.List(context.Background(), gotJob.DeviceID, nil, false)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(gotJobs) != 1 {
		t.Errorf("jobs for device = %d, want exactly 1 (the original job only -- no auto-queued rediscovery job)", len(gotJobs))
	}
}

// TestErrorMappingPermissionDenied covers design §6.4's third special case
// (USP 7006): flagged as a controller-trust misconfiguration, not a
// device fault, and logged at Warn since it signals an operational
// problem with the deployment.
func TestErrorMappingPermissionDenied(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	d, jobsRepo, jobID, msgID, _ := dispatchTestSetup(t, log)

	if matched := d.handleResponse(agent, uspErrorMsg(msgID, 7006, "controller endpoint not in ACL")); !matched {
		t.Fatal("handleResponse() = false, want true")
	}

	gotJob, err := jobsRepo.ByID(context.Background(), jobID)
	if err != nil {
		t.Fatalf("ByID: %v", err)
	}
	if gotJob.Status != jobs.StatusFailed {
		t.Errorf("job status = %s, want FAILED", gotJob.Status)
	}
	if gotJob.FaultCode == nil || *gotJob.FaultCode != "7006" {
		t.Errorf("job fault_code = %v, want 7006", gotJob.FaultCode)
	}
	if gotJob.FaultString == nil || !strings.Contains(*gotJob.FaultString, "controller-trust misconfiguration") {
		t.Errorf("job fault_string = %v, want it to flag a controller-trust misconfiguration, not a plain device fault", gotJob.FaultString)
	}
	if gotJob.FaultString == nil || !strings.Contains(*gotJob.FaultString, "controller endpoint not in ACL") {
		t.Errorf("job fault_string = %v, want it to still carry the agent's own message", gotJob.FaultString)
	}

	logged := buf.String()
	if !strings.Contains(logged, "level=WARN") {
		t.Errorf("logged output should be at Warn level, got: %s", logged)
	}
}

// TestErrorMappingCommandFailure covers design §6.4's fourth special case
// (USP 7022). Per the brief, ErrCommandFailure's own uspErr.Message
// already carries the agent's err_msg verbatim via ErrorFromMsg, so this
// is deliberately NOT special-cased in the switch -- it falls through to
// the same default handling as any unmapped code. This test proves that
// identical behavior rather than asserting on a distinct branch that
// doesn't exist.
func TestErrorMappingCommandFailure(t *testing.T) {
	d, jobsRepo, jobID, msgID, _ := dispatchTestSetup(t)

	if matched := d.handleResponse(agent, uspErrorMsg(msgID, 7022, "reboot command failed on device")); !matched {
		t.Fatal("handleResponse() = false, want true")
	}

	gotJob, err := jobsRepo.ByID(context.Background(), jobID)
	if err != nil {
		t.Fatalf("ByID: %v", err)
	}
	if gotJob.Status != jobs.StatusFailed {
		t.Errorf("job status = %s, want FAILED", gotJob.Status)
	}
	if gotJob.FaultCode == nil || *gotJob.FaultCode != "7022" {
		t.Errorf("job fault_code = %v, want 7022", gotJob.FaultCode)
	}
	if gotJob.FaultString == nil || *gotJob.FaultString != "reboot command failed on device" {
		t.Errorf("job fault_string = %v, want the agent's err_msg verbatim, unprefixed: %q", gotJob.FaultString, "reboot command failed on device")
	}
}

// TestErrorMappingUnmappedCode covers the checklist's final row: a code
// with no sentinel (design §6.4's "every other/unmapped code") must fall
// through to the generic path unchanged -- code and message recorded
// verbatim, exactly like Task 4/5's baseline behavior.
func TestErrorMappingUnmappedCode(t *testing.T) {
	d, jobsRepo, jobID, msgID, _ := dispatchTestSetup(t)

	if matched := d.handleResponse(agent, uspErrorMsg(msgID, 7003, "something went wrong internally")); !matched {
		t.Fatal("handleResponse() = false, want true")
	}

	gotJob, err := jobsRepo.ByID(context.Background(), jobID)
	if err != nil {
		t.Fatalf("ByID: %v", err)
	}
	if gotJob.Status != jobs.StatusFailed {
		t.Errorf("job status = %s, want FAILED", gotJob.Status)
	}
	if gotJob.FaultCode == nil || *gotJob.FaultCode != "7003" {
		t.Errorf("job fault_code = %v, want 7003", gotJob.FaultCode)
	}
	if gotJob.FaultString == nil || *gotJob.FaultString != "something went wrong internally" {
		t.Errorf("job fault_string = %v, want verbatim %q", gotJob.FaultString, "something went wrong internally")
	}
}

// TestErrorMappingNotWriteableAsync covers the same ErrNotWriteable (7013)
// typed handling as TestErrorMappingNotWriteable, but driven through
// handleOperationComplete's async CmdFailure path instead of
// handleResponse's sync Error path -- proving classifyAndFail's shared
// routing actually works from both call sites, not just the sync one.
// Without this, a future change that broke the oc.ErrCode -> USPError.Code
// mapping in handleOperationComplete, or reverted that call site back to
// the old flat resolveFailure, would go uncaught: the only pre-existing
// test on this path (TestHandleOperationCompleteResolvesFailure) uses code
// 7012, which is unmapped and exercises the default case either way.
func TestErrorMappingNotWriteableAsync(t *testing.T) {
	jobsRepo, deviceID := newDispatcherTestDB(t)
	ctx := context.Background()
	// TypeReboot mirrors TestHandleOperationCompleteResolvesFailure's own
	// setup -- the job type doesn't matter to what this test exercises
	// (classifyAndFail's typed-code routing from the async path), only
	// that it reaches RPC_SENT via LeaseForTypes below.
	job, err := jobsRepo.Create(ctx, deviceID, jobs.TypeReboot, jobs.RebootPayload{}, "test")
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	if _, err := jobsRepo.LeaseForTypes(ctx, deviceID, []string{jobs.TypeReboot}); err != nil {
		t.Fatalf("lease job: %v", err)
	}

	store := newFakeIdentityStore()
	registry := mtp.NewRegistry()
	d := newDispatcher(jobsRepo, store, registry, ctrl, slog.Default())

	oc := &usp.OperationComplete{
		CommandKey: job.CommandKey,
		Failed:     true,
		ErrCode:    7013,
		ErrMsg:     "Device.WiFi.SSID is read-only",
	}
	if err := d.handleOperationComplete(ctx, deviceID, oc); err != nil {
		t.Fatalf("handleOperationComplete() = %v, want nil", err)
	}

	gotJob, err := jobsRepo.ByID(ctx, job.ID)
	if err != nil {
		t.Fatalf("ByID: %v", err)
	}
	if gotJob.Status != jobs.StatusFailed {
		t.Errorf("job status = %s, want FAILED", gotJob.Status)
	}
	if gotJob.FaultCode == nil || *gotJob.FaultCode != "7013" {
		t.Errorf("job fault_code = %v, want 7013", gotJob.FaultCode)
	}
	wantFaultString := "parameter is not writeable: Device.WiFi.SSID is read-only"
	if gotJob.FaultString == nil || *gotJob.FaultString != wantFaultString {
		t.Errorf("job fault_string = %v, want %q -- same typed prefix the sync path (TestErrorMappingNotWriteable) produces, proving classifyAndFail's routing from handleOperationComplete", gotJob.FaultString, wantFaultString)
	}
}

// TestDispatcherForgetRemovesPendingForConn covers fix round 1, Important
// 1: forget must drop every pending entry sent on c, mirroring
// probe.forget's exact discipline.
func TestDispatcherForgetRemovesPendingForConn(t *testing.T) {
	d, _, _, _, c := dispatchTestSetup(t)
	if len(d.pending) != 1 {
		t.Fatalf("pending = %+v, want exactly 1 entry before forget", d.pending)
	}

	d.forget(c)

	if len(d.pending) != 0 {
		t.Errorf("pending = %+v, want none after forget(c)", d.pending)
	}
}

// TestDispatcherForgetPreventsStaleResponseAfterDisconnect is the
// regression test for the exact hazard Important 1 named: without
// forget, a late response on a msg_id whose connection already
// disconnected would still match in pending and call
// MarkSuccessWithDetail/MarkFailed with no status guard -- dangerous in
// particular once RecoverExpiredLeases has requeued the stranded job and
// a later trigger has re-dispatched it under a fresh msg_id, since the
// stale entry would then silently overwrite the re-dispatch's state.
// This proves forget breaks that: once forgotten, the old msg_id no
// longer matches at all.
func TestDispatcherForgetPreventsStaleResponseAfterDisconnect(t *testing.T) {
	d, jobsRepo, jobID, msgID, c := dispatchTestSetup(t)

	d.forget(c)
	if len(d.pending) != 0 {
		t.Fatalf("pending = %+v, want none after forget", d.pending)
	}

	if matched := d.handleResponse(agent, getResp(msgID, nil)); matched {
		t.Error("handleResponse matched a msg_id forget already dropped -- a late response could overwrite a since-re-dispatched job's state")
	}

	gotJob, err := jobsRepo.ByID(context.Background(), jobID)
	if err != nil {
		t.Fatalf("ByID: %v", err)
	}
	if gotJob.Status != jobs.StatusRPCSent {
		t.Errorf("job status = %s, want RPC_SENT (untouched -- the forgotten response must not have resolved it)", gotJob.Status)
	}
}

// TestHandleResponseTriggersNextQueuedDispatch covers fix round 1, Minor
// 5: resolving one dispatched job must immediately try to dispatch the
// next queued job for the same device, rather than leaving it to wait
// for the next periodic sweep (up to dispatchSweepInterval later).
func TestHandleResponseTriggersNextQueuedDispatch(t *testing.T) {
	jobsRepo, deviceID := newDispatcherTestDB(t)
	ctx := context.Background()
	job1, err := jobsRepo.Create(ctx, deviceID, jobs.TypeGetParameter,
		jobs.GetParameterPayload{Paths: []string{"Device.DeviceInfo."}}, "test")
	if err != nil {
		t.Fatalf("create job1: %v", err)
	}
	job2, err := jobsRepo.Create(ctx, deviceID, jobs.TypeGetParameter,
		jobs.GetParameterPayload{Paths: []string{"Device.WiFi."}}, "test")
	if err != nil {
		t.Fatalf("create job2: %v", err)
	}

	store := newFakeIdentityStore()
	registry := mtp.NewRegistry()
	c := &captureConn{id: agent}
	registerAgent(store, registry, deviceID, c)
	d := newDispatcher(jobsRepo, store, registry, ctrl, slog.Default())

	if err := d.tryDispatch(ctx, deviceID); err != nil {
		t.Fatalf("tryDispatch() = %v, want nil", err)
	}
	if len(c.sent) != 1 {
		t.Fatalf("c.sent = %d records, want 1 after the first dispatch", len(c.sent))
	}
	rec, err := usp.DecodeRecord(c.sent[0], agent)
	if err != nil {
		t.Fatalf("decode dispatched record: %v", err)
	}
	sentMsg, err := usp.DecodeMsg(rec.Payload)
	if err != nil {
		t.Fatalf("decode dispatched msg: %v", err)
	}
	msgID := sentMsg.GetHeader().GetMsgId()

	if matched := d.handleResponse(agent, getResp(msgID, nil)); !matched {
		t.Fatal("handleResponse did not match the first dispatched job's msg_id")
	}
	if len(c.sent) != 2 {
		t.Fatalf("c.sent = %d records, want 2: the second queued job must be dispatched immediately after the first resolves, not wait for the next sweep", len(c.sent))
	}

	got1, err := jobsRepo.ByID(ctx, job1.ID)
	if err != nil {
		t.Fatalf("ByID job1: %v", err)
	}
	if got1.Status != jobs.StatusSuccess {
		t.Errorf("job1 status = %s, want SUCCESS", got1.Status)
	}
	got2, err := jobsRepo.ByID(ctx, job2.ID)
	if err != nil {
		t.Fatalf("ByID job2: %v", err)
	}
	if got2.Status != jobs.StatusRPCSent {
		t.Errorf("job2 status = %s, want RPC_SENT (dispatched by handleResponse's re-trigger)", got2.Status)
	}
}

func TestHandleResponseUnknownMsgID(t *testing.T) {
	store := newFakeIdentityStore()
	registry := mtp.NewRegistry()
	d := newDispatcher(nil, store, registry, ctrl, slog.Default())

	if matched := d.handleResponse(agent, getResp("never-dispatched", nil)); matched {
		t.Error("handleResponse matched a msg_id this dispatcher never sent")
	}
	// A nil / bodiless message must not panic, mirroring probe.handle's
	// own guard.
	if matched := d.handleResponse(agent, nil); matched {
		t.Error("handleResponse matched a nil msg")
	}
	if matched := d.handleResponse(agent, &uspproto.Msg{Header: &uspproto.Header{MsgId: "x"}}); matched {
		t.Error("handleResponse matched an unrecognised msg_id")
	}
}

// TestHandleOperationCompleteResolves covers the checklist's identity-
// match row: an OperationComplete whose CommandKey names a real RPC_SENT
// job, reported over a connection reconciled to that same job's device,
// resolves the job -- success with OutputArgs in the result detail, via
// the exact same resolve-once path handleResponse uses.
func TestHandleOperationCompleteResolves(t *testing.T) {
	jobsRepo, deviceID := newDispatcherTestDB(t)
	ctx := context.Background()
	job, err := jobsRepo.Create(ctx, deviceID, jobs.TypeReboot, jobs.RebootPayload{}, "test")
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	// handleOperationComplete only resolves a job it finds RPC_SENT
	// (mirrors cmd/acs's own handleTransferComplete status guard) --
	// LeaseForTypes is what actually sets that status in production, so
	// drive it the same way here rather than asserting on a QUEUED job.
	if _, err := jobsRepo.LeaseForTypes(ctx, deviceID, []string{jobs.TypeReboot}); err != nil {
		t.Fatalf("lease job: %v", err)
	}

	store := newFakeIdentityStore()
	registry := mtp.NewRegistry()
	d := newDispatcher(jobsRepo, store, registry, ctrl, slog.Default())

	oc := &usp.OperationComplete{
		CommandKey: job.CommandKey,
		OutputArgs: map[string]string{"Status": "Complete"},
	}
	if err := d.handleOperationComplete(ctx, deviceID, oc); err != nil {
		t.Fatalf("handleOperationComplete() = %v, want nil", err)
	}

	gotJob, err := jobsRepo.ByID(ctx, job.ID)
	if err != nil {
		t.Fatalf("ByID: %v", err)
	}
	if gotJob.Status != jobs.StatusSuccess {
		t.Errorf("job status = %s, want SUCCESS", gotJob.Status)
	}
	if len(gotJob.ResultDetail) == 0 {
		t.Error("job result_detail is empty, want a JSON summary of the OperationComplete")
	}
}

// TestHandleOperationCompleteResolvesFailure covers the CmdFailure
// variant: Failed true resolves the job as FAILED with the agent's
// ErrCode/ErrMsg, not SUCCESS.
func TestHandleOperationCompleteResolvesFailure(t *testing.T) {
	jobsRepo, deviceID := newDispatcherTestDB(t)
	ctx := context.Background()
	job, err := jobsRepo.Create(ctx, deviceID, jobs.TypeReboot, jobs.RebootPayload{}, "test")
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	if _, err := jobsRepo.LeaseForTypes(ctx, deviceID, []string{jobs.TypeReboot}); err != nil {
		t.Fatalf("lease job: %v", err)
	}

	store := newFakeIdentityStore()
	registry := mtp.NewRegistry()
	d := newDispatcher(jobsRepo, store, registry, ctrl, slog.Default())

	oc := &usp.OperationComplete{
		CommandKey: job.CommandKey,
		Failed:     true,
		ErrCode:    7012,
		ErrMsg:     "command failed on device",
	}
	if err := d.handleOperationComplete(ctx, deviceID, oc); err != nil {
		t.Fatalf("handleOperationComplete() = %v, want nil", err)
	}

	gotJob, err := jobsRepo.ByID(ctx, job.ID)
	if err != nil {
		t.Fatalf("ByID: %v", err)
	}
	if gotJob.Status != jobs.StatusFailed {
		t.Errorf("job status = %s, want FAILED", gotJob.Status)
	}
	if gotJob.FaultCode == nil || *gotJob.FaultCode != "7012" {
		t.Errorf("job fault_code = %v, want 7012", gotJob.FaultCode)
	}
	if gotJob.FaultString == nil || *gotJob.FaultString != "command failed on device" {
		t.Errorf("job fault_string = %v, want %q", gotJob.FaultString, "command failed on device")
	}
}

// TestHandleOperationCompleteRefusesIdentityMismatch covers the
// identity-binding decision itself: a reporting connection whose own
// reconciled device id does not match the job's device id must not
// resolve it -- neither for a genuine mismatch (a different device's
// connection) nor for an unreconciled connection (connDeviceID == "",
// the case reconciledDeviceID yields for an unreconciled conn).
func TestHandleOperationCompleteRefusesIdentityMismatch(t *testing.T) {
	for _, tc := range []struct {
		name         string
		connDeviceID string
	}{
		{"mismatched device", "some-other-device-id"},
		{"unreconciled connection", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			jobsRepo, deviceID := newDispatcherTestDB(t)
			ctx := context.Background()
			job, err := jobsRepo.Create(ctx, deviceID, jobs.TypeReboot, jobs.RebootPayload{}, "test")
			if err != nil {
				t.Fatalf("create job: %v", err)
			}
			if _, err := jobsRepo.LeaseForTypes(ctx, deviceID, []string{jobs.TypeReboot}); err != nil {
				t.Fatalf("lease job: %v", err)
			}

			store := newFakeIdentityStore()
			registry := mtp.NewRegistry()
			d := newDispatcher(jobsRepo, store, registry, ctrl, slog.Default())

			oc := &usp.OperationComplete{CommandKey: job.CommandKey, OutputArgs: map[string]string{"Status": "Complete"}}
			if err := d.handleOperationComplete(ctx, tc.connDeviceID, oc); err != nil {
				t.Fatalf("handleOperationComplete() = %v, want nil (a refusal is not an error)", err)
			}

			gotJob, err := jobsRepo.ByID(ctx, job.ID)
			if err != nil {
				t.Fatalf("ByID: %v", err)
			}
			if gotJob.Status != jobs.StatusRPCSent {
				t.Errorf("job status = %s, want RPC_SENT (untouched -- an identity mismatch must not resolve it)", gotJob.Status)
			}
		})
	}
}

// TestHandleOperationCompleteUnknownCommandKey covers the checklist's
// not-found row: a command_key that names no job at all must be a no-op,
// not an error or a crash.
func TestHandleOperationCompleteUnknownCommandKey(t *testing.T) {
	jobsRepo, deviceID := newDispatcherTestDB(t)
	store := newFakeIdentityStore()
	registry := mtp.NewRegistry()
	d := newDispatcher(jobsRepo, store, registry, ctrl, slog.Default())

	oc := &usp.OperationComplete{CommandKey: "no-such-command-key"}
	if err := d.handleOperationComplete(context.Background(), deviceID, oc); err != nil {
		t.Fatalf("handleOperationComplete() = %v, want nil for an unknown command_key", err)
	}
}

// TestHandleOperationCompleteAfterSyncResolutionIsNoop and
// TestHandleOperationCompleteDeletesPendingBeforeSyncResponse both reuse
// dispatchTestSetup, which dispatches a GET_PARAMETER job rather than an
// Operate -- GET_PARAMETER can never actually produce a real
// OperationComplete in production (only Operate commands can), but
// that's irrelevant to what these two tests exercise: the pending-map
// and job-status bookkeeping around resolving a job twice from two
// different signal sources, which doesn't depend on which USP request
// type was dispatched. dispatchTestSetup's msg_id/CommandKey plumbing is
// exactly what both tests need and nothing about it is GET_PARAMETER-
// specific.

// TestHandleOperationCompleteAfterSyncResolutionIsNoop covers the
// checklist's double-resolution row: a job already resolved via a sync
// OperateResp (through handleResponse, exactly as Task 4 left it) must
// not be re-resolved by a later OperationComplete carrying the same
// command_key -- the second signal finds the job no longer RPC_SENT and
// leaves it alone.
func TestHandleOperationCompleteAfterSyncResolutionIsNoop(t *testing.T) {
	d, jobsRepo, jobID, msgID, _ := dispatchTestSetup(t)
	job, err := jobsRepo.ByID(context.Background(), jobID)
	if err != nil {
		t.Fatalf("ByID: %v", err)
	}
	deviceID := job.DeviceID
	commandKey := job.CommandKey

	// Resolve synchronously first, exactly as TestHandleResponseResolvesSuccess does.
	resp := getResp(msgID, map[string]string{"SoftwareVersion": "11.0.7"})
	if matched := d.handleResponse(agent, resp); !matched {
		t.Fatal("handleResponse() = false, want true for the dispatched job's own msg_id")
	}
	gotJob, err := jobsRepo.ByID(context.Background(), jobID)
	if err != nil {
		t.Fatalf("ByID: %v", err)
	}
	if gotJob.Status != jobs.StatusSuccess {
		t.Fatalf("job status = %s, want SUCCESS after the sync resolution", gotJob.Status)
	}

	// A later OperationComplete for the very same job (same command_key)
	// must not re-resolve it -- in particular it must not flip a SUCCESS
	// job to FAILED, or overwrite its result_detail.
	oc := &usp.OperationComplete{CommandKey: commandKey, Failed: true, ErrCode: 9999, ErrMsg: "should never apply"}
	if err := d.handleOperationComplete(context.Background(), deviceID, oc); err != nil {
		t.Fatalf("handleOperationComplete() = %v, want nil", err)
	}

	gotJob, err = jobsRepo.ByID(context.Background(), jobID)
	if err != nil {
		t.Fatalf("ByID: %v", err)
	}
	if gotJob.Status != jobs.StatusSuccess {
		t.Errorf("job status = %s, want still SUCCESS: a late OperationComplete must not re-resolve an already-resolved job", gotJob.Status)
	}
	if gotJob.FaultCode != nil {
		t.Errorf("job fault_code = %v, want nil: the late OperationComplete's failure must not have applied", gotJob.FaultCode)
	}
}

// TestHandleOperationCompleteDeletesPendingBeforeSyncResponse covers fix
// round 1, Important 1: the plan-mandated pending-map deletion in
// handleOperationComplete (dropping the dispatched request's own
// d.pending entry, found by CommandKey) had no test actually exercising
// its loop body -- every other test reaches RPC_SENT via a direct
// jobsRepo.LeaseForTypes call rather than tryDispatch, so d.pending was
// always empty and the loop never ran. This drives the real path:
// tryDispatch populates d.pending, the async OperationComplete arrives
// FIRST and must both resolve the job and delete that pending entry, and
// the dispatched request's own late sync response must then find nothing
// to match -- proving the async-first direction the deletion exists to
// protect, the mirror image of
// TestHandleOperationCompleteAfterSyncResolutionIsNoop's sync-first case.
func TestHandleOperationCompleteDeletesPendingBeforeSyncResponse(t *testing.T) {
	d, jobsRepo, jobID, msgID, _ := dispatchTestSetup(t)
	job, err := jobsRepo.ByID(context.Background(), jobID)
	if err != nil {
		t.Fatalf("ByID: %v", err)
	}
	if len(d.pending) != 1 {
		t.Fatalf("pending = %+v, want exactly 1 entry from dispatchTestSetup's own tryDispatch call", d.pending)
	}

	// The async OperationComplete arrives first (async wins) and must
	// resolve the job.
	oc := &usp.OperationComplete{CommandKey: job.CommandKey, OutputArgs: map[string]string{"Status": "Complete"}}
	if err := d.handleOperationComplete(context.Background(), job.DeviceID, oc); err != nil {
		t.Fatalf("handleOperationComplete() = %v, want nil", err)
	}
	gotJob, err := jobsRepo.ByID(context.Background(), jobID)
	if err != nil {
		t.Fatalf("ByID: %v", err)
	}
	if gotJob.Status != jobs.StatusSuccess {
		t.Fatalf("job status = %s, want SUCCESS after the async OperationComplete", gotJob.Status)
	}
	if len(d.pending) != 0 {
		t.Fatalf("pending = %+v, want none: handleOperationComplete must have deleted the dispatched request's own pending entry", d.pending)
	}

	// The dispatched request's own sync response now arrives late. With
	// the pending entry already gone, it must not match anything --
	// exactly like TestDispatcherForgetPreventsStaleResponseAfterDisconnect's
	// same assertion for the disconnect-driven case.
	if matched := d.handleResponse(agent, getResp(msgID, map[string]string{"SoftwareVersion": "11.0.7"})); matched {
		t.Error("handleResponse matched a msg_id the OperationComplete's pending-delete already removed")
	}

	// The job's result must still reflect the OperationComplete, not
	// whatever the late sync response would have written.
	gotJob, err = jobsRepo.ByID(context.Background(), jobID)
	if err != nil {
		t.Fatalf("ByID: %v", err)
	}
	if gotJob.Status != jobs.StatusSuccess {
		t.Errorf("job status = %s, want still SUCCESS: the late sync response must not have altered it", gotJob.Status)
	}
	var detail dispatchResultDetail
	if err := json.Unmarshal(gotJob.ResultDetail, &detail); err != nil {
		t.Fatalf("unmarshal result_detail: %v", err)
	}
	if detail.MsgType != uspproto.Header_NOTIFY.String() {
		t.Errorf("result_detail msg_type = %q, want %q: the job's result must still be the OperationComplete's, not the late sync GetResp's", detail.MsgType, uspproto.Header_NOTIFY.String())
	}
}

// TestReconcileTriggersDispatch covers Correction 4's hook:
// resolveAndMarkReconciled, on a successful reconciliation, must call
// dispatcher.tryDispatch for the newly-known device -- the "job queued
// before device connected" trigger (design S6.1).
func TestReconcileTriggersDispatch(t *testing.T) {
	jobsRepo, deviceID := newDispatcherTestDB(t)
	ctx := context.Background()
	if _, err := jobsRepo.Create(ctx, deviceID, jobs.TypeGetParameter,
		jobs.GetParameterPayload{Paths: []string{"Device.DeviceInfo."}}, "test"); err != nil {
		t.Fatalf("create job: %v", err)
	}

	store := newFakeIdentityStore()
	registry := mtp.NewRegistry()
	c := &captureConn{id: agent}
	registry.Add(c)
	// Seed the identity link resolveAndMarkReconciled itself resolves via
	// GetUspAgentByEndpointID -- as if reconciler.onBoard's own
	// UpsertFromOnBoard/LinkUspAgent had just run.
	store.agentsByEndpointID[string(agent)] = &devices.UspAgent{
		DeviceID:   deviceID,
		EndpointID: string(agent),
		MTPKind:    "WebSocket",
		Connected:  true,
	}

	h := &handler{
		log:        slog.Default(),
		registry:   registry,
		reconciler: newReconciler(store, slog.Default()),
		dispatcher: newDispatcher(jobsRepo, store, registry, ctrl, slog.Default()),
	}

	h.resolveAndMarkReconciled(c)

	if len(c.sent) != 1 {
		t.Fatalf("c.sent = %d records, want exactly 1 (dispatch triggered by the reconcile)", len(c.sent))
	}
	if gotDeviceID, ok := h.reconciledDeviceID(c); !ok || gotDeviceID != deviceID {
		t.Errorf("reconciledDeviceID(c) = (%q, %v), want (%q, true)", gotDeviceID, ok, deviceID)
	}
}

// ---------------------------------------------------------------------
// Response-classification tests (final review, Critical Finding 1):
// responseSummary used to only ever inspect GetResp, so every other
// *Resp -- AddResp, DeleteResp, OperateResp, GetSupportedDMResp -- was
// treated as an unconditional SUCCESS regardless of a device-reported
// per-item failure nested inside it. Each test below proves the specific
// regression its own doc comment names would NOT have been caught by the
// pre-fix code (classifyResponse/classifyGetResp/classifyAddResp/
// classifyDeleteResp/classifyOperateResp did not exist before this fix
// wave; responseSummary would have built a Params map, if any, and
// handleResponse would have unconditionally called resolveSuccess).
// ---------------------------------------------------------------------

// dispatchTestSetupJob is dispatchTestSetup generalized over job type and
// payload -- the new tests below need to dispatch ADD_OBJECT/
// DELETE_OBJECT/REBOOT/GET_PARAMETER jobs, not just dispatchTestSetup's
// own hardcoded GET_PARAMETER.
func dispatchTestSetupJob(t *testing.T, jobType string, payload any) (d *dispatcher, jobsRepo *jobs.Repository, jobID string, commandKey string, msgID string, c *captureConn) {
	t.Helper()
	jobsRepo, deviceID := newDispatcherTestDB(t)
	ctx := context.Background()
	job, err := jobsRepo.Create(ctx, deviceID, jobType, payload, "test")
	if err != nil {
		t.Fatalf("create job: %v", err)
	}

	store := newFakeIdentityStore()
	registry := mtp.NewRegistry()
	c = &captureConn{id: agent}
	registerAgent(store, registry, deviceID, c)
	d = newDispatcher(jobsRepo, store, registry, ctrl, slog.Default())

	if err := d.tryDispatch(ctx, deviceID); err != nil {
		t.Fatalf("tryDispatch() = %v, want nil", err)
	}
	if len(c.sent) != 1 {
		t.Fatalf("c.sent = %d records, want exactly 1", len(c.sent))
	}
	rec, err := usp.DecodeRecord(c.sent[0], agent)
	if err != nil {
		t.Fatalf("decode dispatched record: %v", err)
	}
	sentMsg, err := usp.DecodeMsg(rec.Payload)
	if err != nil {
		t.Fatalf("decode dispatched msg: %v", err)
	}
	return d, jobsRepo, job.ID, job.CommandKey, sentMsg.GetHeader().GetMsgId(), c
}

// addRespFailureMsg builds an AddResp carrying a single OperFailure --
// the shape a real agent sends for a per-object Add failure with no
// top-level Error body at all (allowPartial=true, dispatch.go's
// EncodeAdd call).
func addRespFailureMsg(msgID, requestedPath string, errCode uint32, errMsg string) *uspproto.Msg {
	return &uspproto.Msg{
		Header: &uspproto.Header{MsgId: msgID, MsgType: uspproto.Header_ADD_RESP},
		Body: &uspproto.Body{MsgBody: &uspproto.Body_Response{Response: &uspproto.Response{
			RespType: &uspproto.Response_AddResp{AddResp: &uspproto.AddResp{
				CreatedObjResults: []*uspproto.AddResp_CreatedObjectResult{{
					RequestedPath: requestedPath,
					OperStatus: &uspproto.AddResp_CreatedObjectResult_OperationStatus{
						OperStatus: &uspproto.AddResp_CreatedObjectResult_OperationStatus_OperFailure{
							OperFailure: &uspproto.AddResp_CreatedObjectResult_OperationStatus_OperationFailure{ErrCode: errCode, ErrMsg: errMsg},
						},
					},
				}},
			}},
		}}},
	}
}

// addRespSuccessMsg builds an AddResp carrying a single OperSuccess.
func addRespSuccessMsg(msgID, requestedPath, instantiatedPath string) *uspproto.Msg {
	return &uspproto.Msg{
		Header: &uspproto.Header{MsgId: msgID, MsgType: uspproto.Header_ADD_RESP},
		Body: &uspproto.Body{MsgBody: &uspproto.Body_Response{Response: &uspproto.Response{
			RespType: &uspproto.Response_AddResp{AddResp: &uspproto.AddResp{
				CreatedObjResults: []*uspproto.AddResp_CreatedObjectResult{{
					RequestedPath: requestedPath,
					OperStatus: &uspproto.AddResp_CreatedObjectResult_OperationStatus{
						OperStatus: &uspproto.AddResp_CreatedObjectResult_OperationStatus_OperSuccess{
							OperSuccess: &uspproto.AddResp_CreatedObjectResult_OperationStatus_OperationSuccess{InstantiatedPath: instantiatedPath},
						},
					},
				}},
			}},
		}}},
	}
}

// deleteRespFailureMsg is addRespFailureMsg's Delete counterpart.
func deleteRespFailureMsg(msgID, requestedPath string, errCode uint32, errMsg string) *uspproto.Msg {
	return &uspproto.Msg{
		Header: &uspproto.Header{MsgId: msgID, MsgType: uspproto.Header_DELETE_RESP},
		Body: &uspproto.Body{MsgBody: &uspproto.Body_Response{Response: &uspproto.Response{
			RespType: &uspproto.Response_DeleteResp{DeleteResp: &uspproto.DeleteResp{
				DeletedObjResults: []*uspproto.DeleteResp_DeletedObjectResult{{
					RequestedPath: requestedPath,
					OperStatus: &uspproto.DeleteResp_DeletedObjectResult_OperationStatus{
						OperStatus: &uspproto.DeleteResp_DeletedObjectResult_OperationStatus_OperFailure{
							OperFailure: &uspproto.DeleteResp_DeletedObjectResult_OperationStatus_OperationFailure{ErrCode: errCode, ErrMsg: errMsg},
						},
					},
				}},
			}},
		}}},
	}
}

// operateRespCmdFailureMsg builds an OperateResp whose single operation
// result is a CmdFailure -- a SYNCHRONOUS command failure (sendResp=true,
// dispatch.go's own EncodeOperate calls), which never arrives as a
// top-level Error body.
func operateRespCmdFailureMsg(msgID, command string, errCode uint32, errMsg string) *uspproto.Msg {
	return &uspproto.Msg{
		Header: &uspproto.Header{MsgId: msgID, MsgType: uspproto.Header_OPERATE_RESP},
		Body: &uspproto.Body{MsgBody: &uspproto.Body_Response{Response: &uspproto.Response{
			RespType: &uspproto.Response_OperateResp{OperateResp: &uspproto.OperateResp{
				OperationResults: []*uspproto.OperateResp_OperationResult{{
					ExecutedCommand: command,
					OperationResp: &uspproto.OperateResp_OperationResult_CmdFailure{
						CmdFailure: &uspproto.OperateResp_OperationResult_CommandFailure{ErrCode: errCode, ErrMsg: errMsg},
					},
				}},
			}},
		}}},
	}
}

// operateRespAcceptedMsg builds an OperateResp whose single operation
// result is only req_obj_path -- an ASYNC command's mere acceptance, not
// its terminal outcome (the two diagnostics job types: the real
// result/failure arrives later via Notify.OperationComplete).
func operateRespAcceptedMsg(msgID, command, reqObjPath string) *uspproto.Msg {
	return &uspproto.Msg{
		Header: &uspproto.Header{MsgId: msgID, MsgType: uspproto.Header_OPERATE_RESP},
		Body: &uspproto.Body{MsgBody: &uspproto.Body_Response{Response: &uspproto.Response{
			RespType: &uspproto.Response_OperateResp{OperateResp: &uspproto.OperateResp{
				OperationResults: []*uspproto.OperateResp_OperationResult{{
					ExecutedCommand: command,
					OperationResp:   &uspproto.OperateResp_OperationResult_ReqObjPath{ReqObjPath: reqObjPath},
				}},
			}},
		}}},
	}
}

// getRespPathErrorMsg builds a GetResp whose single requested-path result
// carries a non-zero err_code -- a per-path failure nested inside an
// otherwise well-formed GetResp, not a top-level Error body.
func getRespPathErrorMsg(msgID, requestedPath string, errCode uint32, errMsg string) *uspproto.Msg {
	return &uspproto.Msg{
		Header: &uspproto.Header{MsgId: msgID, MsgType: uspproto.Header_GET_RESP},
		Body: &uspproto.Body{MsgBody: &uspproto.Body_Response{Response: &uspproto.Response{
			RespType: &uspproto.Response_GetResp{GetResp: &uspproto.GetResp{
				ReqPathResults: []*uspproto.GetResp_RequestedPathResult{{
					RequestedPath: requestedPath,
					ErrCode:       errCode,
					ErrMsg:        errMsg,
				}},
			}},
		}}},
	}
}

// TestAddObjectOperFailureFailsJob covers Critical Finding 1's ADD_OBJECT
// case: an AddResp with a per-object OperFailure and no top-level Error
// body must fail the job, not resolve it SUCCESS. Against the pre-fix
// code (responseSummary only ever inspected GetResp; everything else was
// an unconditional resolveSuccess), this response would have resolved
// the job SUCCESS with an empty result_detail -- this test would have
// FAILED against that code.
func TestAddObjectOperFailureFailsJob(t *testing.T) {
	d, jobsRepo, jobID, _, msgID, _ := dispatchTestSetupJob(t, jobs.TypeAddObject,
		jobs.AddObjectPayload{ObjectPath: "Device.WiFi.SSID."})

	if matched := d.handleResponse(agent, addRespFailureMsg(msgID, "Device.WiFi.SSID.", 7010, "unsupported parameter in initial values")); !matched {
		t.Fatal("handleResponse() = false, want true")
	}

	gotJob, err := jobsRepo.ByID(context.Background(), jobID)
	if err != nil {
		t.Fatalf("ByID: %v", err)
	}
	if gotJob.Status != jobs.StatusFailed {
		t.Errorf("job status = %s, want FAILED", gotJob.Status)
	}
	if gotJob.FaultCode == nil || *gotJob.FaultCode != "7010" {
		t.Errorf("job fault_code = %v, want 7010", gotJob.FaultCode)
	}
	if gotJob.FaultString == nil || *gotJob.FaultString != "unsupported parameter in initial values" {
		t.Errorf("job fault_string = %v, want %q", gotJob.FaultString, "unsupported parameter in initial values")
	}
}

// TestAddObjectSuccessCapturesInstantiatedPath covers the happy path
// alongside TestAddObjectOperFailureFailsJob: an OperSuccess must resolve
// the job SUCCESS with the device-assigned InstantiatedPath captured in
// result_detail.
func TestAddObjectSuccessCapturesInstantiatedPath(t *testing.T) {
	d, jobsRepo, jobID, _, msgID, _ := dispatchTestSetupJob(t, jobs.TypeAddObject,
		jobs.AddObjectPayload{ObjectPath: "Device.WiFi.SSID."})

	if matched := d.handleResponse(agent, addRespSuccessMsg(msgID, "Device.WiFi.SSID.", "Device.WiFi.SSID.5.")); !matched {
		t.Fatal("handleResponse() = false, want true")
	}

	gotJob, err := jobsRepo.ByID(context.Background(), jobID)
	if err != nil {
		t.Fatalf("ByID: %v", err)
	}
	if gotJob.Status != jobs.StatusSuccess {
		t.Errorf("job status = %s, want SUCCESS", gotJob.Status)
	}
	var detail dispatchResultDetail
	if err := json.Unmarshal(gotJob.ResultDetail, &detail); err != nil {
		t.Fatalf("unmarshal result_detail: %v", err)
	}
	if got := detail.Params["Device.WiFi.SSID."]; got != "Device.WiFi.SSID.5." {
		t.Errorf("result_detail params[Device.WiFi.SSID.] = %q, want %q", got, "Device.WiFi.SSID.5.")
	}
}

// TestDeleteObjectOperFailureFailsJob is TestAddObjectOperFailureFailsJob's
// DELETE_OBJECT counterpart -- same pre-fix regression, same proof.
func TestDeleteObjectOperFailureFailsJob(t *testing.T) {
	d, jobsRepo, jobID, _, msgID, _ := dispatchTestSetupJob(t, jobs.TypeDeleteObject,
		jobs.DeleteObjectPayload{ObjectPath: "Device.WiFi.SSID.3."})

	if matched := d.handleResponse(agent, deleteRespFailureMsg(msgID, "Device.WiFi.SSID.3.", 7024, "delete failed on device")); !matched {
		t.Fatal("handleResponse() = false, want true")
	}

	gotJob, err := jobsRepo.ByID(context.Background(), jobID)
	if err != nil {
		t.Fatalf("ByID: %v", err)
	}
	if gotJob.Status != jobs.StatusFailed {
		t.Errorf("job status = %s, want FAILED", gotJob.Status)
	}
	if gotJob.FaultCode == nil || *gotJob.FaultCode != "7024" {
		t.Errorf("job fault_code = %v, want 7024", gotJob.FaultCode)
	}
	if gotJob.FaultString == nil || *gotJob.FaultString != "delete failed on device" {
		t.Errorf("job fault_string = %v, want %q", gotJob.FaultString, "delete failed on device")
	}
}

// TestSyncOperateCmdFailureFailsJob is the regression test called out by
// name in the brief: a synchronous Operate command failure (OperateResp
// carrying CmdFailure, sendResp=true) was previously silently resolved
// SUCCESS -- responseSummary never inspected OperateResp at all, so
// handleResponse's else branch (resolveSuccess) ran unconditionally for
// any non-Error-body response. This would have FAILED against the
// pre-fix code.
func TestSyncOperateCmdFailureFailsJob(t *testing.T) {
	d, jobsRepo, jobID, _, msgID, _ := dispatchTestSetupJob(t, jobs.TypeReboot, jobs.RebootPayload{})

	if matched := d.handleResponse(agent, operateRespCmdFailureMsg(msgID, "Device.Reboot()", 7022, "reboot command failed on device")); !matched {
		t.Fatal("handleResponse() = false, want true")
	}

	gotJob, err := jobsRepo.ByID(context.Background(), jobID)
	if err != nil {
		t.Fatalf("ByID: %v", err)
	}
	if gotJob.Status != jobs.StatusFailed {
		t.Errorf("job status = %s, want FAILED", gotJob.Status)
	}
	if gotJob.FaultCode == nil || *gotJob.FaultCode != "7022" {
		t.Errorf("job fault_code = %v, want 7022", gotJob.FaultCode)
	}
	if gotJob.FaultString == nil || *gotJob.FaultString != "reboot command failed on device" {
		t.Errorf("job fault_string = %v, want %q", gotJob.FaultString, "reboot command failed on device")
	}
}

// TestGetRespPathErrorFailsJob covers Critical Finding 1's GET_PARAMETER
// case: a GetResp whose requested-path result carries a non-zero
// err_code must fail the job. Against the pre-fix responseSummary (which
// only ever walked ResolvedPathResults, never looked at ErrCode at all),
// this would have resolved the job SUCCESS with an empty (or partial)
// Params map -- this test would have FAILED against that code.
func TestGetRespPathErrorFailsJob(t *testing.T) {
	d, jobsRepo, jobID, msgID, _ := dispatchTestSetup(t)

	if matched := d.handleResponse(agent, getRespPathErrorMsg(msgID, "Device.NoSuchPath.", 7016, "object does not exist")); !matched {
		t.Fatal("handleResponse() = false, want true")
	}

	gotJob, err := jobsRepo.ByID(context.Background(), jobID)
	if err != nil {
		t.Fatalf("ByID: %v", err)
	}
	if gotJob.Status != jobs.StatusFailed {
		t.Errorf("job status = %s, want FAILED", gotJob.Status)
	}
	if gotJob.FaultCode == nil || *gotJob.FaultCode != "7016" {
		t.Errorf("job fault_code = %v, want 7016", gotJob.FaultCode)
	}
	if gotJob.FaultString == nil || *gotJob.FaultString != "object does not exist" {
		t.Errorf("job fault_string = %v, want %q", gotJob.FaultString, "object does not exist")
	}
}

// TestAsyncOperateAcceptanceStaysPendingThenResolvesViaOperationComplete
// is Critical Finding 1's central fix, proven end to end: an OperateResp
// carrying only req_obj_path (an async command's mere acceptance) must
// NOT resolve the job and must NOT delete the pending entry -- the job
// stays RPC_SENT, and the eventual Notify.OperationComplete for the same
// CommandKey is what actually resolves it. Against the pre-fix code, the
// acceptance alone would have resolved the job SUCCESS immediately (via
// resolveSuccess, since it wasn't a Body_Error), and the real
// OperationComplete that followed would then have been silently
// discarded by handleOperationComplete's own "job.Status !=
// StatusRPCSent" guard (already correct, but defending a job that had
// already been wrongly resolved) -- this test would have FAILED against
// that code (job status would already be SUCCESS before
// handleOperationComplete ever got a chance to run, and result_detail
// would carry the wrong (empty) summary).
func TestAsyncOperateAcceptanceStaysPendingThenResolvesViaOperationComplete(t *testing.T) {
	d, jobsRepo, jobID, commandKey, msgID, _ := dispatchTestSetupJob(t, jobs.TypeDiagnosticsPing,
		jobs.DiagnosticsPingPayload{Host: "example.test", NumberOfRepetitions: 3, Timeout: 5000, DataBlockSize: 64, DSCP: 0})

	if len(d.pending) != 1 {
		t.Fatalf("pending = %d entries, want exactly 1 before the acceptance arrives", len(d.pending))
	}

	if matched := d.handleResponse(agent, operateRespAcceptedMsg(msgID, "Device.IP.Diagnostics.IPPing()", "Device.IP.Diagnostics.IPPing()")); !matched {
		t.Fatal("handleResponse() = false, want true: the acceptance still answers this dispatcher's own msg_id, it just isn't terminal")
	}

	gotJob, err := jobsRepo.ByID(context.Background(), jobID)
	if err != nil {
		t.Fatalf("ByID: %v", err)
	}
	if gotJob.Status != jobs.StatusRPCSent {
		t.Errorf("job status after acceptance = %s, want still RPC_SENT (the acceptance is not a terminal outcome)", gotJob.Status)
	}
	if len(d.pending) != 1 {
		t.Errorf("pending = %d entries after acceptance, want still 1: the acceptance must not delete the pending entry", len(d.pending))
	}

	// The real result arrives later, correlated by CommandKey, not msg_id.
	oc := &usp.OperationComplete{CommandKey: commandKey, OutputArgs: map[string]string{"SuccessCount": "3"}}
	if err := d.handleOperationComplete(context.Background(), gotJob.DeviceID, oc); err != nil {
		t.Fatalf("handleOperationComplete() = %v, want nil", err)
	}

	gotJob, err = jobsRepo.ByID(context.Background(), jobID)
	if err != nil {
		t.Fatalf("ByID: %v", err)
	}
	if gotJob.Status != jobs.StatusSuccess {
		t.Errorf("job status after OperationComplete = %s, want SUCCESS", gotJob.Status)
	}
	if len(d.pending) != 0 {
		t.Errorf("pending = %d entries after OperationComplete, want none", len(d.pending))
	}
	var detail dispatchResultDetail
	if err := json.Unmarshal(gotJob.ResultDetail, &detail); err != nil {
		t.Fatalf("unmarshal result_detail: %v", err)
	}
	if detail.Params["SuccessCount"] != "3" {
		t.Errorf("result_detail params[SuccessCount] = %q, want %q", detail.Params["SuccessCount"], "3")
	}

	// The stale acceptance's own msg_id must now match nothing (the
	// pending entry was deleted by handleOperationComplete's own
	// CommandKey-keyed cleanup, mirroring
	// TestHandleOperationCompleteDeletesPendingBeforeSyncResponse's same
	// assertion for the sync-GetResp case).
	if matched := d.handleResponse(agent, operateRespAcceptedMsg(msgID, "Device.IP.Diagnostics.IPPing()", "Device.IP.Diagnostics.IPPing()")); matched {
		t.Error("handleResponse matched a msg_id the OperationComplete's pending-delete already removed")
	}
}

// TestGetSupportedDMCapturesSummary covers Critical Finding 1's fifth
// item: GetSupportedDMResp's actual result must land in result_detail,
// not nothing. Against the pre-fix responseSummary (which never
// inspected GetSupportedDMResp at all), result_detail would have had no
// Params -- this test would have FAILED against that code.
func TestGetSupportedDMCapturesSummary(t *testing.T) {
	d, jobsRepo, jobID, _, msgID, _ := dispatchTestSetupJob(t, jobs.TypeParameterDiscovery,
		jobs.ParameterDiscoveryPayload{Root: "Device."})

	dmResp := &uspproto.Msg{
		Header: &uspproto.Header{MsgId: msgID, MsgType: uspproto.Header_GET_SUPPORTED_DM_RESP},
		Body: &uspproto.Body{MsgBody: &uspproto.Body_Response{Response: &uspproto.Response{
			RespType: &uspproto.Response_GetSupportedDmResp{GetSupportedDmResp: &uspproto.GetSupportedDMResp{
				ReqObjResults: []*uspproto.GetSupportedDMResp_RequestedObjectResult{{
					ReqObjPath: "Device.",
					SupportedObjs: []*uspproto.GetSupportedDMResp_SupportedObjectResult{
						{SupportedObjPath: "Device.WiFi."},
						{SupportedObjPath: "Device.DeviceInfo."},
					},
				}},
			}},
		}}},
	}

	if matched := d.handleResponse(agent, dmResp); !matched {
		t.Fatal("handleResponse() = false, want true")
	}

	gotJob, err := jobsRepo.ByID(context.Background(), jobID)
	if err != nil {
		t.Fatalf("ByID: %v", err)
	}
	if gotJob.Status != jobs.StatusSuccess {
		t.Errorf("job status = %s, want SUCCESS", gotJob.Status)
	}
	var detail dispatchResultDetail
	if err := json.Unmarshal(gotJob.ResultDetail, &detail); err != nil {
		t.Fatalf("unmarshal result_detail: %v", err)
	}
	if len(detail.Params) == 0 {
		t.Error("result_detail params is empty, want a summary of the discovered objects")
	}
	if got := detail.Params["Device."]; got != "2 supported objects discovered" {
		t.Errorf("result_detail params[Device.] = %q, want %q", got, "2 supported objects discovered")
	}
}

// ---------------------------------------------------------------------
// Single-flight dispatch tests (final review, Important Finding 2).
// ---------------------------------------------------------------------

// TestTryDispatchSingleFlightPerDevice proves tryDispatch refuses to
// lease a second job for a device that already has one RPC_SENT, and
// that once the first resolves, the next trigger (here, handleResponse's
// own re-trigger -- fix round 1, Minor 5) picks up the second job.
func TestTryDispatchSingleFlightPerDevice(t *testing.T) {
	jobsRepo, deviceID := newDispatcherTestDB(t)
	ctx := context.Background()
	job1, err := jobsRepo.Create(ctx, deviceID, jobs.TypeGetParameter,
		jobs.GetParameterPayload{Paths: []string{"Device.DeviceInfo."}}, "test")
	if err != nil {
		t.Fatalf("create job1: %v", err)
	}
	job2, err := jobsRepo.Create(ctx, deviceID, jobs.TypeGetParameter,
		jobs.GetParameterPayload{Paths: []string{"Device.WiFi."}}, "test")
	if err != nil {
		t.Fatalf("create job2: %v", err)
	}

	store := newFakeIdentityStore()
	registry := mtp.NewRegistry()
	c := &captureConn{id: agent}
	registerAgent(store, registry, deviceID, c)
	d := newDispatcher(jobsRepo, store, registry, ctrl, slog.Default())

	if err := d.tryDispatch(ctx, deviceID); err != nil {
		t.Fatalf("tryDispatch() #1 = %v, want nil", err)
	}
	if len(c.sent) != 1 {
		t.Fatalf("c.sent = %d records after first dispatch, want 1", len(c.sent))
	}
	got1, err := jobsRepo.ByID(ctx, job1.ID)
	if err != nil {
		t.Fatalf("ByID job1: %v", err)
	}
	if got1.Status != jobs.StatusRPCSent {
		t.Fatalf("job1 status = %s, want RPC_SENT", got1.Status)
	}

	// A second trigger (NOTIFY, reconnect, or sweep -- tryDispatch can't
	// tell which) while job1 is still RPC_SENT must NOT lease job2.
	if err := d.tryDispatch(ctx, deviceID); err != nil {
		t.Fatalf("tryDispatch() #2 = %v, want nil", err)
	}
	if len(c.sent) != 1 {
		t.Errorf("c.sent = %d records after second tryDispatch, want still 1: job2 must not be dispatched while job1 is RPC_SENT", len(c.sent))
	}
	got2, err := jobsRepo.ByID(ctx, job2.ID)
	if err != nil {
		t.Fatalf("ByID job2: %v", err)
	}
	if got2.Status != jobs.StatusQueued {
		t.Errorf("job2 status = %s, want still QUEUED (never leased while job1 is in flight)", got2.Status)
	}

	// Resolving job1 frees the device; the existing re-trigger mechanism
	// (resolveSuccess -> retryDispatch -> tryDispatch) must then pick up
	// job2 without waiting for the next periodic sweep.
	rec, err := usp.DecodeRecord(c.sent[0], agent)
	if err != nil {
		t.Fatalf("decode dispatched record: %v", err)
	}
	sentMsg, err := usp.DecodeMsg(rec.Payload)
	if err != nil {
		t.Fatalf("decode dispatched msg: %v", err)
	}
	if matched := d.handleResponse(agent, getResp(sentMsg.GetHeader().GetMsgId(), nil)); !matched {
		t.Fatal("handleResponse did not match job1's msg_id")
	}
	if len(c.sent) != 2 {
		t.Fatalf("c.sent = %d records after job1 resolves, want 2: job2 must now be dispatched", len(c.sent))
	}
	got2, err = jobsRepo.ByID(ctx, job2.ID)
	if err != nil {
		t.Fatalf("ByID job2: %v", err)
	}
	if got2.Status != jobs.StatusRPCSent {
		t.Errorf("job2 status = %s, want RPC_SENT (dispatched once job1 freed the device)", got2.Status)
	}
}
