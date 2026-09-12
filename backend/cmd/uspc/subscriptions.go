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
	"strings"
	"sync"

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
// convention (task-3 brief: "NotifType + "|" + strings.Join(ReferenceList,
// ",")"). joinReferenceList/splitReferenceList are this file's only place
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
// NotifType/ReferenceList) and the actual side (an agent's GetResp
// parameters, decoded via splitReferenceList) call this the same way, so
// a converged subscription always fingerprints identically no matter
// which side built it.
func subscriptionFingerprint(notifType string, refs []string) string {
	return notifType + "|" + joinReferenceList(refs)
}

// splitFingerprint parses a subscriptionFingerprint back into the
// NotifType/ReferenceList it was built from -- the exact inverse of
// subscriptionFingerprint. sendAdd uses this rather than a second lookup
// back into usp_subscriptions: a subscriptions.Item, by design
// (subscriptions.Reconcile's own contract, task-3 brief), carries nothing
// but Key and Fingerprint, so the Fingerprint itself is the only place
// the Add's NotifType/ReferenceList params can come from once toAdd has
// been computed.
func splitFingerprint(fp string) (notifType string, refs []string) {
	notifType, refList, _ := strings.Cut(fp, "|")
	return notifType, splitReferenceList(refList)
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
	}
}

// reconcile is the on-connect entry point: it loads deviceID's desired
// subscriptions from usp_subscriptions and sends a single Get to read
// conn's actual Device.LocalAgent.Subscription. instances. The diff
// itself (subscriptions.Reconcile) and the resulting Add/Delete sends
// happen later, in handleResponse, once the GetResp actually arrives --
// this call only kicks that off. One reconcile pass per connect is the
// whole contract: sends here are not retried or chained beyond this
// single round trip.
func (s *subscriptionReconciler) reconcile(ctx context.Context, deviceID string, conn mtp.Conn) error {
	subs, err := s.repo.ByDevice(ctx, deviceID)
	if err != nil {
		return fmt.Errorf("subscriptionReconciler: list desired subscriptions for device %s: %w", deviceID, err)
	}

	desired := make([]subscriptions.Item, 0, len(subs))
	for _, sub := range subs {
		desired = append(desired, subscriptions.Item{
			Key:         sub.ID,
			Fingerprint: subscriptionFingerprint(sub.NotifType, sub.ReferenceList),
		})
	}

	msgID := usp.NewMsgID()
	payload, err := usp.EncodeGet(msgID, []string{subscriptionRootPath}, 1)
	if err != nil {
		return fmt.Errorf("subscriptionReconciler: encode Get: %w", err)
	}
	record, err := usp.EncodeRecord(s.controllerID, conn.Endpoint(), payload)
	if err != nil {
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
		return fmt.Errorf("subscriptionReconciler: send Get: %w", err)
	}
	return nil
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
func (s *subscriptionReconciler) handleReadResponse(entry pendingSubscribe, msg *uspproto.Msg) {
	getResp := msg.GetBody().GetResponse().GetGetResp()
	if getResp == nil {
		s.log.Warn("uspc: subscription reconciler: response to subscription read was not a GetResp",
			"device_id", entry.deviceID, "msg_type", msg.GetHeader().GetMsgType())
		return
	}

	actual := actualSubscriptionItems(getResp)
	toAdd, toRemove := subscriptions.Reconcile(entry.desired, actual)

	for _, item := range toRemove {
		s.sendDelete(entry.conn, entry.deviceID, item.Key)
	}
	for _, item := range toAdd {
		s.sendAdd(entry.conn, entry.deviceID, item)
	}
}

// actualSubscriptionItems walks a GetResp answering
// subscriptionRootPath's Get into one subscriptions.Item per resolved
// instance -- Key is the instance's own ID parameter (the UUID a desired
// row's Create wrote onto the device, not the resolved path's own
// instance number, which is device-assigned and has no relationship to
// usp_subscriptions.id), Fingerprint built by subscriptionFingerprint
// from that instance's NotifType/ReferenceList, exactly like the desired
// side. An instance with no ID parameter is skipped rather than producing
// a Key="" item that could collide across unrelated devices/instances.
func actualSubscriptionItems(getResp *uspproto.GetResp) []subscriptions.Item {
	var actual []subscriptions.Item
	for _, reqResult := range getResp.GetReqPathResults() {
		for _, resolved := range reqResult.GetResolvedPathResults() {
			params := resolved.GetResultParams()
			id := params["ID"]
			if id == "" {
				continue
			}
			actual = append(actual, subscriptions.Item{
				Key:         id,
				Fingerprint: subscriptionFingerprint(params["NotifType"], splitReferenceList(params["ReferenceList"])),
			})
		}
	}
	return actual
}

// sendDelete encodes and sends a Delete removing one stale subscription
// instance by its wire ID (a toRemove item's Key), registering its own
// pending entry so the DeleteResp can be logged.
func (s *subscriptionReconciler) sendDelete(conn mtp.Conn, deviceID, key string) {
	msgID := usp.NewMsgID()
	payload, err := usp.EncodeDelete(msgID, true, []string{subscriptionRootPath + key + "."})
	if err != nil {
		s.log.Warn("uspc: subscription reconciler: failed to encode Delete", "device_id", deviceID, "subscription_id", key, "error", err)
		return
	}
	s.send(conn, deviceID, key, pendingKindDelete, msgID, payload)
}

// sendAdd encodes and sends an Add creating one missing/stale
// subscription instance under its desired wire ID (a toAdd item's Key),
// recovering the NotifType/ReferenceList that item's Fingerprint was
// built from (splitFingerprint) since a subscriptions.Item itself carries
// nothing else. Registers its own pending entry so the AddResp can be
// logged.
func (s *subscriptionReconciler) sendAdd(conn mtp.Conn, deviceID string, item subscriptions.Item) {
	notifType, refs := splitFingerprint(item.Fingerprint)
	msgID := usp.NewMsgID()
	payload, err := usp.EncodeAdd(msgID, true, subscriptionRootPath, map[string]string{
		"ID":            item.Key,
		"Enable":        "true",
		"NotifType":     notifType,
		"ReferenceList": joinReferenceList(refs),
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
