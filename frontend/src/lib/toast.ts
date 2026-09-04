// Minimal pub-sub toast store, same pattern as auth/tokenStore.ts: module-level
// state outside React so any screen (or a plain event handler) can fire a
// toast without needing a provider higher up the tree.
export type ToastKind = "success" | "error" | "info";
export interface ToastItem {
  id: number;
  kind: ToastKind;
  message: string;
}

type Listener = (items: ToastItem[]) => void;

let items: ToastItem[] = [];
let nextId = 1;
const listeners = new Set<Listener>();

// Errors stay until dismissed. Most screens report failures only via
// toast, and a 4.2s timeout meant an operator who looked away lost the
// only account of what went wrong — often the message naming the reason
// the server refused. Successes and info still self-clear.
const DEFAULT_TTL_MS: Record<ToastKind, number> = {
  error: 0, // 0 = never auto-dismiss
  success: 4200,
  info: 4200,
};

// Sticky errors could otherwise pile up without bound; keep the most
// recent handful rather than burying the screen.
const MAX_VISIBLE = 6;

function emit() {
  listeners.forEach((l) => l(items));
}

export function subscribeToasts(fn: Listener): () => void {
  listeners.add(fn);
  fn(items);
  return () => {
    listeners.delete(fn);
  };
}

export function toast(message: string, kind: ToastKind = "info", ttlMs?: number): number {
  const id = nextId++;
  items = [...items, { id, kind, message }].slice(-MAX_VISIBLE);
  emit();
  const ttl = ttlMs ?? DEFAULT_TTL_MS[kind];
  if (ttl > 0) setTimeout(() => dismissToast(id), ttl);
  return id;
}

export function dismissToast(id: number) {
  items = items.filter((t) => t.id !== id);
  emit();
}
