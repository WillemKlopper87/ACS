package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"

	"acs/internal/store"
	"acs/internal/subscriptions"
	"acs/internal/usp"
	"acs/internal/usp/mtp"
	"acs/internal/usp/uspproto"
)

// newSubscriptionReconcilerTestRepo mirrors the DSN-skip DB-backed test
// harness used throughout this codebase (internal/subscriptions'
// own newSubscriptionsTestRepo, dispatcher_test.go's
// newDispatcherTestDB): a clean, fully migrated schema per test, skipped
// entirely when no live Postgres is configured. It seeds one devices row
// directly via SQL -- usp_subscriptions.device_id is a foreign key into
// devices, and internal/subscriptions must not (and this package need
// not) depend on internal/devices just to satisfy it.
func newSubscriptionReconcilerTestRepo(t *testing.T) (context.Context, *subscriptions.Repository, string) {
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

	deviceID := uuid.New().String()
	if _, err := db.ExecContext(ctx, `INSERT INTO devices (id, oui_serial) VALUES ($1, $2)`, deviceID, "seed-"+deviceID); err != nil {
		t.Fatalf("seed device: %v", err)
	}

	return ctx, subscriptions.NewRepository(db), deviceID
}

// subscriptionReadMsgID decodes wire (the reconciler's own read Get,
// captureConn's first sent record) and returns its msg_id, asserting it
// really is a Get(["Device.LocalAgent.Subscription."], maxDepth=1) --
// mirrors probe_test.go's own sentMsgID.
func subscriptionReadMsgID(t *testing.T, wire []byte) string {
	t.Helper()
	rec, err := usp.DecodeRecord(wire, agent)
	if err != nil {
		t.Fatalf("decode subscription read record: %v", err)
	}
	if rec.From != ctrl {
		t.Fatalf("subscription read record From = %q, want %q", rec.From, ctrl)
	}
	msg, err := usp.DecodeMsg(rec.Payload)
	if err != nil {
		t.Fatal(err)
	}
	if msg.GetHeader().GetMsgType() != uspproto.Header_GET {
		t.Fatalf("subscription reconciler sent %v, want GET", msg.GetHeader().GetMsgType())
	}
	get := msg.GetBody().GetRequest().GetGet()
	if paths := get.GetParamPaths(); len(paths) != 1 || paths[0] != subscriptionRootPath {
		t.Fatalf("subscription read Get paths = %v, want [%s]", paths, subscriptionRootPath)
	}
	if get.GetMaxDepth() != 1 {
		t.Fatalf("subscription read Get max_depth = %d, want 1", get.GetMaxDepth())
	}
	return msg.GetHeader().GetMsgId()
}

// subscriptionGetResp builds a GetResp answering subscriptionRootPath's
// Get with one resolved instance per params map given, numbered
// Device.LocalAgent.Subscription.1., .2., etc. -- mirrors probe_test.go's
// own getResp, generalized to multiple instances since a subscription
// table read can resolve any number of them.
func subscriptionGetResp(msgID string, instances ...map[string]string) *uspproto.Msg {
	resolved := make([]*uspproto.GetResp_ResolvedPathResult, 0, len(instances))
	for i, params := range instances {
		resolved = append(resolved, &uspproto.GetResp_ResolvedPathResult{
			ResolvedPath: fmt.Sprintf("Device.LocalAgent.Subscription.%d.", i+1),
			ResultParams: params,
		})
	}
	return &uspproto.Msg{
		Header: &uspproto.Header{MsgId: msgID, MsgType: uspproto.Header_GET_RESP},
		Body: &uspproto.Body{MsgBody: &uspproto.Body_Response{Response: &uspproto.Response{
			RespType: &uspproto.Response_GetResp{GetResp: &uspproto.GetResp{
				ReqPathResults: []*uspproto.GetResp_RequestedPathResult{{RequestedPath: subscriptionRootPath, ResolvedPathResults: resolved}},
			}},
		}}},
	}
}

