import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { toast, dismissToast, subscribeToasts, type ToastItem } from "./toast";

// The store is module-level state, so each test drains whatever the
// previous one left behind.
function drain() {
  let current: ToastItem[] = [];
  const unsubscribe = subscribeToasts((items) => {
    current = items;
  });
  current.forEach((t) => dismissToast(t.id));
  unsubscribe();
}

describe("toast store", () => {
  let seen: ToastItem[] = [];
  let unsubscribe: () => void;

  beforeEach(() => {
    vi.useFakeTimers();
    drain();
    unsubscribe = subscribeToasts((items) => {
      seen = items;
    });
  });

  afterEach(() => {
    unsubscribe();
    drain();
    vi.useRealTimers();
  });

  it("auto-dismisses a success toast once its ttl elapses", () => {
    toast("saved", "success");
    expect(seen).toHaveLength(1);
    vi.advanceTimersByTime(4200);
    expect(seen).toHaveLength(0);
  });

  // Most screens report failures only via toast, so a timed-out error is
  // the only account of what went wrong disappearing unread.
  it("keeps an error toast until it is dismissed", () => {
    const id = toast("the server refused that", "error");
    vi.advanceTimersByTime(60_000);
    expect(seen).toHaveLength(1);
    expect(seen[0].message).toBe("the server refused that");
    dismissToast(id);
    expect(seen).toHaveLength(0);
  });

  it("still honours an explicit ttl on an error", () => {
    toast("transient", "error", 8000);
    vi.advanceTimersByTime(8000);
    expect(seen).toHaveLength(0);
  });

  it("caps the visible stack so sticky errors cannot bury the screen", () => {
    for (let i = 0; i < 10; i++) toast(`error ${i}`, "error");
    expect(seen).toHaveLength(6);
    // The most recent survive.
    expect(seen[seen.length - 1].message).toBe("error 9");
    expect(seen[0].message).toBe("error 4");
  });
});
