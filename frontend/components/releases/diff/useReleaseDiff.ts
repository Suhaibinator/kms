// The one request behind a release comparison. Re-fetches when the query
// changes; a stale response is dropped by generation, never by aborting in
// an effect cleanup keyed on state (the JSON-editor pass gotcha).

import { useCallback, useEffect, useMemo, useState } from "react";
import { api, isAbortError, type ReleaseDiffQuery } from "@/lib/api";
import { useLatestRequest } from "@/lib/hooks";
import type { ReleaseDiffResponse } from "@/lib/types";

export interface ReleaseDiffState {
  /** The current response, or the previous one while a new query loads (`stale`). */
  diff: ReleaseDiffResponse | null;
  loading: boolean;
  /** True while `diff` belongs to the previous query and the new one is in flight. */
  stale: boolean;
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
  // Keep the previous comparison on screen while the next one loads (a swap,
  // a step, a picker change): the toolbar, filter and expanded rows stay
  // mounted instead of collapsing to a skeleton and back. The view dims and
  // disables the stale body; the first load still shows the skeleton.
  const stale = Boolean(key) && !settled && state.diff !== null;
  return useMemo(
    () => ({
      diff: settled || stale ? state.diff : null,
      loading: Boolean(key) && !settled,
      stale,
      error: settled ? state.error : null,
      reload,
    }),
    [key, settled, stale, state.diff, state.error, reload],
  );
}
