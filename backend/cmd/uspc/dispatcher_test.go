package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
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

// TestTryDispatchRequeuesUnsupportedType covers a leased job whose type
// buildUSPRequest cannot render over USP (FIRMWARE_DOWNLOAD -- see
// dispatch.go's own doc comment on why it's still in
// uspDispatchableTypes despite always returning ErrUnsupportedOverUSP
// today). uspDispatchableTypes no longer includes FIRMWARE_DOWNLOAD (fix
// round 1, Important 2: leasing for a type that can never build a USP
// request lets it sit at the front of a device's queue and starve every
// other dispatchable job behind it, on every trigger, forever -- see that
// var's own doc comment). This confirms the exclusion actually holds: a
// FIRMWARE_DOWNLOAD job is never leased, so it can never even reach
// buildUSPRequest in the first place.
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
// deliver a matching response.
func dispatchTestSetup(t *testing.T) (d *dispatcher, jobsRepo *jobs.Repository, jobID string, msgID string, c *captureConn) {
	t.Helper()
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
