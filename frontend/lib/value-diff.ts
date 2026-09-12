// Type-aware description of how one parameter value became another. Pure:
// the release diff view renders these, the plain-text export prints them.
//
// JSON is compared structurally on the raw token tree (components/JsonTree
// `parseJsonTree`), never through JSON.parse, so `1.0` stays `1.0` and big
// integers keep every digit. Objects are walked by key (union, sorted),
// arrays by index — index alignment is what operators expect for config
// lists; the side-by-side line diff covers reordering.

import { type JsonTreeNode, parseJsonTree } from "@/components/JsonTree";
import { HIGHLIGHT_MAX_BYTES } from "@/lib/json-text";
import { byteLength } from "@/lib/validation";

export type ValueChangeKind = "added" | "removed" | "changed";

export interface ValueChange {
  /** Object keys unquoted; array indexes as `[i]` segments (see `formatValuePath`). */
  path: string[];
  kind: ValueChangeKind;
  /** Raw JSON text of the leaf (strings keep their quotes). */
  before?: string;
  after?: string;
}

export interface StructuralDiff {
  changes: ValueChange[];
  unchangedLeaves: number;
  /** `maxLeaves` changes were recorded and the walk stopped listing more. */
  truncated: boolean;
}

/** Either side over the highlighter cap: structural mode is disabled for it. */
export const STRUCTURAL_MAX_BYTES = HIGHLIGHT_MAX_BYTES;

export function overStructuralCap(text: string): boolean {
  return byteLength(text) > STRUCTURAL_MAX_BYTES;
}

/** `database.pool.max`, `hosts[2].port`. */
export function formatValuePath(path: readonly string[]): string {
  let out = "";
  for (const segment of path) {
    if (segment.startsWith("[")) out += segment;
    else out += out === "" ? segment : `.${segment}`;
  }
  return out;
}

function unquoteKey(raw: string): string {
  try {
    const value = JSON.parse(raw);
    return typeof value === "string" ? value : raw;
  } catch {
    return raw;
  }
}

/** Minified JSON text of a subtree, for the one-sided rows of a structural diff. */
export function nodeText(node: JsonTreeNode): string {
  if (node.kind === "scalar") return node.text;
  const open = node.kind === "object" ? "{" : "[";
  const close = node.kind === "object" ? "}" : "]";
  const parts: string[] = [];
  for (const entry of node.entries) {
    const value = nodeText(entry.node);
    parts.push(entry.key === null ? value : `${entry.key}:${value}`);
  }
  return `${open}${parts.join(",")}${close}`;
}

/**
 * Leaf-level differences between two JSON documents, or null when either
 * side does not tokenize as JSON. Equal scalars are compared on their raw
 * text, so `1.0` vs `1` is a change and is shown as one.
 */
export function structuralDiff(
  before: string,
  after: string,
  maxLeaves = 5_000,
): StructuralDiff | null {
  const left = parseJsonTree(before);
  const right = parseJsonTree(after);
  if (!left || !right) return null;
  const result: StructuralDiff = { changes: [], unchangedLeaves: 0, truncated: false };
  const record = (change: ValueChange) => {
    if (result.changes.length >= maxLeaves) {
      result.truncated = true;
      return;
    }
    result.changes.push(change);
  };
  const walk = (a: JsonTreeNode | undefined, b: JsonTreeNode | undefined, path: string[]) => {
    if (!a && !b) return;
    if (!a) {
      record({ path, kind: "added", after: nodeText(b as JsonTreeNode) });
      return;
    }
    if (!b) {
      record({ path, kind: "removed", before: nodeText(a) });
      return;
    }
    if (a.kind === "scalar" || b.kind === "scalar" || a.kind !== b.kind) {
      const l = nodeText(a);
      const r = nodeText(b);
      if (l === r) result.unchangedLeaves += 1;
      else record({ path, kind: "changed", before: l, after: r });
      return;
    }
    if (a.entries.length === 0 && b.entries.length === 0) {
      result.unchangedLeaves += 1;
      return;
    }
    if (a.kind === "object") {
      const la = new Map<string, JsonTreeNode>();
      const lb = new Map<string, JsonTreeNode>();
      for (const entry of a.entries) la.set(unquoteKey(entry.key ?? ""), entry.node);
      for (const entry of b.entries) lb.set(unquoteKey(entry.key ?? ""), entry.node);
      const keys = [...new Set([...la.keys(), ...lb.keys()])].sort();
      for (const key of keys) walk(la.get(key), lb.get(key), [...path, key]);
      return;
    }
    const length = Math.max(a.entries.length, b.entries.length);
    for (let index = 0; index < length; index += 1) {
      walk(a.entries[index]?.node, b.entries[index]?.node, [...path, `[${index}]`]);
    }
  };
  walk(left, right, []);
  return result;
}