// TestReconcileNothingToConverge covers the checklist's converged-case
// row: no desired subscriptions and no actual ones on the device must
// send nothing beyond the read itself.
func TestReconcileNothingToConverge(t *testing.T) {
	ctx, repo, deviceID := newSubscriptionReconcilerTestRepo(t)
	s := newSubscriptionReconciler(repo, ctrl, slog.Default())
	c := &captureConn{id: agent}

	if err := s.reconcile(ctx, deviceID, c); err != nil {
		t.Fatalf("reconcile() = %v, want nil", err)
	}
	if len(c.sent) != 1 {
		t.Fatalf("c.sent = %d records, want exactly 1 (the read)", len(c.sent))
	}
	msgID := subscriptionReadMsgID(t, c.sent[0])

	if matched := s.handleResponse(agent, subscriptionGetResp(msgID)); !matched {
		t.Fatal("handleResponse() = false, want true for the reconciler's own read msg_id")
	}
	if len(c.sent) != 1 {
		t.Errorf("c.sent = %d records, want still 1: nothing to converge should send nothing beyond the read", len(c.sent))
	}
	s.mu.Lock()
	pendingLen := len(s.pending)
	s.mu.Unlock()
	if pendingLen != 0 {
		t.Errorf("pending has %d entries, want none", pendingLen)
	}
}

// TestReconcileSendsAddForMissingDesired covers the checklist's
// missing-desired row: a desired subscription absent from the device's
// actual instances must produce the right Add.
func TestReconcileSendsAddForMissingDesired(t *testing.T) {
	ctx, repo, deviceID := newSubscriptionReconcilerTestRepo(t)
	sub := subscriptions.Subscription{
		ID:            uuid.New().String(),
		DeviceID:      deviceID,
		NotifType:     "ValueChange",
		ReferenceList: []string{"Device.WiFi.SSID.1.SSID"},
		Persistent:    true,
		CreatedBy:     "test",
		CreatedAt:     time.Now().UTC().Truncate(time.Microsecond),
	}
	if err := repo.Create(ctx, sub); err != nil {
		t.Fatalf("Create: %v", err)
	}

	s := newSubscriptionReconciler(repo, ctrl, slog.Default())
	c := &captureConn{id: agent}
	if err := s.reconcile(ctx, deviceID, c); err != nil {
		t.Fatalf("reconcile() = %v, want nil", err)
	}
	msgID := subscriptionReadMsgID(t, c.sent[0])

	if matched := s.handleResponse(agent, subscriptionGetResp(msgID)); !matched {
		t.Fatal("handleResponse() = false, want true")
	}
	if len(c.sent) != 2 {
		t.Fatalf("c.sent = %d records, want 2 (read + Add)", len(c.sent))
	}

	rec, err := usp.DecodeRecord(c.sent[1], agent)
	if err != nil {
		t.Fatalf("decode Add record: %v", err)
	}
	msg, err := usp.DecodeMsg(rec.Payload)
	if err != nil {
		t.Fatalf("decode Add msg: %v", err)
	}
	add := msg.GetBody().GetRequest().GetAdd()
	if add == nil {
		t.Fatal("second sent message is not an Add")
	}
	if !add.GetAllowPartial() {
		t.Error("Add allow_partial = false, want true")
	}
	if len(add.GetCreateObjs()) != 1 {
		t.Fatalf("Add create_objs = %d, want 1", len(add.GetCreateObjs()))
	}
	obj := add.GetCreateObjs()[0]
	if obj.GetObjPath() != subscriptionRootPath {
		t.Errorf("Add obj_path = %q, want %q", obj.GetObjPath(), subscriptionRootPath)
	}
	params := make(map[string]string)
	for _, p := range obj.GetParamSettings() {
		params[p.GetParam()] = p.GetValue()
		if !p.GetRequired() {
			t.Errorf("Add param %q required = false, want true", p.GetParam())
		}
	}
	if params["ID"] != sub.ID {
		t.Errorf("Add ID = %q, want %q", params["ID"], sub.ID)
	}
	if params["Enable"] != "true" {
		t.Errorf("Add Enable = %q, want %q", params["Enable"], "true")
	}
	if params["NotifType"] != sub.NotifType {
		t.Errorf("Add NotifType = %q, want %q", params["NotifType"], sub.NotifType)
	}
	wantRefs := joinReferenceList(sub.ReferenceList)
	if params["ReferenceList"] != wantRefs {
		t.Errorf("Add ReferenceList = %q, want %q", params["ReferenceList"], wantRefs)
	}

	s.mu.Lock()
	addEntry, ok := s.pending[msg.GetHeader().GetMsgId()]
	pendingLen := len(s.pending)
	s.mu.Unlock()
	if !ok {
		t.Fatalf("pending has %d entries, want one for the Add's own msg_id (awaiting its AddResp)", pendingLen)
	}
	if addEntry.kind != pendingKindAdd || addEntry.key != sub.ID {
		t.Errorf("pending entry = %+v, want kind=pendingKindAdd key=%q", addEntry, sub.ID)
	}
}

