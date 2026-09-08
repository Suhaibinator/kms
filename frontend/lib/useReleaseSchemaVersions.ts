import { useCallback, useEffect, useState } from "react";
import { api, isAbortError } from "@/lib/api";
import { schemaVersionError } from "@/lib/schema";

/** Discover numeric tracks with release-list permission, without reading admin schema documents. */
export function useReleaseSchemaVersions(env: string, app: string, name: string) {
  const scope = JSON.stringify([env, app, name]);
  const [revision, setRevision] = useState(0);
  const reload = useCallback(() => setRevision((value) => value + 1), []);
  const [result, setResult] = useState<{
    scope: string;
    versions: number[] | null;
    error: unknown;
  } | null>(null);
  // biome-ignore lint/correctness/useExhaustiveDependencies: revision reloads discovery after registration.
  useEffect(() => {
    if (!env || !app) return;
    const controller = new AbortController();
    let current = true;
    void (async () => {
      const versions = new Set<number>();
      let token: string | undefined;
      do {
        const page = await api.releaseSchemaVersions({ env, app }, name || undefined, token, {
          signal: controller.signal,
        });
        if (!current) return;
        for (const version of page.schema_versions) {
          if (schemaVersionError(version) || version === 0)
            throw new Error("Invalid registered schema version.");
          versions.add(version);
        }
        token = page.next_page_token || undefined;
      } while (token);
      setResult({ scope, versions: [...versions].sort((a, b) => b - a), error: null });
    })().catch((error: unknown) => {
      if (current && !isAbortError(error)) setResult({ scope, versions: null, error });
    });
    return () => {
      current = false;
      controller.abort();
    };
  }, [env, app, name, scope, revision]);
  return { ...(result?.scope === scope ? result : { versions: null, error: null }), reload };
}
