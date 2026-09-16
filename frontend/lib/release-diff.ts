// Row models, grouping, filtering and the plain-text export for the release
// diff view (components/releases/diff). Pure functions over the
// `ReleaseDiffResponse` wire shape; the components render, this decides.

import { countNoun } from "@/lib/format";
import type {
  OverviewRollout,
  ReleaseDiffChange,
  ReleaseDiffPin,
  ReleaseDiffReason,
  ReleaseDiffResponse,
  ReleaseDiffRow,
  ReleaseDiffSide,
  ReleaseEntryKind,
} from "@/lib/types";
import { formatBytes } from "@/lib/validation";
import {
  describeChange,
  describeLeafChange,
  type FieldCounts,
  fieldCounts,
  formatValuePath,
  type StructuralDiff,
  type ValueChange,
  type ValueChangeDescription,
} from "@/lib/value-diff";

export type DiffFlag = "restart";

export interface DiffRowModel {
  row: ReleaseDiffRow;
  alias: string;
  /** The pinned key on the `to` side (or `from` when removed); "" when unknown. */
  key: string;
  kind: ReleaseEntryKind;
  change: ReleaseDiffChange;
  reasons: ReleaseDiffReason[];
  contentType: string;
  /** Belongs in the "Needs attention" group; `attentionReasons` says why. */
  attention: boolean;
  attentionReasons: string[];
  /** Dormant until the schema carries `x-kms-reload` (plan §E8 F2). */
  flags: DiffFlag[];
  /** Text before the first `_` or `-` of the alias, for "Group by prefix". */
  prefix: string;
  /** Parameters with at least one present value; null for secrets and value-free rows. */
  description: ValueChangeDescription | null;
  /** JSON rows with a structural diff: how many fields were added, removed, changed or moved. */
  fields: FieldCounts | null;
  /** One line: `100 → 20`, `database.pool.max 50 → 5, +1 more`, `v2 → v3`. */
  summary: string;
  /** Lower-cased haystack for the filter: alias, key, reasons, paths, values. */
  searchText: string;
}

export interface FieldAggregate {
  counts: FieldCounts;
  /** Sum of the four counts. */
  total: number;
  /** Rows that contributed. */
  rows: number;
  /** Some row's listing stopped at its cap, so `total` is a floor. */
  partial: boolean;
}

/** Field counts over every row that has them, or null when none does. */
export function aggregateFields(rows: readonly DiffRowModel[]): FieldAggregate | null {
  const counts: FieldCounts = { added: 0, removed: 0, changed: 0, moved: 0 };
  let contributing = 0;
  let partial = false;
  for (const row of rows) {
    if (!row.fields) continue;
    contributing += 1;
    counts.added += row.fields.added;
    counts.removed += row.fields.removed;
    counts.changed += row.fields.changed;
    counts.moved += row.fields.moved;
    if (row.description?.kind === "json" && row.description.structural?.truncated) partial = true;
  }
  if (contributing === 0) return null;
  const total = counts.added + counts.removed + counts.changed + counts.moved;
  return { counts, total, rows: contributing, partial };
}

/** `+3 −21 ~1 ↷2`, zero kinds omitted; "" when every count is zero. */
export function formatFieldCounts(counts: FieldCounts): string {
  const parts: string[] = [];
  if (counts.added) parts.push(`+${counts.added}`);
  if (counts.removed) parts.push(`−${counts.removed}`);
  if (counts.changed) parts.push(`~${counts.changed}`);
  if (counts.moved) parts.push(`↷${counts.moved}`);
  return parts.join(" ");
}

export type RolloutTone = "zero" | "neutral" | "success" | "danger";

/**
 * The rollout cell as words. `null` is "we asked and do not know"; a zero
 * total means no subscriber instance is known for the track, which is not a
 * failed rollout.
 */
export function rolloutSentence(rollout: OverviewRollout | null): string {
  if (!rollout) return "rollout unknown";
  const total = rollout.total;
  if (total === 0) return "not yet applied (no instances subscribed)";
  if (rollout.applied_current === total && rollout.rejected === 0 && rollout.pending === 0) {
    return total === 1 ? "applied on the only instance" : `applied on all ${total} instances`;
  }
  const parts = [
    `applied on ${rollout.applied_current} of ${total} ${countNoun(total, "instances")}`,
  ];
  if (rollout.pending > 0) parts.push(`${rollout.pending} pending`);
  if (rollout.rejected > 0) parts.push(`${rollout.rejected} rejected`);
  return parts.join(", ");
}

