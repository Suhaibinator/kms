import Link from "next/link";
import { useCallback, useEffect, useId, useState } from "react";
import { AddResourceButton } from "@/components/applications/AddResourceButton";
import { Ident } from "@/components/Ident";
import { missingSecrets, valueFor } from "@/components/ship/model";
import { api, isAbortError } from "@/lib/api";
import { useLatestRequest } from "@/lib/hooks";
import { links } from "@/lib/links";
import type { ApplicationOverview } from "@/lib/types";

export interface MissingContractSecret {
  alias: string;
  /** Where the value goes: the alias itself unless the overview resolved a key. */
  key: string;
}

/**
 * The secrets an application's contract declares that have no value in `env`,
 * in contract order. Empty for an archived application or an environment the
 * overview did not return.
 */
export function missingContractSecrets(
  overview: ApplicationOverview,
  env: string,
): MissingContractSecret[] {
  if (overview.application.archived_at_unix_ms > 0) return [];
  const environment =
    overview.environments.find((candidate) => candidate.namespace.env === env) ?? null;
  if (!environment) return [];
  return missingSecrets(overview.application, environment).map((alias) => ({
    alias,
    key: valueFor(environment, alias)?.key || alias,
  }));
}

/**
 * Reads the overview of the application named by `app`, one environment wide,
 * so the secrets list can say which contract secrets it is still missing. A
 * namespace that is not an application, or one this identity cannot read as
 * one, yields nothing: the list page works without it.
 */
export function useMissingContractSecrets(env: string, app: string) {
  const scope = JSON.stringify([env, app]);
  const [result, setResult] = useState<{ scope: string; missing: MissingContractSecret[] }>({
    scope: "",
    missing: [],
  });
  const request = useLatestRequest();

  const load = useCallback(async () => {
    if (!env || !app) {
      request.abort();
      return;
    }
    const run = request.begin();
    try {
      const overview = await api.applicationOverview(app, [env], { signal: run.signal });
      if (run.current) setResult({ scope, missing: missingContractSecrets(overview, env) });
    } catch (error) {
      if (!run.current || isAbortError(error)) return;
      setResult({ scope, missing: [] });
    }
  }, [env, app, scope, request]);

  useEffect(() => {
    void load();
  }, [load]);

  // A previous namespace's answer never shows under this one's header.
  return { missing: result.scope === scope ? result.missing : [], reload: load };
}

/**
 * "These secrets are required and not set": one row per missing contract
 * secret, each opening the create form with its key filled in.
 */
export function MissingContractSecretsPanel({
  env,
  app,
  missing,
  onAdd,
}: {
  env: string;
  app: string;
  missing: MissingContractSecret[];
  onAdd: (secret: MissingContractSecret) => void;
}) {
  const headingId = useId();
  if (missing.length === 0) return null;
  const count = missing.length;
  return (
    <section
      className="warn-panel missing-secrets mb-4"
      aria-labelledby={headingId}
      data-testid="missing-contract-secrets"
    >
      <div className="missing-secrets-head">
        <strong id={headingId}>
          {count === 1 ? "1 required secret is" : `${count} required secrets are`} not set in{" "}
          <Ident kind="env" value={env} />
        </strong>
        <p>
          The{" "}
          <Link className="text-link" href={links.application(app, { env })}>
            {app}
          </Link>{" "}
          contract declares {count === 1 ? "it" : "them"}. Releases here cannot validate until each
          has a value.
        </p>
      </div>
      <ul className="missing-secrets-list">
        {missing.map((secret) => (
          <li key={secret.alias} data-alias={secret.alias}>
            <span className="missing-secrets-name">
              <span className="mono missing-secrets-key">{secret.key}</span>
              {secret.key !== secret.alias ? <Ident kind="alias" value={secret.alias} /> : null}
            </span>
            <AddResourceButton
              kind="secret"
              aria-label={`Add secret ${secret.key}`}
              onClick={() => onAdd(secret)}
            />
          </li>
        ))}
      </ul>
    </section>
  );
}
