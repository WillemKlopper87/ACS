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
  useEffect(() => {
    loadRef.current = load;
  }, [load]);

  useEffect(() => {
    if (!live) return;
    const id = setInterval(() => {
      if (document.visibilityState === "visible") loadRef.current();
    }, intervalMs);
    return () => clearInterval(id);
  }, [live, intervalMs]);

  return [live, setLive] as const;
}
