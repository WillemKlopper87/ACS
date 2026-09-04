import { useEffect, useRef, useState } from "react";

// Shared "auto-refresh" behavior for every list/detail screen: polls `load`
// on an interval, pauses while the tab is backgrounded (no point spending a
// request on a hidden screen), and exposes on/off state so the toolbar can
// render a toggle. Callers are expected to pass a `load` that skips the
// blocking `loading` UI state on background refreshes, so polling never
// causes the table/detail panel to flicker or flash a loading state.
export function useLive(load: () => void | Promise<void>, intervalMs = 6000, defaultOn = true) {
  const [live, setLive] = useState(defaultOn);
  const loadRef = useRef(load);
  // Guards against overlapping polls. Without it, a load slower than the
  // interval (6s by default — easily exceeded by a fleet query under
  // load) starts a second one on top of it, and whichever resolves last
  // wins regardless of which was issued last, so the screen can settle
  // on older data than it was already showing.
  const inFlight = useRef(false);

  useEffect(() => {
    loadRef.current = load;
  }, [load]);

  useEffect(() => {
    if (!live) return;
    const id = setInterval(() => {
      if (document.visibilityState !== "visible") return;
      if (inFlight.current) return;
      inFlight.current = true;
      // load() may be sync; Promise.resolve normalizes both shapes. The
      // caller owns error reporting, so a rejection is swallowed here
      // rather than left to surface as an unhandled rejection — it only
      // needs to release the guard so the next tick can retry.
      Promise.resolve(loadRef.current())
        .catch(() => {})
        .finally(() => {
          inFlight.current = false;
        });
    }, intervalMs);
    return () => clearInterval(id);
  }, [live, intervalMs]);

  return [live, setLive] as const;
}