// TestReconcileSendsDeleteForUndesiredActual covers the checklist's
// undesired-actual row: an actual instance on the device with no
// matching desired row must produce the right Delete.
func TestReconcileSendsDeleteForUndesiredActual(t *testing.T) {
	ctx, repo, deviceID := newSubscriptionReconcilerTestRepo(t)
	s := newSubscriptionReconciler(repo, ctrl, slog.Default())
	c := &captureConn{id: agent}
	if err := s.reconcile(ctx, deviceID, c); err != nil {
		t.Fatalf("reconcile() = %v, want nil", err)
	}
	msgID := subscriptionReadMsgID(t, c.sent[0])

	actualID := uuid.New().String()
	resp := subscriptionGetResp(msgID, map[string]string{
		"ID":            actualID,
		"NotifType":     "ObjectCreation",
		"ReferenceList": "Device.WiFi.AccessPoint.",
	})
	if matched := s.handleResponse(agent, resp); !matched {
		t.Fatal("handleResponse() = false, want true")
	}
	if len(c.sent) != 2 {
		t.Fatalf("c.sent = %d records, want 2 (read + Delete)", len(c.sent))
	}

	rec, err := usp.DecodeRecord(c.sent[1], agent)
	if err != nil {
		t.Fatalf("decode Delete record: %v", err)
	}
	msg, err := usp.DecodeMsg(rec.Payload)
	if err != nil {
		t.Fatalf("decode Delete msg: %v", err)
	}
	del := msg.GetBody().GetRequest().GetDelete()
	if del == nil {
		t.Fatal("second sent message is not a Delete")
	}
	if !del.GetAllowPartial() {
		t.Error("Delete allow_partial = false, want true")
	}
	// The Delete must target the device's real resolved instance path
	// (subscriptionGetResp numbered this actual instance ".1."), NOT a
	// path reconstructed from the ID parameter's UUID value -- fix round
	// 1, Important 2: Device.LocalAgent.Subscription.{i}. is addressed by
	// instance number, unrelated to the ID parameter.
	wantPath := "Device.LocalAgent.Subscription.1."
	if paths := del.GetObjPaths(); len(paths) != 1 || paths[0] != wantPath {
		t.Errorf("Delete obj_paths = %v, want [%s] (the real resolved instance path, not one rebuilt from the ID parameter)", paths, wantPath)
	}

	s.mu.Lock()
	delEntry, ok := s.pending[msg.GetHeader().GetMsgId()]
	pendingLen := len(s.pending)
	s.mu.Unlock()
	if !ok {
		t.Fatalf("pending has %d entries, want one for the Delete's own msg_id (awaiting its DeleteResp)", pendingLen)
	}
	if delEntry.kind != pendingKindDelete || delEntry.key != actualID {
		t.Errorf("pending entry = %+v, want kind=pendingKindDelete key=%q", delEntry, actualID)
	}
}

