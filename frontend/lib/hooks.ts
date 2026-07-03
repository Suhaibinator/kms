import { useRouter } from "next/router";
import { useCallback, useEffect, useState } from "react";
import { api } from "./api";
import type { Namespace } from "./types";

// Reads a single query-string parameter. On a static export, router.query is
// empty until the client router hydrates, so callers must wait for `ready`
// before treating a missing value as "not provided".
export function useQueryParam(key: string): { value: string | null; ready: boolean } {
  const router = useRouter();
  if (!router.isReady) return { value: null, ready: false };
  const raw = router.query[key];
  const value = Array.isArray(raw) ? raw[0] : raw ?? null;
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

// Loads the full namespace list (following pagination) so pages can drive an
// env → app cascading selector and per-namespace counts. Namespaces are few,
// so fetching all of them up front is cheap and keeps the selector snappy.
export function useNamespaces(): {
  namespaces: Namespace[];
  loading: boolean;
  error: unknown;
  reload: () => void;
} {
  const [namespaces, setNamespaces] = useState<Namespace[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<unknown>(null);
  const [nonce, setNonce] = useState(0);

  const reload = useCallback(() => setNonce((n) => n + 1), []);

  useEffect(() => {
    let cancelled = false;
    setLoading(true);
    setError(null);
    (async () => {
      try {
        const all: Namespace[] = [];
        let token = "";
        // Cap the loop defensively; a handful of pages is expected.
        for (let i = 0; i < 50; i += 1) {
          const res = await api.listNamespaces(200, token || undefined);
          all.push(...(res.namespaces ?? []));
          token = res.next_page_token ?? "";
          if (!token) break;
        }
        if (!cancelled) setNamespaces(all);
      } catch (err) {
        if (!cancelled) setError(err);
      } finally {
        if (!cancelled) setLoading(false);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [nonce]);

  return { namespaces, loading, error, reload };
}
