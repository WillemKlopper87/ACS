// VPN/tunnel concentrator panel (admin-platform backlog, deliberately
// last item). Real keypair + overlay IP allocation, real generated
// wg-quick client config — but no OS-level WireGuard interface exists
// behind this yet (see internal/vpn's doc comment for the full scope).
// Same CGNAT-style honesty note as DeviceWebGUI/RemoteShell: enrolling a
// device here doesn't make it reachable by itself.
import { useEffect, useState } from "react";
import { api, ApiError } from "../api/client";
import type { VPNConcentrator, VPNPeer } from "../api/types";
import { toast } from "../lib/toast";

export function VPNTunnel({ id, writable }: { id: string; writable: boolean }) {
  const [peer, setPeer] = useState<VPNPeer | null>(null);
  const [config, setConfig] = useState<string | null>(null);
  const [concentrator, setConcentrator] = useState<VPNConcentrator | null>(null);
  const [showConfig, setShowConfig] = useState(false);
  const [busy, setBusy] = useState(false);

  async function load() {
    try {
      const result = await api.getVPNPeerConfig(id);
      setPeer(result.peer);
      setConfig(result.config);
    } catch (e) {
      if (e instanceof ApiError && e.status === 404) {
        setPeer(null);
        setConfig(null);
      } else {
        toast(e instanceof ApiError ? e.message : "Failed to load VPN peer", "error");
      }
    }
    try {
      setConcentrator(await api.getVPNConcentrator());
    } catch {
      // Non-fatal — the panel still works without the concentrator banner.
    }
  }

  useEffect(() => {
    load();
    setShowConfig(false);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [id]);

  async function handleEnroll() {
    setBusy(true);
    try {
      const result = await api.enrollVPNPeer(id);
      setPeer(result.peer);
      setConfig(result.config);
      toast("Device enrolled in the VPN overlay", "success");
    } catch (e) {
      toast(e instanceof ApiError ? e.message : "Failed to enroll device", "error");
    } finally {
      setBusy(false);
    }
  }

  async function handleRevoke() {
    if (!peer) return;
    if (!window.confirm("Revoke this device's VPN peer? Its overlay IP becomes available for reallocation.")) return;
    setBusy(true);
    try {
      await api.revokeVPNPeer(peer.id);
      setPeer(null);
      setConfig(null);
      setShowConfig(false);
      toast("VPN peer revoked", "success");
    } catch (e) {
      toast(e instanceof ApiError ? e.message : "Failed to revoke VPN peer", "error");
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="panel">
      <h3>
        VPN tunnel
        {!peer && (
          <button className="btn primary sm" onClick={handleEnroll} disabled={!writable || busy}>
            Enroll
          </button>
        )}
      </h3>

      {!peer ? (
        <p style={{ color: "var(--ink-faint)", fontSize: "0.84rem", margin: "0.5rem 0 0" }}>
          Not enrolled in the VPN overlay yet.
        </p>
      ) : (
        <>
          <div className="param-row">
            <span className="path">Overlay IP</span>
            <span className="val">{peer.overlay_ip}</span>
          </div>
          <div className="param-row">
            <span className="path">Public key</span>
            <span className="val" style={{ fontSize: "0.74rem" }}>{peer.public_key}</span>
          </div>
          <div className="param-row">
            <span className="path">Status</span>
            <span className="val">{peer.status}</span>
          </div>

          <div className="form-row">
            <button className="btn" onClick={() => setShowConfig((s) => !s)}>
              {showConfig ? "Hide config" : "View client config"}
            </button>
            <button className="btn" onClick={handleRevoke} disabled={!writable || busy}>
              Revoke
            </button>
          </div>

          {showConfig && (
            <pre
              style={{
                marginTop: "0.6rem",
                fontSize: "0.76rem",
                overflowX: "auto",
                background: "var(--surface-2)",
                border: "1px solid var(--border)",
                borderRadius: "var(--radius-md, 6px)",
                padding: "0.6rem",
              }}
            >
              {config}
            </pre>
          )}
        </>
      )}

      {concentrator && !concentrator.configured && (
        <p style={{ color: "var(--warn)", fontSize: "0.72rem", marginTop: "0.6rem", marginBottom: 0 }}>
          Concentrator endpoint not configured (ACS_VPN_SERVER_PUBLIC_KEY / ACS_VPN_SERVER_ENDPOINT) — enrollment
          still works, but the generated config's [Peer] section will be blank until those are set.
        </p>
      )}
      <p style={{ color: "var(--ink-faint)", fontSize: "0.72rem", marginTop: "0.6rem", marginBottom: 0 }}>
        This allocates a real keypair and overlay IP and generates a standard wg-quick config — but no WireGuard
        interface is running behind it yet. A CGNAT'd device stays unreachable until a concentrator host applies
        these peers and the config is pushed onto the device by some vendor-specific mechanism (TR-069 has no
        native "here's your VPN config" RPC).
      </p>
    </div>
  );
}
