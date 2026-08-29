/**
 * Line-level diff for the console's value viewers.
 *
 * Dependency-free: common prefix and suffix are trimmed first, then the
 * middle is aligned with a longest-common-subsequence table. Values are at
 * most a few hundred lines, so the quadratic table is fine; anything larger
 * than the highlighter can handle (or whose middle would need a table beyond
 * `MAX_TABLE_CELLS`) is reported as a plain replacement instead of hanging
 * the tab.
 */

import { HIGHLIGHT_MAX_BYTES } from "@/lib/json-text";
import { byteLength } from "@/lib/validation";

export type DiffOp = "same" | "add" | "del";

/** One line of the unified sequence; `left`/`right` are 1-based line numbers. */
export interface DiffLine {
  op: DiffOp;
  text: string;
  left?: number;
  right?: number;
}

/** One row of the side-by-side view; a changed row may be empty on one side. */
export interface DiffRow {
  kind: "same" | "change";
  left: { line: number; text: string } | null;
  right: { line: number; text: string } | null;
}

export interface DiffResult {
  lines: DiffLine[];
  added: number;
  removed: number;
  /** The inputs were too large to align; every line is reported as replaced. */
  truncated: boolean;
}

/** Either side above this many bytes is not aligned line by line. */
export const DIFF_MAX_BYTES = HIGHLIGHT_MAX_BYTES;
/** Largest LCS table the aligner will build (rows × columns of the trimmed middle). */
export const MAX_TABLE_CELLS = 2_000_000;

function splitText(text: string): string[] {
  return text === "" ? [] : text.split("\n");
}

/** Longest-common-subsequence alignment of `a` and `b`, as a sequence of operations. */
function align(a: string[], b: string[]): DiffLine[] {
  const n = a.length;
  const m = b.length;
  // table[i][j] = LCS length of a[i..] and b[j..]; one flat typed array keeps it cheap.
  const width = m + 1;
  const table = new Uint32Array((n + 1) * width);
  for (let i = n - 1; i >= 0; i -= 1) {
    for (let j = m - 1; j >= 0; j -= 1) {
      table[i * width + j] =
        a[i] === b[j]
          ? table[(i + 1) * width + j + 1] + 1
          : Math.max(table[(i + 1) * width + j], table[i * width + j + 1]);
    }
  }
  const out: DiffLine[] = [];
  let i = 0;
  let j = 0;
  while (i < n && j < m) {
    if (a[i] === b[j]) {
      out.push({ op: "same", text: a[i] });
      i += 1;
      j += 1;
    } else if (table[(i + 1) * width + j] >= table[i * width + j + 1]) {
      out.push({ op: "del", text: a[i] });
      i += 1;
    } else {
      out.push({ op: "add", text: b[j] });
      j += 1;
    }
  }
  while (i < n) {
    out.push({ op: "del", text: a[i] });
    i += 1;
  }
  while (j < m) {
    out.push({ op: "add", text: b[j] });
    j += 1;
  }
  return out;
}

function number(lines: DiffLine[]): DiffLine[] {
  let left = 0;
  let right = 0;
  return lines.map((line) => {
    if (line.op === "same") {
      left += 1;
      right += 1;
      return { ...line, left, right };
    }
    if (line.op === "del") {
      left += 1;
      return { ...line, left };
    }
    right += 1;
    return { ...line, right };
  });
}

/**
 * Diffs `before` against `after` line by line. Identical inputs yield only
 * `same` lines; a replacement of everything is the fallback for inputs too
 * large to align (`truncated: true`).
 */
