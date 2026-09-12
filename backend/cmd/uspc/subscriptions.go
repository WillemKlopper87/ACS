// subscriptions.go is the USP-specific subscription reconciler: on
// connect, it reads a device's actual Device.LocalAgent.Subscription.
// instances, diffs them against usp_subscriptions (internal/subscriptions,
// Task 3's desired-state table) via the generic Reconcile engine, and
// issues Delete-then-Add over USP to converge. It mirrors dispatcher.go's
// own pending-map-plus-mutex, msg_id-correlated request/response pattern
// (Task 4) -- but is a deliberately separate, smaller type: subscription
// reconciliation isn't a job, it has no command_key, no jobs.Job, no
// MarkSuccess/MarkFailed, so routing it through dispatcher would only add
// job-queue machinery this problem doesn't have.
//
// ReferenceList is immutable on the wire (task-3 brief): there is no USP
// primitive to alter a live subscription's watched paths in place, so a
// subscription whose desired NotifType/ReferenceList has changed since it
// was last converged is always recreated -- deleted, then re-added under
// the same ID -- never Set.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"

	"github.com/google/uuid"

	"acs/internal/subscriptions"
	"acs/internal/usp"
	"acs/internal/usp/mtp"
	"acs/internal/usp/uspproto"
)

// subscriptionRootPath is the TR-369 Device.LocalAgent.Subscription.
// table. A single maxDepth=1 Get against it returns every existing
// subscription instance's parameters (ID, NotifType, ReferenceList, ...)
// in one round trip -- probe.go's own Device.DeviceInfo. pattern,
// generalized from a single-instance object to a multi-instance table
// (task-5 brief, Correction 2: EncodeGet, not the brief's original
// two-step EncodeGetInstances-then-per-instance-EncodeGet).
const subscriptionRootPath = "Device.LocalAgent.Subscription."

// referenceListSeparator is the wire encoding of
// Device.LocalAgent.Subscription.{i}.ReferenceList: a comma-joined list
// of paths, matching internal/subscriptions.Item's own Fingerprint
// convention (task-3 brief's original "NotifType + "|" +
// strings.Join(ReferenceList, ",")", extended by final-review finding 3
// to also encode Persistent -- see subscriptionFingerprint's own doc
// comment for the current exact shape). joinReferenceList/
// splitReferenceList are this file's only place the ReferenceList half of
// that convention is encoded/decoded, and are exact inverses of each
// other (task-5 brief's binding requirement), so a desired row's
// Fingerprint and an actual instance's Fingerprint are built the same way
// regardless of which side (a []string from Postgres, or a comma string
// from the wire) started it.
const referenceListSeparator = ","

// joinReferenceList renders refs the way both a desired Subscription row
// and an Add request's ReferenceList parameter encode a path list.
func joinReferenceList(refs []string) string {
	return strings.Join(refs, referenceListSeparator)
}

// splitReferenceList parses a wire ReferenceList parameter value back
// into the []string form subscriptionFingerprint expects -- the exact
// inverse of joinReferenceList. An empty string means no referenced
// paths, not a one-element slice containing "".
func splitReferenceList(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, referenceListSeparator)
}

// subscriptionFingerprint builds the Fingerprint subscriptions.Reconcile
// compares. Both the desired side (a usp_subscriptions row's own
// NotifType/Persistent/ReferenceList) and the actual side (an agent's
// GetResp parameters, decoded via splitReferenceList) call this the same
// way, so a converged subscription always fingerprints identically no
// matter which side built it.
//
// Persistent is included (final-review finding 3): it is a real column on
// usp_subscriptions and a real wire parameter this controller both sends
// on Add and can read back from a device's GetResp, so a subscription
// that differs only in Persistent must fingerprint differently -- a
// desired/actual pair agreeing on NotifType/ReferenceList but disagreeing
// on Persistent is real drift, and Reconcile can only see that if
// Persistent is part of what it compares.
func subscriptionFingerprint(notifType string, persistent bool, refs []string) string {
	return notifType + "|" + strconv.FormatBool(persistent) + "|" + joinReferenceList(refs)
}

