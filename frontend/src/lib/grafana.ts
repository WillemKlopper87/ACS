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