export function rolloutTone(rollout: OverviewRollout | null): RolloutTone {
  if (!rollout || rollout.total === 0) return "zero";
  if (rollout.rejected > 0) return "danger";
  if (rollout.applied_current === rollout.total && rollout.pending === 0) return "success";
  return "neutral";
}

export interface BuildRowsOptions {
  /** The pinned schema JSON, when the page has it; promotes `format: go-duration` strings. */
  schemaJson?: string | null;
  /** For secret expiry; defaults to Date.now(). */
  now?: number;
}

export type GroupMode = "kind" | "prefix";
export type DiffGroupTone =
  | "attention"
  | "secret"
  | "changed"
  | "added"
  | "removed"
  | "unchanged"
  | "prefix";

export interface DiffGroup {
  id: string;
  title: string;
  tone: DiffGroupTone;
  rows: DiffRowModel[];
}

export type DiffKindFilter = "all" | "parameter" | "secret";
export type DiffView = "changed" | "all";

const REASON_LABEL: Record<ReleaseDiffReason, string> = {
  value: "value",
  pin: "pin only",
  key: "key changed",
  kind: "kind changed",
  content_type: "type changed",
};

export function reasonLabel(reason: ReleaseDiffReason): string {
  return REASON_LABEL[reason];
}

/** The schema's `format` for an alias, unwrapping the generator's nullable `anyOf`. */
export function resolveDurationFormat(
  schemaJson: string | null | undefined,
  alias: string,
): "go-duration" | undefined {
  if (!schemaJson) return undefined;
  let schema: unknown;
  try {
    schema = JSON.parse(schemaJson);
  } catch {
    return undefined;
  }
  if (!schema || typeof schema !== "object") return undefined;
  const properties = (schema as { properties?: unknown }).properties;
  if (!properties || typeof properties !== "object") return undefined;
  const field = (properties as Record<string, unknown>)[alias];
  return fieldFormat(field) === "go-duration" ? "go-duration" : undefined;
}

function fieldFormat(field: unknown): string | undefined {
  if (!field || typeof field !== "object") return undefined;
  const direct = (field as { format?: unknown }).format;
  if (typeof direct === "string") return direct;
  const anyOf = (field as { anyOf?: unknown }).anyOf;
  if (Array.isArray(anyOf)) {
    for (const branch of anyOf) {
      const format = fieldFormat(branch);
      if (format) return format;
    }
  }
  return undefined;
}

export function aliasPrefix(alias: string): string {
  const match = alias.match(/^[^_-]+/);
  return match ? match[0] : alias;
}

const ELIDE_AT = 120;

/** JSON scalars over 120 characters are elided in summaries and the text export. */
export function elide(text: string, at = ELIDE_AT): string {
  return text.length > at ? `${text.slice(0, at - 1)}…` : text;
}

function leafText(change: ValueChange): string {
  const path = formatValuePath(change.path);
  if (change.kind === "added") return `${path} + ${elide(change.after ?? "")}`;
  if (change.kind === "removed") return `${path} − ${elide(change.before ?? "")}`;
  if (change.kind === "moved") {
    return `${formatValuePath(change.fromPath ?? [])} ↷ ${path} ${elide(change.after ?? "")}`;
  }
  return `${path} ${elide(change.before ?? "")} → ${elide(change.after ?? "")}`;
}

const FIELD_GLYPH: Record<ValueChange["kind"], string> = {
  added: "+",
  removed: "−",
  changed: "~",
  moved: "↷",
};

/** `(−45, −90 %)` / `(×2)` for a changed scalar leaf, "" otherwise. */
function leafDelta(change: ValueChange): string {
  if (change.kind !== "changed") return "";
  const leaf = describeLeafChange(change.before, change.after);
  if (leaf.kind === "number" && leaf.delta) {
    return leaf.percent ? ` (${leaf.delta}, ${leaf.percent})` : ` (${leaf.delta})`;
  }
  if (leaf.kind === "duration" && leaf.ratio) return ` (${leaf.ratio})`;
  return "";
}

