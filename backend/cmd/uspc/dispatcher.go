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

// uspDispatchableTypes are the job types dispatch.go's buildUSPRequest
// knows how to render as a USP request -- the ten of internal/jobs'
// fifteen types dispatch.go's own package doc describes as "have a USP
// equivalent" (the other five are CWMP-only and never reach this list).
// FIRMWARE_DOWNLOAD is deliberately included even though buildUSPRequest
// currently always returns ErrUnsupportedOverUSP for it (the missing
// instance-selection step, see dispatch.go): it belongs on this
// leasable list so LeaseForTypes still picks it up in dispatch order
// rather than silently starving behind it, and tryDispatch's
// ErrUnsupportedOverUSP handling requeues it immediately rather than
// leaving it falsely RPC_SENT. internal/jobs itself stays protocol-
// agnostic -- this subset is cmd/uspc's own concern, not something
// internal/jobs needs to know.
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
	jobs.TypeFirmwareDownload,
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
		// The lease already consumed an attempt and left the job RPC_SENT;
		// left alone it would sit there forever since no agent will ever
		// answer an RPC that was never sent. Requeue immediately so it goes
		// back to QUEUED for a human (or a later plan that adds real
		// support) rather than falsely appearing in-flight.
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
		d.log.Warn("uspc: dispatcher: dispatched job's request was answered with an error", "job_id", entry.job.ID, "endpoint", from, "msg_id", msgID, "error", uspErr)
		if err := d.jobsRepo.MarkFailed(ctx, entry.job.ID, strconv.Itoa(int(uspErr.Code)), uspErr.Message); err != nil {
			d.log.Warn("uspc: dispatcher: failed to mark job failed", "job_id", entry.job.ID, "endpoint", from, "error", err)
		}
		return true
	}

	if err := d.jobsRepo.MarkSuccessWithDetail(ctx, entry.job.ID, responseSummary(msg)); err != nil {
		d.log.Warn("uspc: dispatcher: failed to mark job success", "job_id", entry.job.ID, "endpoint", from, "error", err)
	}
	return true
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
