import { act, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { useNow } from "@/lib/useNow";

const START = Date.UTC(2026, 7, 29, 12, 0, 0);

describe("useNow", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    vi.setSystemTime(START);
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  it("ticks on the interval and catches up when the tab becomes visible", () => {
    const { result } = renderHook(() => useNow(1_000));
    expect(result.current).toBe(START);
    act(() => {
      vi.advanceTimersByTime(999);
    });
    expect(result.current).toBe(START);
    act(() => {
      vi.advanceTimersByTime(1);
    });
    expect(result.current).toBe(START + 1_000);

    vi.setSystemTime(START + 60_000);
    act(() => {
      document.dispatchEvent(new Event("visibilitychange"));
    });
    expect(result.current).toBe(START + 60_000);
  });

  it("stops ticking on unmount and never ticks for a non-positive interval", () => {
    const { result, unmount } = renderHook(() => useNow(1_000));
    unmount();
    act(() => {
      vi.advanceTimersByTime(5_000);
    });
    expect(result.current).toBe(START);

    const frozen = renderHook(() => useNow(0));
    const initial = frozen.result.current;
    act(() => {
      vi.advanceTimersByTime(60_000);
    });
    expect(frozen.result.current).toBe(initial);
  });
});
