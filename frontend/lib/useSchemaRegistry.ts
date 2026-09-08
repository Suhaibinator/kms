import { useCallback, useEffect, useState } from "react";
import { api, isAbortError } from "@/lib/api";
import { schemaVersionError } from "@/lib/schema";
import type { ConfigurationSchema } from "@/lib/types";

/** Registry pages share one request generation, including when the application changes. */
export function useSchemaRegistry(application: string, releaseName?: string) {
  const scope = JSON.stringify([application, releaseName]);
  const [revision, setRevision] = useState(0);
  const reload = useCallback(() => setRevision((value) => value + 1), []);
  const [result, setResult] = useState<{
    scope: string;
    schemas: ConfigurationSchema[] | null;
    error: unknown;
  } | null>(null);
  // biome-ignore lint/correctness/useExhaustiveDependencies: revision explicitly reloads the registry after registration.
  useEffect(() => {
    if (!application) return;
    const controller = new AbortController();
    let current = true;
    void (async () => {
      const schemas: ConfigurationSchema[] = [];
      let token: string | undefined;
      do {
        const page = await api.listSchemas(application, releaseName, token, {
          signal: controller.signal,
        });
        if (!current) return;
        for (const schema of page.schemas ?? []) {
          if (schemaVersionError(schema.version))
            throw new Error("Invalid registered schema version.");
          schemas.push(schema);
        }
        token = page.next_page_token || undefined;
      } while (token);
      setResult({ scope, schemas: schemas.sort((a, b) => b.version - a.version), error: null });
    })().catch((error: unknown) => {
      if (current && !isAbortError(error)) setResult({ scope, schemas: null, error });
    });
    return () => {
      current = false;
      controller.abort();
    };
  }, [application, releaseName, scope, revision]);
  return { ...(result?.scope === scope ? result : { schemas: null, error: null }), reload };
}
