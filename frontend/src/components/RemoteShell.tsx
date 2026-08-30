// SSH/Telnet remote shell panel (admin-platform backlog, scaffolded "build
// now, functional later" per the user 2026-08-06). Real xterm.js terminal
// wired to a real WebSocket-to-SSH/Telnet bridge — see
// internal/cliaccess/bridge.go, verified end to end against a real SSH
// server. What it CANNOT do yet is reach a CGNAT'd device (this project's
// current ZOWEE test unit included) — the ACS has to originate that
// connection, and CGNAT blocks exactly that direction, same constraint as
// Connection Request. This panel is fully functional against any
// device/host the ACS can actually reach.
import { useEffect, useRef, useState } from "react";
import "@xterm/xterm/css/xterm.css";
import { Terminal } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import { api, ApiError } from "../api/client";
import type { CLICredential } from "../api/types";
import { toast } from "../lib/toast";

export function RemoteShell({ id, writable }: { id: string; writable: boolean }) {
  const [credentials, setCredentials] = useState<CLICredential[]>([]);
  const [selected, setSelected] = useState<string>("");
  const [connected, setConnected] = useState(false);
  const [showForm, setShowForm] = useState(false);
  const [form, setForm] = useState({ protocol: "SSH" as "SSH" | "TELNET", host: "", port: 22, username: "", password: "" });

  const termRef = useRef<HTMLDivElement>(null);
  const terminal = useRef<Terminal | null>(null);
  const fitAddon = useRef<FitAddon | null>(null);
  const ws = useRef<WebSocket | null>(null);

  async function loadCredentials() {
    try {
      const res = await api.listCLICredentials(id);
      setCredentials(res.items);
      if (!selected && res.items.length > 0) setSelected(res.items[0].id);
    } catch (e) {
      toast(e instanceof ApiError ? e.message : "Failed to load CLI credentials", "error");
    }
  }

  useEffect(() => {
    loadCredentials();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [id]);

  useEffect(() => {
    if (!termRef.current || terminal.current) return;
    const term = new Terminal({
      convertEol: true,
      fontFamily: "var(--font-mono)",
      fontSize: 13,
      theme: { background: "#00000000" }, // transparent — inherits the panel's own background
    });
    const fit = new FitAddon();
    term.loadAddon(fit);
    term.open(termRef.current);
    fit.fit();
    terminal.current = term;
    fitAddon.current = fit;

    const onResize = () => fitAddon.current?.fit();
    window.addEventListener("resize", onResize);
    return () => {
      window.removeEventListener("resize", onResize);
      term.dispose();
      terminal.current = null;
    };
  }, []);

  async function connect() {
    if (!selected || !terminal.current) return;
    let url: string;
    try {
      url = await api.cliConnectURL(id, selected);
    } catch (e) {
      toast(e instanceof ApiError ? e.message : "Failed to obtain a console ticket", "error");
      return;
    }
    if (!terminal.current) return;
    const socket = new WebSocket(url);
    socket.binaryType = "arraybuffer";

    socket.onopen = () => {
      setConnected(true);
      terminal.current?.reset();
      terminal.current?.writeln("[connecting…]");
    };
    socket.onmessage = (ev) => {
      const data = ev.data instanceof ArrayBuffer ? new Uint8Array(ev.data) : ev.data;
      terminal.current?.write(typeof data === "string" ? data : data);
    };
    socket.onclose = () => {
      setConnected(false);
      terminal.current?.writeln("\r\n[disconnected]");
    };
    socket.onerror = () => {
      toast("WebSocket error — check that the device/host is actually reachable from the ACS", "error");
    };

    const dataHandler = terminal.current.onData((chunk) => {
      if (socket.readyState === WebSocket.OPEN) socket.send(chunk);
    });

    ws.current = socket;
    // Clean up the xterm data listener when this connection ends.
    socket.addEventListener("close", () => dataHandler.dispose(), { once: true });
  }

  function disconnect() {
    ws.current?.close();
    ws.current = null;
  }

  async function handleCreateCredential() {
    if (!form.host.trim() || !form.username.trim()) {
      toast("Host and username are required", "error");
      return;
    }
    try {
      await api.createCLICredential(id, form);
      setForm({ protocol: "SSH", host: "", port: 22, username: "", password: "" });
      setShowForm(false);
      await loadCredentials();
      toast("CLI credential saved", "success");
    } catch (e) {
      toast(e instanceof ApiError ? e.message : "Failed to save credential", "error");
    }
  }

  async function handleDeleteCredential(credentialId: string) {
    if (!window.confirm("Delete this CLI credential?")) return;
    try {
      await api.deleteCLICredential(id, credentialId);
      if (selected === credentialId) setSelected("");
      await loadCredentials();
    } catch (e) {
      toast(e instanceof ApiError ? e.message : "Failed to delete credential", "error");
    }
  }

  return (
    <div className="panel">
      <h3>
        Remote shell (SSH / Telnet)
        <button className="btn" style={{ padding: "0.2em 0.6em", fontSize: "0.72rem" }} onClick={() => setShowForm((s) => !s)} disabled={!writable}>
          {showForm ? "Cancel" : "+ Add credential"}
        </button>
      </h3>

      {showForm && (
        <div className="form-row" style={{ marginTop: 0, flexWrap: "wrap" }}>
          <select className="chip" value={form.protocol} onChange={(e) => setForm({ ...form, protocol: e.target.value as "SSH" | "TELNET", port: e.target.value === "SSH" ? 22 : 23 })}>
            <option value="SSH">SSH</option>
            <option value="TELNET">TELNET</option>
          </select>
          <input placeholder="Host / IP" value={form.host} onChange={(e) => setForm({ ...form, host: e.target.value })} />
          <input type="number" placeholder="Port" value={form.port} onChange={(e) => setForm({ ...form, port: Number(e.target.value) })} style={{ maxWidth: "6rem" }} />
          <input placeholder="Username" value={form.username} onChange={(e) => setForm({ ...form, username: e.target.value })} />
          <input type="password" placeholder="Password" value={form.password} onChange={(e) => setForm({ ...form, password: e.target.value })} />
          <button className="btn primary" onClick={handleCreateCredential}>
            Save
          </button>
        </div>
      )}

      {credentials.length === 0 ? (
        <p style={{ color: "var(--ink-faint)", fontSize: "0.84rem", margin: "0.5rem 0 0" }}>
          No SSH/Telnet credentials saved for this device yet.
        </p>
      ) : (
        <div className="form-row" style={{ marginTop: showForm ? "0.75rem" : 0 }}>
          <select className="chip" value={selected} onChange={(e) => setSelected(e.target.value)} disabled={connected}>
            {credentials.map((c) => (
              <option key={c.id} value={c.id}>
                {c.protocol} · {c.username}@{c.host}:{c.port}
              </option>
            ))}
          </select>
          {!connected ? (
            <button className="btn primary" onClick={connect} disabled={!selected || !writable}>
              Connect
            </button>
          ) : (
            <button className="btn danger" onClick={disconnect}>
              Disconnect
            </button>
          )}
          {selected && !connected && (
            <button className="btn" onClick={() => handleDeleteCredential(selected)} disabled={!writable}>
              Delete
            </button>
          )}
        </div>
      )}

      <div
        ref={termRef}
        style={{
          marginTop: "0.6rem",
          height: "16rem",
          background: "var(--bg)",
          border: "1px solid var(--border)",
          borderRadius: "var(--radius-md, 6px)",
          padding: "0.4rem",
        }}
      />

      <p style={{ color: "var(--ink-faint)", fontSize: "0.72rem", marginTop: "0.6rem", marginBottom: 0 }}>
        The ACS has to originate this connection to the device, same as Connection Request — a device behind CGNAT
        (unconfirmed reachability) will fail to connect here until a VPN/tunnel path exists.
      </p>
    </div>
  );
}
