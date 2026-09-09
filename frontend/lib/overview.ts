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
 * `kind:key` → the value stored in `env`, for the filters that search inside
 * values. The overview already carries every parameter's current value (the
 * matrix renders it), so no page has to load anything to search one; secrets
 * have no readable value and are simply absent from the map.
 */
export function valuesByEnv(
  rows: readonly ApplicationConfigurationRow[],
  env: string,
): Map<string, string> {
  const out = new Map<string, string>();
  for (const row of rows) {
    const cell = row.environments[env];
    if (!cell?.present || cell.value === undefined) continue;
    out.set(resourceId(row.kind, row.key), cell.value);
  }
  return out;
}

/** The value stored behind one contract value, from a `valuesByEnv` map. */
export function storedValue(
  values: ReadonlyMap<string, string> | undefined,
  value: OverviewValue,
): string | undefined {
  return value.key ? values?.get(resourceId(value.kind, value.key)) : undefined;
}

/**
 * Every distinct value one matrix row holds, across its environments, as one
 * searchable text. The matrix row spans the environments, so a token found in
 * any of them keeps the row: identical values (the common case) are joined
 * once, so the text stays the size of the value and not of the row.
 */
export function rowValueText(row: ApplicationConfigurationRow): string {
  const seen = new Set<string>();
  for (const cell of Object.values(row.environments)) {
    if (!cell.present || cell.value === undefined) continue;
    seen.add(cell.value);
  }
  return Array.from(seen).join("\n");
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
