import { act, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError } from "@/lib/api";
import type { ReleaseSubscriberState, SubscriberStreamSnapshot } from "@/lib/types";
import { reconnectDelay, useReleaseSubscribers } from "@/lib/useReleaseSubscribers";

const mocks = vi.hoisted(() => ({
  releaseSubscribers: vi.fn(),
  subscriberStream: vi.fn(),
}));

vi.mock("@/lib/api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/api")>();
  return {
    ...actual,
    api: {
      ...actual.api,
      releaseSubscribers: mocks.releaseSubscribers,
      subscriberStream: mocks.subscriberStream,
    },
  };
});

const ns = { env: "prod", app: "gradethis" };

const row = (patch: Partial<ReleaseSubscriberState>): ReleaseSubscriberState => ({
  namespace: ns,
  release_name: "runtime",
  client_name: "api",
  instance_id: "api-1",
  identity: "gradethis-prod",
  state: "applied",
  release_version: 12,
  activation_revision: 41,
  rejection_category: "",
  diagnostic: "",
  client_timestamp_unix_ms: 1,
  server_timestamp_unix_ms: 1,
  connected: true,
  ...patch,
});

const snapshot = (
  subscribers: ReleaseSubscriberState[],
  revision: number,
): SubscriberStreamSnapshot => ({
  summary: {
    total: subscribers.length,
    connected: subscribers.length,
    applied_current: 0,
    rejected: 0,
    pending: 0,
    stale: 0,
    other_release_names: [],
    rejected_instances: [],
    truncated: false,
  },
  subscribers,
  current_revision: revision,
  server_time_unix_ms: 5,
});

function abortError(): Error {
  return new DOMException("aborted", "AbortError");
}

/** A stream mock that stays open until its signal aborts, exposing onSnapshot. */
function openStream() {
  const handle: { push?: (s: SubscriberStreamSnapshot) => void; end?: () => void } = {};
  mocks.subscriberStream.mockImplementationOnce(
    (_ns, _name, { signal, onSnapshot }) =>
      new Promise<void>((resolve, reject) => {
        handle.push = onSnapshot;
        handle.end = resolve;
        signal?.addEventListener("abort", () => reject(abortError()), { once: true });
      }),
  );
  return handle;
}

