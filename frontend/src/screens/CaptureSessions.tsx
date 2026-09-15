import type { ColumnDef } from "@tanstack/react-table";
import { useCallback, useEffect, useMemo, useState } from "react";
import { useSearchParams } from "react-router-dom";
import { api, ApiError } from "../api/client";
import type { CaptureMatchType, CaptureProtocol, CaptureSession } from "../api/types";
import { canWrite } from "../auth/roles";
import { useAuth } from "../auth/useAuth";
import { CaptureDetail } from "../components/CaptureDetail";
import { DataTable } from "../components/DataTable";
import { StatusBadge } from "../components/StatusBadge";
import { timeAgo } from "../lib/format";
import { useLive } from "../lib/useLive";

type TroubleshootingProfile = "generic" | "huawei-n5368x" | "huawei-tr098";

const PROFILE_HELP: Record<TroubleshootingProfile, { title: string; text: string }> = {
  generic: {
    title: "Generic first-contact capture",
    text: "If the device has never authenticated, match its source IP. An AuthenticationFailure event proves the HTTP request reached ACS; an Inform proves authentication and CWMP session startup succeeded.",
  },
  "huawei-n5368x": {
    title: "Huawei N5368X / 5G CPE",
    text: "Start a remote-IP CWMP capture before repointing the CPE. Keep Connection Request authentication on Digest-SHA256. If the device repeatedly reaches ACS but never produces an Inform, inspect the 401 retry first; in an isolated test deployment try ACS_DIGEST_ALGORITHMS=SHA-256 before considering Basic authentication.",
  },
  "huawei-tr098": {
    title: "Huawei EchoLife / TR-098",
    text: "Capture onboarding first, then discover InternetGatewayDevice. paths before writes. Some Huawei ONTs advertise WLANConfiguration.{i}.KeyPassphrase as non-writable; prefer a discovered writable PreSharedKey.1.KeyPassphrase path instead of assuming the advertised bare path can be set.",
  },
};