// splitFingerprint parses a subscriptionFingerprint back into the
// NotifType/Persistent/ReferenceList it was built from -- the exact
// inverse of subscriptionFingerprint. sendAdd uses this rather than a
// second lookup back into usp_subscriptions: a subscriptions.Item, by
// design (subscriptions.Reconcile's own contract, task-3 brief), carries
// nothing but Key and Fingerprint, so the Fingerprint itself is the only
// place the Add's NotifType/Persistent/ReferenceList params can come from
// once toAdd has been computed. An unparseable persistent segment (should
// be unreachable -- every fingerprint in this file is built by
// subscriptionFingerprint itself, which always writes strconv.FormatBool's
// output) decodes as false rather than panicking, matching
// strconv.ParseBool's own zero-value-on-error convention.
func splitFingerprint(fp string) (notifType string, persistent bool, refs []string) {
	notifType, rest, _ := strings.Cut(fp, "|")
	persistentStr, refList, _ := strings.Cut(rest, "|")
	persistent, _ = strconv.ParseBool(persistentStr)
	return notifType, persistent, splitReferenceList(refList)
}

// pendingKind distinguishes what a subscriptionReconciler's outstanding
// request was for, so handleResponse knows how to interpret its answer.
// All three kinds share one pending map/msg_id space (rather than three
// separate ones) because they are all steps of the same reconcile pass:
// the read kicks off the diff, and the diff's own toAdd/toRemove results
// are what produce the add/delete kinds.
type pendingKind int

const (
	// pendingKindRead is the initial Get(["Device.LocalAgent.Subscription."])
	// that discovers actual instances.
	pendingKindRead pendingKind = iota
	// pendingKindAdd is a follow-up Add for one desired item missing (or
	// stale) on the device.
	pendingKindAdd
	// pendingKindDelete is a follow-up Delete for one actual instance not
	// (or no longer) desired.
	pendingKindDelete
)

// pendingSubscribe is what reconcile/sendAdd/sendDelete remember about
// one outstanding request, keyed by its msg_id -- this reconciler's own
// version of dispatcher.pendingDispatch/probe.pendingProbe, carrying
// whatever each of the three pendingKinds needs to interpret its own
// response: conn and deviceID for every kind (routing and logging);
// desired only for pendingKindRead, since that is what the diff against
// the device's GetResp answer needs; key (the subscription id) only for
// pendingKindAdd/pendingKindDelete, purely for logging the outcome.
type pendingSubscribe struct {
	conn     mtp.Conn
	deviceID string
	kind     pendingKind
	desired  []subscriptions.Item
	key      string
}

// subscriptionReconciler owns one reconcile pass's outstanding requests,
// keyed by msg_id. Mirrors dispatcher's own fields/mutex placement and
// nil-log-default constructor idiom.
type subscriptionReconciler struct {
	repo         *subscriptions.Repository
	controllerID usp.EndpointID
	log          *slog.Logger

	mu      sync.Mutex
	pending map[string]pendingSubscribe
	// inFlight maps a device id with an outstanding read (a Get sent, its
	// GetResp not yet answered) to the conn that read was sent on -- fix
	// round 1, Important 1. handler.checkSubscriptionID calls reconcile
	// once per Notify whose subscription_id doesn't match a known
	// usp_subscriptions row, and an agent that streams such Notifies (or
	// that simply never answers the read) would otherwise grow s.pending
	// and re-send the read Get without bound, throttling that
	// connection's own read loop with a ByDevice query plus a Get send
	// per Notify. reconcile checks/sets this before doing any of that
	// work and no-ops if the SAME conn already has a pass in flight for
	// that device, so repeated triggers on one connection collapse into
	// the one pass already running. Cleared once that pass's own read
	// response is processed (handleResponse, for every outcome --
	// success, an error response, or a malformed one) or, if the
	// connection dies before any response arrives, by forget.
	//
	// Keyed by conn, not just a bare set (fix round 2): a device id alone
	// can't distinguish "this same connection's pass is still running"
	// from "a DIFFERENT, now-dead connection's pass never got cleaned up"
	// -- the exact reconnect-before-disconnect takeover race forget's own
	// doc comment already documents as real here (mirroring
	// mtp.Registry.Add/dispatcher's own takeover handling). Without the
	// conn identity, a new connection for a device whose old connection
	// died mid-reconcile without ever calling forget would see
	// "in flight" forever and never get its own reconcile pass triggered.
	// reconcile treats a different conn's in-flight entry as stale and
	// supersedes it (see reconcile's own doc comment) rather than
	// blocking on it.
	inFlight map[string]mtp.Conn
}

