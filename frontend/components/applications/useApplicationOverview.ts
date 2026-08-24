import { useCallback, useEffect, useState } from "react";
import { useToast } from "@/context/ToastContext";
import { ApiError, api, isAbortError } from "@/lib/api";
import { useLatestRequest } from "@/lib/hooks";
import type { ApplicationOverview } from "@/lib/types";

export type OverviewStatus = "loading" | "success" | "not-found" | "forbidden" | "error";

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

export function useApplicationOverview(name: string): {
  /** Only ever the slot for `name`; null before the first response. */
  slot: OverviewSlot | null;
  loading: boolean;
  reload: () => Promise<void>;
} {
  const toast = useToast();
  const request = useLatestRequest();
  const [slot, setSlot] = useState<OverviewSlot | null>(null);
  const [loading, setLoading] = useState(false);

  const reload = useCallback(async () => {
    if (!name) return;
    const run = request.begin();
    setLoading(true);
    setSlot((current) =>
      current?.name === name
        ? { ...current, status: "loading" }
        : { name, status: "loading", data: null },
    );
    try {
      const data = await api.applicationOverview(name, undefined, { signal: run.signal });
      if (!run.current) return;
      setSlot({ name, status: "success", data });
    } catch (error) {
      if (!run.current || isAbortError(error)) return;
      if (error instanceof ApiError && error.status === 404) {
        setSlot({ name, status: "not-found", data: null });
        return;
      }
      if (error instanceof ApiError && error.status === 403) {
        setSlot({ name, status: "forbidden", data: null });
        return;
      }
      setSlot((current) => ({
        name,
        status: "error",
        data: current?.name === name ? current.data : null,
      }));
      toast.error(error, "Failed to load application");
    } finally {
      if (run.current) setLoading(false);
    }
  }, [name, request, toast]);

  useEffect(() => {
    if (name) void reload();
  }, [name, reload]);

  return { slot: slot?.name === name ? slot : null, loading, reload };
}