// TestReconcileRecreatesOnFingerprintMismatch covers the checklist's
// fingerprint-mismatch row: a desired/actual pair sharing a Key but
// disagreeing on NotifType/ReferenceList must be recreated -- Delete
// (the stale actual) ordered before Add (the desired replacement), since
// ReferenceList is immutable on the wire.
func TestReconcileRecreatesOnFingerprintMismatch(t *testing.T) {
	ctx, repo, deviceID := newSubscriptionReconcilerTestRepo(t)
	sub := subscriptions.Subscription{
		ID:            uuid.New().String(),
		DeviceID:      deviceID,
		NotifType:     "ValueChange",
		ReferenceList: []string{"Device.WiFi.SSID.1.SSID"},
		Persistent:    true,
		CreatedBy:     "test",
		CreatedAt:     time.Now().UTC().Truncate(time.Microsecond),
	}
	if err := repo.Create(ctx, sub); err != nil {
		t.Fatalf("Create: %v", err)
	}

	s := newSubscriptionReconciler(repo, ctrl, slog.Default())
	c := &captureConn{id: agent}
	if err := s.reconcile(ctx, deviceID, c); err != nil {
		t.Fatalf("reconcile() = %v, want nil", err)
	}
	msgID := subscriptionReadMsgID(t, c.sent[0])

	// Same ID on the device, but a different NotifType than desired --
	// fingerprint mismatch, must recreate.
	resp := subscriptionGetResp(msgID, map[string]string{
		"ID":            sub.ID,
		"NotifType":     "ObjectCreation",
		"ReferenceList": "Device.WiFi.SSID.1.SSID",
	})
	if matched := s.handleResponse(agent, resp); !matched {
		t.Fatal("handleResponse() = false, want true")
	}
	if len(c.sent) != 3 {
		t.Fatalf("c.sent = %d records, want 3 (read, Delete, Add)", len(c.sent))
	}

	deleteRec, err := usp.DecodeRecord(c.sent[1], agent)
	if err != nil {
		t.Fatalf("decode second record: %v", err)
	}
	deleteMsg, err := usp.DecodeMsg(deleteRec.Payload)
	if err != nil {
		t.Fatalf("decode second msg: %v", err)
	}
	del := deleteMsg.GetBody().GetRequest().GetDelete()
	if del == nil {
		t.Fatal("second sent message must be the Delete: delete must be ordered before add on a fingerprint mismatch")
	}
	// Real resolved instance path (subscriptionGetResp numbered this
	// actual instance ".1."), not one reconstructed from sub.ID -- see
	// TestReconcileSendsDeleteForUndesiredActual's same assertion.
	wantPath := "Device.LocalAgent.Subscription.1."
	if paths := del.GetObjPaths(); len(paths) != 1 || paths[0] != wantPath {
		t.Errorf("Delete obj_paths = %v, want [%s] (the real resolved instance path, not one rebuilt from sub.ID)", paths, wantPath)
	}

	addRec, err := usp.DecodeRecord(c.sent[2], agent)
	if err != nil {
		t.Fatalf("decode third record: %v", err)
	}
	addMsg, err := usp.DecodeMsg(addRec.Payload)
	if err != nil {
		t.Fatalf("decode third msg: %v", err)
	}
	add := addMsg.GetBody().GetRequest().GetAdd()
	if add == nil {
		t.Fatal("third sent message must be the Add (the desired replacement)")
	}
	if len(add.GetCreateObjs()) != 1 || add.GetCreateObjs()[0].GetObjPath() != subscriptionRootPath {
		t.Fatalf("Add create_objs = %+v, want one object under %s", add.GetCreateObjs(), subscriptionRootPath)
	}
	params := make(map[string]string)
	for _, p := range add.GetCreateObjs()[0].GetParamSettings() {
		params[p.GetParam()] = p.GetValue()
	}
	if params["ID"] != sub.ID {
		t.Errorf("Add ID = %q, want %q", params["ID"], sub.ID)
	}
	if params["NotifType"] != sub.NotifType {
		t.Errorf("Add NotifType = %q, want the desired (new) value %q, not the stale device value", params["NotifType"], sub.NotifType)
	}
}

