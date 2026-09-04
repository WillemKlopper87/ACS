import { useSyncExternalStore } from "react";

// Real reachability state for the sidebar's connection indicator. The
// indicator used to be a bare green dot with no source of truth behind
// it — index.css has always styled `.conn-indicator.down .dot` red, but
// nothing ever applied `.down`, so it read "connected" even with the API
// down.
//
// "Reachable" here means the last fetch got an HTTP response at all. A
// 4xx/5xx is a reachable server answering, so it counts as up; only a
// thrown fetch (DNS failure, connection refused, TLS failure, CORS
// preflight rejection) counts as down. That is the distinction the dot
// is actually claiming to show.
//
// Starts optimistic: a fresh page load has made no request yet, and
// flashing a red "down" indicator before the first call resolves would
// be its own kind of lie.
let reachable = true;
const listeners = new Set<() => void>();

export function markApiReachable(next: boolean) {
  if (reachable === next) return;
  reachable = next;
  listeners.forEach((l) => l());
}

function subscribe(listener: () => void): () => void {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}

export function useApiReachable(): boolean {
  return useSyncExternalStore(
    subscribe,
    () => reachable,
    () => true, // server snapshot — nothing has been fetched during SSR/prerender
  );
}
