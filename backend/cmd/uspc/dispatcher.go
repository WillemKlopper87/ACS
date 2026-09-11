// dispatcher.go is the dispatch loop and sync-completion correlation
// that ties internal/jobs (the durable ACS-initiated work queue) to a
// live USP connection: dispatch.go builds the request bytes, this file
// decides when to build one and what to do with the answer.
//
// Three trigger paths converge on tryDispatch (design S6.1): a Postgres
// NOTIFY for an already-connected device (main.go's drain goroutine), a
// successful identity reconcile for a device that queued a job before it
// connected (handler.go's resolveAndMarkReconciled), and a periodic
// sweep as the stated safety net for a dropped NOTIFY or a race between
// the other two triggers. Sync completions correlate by msg_id, the same
// pattern probe.go already established for its own single-purpose
// Get/GetResp round trip, generalized here to every dispatched job.
package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"acs/internal/devices"
	"acs/internal/jobs"
	"acs/internal/usp"
	"acs/internal/usp/mtp"
	"acs/internal/usp/uspproto"
)

// uspDispatchableTypes are the job types dispatch.go's buildUSPRequest can
// actually render as a USP request today -- nine of internal/jobs'
// fifteen types (the other five are CWMP-only and never reach this list).
// FIRMWARE_DOWNLOAD is deliberately EXCLUDED (fix round 1, Important 2)
// even though dispatch.go's own package doc calls it one of "the ten
// types [that] have a USP equivalent" in principle: buildUSPRequest
// unconditionally returns ErrUnsupportedOverUSP for it today (the missing
// instance-selection step), so leasing for it would only ever repeat the
// same lease-then-requeue cycle forever. That isn't a harmless no-op: a
// prior version of this list included FIRMWARE_DOWNLOAD reasoning that
// requeuing it would keep other work unstarved, but the opposite is true
// -- LeaseForTypes always takes the OLDEST queued matching job, so a
// dispatchable-but-never-buildable job at the front of a device's queue
// would starve every genuinely dispatchable job queued behind it, on
// every trigger (NOTIFY, reconnect, sweep -- every 30s, forever), while
// its own attempts counter grows unbounded (Requeue clears the lease
// without touching attempts, so RecoverExpiredLeases' stale-lease reaper
// never sees it as expired either). Leasing for a type this dispatcher
// can never build is the bug; the fix is to never lease for it, not to
// requeue faster. A later plan that adds the missing instance-discovery
// step can add FIRMWARE_DOWNLOAD back here once buildUSPRequest can
// actually build it. internal/jobs itself stays protocol-agnostic -- this
// subset is cmd/uspc's own concern, not something internal/jobs needs to
// know.
var uspDispatchableTypes = []string{
	jobs.TypeGetParameter,
	jobs.TypeSetParameter,
	jobs.TypeAddObject,
	jobs.TypeDeleteObject,
	jobs.TypeReboot,
	jobs.TypeFactoryReset,
	jobs.TypeDiagnosticsPing,
	jobs.TypeDiagnosticsTraceroute,
	jobs.TypeParameterDiscovery,
}

// dispatchSweepInterval sets how often periodicSweep re-checks every
// live, reconciled connection for leasable work -- the safety net for a
// NOTIFY that was dropped (jobs.notifyBufferSize overflow) or fired
// before OnConnect/reconcile finished. 30s matches the order of
// magnitude of CWMP's own periodic-Inform-adjacent intervals (a device's
// PeriodicInformInterval is commonly configured in the tens of seconds
// to low minutes range): frequent enough that a missed NOTIFY is caught
// promptly, infrequent enough not to hammer Postgres with a full
// registry scan every few seconds.
const dispatchSweepInterval = 30 * time.Second

// pendingDispatch is what tryDispatch remembers about one outstanding
// dispatched job, keyed by its msg_id, so handleResponse can find the
// job to complete and probe.go's own pendingProbe pattern is mirrored
// rather than reinvented.
type pendingDispatch struct {
	job  *jobs.Job
	conn mtp.Conn
}

