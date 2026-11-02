// "Grafana ↗" in the sidebar. Grafana runs on its own origin on purpose:
// serving it under the console's origin would let any script inside
// Grafana (a plugin, an HTML panel) read the operator's console token from
// browser storage. scripts/start.sh passes the address as VITE_GRAFANA_URL:
// ACS_GRAFANA_URL when set (e.g. https://grafana.<domain> behind the TLS
// proxy), http://<public IP>:3000 when ACS_GRAFANA_PUBLIC=1, otherwise
// http://localhost:3000, which opens through an SSH tunnel.

const LOOPBACK = new Set(["localhost", "127.0.0.1", "[::1]"]);

export interface GrafanaLink {
  href: string;
  /** The SSH command that makes a loopback-only Grafana reachable, or null. */
  tunnelHint: string | null;
}

export function grafanaLink(configured: string | undefined, apiBase: string | undefined): GrafanaLink | null {
  if (!configured) return null;
  let url: URL;
  try {
    url = new URL(configured);
  } catch {
    return null;
  }
  if (url.protocol !== "http:" && url.protocol !== "https:") return null;
  if (!LOOPBACK.has(url.hostname)) return { href: url.href, tunnelHint: null };

  let host = "<ACS host>";
  try {
    if (apiBase) host = new URL(apiBase).hostname || host;
  } catch {
    // keep the placeholder
  }
  const port = url.port || (url.protocol === "https:" ? "443" : "80");
  return { href: url.href, tunnelHint: `ssh -L ${port}:127.0.0.1:${port} ubuntu@${host}` };
}

// With the Grafana gate on (VITE_GRAFANA_GATE=1, from ACS_GRAFANA_GATE),
// the link is a button: the console mints a 30-second ticket and opens
// Grafana's origin with it, where the proxy turns it into a session
// cookie. The tab is opened before the request so the click still counts
// as a user gesture for the pop-up blocker, and its opener is cleared.
export interface TabLike {
  opener: unknown;
  location: { href: string };
  close: () => void;
}

export async function openGatedGrafana(
  mintTicket: () => Promise<{ url: string }>,
  openTab: () => TabLike | null,
): Promise<string | null> {
  const tab = openTab();
  try {
    const { url } = await mintTicket();
    if (!tab) return "Your browser blocked the new tab; allow pop-ups for the console and try again.";
    tab.opener = null;
    tab.location.href = url;
    return null;
  } catch (err) {
    tab?.close();
    const status = (err as { status?: number }).status;
    if (status === 403) return "Grafana is open to platform operators only (monitoring.view, no customer scope).";
    if (status === 404) return "Grafana access is not configured on this ACS.";
    return "Could not open Grafana; try again.";
  }
}
