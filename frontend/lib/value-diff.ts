// Type-aware description of how one parameter value became another. Pure:
// the release diff view renders these, the plain-text export prints them.
//
// JSON is compared structurally on the raw token tree (components/JsonTree
// `parseJsonTree`), never through JSON.parse, so `1.0` stays `1.0` and big
// integers keep every digit. Objects are walked by key (union, sorted),
// arrays by index — index alignment is what operators expect for config
// lists; the side-by-side line diff covers reordering.

import { type JsonTreeNode, parseJsonTree } from "@/components/JsonTree";
import { HIGHLIGHT_MAX_BYTES, tokenizeJson } from "@/lib/json-text";
import { byteLength } from "@/lib/validation";

export type ValueChangeKind = "added" | "removed" | "changed" | "moved";

export interface ValueChange {
  /** Object keys unquoted; array indexes as `[i]` segments (see `formatValuePath`). */
  path: string[];
  kind: ValueChangeKind;
  /** Raw JSON text of the leaf (strings keep their quotes). */
  before?: string;
  after?: string;
  /** `moved` only: where the value was; `path` is where it is now. Never set on other kinds. */
  fromPath?: string[];
}

export interface StructuralDiff {
  changes: ValueChange[];
  unchangedLeaves: number;
  /** `maxLeaves` changes were recorded and the walk stopped listing more. */
  truncated: boolean;
}

export interface FieldCounts {
  added: number;
  removed: number;
  changed: number;
  moved: number;
}

/** How many changes of each kind a structural diff lists. */
export function fieldCounts(diff: StructuralDiff): FieldCounts {
  const counts: FieldCounts = { added: 0, removed: 0, changed: 0, moved: 0 };
  for (const change of diff.changes) counts[change.kind] += 1;
  return counts;
}