// dispatcher owns the dispatch loop's mutable state: which jobs are
// outstanding, keyed by the msg_id their request was sent with. Reads
// jobs via jobsRepo.LeaseForTypes/MarkSuccessWithDetail/MarkFailed and
// resolves device_id -> live conn via devicesRepo (device_id ->
// endpoint_id) and registry (endpoint_id -> mtp.Conn).
type dispatcher struct {
	jobsRepo     *jobs.Repository
	devicesRepo  identityStore
	registry     *mtp.Registry
	controllerID usp.EndpointID
	log          *slog.Logger

	mu      sync.Mutex
	pending map[string]pendingDispatch
}

// newDispatcher returns a dispatcher ready for concurrent use. log
// defaults to slog.Default() when nil, matching this package's other
// constructors (see newProbe, newReconciler).
func newDispatcher(jobsRepo *jobs.Repository, devicesRepo identityStore, registry *mtp.Registry, controllerID usp.EndpointID, log *slog.Logger) *dispatcher {
	if log == nil {
		log = slog.Default()
	}
	return &dispatcher{
		jobsRepo:     jobsRepo,
		devicesRepo:  devicesRepo,
		registry:     registry,
		controllerID: controllerID,
		log:          log,
		pending:      make(map[string]pendingDispatch),
	}
}