// TestSubscriptionFingerprintRoundTrip covers splitFingerprint/
// subscriptionFingerprint as exact inverses (fix round 1, Minor 3),
// independent of any DB-gated test: splitFingerprint(subscriptionFingerprint(notifType,
// refs)) must always recover the original notifType/refs, since sendAdd
// relies on exactly this round trip to recover a toAdd item's
// NotifType/ReferenceList (a subscriptions.Item carries nothing else).
func TestSubscriptionFingerprintRoundTrip(t *testing.T) {
	cases := []struct {
		name      string
		notifType string
		refs      []string
	}{
		{"empty refs", "ValueChange", nil},
		{"single ref", "ValueChange", []string{"Device.WiFi.SSID.1.SSID"}},
		{"multi ref", "ObjectCreation", []string{"Device.WiFi.AccessPoint.", "Device.WiFi.SSID.1.SSID"}},
		{"empty notifType", "", []string{"Device.WiFi.SSID.1.SSID"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fp := subscriptionFingerprint(tc.notifType, tc.refs)
			gotNotifType, gotRefs := splitFingerprint(fp)
			if gotNotifType != tc.notifType {
				t.Errorf("splitFingerprint(%q) notifType = %q, want %q", fp, gotNotifType, tc.notifType)
			}
			if !reflect.DeepEqual(gotRefs, tc.refs) {
				t.Errorf("splitFingerprint(%q) refs = %v, want %v", fp, gotRefs, tc.refs)
			}
		})
	}
}

// TestSubscriptionForgetRemovesPendingForConn covers fix round 1,
// Important 1: forget must drop every pending entry sent on c and leave
// every other conn's entries alone, mirroring
// TestDispatcherForgetRemovesPendingForConn. Needs no live DB (forget
// never touches s.repo), so this reuses the nil-DB Repository trick
// TestSubscriptionHandleResponseUnknownMsgID already established.
func TestSubscriptionForgetRemovesPendingForConn(t *testing.T) {
	s := newSubscriptionReconciler(subscriptions.NewRepository(nil), ctrl, slog.Default())
	c1 := &captureConn{id: agent}
	c2 := &captureConn{id: usp.EndpointID("os::other-agent")}

	s.mu.Lock()
	s.pending["msg-read"] = pendingSubscribe{conn: c1, deviceID: "device-1", kind: pendingKindRead}
	s.pending["msg-add"] = pendingSubscribe{conn: c1, deviceID: "device-1", kind: pendingKindAdd, key: "sub-1"}
	s.pending["msg-other"] = pendingSubscribe{conn: c2, deviceID: "device-2", kind: pendingKindDelete, key: "sub-2"}
	s.mu.Unlock()

	s.forget(c1)

	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.pending["msg-read"]; ok {
		t.Error("forget(c1) left msg-read's entry (sent on c1) in pending")
	}
	if _, ok := s.pending["msg-add"]; ok {
		t.Error("forget(c1) left msg-add's entry (sent on c1) in pending")
	}
	if _, ok := s.pending["msg-other"]; !ok {
		t.Error("forget(c1) removed msg-other's entry (sent on c2), want it left alone")
	}
	if len(s.pending) != 1 {
		t.Errorf("pending = %+v, want exactly the one entry sent on c2", s.pending)
	}
}