export function diffLines(before: string, after: string): DiffResult {
  const a = splitText(before);
  const b = splitText(after);
  const finish = (lines: DiffLine[], truncated: boolean): DiffResult => ({
    lines: number(lines),
    added: lines.filter((line) => line.op === "add").length,
    removed: lines.filter((line) => line.op === "del").length,
    truncated,
  });

  if (byteLength(before) > DIFF_MAX_BYTES || byteLength(after) > DIFF_MAX_BYTES) {
    return finish(
      [
        ...a.map((text): DiffLine => ({ op: "del", text })),
        ...b.map((text): DiffLine => ({ op: "add", text })),
      ],
      true,
    );
  }

  let prefix = 0;
  while (prefix < a.length && prefix < b.length && a[prefix] === b[prefix]) prefix += 1;
  let suffix = 0;
  while (
    suffix < a.length - prefix &&
    suffix < b.length - prefix &&
    a[a.length - 1 - suffix] === b[b.length - 1 - suffix]
  ) {
    suffix += 1;
  }

  const middleA = a.slice(prefix, a.length - suffix);
  const middleB = b.slice(prefix, b.length - suffix);
  const head = a.slice(0, prefix).map((text): DiffLine => ({ op: "same", text }));
  const tail = a.slice(a.length - suffix).map((text): DiffLine => ({ op: "same", text }));

  if ((middleA.length + 1) * (middleB.length + 1) > MAX_TABLE_CELLS) {
    return finish(
      [
        ...head,
        ...middleA.map((text): DiffLine => ({ op: "del", text })),
        ...middleB.map((text): DiffLine => ({ op: "add", text })),
        ...tail,
      ],
      true,
    );
  }

  return finish([...head, ...align(middleA, middleB), ...tail], false);
}

/**
 * Pairs the unified sequence into side-by-side rows: each run of removed and
 * added lines is zipped so the i-th removed line sits beside the i-th added
 * one, and the longer side's leftovers sit beside an empty cell.
 */
export function toSideBySide(lines: DiffLine[]): DiffRow[] {
  const rows: DiffRow[] = [];
  let k = 0;
  while (k < lines.length) {
    const line = lines[k];
    if (line.op === "same") {
      rows.push({
        kind: "same",
        left: { line: line.left ?? 0, text: line.text },
        right: { line: line.right ?? 0, text: line.text },
      });
      k += 1;
      continue;
    }
    const dels: DiffLine[] = [];
    const adds: DiffLine[] = [];
    while (k < lines.length && lines[k].op !== "same") {
      if (lines[k].op === "del") dels.push(lines[k]);
      else adds.push(lines[k]);
      k += 1;
    }
    const length = Math.max(dels.length, adds.length);
    for (let index = 0; index < length; index += 1) {
      const del = dels[index];
      const add = adds[index];
      rows.push({
        kind: "change",
        left: del ? { line: del.left ?? 0, text: del.text } : null,
        right: add ? { line: add.right ?? 0, text: add.text } : null,
      });
    }
  }
  return rows;
}

/** A row of the folded view: either a real row or a placeholder for hidden unchanged rows. */
export type FoldedRow = { kind: "row"; row: DiffRow } | { kind: "fold"; count: number; at: number };

/**
 * Hides long unchanged stretches, keeping `context` rows on either side of a
 * change so the reader sees where the change sits. Runs of at most
 * `2 * context + 1` unchanged rows are never folded — a one-row fold would
 * take more space than the row it hides.
 */
export function foldUnchanged(rows: DiffRow[], context = 3): FoldedRow[] {
  const out: FoldedRow[] = [];
  let index = 0;
  while (index < rows.length) {
    if (rows[index].kind === "change") {
      out.push({ kind: "row", row: rows[index] });
      index += 1;
      continue;
    }
    let end = index;
    while (end < rows.length && rows[end].kind === "same") end += 1;
    const run = end - index;
    const leading = index === 0 ? 0 : context;
    const trailing = end === rows.length ? 0 : context;
    if (run <= leading + trailing + 1) {
      for (let k = index; k < end; k += 1) out.push({ kind: "row", row: rows[k] });
    } else {
      for (let k = index; k < index + leading; k += 1) out.push({ kind: "row", row: rows[k] });
      out.push({ kind: "fold", count: run - leading - trailing, at: index + leading });
      for (let k = end - trailing; k < end; k += 1) out.push({ kind: "row", row: rows[k] });
    }
    index = end;
  }
  return out;
}