// tryDispatch attempts to dispatch one leasable job for deviceID over
// its live USP connection, if any. It is deliberately quiet (nil, no
// log) for the ordinary "nothing to do yet" outcomes -- no live
// connection, no leasable job -- since every one of its three callers
// (a NOTIFY, a fresh reconcile, the periodic sweep) fires far more often
// than there is actually a job waiting, and every one of them is
// prepared for that.
func (d *dispatcher) tryDispatch(ctx context.Context, deviceID string) error {
	if d.jobsRepo == nil {
		// A dispatcher built without a jobs repository -- handler_test.go's
		// newTestHandler wires a *dispatcher into handler for its own
		// DB-free identity/reconciliation tests, which never need real job
		// dispatch. Mirrors probe.metrics' own "nil is a valid no-op
		// configuration for unit tests" convention; main.go always
		// constructs a real *jobs.Repository, so this never triggers in
		// production.
		return nil
	}

	lookupCtx, cancel := context.WithTimeout(ctx, dbCallTimeout)
	agentRow, err := d.devicesRepo.GetUspAgentByDeviceID(lookupCtx, deviceID)
	cancel()
	if err != nil {
		if errors.Is(err, devices.ErrUspAgentNotFound) {
			return nil
		}
		return fmt.Errorf("dispatcher: look up usp agent for device %s: %w", deviceID, err)
	}

	conn, ok := d.registry.Get(usp.EndpointID(agentRow.EndpointID))
	if !ok {
		// Reconciled in the database but not (or no longer) live in this
		// process's registry -- a race between reconcile and disconnect, or
		// a stale usp_agents row. Nothing to send to.
		return nil
	}

	leaseCtx, cancel := context.WithTimeout(ctx, dbCallTimeout)
	job, err := d.jobsRepo.LeaseForTypes(leaseCtx, deviceID, uspDispatchableTypes)
	cancel()
	if err != nil {
		return fmt.Errorf("dispatcher: lease job for device %s: %w", deviceID, err)
	}
	if job == nil {
		return nil
	}

	msgID := usp.NewMsgID()
	msgBytes, err := buildUSPRequest(msgID, job)
	if errors.Is(err, ErrUnsupportedOverUSP) {
		// Defensive default, not a normal path: every type currently in
		// uspDispatchableTypes maps to a real USP request (see that var's
		// own doc comment -- FIRMWARE_DOWNLOAD, the one type that always
		// returned this sentinel, was removed from the leasable list in fix
		// round 1 rather than relying on this branch to paper over leasing
		// for it). This guards only against a future edit to
		// uspDispatchableTypes accidentally reintroducing a type
		// buildUSPRequest can't build. The lease already consumed an
		// attempt and left the job RPC_SENT; left alone it would sit there
		// forever since no agent will ever answer an RPC that was never
		// sent, so requeue immediately rather than falsely appearing
		// in-flight.
		d.log.Warn("uspc: dispatcher: leased job has no USP mapping, requeuing", "job_id", job.ID, "job_type", job.Type, "device_id", deviceID)
		requeueCtx, requeueCancel := context.WithTimeout(ctx, dbCallTimeout)
		defer requeueCancel()
		if requeueErr := d.jobsRepo.Requeue(requeueCtx, job.ID); requeueErr != nil {
			d.log.Warn("uspc: dispatcher: failed to requeue unsupported job", "job_id", job.ID, "error", requeueErr)
		}
		return nil
	}
	if err != nil {
		// Any other build failure (e.g. an unmarshalable payload) is a
		// genuine defect in the job's own data, not a missing mapping --
		// retrying would fail identically, so mark it FAILED for an
		// operator to see rather than requeuing it into a retry loop.
		d.log.Warn("uspc: dispatcher: failed to build usp request for leased job, marking failed", "job_id", job.ID, "job_type", job.Type, "device_id", deviceID, "error", err)
		failCtx, failCancel := context.WithTimeout(ctx, dbCallTimeout)
		defer failCancel()
		if failErr := d.jobsRepo.MarkFailed(failCtx, job.ID, "USP_DISPATCH_BUILD_ERROR", err.Error()); failErr != nil {
			d.log.Warn("uspc: dispatcher: failed to mark job failed after build error", "job_id", job.ID, "error", failErr)
		}
		return nil
	}

	record, err := usp.EncodeRecord(d.controllerID, conn.Endpoint(), msgBytes)
	if err != nil {
		return fmt.Errorf("dispatcher: encode record for job %s: %w", job.ID, err)
	}

	d.mu.Lock()
	d.pending[msgID] = pendingDispatch{job: job, conn: conn}
	d.mu.Unlock()

	sendCtx, sendCancel := context.WithTimeout(ctx, sendTimeout)
	defer sendCancel()
	if err := conn.Send(sendCtx, record); err != nil {
		d.mu.Lock()
		delete(d.pending, msgID)
		d.mu.Unlock()
		return fmt.Errorf("dispatcher: send job %s: %w", job.ID, err)
	}
	return nil
}

// dispatchResultDetail is the JSON-serializable shape stored in
// jobs.result_detail on a successful dispatch response. It does not aim
// to match CWMP's own result_detail shape byte-for-byte -- just to be
// useful: the response's message type, and for a GetResp, the resolved
// parameters it carried (Task 6 tightens this per message type).
type dispatchResultDetail struct {
	MsgType string            `json:"msg_type"`
	Params  map[string]string `json:"params,omitempty"`
}

// responseSummary builds the result detail for msg, a successful
// (non-Error) response to a dispatched job.
func responseSummary(msg *uspproto.Msg) dispatchResultDetail {
	detail := dispatchResultDetail{MsgType: msg.GetHeader().GetMsgType().String()}
	if getResp := msg.GetBody().GetResponse().GetGetResp(); getResp != nil {
		params := make(map[string]string)
		for _, reqResult := range getResp.GetReqPathResults() {
			for _, resolved := range reqResult.GetResolvedPathResults() {
				for name, value := range resolved.GetResultParams() {
					params[resolved.GetResolvedPath()+name] = value
				}
			}
		}
		detail.Params = params
	}
	return detail
}

