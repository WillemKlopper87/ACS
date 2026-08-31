// Device troubleshooting console (admin-platform backlog: "console/REPL
// screen interface"). Not a remote shell into the device — TR-069/CWMP has
// no such RPC (verified against how GenieACS/Axiros handle this: neither
// offers one either, only a task/job API). This is that job API wearing a
// terminal costume: every command below is a thin parser over the same
// REST endpoints the buttons elsewhere on this page already call — no new
// device-facing capability, just a faster way to reach the existing ones.
import { useEffect, useRef, useState } from "react";
import { api, ApiError } from "../api/client";
import type { Job } from "../api/types";

type Line = {
  kind: "input" | "output" | "error" | "info";
  text: string;
};

const HELP = [
  "Commands:",
  "  get <path> [path2 ...]         live GetParameterValues",
  "  set <path> <value>              queue SetParameterValues (applies next session)",
  "  addobject <path.>                e.g. addobject Device.WiFi.SSID.",
  "  delobject <path.N.>              e.g. delobject Device.WiFi.SSID.3.",
  "  ping <host>                      queue an IP ping diagnostic",
  "  traceroute <host>                queue a traceroute diagnostic",
  "  reboot                           queue Reboot",
  "  factory-reset                    queue FactoryReset (asks to confirm)",
  "  schedule-inform <seconds>        ask the device to check in again after N seconds",
  "  discover                         re-run parameter discovery (GetParameterNames)",
  "  clear                            clear this transcript",
  "  help                             show this list",
  "",
  "Anything queued shows QUEUED here immediately, then this console polls",
  "Jobs until it completes — same job feed as Recent Jobs above.",
].join("\n");

// Raw-JSON mode: one canonical body shape per job type, pre-filled so a
// user editing it is editing real values, not guessing the schema.
const RAW_TEMPLATES: Record<string, { method: (id: string, body: unknown) => Promise<{ command_key: string; status: string }>; sample: unknown }> = {
  GET_PARAMETER: {
    method: (id, body) => api.getParametersLive(id, (body as { paths: string[] }).paths),
    sample: { paths: ["Device.DeviceInfo.SoftwareVersion"] },
  },
  SET_PARAMETER: {
    method: (id, body) => api.putDeviceParameters(id, (body as { parameters: { name: string; value: string; type?: string }[] }).parameters),
    sample: { parameters: [{ name: "Device.WiFi.SSID.1.SSID", value: "MyNetwork", type: "string" }] },
  },
  REBOOT: { method: (id) => api.rebootDevice(id), sample: {} },
  FACTORY_RESET: { method: (id) => api.factoryResetDevice(id), sample: {} },
  DIAGNOSTICS_PING: {
    method: (id, body) => api.createDiagnosticsPing(id, (body as { host: string }).host),
    sample: { host: "8.8.8.8" },
  },
  DIAGNOSTICS_TRACEROUTE: {
    method: (id, body) => api.createDiagnosticsTraceroute(id, (body as { host: string }).host),
    sample: { host: "8.8.8.8" },
  },
  ADD_OBJECT: {
    method: (id, body) => api.addObject(id, (body as { object_path: string }).object_path),
    sample: { object_path: "Device.WiFi.SSID." },
  },
  DELETE_OBJECT: {
    method: (id, body) => api.deleteObject(id, (body as { object_path: string }).object_path),
    sample: { object_path: "Device.WiFi.SSID.3." },
  },
  SCHEDULE_INFORM: {
    method: (id, body) => api.scheduleInform(id, (body as { delay_seconds: number }).delay_seconds),
    sample: { delay_seconds: 3600 },
  },
  PARAMETER_DISCOVERY: { method: (id) => api.discoverParameters(id), sample: {} },
};

