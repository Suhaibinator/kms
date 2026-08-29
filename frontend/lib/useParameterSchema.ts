import { useEffect, useMemo, useState } from "react";
import { api, isAbortError } from "@/lib/api";
import { useLatestRequest } from "@/lib/hooks";
import { aliasSchema, buildForm, type JsonSchema } from "@/lib/schema-form";
import type { ApplicationOverview } from "@/lib/types";

export interface ParameterSchema {
  /** `none` covers every way a schema can be absent: no application, no pin, alias not described. */
  status: "idle" | "loading" | "ready" | "none";
  /** The renderable sub-schema for this key, else null. */
  schema: JsonSchema | null;
  alias: string;
  schemaId: string;
  schemaVersion: number;
}

const NONE: ParameterSchema = {
  status: "none",
  schema: null,
  alias: "",
  schemaId: "",
  schemaVersion: 0,
};
const IDLE: ParameterSchema = { ...NONE, status: "idle" };

/**
 * The alias the application uses for `key` in `env`, following the backend's
 * resolution (release entries first, then key == alias). Falls back to the
 * key itself when the overview does not mention it.
 */
export function resolveAlias(overview: ApplicationOverview, key: string): string {
  const values = overview.environments[0]?.values ?? [];
  const match = values.find(
    (value) => value.kind === "parameter" && (value.key ?? value.alias) === key,
  );
  return match?.alias ?? key;
}

export function schemaForKey(overview: ApplicationOverview | null, key: string): ParameterSchema {
  if (!overview?.schema_json) return NONE;
  const alias = resolveAlias(overview, key);
  const sub = aliasSchema(overview.schema_json, alias);
  if (!sub || buildForm(sub) === null) return { ...NONE, alias };
  return {
    status: "ready",
    schema: sub,
    alias,
    schemaId: overview.application.schema_id,
    schemaVersion: overview.application.schema_version,
  };
}

/**
 * Loads the application overview for `app` in `env` and picks out the schema
 * that describes `key`. Every failure — the namespace has no application, the
 * environment is not one of its environments, a permission or network error —
 * is the ordinary "no schema" case and stays silent. The overview is fetched
 * once per (env, app) while `enabled`.
 */
export function useParameterSchema({
  env,
  app,
  key,
  enabled,
}: {
  env: string;
  app: string;
  key: string;
  enabled: boolean;
}): ParameterSchema {
  const request = useLatestRequest();
  const [overview, setOverview] = useState<{
    env: string;
    app: string;
    status: "loading" | "done";
    data: ApplicationOverview | null;
  } | null>(null);

  useEffect(() => {
    if (!enabled || !env || !app) return;
    if (overview && overview.env === env && overview.app === app) return;
    const run = request.begin();
    setOverview({ env, app, status: "loading", data: null });
    api
      .applicationOverview(app, [env], { signal: run.signal })
      .then((data) => {
        if (run.current) setOverview({ env, app, status: "done", data });
      })
      .catch((error: unknown) => {
        if (!run.current || isAbortError(error)) return;
        setOverview({ env, app, status: "done", data: null });
      });
    // No cleanup here: `useLatestRequest` aborts on unmount, and a new
    // (env, app) supersedes the run through `begin()`.
  }, [enabled, env, app, overview, request]);

  return useMemo(() => {
    if (!enabled || !overview || overview.env !== env || overview.app !== app) return IDLE;
    if (overview.status === "loading") return { ...NONE, status: "loading" };
    return schemaForKey(overview.data, key);
  }, [enabled, overview, env, app, key]);
}