// handleResponse reports whether msg answers one of this dispatcher's
// outstanding requests (by msg_id), and if so completes the
// corresponding job: MarkFailed for an Error body, MarkSuccessWithDetail
// otherwise. Same shape as probe.handle -- look up by msg_id,
// delete-on-match, tolerate a nil msg/header or an unrecognised msg_id
// without panicking, since this is called on every inbound message that
// the OnBoardRequest and probe fallthroughs didn't already claim.
//
// On a match it also re-triggers tryDispatch for the same device (fix
// round 1, Minor 5): without this, a device with several jobs queued back
// to back would only ever drain one job per periodicSweep interval (30s)
// once its first dispatch completed, instead of as fast as the device
// itself answers. This runs regardless of whether the completed job
// itself succeeded or failed -- either way the device is still connected
// and may have more queued work.
func (d *dispatcher) handleResponse(from usp.EndpointID, msg *uspproto.Msg) (matched bool) {
	if msg == nil || msg.GetHeader() == nil {
		return false
	}

	msgID := msg.GetHeader().GetMsgId()
	d.mu.Lock()
	entry, ok := d.pending[msgID]
	if ok {
		delete(d.pending, msgID)
	}
	d.mu.Unlock()
	if !ok {
		return false
	}

	ctx, cancel := context.WithTimeout(context.Background(), dbCallTimeout)
	defer cancel()

	if uspErr := usp.ErrorFromMsg(msg); uspErr != nil {
		d.classifyAndFail(ctx, entry.job, from, uspErr, "msg_id", msgID)
	} else {
		d.resolveSuccess(ctx, entry.job, from, responseSummary(msg))
	}
	return true
}

// classifyAndFail is handleResponse and handleOperationComplete's shared
// error-mapping entry point (design §6.4): both a sync Error body and an
// async Notify.OperationComplete CmdFailure carry a USP 7xxx code, so both
// route through the exact same errors.Is-based switch rather than
// duplicating it. extraLogAttrs lets a caller add context this function
// doesn't otherwise have (handleResponse's own msg_id; handleOperationComplete
// has none to add).
//
// Four codes get typed handling per design §6.4:
//
//   - ErrNotWriteable (7013): the Huawei-class trap that resolves, sends
//     and silently fails on CWMP. USP makes it a typed error; this keeps
//     it typed all the way into the job's FaultString instead of letting
//     it collapse back into an opaque code+message pair, so an operator
//     can actually tell what happened without cross-referencing the USP
//     error table.
//   - ErrObjectDoesNotExist (7016): design §6.4 says "fail; trigger
//     re-discovery," but this plan deliberately does NOT auto-queue a
//     TypeParameterDiscovery job from inside error handling. Every
//     failed Add/Delete/Set against a persistently stale data model
//     would otherwise requeue another discovery RPC round trip -- a
//     retry storm risk with no backoff of its own (unlike
//     internal/jobs' own retry machinery -- MaxAttempts,
//     RecoverExpiredLeases' nonRepeatableTypes handling -- which governs
//     retrying the SAME job, not spawning a new one). Re-discovery-on-
//     demand is a reasonable operator/console feature for a later plan;
//     for now this just logs distinctly so an operator (or that future
//     automation) can act on it.
//   - ErrPermissionDenied (7006): flagged as a controller-trust
//     misconfiguration, not a device fault, and logged at Warn since it
//     signals an operational problem with the deployment rather than a
//     routine per-job failure.
//   - ErrCommandFailure (7022) is deliberately NOT special-cased:
//     ErrorFromMsg already carries the agent's err_msg verbatim in
//     uspErr.Message, which is exactly what the default case already
//     records, so a dedicated branch here would just be the default
//     case again under a different name.
//
// Every other/unmapped code falls through to the default case, unchanged
// from Task 4/5's baseline: MarkFailed with code and message recorded
// verbatim.
func (d *dispatcher) classifyAndFail(ctx context.Context, job *jobs.Job, from usp.EndpointID, uspErr *usp.USPError, extraLogAttrs ...any) {
	logAttrs := append([]any{"job_id", job.ID, "endpoint", from, "error", uspErr}, extraLogAttrs...)

	switch {
	case errors.Is(uspErr, usp.ErrNotWriteable):
		d.log.Warn("uspc: dispatcher: job failed: attempt to update a non-writeable parameter -- typed and surfaced rather than a silent Huawei-class no-op", logAttrs...)
		d.resolveFailure(ctx, job, from, strconv.Itoa(int(uspErr.Code)), "parameter is not writeable: "+uspErr.Message)
	case errors.Is(uspErr, usp.ErrObjectDoesNotExist):
		d.log.Warn("uspc: dispatcher: job failed: target object does not exist on the device -- data model may be stale, consider re-running parameter discovery", append(logAttrs, "device_id", job.DeviceID)...)
		d.resolveFailure(ctx, job, from, strconv.Itoa(int(uspErr.Code)), uspErr.Message)
	case errors.Is(uspErr, usp.ErrPermissionDenied):
		d.log.Warn("uspc: dispatcher: job failed: permission denied -- likely a controller-trust misconfiguration, not a device fault", logAttrs...)
		d.resolveFailure(ctx, job, from, strconv.Itoa(int(uspErr.Code)), "controller-trust misconfiguration (permission denied): "+uspErr.Message)
	default:
		d.log.Warn("uspc: dispatcher: dispatched job's request was answered with an error", logAttrs...)
		d.resolveFailure(ctx, job, from, strconv.Itoa(int(uspErr.Code)), uspErr.Message)
	}
}

