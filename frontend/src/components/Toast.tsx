import { useEffect, useState } from "react";
import { dismissToast, subscribeToasts, type ToastItem } from "../lib/toast";

export function ToastHost() {
  const [items, setItems] = useState<ToastItem[]>([]);
  useEffect(() => subscribeToasts(setItems), []);

  // The host stays mounted even when empty: a live region inserted into
  // the DOM at the same moment as its content is not reliably announced,
  // and most screens report failures exclusively through here — so an
  // unmounted region meant errors were silent for screen-reader users.
  return (
    <div className="toast-host" aria-live="polite">
      {items.map((t) => (
        <div
          key={t.id}
          className={`toast toast-${t.kind}`}
          // Errors interrupt; everything else waits its turn.
          role={t.kind === "error" ? "alert" : "status"}
          onClick={() => dismissToast(t.id)}
        >
          <span className="toast-msg">{t.message}</span>
          <button
            className="toast-close"
            aria-label="Dismiss notification"
            onClick={(e) => {
              e.stopPropagation();
              dismissToast(t.id);
            }}
          >
            ✕
          </button>
        </div>
      ))}
    </div>
  );
}
