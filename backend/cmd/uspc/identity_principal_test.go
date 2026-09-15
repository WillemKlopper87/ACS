package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"acs/internal/cwmp"
	"acs/internal/devices"
	"acs/internal/usp"
	"acs/internal/usp/principal"
)

// Get completes fakeIdentityStore's identityStore implementation now that
// production reconciliation pre-loads the device bound to an authenticated
// transport principal before trusting self-reported OnBoard identity.
func (f *fakeIdentityStore) Get(_ context.Context, id string) (*devices.Device, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, d := range f.devicesByKey {
		if d.ID != id {
			continue
		}
		copy := *d
		if copy.OUISerial == "" {
			copy.OUISerial = (cwmp.DeviceID{OUI: copy.OUI, ProductClass: copy.ProductClass, SerialNumber: copy.SerialNumber}).NaturalKey()
		}
		return &copy, nil
	}
	return nil, errors.New("device not found")
}

type fakeEndpointPrincipalStore struct {
	byEndpoint map[usp.EndpointID]*principal.Principal
	err        error
}

func (f *fakeEndpointPrincipalStore) ByEndpointID(_ context.Context, endpointID usp.EndpointID) (*principal.Principal, error) {
	if f.err != nil {
		return nil, f.err
	}
	p, ok := f.byEndpoint[endpointID]
	if !ok {
		return nil, principal.ErrNotFound
	}
	copy := *p
	return &copy, nil
}

func TestReconcilerAuthenticatedPrincipalAllowsBoundIdentity(t *testing.T) {
	store := newFakeIdentityStore()
	bound := store.seedKnownDevice("0025C2", "Gateway", "SN12345")
	r := newReconciler(store, nil)
	r.usePrincipalStore(&fakeEndpointPrincipalStore{byEndpoint: map[usp.EndpointID]*principal.Principal{
		agent: {DeviceID: bound.ID, EndpointID: agent},
	}})

	c := &captureConn{id: agent}
	ob := &usp.OnBoardRequest{OUI: "00:25:c2", ProductClass: "Gateway", SerialNumber: "SN12345"}
	if err := r.onBoard(context.Background(), c, ob); err != nil {
		t.Fatalf("bound identity rejected: %v", err)
	}
	if len(store.upsertCalls) != 1 || len(store.linkCalls) != 1 {
		t.Fatalf("bound identity calls: reconcile=%d link=%d, want 1/1", len(store.upsertCalls), len(store.linkCalls))
	}
}

func TestReconcilerAuthenticatedPrincipalRejectsDifferentDeviceBeforeMutation(t *testing.T) {
	store := newFakeIdentityStore()
	bound := store.seedKnownDevice("0025C2", "Gateway", "BOUND-01")
	store.seedKnownDevice("0025C2", "Gateway", "OTHER-02")
	r := newReconciler(store, nil)
	r.usePrincipalStore(&fakeEndpointPrincipalStore{byEndpoint: map[usp.EndpointID]*principal.Principal{
		agent: {DeviceID: bound.ID, EndpointID: agent},
	}})

	c := &captureConn{id: agent}
	ob := &usp.OnBoardRequest{OUI: "0025C2", ProductClass: "Gateway", SerialNumber: "OTHER-02"}
	err := r.onBoard(context.Background(), c, ob)
	if err == nil || !strings.Contains(err.Error(), "identity mismatch") {
		t.Fatalf("different-device identity error = %v, want principal identity mismatch", err)
	}
	if len(store.upsertCalls) != 0 {
		t.Fatalf("ReconcileFromOnBoard called %d times after principal mismatch; want 0 so foreign device state is untouched", len(store.upsertCalls))
	}
	if len(store.linkCalls) != 0 {
		t.Fatalf("LinkUspAgent called %d times after principal mismatch; want 0", len(store.linkCalls))
	}
}

func TestProbeFallbackAlsoEnforcesAuthenticatedPrincipal(t *testing.T) {
	store := newFakeIdentityStore()
	bound := store.seedKnownDevice("0025C2", "Gateway", "BOUND-01")
	r := newReconciler(store, nil)
	r.usePrincipalStore(&fakeEndpointPrincipalStore{byEndpoint: map[usp.EndpointID]*principal.Principal{
		agent: {DeviceID: bound.ID, EndpointID: agent},
	}})

	c := &captureConn{id: agent}
	err := r.fromProbeFallback(context.Background(), c, "0025C2", "Gateway", "OTHER-02")
	if err == nil || !strings.Contains(err.Error(), "identity mismatch") {
		t.Fatalf("probe fallback identity error = %v, want principal identity mismatch", err)
	}
	if len(store.upsertCalls) != 0 || len(store.linkCalls) != 0 {
		t.Fatalf("probe mismatch mutated identity state: reconcile=%d link=%d", len(store.upsertCalls), len(store.linkCalls))
	}
}
