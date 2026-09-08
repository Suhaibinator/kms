import { act, render, renderHook, screen, waitFor } from "@testing-library/react";
import { startTransition, Suspense, useState } from "react";
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
  applied_divergent: false,
  divergent_field_count: 0,
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
    applied_divergent: 0,
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

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => {
    resolve = done;
  });
  return { promise, resolve };
}

/** A stream mock that stays open until its signal aborts, exposing onSnapshot. */
function openStream() {
  const handle: { push?: (s: SubscriberStreamSnapshot) => void; end?: () => void } = {};
  mocks.subscriberStream.mockImplementationOnce(
    (_ns, _name, _schemaVersion, { signal, onSnapshot }) =>
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
    expect(mocks.releaseSubscribers).toHaveBeenCalledWith(
      ns,
      "runtime",
      1000,
      undefined,
      { signal: expect.any(AbortSignal) },
      0,
    );
    expect(result.current.instances[0]?.state).toBe("prepared");
    expect(result.current.currentRevision).toBe(41);
    expect(result.current.lastUpdatedAt).not.toBeNull();

    await waitFor(() => expect(stream.push).toBeDefined());
    expect(mocks.subscriberStream).toHaveBeenCalledWith(ns, "runtime", 0, {
      signal: expect.any(AbortSignal),
      onSnapshot: expect.any(Function),
    });
    act(() => stream.push?.(snapshot([row({ state: "applied", activation_revision: 42 })], 42)));
    expect(result.current.transport).toBe("stream");
    expect(result.current.instances[0]?.state).toBe("applied");
    expect(result.current.currentRevision).toBe(42);
    expect(result.current.stale).toBe(false);

    const signal = mocks.subscriberStream.mock.calls[0]?.[3].signal as AbortSignal;
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

  it.each([
    ["environment", { env: "staging", app: "gradethis" }, "runtime", 1],
    ["application", { env: "prod", app: "billing" }, "runtime", 1],
    ["release name", ns, "batch", 1],
    ["schema", ns, "runtime", 2],
  ])(
    "hides prior-track rows during a delayed %s switch",
    async (_label, nextNS, nextName, nextSchema) => {
      const second = deferred<{
        subscribers: ReleaseSubscriberState[];
        current_revision: number;
        next_page_token: string;
      }>();
      mocks.releaseSubscribers
        .mockResolvedValueOnce({
          subscribers: [row({ release_version: 1, activation_revision: 7 })],
          current_revision: 7,
          next_page_token: "",
        })
        .mockImplementationOnce(() => second.promise);
      openStream();
      openStream();
      const { result, rerender } = renderHook(
        ({ targetNS, targetName, schemaVersion }) =>
          useReleaseSubscribers(targetNS, targetName, { schemaVersion }),
        { initialProps: { targetNS: ns, targetName: "runtime", schemaVersion: 1 } },
      );
      await waitFor(() => expect(result.current.instances).toHaveLength(1));

      rerender({ targetNS: nextNS, targetName: nextName, schemaVersion: nextSchema });
      expect(result.current.instances).toEqual([]);
      expect(result.current.currentRevision).toBe(0);
      expect(result.current.transport).toBe("off");
      expect(result.current.stale).toBe(false);
      expect(result.current.lastUpdatedAt).toBeNull();

      second.resolve({ subscribers: [], current_revision: 9, next_page_token: "" });
      await waitFor(() => expect(result.current.currentRevision).toBe(9));
    },
  );

  it("rejects stale list and SSE callbacks after switching away and back to the same track", async () => {
    const oldList = deferred<{
      subscribers: ReleaseSubscriberState[];
      current_revision: number;
      next_page_token: string;
    }>();
    mocks.releaseSubscribers
      .mockResolvedValueOnce({ subscribers: [], current_revision: 10, next_page_token: "" })
      .mockImplementationOnce(() => oldList.promise)
      .mockResolvedValueOnce({ subscribers: [], current_revision: 20, next_page_token: "" })
      .mockResolvedValueOnce({ subscribers: [], current_revision: 30, next_page_token: "" });
    const oldStream = openStream();
    openStream();
    openStream();
    const { result, rerender } = renderHook(
      ({ schemaVersion }) => useReleaseSubscribers(ns, "runtime", { schemaVersion }),
      { initialProps: { schemaVersion: 1 } },
    );
    await waitFor(() => expect(oldStream.push).toBeDefined());
    void result.current.refresh();
    await waitFor(() => expect(mocks.releaseSubscribers).toHaveBeenCalledTimes(2));
    rerender({ schemaVersion: 2 });
    await waitFor(() => expect(result.current.currentRevision).toBe(20));
    rerender({ schemaVersion: 1 });
    expect(result.current.instances).toEqual([]);
    await waitFor(() => expect(result.current.currentRevision).toBe(30));

    act(() => {
      oldList.resolve({
        subscribers: [row({ release_version: 1, activation_revision: 7 })],
        current_revision: 7,
        next_page_token: "",
      });
      oldStream.push?.(snapshot([row({ release_version: 1, activation_revision: 8 })], 8));
    });
    await act(async () => Promise.resolve());
    expect(result.current.instances).toEqual([]);
    expect(result.current.currentRevision).toBe(30);
  });

  it("hides state synchronously and ignores callbacks after disabling", async () => {
    const stream = openStream();
    const { result, rerender } = renderHook(
      ({ enabled }) => useReleaseSubscribers(ns, "runtime", { enabled, schemaVersion: 4 }),
      { initialProps: { enabled: true } },
    );
    await waitFor(() => expect(result.current.instances).toHaveLength(1));
    await waitFor(() => expect(stream.push).toBeDefined());

    rerender({ enabled: false });
    expect(result.current).toMatchObject({
      instances: [],
      currentRevision: 0,
      transport: "off",
      stale: false,
      lastUpdatedAt: null,
    });
    act(() => stream.push?.(snapshot([row({ activation_revision: 99 })], 99)));
    expect(result.current.instances).toEqual([]);
    expect(result.current.currentRevision).toBe(0);
  });

  it("keeps the committed track live when a proposed track render suspends", async () => {
    const stream = openStream();
    const never = new Promise<void>(() => {});
    let selectSchema!: (version: number) => void;
    function Harness() {
      const [schemaVersion, setSchemaVersion] = useState(1);
      selectSchema = setSchemaVersion;
      const live = useReleaseSubscribers(ns, "runtime", { schemaVersion });
      if (schemaVersion === 2) throw never;
      return <output data-testid="committed-revision">{live.currentRevision}</output>;
    }
    render(
      <Suspense fallback={<span>Loading proposed track</span>}>
        <Harness />
      </Suspense>,
    );
    await waitFor(() => expect(stream.push).toBeDefined());
    await waitFor(() => expect(screen.getByTestId("committed-revision")).toHaveTextContent("41"));

    act(() => {
      startTransition(() => selectSchema(2));
    });
    expect(screen.queryByText("Loading proposed track")).not.toBeInTheDocument();
    act(() => stream.push?.(snapshot([row({ activation_revision: 44 })], 44)));
    expect(screen.getByTestId("committed-revision")).toHaveTextContent("44");
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