// newSubscriptionReconciler returns a subscriptionReconciler ready for
// concurrent use. log defaults to slog.Default() when nil, matching this
// package's other constructors (newProbe, newDispatcher).
func newSubscriptionReconciler(repo *subscriptions.Repository, controllerID usp.EndpointID, log *slog.Logger) *subscriptionReconciler {
	if log == nil {
		log = slog.Default()
	}
	return &subscriptionReconciler{
		repo:         repo,
		controllerID: controllerID,
		log:          log,
		pending:      make(map[string]pendingSubscribe),
		inFlight:     make(map[string]mtp.Conn),
	}
}

// reconcileInFlight reports whether conn already has an outstanding read
// in flight for deviceID (see inFlight's own doc comment) --
// handler.checkSubscriptionID uses this to decide whether its own
// "triggering subscription reconciliation" log line would be accurate
// before calling reconcile, so a stream of unknown-subscription-id
// Notifies on one connection for one device logs once, not once per
// Notify. Deliberately conn-scoped, not "does deviceID have any entry at
// all" (fix round 2): a stale entry left by a different, now-dead conn
// is exactly what reconcile itself is about to supersede, not something
// this peek should report as "already handled".
func (s *subscriptionReconciler) reconcileInFlight(deviceID string, conn mtp.Conn) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.inFlight[deviceID] == conn
}

