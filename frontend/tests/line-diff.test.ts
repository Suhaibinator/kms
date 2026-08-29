import { describe, expect, it } from "vitest";
import {
  DIFF_MAX_BYTES,
  diffLines,
  foldUnchanged,
  MAX_TABLE_CELLS,
  toSideBySide,
} from "@/lib/line-diff";

function ops(before: string, after: string): string[] {
  return diffLines(before, after).lines.map((line) => `${line.op}:${line.text}`);
}

describe("diffLines", () => {
  it("reports identical inputs as unchanged", () => {
    const result = diffLines("a\nb", "a\nb");
    expect(result.lines.map((line) => line.op)).toEqual(["same", "same"]);
    expect(result.added).toBe(0);
    expect(result.removed).toBe(0);
    expect(result.truncated).toBe(false);
  });

  it("aligns a changed line between unchanged ones and numbers each side", () => {
    const result = diffLines("a\nb\nc", "a\nB\nc");
    expect(result.lines).toEqual([
      { op: "same", text: "a", left: 1, right: 1 },
      { op: "del", text: "b", left: 2 },
      { op: "add", text: "B", right: 2 },
      { op: "same", text: "c", left: 3, right: 3 },
    ]);
    expect(result.added).toBe(1);
    expect(result.removed).toBe(1);
  });

  it("finds insertions and deletions", () => {
    expect(ops("a\nc", "a\nb\nc")).toEqual(["same:a", "add:b", "same:c"]);
    expect(ops("a\nb\nc", "a\nc")).toEqual(["same:a", "del:b", "same:c"]);
    expect(ops("", "x\ny")).toEqual(["add:x", "add:y"]);
    expect(ops("x", "")).toEqual(["del:x"]);
  });

  it("keeps a long common tail aligned after an early change", () => {
    const tail = Array.from({ length: 50 }, (_, index) => `line ${index}`).join("\n");
    const result = diffLines(`start\n${tail}`, `begin\n${tail}`);
    expect(result.added).toBe(1);
    expect(result.removed).toBe(1);
    expect(result.lines.filter((line) => line.op === "same")).toHaveLength(50);
  });

  it("falls back to a replacement above the byte cap", () => {
    const big = "x".repeat(DIFF_MAX_BYTES + 1);
    const result = diffLines(big, `${big}y`);
    expect(result.truncated).toBe(true);
    expect(result.lines.map((line) => line.op)).toEqual(["del", "add"]);
  });

  it("falls back to a replacement when the middle would need too large a table", () => {
    const side = Math.ceil(Math.sqrt(MAX_TABLE_CELLS)) + 1;
    const before = Array.from({ length: side }, (_, index) => `a${index}`).join("\n");
    const after = Array.from({ length: side }, (_, index) => `b${index}`).join("\n");
    const result = diffLines(before, after);
    expect(result.truncated).toBe(true);
    expect(result.removed).toBe(side);
    expect(result.added).toBe(side);
  });
});

describe("toSideBySide", () => {
  it("pairs removed and added runs row by row and pads the shorter side", () => {
    const rows = toSideBySide(diffLines("a\nb\nc", "a\nB\nC\nD").lines);
    expect(rows).toEqual([
      { kind: "same", left: { line: 1, text: "a" }, right: { line: 1, text: "a" } },
      { kind: "change", left: { line: 2, text: "b" }, right: { line: 2, text: "B" } },
      { kind: "change", left: { line: 3, text: "c" }, right: { line: 3, text: "C" } },
      { kind: "change", left: null, right: { line: 4, text: "D" } },
    ]);
  });
});

describe("foldUnchanged", () => {
  it("hides long unchanged stretches but keeps context around a change", () => {
    const lines = Array.from({ length: 20 }, (_, index) => `l${index + 1}`);
    const changed = lines.map((line) => (line === "l10" ? "L10" : line));
    const folded = foldUnchanged(
      toSideBySide(diffLines(lines.join("\n"), changed.join("\n")).lines),
    );
    expect(folded.map((entry) => (entry.kind === "fold" ? `fold:${entry.count}` : "row"))).toEqual([
      "fold:6",
      "row",
      "row",
      "row",
      "row",
      "row",
      "row",
      "row",
      "fold:7",
    ]);
    // The fold remembers where the hidden rows start so the view can expand them.
    expect(folded[0]).toEqual({ kind: "fold", count: 6, at: 0 });
  });

  it("never folds a run too short to save space", () => {
    const rows = toSideBySide(diffLines("a\nb\nc\nd", "a\nb\nc\nD").lines);
    expect(foldUnchanged(rows).every((entry) => entry.kind === "row")).toBe(true);
  });
});
