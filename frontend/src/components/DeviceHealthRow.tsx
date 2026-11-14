import type { Device, Job } from "../api/types";
import { fmtUptime, type DeviceHealth } from "../lib/deviceHealth";
import { timeAgo } from "../lib/format";
import { StatusBadge } from "./StatusBadge";

// The first thing on a device's overview: is it up, what is it running,
// how good is its link, who is on it, and how did the last action go.
// A figure the device does not report is left out rather than shown blank,
// so a mesh satellite with no radio of its own does not show an empty
// "Signal" box.
export function DeviceHealthRow({ device, health, lastJob }: { device: Device; health: DeviceHealth; lastJob?: Job }) {
  return (
    <div className="stat-strip device-health" aria-label="Device health">
      <div className="stat">
        <div className="n">{device.last_inform_at ? timeAgo(device.last_inform_at) : "never"}</div>
        <div className="l">last check-in</div>
      </div>
      {health.firmware && (
        <div className="stat">
          <div className="n">{health.firmware}</div>
          <div className="l">firmware</div>
        </div>
      )}
      {health.uptimeSeconds !== undefined && (
        <div className="stat">
          <div className="n">{fmtUptime(health.uptimeSeconds)}</div>
          <div className="l">uptime at last report</div>
        </div>
      )}
      {health.rsrp !== undefined && health.signal && (
        <div className="stat">
          <div className={`n signal-${health.signal}`}>
            {health.rsrp} dBm <span className="stat-note">{health.signal}</span>
          </div>
          <div className="l">signal (RSRP){health.technology ? ` · ${health.technology}` : ""}</div>
        </div>
      )}
      {health.wifiClients !== undefined && (
        <div className="stat">
          <div className="n">{health.wifiClients}</div>
          <div className="l">Wi-Fi clients</div>
        </div>
      )}
      {lastJob && (
        <div className="stat">
          <div className="n">
            <StatusBadge value={lastJob.status} />
          </div>
          <div className="l">last job · {timeAgo(lastJob.created_at)}</div>
        </div>
      )}
    </div>
  );
}