// reconcile loads deviceID's desired subscriptions from usp_subscriptions
// and sends a single Get to read conn's actual
// Device.LocalAgent.Subscription. instances -- the on-connect trigger
// (resolveAndMarkReconciled) and the unknown-subscription-id trigger
// (handler.checkSubscriptionID) both call this the same way. The diff
// itself (subscriptions.Reconcile) and the resulting Add/Delete sends
// happen later, in handleResponse, once the GetResp actually arrives --
// this call only kicks that off.
//
// deviceID's inFlight entry (see its own doc comment) debounces repeated
// calls: if conn already has a read outstanding for deviceID, this is a
// silent no-op (nil error) -- one pass already running satisfies the
// request without a second Get/pending entry (fix round 1, Important 1).
// If a DIFFERENT conn's entry is outstanding instead, that conn is
// treated as stale/superseded (fix round 2): its own outstanding
// pendingKindRead entry for deviceID is dropped from s.pending (it
// belongs to a connection on its way out, and would otherwise leak until
// some future forget/response that may never come -- see this file's own
// reconnect-before-disconnect takeover discussion on forget), and a
// fresh pass proceeds for conn. Without this, a connection that dies
// mid-reconcile without ever calling forget (the same takeover race
// mtp.Registry.Add/dispatcher/probe already guard against elsewhere)
// would permanently block subscription reconciliation for any later
// connection for that same device.
func (s *subscriptionReconciler) reconcile(ctx context.Context, deviceID string, conn mtp.Conn) error {
	s.mu.Lock()
	if existing, ok := s.inFlight[deviceID]; ok {
		if existing == conn {
			s.mu.Unlock()
			return nil
		}
		// Takeover: existing is a different (presumably dead) conn.
		// Drop its stale outstanding read so it can't act on a late
		// GetResp naming a conn/desired-set pair this fresh pass is about
		// to replace, then fall through to start conn's own pass below.
		for id, entry := range s.pending {
			if entry.kind == pendingKindRead && entry.deviceID == deviceID && entry.conn == existing {
				delete(s.pending, id)
			}
		}
	}
	s.inFlight[deviceID] = conn
	s.mu.Unlock()

	subs, err := s.repo.ByDevice(ctx, deviceID)
	if err != nil {
		s.clearInFlightIfOwnedBy(deviceID, conn)
		return fmt.Errorf("subscriptionReconciler: list desired subscriptions for device %s: %w", deviceID, err)
	}

	desired := make([]subscriptions.Item, 0, len(subs))
	for _, sub := range subs {
		desired = append(desired, subscriptions.Item{
			Key:         sub.ID,
			Fingerprint: subscriptionFingerprint(sub.NotifType, sub.Persistent, sub.ReferenceList),
		})
	}

	msgID := usp.NewMsgID()
	payload, err := usp.EncodeGet(msgID, []string{subscriptionRootPath}, 1)
	if err != nil {
		s.clearInFlightIfOwnedBy(deviceID, conn)
		return fmt.Errorf("subscriptionReconciler: encode Get: %w", err)
	}
	record, err := usp.EncodeRecord(s.controllerID, conn.Endpoint(), payload)
	if err != nil {
		s.clearInFlightIfOwnedBy(deviceID, conn)
		return fmt.Errorf("subscriptionReconciler: encode record: %w", err)
	}

	s.mu.Lock()
	s.pending[msgID] = pendingSubscribe{conn: conn, deviceID: deviceID, kind: pendingKindRead, desired: desired}
	s.mu.Unlock()

	sendCtx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()
	if err := conn.Send(sendCtx, record); err != nil {
		s.mu.Lock()
		delete(s.pending, msgID)
		s.mu.Unlock()
		s.clearInFlightIfOwnedBy(deviceID, conn)
		return fmt.Errorf("subscriptionReconciler: send Get: %w", err)
	}
	return nil
}

// clearInFlightIfOwnedBy clears deviceID's inFlight entry only if it is
// still owned by conn -- used on every one of reconcile's own failure
// paths. The owner check (rather than an unconditional delete) matters
// because a concurrent reconcile call for the same deviceID from a
// DIFFERENT conn could have already superseded this entry (fix round
// 2's takeover handling) between this call's own inFlight assignment and
// the point where it hit an error; an unconditional delete here would
// incorrectly clear that newer conn's own in-flight marker out from
// under it.
func (s *subscriptionReconciler) clearInFlightIfOwnedBy(deviceID string, conn mtp.Conn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.inFlight[deviceID] == conn {
		delete(s.inFlight, deviceID)
	}
}

