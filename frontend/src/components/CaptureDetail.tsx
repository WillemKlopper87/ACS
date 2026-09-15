import { Fragment, useCallback, useEffect, useState } from "react";
import { api, ApiError } from "../api/client";
import type { CaptureEvent, CaptureSession } from "../api/types";
import { useAuth } from "../auth/useAuth";
import { canWrite } from "../auth/roles";
import { diagnoseCapture } from "../lib/captureDiagnostics";
import { fmtTime, timeAgo } from "../lib/format";
import { useLive } from "../lib/useLive";
import { StatusBadge } from "./StatusBadge";

const STAGE_BADGE = { pass: "PASS", warn: "CHECK", pending: "PENDING" } as const;

export function CaptureDetail({ sessionId, onStopped }: { sessionId: string; onStopped?: () => void }) {
  const { role } = useAuth();
  const writable = canWrite(role);
  const [session, setSession] = useState<CaptureSession | null>(null);
  const [events, setEvents] = useState<CaptureEvent[]>([]);
  const [expanded, setExpanded] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const load = useCallback(async (background = false) => {
    try {
      const [sessions, eventResponse] = await Promise.all([
        api.listCaptures(),
        api.getCaptureEvents(sessionId),
      ]);
      setSession(sessions.items.find((item) => item.id === sessionId) ?? null);
      setEvents(eventResponse.items);
      setError(null);
    } catch (e) {
      if (!background) {
        setError(e instanceof ApiError ? `${e.status}: ${e.message}` : "Failed to load capture session");
      }
    }
  }, [sessionId]);

  useEffect(() => {
    setExpanded(null);
    void load();
  }, [load]);

  const [live, setLive] = useLive(() => load(true), 1500, true);

  useEffect(() => {
    if (session && session.effective_status !== "ACTIVE" && live) setLive(false);
  }, [live, session, setLive]);

  const handleStop = async () => {
    setBusy(true);
    try {
      await api.stopCapture(sessionId);
      await load();
      onStopped?.();
    } catch (e) {
      setError(e instanceof ApiError ? `${e.status}: ${e.message}` : "Failed to stop capture");
    } finally {
      setBusy(false);
    }
  };

  const handleDownload = async () => {
    try {
      await api.downloadCaptureExport(sessionId);
    } catch (e) {
      setError(e instanceof ApiError ? `${e.status}: ${e.message}` : "Failed to download transcript");
    }
  };

  if (error) {
    return (
      <div className="banner error">
        {error} <button className="btn" onClick={() => void load()}>Retry</button>
      </div>
    );
  }
  if (!session) return <div className="loading">Loading capture…</div>;

  const diagnosis = diagnoseCapture(events);

  return (
    <section className="panel capture-detail" aria-label={`Capture ${session.id}`}>
      <div className="capture-detail-heading">
        <div>
          <h3>Live transcript</h3>
          <div className="capture-id" title={session.id}>{session.id}</div>
        </div>
        <StatusBadge value={session.effective_status} />
      </div>

      <div className="toolbar capture-actions">
        {session.effective_status === "ACTIVE" && (
          <button className="btn danger" onClick={handleStop} disabled={busy || !writable}>Stop capture</button>
        )}
        <button className="btn" onClick={handleDownload}>Download transcript</button>
        {session.effective_status === "ACTIVE" && (
          <button
            className={`btn live-toggle ${live ? "on" : ""}`}
            onClick={() => setLive((value) => !value)}
            title={live ? "Refreshing every 1.5 seconds — click to pause" : "Refresh paused — click to resume"}
          >
            <span className="live-dot" /> {live ? "Live" : "Paused"}
          </button>
        )}
      </div>

      <dl className="kv capture-meta">
        <dt>Target</dt>
        <dd>{session.match_type} = {session.match_value}</dd>
        <dt>Protocol</dt>
        <dd>{session.protocol}</dd>
        <dt>Started</dt>
        <dd title={`${fmtTime(session.started_at)} UTC`}>{timeAgo(session.started_at)} by {session.started_by}</dd>
        <dt>Expires</dt>
        <dd>{fmtTime(session.expires_at)} UTC</dd>
      </dl>

      {session.protocol === "CWMP" && (
        <div style={{ border: "1px solid var(--border)", borderRadius: "8px", padding: "0.75rem", margin: "0.8rem 0" }}>
          <div style={{ display: "flex", justifyContent: "space-between", gap: "0.7rem", alignItems: "baseline", flexWrap: "wrap" }}>
            <strong>{diagnosis.headline}</strong>
            {diagnosis.huaweiN5368 && <span className="chip">Huawei · N5368 family detected</span>}
          </div>
          <div
            style={{
              display: "grid",
              gridTemplateColumns: "repeat(auto-fit, minmax(170px, 1fr))",
              gap: "0.45rem",
              marginTop: "0.6rem",
            }}
          >
            {diagnosis.stages.map((stage) => (
              <div key={stage.id} style={{ borderTop: "1px solid var(--border)", paddingTop: "0.45rem" }}>
                <div style={{ display: "flex", justifyContent: "space-between", gap: "0.4rem", alignItems: "center" }}>
                  <span style={{ fontSize: "0.76rem", fontWeight: 600 }}>{stage.label}</span>
                  <StatusBadge value={STAGE_BADGE[stage.state]} />
                </div>
                <div className="dim" style={{ fontSize: "0.7rem", lineHeight: 1.35, marginTop: "0.25rem" }}>{stage.detail}</div>
              </div>
            ))}
          </div>
          <div className={diagnosis.faultCodes.length > 0 ? "banner" : "dim"} style={{ marginTop: "0.65rem", fontSize: "0.76rem", lineHeight: 1.4 }}>
            <strong>Next step:</strong> {diagnosis.nextStep}
          </div>
        </div>
      )}

      <div className="table-wrap capture-events">
        <table>
          <thead>
            <tr><th>#</th><th>Time</th><th>Flow</th><th>Kind</th><th>Summary</th></tr>
          </thead>
          <tbody>
            {events.length === 0 && (
              <tr><td colSpan={5} className="empty-cell">No events recorded yet. Leave live refresh on while reproducing the issue.</td></tr>
            )}
            {events.map((event) => (
              <Fragment key={event.id}>
                <tr>
                  <td className="dim">{event.seq}</td>
                  <td className="dim" title={`${fmtTime(event.occurred_at)} UTC`}>{timeAgo(event.occurred_at)}</td>
                  <td><span className={`capture-direction ${event.direction}`}>{event.direction}</span></td>
                  <td>{event.kind}</td>
                  <td>
                    {event.body ? (
                      <button
                        type="button"
                        className="capture-event-toggle"
                        aria-expanded={expanded === event.id}
                        onClick={() => setExpanded(expanded === event.id ? null : event.id)}
                      >
                        {event.summary}<span aria-hidden="true">{expanded === event.id ? " −" : " +"}</span>
                      </button>
                    ) : event.summary}
                  </td>
                </tr>
                {expanded === event.id && event.body && (
                  <tr className="capture-body-row">
                    <td colSpan={5}><pre>{event.body}</pre></td>
                  </tr>
                )}
              </Fragment>
            ))}
          </tbody>
        </table>
      </div>
    </section>
  );
}