// resolveSuccess marks job successfully complete with detail as its
// result, then re-triggers tryDispatch for job's device (fix round 1,
// Minor 5) so a next queued job doesn't wait for the next periodic
// sweep. Shared by handleResponse (a sync *Resp answering a dispatched
// request) and handleOperationComplete (an async Notify), so a job
// resolved either way goes through the exact same completion path. from
// is the endpoint that reported the completion, purely for logging --
// handleOperationComplete has no live endpoint to hand it (only the
// device id its own identity check already resolved), so it passes "".
func (d *dispatcher) resolveSuccess(ctx context.Context, job *jobs.Job, from usp.EndpointID, detail dispatchResultDetail) {
	if err := d.jobsRepo.MarkSuccessWithDetail(ctx, job.ID, detail); err != nil {
		d.log.Warn("uspc: dispatcher: failed to mark job success", "job_id", job.ID, "device_id", job.DeviceID, "endpoint", from, "error", err)
	}
	d.retryDispatch(job.DeviceID, from)
}

// resolveFailure is resolveSuccess's failure counterpart.
func (d *dispatcher) resolveFailure(ctx context.Context, job *jobs.Job, from usp.EndpointID, faultCode, faultString string) {
	if err := d.jobsRepo.MarkFailed(ctx, job.ID, faultCode, faultString); err != nil {
		d.log.Warn("uspc: dispatcher: failed to mark job failed", "job_id", job.ID, "device_id", job.DeviceID, "endpoint", from, "error", err)
	}
	d.retryDispatch(job.DeviceID, from)
}

// retryDispatch re-triggers tryDispatch for deviceID after a job
// resolves. handleResponse/handleOperationComplete both run inline on
// the transport's own read-loop goroutine (same hazard
// resolveAndMarkReconciled's own doc comment names), so this gets its
// own single bounded context rather than chaining unboundedly off
// whatever budget the caller's own DB call left behind.
func (d *dispatcher) retryDispatch(deviceID string, from usp.EndpointID) {
	retryCtx, retryCancel := context.WithTimeout(context.Background(), dbCallTimeout)
	defer retryCancel()
	if err := d.tryDispatch(retryCtx, deviceID); err != nil {
		d.log.Warn("uspc: dispatcher: failed to trigger dispatch for next queued job", "device_id", deviceID, "endpoint", from, "error", err)
	}
}