// handleResponse reports whether msg answers one of this reconciler's
// outstanding requests (by msg_id), dispatching to the handling that
// matches the entry's pendingKind. Tolerates a nil msg/header or an
// unrecognised msg_id without panicking, mirroring
// dispatcher.handleResponse/probe.handle -- this is called on every
// inbound message the rest of handler.OnRecord's fallthrough chain
// didn't already claim.
func (s *subscriptionReconciler) handleResponse(from usp.EndpointID, msg *uspproto.Msg) (matched bool) {
	if msg == nil || msg.GetHeader() == nil {
		return false
	}

	msgID := msg.GetHeader().GetMsgId()
	s.mu.Lock()
	entry, ok := s.pending[msgID]
	if ok {
		delete(s.pending, msgID)
		if entry.kind == pendingKindRead && s.inFlight[entry.deviceID] == entry.conn {
			// This device's read has now been answered (whatever the
			// outcome), so it no longer counts as in flight -- clears the
			// debounce reconcile's own inFlight guard set, letting a
			// future reconcile call for this device actually send a new
			// Get rather than silently no-op forever. The owner check is
			// defensive: by construction (reconcile's own takeover
			// handling always removes a superseded conn's pending entry
			// first) entry's conn is always inFlight's current owner by
			// the time this runs, but checking rather than assuming keeps
			// that invariant local to this line instead of implicit.
			delete(s.inFlight, entry.deviceID)
		}
	}
	s.mu.Unlock()
	if !ok {
		return false
	}

	if uspErr := usp.ErrorFromMsg(msg); uspErr != nil {
		s.log.Warn("uspc: subscription reconciler: request answered with an error",
			"endpoint", from, "device_id", entry.deviceID, "kind", entry.kind, "subscription_id", entry.key, "msg_id", msgID, "error", uspErr)
		return true
	}

	switch entry.kind {
	case pendingKindRead:
		s.handleReadResponse(entry, msg)
	case pendingKindAdd:
		s.logAddResult(entry, msg)
	case pendingKindDelete:
		s.logDeleteResult(entry, msg)
	}
	return true
}

// handleReadResponse is handleResponse's pendingKindRead branch: it
// parses actual out of the GetResp, diffs it against entry's own desired
// (computed back in reconcile), and issues Delete for every toRemove
// before Add for every toAdd -- delete-before-add, since ReferenceList is
// immutable on the wire and a changed subscription is always recreated
// under the same ID, never Set (see this file's own package doc comment).
//
// Before any of that, every one of the GetResp's own per-requested-path
// results is checked for a non-zero ErrCode (final-review finding 2):
// usp.ErrorFromMsg (handleResponse's own earlier check) only catches a
// top-level Body_Error, not a device refusing this specific path (e.g.
// USP error 7006 permission denied, or 7016/7026) while still returning
// a structurally well-formed GetResp with err_code set and zero
// resolved_path_results. Reading that as "the device has zero actual
// subscriptions" would make every desired subscription look missing and
// re-Add it on every single connect, silently, forever -- so any per-path
// error abandons this reconcile pass entirely: no Add, no Delete, just a
// Warn. Mirrors dispatcher.go's classifyGetResp, which already does this
// same per-path check for the job-dispatch path. inFlight/pending state
// for this device is already cleared by handleResponse before this is
// called (on lookup, unconditionally on outcome), so abandoning here
// leaks nothing -- a future trigger (the next connect, or the next
// unknown-subscription_id Notify) gets a fresh pass.
func (s *subscriptionReconciler) handleReadResponse(entry pendingSubscribe, msg *uspproto.Msg) {
	getResp := msg.GetBody().GetResponse().GetGetResp()
	if getResp == nil {
		s.log.Warn("uspc: subscription reconciler: response to subscription read was not a GetResp",
			"device_id", entry.deviceID, "msg_type", msg.GetHeader().GetMsgType())
		return
	}

	for _, reqResult := range getResp.GetReqPathResults() {
		if reqResult.GetErrCode() != 0 {
			s.log.Warn("uspc: subscription reconciler: device refused the subscription read, abandoning this reconcile pass",
				"device_id", entry.deviceID, "requested_path", reqResult.GetRequestedPath(), "err_code", reqResult.GetErrCode(), "err_msg", reqResult.GetErrMsg())
			return
		}
	}

	actual, resolvedPaths := actualSubscriptionItems(getResp, s.log, entry.deviceID)
	toAdd, toRemove := subscriptions.Reconcile(entry.desired, actual)

	for _, item := range toRemove {
		objPath, ok := resolvedPaths[item.Key]
		if !ok {
			// Defensive only: every toRemove item came from actual, and
			// actualSubscriptionItems populates resolvedPaths with exactly
			// actual's own Keys, so this should be unreachable.
			s.log.Warn("uspc: subscription reconciler: no resolved path recorded for a toRemove item, skipping its Delete",
				"device_id", entry.deviceID, "subscription_id", item.Key)
			continue
		}
		s.sendDelete(entry.conn, entry.deviceID, item.Key, objPath)
	}
	for _, item := range toAdd {
		s.sendAdd(entry.conn, entry.deviceID, item)
	}
}