describe("useReleaseSubscribers", () => {
  beforeEach(() => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    mocks.releaseSubscribers.mockReset();
    mocks.subscriberStream.mockReset();
    mocks.releaseSubscribers.mockResolvedValue({
      subscribers: [row({ state: "prepared", activation_revision: 41 })],
      current_revision: 41,
      next_page_token: "",
    });
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  it("loads the paged state first, then switches to the stream on the first snapshot", async () => {
    const stream = openStream();
    const { result, unmount } = renderHook(() => useReleaseSubscribers(ns, "runtime"));
    expect(result.current.transport).toBe("off");

    await waitFor(() => expect(result.current.instances).toHaveLength(1));
    expect(mocks.releaseSubscribers).toHaveBeenCalledWith(ns, "runtime", 1000, undefined, {
      signal: expect.any(AbortSignal),
    });
    expect(result.current.instances[0]?.state).toBe("prepared");
    expect(result.current.currentRevision).toBe(41);
    expect(result.current.lastUpdatedAt).not.toBeNull();

    await waitFor(() => expect(stream.push).toBeDefined());
    expect(mocks.subscriberStream).toHaveBeenCalledWith(ns, "runtime", {
      signal: expect.any(AbortSignal),
      onSnapshot: expect.any(Function),
    });
    act(() => stream.push?.(snapshot([row({ state: "applied", activation_revision: 42 })], 42)));
    expect(result.current.transport).toBe("stream");
    expect(result.current.instances[0]?.state).toBe("applied");
    expect(result.current.currentRevision).toBe(42);
    expect(result.current.stale).toBe(false);

    const signal = mocks.subscriberStream.mock.calls[0]?.[2].signal as AbortSignal;
    unmount();
    expect(signal.aborted).toBe(true);
  });

  it("falls back to 5 s polling when the server has no stream endpoint", async () => {
    mocks.subscriberStream.mockRejectedValueOnce(new ApiError("unimplemented", "no stream", 404));
    const { result } = renderHook(() => useReleaseSubscribers(ns, "runtime"));
    await waitFor(() => expect(result.current.transport).toBe("poll"));
    expect(mocks.subscriberStream).toHaveBeenCalledTimes(1);
    expect(mocks.releaseSubscribers).toHaveBeenCalledTimes(1);

    await act(async () => {
      await vi.advanceTimersByTimeAsync(5_000);
    });
    expect(mocks.releaseSubscribers).toHaveBeenCalledTimes(2);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(5_000);
    });
    expect(mocks.releaseSubscribers).toHaveBeenCalledTimes(3);
    expect(result.current.stale).toBe(false);
  });

  it("reconnects once with jitter, then polls after two consecutive stream failures", async () => {
    vi.spyOn(Math, "random").mockReturnValue(0.5);
    mocks.subscriberStream
      .mockRejectedValueOnce(new ApiError("unavailable", "dropped", 0))
      .mockRejectedValueOnce(new ApiError("unavailable", "dropped again", 0));
    const { result } = renderHook(() => useReleaseSubscribers(ns, "runtime"));
    await waitFor(() => expect(mocks.subscriberStream).toHaveBeenCalledTimes(1));
    await waitFor(() => expect(result.current.stale).toBe(true));
    expect(result.current.transport).toBe("off");

    // attempt 1 → ceiling 1 s, random 0.5 → 500 ms before the second try.
    await act(async () => {
      await vi.advanceTimersByTimeAsync(500);
    });
    await waitFor(() => expect(mocks.subscriberStream).toHaveBeenCalledTimes(2));
    await waitFor(() => expect(result.current.transport).toBe("poll"));
    await act(async () => {
      await vi.advanceTimersByTimeAsync(5_000);
    });
    expect(mocks.releaseSubscribers).toHaveBeenCalledTimes(2);
    expect(mocks.subscriberStream).toHaveBeenCalledTimes(2);
  });

  it("reconnects after a clean server end and resets the failure count on a snapshot", async () => {
    vi.spyOn(Math, "random").mockReturnValue(1);
    const first = openStream();
    const second = openStream();
    const { result } = renderHook(() => useReleaseSubscribers(ns, "runtime"));
    await waitFor(() => expect(first.push).toBeDefined());
    act(() => first.push?.(snapshot([row({})], 41)));
    expect(result.current.transport).toBe("stream");
    act(() => first.end?.());
    await act(async () => {
      await vi.advanceTimersByTimeAsync(1_000);
    });
    await waitFor(() => expect(second.push).toBeDefined());
    expect(mocks.subscriberStream).toHaveBeenCalledTimes(2);
    expect(result.current.stale).toBe(false);
  });

  it("stays off and clears state when disabled or without a namespace", async () => {
    const { result, rerender } = renderHook(
      ({ enabled }: { enabled: boolean }) => useReleaseSubscribers(ns, "runtime", { enabled }),
      { initialProps: { enabled: false } },
    );
    expect(result.current.transport).toBe("off");
    expect(mocks.releaseSubscribers).not.toHaveBeenCalled();
    expect(mocks.subscriberStream).not.toHaveBeenCalled();

    openStream();
    rerender({ enabled: true });
    await waitFor(() => expect(result.current.instances).toHaveLength(1));
    rerender({ enabled: false });
    await waitFor(() => expect(result.current.transport).toBe("off"));
    expect(result.current.instances).toEqual([]);

    const { result: noNs } = renderHook(() => useReleaseSubscribers(null, "runtime"));
    expect(noNs.current.transport).toBe("off");
  });

  it("uses polling only when transport is poll, and refresh() reloads on demand", async () => {
    const { result } = renderHook(() =>
      useReleaseSubscribers(ns, "runtime", { transport: "poll" }),
    );
    await waitFor(() => expect(result.current.transport).toBe("poll"));
    expect(mocks.subscriberStream).not.toHaveBeenCalled();
    mocks.releaseSubscribers.mockRejectedValueOnce(new Error("offline"));
    await act(async () => {
      await result.current.refresh();
    });
    expect(result.current.stale).toBe(true);
    await act(async () => {
      await result.current.refresh();
    });
    expect(result.current.stale).toBe(false);
  });

  it("does not poll while the tab is hidden and refreshes on return", async () => {
    mocks.subscriberStream.mockRejectedValueOnce(new ApiError("unimplemented", "no stream", 501));
    const { result } = renderHook(() => useReleaseSubscribers(ns, "runtime"));
    await waitFor(() => expect(result.current.transport).toBe("poll"));
    const calls = mocks.releaseSubscribers.mock.calls.length;

    Object.defineProperty(document, "hidden", { value: true, configurable: true });
    document.dispatchEvent(new Event("visibilitychange"));
    await act(async () => {
      await vi.advanceTimersByTimeAsync(12_000);
    });
    expect(mocks.releaseSubscribers).toHaveBeenCalledTimes(calls);

    Object.defineProperty(document, "hidden", { value: false, configurable: true });
    await act(async () => {
      document.dispatchEvent(new Event("visibilitychange"));
    });
    await waitFor(() => expect(mocks.releaseSubscribers).toHaveBeenCalledTimes(calls + 1));
  });
});

describe("reconnectDelay", () => {
  it("doubles the ceiling from 1 s and caps at 30 s with full jitter", () => {
    expect(reconnectDelay(1, () => 1)).toBe(1_000);
    expect(reconnectDelay(2, () => 1)).toBe(2_000);
    expect(reconnectDelay(5, () => 1)).toBe(16_000);
    expect(reconnectDelay(6, () => 1)).toBe(30_000);
    expect(reconnectDelay(40, () => 1)).toBe(30_000);
    expect(reconnectDelay(3, () => 0)).toBe(0);
    expect(reconnectDelay(3, () => 0.25)).toBe(1_000);
  });
});
