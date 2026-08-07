import { useEffect, useState } from "react";
import { dismissToast, subscribeToasts, type ToastItem } from "../lib/toast";

export function ToastHost() {
  const [items, setItems] = useState<ToastItem[]>([]);
  useEffect(() => subscribeToasts(setItems), []);

  if (items.length === 0) return null;
  return (
    <div className="toast-host">
      {items.map((t) => (
        <div key={t.id} className={`toast toast-${t.kind}`} onClick={() => dismissToast(t.id)}>
          <span className="toast-msg">{t.message}</span>
          <button
            className="toast-close"
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
