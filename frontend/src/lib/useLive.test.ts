import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { renderHook, act } from "@testing-library/react";
import { useLive } from "./useLive";

describe("useLive", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    vi.spyOn(document, "visibilityState", "get").mockReturnValue("visible");
  });

  afterEach(() => {
    vi.restoreAllMocks();
    vi.useRealTimers();
  });

  it("polls on the interval while live", async () => {
    const load = vi.fn().mockResolvedValue(undefined);
    renderHook(() => useLive(load, 1000));

    // One interval at a time: each await flushes the microtask queue so
    // the previous load settles and releases the in-flight guard.
    // Advancing 3000ms in one synchronous batch would (correctly) yield
    // a single call, since nothing resolves between timer callbacks.
    for (let i = 0; i < 3; i++) {
      await act(async () => {
        vi.advanceTimersByTime(1000);
      });
    }
    expect(load).toHaveBeenCalledTimes(3);
  });

  // A load slower than the interval used to have a second one started on
  // top of it, letting an older response land last and overwrite newer
  // data on screen.
  it("does not start a poll while the previous one is still running", async () => {
    let release: (() => void) | undefined;
    const load = vi.fn(() => new Promise<void>((resolve) => { release = resolve; }));
    renderHook(() => useLive(load, 1000));

    await act(async () => {
      vi.advanceTimersByTime(5000);
    });
    expect(load).toHaveBeenCalledTimes(1);

    await act(async () => {
      release?.();
    });
    await act(async () => {
      vi.advanceTimersByTime(1000);
    });
    expect(load).toHaveBeenCalledTimes(2);
  });

  it("releases the guard when a load rejects, so polling recovers", async () => {
    const load = vi.fn().mockRejectedValue(new Error("network"));
    renderHook(() => useLive(load, 1000));

    await act(async () => {
      vi.advanceTimersByTime(1000);
    });
    await act(async () => {
      vi.advanceTimersByTime(1000);
    });
    expect(load).toHaveBeenCalledTimes(2);
  });

  it("skips polling while the tab is hidden", async () => {
    vi.spyOn(document, "visibilityState", "get").mockReturnValue("hidden");
    const load = vi.fn().mockResolvedValue(undefined);
    renderHook(() => useLive(load, 1000));

    await act(async () => {
      vi.advanceTimersByTime(5000);
    });
    expect(load).not.toHaveBeenCalled();
  });

  it("stops polling once switched off", async () => {
    const load = vi.fn().mockResolvedValue(undefined);
    const { result } = renderHook(() => useLive(load, 1000));

    act(() => {
      result.current[1](false);
    });
    await act(async () => {
      vi.advanceTimersByTime(5000);
    });
    expect(load).not.toHaveBeenCalled();
  });
});
