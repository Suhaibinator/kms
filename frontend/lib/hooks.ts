import { useRouter } from "next/router";
import { useCallback, useEffect, useState } from "react";
import { api, getToken, isAbortError } from "./api";
import type { Namespace } from "./types";

// Reads a single query-string parameter. On a static export, router.query is
// empty until the client router hydrates, so callers must wait for `ready`
// before treating a missing value as "not provided".
export function useQueryParam(key: string): { value: string | null; ready: boolean } {
  const router = useRouter();
  if (!router.isReady) return { value: null, ready: false };
  const raw = router.query[key];
  const value = Array.isArray(raw) ? raw[0] : (raw ?? null);
  return { value: value ?? null, ready: true };
}

// Reads several query-string parameters at once. Same hydration caveat as
// useQueryParam: values are null until `ready`.
export function useQueryParams<K extends string>(
  keys: readonly K[],
): { values: Record<K, string | null>; ready: boolean } {
  const router = useRouter();
  const values = {} as Record<K, string | null>;
  const ready = router.isReady;
  for (const key of keys) {
    if (!ready) {
      values[key] = null;
      continue;
    }
    const raw = router.query[key];
    values[key] = (Array.isArray(raw) ? raw[0] : raw) ?? null;
  }
  return { values, ready };
}

interface CursorState {
  scope: string;
  tokens: string[];
  index: number;
  nextToken: string;
}

function initialCursor(scope: string): CursorState {
  return { scope, tokens: [""], index: 0, nextToken: "" };
}

export function useCursorPagination(scope: string) {
  const [state, setState] = useState<CursorState>(() => initialCursor(scope));
  const active = state.scope === scope ? state : initialCursor(scope);
  const pageToken = active.tokens[active.index] ?? "";

  useEffect(() => {
    setState((current) => (current.scope === scope ? current : initialCursor(scope)));
  }, [scope]);

  const setNextToken = useCallback(
    (nextToken: string) => {
      setState((current) => {
        const scoped = current.scope === scope ? current : initialCursor(scope);
        return scoped.nextToken === nextToken ? scoped : { ...scoped, nextToken };
      });
    },
    [scope],
  );

  const next = useCallback(() => {
    setState((current) => {
      const scoped = current.scope === scope ? current : initialCursor(scope);
      if (!scoped.nextToken || scoped.nextToken === pageToken) return scoped;
      return {
        ...scoped,
        tokens: [...scoped.tokens.slice(0, scoped.index + 1), scoped.nextToken],
        index: scoped.index + 1,
        nextToken: "",
      };
    });
  }, [pageToken, scope]);

  const previous = useCallback(() => {
    setState((current) => {
      const scoped = current.scope === scope ? current : initialCursor(scope);
      if (scoped.index === 0) return scoped;
      return { ...scoped, index: scoped.index - 1, nextToken: "" };
    });
  }, [scope]);

  const reset = useCallback(() => setState(initialCursor(scope)), [scope]);

  return {
    pageToken,
    page: active.index + 1,
    hasPrevious: active.index > 0,
    hasNext: Boolean(active.nextToken) && active.nextToken !== pageToken,
    setNextToken,
    next,
    previous,
    reset,
  };
}

interface NamespaceState {
  namespaces: Namespace[];
  loading: boolean;
  error: unknown;
  loaded: boolean;
}

const MAX_NAMESPACE_PAGES = 50;
const NAMESPACE_CACHE_TTL_MS = 30_000;
let namespaceState: NamespaceState = {
  namespaces: [],
  loading: true,
  error: null,
  loaded: false,
};
let namespaceLoadedAt = 0;
let namespaceToken: string | null | undefined;
let namespaceGeneration = 0;
let namespaceController: AbortController | null = null;
const namespaceListeners = new Set<(state: NamespaceState) => void>();

function publishNamespaces(next: NamespaceState): void {
  namespaceState = next;
  for (const listener of namespaceListeners) listener(next);
}

async function loadNamespaces(force: boolean): Promise<void> {
  const token = getToken();
  const sameSession = token === namespaceToken;
  const cacheFresh =
    namespaceState.loaded && Date.now() - namespaceLoadedAt < NAMESPACE_CACHE_TTL_MS;
  if (!force && sameSession && (namespaceState.loading || cacheFresh)) return;

  namespaceController?.abort();
  const controller = new AbortController();
  namespaceController = controller;
  const generation = ++namespaceGeneration;
  namespaceToken = token;
  publishNamespaces({
    namespaces: sameSession ? namespaceState.namespaces : [],
    loading: true,
    error: null,
    loaded: false,
  });

  try {
    const all: Namespace[] = [];
    const seenTokens = new Set<string>();
    let pageToken = "";
    for (let page = 0; page < MAX_NAMESPACE_PAGES; page += 1) {
      const res = await api.listNamespaces(200, pageToken || undefined, {
        signal: controller.signal,
      });
      all.push(...(res.namespaces ?? []));
      const nextToken = res.next_page_token ?? "";
      if (!nextToken) {
        if (generation === namespaceGeneration) {
          namespaceLoadedAt = Date.now();
          publishNamespaces({ namespaces: all, loading: false, error: null, loaded: true });
        }
        return;
      }
      if (seenTokens.has(nextToken)) {
        throw new Error("Namespace pagination returned a repeated page token.");
      }
      seenTokens.add(nextToken);
      pageToken = nextToken;
    }
    throw new Error(
      `Namespace list exceeded ${MAX_NAMESPACE_PAGES} pages; refusing to show a truncated list.`,
    );
  } catch (error) {
    if (generation !== namespaceGeneration || isAbortError(error)) return;
    namespaceLoadedAt = 0;
    publishNamespaces({ namespaces: [], loading: false, error, loaded: false });
  } finally {
    if (generation === namespaceGeneration) namespaceController = null;
  }
}

// Loads and centrally caches the full namespace list. Concurrent consumers
// share one paginated request; reload invalidates the cache for every consumer.
export function useNamespaces(): {
  namespaces: Namespace[];
  loading: boolean;
  error: unknown;
  reload: () => void;
} {
  const [state, setState] = useState(namespaceState);
  const reload = useCallback(() => void loadNamespaces(true), []);

  useEffect(() => {
    namespaceListeners.add(setState);
    setState(namespaceState);
    void loadNamespaces(false);
    return () => {
      namespaceListeners.delete(setState);
    };
  }, []);

  return {
    namespaces: state.namespaces,
    loading: state.loading,
    error: state.error,
    reload,
  };
}
