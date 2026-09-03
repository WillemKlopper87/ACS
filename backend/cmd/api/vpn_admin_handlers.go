// VPN/tunnel concentrator admin endpoints (admin-platform backlog,
// deliberately last). See internal/vpn's package doc comment for what
// this does and does not manage — real keypairs and overlay IP
// allocation, real generated client config, but no OS-level WireGuard
// interface. Gated behind operators.PermCLIAccess: a VPN peer's private
// key is the same class of remote-access credential material as the CLI
// console's SSH/Telnet credentials and the device web-GUI's Basic Auth
// pair, so it shares that curated permission rather than adding a new
// one to the matrix for what's conceptually the same capability.
package main

import (
	"errors"
	"net/http"

	"acs/internal/vpn"
)

func (h *handler) enrollVPNPeer(w http.ResponseWriter, r *http.Request) {
	deviceID := r.PathValue("id")
	if _, ok := h.getScopedDevice(w, r, deviceID); !ok {
		return
	}
	peer, err := h.vpnPeers.EnrollDevice(r.Context(), deviceID)
	if err == vpn.ErrAlreadyEnrolled {
		http.Error(w, "device already has an enrolled vpn peer — revoke it first to re-enroll", http.StatusConflict)
		return
	}
	if err == vpn.ErrOverlaySubnetExhausted {
		http.Error(w, "overlay subnet exhausted — no free addresses left to allocate", http.StatusConflict)
		return
	}
	if err != nil {
		h.logger.Error("failed to enroll vpn peer", "err", err, "device_id", deviceID)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	actor := operatorFromRequest(r)
	if err := h.auditor.Record(r.Context(), actor, deviceID, "VPNPeerEnrolled", map[string]any{
		"overlay_ip": peer.OverlayIP, "public_key": peer.PublicKey,
	}); err != nil {
		h.logger.Error("failed to write audit record", "err", err)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"peer":   peer,
		"config": vpn.RenderClientConfig(peer, h.vpnConcentrator),
	})
}

func (h *handler) listVPNPeers(w http.ResponseWriter, r *http.Request) {
	// audit P2.1/M-12: a peer row names its device and overlay IP, both
	// cross-tenant identifying details — this list must not be
	// fleet-wide for a scoped operator.
	customerIDs, scoped, err := h.deviceScope(r)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	var items []vpn.Peer
	if scoped {
		items, err = h.vpnPeers.ListPeersForCustomers(r.Context(), customerIDs)
	} else {
		items, err = h.vpnPeers.ListPeers(r.Context())
	}
	if err != nil {
		h.logger.Error("failed to list vpn peers", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if items == nil {
		items = []vpn.Peer{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *handler) getVPNPeerConfig(w http.ResponseWriter, r *http.Request) {
	deviceID := r.PathValue("id")
	if _, ok := h.getScopedDevice(w, r, deviceID); !ok {
		return
	}
	peer, err := h.vpnPeers.GetPeerConfig(r.Context(), deviceID)
	if err == vpn.ErrNotFound {
		http.Error(w, "device has no enrolled vpn peer", http.StatusNotFound)
		return
	}
	if err != nil {
		h.logger.Error("failed to load vpn peer config", "err", err, "device_id", deviceID)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"peer":   peer,
		"config": vpn.RenderClientConfig(peer, h.vpnConcentrator),
	})
}

func (h *handler) revokeVPNPeer(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("peer_id")
	// audit P2.1/H-3: this route is peer-addressed with no device_id in
	// the path — without loading the peer first to learn its device, any
	// operator could revoke any tenant's VPN peer by UUID alone.
	peer, err := h.vpnPeers.GetPeerByID(r.Context(), id)
	if errors.Is(err, vpn.ErrNotFound) {
		http.Error(w, "vpn peer not found or already revoked", http.StatusNotFound)
		return
	} else if err != nil {
		h.logger.Error("failed to load vpn peer", "err", err, "id", id)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if _, ok := h.getScopedDevice(w, r, peer.DeviceID); !ok {
		return
	}
	if err := h.vpnPeers.RevokePeer(r.Context(), id); err == vpn.ErrNotFound {
		http.Error(w, "vpn peer not found or already revoked", http.StatusNotFound)
		return
	} else if err != nil {
		h.logger.Error("failed to revoke vpn peer", "err", err, "id", id)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	actor := operatorFromRequest(r)
	if err := h.auditor.Record(r.Context(), actor, "", "VPNPeerRevoked", map[string]any{"peer_id": id}); err != nil {
		h.logger.Error("failed to write audit record", "err", err)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) getVPNConcentrator(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"server_public_key": h.vpnConcentrator.ServerPublicKey,
		"endpoint":          h.vpnConcentrator.Endpoint,
		"overlay_subnet":    h.vpnConcentrator.OverlaySubnet,
		"configured":        h.vpnConcentrator.Configured(),
	})
}