// operationCompleteResultDetail builds the result detail for a
// successful OperationComplete, mirroring responseSummary's shape for a
// sync GetResp -- the message type actually received (NOTIFY, since an
// OperationComplete is a Notify, not a *Resp) plus whatever output
// arguments the agent reported.
func operationCompleteResultDetail(oc *usp.OperationComplete) dispatchResultDetail {
	return dispatchResultDetail{MsgType: uspproto.Header_NOTIFY.String(), Params: oc.OutputArgs}
}

// handleOperationComplete resolves the job named by oc.CommandKey (the
// async completion path, design S6.3 -- the same command_key
// correlation CWMP's TransferComplete already uses, mirrored in
// cmd/acs/session.go's handleTransferComplete). connDeviceID is the
// device id the reporting connection itself reconciled to (handler's
// own reconciledDeviceID, empty if unreconciled); USP has no
// per-request credential the way CWMP's mTLS/basic-auth binding does, so
// this identity check is the substitute -- a command_key resolution is
// refused, not trusted, unless the reporting connection's own
// reconciled device_id matches the job it claims to complete.
//
// An unknown command_key is a no-op, not an error: it may be a
// retransmission for a job already resolved and no longer trackable, or
// an agent-side artifact. A job not currently RPC_SENT is treated the
// same way (duplicate/late signal, mirroring handleTransferComplete's
// own status guard) -- in particular this is what makes a second signal
// for the same job (a sync OperateResp having already resolved it, or
// vice versa) a harmless no-op rather than a double resolution: whichever
// signal arrives second finds the job already terminal (if the first
// signal resolved it) or finds no pending entry left to worry about (if
// this signal is the one resolving it first, since the loop below drops
// the pending entry unconditionally on a match).
func (d *dispatcher) handleOperationComplete(ctx context.Context, connDeviceID string, oc *usp.OperationComplete) error {
	if d.jobsRepo == nil {
		// Mirrors tryDispatch's own nil-jobsRepo guard: a dispatcher built
		// without a jobs repository (handler_test.go's newTestHandler, for
		// its DB-free identity/reconciliation tests) never has a job to
		// resolve. main.go always constructs a real *jobs.Repository, so
		// this never triggers in production.
		return nil
	}

	lookupCtx, cancel := context.WithTimeout(ctx, dbCallTimeout)
	job, err := d.jobsRepo.ByCommandKey(lookupCtx, oc.CommandKey)
	cancel()
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			d.log.Info("uspc: dispatcher: OperationComplete for unknown command_key, ignoring", "command_key", oc.CommandKey)
			return nil
		}
		return fmt.Errorf("dispatcher: look up job by command_key %s: %w", oc.CommandKey, err)
	}

	if connDeviceID == "" || connDeviceID != job.DeviceID {
		d.log.Warn("uspc: dispatcher: OperationComplete refused: reporting connection's device id does not match the job's device id",
			"command_key", oc.CommandKey, "job_id", job.ID, "job_device_id", job.DeviceID, "conn_device_id", connDeviceID)
		return nil
	}

	if job.Status != jobs.StatusRPCSent {
		d.log.Info("uspc: dispatcher: duplicate or late OperationComplete ignored", "command_key", oc.CommandKey, "job_id", job.ID, "status", job.Status)
		return nil
	}

	// A job that got both a sync OperateResp (SendResp true) and this
	// async OperationComplete must only resolve once: d.pending is keyed
	// by msg_id, but OperationComplete doesn't carry the original
	// msg_id -- only CommandKey -- so find the pending entry the other
	// way, by its job's CommandKey, and drop it. That makes a still-
	// outstanding sync response for the same job a harmless unknown-
	// msg_id no-op in handleResponse, exactly like any other stale entry.
	// No break: a requeue-and-redispatch without an intervening
	// disconnect could in principle leave two pending entries sharing one
	// CommandKey (the old dispatch's forgotten pending survives only if
	// forget was never called), so every match is removed, not just the
	// first found.
	d.mu.Lock()
	for msgID, entry := range d.pending {
		if entry.job.CommandKey == oc.CommandKey {
			delete(d.pending, msgID)
		}
	}
	d.mu.Unlock()

	resolveCtx, resolveCancel := context.WithTimeout(ctx, dbCallTimeout)
	defer resolveCancel()
	if oc.Failed {
		// oc.ErrCode/ErrMsg come from the same USP 7xxx error-code table
		// as an Error body's err_code/err_msg (design §3.3) -- a
		// CmdFailure is just async instead of sync -- so route through
		// the exact same classifyAndFail switch handleResponse uses
		// rather than duplicating it here.
		d.classifyAndFail(resolveCtx, job, "", &usp.USPError{Code: usp.ErrorCode(oc.ErrCode), Message: oc.ErrMsg})
	} else {
		d.resolveSuccess(resolveCtx, job, "", operationCompleteResultDetail(oc))
	}
	return nil
}

