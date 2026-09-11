package main

import (
	"context"
	"fmt"
	"log/slog"

	"acs/internal/devices"
	"acs/internal/usp"
	"acs/internal/usp/mtp"
)

// identityStore is the narrow slice of *devices.Repository the
// reconciler needs -- not the concrete repository itself -- so
// identity_test.go (and handler_test.go) can exercise reconciliation
// against a fake and stay fast and DB-free, matching the plan's stated
// intent for cmd/uspc's own tests.
type identityStore interface {
	UpsertFromOnBoard(ctx context.Context, oui, productClass, serialNumber string) (*devices.Device, error)
	LinkUspAgent(ctx context.Context, deviceID, endpointID, mtpKind string) error
	MarkUspAgentDisconnected(ctx context.Context, deviceID string) error
	GetUspAgentByEndpointID(ctx context.Context, endpointID string) (*devices.UspAgent, error)
}

// reconciler ties a connected USP agent's identity (OUI/ProductClass/
// SerialNumber) to a devices row and its usp_agents live-connection
// state. An OnBoardRequest Notify is the primary path (design spec
// S5.3); fromProbeFallback is the last-resort path used when only the
// interop probe's GetResp -- not an OnBoardRequest -- yields identity.
type reconciler struct {
	store identityStore
	log   *slog.Logger
}

// newReconciler returns a reconciler ready for use. log defaults to
// slog.Default() when nil, matching this package's other constructors
// (see newProbe).
func newReconciler(store identityStore, log *slog.Logger) *reconciler {
	if log == nil {
		log = slog.Default()
	}
	return &reconciler{store: store, log: log}
}

// onBoard reconciles identity from an OnBoardRequest Notify: upsert the
// devices row on its oui_serial natural key, then link this
// connection's endpoint id and MTP kind to it.
func (r *reconciler) onBoard(ctx context.Context, c mtp.Conn, ob *usp.OnBoardRequest) error {
	device, err := r.store.UpsertFromOnBoard(ctx, ob.OUI, ob.ProductClass, ob.SerialNumber)
	if err != nil {
		return fmt.Errorf("reconcile onboard: upsert device: %w", err)
	}
	if err := r.store.LinkUspAgent(ctx, device.ID, string(c.Endpoint()), string(c.Kind())); err != nil {
		return fmt.Errorf("reconcile onboard: link usp agent: %w", err)
	}
	return nil
}

// fromProbeFallback reconciles identity from the interop probe's own
// GetResp when no OnBoardRequest arrived on this connection -- the
// design spec's "last resort" path (S5.3). Same upsert-then-link shape
// as onBoard, but logged at Info: which path onboarded a device is
// diagnostically useful, since the primary path (OnBoardRequest) not
// firing may indicate an agent that doesn't implement it.
func (r *reconciler) fromProbeFallback(ctx context.Context, c mtp.Conn, oui, productClass, serialNumber string) error {
	device, err := r.store.UpsertFromOnBoard(ctx, oui, productClass, serialNumber)
	if err != nil {
		return fmt.Errorf("reconcile probe fallback: upsert device: %w", err)
	}
	if err := r.store.LinkUspAgent(ctx, device.ID, string(c.Endpoint()), string(c.Kind())); err != nil {
		return fmt.Errorf("reconcile probe fallback: link usp agent: %w", err)
	}
	r.log.Info("uspc: device onboarded via probe fallback, not the primary OnBoardRequest path",
		"device_id", device.ID, "endpoint", c.Endpoint(), "mtp", c.Kind(),
		"oui", oui, "product_class", productClass, "serial_number", serialNumber)
	return nil
}

// disconnect records that deviceID's live USP session ended. It only
// logs (never returns) an error: a disconnect-path failure must never
// block the transport's own connection cleanup (registry.Remove,
// probe.forget), which run regardless of whether this succeeds.
func (r *reconciler) disconnect(ctx context.Context, deviceID string) {
	if err := r.store.MarkUspAgentDisconnected(ctx, deviceID); err != nil {
		r.log.Warn("uspc: failed to mark usp agent disconnected", "device_id", deviceID, "error", err)
	}
}
