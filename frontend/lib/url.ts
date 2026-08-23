import { useRouter } from "next/router";
import { useCallback } from "react";

/** Normalises a `router.query` entry to a single string ("" when absent). */
export function queryValue(value: string | string[] | undefined): string {
  return Array.isArray(value) ? (value[0] ?? "") : (value ?? "");
}

/**
 * Returns a shallow, non-scrolling `router.replace` that merges `patch` into the current
 * query. An empty-string value deletes the key. Never call this from an effect — only
 * from an event handler — so the URL can never fight the form state.
 */
export function useQueryReplace(pathname: string): (patch: Record<string, string>) => void {
  const router = useRouter();
  return useCallback(
    (patch: Record<string, string>) => {
      const next: Record<string, string> = {};
      for (const [key, value] of Object.entries(router.query)) {
        const normalized = queryValue(value);
        if (normalized) next[key] = normalized;
      }
      for (const [key, value] of Object.entries(patch)) {
        if (value) next[key] = value;
        else delete next[key];
      }
      void router.replace({ pathname, query: next }, undefined, {
        shallow: true,
        scroll: false,
      });
    },
    [pathname, router],
  );
}