// forget drops every pending dispatch sent on c (fix round 1, Important
// 1). Called from handler.OnDisconnect, mirroring probe.forget's exact
// shape and rationale: without this, a job dispatched to a device that
// then disconnects (or never answers) leaks its pendingDispatch entry
// forever, and worse -- after RecoverExpiredLeases requeues the stranded
// job and a later trigger re-dispatches it under a fresh msg_id, the OLD
// entry would still be live in pending. A late/spurious response on that
// stale msg_id (e.g. a slow reply arriving after a reconnect) would then
// match it and call MarkSuccessWithDetail/MarkFailed with no status
// guard, overwriting whatever state the re-dispatch is now in. Keyed on
// the disconnecting Conn itself, not its endpoint id, for the same
// takeover-race reason probe.forget documents: a reconnect can register a
// new Conn for the same endpoint id before the old Conn's disconnect
// callback lands, and keying on endpoint id alone would let a stale
// disconnect delete the new connection's just-dispatched entry out from
// under it.
func (d *dispatcher) forget(c mtp.Conn) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for id, entry := range d.pending {
		if entry.conn == c {
			delete(d.pending, id)
		}
	}
}

// periodicSweep calls tryDispatch, every interval, for the device id of
// every live connection in registry that has already been reconciled to
// a device (GetUspAgentByEndpointID finding a row is what "reconciled"
// means here -- this file has no access to handler's own connIdentity
// map, nor does it need it). Runs until ctx is canceled -- started as a
// goroutine from main.go's run, stopped by the same shutdown context the
// transports and HTTP server already use.
func (d *dispatcher) periodicSweep(ctx context.Context, registry *mtp.Registry, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.sweepOnce(ctx, registry)
		}
	}
}

// sweepOnce is one periodicSweep pass, split out so it can be
// unit-tested without waiting on a real ticker.
func (d *dispatcher) sweepOnce(ctx context.Context, registry *mtp.Registry) {
	registry.Each(func(c mtp.Conn) {
		lookupCtx, cancel := context.WithTimeout(ctx, dbCallTimeout)
		agentRow, err := d.devicesRepo.GetUspAgentByEndpointID(lookupCtx, string(c.Endpoint()))
		cancel()
		if err != nil {
			if !errors.Is(err, devices.ErrUspAgentNotFound) {
				d.log.Warn("uspc: dispatcher: periodic sweep failed to resolve a connection's device id", "endpoint", c.Endpoint(), "mtp", c.Kind(), "error", err)
			}
			// Not (yet) reconciled -- nothing to sweep for this connection.
			return
		}
		if err := d.tryDispatch(ctx, agentRow.DeviceID); err != nil {
			d.log.Warn("uspc: dispatcher: periodic sweep failed to dispatch for device", "device_id", agentRow.DeviceID, "endpoint", c.Endpoint(), "mtp", c.Kind(), "error", err)
		}
	})
}
