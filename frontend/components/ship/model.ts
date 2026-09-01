// Pure state helpers for the ship modal. Nothing here touches the
// DOM or the API, so the row/change/outcome logic is unit-testable on its own
// and the components stay presentational.

import { canonicalParameterValue, valuesEquivalent } from "@/lib/json-text";
import { isProductionEnvironment } from "@/lib/readiness";
import type {
  Application,
  EnvironmentOverview,
  OverviewValue,
  ShipChange,
  ShipResult,
  SubscriberInstance,
} from "@/lib/types";
import { validateParameterValue, validateValueSize } from "@/lib/validation";

export type ShipPhase =
  | "compose"
  | "shipping"
  | "rejected"
  | "release_created_not_activated"
  | "conflict"
  | "rollout";

export type ShipMode = "guided" | "express";

export const SHIP_MODE_STORAGE_KEY = "kms-ship-mode";
export const PREVIEW_DEBOUNCE_MS = 400;

/** One editable parameter row. Secrets never become rows: they are pinned, not typed. */
export interface ShipRow {
  alias: string;
  content_type: string;
  /** The resolved key in this environment, when the alias has a resource. */
  key?: string;
  /** No resource exists yet: a value is required (first release, missing alias). */
  missing: boolean;
  value: string;
  /** The editor has been prefilled with the current value (or there was none). */
  loaded: boolean;
  loadError?: string;
  /** A version written by an earlier attempt; shipped as a pin until the row is edited. */
  reuseVersion?: number;
  /** The value the editor was prefilled with, for Revert and Show diff. */
  originalValue?: string;
  /** The operator has typed in this row; validation stays quiet until then. */
  touched?: boolean;
}

export function contractParameters(application: Application): Application["contract"] {
  return application.contract.filter((field) => field.kind === "parameter");
}

export function contractSecrets(application: Application): Application["contract"] {
  return application.contract.filter((field) => field.kind === "secret");
}

export function valueFor(env: EnvironmentOverview | null, alias: string): OverviewValue | null {
  return env?.values.find((value) => value.alias === alias) ?? null;
}

/** Secret aliases with no readable resource in this environment: blocker rows. */
export function missingSecrets(
  application: Application,
  env: EnvironmentOverview | null,
): string[] {
  return contractSecrets(application)
    .filter((field) => !valueFor(env, field.alias)?.present)
    .map((field) => field.alias);
}

export function makeRow(
  application: Application,
  env: EnvironmentOverview | null,
  alias: string,
): ShipRow | null {
  const field = contractParameters(application).find((entry) => entry.alias === alias);
  if (!field) return null;
  const value = valueFor(env, alias);
  const missing = !value?.present;
  return {
    alias,
    content_type: field.content_type ?? "string",
    key: value?.key,
    missing,
    value: "",
    // A missing resource has nothing to prefill; the editor is ready at once.
    loaded: missing,
  };
}

/**
 * The rows a fresh compose starts with: every parameter alias without a
 * resource (a first release must list them all), plus the alias the caller
 * asked to edit.
 */
export function initialRows(
  application: Application,
  env: EnvironmentOverview | null,
  initialAlias?: string,
): ShipRow[] {
  const rows: ShipRow[] = [];
  for (const field of contractParameters(application)) {
    const wanted = field.alias === initialAlias || !valueFor(env, field.alias)?.present;
    if (!wanted) continue;
    const row = makeRow(application, env, field.alias);
    if (row) rows.push(row);
  }
  return rows;
}

/** Parameter aliases not yet edited, for the "Add change" picker. */
export function addableAliases(application: Application, rows: readonly ShipRow[]): string[] {
  const taken = new Set(rows.map((row) => row.alias));
  return contractParameters(application)
    .map((field) => field.alias)
    .filter((alias) => !taken.has(alias));
}

/** The editor's validation message for a row, or null when it parses. */
export function rowError(row: ShipRow): string | null {
  if (row.reuseVersion !== undefined) return null;
  if (!row.loaded) return "Loading the current value…";
  return validateParameterValue(row.value, row.content_type) ?? validateValueSize(row.value);
}

/**
 * The message the row should display. A row the operator has not touched
 * yet keeps its problem to itself — a freshly added integer row is empty by
 * construction, not by mistake — unless it was prefilled with a value the
 * contract's content type now rejects, which is worth seeing at once.
 */
export function shownRowError(row: ShipRow): string | null {
  if (row.touched || (!row.missing && row.loaded && !row.loadError)) return rowError(row);
  return null;
}

/** Whether the row's value differs from what it was prefilled with. */
export function rowChanged(row: ShipRow): boolean {
  if (row.originalValue === undefined) return row.touched === true;
  return !valuesEquivalent(row.value, row.originalValue, row.content_type);
}

