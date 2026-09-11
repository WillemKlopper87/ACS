package main

import (
	"context"
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
// today). Left alone the lease would strand it RPC_SENT forever, since
// no agent will ever answer an RPC that was never sent.
func TestTryDispatchRequeuesUnsupportedType(t *testing.T) {
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