// ---- scalars ---------------------------------------------------------------

/** Go's duration syntax, compound units allowed (`1h30m`, `1.5s`, `250ms`). */
const GO_DURATION = /^(\d+(?:\.\d+)?(?:ns|us|µs|ms|s|m|h))+$/;
const GO_DURATION_PART = /(\d+(?:\.\d+)?)(ns|us|µs|ms|s|m|h)/g;
const UNIT_MS: Record<string, number> = {
  ns: 1e-6,
  us: 1e-3,
  µs: 1e-3,
  ms: 1,
  s: 1_000,
  m: 60_000,
  h: 3_600_000,
};

export function isGoDuration(text: string): boolean {
  return GO_DURATION.test(text);
}

/** Milliseconds, or null when the text is not a Go duration. */
export function parseGoDurationMs(text: string): number | null {
  if (!GO_DURATION.test(text)) return null;
  let total = 0;
  for (const match of text.matchAll(GO_DURATION_PART)) {
    total += Number(match[1]) * (UNIT_MS[match[2]] ?? 0);
  }
  return total;
}

const INTEGER = /^-?\d+$/;

/** A number with a sign and no exponent; the real minus sign, as the design shows. */
function signed(text: string): string {
  if (text.startsWith("-")) return `−${text.slice(1)}`;
  return `+${text}`;
}

function trimFloat(value: number): string {
  if (!Number.isFinite(value)) return String(value);
  const fixed = Math.abs(value) >= 1e15 ? value.toExponential(3) : value.toPrecision(6);
  return String(Number(fixed));
}

function percentText(delta: number, base: number): string | undefined {
  if (base === 0 || !Number.isFinite(delta) || !Number.isFinite(base)) return undefined;
  const percent = (delta / Math.abs(base)) * 100;
  const rounded = Math.abs(percent) >= 10 ? Math.round(percent) : Math.round(percent * 10) / 10;
  return `${percent < 0 ? "−" : "+"}${Math.abs(rounded)} %`;
}

export type ScalarChange =
  | { kind: "boolean"; before?: string; after?: string }
  | {
      kind: "number";
      before?: string;
      after?: string;
      /** Signed difference on the raw decimal text (BigInt for integers). */
      delta?: string;
      /** Omitted when the before side is 0 or missing. */
      percent?: string;
    }
  | {
      kind: "duration";
      before?: string;
      after?: string;
      /** `×10`, `×0.5`; omitted when the before side is 0 or missing. */
      ratio?: string;
    }
  | {
      kind: "string";
      before?: string;
      after?: string;
      /** Characters shared at each end, so the differing span can be marked. */
      common: { prefix: number; suffix: number } | null;
      /** Either side is over 80 characters or spans lines: expand to compare. */
      long: boolean;
    }
  | { kind: "binary"; beforeBytes?: number; afterBytes?: number };

