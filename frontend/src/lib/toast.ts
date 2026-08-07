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

function emit() {
  listeners.forEach((l) => l(items));
}

export function subscribeToasts(fn: Listener): () => void {
  listeners.add(fn);
  fn(items);
  return () => listeners.delete(fn);
}

export function toast(message: string, kind: ToastKind = "info", ttlMs = 4200): number {
  const id = nextId++;
  items = [...items, { id, kind, message }];
  emit();
  setTimeout(() => dismissToast(id), ttlMs);
  return id;
}

export function dismissToast(id: number) {
  items = items.filter((t) => t.id !== id);
  emit();
}
