// Live rollout state for one release name in one namespace.
//
// Initial load through the paged endpoint, then a fetch-streamed SSE
// subscription when the server offers one. The stream reconnects with full
// jitter (1 s → 30 s); after two consecutive failures — or as soon as the
// server says the endpoint does not exist — the hook falls back to the 5 s
// visibility-gated polling the Subscribers page uses. Everything stops on
// unmount or when `enabled` flips off. Manual refresh retires the stream and
// keeps this scope on polling, preventing buffered frames racing the page.

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { ApiError, api, isAbortError } from "@/lib/api";
import { useLatestRequest } from "@/lib/hooks";
import type { NamespaceRef, OverviewRollout, SubscriberInstance } from "@/lib/types";

export type SubscriberTransport = "stream" | "poll" | "off";

export interface ReleaseSubscribersState {
  instances: SubscriberInstance[];
  summary: OverviewRollout | null;
  projectionRevision: string | null;
  truncated: boolean;
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
  summary: null,
  projectionRevision: null,
  truncated: false,
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
  const [result, setResult] = useState<{
    scope: string;
    data: ReleaseSubscribersData;
  }>(() => ({ scope, data: emptySubscriberData() }));
  const request = useLatestRequest();
  const manualOwner = useRef<{ scope: string; takePolling: () => void } | null>(null);

  const update = useCallback(
    (change: Partial<ReleaseSubscribersData>) => {
      if (scope === "") return;
      setResult((current) =>
        current.scope === scope ? { scope, data: { ...current.data, ...change } } : current,
      );
    },
    [scope],
  );

  const loadPage = useCallback(async () => {
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
      if (!run.current) return;
      update({
        instances: page.instances ?? [],
        summary: page.summary ?? null,
        projectionRevision: page.projection_revision ?? null,
        truncated: !!page.next_page_token,
        currentRevision: page.current_revision ?? 0,
        lastUpdatedAt: Date.now(),
        stale: !page.summary?.complete || !page.instances || !page.projection_revision,
      });
    } catch (err) {
      if (!run.current || isAbortError(err)) return;
      update({ stale: true });
    }
  }, [enabled, env, app, name, request, schemaVersion, update]);

  const refresh = useCallback(async () => {
    // Projection revisions are content identities, not clocks. Stop accepting
    // stream frames before fetching a manual refresh: a buffered older frame
    // must never overwrite a newer completed page. Keep polling until the
    // scope changes so there is exactly one snapshot source.
    if (manualOwner.current?.scope !== scope) return;
    manualOwner.current.takePolling();
    await loadPage();
  }, [loadPage, scope]);

  useEffect(() => {
    if (!enabled) {
      setResult({ scope, data: emptySubscriberData() });
      return;
    }

    setResult((current) =>
      current.scope === scope ? current : { scope, data: emptySubscriberData() },
    );

    const controller = new AbortController();
    const { signal } = controller;
    const streamController = new AbortController();
    const streamSignal = streamController.signal;
    let pollTimer: number | undefined;
    let polling = false;

    const schedulePoll = () => {
      if (signal.aborted || document.hidden || pollTimer !== undefined) return;
      pollTimer = window.setTimeout(async () => {
        pollTimer = undefined;
        await loadPage();
        schedulePoll();
      }, POLL_INTERVAL_MS);
    };
    const onVisibilityChange = () => {
      if (pollTimer !== undefined) window.clearTimeout(pollTimer);
      pollTimer = undefined;
      if (!document.hidden) void loadPage().finally(schedulePoll);
    };
    const startPolling = () => {
      if (polling || signal.aborted) return;
      polling = true;
      update({ transport: "poll" });
      document.addEventListener("visibilitychange", onVisibilityChange);
      schedulePoll();
    };

    const owner = {
      scope,
      takePolling: () => {
        streamController.abort();
        startPolling();
      },
    };
    manualOwner.current = owner;

    const streamLoop = async () => {
      let failures = 0;
      let attempt = 0;
      while (!signal.aborted && !streamSignal.aborted) {
        try {
          await api.subscriberStream({ env, app }, name, schemaVersion, {
            signal: streamSignal,
            onSnapshot: (snapshot) => {
              if (signal.aborted || streamSignal.aborted) return;
              // A stream frame supersedes any in-flight poll as one atomic
              // projection. Never let a delayed page replace its counts/rows.
              request.abort();
              failures = 0;
              attempt = 0;
              update({
                instances: snapshot.instances ?? [],
                summary: snapshot.summary ?? null,
                projectionRevision: snapshot.projection_revision ?? null,
                truncated: snapshot.summary?.truncated ?? false,
                currentRevision: snapshot.current_revision ?? 0,
                lastUpdatedAt: Date.now(),
                stale:
                  !snapshot.summary?.complete ||
                  !snapshot.instances ||
                  !snapshot.projection_revision,
                transport: "stream",
              });
            },
          });
          // The server ended the stream cleanly; reconnect without penalty.
          if (signal.aborted || streamSignal.aborted) return;
        } catch (err) {
          if (signal.aborted || streamSignal.aborted || isAbortError(err)) return;
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
        await sleep(reconnectDelay(attempt), streamSignal);
      }
    };

    void loadPage().finally(() => {
      if (signal.aborted) return;
      if (mode === "poll" || streamSignal.aborted) startPolling();
      else void streamLoop();
    });

    return () => {
      controller.abort();
      streamController.abort();
      if (manualOwner.current === owner) manualOwner.current = null;
      if (pollTimer !== undefined) window.clearTimeout(pollTimer);
      document.removeEventListener("visibilitychange", onVisibilityChange);
    };
  }, [enabled, mode, loadPage, scope, update, env, app, name, schemaVersion, request]);

  const data = result.scope === scope && enabled ? result.data : emptySubscriberData();

  return useMemo(() => ({ ...data, refresh }), [data, refresh]);
}
