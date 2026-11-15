// One confirmation dialog for the whole console, called like the browser's
// own confirm() but awaited:
//
//   if (!(await confirmDialog(`Delete group "${name}"? …`))) return;
//
// Same store-outside-React pattern as lib/toast.ts, so an event handler
// can ask without a provider above it. ConfirmHost (mounted once in App)
// draws the dialog. With no host mounted (a component rendered alone in a
// unit test) it falls back to window.confirm, so a destructive action is
// never left without a question.

export interface ConfirmRequest {
  id: number;
  // The question, first; any further paragraphs explain the consequence.
  paragraphs: string[];
  confirmLabel: string;
  danger: boolean;
  resolve: (ok: boolean) => void;
}

export interface ConfirmOptions {
  confirmLabel?: string;
  danger?: boolean;
}

type Listener = (current: ConfirmRequest | null) => void;

let current: ConfirmRequest | null = null;
let nextId = 1;
const listeners = new Set<Listener>();

function notify() {
  for (const l of listeners) l(current);
}

export function subscribeConfirm(listener: Listener): () => void {
  listeners.add(listener);
  listener(current);
  return () => {
    listeners.delete(listener);
  };
}

// Verbs that take something away or cannot be undone get the danger
// styling, and name the button, so it reads "Delete", not "OK".
const DANGER_VERBS = ["Delete", "Revoke", "Factory reset", "Disable", "Remove", "Reset", "Cancel", "Turn off"];
const PLAIN_VERBS = ["Reboot", "Apply", "Retry", "Start", "Enable"];

export function describeConfirm(message: string, options: ConfirmOptions = {}): { confirmLabel: string; danger: boolean } {
  const verb = [...DANGER_VERBS, ...PLAIN_VERBS].find((v) => message.startsWith(v + " "));
  const danger = options.danger ?? (verb !== undefined && DANGER_VERBS.includes(verb));
  // "Cancel" as a button would read as "dismiss this dialog".
  const label = verb === "Cancel" ? "Yes, cancel it" : verb;
  return { confirmLabel: options.confirmLabel ?? label ?? "Confirm", danger };
}

export function confirmDialog(message: string, options: ConfirmOptions = {}): Promise<boolean> {
  if (listeners.size === 0) return Promise.resolve(window.confirm(message));
  // A second question while one is open would hide the first: refuse it.
  if (current) return Promise.resolve(false);
  return new Promise((resolve) => {
    current = {
      id: nextId++,
      paragraphs: message.split(/\n\s*\n/).map((p) => p.trim()).filter(Boolean),
      ...describeConfirm(message, options),
      resolve,
    };
    notify();
  });
}

export function answerConfirm(ok: boolean) {
  const request = current;
  if (!request) return;
  current = null;
  notify();
  request.resolve(ok);
}
