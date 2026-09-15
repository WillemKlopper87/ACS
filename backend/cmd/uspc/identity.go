package main

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"acs/internal/cwmp"
	"acs/internal/devices"
	"acs/internal/usp"
	"acs/internal/usp/mtp"
	"acs/internal/usp/principal"
)

// identityStore is the narrow slice of *devices.Repository the
// reconciler needs -- not the concrete repository itself -- so
// identity_test.go (and handler_test.go) can exercise reconciliation
// against a fake and stay fast and DB-free.
type identityStore interface {
	Get(ctx context.Context, id string) (*devices.Device, error)
	ReconcileFromOnBoard(ctx context.Context, oui, productClass, serialNumber string) (*devices.Device, error)
	LinkUspAgent(ctx context.Context, deviceID, endpointID, mtpKind string, supportedProtocolVersions []string) error
	MarkUspAgentDisconnected(ctx context.Context, deviceID, endpointID string) error
	GetUspAgentByEndpointID(ctx context.Context, endpointID string) (*devices.UspAgent, error)
	GetUspAgentByDeviceID(ctx context.Context, deviceID string) (*devices.UspAgent, error)
}

// endpointPrincipalStore is deliberately narrower than
// *principal.Repository. Reconciliation only needs the durable
// EndpointID->device binding; certificate verification remains the MTP's
// responsibility. It is nil in lab mode and configured in production.
type endpointPrincipalStore interface {
	ByEndpointID(ctx context.Context, endpointID usp.EndpointID) (*principal.Principal, error)
}

// splitProtocolVersions parses AgentSupportedProtocolVersions, a
// comma-separated list per the USP wire format, into the slice
// usp_agents.supported_protocol_versions stores.
func splitProtocolVersions(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	versions := make([]string, 0, len(parts))
	for _, p := range parts {
		if v := strings.TrimSpace(p); v != "" {
			versions = append(versions, v)
		}
	}
	return versions
}

// reconciler ties a connected USP agent's identity (OUI/ProductClass/
// SerialNumber) to a devices row and its usp_agents live-connection
// state. In production principals adds a second, independent identity
// check: the reported natural key must belong to the devices row already
// bound to the cryptographically authenticated EndpointID.
type reconciler struct {
	store      identityStore
	principals endpointPrincipalStore
	log        *slog.Logger
}

// newReconciler returns a reconciler ready for use. principals is left nil
// for lab compatibility; production wiring calls usePrincipalStore.
func newReconciler(store identityStore, log *slog.Logger) *reconciler {
	if log == nil {
		log = slog.Default()
	}
	return &reconciler{store: store, log: log}
}

func (r *reconciler) usePrincipalStore(store endpointPrincipalStore) {
	r.principals = store
}

// validateAuthenticatedIdentity runs before ReconcileFromOnBoard so a valid
// certificate for device A cannot self-report device B's OUI/serial and cause
// device B to be marked online or linked to A's EndpointID. The transport has
// already authenticated the certificate and constrained the claimed EndpointID;
// this independently binds the USP application identity to the same device row.
func (r *reconciler) validateAuthenticatedIdentity(ctx context.Context, c mtp.Conn, oui, productClass, serialNumber string) error {
	if r.principals == nil {
		return nil
	}
	if oui == "" || serialNumber == "" {
		return devices.ErrEmptyIdentity
	}

	p, err := r.principals.ByEndpointID(ctx, c.Endpoint())
	if err != nil {
		return fmt.Errorf("resolve authenticated USP principal: %w", err)
	}
	boundDevice, err := r.store.Get(ctx, p.DeviceID)
	if err != nil {
		return fmt.Errorf("load authenticated USP principal device: %w", err)
	}
	reportedKey := (cwmp.DeviceID{OUI: oui, ProductClass: productClass, SerialNumber: serialNumber}).NaturalKey()
	if boundDevice.OUISerial != reportedKey {
		return fmt.Errorf("authenticated USP principal identity mismatch: endpoint %q is bound to device %s (%q), agent reported %q",
			c.Endpoint(), p.DeviceID, boundDevice.OUISerial, reportedKey)
	}
	return nil
}

// onBoard reconciles identity from an OnBoardRequest Notify: validate any
// production principal binding, refresh the known devices row on its
// oui_serial natural key, then link this connection's endpoint id and MTP.
func (r *reconciler) onBoard(ctx context.Context, c mtp.Conn, ob *usp.OnBoardRequest) error {
	if err := r.validateAuthenticatedIdentity(ctx, c, ob.OUI, ob.ProductClass, ob.SerialNumber); err != nil {
		return fmt.Errorf("reconcile onboard: %w", err)
	}
	device, err := r.store.ReconcileFromOnBoard(ctx, ob.OUI, ob.ProductClass, ob.SerialNumber)
	if err != nil {
		return fmt.Errorf("reconcile onboard: %w", err)
	}
	versions := splitProtocolVersions(ob.AgentSupportedProtocolVersions)
	if err := r.store.LinkUspAgent(ctx, device.ID, string(c.Endpoint()), string(c.Kind()), versions); err != nil {
		return fmt.Errorf("reconcile onboard: link usp agent: %w", err)
	}
	return nil
}

// fromProbeFallback reconciles identity from the interop probe's own
// GetResp when no OnBoardRequest arrived on this connection. It applies the
// same production principal binding before touching device state.
func (r *reconciler) fromProbeFallback(ctx context.Context, c mtp.Conn, oui, productClass, serialNumber string) error {
	if err := r.validateAuthenticatedIdentity(ctx, c, oui, productClass, serialNumber); err != nil {
		return fmt.Errorf("reconcile probe fallback: %w", err)
	}
	device, err := r.store.ReconcileFromOnBoard(ctx, oui, productClass, serialNumber)
	if err != nil {
		return fmt.Errorf("reconcile probe fallback: %w", err)
	}
	// The probe's GetResp carries no AgentSupportedProtocolVersions
	// equivalent -- nil leaves whatever LinkUspAgent already has for this
	// device untouched rather than blanking it.
	if err := r.store.LinkUspAgent(ctx, device.ID, string(c.Endpoint()), string(c.Kind()), nil); err != nil {
		return fmt.Errorf("reconcile probe fallback: link usp agent: %w", err)
	}
	r.log.Info("uspc: device onboarded via probe fallback, not the primary OnBoardRequest path",
		"device_id", device.ID, "endpoint", c.Endpoint(), "mtp", c.Kind(),
		"oui", oui, "product_class", productClass, "serial_number", serialNumber)
	return nil
}

// disconnect records that deviceID's live USP session on endpointID ended.
// It only logs (never returns) an error: a disconnect-path failure must
// never block the transport's own connection cleanup.
func (r *reconciler) disconnect(ctx context.Context, deviceID, endpointID string) {
	if err := r.store.MarkUspAgentDisconnected(ctx, deviceID, endpointID); err != nil {
		r.log.Warn("uspc: failed to mark usp agent disconnected", "device_id", deviceID, "endpoint", endpointID, "error", err)
	}
}