export function rowsParse(rows: readonly ShipRow[]): boolean {
  return rows.every((row) => rowError(row) === null);
}

/**
 * Aliases whose resource has moved past the active pin and that no row edits:
 * the "Unreleased changes not included" opt-ins. Both kinds qualify — a secret
 * is pinned by label, never by value.
 */
export interface DriftCandidate {
  alias: string;
  kind: OverviewValue["kind"];
  current: number;
  pinned: number;
}

export function driftCandidates(
  env: EnvironmentOverview | null,
  rows: readonly ShipRow[],
): DriftCandidate[] {
  if (!env?.release.active) return [];
  const edited = new Set(rows.map((row) => row.alias));
  const out: DriftCandidate[] = [];
  for (const value of env.values) {
    if (edited.has(value.alias) || !value.present) continue;
    const current = value.current_version ?? 0;
    const pinned = value.pinned_version ?? 0;
    if (current > 0 && pinned > 0 && current !== pinned) {
      out.push({ alias: value.alias, kind: value.kind, current, pinned });
    }
  }
  return out;
}

/** The wire changes for the current rows and opt-ins. */
export function buildChanges(rows: readonly ShipRow[], optIns: readonly string[]): ShipChange[] {
  const changes: ShipChange[] = rows.map((row) =>
    row.reuseVersion !== undefined
      ? { alias: row.alias, version: row.reuseVersion }
      : {
          alias: row.alias,
          value: canonicalParameterValue(row.value, row.content_type),
          content_type: row.content_type,
        },
  );
  for (const alias of optIns) changes.push({ alias, label: "current" });
  return changes;
}

/** A stable identity for a change set, used to tell a preview from the edits after it. */
export function changesKey(changes: readonly ShipChange[]): string {
  return JSON.stringify(changes);
}

/** Rows after a failed ship: parameters the server already wrote are pinned by version. */
export function reuseWrittenVersions(rows: readonly ShipRow[], result: ShipResult): ShipRow[] {
  const written = new Map(result.parameters.map((entry) => [entry.alias, entry.version]));
  return rows.map((row) => {
    const version = written.get(row.alias);
    return version === undefined ? row : { ...row, reuseVersion: version };
  });
}

/** The focused environment, else the first non-production one, else the first. */
export function defaultEnvironment(
  environments: readonly EnvironmentOverview[],
  initial?: string,
): string {
  if (initial && environments.some((env) => env.namespace.env === initial)) return initial;
  const nonProd = environments.find((env) => !env.production);
  return (nonProd ?? environments[0])?.namespace.env ?? "";
}

/** Guided until the application has ever had an active release anywhere. */
export function everActivated(environments: readonly EnvironmentOverview[]): boolean {
  return environments.some(
    (env) =>
      env.release.active !== undefined ||
      env.release_state === "active" ||
      env.release_state === "drift" ||
      env.status === "ready" ||
      env.status === "degraded" ||
      env.status === "rolling" ||
      env.status === "drift",
  );
}

export function readStoredMode(): ShipMode | null {
  try {
    const raw = window.localStorage.getItem(SHIP_MODE_STORAGE_KEY);
    return raw === "guided" || raw === "express" ? raw : null;
  } catch {
    return null;
  }
}

export function storeMode(mode: ShipMode): void {
  try {
    window.localStorage.setItem(SHIP_MODE_STORAGE_KEY, mode);
  } catch {
    /* storage unavailable; the toggle still works for this open */
  }
}

export function needsTypedConfirmation(env: string): boolean {
  return isProductionEnvironment(env);
}

const STATE_ORDER: Record<SubscriberInstance["state"], number> = {
  rejected: 0,
  received: 1,
  prepared: 2,
  "": 3,
  applied: 4,
};

/**
 * Rollout order: rejected first, then anything still in flight, then applied;
 * disconnected instances sink within their group. Ties keep identity order.
 */
export function sortForRollout(
  instances: readonly SubscriberInstance[],
  activationRevision: number,
): SubscriberInstance[] {
  const rank = (instance: SubscriberInstance): number => {
    const atCurrent = instance.activation_revision >= activationRevision;
    if (instance.state === "rejected" && atCurrent) return 0;
    if (instance.state === "applied" && atCurrent) return 4;
    // Applied to an older activation counts as pending for this one.
    return STATE_ORDER[instance.state] === 4 ? 2 : STATE_ORDER[instance.state];
  };
  return [...instances]
    .map((instance, index) => ({ instance, index }))
    .sort((a, b) => {
      const byRank = rank(a.instance) - rank(b.instance);
      if (byRank !== 0) return byRank;
      const byConnection = Number(b.instance.connected) - Number(a.instance.connected);
      return byConnection !== 0 ? byConnection : a.index - b.index;
    })
    .map(({ instance }) => instance);
}
