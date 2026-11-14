import { useEffect, useId, useRef, type ReactNode } from "react";

// A panel that slides over the right edge of the page for creating or
// inspecting one thing, so the list it belongs to stays where it is.
// Escape and a click on the backdrop close it; focus moves into it on
// open and back to where it was on close.
export function Drawer({ title, onClose, children, wide = false }: { title: ReactNode; onClose: () => void; children: ReactNode; wide?: boolean }) {
  const titleId = useId();
  const panelRef = useRef<HTMLDivElement>(null);
  const onCloseRef = useRef(onClose);
  onCloseRef.current = onClose;

  useEffect(() => {
    const returnTo = document.activeElement as HTMLElement | null;
    const panel = panelRef.current;
    // First field if there is one, otherwise the panel itself.
    (panel?.querySelector<HTMLElement>("input, select, textarea") ?? panel)?.focus();
    const onKey = (e: KeyboardEvent) => {
      // A confirmation dialog on top handles its own Escape first.
      if (e.key === "Escape" && !document.querySelector(".confirm-dialog")) onCloseRef.current();
    };
    window.addEventListener("keydown", onKey);
    return () => {
      window.removeEventListener("keydown", onKey);
      returnTo?.focus?.();
    };
  }, []);

  return (
    <div className="drawer-backdrop" onClick={onClose}>
      <div
        ref={panelRef}
        className={`drawer${wide ? " wide" : ""}`}
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        tabIndex={-1}
        onClick={(e) => e.stopPropagation()}
      >
        <div className="drawer-head">
          <h3 id={titleId}>{title}</h3>
          <button className="close-detail" onClick={onClose} aria-label="Close">
            ✕
          </button>
        </div>
        <div className="drawer-body">{children}</div>
      </div>
    </div>
  );
}