export function DeviceConsole({ id, writable }: { id: string; writable: boolean }) {
  const [mode, setMode] = useState<"command" | "raw">("command");
  const [lines, setLines] = useState<Line[]>([{ kind: "info", text: 'Device console ready. Type "help" for commands.' }]);
  const [input, setInput] = useState("");
  const [busy, setBusy] = useState(false);
  const [rawType, setRawType] = useState<keyof typeof RAW_TEMPLATES>("GET_PARAMETER");
  const [rawBody, setRawBody] = useState(JSON.stringify(RAW_TEMPLATES.GET_PARAMETER.sample, null, 2));
  const scrollRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    scrollRef.current?.scrollTo({ top: scrollRef.current.scrollHeight });
  }, [lines]);

  function print(kind: Line["kind"], text: string) {
    setLines((prev) => [...prev, { kind, text }]);
  }

  async function pollJob(commandKey: string) {
    for (let i = 0; i < 15; i++) {
      await new Promise((r) => setTimeout(r, 1000));
      let job: Job;
      try {
        job = await api.getJob(commandKey);
      } catch {
        return; // job lookup itself failing isn't worth spamming the transcript over
      }
      if (job.status === "QUEUED" || job.status === "RPC_SENT" || job.status === "IN_PROGRESS") continue;
      if (job.status === "SUCCESS") {
        const detail = job.result_detail ? ` ${JSON.stringify(job.result_detail)}` : "";
        print("output", `${commandKey}: SUCCESS${detail}`);
      } else {
        print("error", `${commandKey}: ${job.status}${job.fault_string ? ` — ${job.fault_string}` : ""}`);
      }
      return;
    }
    print("info", `${commandKey}: still not complete after 15s — check Recent Jobs, it may need the device's next periodic check-in`);
  }

  async function runQueue(label: string, fn: () => Promise<{ command_key: string; status: string }>) {
    setBusy(true);
    try {
      const res = await fn();
      print("output", `${label} → ${res.command_key} (${res.status})`);
      pollJob(res.command_key); // fire-and-forget — the transcript updates itself when it lands
    } catch (e) {
      print("error", e instanceof ApiError ? `${e.status}: ${e.message}` : "Command failed");
    } finally {
      setBusy(false);
    }
  }

  async function runCommand(raw: string) {
    const trimmed = raw.trim();
    if (!trimmed) return;
    print("input", trimmed);
    const [cmd, ...args] = trimmed.split(/\s+/);

    switch (cmd.toLowerCase()) {
      case "help":
        print("info", HELP);
        return;
      case "clear":
        setLines([]);
        return;
      case "get":
        if (args.length === 0) return print("error", "usage: get <path> [path2 ...]");
        return runQueue(`get ${args.join(" ")}`, () => api.getParametersLive(id, args));
      case "set": {
        if (args.length < 2) return print("error", "usage: set <path> <value>");
        const [path, ...valueParts] = args;
        return runQueue(`set ${path}`, () => api.putDeviceParameters(id, [{ name: path, value: valueParts.join(" ") }]));
      }
      case "addobject":
        if (args.length !== 1 || !args[0].endsWith(".")) return print("error", 'usage: addobject <path.> (must end in ".")');
        return runQueue(`addobject ${args[0]}`, () => api.addObject(id, args[0]));
      case "delobject":
        if (args.length !== 1 || !args[0].endsWith(".")) return print("error", 'usage: delobject <path.N.> (must end in ".")');
        if (!window.confirm(`Delete object instance "${args[0]}"? This cannot be undone.`)) return;
        return runQueue(`delobject ${args[0]}`, () => api.deleteObject(id, args[0]));
      case "ping":
        if (args.length !== 1) return print("error", "usage: ping <host>");
        return runQueue(`ping ${args[0]}`, () => api.createDiagnosticsPing(id, args[0]));
      case "traceroute":
        if (args.length !== 1) return print("error", "usage: traceroute <host>");
        return runQueue(`traceroute ${args[0]}`, () => api.createDiagnosticsTraceroute(id, args[0]));
      case "reboot":
        return runQueue("reboot", () => api.rebootDevice(id));
      case "factory-reset":
        if (!window.confirm("Factory reset this device? This wipes it back to defaults and cannot be undone.")) return;
        return runQueue("factory-reset", () => api.factoryResetDevice(id));
      case "schedule-inform": {
        const seconds = Number(args[0]);
        if (!args[0] || Number.isNaN(seconds) || seconds < 1) return print("error", "usage: schedule-inform <seconds>");
        return runQueue(`schedule-inform ${seconds}`, () => api.scheduleInform(id, seconds));
      }
      case "discover":
        return runQueue("discover", () => api.discoverParameters(id));
      default:
        print("error", `unknown command: ${cmd} — type "help" for the list`);
    }
  }

  async function runRaw() {
    let parsed: unknown;
    try {
      parsed = rawBody.trim() ? JSON.parse(rawBody) : {};
    } catch {
      print("error", "Invalid JSON body");
      return;
    }
    print("input", `[raw ${rawType}] ${rawBody.replace(/\s+/g, " ")}`);
    const tmpl = RAW_TEMPLATES[rawType];
    await runQueue(`raw ${rawType}`, () => tmpl.method(id, parsed));
  }

  return (
    <div className="panel">
      <h3>
        Device console
        <span style={{ display: "flex", gap: "0.4rem" }}>
          <button
            className={`btn ${mode === "command" ? "primary" : ""}`}
            style={{ padding: "0.2em 0.6em", fontSize: "0.72rem" }}
            onClick={() => setMode("command")}
          >
            Command
          </button>
          <button
            className={`btn ${mode === "raw" ? "primary" : ""}`}
            style={{ padding: "0.2em 0.6em", fontSize: "0.72rem" }}
            onClick={() => setMode("raw")}
          >
            Raw JSON
          </button>
        </span>
      </h3>

      <div
        ref={scrollRef}
        style={{
          background: "var(--bg)",
          border: "1px solid var(--border)",
          borderRadius: "var(--radius-md, 6px)",
          padding: "0.6rem 0.75rem",
          height: "14rem",
          overflowY: "auto",
          fontFamily: "var(--font-mono)",
          fontSize: "0.78rem",
          whiteSpace: "pre-wrap",
        }}
      >
        {lines.map((l, i) => (
          <div
            key={i}
            style={{
              color: l.kind === "input" ? "var(--ink)" : l.kind === "error" ? "var(--danger)" : l.kind === "info" ? "var(--ink-faint)" : "var(--ok)",
              marginBottom: "0.15rem",
            }}
          >
            {l.kind === "input" ? "> " : ""}
            {l.text}
          </div>
        ))}
      </div>

      {mode === "command" ? (
        <div className="form-row">
          <input
            placeholder='Type a command — try "help"'
            value={input}
            disabled={busy || !writable}
            onChange={(e) => setInput(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter" && input.trim()) {
                runCommand(input);
                setInput("");
              }
            }}
          />
          <button
            className="btn"
            disabled={busy || !writable || !input.trim()}
            onClick={() => {
              runCommand(input);
              setInput("");
            }}
          >
            Run
          </button>
        </div>
      ) : (
        <>
          <div className="form-row">
            <select
              aria-label="Raw RPC type"
              className="chip"
              value={rawType}
              disabled={busy || !writable}
              onChange={(e) => {
                const t = e.target.value as keyof typeof RAW_TEMPLATES;
                setRawType(t);
                setRawBody(JSON.stringify(RAW_TEMPLATES[t].sample, null, 2));
              }}
            >
              {Object.keys(RAW_TEMPLATES).map((t) => (
                <option key={t} value={t}>
                  {t}
                </option>
              ))}
            </select>
            <button className="btn" disabled={busy || !writable} onClick={runRaw}>
              Send
            </button>
          </div>
          <textarea
            value={rawBody}
            disabled={busy || !writable}
            onChange={(e) => setRawBody(e.target.value)}
            rows={5}
            style={{
              width: "100%",
              marginTop: "0.5rem",
              background: "var(--surface-2)",
              border: "1px solid var(--border)",
              borderRadius: "var(--radius-md, 6px)",
              padding: "0.5rem 0.6rem",
              color: "var(--ink)",
              fontFamily: "var(--font-mono)",
              fontSize: "0.78rem",
              resize: "vertical",
            }}
          />
        </>
      )}

      <p style={{ color: "var(--ink-faint)", fontSize: "0.72rem", marginTop: "0.6rem", marginBottom: 0 }}>
        Not a shell into the device — TR-069 has no remote-shell RPC. Every command here queues the same job types the
        rest of this page uses; the transcript polls until each one completes or the device's next check-in picks it up.
      </p>
    </div>
  );
}