// actualSubscriptionItems walks a GetResp answering
// subscriptionRootPath's Get into one subscriptions.Item per resolved
// instance this controller owns -- Key is the instance's own ID
// parameter (the UUID a desired row's Create wrote onto the device, not
// the resolved path's own instance number, which is device-assigned and
// has no relationship to usp_subscriptions.id), Fingerprint built by
// subscriptionFingerprint from that instance's own
// NotifType/Persistent/ReferenceList, exactly like the desired side.
//
// Ownership rule (final-review finding 4): this controller only
// reconciles Device.LocalAgent.Subscription. instances whose ID
// parameter is a UUID matching its own creation convention -- sendAdd
// always writes a usp_subscriptions.id (migration 0055's own UUID
// convention) verbatim into ID. TR-369 explicitly permits multiple
// controllers on one agent, and subscriptions.Reconcile treats any actual
// key absent from desired as toRemove; ingesting an instance this
// controller didn't create (another controller's own subscription, or a
// factory-preprovisioned one) would make the reconciler issue a Delete
// for it. So an instance whose ID does not parse as a UUID (uuid.Parse) --
// including an empty ID -- is assumed to belong to another controller or
// the factory default, and is skipped entirely: never added to actual,
// never deleted, never logged as an orphan (a missing/malformed ID on one
// of OUR OWN subscriptions is a separate, already-ledgered concern this
// is not).
//
// resolvedPaths maps each returned item's Key back to the actual resolved
// object path the device reported it under (e.g.
// "Device.LocalAgent.Subscription.1."), which is NOT derivable from Key
// alone: Device.LocalAgent.Subscription.{i}. is addressed by the device's
// own instance number, completely unrelated to the ID parameter's UUID
// value. A Delete must target that real resolved path -- reconstructing
// "Device.LocalAgent.Subscription."+Key+"." instead (fix round 1,
// Important 2) builds a path no conforming agent actually has, since Key
// is a parameter value, not an instance number, and the agent rejects it.
func actualSubscriptionItems(getResp *uspproto.GetResp, log *slog.Logger, deviceID string) (items []subscriptions.Item, resolvedPaths map[string]string) {
	resolvedPaths = make(map[string]string)
	for _, reqResult := range getResp.GetReqPathResults() {
		for _, resolved := range reqResult.GetResolvedPathResults() {
			params := resolved.GetResultParams()
			id := params["ID"]
			if _, err := uuid.Parse(id); err != nil {
				// Not ours -- empty, or shaped by some other controller or
				// the factory default. Skip without touching it (see this
				// function's own doc comment for why), but still log at
				// Debug (fix round 2 piggyback): correct behavior is silent
				// as far as reconcile's own Add/Delete decisions go, but
				// without this a non-UUID ID on one of OUR OWN subscriptions
				// -- which should be unreachable, since sendAdd always
				// writes a usp_subscriptions.id verbatim -- would silently
				// re-Add it forever on every connect with no log line ever
				// explaining why.
				log.Debug("uspc: subscription reconciler: skipping non-UUID subscription instance, not owned by this controller",
					"device_id", deviceID, "id", id, "resolved_path", resolved.GetResolvedPath())
				continue
			}
			persistent, _ := strconv.ParseBool(params["Persistent"])
			items = append(items, subscriptions.Item{
				Key:         id,
				Fingerprint: subscriptionFingerprint(params["NotifType"], persistent, splitReferenceList(params["ReferenceList"])),
			})
			resolvedPaths[id] = resolved.GetResolvedPath()
		}
	}
	return items, resolvedPaths
}