// TestSubscriptionHandleResponseUnknownMsgID covers the checklist's
// unknown-msg_id row, mirroring dispatcher_test.go's/probe_test.go's own
// versions of the same guard. Needs no live DB: it never calls
// s.repo.ByDevice, so a Repository wrapping a nil *sql.DB is enough,
// exactly like TestHandleResponseUnknownMsgID's own nil jobsRepo.
func TestSubscriptionHandleResponseUnknownMsgID(t *testing.T) {
	s := newSubscriptionReconciler(subscriptions.NewRepository(nil), ctrl, slog.Default())

	if matched := s.handleResponse(agent, subscriptionGetResp("never-sent")); matched {
		t.Error("handleResponse matched a msg_id this reconciler never sent")
	}
	// A nil / bodiless message must not panic.
	if matched := s.handleResponse(agent, nil); matched {
		t.Error("handleResponse matched a nil msg")
	}
	if matched := s.handleResponse(agent, &uspproto.Msg{Header: &uspproto.Header{MsgId: "x"}}); matched {
		t.Error("handleResponse matched an unrecognised msg_id")
	}
}

// TestReconcileSupersedesStaleInFlightOnDifferentConn covers fix round
// 2: inFlight is conn-aware (device_id -> the conn whose read pass is in
// flight), not a bare set. Scenario: connA's read goes out (inFlight[D]
// = connA) but connA dies silently -- no forget(connA) call, simulating
// the reconnect-before-disconnect takeover race forget's own doc comment
// already documents as real here (the same race mtp.Registry.Add/
// dispatcher/probe all guard against). A new connB then calls reconcile
// for the same device. Before this fix, inFlight[D] == true regardless
// of which conn set it, so connB's own reconcile call would have
// silently no-op'd and that session would get zero subscription
// reconciliation until some later trigger happened to fire one. The fix
// must instead treat connA's entry as stale and supersede it: drop
// connA's own outstanding pending read for D and proceed with a fresh
// pass on connB.
func TestReconcileSupersedesStaleInFlightOnDifferentConn(t *testing.T) {
	ctx, repo, deviceID := newSubscriptionReconcilerTestRepo(t)
	s := newSubscriptionReconciler(repo, ctrl, slog.Default())

	connA := &captureConn{id: agent}
	if err := s.reconcile(ctx, deviceID, connA); err != nil {
		t.Fatalf("reconcile(connA) = %v, want nil", err)
	}
	if len(connA.sent) != 1 {
		t.Fatalf("connA.sent = %d records, want 1 (the read Get)", len(connA.sent))
	}

	s.mu.Lock()
	inFlightConn := s.inFlight[deviceID]
	pendingLen := len(s.pending)
	s.mu.Unlock()
	if inFlightConn != mtp.Conn(connA) {
		t.Fatalf("inFlight[deviceID] = %v, want connA", inFlightConn)
	}
	if pendingLen != 1 {
		t.Fatalf("pending has %d entries, want 1 (connA's own read)", pendingLen)
	}

	// connA never disconnects (no forget call) -- it's just gone from the
	// reconciler's point of view, exactly as it would be if its read loop
	// hadn't yet noticed the drop.
	connB := &captureConn{id: usp.EndpointID("os::012345-AAAA-new")}
	if err := s.reconcile(ctx, deviceID, connB); err != nil {
		t.Fatalf("reconcile(connB) = %v, want nil", err)
	}
	if len(connB.sent) != 1 {
		t.Fatalf("connB.sent = %d records, want 1: a takeover must still send a fresh read Get on the new conn, not silently no-op because a different (dead) conn's entry was already in flight", len(connB.sent))
	}

	s.mu.Lock()
	inFlightConn = s.inFlight[deviceID]
	pendingLen = len(s.pending)
	s.mu.Unlock()
	if inFlightConn != mtp.Conn(connB) {
		t.Fatalf("inFlight[deviceID] = %v, want connB after the takeover", inFlightConn)
	}
	if pendingLen != 1 {
		t.Fatalf("pending has %d entries, want exactly 1: connA's stale read entry must have been dropped by the takeover, leaving only connB's fresh one", pendingLen)
	}
}
