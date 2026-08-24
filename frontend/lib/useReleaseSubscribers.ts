// Live rollout state for one release name in one namespace.
//
// Initial load through the paged endpoint, then a fetch-streamed SSE
// subscription when the server offers one. The stream reconnects with full
// jitter (1 s → 30 s); after two consecutive failures — or as soon as the
// server says the endpoint does not exist — the hook falls back to the 5 s
// visibility-gated polling the Subscribers page uses. Everything stops on
// unmount or when `enabled` flips off.

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { ApiError, api, isAbortError } from "@/lib/api";
import { useLatestRequest } from "@/lib/hooks";
import { groupSubscriberInstances } from "@/lib/subscribers";
import type { NamespaceRef, SubscriberInstance } from "@/lib/types";

export type SubscriberTransport = "stream" | "poll" | "off";

export interface ReleaseSubscribersState {
  instances: SubscriberInstance[];
  currentRevision: number;
  transport: SubscriberTransport;
  /** The last refresh failed (or the stream dropped); data may be behind. */
  stale: boolean;
  lastUpdatedAt: number | null;
  refresh: () => Promise<void>;
}

export interface UseReleaseSubscribersOptions {
  enabled?: boolean;
  /** `poll` skips the stream entirely (tests, constrained proxies). */
  transport?: "auto" | "poll";
}

export const POLL_INTERVAL_MS = 5_000;
export const RECONNECT_BASE_MS = 1_000;
export const RECONNECT_MAX_MS = 30_000;
export const STREAM_FAILURES_BEFORE_POLL = 2;

/** Full-jitter backoff: uniform in [0, min(max, base·2^(attempt-1))]. */
export function reconnectDelay(attempt: number, random: () => number = Math.random): number {
  const ceiling = Math.min(RECONNECT_MAX_MS, RECONNECT_BASE_MS * 2 ** Math.max(0, attempt - 1));
  return Math.floor(random() * ceiling);
}

const sleep = (ms: number, signal: AbortSignal): Promise<void> =>
  new Promise((resolve) => {
    if (signal.aborted) return resolve();
    const timer = globalThis.setTimeout(done, ms);
    function done() {
      signal.removeEventListener("abort", done);
      globalThis.clearTimeout(timer);
      resolve();
    }
    signal.addEventListener("abort", done, { once: true });
  });

export function useReleaseSubscribers(
  ns: NamespaceRef | null,
  name: string,
  opts: UseReleaseSubscribersOptions = {},
): ReleaseSubscribersState {
  const enabled = (opts.enabled ?? true) && ns !== null && name !== "";
  const mode = opts.transport ?? "auto";
  const env = ns?.env ?? "";
  const app = ns?.app ?? "";

  const [instances, setInstances] = useState<SubscriberInstance[]>([]);
  const [currentRevision, setCurrentRevision] = useState(0);
  const [transport, setTransport] = useState<SubscriberTransport>("off");
  const [stale, setStale] = useState(false);
  const [lastUpdatedAt, setLastUpdatedAt] = useState<number | null>(null);
  const request = useLatestRequest();
  // The stream has its own controller: a refresh must not abort it.
  const mounted = useRef(false);

  const refresh = useCallback(async () => {
    if (!enabled) return;
    const run = request.begin();
    try {
      const page = await api.releaseSubscribers({ env, app }, name, 1000, undefined, {
        signal: run.signal,
      });
      if (!run.current) return;
      setInstances(groupSubscriberInstances(page.subscribers ?? []));
      setCurrentRevision(page.current_revision ?? 0);
      setLastUpdatedAt(Date.now());
      setStale(false);
    } catch (err) {
      if (!run.current || isAbortError(err)) return;
      setStale(true);
    }
  }, [enabled, env, app, name, request]);

  // biome-ignore lint/correctness/useExhaustiveDependencies: `refresh` already encodes the namespace, name and enabled flag; listing them again would only restart the stream twice.
  useEffect(() => {
    mounted.current = true;
    if (!enabled) {
      setTransport("off");
      setInstances([]);
      setCurrentRevision(0);
      setStale(false);
      setLastUpdatedAt(null);
      return;
    }

    const controller = new AbortController();
    const { signal } = controller;
    let pollTimer: number | undefined;
    let polling = false;

    const schedulePoll = () => {
      if (signal.aborted || document.hidden || pollTimer !== undefined) return;
      pollTimer = window.setTimeout(async () => {
        pollTimer = undefined;
        await refresh();
        schedulePoll();
      }, POLL_INTERVAL_MS);
    };
    const onVisibilityChange = () => {
      if (pollTimer !== undefined) window.clearTimeout(pollTimer);
      pollTimer = undefined;
      if (!document.hidden) void refresh().finally(schedulePoll);
    };
    const startPolling = () => {
      if (polling || signal.aborted) return;
      polling = true;
      setTransport("poll");
      document.addEventListener("visibilitychange", onVisibilityChange);
      schedulePoll();
    };

    const streamLoop = async () => {
      let failures = 0;
      let attempt = 0;
      while (!signal.aborted) {
        try {
          await api.subscriberStream({ env, app }, name, {
            signal,
            onSnapshot: (snapshot) => {
              if (signal.aborted) return;
              failures = 0;
              attempt = 0;
              setInstances(groupSubscriberInstances(snapshot.subscribers ?? []));
              setCurrentRevision(snapshot.current_revision ?? 0);
              setLastUpdatedAt(Date.now());
              setStale(false);
              setTransport("stream");
            },
          });
          // The server ended the stream cleanly; reconnect without penalty.
          if (signal.aborted) return;
        } catch (err) {
          if (signal.aborted || isAbortError(err)) return;
          if (err instanceof ApiError && err.code === "unimplemented") {
            startPolling();
            return;
          }
          failures += 1;
          setStale(true);
          if (failures >= STREAM_FAILURES_BEFORE_POLL) {
            startPolling();
            return;
          }
        }
        attempt += 1;
        await sleep(reconnectDelay(attempt), signal);
      }
    };

    void refresh().finally(() => {
      if (signal.aborted) return;
      if (mode === "poll") startPolling();
      else void streamLoop();
    });

    return () => {
      mounted.current = false;
      controller.abort();
      if (pollTimer !== undefined) window.clearTimeout(pollTimer);
      document.removeEventListener("visibilitychange", onVisibilityChange);
    };
  }, [enabled, mode, refresh]);

  return useMemo(
    () => ({ instances, currentRevision, transport, stale, lastUpdatedAt, refresh }),
    [instances, currentRevision, transport, stale, lastUpdatedAt, refresh],
  );
}
