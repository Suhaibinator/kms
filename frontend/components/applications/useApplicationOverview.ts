import { useCallback, useEffect, useRef, useState } from "react";
import { useToast } from "@/context/ToastContext";
import { ApiError, api, isAbortError } from "@/lib/api";
import { useLatestRequest } from "@/lib/hooks";
import type { ApplicationOverview } from "@/lib/types";

export type OverviewStatus = "loading" | "success" | "not-found" | "forbidden" | "error";

/** How often an open application page checks whether it has fallen behind. */
export const OVERVIEW_CHECK_MS = 30_000;

/**
 * The overview is keyed by the application it belongs to, so a response that
 * lands after the user has switched to another application can never be
 * rendered under the new header. `data` survives a reload so the page does not
 * collapse to a skeleton while a refresh is in flight.
 */
export interface OverviewSlot {
  name: string;
  status: OverviewStatus;
  data: ApplicationOverview | null;
}

export interface OverviewFreshness {
  /** When the data on screen was loaded; null before the first response. */
  lastLoadedAt: number | null;
  /** `failed`: the last reload failed and older data is still shown.
   *  `changed`: a background check found lifecycle or release state the page does not show. */
  staleReason: "failed" | "changed" | null;
}

/**
 * Lifecycle or release activations that differ between two overviews of the
 * same application, as value-free lines ("application archived by alice" or
 * "prod: runtime@13 activated at rev 13 by alice"). Environments that appeared
 * or disappeared count as well.
 */
export function releaseMovements(prev: ApplicationOverview, next: ApplicationOverview): string[] {
  const before = new Map(prev.environments.map((env) => [env.namespace.env, env]));
  const after = new Map(next.environments.map((env) => [env.namespace.env, env]));
  const lines: string[] = [];
  const wasArchived = prev.application.archived_at_unix_ms > 0;
  const isArchived = next.application.archived_at_unix_ms > 0;
  if (
    prev.application.archived_at_unix_ms !== next.application.archived_at_unix_ms ||
    prev.application.archived_by !== next.application.archived_by
  ) {
    if (!wasArchived && isArchived) {
      lines.push(
        `application archived${next.application.archived_by ? ` by ${next.application.archived_by}` : ""}`,
      );
    } else if (wasArchived && !isArchived) {
      lines.push("application unarchived");
    } else {
      lines.push(
        `application archive record changed${next.application.archived_by ? ` by ${next.application.archived_by}` : ""}`,
      );
    }
  }
  for (const [name, env] of after) {
    const old = before.get(name);
    if (!old) {
      lines.push(`${name}: environment added`);
      continue;
    }
    const was = old.release.active?.activation_revision ?? 0;
    const active = env.release.active;
    const now = active?.activation_revision ?? 0;
    if (was === now) continue;
    lines.push(
      active
        ? `${name}: ${active.name}@${active.version} activated at rev ${active.activation_revision}${active.created_by ? ` by ${active.created_by}` : ""}`
        : `${name}: no release is active any more`,
    );
  }
  for (const name of before.keys()) {
    if (!after.has(name)) lines.push(`${name}: environment removed`);
  }
  return lines;
}

const changedToastId = (name: string) => `overview-changed:${name}`;

export function useApplicationOverview(name: string): {
  /** Only ever the slot for `name`; null before the first response. */
  slot: OverviewSlot | null;
  loading: boolean;
  reload: () => Promise<void>;
  freshness: OverviewFreshness;
} {
  const toast = useToast();
  const request = useLatestRequest();
  const [slot, setSlot] = useState<OverviewSlot | null>(null);
  const [loading, setLoading] = useState(false);
  const [freshness, setFreshness] = useState<OverviewFreshness>({
    lastLoadedAt: null,
    staleReason: null,
  });
  // What the page is showing, written the moment it is set rather than on
  // render, so the background check never compares against a stale commit.
  const shownRef = useRef<{ name: string; data: ApplicationOverview } | null>(null);
  const loadingRef = useRef(false);
  // Bumped when a reload starts; a check that began before it is discarded.
  const generationRef = useRef(0);

  const reload = useCallback(async () => {
    if (!name) return;
    const run = request.begin();
    generationRef.current += 1;
    loadingRef.current = true;
    setLoading(true);
    setSlot((current) =>
      current?.name === name
        ? { ...current, status: "loading" }
        : { name, status: "loading", data: null },
    );
    try {
      const data = await api.applicationOverview(name, undefined, { signal: run.signal });
      if (!run.current) return;
      shownRef.current = { name, data };
      setSlot({ name, status: "success", data });
      setFreshness({ lastLoadedAt: Date.now(), staleReason: null });
      toast.dismiss(changedToastId(name));
    } catch (error) {
      if (!run.current || isAbortError(error)) return;
      if (error instanceof ApiError && error.status === 404) {
        shownRef.current = null;
        setSlot({ name, status: "not-found", data: null });
        return;
      }
      if (error instanceof ApiError && error.status === 403) {
        shownRef.current = null;
        setSlot({ name, status: "forbidden", data: null });
        return;
      }
      setSlot((current) => ({
        name,
        status: "error",
        data: current?.name === name ? current.data : null,
      }));
      setFreshness((current) => ({ ...current, staleReason: "failed" }));
      toast.error(error, "Failed to load application");
    } finally {
      if (run.current) {
        loadingRef.current = false;
        setLoading(false);
      }
    }
  }, [name, request, toast]);

  useEffect(() => {
    if (name) void reload();
  }, [name, reload]);

  // Another operator can change lifecycle state, ship or roll back while this page is open. A
  // read-only check every OVERVIEW_CHECK_MS (visible tab, nothing in flight)
  // compares release activations and, when they moved, says so once with a
  // Reload action — it never swaps the data under an open modal.
  useEffect(() => {
    if (!name) return;
    let controller: AbortController | null = null;
    let disposed = false;
    const check = async () => {
      const shown = shownRef.current;
      if (disposed || document.hidden || loadingRef.current || controller || shown?.name !== name) {
        return;
      }
      const generation = generationRef.current;
      controller = new AbortController();
      try {
        const latest = await api.applicationOverview(name, undefined, {
          signal: controller.signal,
        });
        // A reload that started meanwhile supersedes this comparison.
        if (disposed || generationRef.current !== generation || loadingRef.current) return;
        const moved = releaseMovements(shown.data, latest);
        if (moved.length === 0) return;
        setFreshness((state) => ({ ...state, staleReason: "changed" }));
        toast.info("This application changed since the page loaded", moved.join(" · "), {
          id: changedToastId(name),
          duration: Number.POSITIVE_INFINITY,
          action: { label: "Reload", onClick: () => void reload() },
        });
      } catch {
        // A failed check is not news; the next tick tries again.
      } finally {
        controller = null;
      }
    };
    const timer = window.setInterval(() => void check(), OVERVIEW_CHECK_MS);
    const onVisibility = () => {
      if (!document.hidden) void check();
    };
    document.addEventListener("visibilitychange", onVisibility);
    return () => {
      disposed = true;
      window.clearInterval(timer);
      document.removeEventListener("visibilitychange", onVisibility);
      controller?.abort();
      toast.dismiss(changedToastId(name));
    };
  }, [name, reload, toast]);

  return { slot: slot?.name === name ? slot : null, loading, reload, freshness };
}