export type JsonChange = {
  kind: "json";
  before?: string;
  after?: string;
  /** null when a side is not JSON, both sides are present and one is over the cap, or one side is missing. */
  structural: StructuralDiff | null;
  /** A side is over `STRUCTURAL_MAX_BYTES`. */
  oversize: boolean;
  /** A present side failed to tokenize. */
  invalid: boolean;
};

export type ValueChangeDescription = ScalarChange | JsonChange;

function commonEnds(a: string, b: string): { prefix: number; suffix: number } {
  let prefix = 0;
  const max = Math.min(a.length, b.length);
  while (prefix < max && a[prefix] === b[prefix]) prefix += 1;
  let suffix = 0;
  while (suffix < max - prefix && a[a.length - 1 - suffix] === b[b.length - 1 - suffix]) {
    suffix += 1;
  }
  return { prefix, suffix };
}

const SHORT_STRING = 80;

/**
 * Describes a scalar change by content type. `format` is the schema's
 * `format` for the alias when the caller has it (`go-duration` promotes a
 * string to a duration); duration syntax is also recognised on its own.
 */
export function describeScalarChange(
  before: string | undefined,
  after: string | undefined,
  contentType: string,
  format?: string,
): ScalarChange {
  if (contentType === "boolean") return { kind: "boolean", before, after };
  if (contentType === "integer" || contentType === "float") {
    const change: ScalarChange = { kind: "number", before, after };
    if (before !== undefined && after !== undefined) {
      if (INTEGER.test(before) && INTEGER.test(after)) {
        try {
          const delta = BigInt(after) - BigInt(before);
          change.delta = signed(delta.toString());
          change.percent = percentText(Number(delta), Number(BigInt(before)));
        } catch {
          // Not a decimal integer after all; leave the raw texts.
        }
      } else {
        const l = Number(before);
        const r = Number(after);
        if (Number.isFinite(l) && Number.isFinite(r)) {
          change.delta = signed(trimFloat(r - l));
          change.percent = percentText(r - l, l);
        }
      }
    }
    return change;
  }
  if (contentType === "binary") {
    return {
      kind: "binary",
      beforeBytes: before === undefined ? undefined : byteLength(before),
      afterBytes: after === undefined ? undefined : byteLength(after),
    };
  }
  const durationLike =
    format === "go-duration" ||
    ((before === undefined || isGoDuration(before)) &&
      (after === undefined || isGoDuration(after)) &&
      (before !== undefined || after !== undefined));
  if (durationLike && contentType === "string") {
    const change: ScalarChange = { kind: "duration", before, after };
    const l = before === undefined ? null : parseGoDurationMs(before);
    const r = after === undefined ? null : parseGoDurationMs(after);
    if (l !== null && r !== null && l > 0) {
      const ratio = r / l;
      change.ratio = `×${trimFloat(Math.round(ratio * 100) / 100)}`;
    }
    return change;
  }
  const long =
    (before !== undefined && (before.length > SHORT_STRING || before.includes("\n"))) ||
    (after !== undefined && (after.length > SHORT_STRING || after.includes("\n")));
  return {
    kind: "string",
    before,
    after,
    common: before !== undefined && after !== undefined ? commonEnds(before, after) : null,
    long,
  };
}

/** `describeScalarChange`, plus the structural JSON case. */
export function describeChange(
  before: string | undefined,
  after: string | undefined,
  contentType: string,
  format?: string,
): ValueChangeDescription {
  if (contentType !== "json") return describeScalarChange(before, after, contentType, format);
  const oversize =
    (before !== undefined && overStructuralCap(before)) ||
    (after !== undefined && overStructuralCap(after));
  const invalid =
    (before !== undefined && !oversize && parseJsonTree(before) === null) ||
    (after !== undefined && !oversize && parseJsonTree(after) === null);
  const structural =
    before !== undefined && after !== undefined && !oversize && !invalid
      ? structuralDiff(before, after)
      : null;
  return { kind: "json", before, after, structural, oversize, invalid };
}