// sendDelete encodes and sends a Delete removing one stale subscription
// instance at its real resolved object path (objPath, from
// actualSubscriptionItems' own resolvedPaths -- see that function's doc
// comment for why this can't be reconstructed from key alone), registering
// its own pending entry so the DeleteResp can be logged. key is carried
// through only for logging.
func (s *subscriptionReconciler) sendDelete(conn mtp.Conn, deviceID, key, objPath string) {
	msgID := usp.NewMsgID()
	payload, err := usp.EncodeDelete(msgID, true, []string{objPath})
	if err != nil {
		s.log.Warn("uspc: subscription reconciler: failed to encode Delete", "device_id", deviceID, "subscription_id", key, "obj_path", objPath, "error", err)
		return
	}
	s.send(conn, deviceID, key, pendingKindDelete, msgID, payload)
}

// sendAdd encodes and sends an Add creating one missing/stale
// subscription instance under its desired wire ID (a toAdd item's Key),
// recovering the NotifType/Persistent/ReferenceList that item's
// Fingerprint was built from (splitFingerprint) since a
// subscriptions.Item itself carries nothing else. Registers its own
// pending entry so the AddResp can be logged.
//
// Persistent is sent as a wire param (final-review finding 3), formatted
// the same way Enable's boolean is: the literal strconv.FormatBool
// string. Before this fix, Persistent was stored on usp_subscriptions and
// used nowhere else, so a subscription's persistence was never actually
// applied to the device.
func (s *subscriptionReconciler) sendAdd(conn mtp.Conn, deviceID string, item subscriptions.Item) {
	notifType, persistent, refs := splitFingerprint(item.Fingerprint)
	msgID := usp.NewMsgID()
	payload, err := usp.EncodeAdd(msgID, true, subscriptionRootPath, map[string]string{
		"ID":            item.Key,
		"Enable":        "true",
		"NotifType":     notifType,
		"ReferenceList": joinReferenceList(refs),
		"Persistent":    strconv.FormatBool(persistent),
	})
	if err != nil {
		s.log.Warn("uspc: subscription reconciler: failed to encode Add", "device_id", deviceID, "subscription_id", item.Key, "error", err)
		return
	}
	s.send(conn, deviceID, item.Key, pendingKindAdd, msgID, payload)
}

// send is sendAdd/sendDelete's shared encode-record/register-pending/send
// tail, bounded by sendTimeout against a fresh background context --
// handleReadResponse (send's only caller's caller) runs inline off of
// handleResponse, which has no context of the original reconcile call's
// own to inherit from (that call's ctx, and its dbCallTimeout budget, is
// long gone by the time a real device answers), the same reasoning
// dispatcher.retryDispatch's own doc comment gives for its own fresh
// context.Background().
func (s *subscriptionReconciler) send(conn mtp.Conn, deviceID, key string, kind pendingKind, msgID string, payload []byte) {
	record, err := usp.EncodeRecord(s.controllerID, conn.Endpoint(), payload)
	if err != nil {
		s.log.Warn("uspc: subscription reconciler: failed to encode record", "device_id", deviceID, "subscription_id", key, "kind", kind, "error", err)
		return
	}

	s.mu.Lock()
	s.pending[msgID] = pendingSubscribe{conn: conn, deviceID: deviceID, kind: kind, key: key}
	s.mu.Unlock()

	sendCtx, cancel := context.WithTimeout(context.Background(), sendTimeout)
	defer cancel()
	if err := conn.Send(sendCtx, record); err != nil {
		s.mu.Lock()
		delete(s.pending, msgID)
		s.mu.Unlock()
		s.log.Warn("uspc: subscription reconciler: failed to send", "device_id", deviceID, "subscription_id", key, "kind", kind, "error", err)
	}
}

