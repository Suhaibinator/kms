import { useRouter } from "next/router";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { api, getToken, isAbortError } from "./api";
import type { Namespace } from "./types";

const first = (raw: string | string[] | undefined): string | null =>
  (Array.isArray(raw) ? raw[0] : raw) ?? null;

// Reads a single query-string parameter. On a static export, router.query is
// empty until the client router hydrates, so callers must wait for `ready`
// before treating a missing value as "not provided".
export function useQueryParam(key: string): { value: string | null; ready: boolean } {
  const router = useRouter();
  if (!router.isReady) return { value: null, ready: false };
  return { value: first(router.query[key]), ready: true };
}

// Reads several query-string parameters at once. Same hydration caveat as
// useQueryParam: values are null until `ready`. `values` is referentially
// stable while the requested keys resolve to the same strings, so it is safe
// in effect dependency arrays.
export function useQueryParams<K extends string>(
  keys: readonly K[],
): { values: Record<K, string | null>; ready: boolean } {
  const router = useRouter();
  const ready = router.isReady;
  const query = router.query;
  // `keys` and `router.query` are fresh objects on every render, so the memo
  // keys on a string that encodes both. "\u0000" cannot appear in a URL.
  const signature = keys
    .map((key) => `${key}=${(ready && first(query[key])) ?? ""}`)
    .join("\u0000");
  // biome-ignore lint/correctness/useExhaustiveDependencies: `signature` encodes `keys`, `ready` and the relevant slice of `query`.
  const values = useMemo(() => {
    const next = {} as Record<K, string | null>;
    for (const key of keys) next[key] = ready ? first(query[key]) : null;
    return next;
  }, [signature]);
  return { values, ready };
}

export interface LoadRun {
  readonly signal: AbortSignal;
  /** False once a newer run has begun, abort() was called, or the component unmounted. */
  readonly current: boolean;
}

/**
 * Serialises a component's loads. begin() aborts the previous request and hands back a
 * token whose `current` flips to false as soon as a newer load starts or the component
 * unmounts. Guard every setState after an await with `if (!run.current) return;`.
 * `current` is a getter — read it through the token, never destructure it.
 */
export function useLatestRequest(): { begin: () => LoadRun; abort: () => void } {
  const controller = useRef<AbortController | null>(null);
  const generation = useRef(0);

  const abort = useCallback(() => {
    generation.current += 1;
    controller.current?.abort();
    controller.current = null;
  }, []);

  const begin = useCallback((): LoadRun => {
    controller.current?.abort();
    const next = new AbortController();
    controller.current = next;
    const mine = ++generation.current;
    return {
      signal: next.signal,
      get current() {
        return generation.current === mine;
      },
    };
  }, []);

  useEffect(() => abort, [abort]);
  // A stable object, so a loader that lists `request` in its deps keeps its identity.
  return useMemo(() => ({ begin, abort }), [begin, abort]);
}

/**
 * Tracks which form fields may show their validation message: a field reveals its
 * error once blurred (`touch`) or once a submit has been attempted (`markAllTouched`).
 */
export function useFieldErrors<F extends string = string>(): {
  /** Reveal this field's message (call from onBlur). */
  touch: (field: F) => void;
  /** Reveal every message (call at the top of submit). */
  markAllTouched: () => void;
  /** Clear everything (call when a form/modal opens). */
  reset: () => void;
  /** The message, or null while the field is still pristine. */
  shown: (field: F, error: string | null | undefined) => string | null;
  submitted: boolean;
} {
  const [touched, setTouched] = useState<ReadonlySet<F>>(() => new Set());
  const [submitted, setSubmitted] = useState(false);
  const touch = useCallback((field: F) => {
    setTouched((current) => (current.has(field) ? current : new Set(current).add(field)));
  }, []);
  const markAllTouched = useCallback(() => setSubmitted(true), []);
  const reset = useCallback(() => {
    setTouched(new Set());
    setSubmitted(false);
  }, []);
  const shown = useCallback(
    (field: F, error: string | null | undefined) =>
      error && (submitted || touched.has(field)) ? error : null,
    [submitted, touched],
  );
  return { touch, markAllTouched, reset, shown, submitted };
}

interface CursorState {
  scope: string;
  tokens: string[];
  index: number;
  nextToken: string;
  /** Page number of `tokens[1]`; 2 normally, higher when seeded from a URL
   *  whose intermediate tokens are unknown. */
  secondPage: number;
}

function initialCursor(scope: string): CursorState {
  return { scope, tokens: [""], index: 0, nextToken: "", secondPage: 2 };
}

export interface CursorSeed {
  /** The page token to start on (from `?page_token=`). */
  pageToken?: string | null;
  /** The page number that token represents (from `?page=`); 2 when omitted. */
  page?: number | null;
}

/**
 * A cursor stack for page_token APIs: `next` pushes the server's token,
 * `previous` pops. `seed` (first mount only) starts on a token restored from
 * the URL — the pages between 1 and it are unknown, so there is no Previous
 * from it (only First page) and `page` counts from the seeded number.
 */
export function useCursorPagination(scope: string, seed?: CursorSeed) {
  const [state, setState] = useState<CursorState>(() => {
    const base = initialCursor(scope);
    if (!seed?.pageToken) return base;
    const page = Math.max(2, Math.floor(seed.page ?? 2));
    return { ...base, tokens: ["", seed.pageToken], index: 1, secondPage: page };
  });
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

  const hasNext = Boolean(active.nextToken) && active.nextToken !== pageToken;
  // From a restored token the pages before it are unknown, so there is no
  // "previous" — only "first"; callers show the reset control from `page > 1`.
  const hasPrevious = active.index > 1 || (active.index === 1 && active.secondPage === 2);
  return {
    pageToken,
    page: active.index === 0 ? 1 : active.index - 1 + active.secondPage,
    hasPrevious,
    hasNext,
    /** The token `next()` will move to ("" when there is none). */
    nextToken: hasNext ? active.nextToken : "",
    /** The token `previous()` will move to ("" for page 1). */
    previousToken: hasPrevious ? (active.tokens[active.index - 1] ?? "") : "",
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