/** Minified object or array text, as `nodeText` produces for a one-sided subtree. */
export function isSubtreeText(text: string): boolean {
  return /^\s*[{[]/.test(text);
}

/**
 * A value that is too short to identify a move: a literal, an empty
 * container, a one- or two-digit number or a string of under three
 * characters. Pairing those would call every `enabled: true` that moved
 * between keys a move of the same field.
 */
function trivialLeafText(text: string): boolean {
  if (text === "true" || text === "false" || text === "null" || text === "{}" || text === "[]") {
    return true;
  }
  if (text.startsWith('"')) return text.length < 5;
  if (/^-?\d/.test(text)) return text.length < 3;
  return false;
}

interface Leaf {
  path: string[];
  text: string;
}

/** The scalar leaves (and empty containers) under a node, with their paths. */
function collectLeaves(node: JsonTreeNode, path: string[], out: Leaf[]): void {
  if (node.kind === "scalar" || node.entries.length === 0) {
    out.push({ path, text: nodeText(node) });
    return;
  }
  node.entries.forEach((entry, index) => {
    const segment = node.kind === "object" ? unquoteKey(entry.key ?? "") : `[${index}]`;
    collectLeaves(entry.node, [...path, segment], out);
  });
}

function pathKey(path: readonly string[]): string {
  return path.join("\u0000");
}

/**
 * Pairs a removed leaf with an added leaf of the same raw text into one
 * `moved` change at the added position (the list reads "what the new
 * document has"). Only unique texts on both sides pair, so two fields that
 * shared a value never guess at each other, and trivial literals never pair.
 *
 * Whole subtrees pair first on their minified text, so a renamed object is
 * one move. Then the leaves inside the remaining one-sided subtrees take
 * part: a key that moved under a new parent (`legacy_url` →
 * `endpoints.legacy`, where `endpoints` is new) pairs with its twin, and the
 * subtree that held it is split into its leaves — moved ones and the rest —
 * down to the branches that contain a move; branches without one stay whole.
 * An object wrapped in a new parent is therefore a move of each field, not
 * of the object. Array elements aligned by index are `changed`, never
 * candidates; use the line view for reorders.
 */
export function detectMoves(changes: ValueChange[]): ValueChange[] {
  const merged = new Set<number>();
  const moves = new Map<number, ValueChange>();

  // Phase 1: whole changes (scalar or subtree) with identical text.
  const removedWhole = new Map<string, number[]>();
  const addedWhole = new Map<string, number[]>();
  const index = (map: Map<string, number[]>, text: string, at: number) => {
    if (trivialLeafText(text)) return;
    const list = map.get(text) ?? [];
    list.push(at);
    map.set(text, list);
  };
  changes.forEach((change, at) => {
    if (change.kind === "removed" && change.before !== undefined) {
      index(removedWhole, change.before, at);
    }
    if (change.kind === "added" && change.after !== undefined) index(addedWhole, change.after, at);
  });
  for (const [text, twins] of addedWhole) {
    const sources = removedWhole.get(text);
    if (twins.length !== 1 || !sources || sources.length !== 1) continue;
    merged.add(sources[0]);
    moves.set(twins[0], {
      path: changes[twins[0]].path,
      kind: "moved",
      fromPath: changes[sources[0]].path,
      before: text,
      after: text,
    });
  }

  // Phase 2: leaves inside the one-sided changes that are still unpaired.
  interface Located extends Leaf {
    at: number;
  }
  const trees = new Map<number, JsonTreeNode>();
  const leavesOf = (at: number, text: string): Located[] => {
    if (!isSubtreeText(text)) return [{ at, path: changes[at].path, text }];
    const node = parseJsonTree(text);
    if (!node) return [];
    trees.set(at, node);
    const out: Leaf[] = [];
    collectLeaves(node, changes[at].path, out);
    return out.map((leaf) => ({ ...leaf, at }));
  };
  const removedLeaves = new Map<string, Located[]>();
  const addedLeaves = new Map<string, Located[]>();
  const indexLeaves = (map: Map<string, Located[]>, leaves: Located[]) => {
    for (const leaf of leaves) {
      if (trivialLeafText(leaf.text)) continue;
      const list = map.get(leaf.text) ?? [];
      list.push(leaf);
      map.set(leaf.text, list);
    }
  };
  changes.forEach((change, at) => {
    if (merged.has(at) || moves.has(at)) return;
    if (change.kind === "removed" && change.before !== undefined) {
      indexLeaves(removedLeaves, leavesOf(at, change.before));
    }
    if (change.kind === "added" && change.after !== undefined) {
      indexLeaves(addedLeaves, leavesOf(at, change.after));
    }
  });
  /** Per change index: the paths of its leaves that paired, and for added ones where each came from. */
  const pairedAdded = new Map<number, Map<string, string[]>>();
  const pairedRemoved = new Map<number, Set<string>>();
  for (const [text, twins] of addedLeaves) {
    const sources = removedLeaves.get(text);
    if (twins.length !== 1 || !sources || sources.length !== 1) continue;
    const [twin] = twins;
    const [source] = sources;
    const byPath = pairedAdded.get(twin.at) ?? new Map<string, string[]>();
    byPath.set(pathKey(twin.path), source.path);
    pairedAdded.set(twin.at, byPath);
    const paths = pairedRemoved.get(source.at) ?? new Set<string>();
    paths.add(pathKey(source.path));
    pairedRemoved.set(source.at, paths);
  }
  if (moves.size === 0 && pairedAdded.size === 0) return changes;

  /** Splits a one-sided subtree down to the branches that hold a paired leaf. */
  const expand = (
    node: JsonTreeNode,
    path: string[],
    paired: (key: string) => boolean,
    emit: (leaf: Leaf | null, node: JsonTreeNode, path: string[], isPaired: boolean) => void,
  ) => {
    const leaves: Leaf[] = [];
    collectLeaves(node, path, leaves);
    if (!leaves.some((leaf) => paired(pathKey(leaf.path)))) {
      emit(null, node, path, false);
      return;
    }
    if (node.kind === "scalar" || node.entries.length === 0) {
      emit(leaves[0], node, path, true);
      return;
    }
    node.entries.forEach((entry, i) => {
      const segment = node.kind === "object" ? unquoteKey(entry.key ?? "") : `[${i}]`;
      expand(entry.node, [...path, segment], paired, emit);
    });
  };

  const out: ValueChange[] = [];
  changes.forEach((change, at) => {
    if (merged.has(at)) return;
    const move = moves.get(at);
    if (move) {
      out.push(move);
      return;
    }
    const addedPairs = pairedAdded.get(at);
    if (addedPairs) {
      const tree = trees.get(at);
      if (!tree) {
        // A scalar leaf whose twin sat inside a removed subtree.
        const from = addedPairs.get(pathKey(change.path));
        out.push({
          path: change.path,
          kind: "moved",
          fromPath: from ?? [],
          before: change.after,
          after: change.after,
        });
        return;
      }
      expand(
        tree,
        change.path,
        (key) => addedPairs.has(key),
        (leaf, node, path, isPaired) => {
          const from = leaf ? addedPairs.get(pathKey(leaf.path)) : undefined;
          if (isPaired && leaf && from) {
            out.push({ path, kind: "moved", fromPath: from, before: leaf.text, after: leaf.text });
          } else {
            out.push({ path, kind: "added", after: nodeText(node) });
          }
        },
      );
      return;
    }
    const removedPairs = pairedRemoved.get(at);
    if (removedPairs) {
      const tree = trees.get(at);
      if (!tree) return; // A scalar that reappeared inside an added subtree: shown there as moved.
      expand(
        tree,
        change.path,
        (key) => removedPairs.has(key),
        (_leaf, node, path, isPaired) => {
          if (!isPaired) out.push({ path, kind: "removed", before: nodeText(node) });
        },
      );
      return;
    }
    out.push(change);
  });
  return out;
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
  result.changes = detectMoves(result.changes);
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

export type LeafChange =
  | ScalarChange
  | {
      /** `null` on the present side(s); `mixed` when the two sides are different JSON kinds (`"30"` vs `30`); `subtree` for an object or array. */
      kind: "null" | "mixed" | "subtree";
      before?: string;
      after?: string;
    };

type LeafKind = "string" | "number" | "boolean" | "null" | "subtree" | "other";

function leafKind(text: string): LeafKind {
  if (isSubtreeText(text)) return "subtree";
  const tokens = tokenizeJson(text.trim()).filter((token) => token.kind !== "ws");
  const kind = tokens.length === 1 ? tokens[0].kind : "other";
  return kind === "string" || kind === "number" || kind === "boolean" || kind === "null"
    ? kind
    : "other";
}

function unquote(text: string | undefined): string | undefined {
  if (text === undefined) return undefined;
  try {
    const value = JSON.parse(text);
    return typeof value === "string" ? value : text;
  } catch {
    return text;
  }
}

/**
 * The typed description of one structural leaf, so a field line gets the
 * same deltas a scalar parameter does: numbers their difference and percent,
 * Go durations their ratio, strings their differing span. String texts are
 * unquoted in the result (`StringToken` re-quotes); every other kind keeps
 * its raw JSON text.
 */
export function describeLeafChange(before?: string, after?: string): LeafChange {
  const kb = before === undefined ? undefined : leafKind(before);
  const ka = after === undefined ? undefined : leafKind(after);
  if (kb !== undefined && ka !== undefined && kb !== ka) return { kind: "mixed", before, after };
  const kind = kb ?? ka;
  if (kind === "subtree") return { kind: "subtree", before, after };
  if (kind === "null") return { kind: "null", before, after };
  if (kind === "boolean") return describeScalarChange(before, after, "boolean");
  if (kind === "number") {
    const integer =
      (before === undefined || INTEGER.test(before)) &&
      (after === undefined || INTEGER.test(after));
    return describeScalarChange(before, after, integer ? "integer" : "float");
  }
  if (kind === "string") return describeScalarChange(unquote(before), unquote(after), "string");
  return { kind: "mixed", before, after };
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
