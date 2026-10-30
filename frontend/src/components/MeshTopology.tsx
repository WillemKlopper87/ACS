// Mesh topology panel (field test 2026-10-30): a gateway lists the mesh
// satellites that reported it via TR-111 Device.GatewayInfo; a satellite
// links back to its gateway. Data comes from lib/useMeshTopology, shared
// with DeviceDetail so reboot/Wi-Fi/Connection Request guidance can name
// the gateway.
import { Link } from "react-router-dom";
import type { Device } from "../api/types";
import type { MeshTopologyState } from "../lib/useMeshTopology";
import { StatusBadge } from "./StatusBadge";
import { timeAgo } from "../lib/format";
import { deviceDisplayName, deviceLink, isMeshSatellite } from "../lib/mesh";
import { vendorLabel } from "../lib/vendorFamily";

export function MeshTopology({ device, mesh }: { device: Device; mesh: MeshTopologyState }) {
  const satellite = isMeshSatellite(device);
  if (!satellite && mesh.satellites.length === 0) return null;

  return (
    <div className="panel">
      <h3>Mesh</h3>
      {satellite && (
        <div className="param-row">
          <span className="path" style={{ fontFamily: "var(--font-ui)" }}>
            {mesh.gateway ? (
              <>
                Mesh satellite of <Link to={deviceLink(mesh.gateway.id)}>{deviceDisplayName(mesh.gateway)}</Link>
                <span className="src">
                  {vendorLabel(mesh.gateway.manufacturer)} · {mesh.gateway.product_class} · last inform {timeAgo(mesh.gateway.last_inform_at)}
                </span>
              </>
            ) : (
              <>
                Mesh satellite of <span className="mono">{device.gateway_oui_serial ?? "an unknown gateway"}</span>
                <span className="src">
                  Reported via Device.GatewayInfo; that gateway is not registered in this ACS or is outside your scope.
                </span>
              </>
            )}
          </span>
          {mesh.gateway && (
            <span>
              <StatusBadge value={mesh.gateway.online_status} />
            </span>
          )}
        </div>
      )}

      {mesh.satellites.length > 0 && (
        <>
          <p className="dim" style={{ margin: satellite ? "0.8rem 0 0.3rem" : "0 0 0.3rem", fontSize: "0.8rem" }}>
            {mesh.satellites.length} mesh satellite{mesh.satellites.length === 1 ? "" : "s"} behind this gateway
          </p>
          {mesh.satellites.map((s) => (
            <div className="param-row" key={s.id}>
              <span className="path" style={{ fontFamily: "var(--font-ui)" }}>
                <Link to={deviceLink(s.id)}>{deviceDisplayName(s)}</Link>
                <span className="src">
                  {vendorLabel(s.manufacturer)} · {s.product_class} · last inform {timeAgo(s.last_inform_at)}
                </span>
              </span>
              <span>
                <StatusBadge value={s.online_status} />
              </span>
            </div>
          ))}
        </>
      )}
    </div>
  );
}
