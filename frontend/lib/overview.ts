import type {
  ApplicationConfigurationRow,
  EnvironmentOverview,
  OverviewValue,
  ReleaseEntryKind,
} from "@/lib/types";

/**
 * Lookups over the application overview. Alias-to-key resolution is decided
 * server-side by one rule (active release, latest release, same-named key,
 * another environment's pin); the console only ever reads the result from
 * `values` and never assumes a key equals its alias.
 */

/** The contract value for `alias` in one environment, or undefined. */
export function valueForAlias(
  environment: EnvironmentOverview | null | undefined,
  alias: string,
): OverviewValue | undefined {
  return environment?.values.find((candidate) => candidate.alias === alias);
}

/** The contract value for `alias` in `env`, or undefined when either is unknown. */
export function valueFor(
  environments: readonly EnvironmentOverview[],
  env: string,
  alias: string,
): OverviewValue | undefined {
  return valueForAlias(
    environments.find((candidate) => candidate.namespace.env === env),
    alias,
  );
}

/** The contract value that resolved to a physical resource in one environment, if any. */
export function valueForKey(
  environment: EnvironmentOverview | undefined,
  kind: ReleaseEntryKind | string,
  key: string,
): OverviewValue | undefined {
  return environment?.values.find((value) => value.kind === kind && value.key === key);
}

/** `kind:key` → alias for every resolved value in one environment. */
export function aliasesByKey(environment: EnvironmentOverview): Map<string, string> {
  const out = new Map<string, string>();
  for (const value of environment.values) {
    if (!value.key) continue;
    const id = resourceId(value.kind, value.key);
    if (!out.has(id)) out.set(id, value.alias);
  }
  return out;
}

export function resourceId(kind: ReleaseEntryKind | string, key: string): string {
  return `${kind}:${key}`;
}

/**
 * Present resources in the environment that no contract alias resolves to,
 * counted per kind. Keyed by kind and key so a secret alias resolving to
 * `x` cannot hide an unrelated parameter `x`.
 */
export function countOtherKeys(
  environment: EnvironmentOverview,
  rows: readonly ApplicationConfigurationRow[],
): { parameters: number; secrets: number } {
  const env = environment.namespace.env;
  const resolved = aliasesByKey(environment);
  const out = { parameters: 0, secrets: 0 };
  for (const row of rows) {
    if (!row.environments[env]?.present) continue;
    if (resolved.has(resourceId(row.kind, row.key))) continue;
    if (row.kind === "secret") out.secrets += 1;
    else out.parameters += 1;
  }
  return out;
}
