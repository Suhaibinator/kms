// The one request behind a release comparison. Re-fetches when the query
// changes; a stale response is dropped by generation, never by aborting in
// an effect cleanup keyed on state (the JSON-editor pass gotcha).

import { useCallback, useEffect, useMemo, useState } from "react";
import { api, isAbortError, type ReleaseDiffQuery } from "@/lib/api";
import { useLatestRequest } from "@/lib/hooks";
import type { ReleaseDiffResponse } from "@/lib/types";

export interface ReleaseDiffState {
  diff: ReleaseDiffResponse | null;
  loading: boolean;
  error: unknown;
  reload: () => void;
}

/** `null` skips the request (a page still resolving its params). */
export function useReleaseDiff(query: ReleaseDiffQuery | null): ReleaseDiffState {
  const key = query ? JSON.stringify(query) : "";
  const request = useLatestRequest();
  const [attempt, setAttempt] = useState(0);
  const [state, setState] = useState<{
    key: string;
    diff: ReleaseDiffResponse | null;
    error: unknown;
  }>({ key: "", diff: null, error: null });

  // biome-ignore lint/correctness/useExhaustiveDependencies: `attempt` exists only to re-run this load from Retry.
  useEffect(() => {
    if (!key) return;
    const parsed = JSON.parse(key) as ReleaseDiffQuery;
    const run = request.begin();
    api
      .releaseDiff(parsed, { signal: run.signal })
      .then((diff) => {
        if (run.current) setState({ key, diff, error: null });
      })
      .catch((error: unknown) => {
        if (isAbortError(error) || !run.current) return;
        setState({ key, diff: null, error });
      });
  }, [key, attempt, request]);

  const reload = useCallback(() => setAttempt((n) => n + 1), []);
  const settled = state.key === key;
  return useMemo(
    () => ({
      diff: settled ? state.diff : null,
      loading: Boolean(key) && !settled,
      error: settled ? state.error : null,
      reload,
    }),
    [key, settled, state.diff, state.error, reload],
  );
}
