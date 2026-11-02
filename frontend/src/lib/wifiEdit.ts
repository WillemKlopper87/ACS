import type { components } from "../api/generated";

export type WiFiNetwork = components["schemas"]["WiFiNetwork"];

// The per-network Wi-Fi editor's rules (components/WiFiNetworksPanel.tsx).
// The server enforces the same rules (cmd/api/wifi_edit.go); these exist
// so the operator sees the consequence before sending.

export interface WiFiForm {
  name: string;
  passphrase: string;
  securityMode: string;
  enabled: boolean;
  advertised: boolean;
  alsoInstances: number[];
  allowOpen: boolean;
}

export function formFor(n: WiFiNetwork): WiFiForm {
  return {
    name: n.name ?? "",
    passphrase: "",
    securityMode: n.security_mode ?? "",
    enabled: n.enabled ?? true,
    advertised: n.advertised ?? true,
    alsoInstances: [],
    allowOpen: false,
  };
}

export const isOpen = (mode: string) => mode.trim().toLowerCase() === "none";

/** Networks an edit can also apply to: same name, other band, same role. */
export function pairedNetworks(n: WiFiNetwork, all: WiFiNetwork[]): WiFiNetwork[] {
  if (!n.name) return [];
  return all.filter(
    (o) => o.instance !== n.instance && o.access_point && o.name === n.name && o.role === n.role && o.band && o.band !== n.band,
  );
}

/** The PUT body: only what changed. */
export function editBody(n: WiFiNetwork, f: WiFiForm): Record<string, unknown> {
  const body: Record<string, unknown> = {};
  if (f.name.trim() !== (n.name ?? "")) body.name = f.name.trim();
  if (f.passphrase) body.passphrase = f.passphrase;
  if (f.securityMode && f.securityMode !== (n.security_mode ?? "")) body.security_mode = f.securityMode;
  if (f.enabled !== (n.enabled ?? true)) body.enabled = f.enabled;
  if (f.advertised !== (n.advertised ?? true)) body.advertised = f.advertised;
  if (Object.keys(body).length > 0 && f.alsoInstances.length > 0) body.also_instances = f.alsoInstances;
  if (f.allowOpen) body.allow_open = true;
  return body;
}

export interface Impact {
  /** Clients on the network must reconnect (or lose Wi-Fi). */
  disconnects: boolean;
  /** Connected clients on the affected networks, when sampled. */
  clients: number | null;
  /** Problems that block sending, in the server's words. */
  problems: string[];
  /** What happens, for the confirmation line. */
  summary: string;
}

export function impactOf(
  n: WiFiNetwork,
  f: WiFiForm,
  clientsByInstance: Record<string, number> | null | undefined,
): Impact {
  const body = editBody(n, f);
  const wasOn = n.enabled ?? true;
  const problems: string[] = [];
  const mode = f.securityMode || n.security_mode || "";

  if ("name" in body && (f.name.trim() === "" || new TextEncoder().encode(f.name.trim()).length > 32)) problems.push("The name must be 1 to 32 bytes.");
  if (f.passphrase) {
    const hex = /^[0-9a-fA-F]{64}$/.test(f.passphrase);
    // eslint-disable-next-line no-control-regex
    if (!hex && (f.passphrase.length < 8 || f.passphrase.length > 63 || /[^\x20-\x7e]/.test(f.passphrase))) {
      problems.push("The passphrase must be 8 to 63 printable characters (or 64 hex digits).");
    }
    if (isOpen(mode)) problems.push("An open network has no passphrase; choose a WPA mode.");
  }
  if (f.enabled && isOpen(mode) && !f.allowOpen) problems.push("This network would be open to anyone. Choose a WPA mode or confirm an open network.");
  if (!wasOn && f.enabled && !f.passphrase && !isOpen(mode)) problems.push("Set a passphrase to turn this network on: the ACS does not know its current one.");
  if ("security_mode" in body && isOpen(n.security_mode ?? "") && !isOpen(mode) && !f.passphrase) problems.push("Set a passphrase with the new security mode.");
  if (Object.keys(body).filter((k) => k !== "allow_open").length === 0) problems.push("Nothing has changed.");

  const disconnects = wasOn && ("name" in body || "passphrase" in body || "security_mode" in body || body.enabled === false);
  const instances = [n.instance, ...f.alsoInstances];
  let clients: number | null = null;
  if (clientsByInstance) clients = instances.reduce((sum, i) => sum + (clientsByInstance[String(i)] ?? 0), 0);

  let summary: string;
  if (!wasOn && f.enabled) summary = "Turns this network on. Nobody is connected to it yet.";
  else if (body.enabled === false) summary = "Turns this network off.";
  else if (disconnects) summary = "Changes the name or security of a network that is on.";
  else summary = "Applies without disconnecting anyone.";
  if (disconnects) {
    const who = clients === null ? "Every connected device" : clients === 1 ? "1 connected device" : `${clients} connected devices`;
    summary +=
      body.enabled === false
        ? ` ${who} will lose this Wi-Fi.`
        : ` ${who} will disconnect and must rejoin${"name" in body ? ` "${f.name.trim()}"` : ""}${"passphrase" in body ? " with the new passphrase" : ""}.`;
  }
  return { disconnects, clients, problems, summary };
}

/** Spare networks a new one can be made from: off, served by an AP. */
export function spareNetworks(all: WiFiNetwork[]): WiFiNetwork[] {
  return all.filter((n) => n.access_point && n.role === "disabled" && n.name_writable && n.enable_writable && n.passphrase_writable);
}