/**
 * One plain-text line per field change, the path column padded so the values
 * align: `~ pool.max   50 → 5 (−45, −90 %)`, `+ ssl        true`,
 * `↷ legacy_endpoint → endpoints.legacy   "https://…"`. Subtrees stay minified.
 */
export function fieldLines(structural: StructuralDiff): string[] {
  const entries = structural.changes.map((change) => {
    const path = formatValuePath(change.path) || "(root)";
    const label =
      change.kind === "moved" ? `${formatValuePath(change.fromPath ?? [])} → ${path}` : path;
    const value =
      change.kind === "added" || change.kind === "moved"
        ? elide(change.after ?? "")
        : change.kind === "removed"
          ? elide(change.before ?? "")
          : `${elide(change.before ?? "")} → ${elide(change.after ?? "")}${leafDelta(change)}`;
    return { glyph: FIELD_GLYPH[change.kind], label, value };
  });
  const width = Math.max(0, ...entries.map((entry) => entry.label.length));
  return entries.map((entry) => `${entry.glyph} ${entry.label.padEnd(width)}   ${entry.value}`);
}

/** The inline one-liner for a described parameter change. */
export function describeInline(description: ValueChangeDescription): string {
  if (description.kind === "binary") {
    const l = description.beforeBytes === undefined ? "—" : formatBytes(description.beforeBytes);
    const r = description.afterBytes === undefined ? "—" : formatBytes(description.afterBytes);
    return `${l} → ${r}`;
  }
  if (description.kind === "json") {
    if (description.before === undefined) return elide(description.after ?? "");
    if (description.after === undefined) return elide(description.before);
    const structural = description.structural;
    if (!structural) {
      return description.oversize
        ? "too large to compare structurally"
        : description.invalid
          ? "not valid JSON on one side"
          : "changed";
    }
    if (structural.changes.length === 0) return "no leaf differences";
    const shown = structural.changes.slice(0, 2).map(leafText);
    const rest = structural.changes.length - shown.length;
    return rest > 0
      ? `${shown.join(", ")}, +${rest} more${structural.truncated ? "+" : ""}`
      : shown.join(", ");
  }
  const l = description.before === undefined ? "—" : elide(description.before);
  const r = description.after === undefined ? "—" : elide(description.after);
  const base = `${l} → ${r}`;
  if (description.kind === "number" && description.delta) {
    return description.percent
      ? `${base} (${description.delta}, ${description.percent})`
      : `${base} (${description.delta})`;
  }
  if (description.kind === "duration" && description.ratio) return `${base} (${description.ratio})`;
  return base;
}

function secretSummary(from: ReleaseDiffPin | undefined, to: ReleaseDiffPin | undefined): string {
  const side = (pin: ReleaseDiffPin | undefined) => (pin ? `v${pin.version}` : "—");
  const mode = (pin: ReleaseDiffPin | undefined) =>
    pin ? (pin.bound ? "binding key" : "master key only") : "—";
  const versions = `${side(from)} → ${side(to)}`;
  if (!from || !to) return versions;
  const state = to.secret_state && to.secret_state !== "enabled" ? `, ${to.secret_state}` : "";
  return `${versions} (${mode(from)} → ${mode(to)}${state})`;
}

function presentValue(pin: ReleaseDiffPin | undefined): string | undefined {
  return pin && pin.value_state === "present" ? pin.value : undefined;
}

function parameterSummary(
  row: ReleaseDiffRow,
  description: ValueChangeDescription | null,
  valuesIncluded: boolean,
): string {
  const side = row.to ?? row.from;
  if (description) return describeInline(description);
  if (!valuesIncluded) {
    const l = row.from ? `v${row.from.version}` : "—";
    const r = row.to ? `v${row.to.version}` : "—";
    return row.reasons.length > 0
      ? `${l} → ${r} (${row.reasons.map(reasonLabel).join(", ")})`
      : `${l} → ${r}`;
  }
  const state = side?.value_state;
  if (state === "omitted_size") {
    return `too large to compare inline (${formatBytes(side?.value_bytes ?? 0)})`;
  }
  if (state === "unavailable") return "value not readable with your permissions";
  if (state === "omitted_request") return "values not loaded";
  if (state === "omitted_unchanged") return "unchanged";
  return row.reasons.map(reasonLabel).join(", ") || row.change;
}

