import { useEffect, useRef, useState } from "react";
import { answerConfirm, subscribeConfirm, type ConfirmRequest } from "../lib/confirm";

// Draws the console's one confirmation dialog (lib/confirm.ts). Focus
// starts on the safe choice, Escape and a click outside both mean "no",
// and focus goes back to where it was when the dialog closes.
export function ConfirmHost() {
  const [request, setRequest] = useState<ConfirmRequest | null>(null);
  const cancelRef = useRef<HTMLButtonElement>(null);
  const returnTo = useRef<HTMLElement | null>(null);

  useEffect(() => subscribeConfirm(setRequest), []);

  useEffect(() => {
    if (!request) {
      returnTo.current?.focus?.();
      returnTo.current = null;
      return;
    }
    returnTo.current = document.activeElement as HTMLElement | null;
    cancelRef.current?.focus();
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        e.stopPropagation();
        answerConfirm(false);
      }
    };
    window.addEventListener("keydown", onKey, true);
    return () => window.removeEventListener("keydown", onKey, true);
  }, [request]);

  if (!request) return null;
  const [question, ...rest] = request.paragraphs;
  return (
    <div className="modal-backdrop" onClick={() => answerConfirm(false)}>
      <div
        className="modal confirm-dialog"
        role="alertdialog"
        aria-modal="true"
        aria-labelledby="confirm-question"
        aria-describedby={rest.length ? "confirm-detail" : undefined}
        onClick={(e) => e.stopPropagation()}
      >
        <p id="confirm-question" className="confirm-question">
          {question}
        </p>
        {rest.length > 0 && (
          <div id="confirm-detail" className="confirm-detail">
            {rest.map((p, i) => (
              <p key={i}>{p}</p>
            ))}
          </div>
        )}
        <div className="confirm-actions">
          <button ref={cancelRef} className="btn" onClick={() => answerConfirm(false)}>
            Keep as it is
          </button>
          <button className={`btn ${request.danger ? "danger" : "primary"}`} onClick={() => answerConfirm(true)}>
            {request.confirmLabel}
          </button>
        </div>
      </div>
    </div>
  );
}
