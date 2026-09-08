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
  schemaVersion?: number;
}

export const POLL_INTERVAL_MS = 5_000;
export const RECONNECT_BASE_MS = 1_000;
export const RECONNECT_MAX_MS = 30_000;
export const STREAM_FAILURES_BEFORE_POLL = 2;

type ReleaseSubscribersData = Omit<ReleaseSubscribersState, "refresh">;

const emptySubscriberData = (): ReleaseSubscribersData => ({
  instances: [],
  currentRevision: 0,
  transport: "off",
  stale: false,
  lastUpdatedAt: null,
});

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
  const schemaVersion = opts.schemaVersion ?? 0;
  const env = ns?.env ?? "";
  const app = ns?.app ?? "";
  const scope = enabled ? JSON.stringify([env, app, name, schemaVersion]) : "";
  const scopeRef = useRef({ scope, generation: 0 });
  if (scopeRef.current.scope !== scope) {
    scopeRef.current = { scope, generation: scopeRef.current.generation + 1 };
  }
  const generation = scopeRef.current.generation;
  const [result, setResult] = useState<{
    scope: string;
    generation: number;
    data: ReleaseSubscribersData;
  }>(() => ({ scope, generation, data: emptySubscriberData() }));
  const request = useLatestRequest();

  const isCurrentScope = useCallback(
    () =>
      scopeRef.current.scope === scope &&
      scopeRef.current.generation === generation &&
      scope !== "",
    [scope, generation],
  );

  const update = useCallback(
    (change: Partial<ReleaseSubscribersData>) => {
      if (!isCurrentScope()) return;
      setResult((current) => ({
        scope,
        generation,
        data: {
          ...(current.scope === scope && current.generation === generation
            ? current.data
            : emptySubscriberData()),
          ...change,
        },
      }));
    },
    [generation, isCurrentScope, scope],
  );

  const refresh = useCallback(async () => {
    if (!enabled) return;
    const run = request.begin();
    try {
      const page = await api.releaseSubscribers(
        { env, app },
        name,
        1000,
        undefined,
        { signal: run.signal },
        schemaVersion,
      );
      if (!run.current || !isCurrentScope()) return;
      update({
        instances: groupSubscriberInstances(page.subscribers ?? []),
        currentRevision: page.current_revision ?? 0,
        lastUpdatedAt: Date.now(),
        stale: false,
      });
    } catch (err) {
      if (!run.current || !isCurrentScope() || isAbortError(err)) return;
      update({ stale: true });
    }
  }, [enabled, env, app, name, request, schemaVersion, isCurrentScope, update]);

  useEffect(() => {
    if (!enabled) {
      setResult({ scope, generation, data: emptySubscriberData() });
      return;
    }

    setResult((current) =>
      current.scope === scope && current.generation === generation
        ? current
        : { scope, generation, data: emptySubscriberData() },
    );

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
      update({ transport: "poll" });
      document.addEventListener("visibilitychange", onVisibilityChange);
      schedulePoll();
    };

    const streamLoop = async () => {
      let failures = 0;
      let attempt = 0;
      while (!signal.aborted) {
        try {
          await api.subscriberStream({ env, app }, name, schemaVersion, {
            signal,
            onSnapshot: (snapshot) => {
              if (signal.aborted || !isCurrentScope()) return;
              failures = 0;
              attempt = 0;
              update({
                instances: groupSubscriberInstances(snapshot.subscribers ?? []),
                currentRevision: snapshot.current_revision ?? 0,
                lastUpdatedAt: Date.now(),
                stale: false,
                transport: "stream",
              });
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
          update({ stale: true });
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
      controller.abort();
      if (pollTimer !== undefined) window.clearTimeout(pollTimer);
      document.removeEventListener("visibilitychange", onVisibilityChange);
    };
  }, [
    enabled,
    mode,
    refresh,
    generation,
    isCurrentScope,
    scope,
    update,
    env,
    app,
    name,
    schemaVersion,
  ]);

  const data =
    result.scope === scope && result.generation === generation && enabled
      ? result.data
      : emptySubscriberData();

  return useMemo(() => ({ ...data, refresh }), [data, refresh]);
}