export function CaptureSessions() {
  const { role } = useAuth();
  const writable = canWrite(role);
  const [searchParams, setSearchParams] = useSearchParams();
  const [sessions, setSessions] = useState<CaptureSession[]>([]);
  const [selected, setSelected] = useState<string | null>(() => searchParams.get("open"));
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const [matchType, setMatchType] = useState<Exclude<CaptureMatchType, "device">>("identity");
  const [matchValue, setMatchValue] = useState("");
  const [protocol, setProtocol] = useState<CaptureProtocol>("CWMP");
  const [profile, setProfile] = useState<TroubleshootingProfile>("generic");
  const [busy, setBusy] = useState(false);

  const load = useCallback(async (background = false) => {
    if (!background) setLoading(true);
    try {
      const response = await api.listCaptures();
      setSessions(response.items);
      setError(null);
    } catch (e) {
      if (!background) setError(e instanceof ApiError ? `${e.status}: ${e.message}` : "Failed to reach the API — is cmd/api running?");
    } finally {
      if (!background) setLoading(false);
    }
  }, []);

  useEffect(() => { void load(); }, [load]);
  useEffect(() => {
    const requested = searchParams.get("open");
    if (requested) setSelected(requested);
  }, [searchParams]);
  const [live, setLive] = useLive(() => load(true), 5000);

  const selectSession = (id: string) => {
    setSelected(id);
    setSearchParams({ open: id }, { replace: true });
  };

  const selectProfile = (next: TroubleshootingProfile) => {
    setProfile(next);
    if (next !== "generic") {
      setProtocol("CWMP");
      // Huawei first-contact failures are most useful by source IP because
      // identity is not available until an authenticated Inform succeeds.
      setMatchType("remote_ip");
    }
  };

  const handleStart = async () => {
    const target = matchValue.trim();
    if (!target) return;
    setBusy(true);
    try {
      const session = await api.startCapture(matchType, target, protocol);
      setMatchValue("");
      await load(true);
      selectSession(session.id);
    } catch (e) {
      setError(e instanceof ApiError ? `${e.status}: ${e.message}` : "Failed to start capture");
    } finally {
      setBusy(false);
    }
  };

  const columns = useMemo<ColumnDef<CaptureSession, any>[]>(() => [
    { accessorKey: "match_value", header: "Target", cell: ({ row }) => <span title={row.original.id}>{row.original.match_value}</span> },
    { accessorKey: "match_type", header: "Mode", cell: ({ getValue }) => <span className="dim">{getValue() as string}</span> },
    { accessorKey: "protocol", header: "Protocol" },
    { accessorKey: "effective_status", header: "Status", cell: ({ getValue }) => <StatusBadge value={getValue() as string} /> },
    { accessorKey: "started_by", header: "Started by", cell: ({ getValue }) => <span className="dim">{getValue() as string}</span> },
    { accessorKey: "started_at", header: "Started", cell: ({ getValue }) => <span className="dim">{timeAgo(getValue() as string)}</span> },
  ], []);

  const help = PROFILE_HELP[profile];

  return (
    <section className="capture-workbench">
      <div className="capture-master">
        {error && (
          <div className="banner error">{error} <button className="btn" onClick={() => void load()}>Retry</button></div>
        )}

        <div className="panel capture-launcher">
          <h3>Start a capture</h3>
          <p className="dim capture-guidance">
            Use expected identity before a device authenticates, or a known remote IP for first-contact/fleet-wide troubleshooting.
            For an onboarded device, start from its Device Detail panel.
          </p>

          <div className="form-row" style={{ marginTop: 0 }}>
            <label className="field" style={{ minWidth: "15rem" }}>
              <span>Troubleshooting profile</span>
              <select value={profile} onChange={(event) => selectProfile(event.target.value as TroubleshootingProfile)}>
                <option value="generic">Generic CPE</option>
                <option value="huawei-n5368x">Huawei N5368X / 5G CPE</option>
                <option value="huawei-tr098">Huawei EchoLife / TR-098</option>
              </select>
            </label>
          </div>

          <div className="banner" style={{ margin: "0.55rem 0 0.75rem" }}>
            <strong>{help.title}</strong>
            <div style={{ fontSize: "0.78rem", marginTop: "0.25rem", lineHeight: 1.4 }}>{help.text}</div>
            {profile !== "generic" && (
              <div className="dim" style={{ fontSize: "0.72rem", marginTop: "0.35rem" }}>
                Optional host-side reachability probe: set <code>ACS_ONBOARDING_LISTENER=once</code>. It logs the first POST and disables itself after a successful Inform; it does not bypass CWMP authentication.
              </div>
            )}
          </div>

          <div className="form-row">
            <label className="field">
              <span>Match mode</span>
              <select value={matchType} onChange={(event) => setMatchType(event.target.value as Exclude<CaptureMatchType, "device">)} disabled={busy || !writable}>
                <option value="identity">Expected identity</option>
                <option value="remote_ip">Remote IP (global access)</option>
              </select>
            </label>
            <label className="field capture-target-field">
              <span>{matchType === "identity" ? "OUI + serial" : "IP address"}</span>
              <input
                placeholder={matchType === "identity" ? "001349+S230Q12345678" : "192.168.1.50"}
                value={matchValue}
                onChange={(event) => setMatchValue(event.target.value)}
                disabled={busy || !writable}
              />
            </label>
            <label className="field">
              <span>Protocol</span>
              <select value={protocol} onChange={(event) => setProtocol(event.target.value as CaptureProtocol)} disabled={busy || !writable}>
                <option value="CWMP">CWMP</option>
                <option value="USP">USP</option>
              </select>
            </label>
            <button className="btn primary capture-start" onClick={handleStart} disabled={busy || !writable || !matchValue.trim()}>
              {busy ? "Starting…" : "Start capture"}
            </button>
          </div>
        </div>

        <div className="toolbar capture-list-toolbar">
          <span className="result-count">{sessions.length} session{sessions.length === 1 ? "" : "s"}</span>
          <div className="toolbar-spacer" />
          <button className={`btn live-toggle ${live ? "on" : ""}`} onClick={() => setLive((value) => !value)}>
            <span className="live-dot" /> {live ? "Live" : "Paused"}
          </button>
          <button className="btn" onClick={() => void load()} disabled={loading}>{loading ? "Loading…" : "Refresh"}</button>
        </div>

        {loading && sessions.length === 0 ? (
          <div className="loading">Loading capture sessions…</div>
        ) : (
          <DataTable
            data={sessions}
            columns={columns}
            getRowId={(session) => session.id}
            selectedRowId={selected ?? undefined}
            emptyMessage="No capture sessions yet. Start one above or from a device detail panel."
            onRowClick={(session) => selectSession(session.id)}
          />
        )}
      </div>

      <div className="capture-inspector">
        {selected ? (
          <CaptureDetail key={selected} sessionId={selected} onStopped={() => void load(true)} />
        ) : (
          <div className="panel capture-empty">
            <h3>Transcript</h3>
            <p>Select a capture session to inspect its redacted protocol events and guided diagnosis.</p>
          </div>
        )}
      </div>
    </section>
  );
}
