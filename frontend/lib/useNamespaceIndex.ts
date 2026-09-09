import { useCallback, useEffect, useRef, useState } from "react";
import { isAbortError } from "./api";
import { useLatestRequest } from "./hooks";
import { INDEX_MAX_PAGES } from "./key-search";

export interface IndexPage<T> {
  items: T[];
  /** The next page token, "" when this was the last page. */
  next: string;
}

export interface NamespaceIndex<T> {
  rows: T[];
  /** False when the walk stopped at `INDEX_MAX_PAGES` with pages still to come. */
  complete: boolean;
  loading: boolean;
  /** A walk has finished for exactly this scope; the rows below are its answer. */
  ready: boolean;
  /**
   * What the walk failed with, if it did. `ready` stays false and nothing is
   * cached, so a caller's retry is a real retry: an empty index is never
   * mistaken for "the namespace has nothing matching".
   */
  error: unknown;
  /** Drop the cache and walk again — call it after every write, and to retry. */
  invalidate: () => void;
}

interface IndexState<T> {
  scope: string;
  rows: T[];
  complete: boolean;
  loading: boolean;
  ready: boolean;
  error: unknown;
}

function idle<T>(scope: string): IndexState<T> {
  return { scope, rows: [], complete: true, loading: false, ready: false, error: null };
}

/**
 * Loads a whole namespace on demand so a search can reach keys the server's
 * 100-row page never showed. Walks the cursor until the
 * server runs out or `INDEX_MAX_PAGES` is reached, and caches the answer for
 * the scope it was loaded for.
 *
 * `fetchPage` and `onError` are read through refs, so they need no memoising;
 * `scope` is what identifies the list, and changing it starts a new walk.
 * The hook owns its own `useLatestRequest` — sharing a page's instance would
 * let the browse list's next load abort this walk, and vice versa.
 */
export function useNamespaceIndex<T>(
  scope: string,
  enabled: boolean,
  fetchPage: (token: string, signal: AbortSignal) => Promise<IndexPage<T>>,
  onError?: (error: unknown) => void,
): NamespaceIndex<T> {
  const request = useLatestRequest();
  const cache = useRef<{ scope: string; rows: T[]; complete: boolean } | null>(null);
  const [generation, setGeneration] = useState(0);
  const [state, setState] = useState<IndexState<T>>(() => idle<T>(scope));

  const fetchRef = useRef(fetchPage);
  fetchRef.current = fetchPage;
  const errorRef = useRef(onError);
  errorRef.current = onError;

  const invalidate = useCallback(() => {
    cache.current = null;
    setGeneration((current) => current + 1);
  }, []);

  // Loading from an effect is fine here; only `replaceQuery` is forbidden in
  // one (see lib/url.ts), and this hook never touches the URL.
  // biome-ignore lint/correctness/useExhaustiveDependencies: `fetchPage`/`onError` are read through refs on purpose, and `generation` is the invalidation signal.
  useEffect(() => {
    if (!enabled) {
      // A walk that failed is forgotten along with the search that ran it, so
      // the next one starts from a clean slate rather than a stale error.
      setState((current) =>
        current.scope === scope && !current.ready && !current.loading && !current.error
          ? current
          : idle<T>(scope),
      );
      return;
    }
    const cached = cache.current;
    if (cached && cached.scope === scope) {
      setState({
        scope,
        rows: cached.rows,
        complete: cached.complete,
        loading: false,
        ready: true,
        error: null,
      });
      return;
    }
    const run = request.begin();
    setState({ scope, rows: [], complete: false, loading: true, ready: false, error: null });
    void (async () => {
      const all: T[] = [];
      // The same guard `loadNamespaces` uses: a server that keeps handing back
      // the token it was given would otherwise spin until the page cap.
      const seen = new Set<string>();
      let token = "";
      let complete = false;
      try {
        for (let page = 0; page < INDEX_MAX_PAGES; page += 1) {
          const res = await fetchRef.current(token, run.signal);
          if (!run.current) return;
          all.push(...res.items);
          const next = res.next;
          if (!next || seen.has(next)) {
            complete = true;
            break;
          }
          seen.add(next);
          token = next;
        }
        if (!run.current) return;
        cache.current = { scope, rows: all, complete };
        setState({ scope, rows: all, complete, loading: false, ready: true, error: null });
      } catch (error) {
        if (!run.current || isAbortError(error)) return;
        errorRef.current?.(error);
        // Nothing is cached and `ready` stays false: an index that failed to
        // load must not render as a namespace with no matches.
        setState({ scope, rows: [], complete: true, loading: false, ready: false, error });
      }
    })();
  }, [scope, enabled, generation, request]);

  const current = state.scope === scope ? state : idle<T>(scope);
  return {
    rows: current.rows,
    complete: current.complete,
    loading: current.loading,
    ready: current.ready,
    error: current.error,
    invalidate,
  };
}
