// Loads both directions of a device's mesh link: the gateway of a satellite
// (devices.gateway_device_id) and the satellites of a gateway.
import { useEffect, useState } from "react";
import { api } from "../api/client";
import type { Device } from "../api/types";

export interface MeshTopologyState {
  /** The linked gateway, when this device is a satellite and it is readable. */
  gateway: Device | null;
  /** Satellites linked to this device, when it is a gateway. */
  satellites: Device[];
}

export function useMeshTopology(device: Device | null): MeshTopologyState {
  const [gateway, setGateway] = useState<Device | null>(null);
  const [satellites, setSatellites] = useState<Device[]>([]);
  const id = device?.id;
  const gatewayId = device?.gateway_device_id;
  // Re-read on the parent's poll so satellite online state stays current.
  const refreshKey = device?.last_inform_at ?? device?.online_status;

  useEffect(() => {
    let cancelled = false;
    if (!gatewayId) {
      setGateway(null);
      return;
    }
    // A gateway outside the caller's scope answers 404: keep the reported
    // identity only.
    api.getDevice(gatewayId).then(
      (g) => !cancelled && setGateway(g),
      () => !cancelled && setGateway(null),
    );
    return () => {
      cancelled = true;
    };
  }, [gatewayId, refreshKey]);

  useEffect(() => {
    let cancelled = false;
    if (!id) {
      setSatellites([]);
      return;
    }
    api.listDeviceSatellites(id).then(
      (r) => !cancelled && setSatellites(r.items ?? []),
      () => !cancelled && setSatellites([]),
    );
    return () => {
      cancelled = true;
    };
  }, [id, refreshKey]);

  return { gateway, satellites };
}