// logAddResult is handleResponse's pendingKindAdd branch: it logs
// whether the device accepted or rejected the Add, per created-object
// result (allowPartial=true means a single Add could in principle answer
// for more than one requested object, though sendAdd only ever requests
// one).
func (s *subscriptionReconciler) logAddResult(entry pendingSubscribe, msg *uspproto.Msg) {
	addResp := msg.GetBody().GetResponse().GetAddResp()
	if addResp == nil {
		s.log.Warn("uspc: subscription reconciler: response to Add was not an AddResp",
			"device_id", entry.deviceID, "subscription_id", entry.key, "msg_type", msg.GetHeader().GetMsgType())
		return
	}
	for _, r := range addResp.GetCreatedObjResults() {
		status := r.GetOperStatus()
		if fail := status.GetOperFailure(); fail != nil {
			s.log.Warn("uspc: subscription reconciler: Add failed",
				"device_id", entry.deviceID, "subscription_id", entry.key, "err_code", fail.GetErrCode(), "err_msg", fail.GetErrMsg())
			continue
		}
		if succ := status.GetOperSuccess(); succ != nil {
			s.log.Info("uspc: subscription reconciler: Add succeeded",
				"device_id", entry.deviceID, "subscription_id", entry.key, "instantiated_path", succ.GetInstantiatedPath())
		}
	}
}

// forget drops every pending request sent on c (fix round 1, Important
// 1). Called from Handler.OnDisconnect (Task 6 wires the call site; this
// method itself is unused, and compiles clean unused, until then) --
// mirrors dispatcher.forget's/probe.forget's exact shape and rationale.
// Without this, an agent that disconnects mid-reconcile leaks its
// pendingSubscribe entries forever, and a late response arriving after
// disconnect could act on a stale conn.
//
// Keyed on the disconnecting Conn itself, not its endpoint id, for the
// same takeover-race reason dispatcher.forget/probe.forget document: a
// reconnect can register a new Conn for the same endpoint id before the
// old Conn's disconnect callback lands, and keying on endpoint id alone
// would let a stale disconnect delete the new connection's just-sent
// entries out from under it.
//
// Also clears inFlight for any device whose read (pendingKindRead) was
// still outstanding on c (task-6 fix round 1, Important 1): without
// this, a connection that dies between reconcile's own Get send and its
// GetResp would leave that device's inFlight entry set forever -- no
// response will ever arrive to clear it via handleResponse -- and every
// future reconcile call for that device (including a fresh connect's
// own on-connect trigger) would silently no-op as "already in flight"
// for a pass that in fact died with the connection.
func (s *subscriptionReconciler) forget(c mtp.Conn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, entry := range s.pending {
		if entry.conn == c {
			delete(s.pending, id)
			if entry.kind == pendingKindRead && s.inFlight[entry.deviceID] == c {
				delete(s.inFlight, entry.deviceID)
			}
		}
	}
}

// logDeleteResult is handleResponse's pendingKindDelete branch,
// logAddResult's Delete counterpart.
func (s *subscriptionReconciler) logDeleteResult(entry pendingSubscribe, msg *uspproto.Msg) {
	deleteResp := msg.GetBody().GetResponse().GetDeleteResp()
	if deleteResp == nil {
		s.log.Warn("uspc: subscription reconciler: response to Delete was not a DeleteResp",
			"device_id", entry.deviceID, "subscription_id", entry.key, "msg_type", msg.GetHeader().GetMsgType())
		return
	}
	for _, r := range deleteResp.GetDeletedObjResults() {
		status := r.GetOperStatus()
		if fail := status.GetOperFailure(); fail != nil {
			s.log.Warn("uspc: subscription reconciler: Delete failed",
				"device_id", entry.deviceID, "subscription_id", entry.key, "err_code", fail.GetErrCode(), "err_msg", fail.GetErrMsg())
			continue
		}
		if succ := status.GetOperSuccess(); succ != nil {
			s.log.Info("uspc: subscription reconciler: Delete succeeded",
				"device_id", entry.deviceID, "subscription_id", entry.key, "affected_paths", succ.GetAffectedPaths())
		}
	}
}