/** Row models with attention, prefix, description, summary and search text. */
export function buildRows(diff: ReleaseDiffResponse, opts: BuildRowsOptions = {}): DiffRowModel[] {
  const now = opts.now ?? Date.now();
  return diff.rows.map((row) => {
    const side = row.to ?? row.from;
    const key = side?.ref.key ?? "";
    const contentType = side?.content_type ?? "";
    const before = presentValue(row.from);
    const after = presentValue(row.to);
    const description =
      row.kind === "parameter" && (before !== undefined || after !== undefined)
        ? describeChange(
            before,
            after,
            contentType,
            resolveDurationFormat(opts.schemaJson, row.alias),
          )
        : null;
    const attentionReasons: string[] = [];
    if (row.reasons.includes("kind")) attentionReasons.push("kind changed");
    if (row.reasons.includes("content_type")) attentionReasons.push("content type changed");
    if (row.kind === "secret" && row.to) {
      if (row.to.secret_state && row.to.secret_state !== "enabled") {
        attentionReasons.push(`secret ${row.to.secret_state}`);
      }
      if (row.to.expires_at_unix_ms && row.to.expires_at_unix_ms <= now) {
        attentionReasons.push("secret expired");
      }
    }
    if (
      row.kind === "parameter" &&
      row.change === "changed" &&
      diff.values_included &&
      side &&
      side.value_state !== "present"
    ) {
      attentionReasons.push(
        side.value_state === "unavailable" ? "value not readable" : "value too large to compare",
      );
    }
    if (diff.schema_changed && row.change !== "changed" && row.change !== "unchanged") {
      attentionReasons.push("only on one schema track");
    }
    const flags: DiffFlag[] = [];
    if (flags.includes("restart")) attentionReasons.push("restart required");
    const summary =
      row.kind === "secret"
        ? secretSummary(row.from, row.to)
        : parameterSummary(row, description, diff.values_included);
    const haystack = [
      row.alias,
      key,
      ...row.reasons.map(reasonLabel),
      ...attentionReasons,
      summary,
      before ?? "",
      after ?? "",
    ];
    if (description?.kind === "json" && description.structural) {
      for (const change of description.structural.changes) {
        haystack.push(formatValuePath(change.path));
        if (change.fromPath) haystack.push(formatValuePath(change.fromPath));
      }
    }
    return {
      row,
      alias: row.alias,
      key,
      kind: row.kind,
      change: row.change,
      reasons: row.reasons,
      contentType,
      attention: attentionReasons.length > 0,
      attentionReasons,
      flags,
      prefix: aliasPrefix(row.alias),
      description,
      fields:
        description?.kind === "json" && description.structural
          ? fieldCounts(description.structural)
          : null,
      summary,
      searchText: haystack.join("\n").toLowerCase(),
    };
  });
}

const KIND_GROUPS: ReadonlyArray<{ id: string; title: string; tone: DiffGroupTone }> = [
  { id: "attention", title: "Needs attention", tone: "attention" },
  { id: "secrets", title: "Secrets", tone: "secret" },
  { id: "changed", title: "Changed", tone: "changed" },
  { id: "added", title: "Added", tone: "added" },
  { id: "removed", title: "Removed", tone: "removed" },
  { id: "unchanged", title: "Unchanged", tone: "unchanged" },
];

function kindGroupOf(row: DiffRowModel): string {
  if (row.change === "unchanged") return "unchanged";
  if (row.attention) return "attention";
  if (row.kind === "secret") return "secrets";
  return row.change;
}

/**
 * Groups in display order. `kind` puts "Needs attention" first, then secrets,
 * then changed / added / removed / unchanged; `prefix` makes one group per
 * alias prefix, alphabetical. Empty groups are dropped; rows keep their own
 * change tone either way.
 */
