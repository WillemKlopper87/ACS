package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"testing"

	"acs/internal/devices"
	"acs/internal/usp"
)

// onboardCall and linkCall record one call each to fakeIdentityStore's
// UpsertFromOnBoard and LinkUspAgent, so tests can assert on exactly
// what the reconciler passed through.
type onboardCall struct{ OUI, ProductClass, SerialNumber string }
type linkCall struct{ DeviceID, EndpointID, MTPKind string }

// fakeIdentityStore is an in-memory identityStore recording every call
// it receives, used by identity_test.go and handler_test.go to test the
// reconciler and handler without a database -- matching the plan's
// stated intent to keep cmd/uspc's own tests fast and DB-free.
//
// It approximates enough of *devices.Repository's real behaviour
// (devices keyed by their oui/productClass/serialNumber natural key,
// usp_agents keyed by endpoint id, resolvable by endpoint id after a
// link) for handler_test.go to exercise the endpoint-id lookup handler
// performs after a successful reconciliation.
type fakeIdentityStore struct {
	mu sync.Mutex

	devicesByKey map[string]*devices.Device
	nextDeviceID int

	agentsByEndpointID map[string]*devices.UspAgent

	upsertCalls     []onboardCall
	linkCalls       []linkCall
	disconnectCalls []string
	getCalls        []string

	upsertErr     error
	linkErr       error
	disconnectErr error
	getErr        error
}

var _ identityStore = (*fakeIdentityStore)(nil)

func newFakeIdentityStore() *fakeIdentityStore {
	return &fakeIdentityStore{
		devicesByKey:       make(map[string]*devices.Device),
		agentsByEndpointID: make(map[string]*devices.UspAgent),
	}
}

func (f *fakeIdentityStore) UpsertFromOnBoard(_ context.Context, oui, productClass, serialNumber string) (*devices.Device, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.upsertCalls = append(f.upsertCalls, onboardCall{oui, productClass, serialNumber})
	if f.upsertErr != nil {
		return nil, f.upsertErr
	}
	key := oui + "|" + productClass + "|" + serialNumber
	if d, ok := f.devicesByKey[key]; ok {
		return d, nil
	}
	f.nextDeviceID++
	d := &devices.Device{
		ID:           fmt.Sprintf("device-%d", f.nextDeviceID),
		OUI:          oui,
		ProductClass: productClass,
		SerialNumber: serialNumber,
	}
	f.devicesByKey[key] = d
	return d, nil
}

func (f *fakeIdentityStore) LinkUspAgent(_ context.Context, deviceID, endpointID, mtpKind string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.linkCalls = append(f.linkCalls, linkCall{deviceID, endpointID, mtpKind})
	if f.linkErr != nil {
		return f.linkErr
	}
	f.agentsByEndpointID[endpointID] = &devices.UspAgent{
		DeviceID:   deviceID,
		EndpointID: endpointID,
		MTPKind:    mtpKind,
		Connected:  true,
	}
	return nil
}

func (f *fakeIdentityStore) MarkUspAgentDisconnected(_ context.Context, deviceID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.disconnectCalls = append(f.disconnectCalls, deviceID)
	if f.disconnectErr != nil {
		return f.disconnectErr
	}
	for _, agent := range f.agentsByEndpointID {
		if agent.DeviceID == deviceID {
			agent.Connected = false
		}
	}
	return nil
}

func (f *fakeIdentityStore) GetUspAgentByEndpointID(_ context.Context, endpointID string) (*devices.UspAgent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.getCalls = append(f.getCalls, endpointID)
	if f.getErr != nil {
		return nil, f.getErr
	}
	agent, ok := f.agentsByEndpointID[endpointID]
	if !ok {
		return nil, devices.ErrUspAgentNotFound
	}
	return agent, nil
}

func TestReconcilerOnBoard(t *testing.T) {
	store := newFakeIdentityStore()
	r := newReconciler(store, slog.Default())
	c := &captureConn{id: agent}
	ob := &usp.OnBoardRequest{OUI: "0025C2", ProductClass: "Gateway", SerialNumber: "SN12345"}

	if err := r.onBoard(context.Background(), c, ob); err != nil {
		t.Fatalf("onBoard() = %v, want nil", err)
	}

	if len(store.upsertCalls) != 1 || store.upsertCalls[0] != (onboardCall{"0025C2", "Gateway", "SN12345"}) {
		t.Fatalf("upsertCalls = %+v, want one call with the OnBoardRequest's identity", store.upsertCalls)
	}
	if len(store.linkCalls) != 1 {
		t.Fatalf("linkCalls = %+v, want exactly 1", store.linkCalls)
	}
	got := store.linkCalls[0]
	if got.EndpointID != string(agent) || got.MTPKind != "WebSocket" {
		t.Errorf("linkCalls[0] = %+v, want endpoint=%q mtp=WebSocket", got, agent)
	}
	if got.DeviceID == "" {
		t.Error("linkCalls[0].DeviceID is empty, want the upserted device's id")
	}
}

func TestReconcilerOnBoardUpsertError(t *testing.T) {
	store := newFakeIdentityStore()
	store.upsertErr = errors.New("boom")
	r := newReconciler(store, slog.Default())
	c := &captureConn{id: agent}
	ob := &usp.OnBoardRequest{OUI: "0025C2", ProductClass: "Gateway", SerialNumber: "SN12345"}

	if err := r.onBoard(context.Background(), c, ob); err == nil {
		t.Fatal("onBoard() = nil error, want the upsert failure")
	}
	if len(store.linkCalls) != 0 {
		t.Errorf("linkCalls = %+v, want none after a failed upsert", store.linkCalls)
	}
}

func TestReconcilerFromProbeFallback(t *testing.T) {
	store := newFakeIdentityStore()
	r := newReconciler(store, slog.Default())
	c := &captureConn{id: agent}

	if err := r.fromProbeFallback(context.Background(), c, "0025C2", "Gateway", "SN12345"); err != nil {
		t.Fatalf("fromProbeFallback() = %v, want nil", err)
	}

	if len(store.upsertCalls) != 1 || store.upsertCalls[0] != (onboardCall{"0025C2", "Gateway", "SN12345"}) {
		t.Fatalf("upsertCalls = %+v, want one call with the given identity", store.upsertCalls)
	}
	if len(store.linkCalls) != 1 {
		t.Fatalf("linkCalls = %+v, want exactly 1", store.linkCalls)
	}
}

func TestReconcilerDisconnect(t *testing.T) {
	store := newFakeIdentityStore()
	r := newReconciler(store, slog.Default())

	r.disconnect(context.Background(), "device-1")

	if len(store.disconnectCalls) != 1 || store.disconnectCalls[0] != "device-1" {
		t.Fatalf("disconnectCalls = %v, want [device-1]", store.disconnectCalls)
	}
}

func TestReconcilerDisconnectSwallowsError(t *testing.T) {
	store := newFakeIdentityStore()
	store.disconnectErr = errors.New("boom")
	r := newReconciler(store, slog.Default())

	// Must not panic and must not surface the error -- disconnect has no
	// return value by design (a disconnect-path failure must never block
	// transport cleanup).
	r.disconnect(context.Background(), "device-1")

	if len(store.disconnectCalls) != 1 {
		t.Fatalf("disconnectCalls = %v, want exactly 1 attempt recorded", store.disconnectCalls)
	}
}
