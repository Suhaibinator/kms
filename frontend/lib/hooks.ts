import { useRouter } from "next/router";

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