export function groupRows(rows: readonly DiffRowModel[], mode: GroupMode): DiffGroup[] {
  if (mode === "prefix") {
    const byPrefix = new Map<string, DiffRowModel[]>();
    for (const row of rows) {
      const list = byPrefix.get(row.prefix) ?? [];
      list.push(row);
      byPrefix.set(row.prefix, list);
    }
    return [...byPrefix.keys()].sort().map((prefix) => ({
      id: `prefix:${prefix}`,
      title: prefix,
      tone: "prefix" as const,
      rows: sortByAlias(byPrefix.get(prefix) ?? []),
    }));
  }
  const buckets = new Map<string, DiffRowModel[]>();
  for (const row of rows) {
    const id = kindGroupOf(row);
    const list = buckets.get(id) ?? [];
    list.push(row);
    buckets.set(id, list);
  }
  return KIND_GROUPS.flatMap((group) => {
    const list = buckets.get(group.id);
    return list && list.length > 0 ? [{ ...group, rows: sortByAlias(list) }] : [];
  });
}

function sortByAlias(rows: DiffRowModel[]): DiffRowModel[] {
  return [...rows].sort((a, b) => a.alias.localeCompare(b.alias));
}

/** Every whitespace-separated token of `q` must appear in the row's search text. */
export function filterRows(
  rows: readonly DiffRowModel[],
  q: string,
  kind: DiffKindFilter = "all",
  view: DiffView = "changed",
): DiffRowModel[] {
  const tokens = q.toLowerCase().split(/\s+/).filter(Boolean);
  return rows.filter((row) => {
    if (view === "changed" && row.change === "unchanged") return false;
    if (kind !== "all" && row.kind !== kind) return false;
    return tokens.every((token) => row.searchText.includes(token));
  });
}

function utcStamp(ms: number): string {
  const date = new Date(ms);
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${date.getUTCFullYear()}-${pad(date.getUTCMonth() + 1)}-${pad(date.getUTCDate())} ${pad(date.getUTCHours())}:${pad(date.getUTCMinutes())} UTC`;
}

export function sideKey(side: ReleaseDiffSide): string {
  return `${side.name}@${side.schema_version}:${side.version}`;
}

/**
 * The comparison as plain text for an incident channel or a postmortem: a
 * header, one padded line per changed alias, and under a JSON parameter one
 * indented line per field (`fieldLines`, uncapped). Secrets appear with
 * versions and modes only.
 */
export function releaseDiffAsText(diff: ReleaseDiffResponse, opts: BuildRowsOptions = {}): string {
  const from = diff.from;
  const to = diff.to;
  const where = diff.cross_environment
    ? `${from.namespace.env}/${from.namespace.app} → ${to.namespace.env}/${to.namespace.app}`
    : `${to.namespace.env}/${to.namespace.app}`;
  const shipped: string[] = [];
  if (to.created_by) shipped.push(`shipped by ${to.created_by}`);
  if (to.created_at_unix_ms) shipped.push(utcStamp(to.created_at_unix_ms));
  if (to.current && to.activation_revision) shipped.push(`rev ${to.activation_revision}`);
  const header = `${sideKey(from)} → ${sideKey(to)} in ${where}${shipped.length ? ` (${shipped.join(", ")})` : ""}`;
  const rows = buildRows(diff, opts).filter((row) => row.change !== "unchanged");
  if (rows.length === 0) return `${header}\nno differences`;
  const aliasWidth = Math.max(12, ...rows.map((row) => row.alias.length));
  const lines = rows.flatMap((row) => {
    const label = row.kind === "secret" ? "secret" : row.change;
    const structural =
      row.description?.kind === "json" ? (row.description.structural ?? null) : null;
    if (!structural || !row.fields) {
      return [`${label.padEnd(8)} ${row.alias.padEnd(aliasWidth)} ${row.summary}`];
    }
    const total = structural.changes.length;
    const versions = `v${row.row.from?.version ?? "—"} → v${row.row.to?.version ?? "—"}`;
    const head = `${total} ${countNoun(total, "fields")} (${formatFieldCounts(row.fields)}) ${versions}`;
    return [
      `${label.padEnd(8)} ${row.alias.padEnd(aliasWidth)} ${head}`,
      ...fieldLines(structural).map((line) => `  ${line}`),
    ];
  });
  return [header, ...lines].join("\n");
}
