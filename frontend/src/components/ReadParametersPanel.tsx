// "Read parameters now": queue a live GetParameterValues for a handful of
// paths. The preset is the FastMile telemetry set. Location coordinates are
// read only when the "Include the location coordinates" box is ticked: it gates
// the preset and typed paths alike (Read now stays disabled while a coordinate
// path is in the text and the box is unticked). After a successful send the box
// is unticked and the coordinate paths are removed from the text; other lines stay.
import { useState } from "react";
import { api } from "../api/client";
import { toast } from "../lib/toast";
import { MAX_PROBE_PATHS, checkPaths, isCoordinatePath, parsePaths, probePaths } from "../lib/telemetryProbe";

const note = { margin: "0.4rem 0 0", fontSize: "0.78rem" } as const;

export function ReadParametersPanel({
  deviceId,
  names,
  writable,
}: {
  deviceId: string;
  names: Record<string, boolean> | null;
  writable: boolean;
}) {
  const [text, setText] = useState("");
  const [coordinates, setCoordinates] = useState(false);
  const [busy, setBusy] = useState(false);
  const [presetNote, setPresetNote] = useState("");

  // No discovered names (null or empty) means nothing to check against and
  // nothing to build the preset from.
  const known = names && Object.keys(names).length > 0 ? names : null;
  const paths = parsePaths(text);
  const check = checkPaths(paths, known);
  const coordinatePaths = paths.filter(isCoordinatePath);
  const reasons: string[] = [];
  if (!writable) reasons.push("Your role cannot read parameters from a device.");
  if (check.unknown.length > 0) reasons.push(`The device does not have: ${check.unknown.join(", ")}.`);
  if (check.objectPaths.length > 0) reasons.push(`Object paths ending in a dot cannot be read here: ${check.objectPaths.join(", ")}.`);
  if (!coordinates && coordinatePaths.length > 0) {
    reasons.push(`Tick "Include the location coordinates" to read: ${coordinatePaths.join(", ")}.`);
  }
  if (check.tooMany) reasons.push(`Read at most ${MAX_PROBE_PATHS} paths at a time (you have ${paths.length}).`);
  const blocked = !writable || paths.length === 0 || reasons.length > 0;

  const send = async () => {
    if (busy || blocked) return;
    setBusy(true);
    try {
      const res = await api.getParametersLive(deviceId, paths);
      toast(`Queued ${res.command_key}. It runs when the device next checks in.`);
      setCoordinates(false);
      setText((t) => t.split(/\r?\n/).filter((line) => !isCoordinatePath(line.trim())).join("\n"));
    } catch (e) {
      toast(e instanceof Error ? e.message : String(e), "error");
    } finally {
      setBusy(false);
    }
  };

  const usePreset = () => {
    const preset = probePaths(known, { includeCoordinates: coordinates });
    if (preset.length === 0) {
      setPresetNote("None of the preset parameters were found on this device.");
      return;
    }
    setPresetNote("");
    setText(preset.join("\n"));
  };

  return (
    <div className="panel">
      <h3>Read parameters now</h3>
      <p className="dim" style={{ margin: "0 0 0.5rem", fontSize: "0.78rem" }}>
        Queues a live read. It runs when the device next checks in.
      </p>
      <label htmlFor="read-params-paths" style={{ display: "block", fontSize: "0.8rem", marginBottom: "0.25rem" }}>
        Paths to read, one per line
      </label>
      <textarea
        id="read-params-paths"
        rows={6}
        value={text}
        onChange={(e) => {
          setText(e.target.value);
          setPresetNote("");
        }}
        spellCheck={false}
        style={{ width: "100%", fontFamily: "var(--font-mono, monospace)", fontSize: "0.78rem" }}
      />
      <div style={{ marginTop: "0.5rem" }}>
        <button className="btn sm" onClick={usePreset} disabled={!known}>
          Use FastMile telemetry preset
        </button>
        {!known && (
          <p className="dim" style={note}>
            Discovered names are not available, so the preset is unavailable. You can still type paths.
          </p>
        )}
        {presetNote && (
          <p className="dim" style={note}>
            {presetNote}
          </p>
        )}
      </div>
      <label style={{ display: "block", marginTop: "0.5rem", fontSize: "0.8rem" }}>
        <input type="checkbox" checked={coordinates} onChange={(e) => setCoordinates(e.target.checked)} />{" "}
        Include the location coordinates (read once, to learn their format)
      </label>
      <div style={{ marginTop: "0.6rem" }}>
        <button className="btn primary" onClick={send} disabled={busy || blocked}>
          Read now
        </button>
      </div>
      {reasons.map((r) => (
        <p key={r} className="dim" style={note}>
          {r}
        </p>
      ))}
    </div>
  );
}
