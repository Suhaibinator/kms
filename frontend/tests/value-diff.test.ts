import { describe, expect, it } from "vitest";
import { parseJsonTree } from "@/components/JsonTree";
import {
  describeChange,
  describeScalarChange,
  formatValuePath,
  isGoDuration,
  nodeText,
  overStructuralCap,
  parseGoDurationMs,
  STRUCTURAL_MAX_BYTES,
  structuralDiff,
} from "@/lib/value-diff";

describe("structuralDiff", () => {
  it("lists leaf changes by path with unchanged subtrees counted, not listed", () => {
    const before = '{"database":{"pool":{"max":50,"idle":10},"host":"db"},"debug":false}';
    const after = '{"database":{"pool":{"max":5,"idle":10},"host":"db"},"debug":false,"beta":true}';
    const diff = structuralDiff(before, after);
    expect(diff).not.toBeNull();
    expect(diff?.changes).toEqual([
      { path: ["beta"], kind: "added", after: "true" },
      { path: ["database", "pool", "max"], kind: "changed", before: "50", after: "5" },
    ]);
    expect(diff?.unchangedLeaves).toBe(3);
    expect(diff?.truncated).toBe(false);
  });

  it("compares scalars on their raw text, so 1.0 and 1 differ while big integers keep their digits", () => {
    expect(structuralDiff('{"a":1.0}', '{"a":1.0}')?.changes).toEqual([]);
    expect(structuralDiff('{"a":1.0}', '{"a":1}')?.changes).toEqual([
      { path: ["a"], kind: "changed", before: "1.0", after: "1" },
    ]);
    const big = "12345678901234567890123";
    expect(structuralDiff(`{"n":${big}}`, `{"n":${big}1}`)?.changes[0]).toEqual({
      path: ["n"],
      kind: "changed",
      before: big,
      after: `${big}1`,
    });
  });

  it("aligns arrays by index and shows a kind change as one changed leaf", () => {
    expect(structuralDiff('{"hosts":["a","b"]}', '{"hosts":["a","c","d"]}')?.changes).toEqual([
      { path: ["hosts", "[1]"], kind: "changed", before: '"b"', after: '"c"' },
      { path: ["hosts", "[2]"], kind: "added", after: '"d"' },
    ]);
    expect(structuralDiff('{"x":{"a":1}}', '{"x":[1]}')?.changes).toEqual([
      { path: ["x"], kind: "changed", before: '{"a":1}', after: "[1]" },
    ]);
    expect(structuralDiff('{"x":{"a":1}}', '{"x":7}')?.changes).toEqual([
      { path: ["x"], kind: "changed", before: '{"a":1}', after: "7" },
    ]);
  });

  it("returns null when a side is not JSON and truncates past maxLeaves", () => {
    expect(structuralDiff("{", "{}")).toBeNull();
    expect(structuralDiff("{}", "not json")).toBeNull();
    const many = JSON.stringify(
      Object.fromEntries(Array.from({ length: 10 }, (_, i) => [`k${i}`, i])),
    );
    const diff = structuralDiff("{}", many, 3);
    expect(diff?.changes).toHaveLength(3);
    expect(diff?.truncated).toBe(true);
  });

  it("treats empty containers as one leaf and keeps object keys unquoted in paths", () => {
    expect(structuralDiff("{}", "{}")).toEqual({
      changes: [],
      unchangedLeaves: 1,
      truncated: false,
    });
    expect(structuralDiff('{"a b":{"c":[]}}', '{"a b":{"c":[]}}')?.unchangedLeaves).toBe(1);
    expect(formatValuePath(["database", "pool", "max"])).toBe("database.pool.max");
    expect(formatValuePath(["hosts", "[2]", "port"])).toBe("hosts[2].port");
    expect(formatValuePath([])).toBe("");
  });

  it("serialises a subtree minified from the token tree", () => {
    const node = parseJsonTree(' { "a" : [ 1 , 2.50 , "x" ] , "b" : null } ');
    expect(node && nodeText(node)).toBe('{"a":[1,2.50,"x"],"b":null}');
  });
});

describe("describeScalarChange", () => {
  it("computes integer deltas with BigInt and percentages against the before side", () => {
    expect(describeScalarChange("100", "20", "integer")).toEqual({
      kind: "number",
      before: "100",
      after: "20",
      delta: "−80",
      percent: "−80 %",
    });
    expect(describeScalarChange("0", "5", "integer")).toEqual({
      kind: "number",
      before: "0",
      after: "5",
      delta: "+5",
      percent: undefined,
    });
    const big = "9007199254740993";
    expect(describeScalarChange(big, "9007199254740995", "integer").kind === "number").toBe(true);
    expect(
      (describeScalarChange(big, "9007199254740995", "integer") as { delta?: string }).delta,
    ).toBe("+2");
  });

  it("rounds float deltas and small percentages to one decimal", () => {
    const change = describeScalarChange("0.5", "0.75", "float");
    expect(change).toMatchObject({ kind: "number", delta: "+0.25", percent: "+50 %" });
    expect(describeScalarChange("100", "104", "float")).toMatchObject({ percent: "+4 %" });
    expect(describeScalarChange("1", "2", "integer")).toMatchObject({ percent: "+100 %" });
  });

  it("recognises Go durations and reports the ratio", () => {
    expect(isGoDuration("1h30m")).toBe(true);
    expect(isGoDuration("30")).toBe(false);
    expect(parseGoDurationMs("1h30m")).toBe(5_400_000);
    expect(parseGoDurationMs("250ms")).toBe(250);
    expect(parseGoDurationMs("1.5s")).toBe(1500);
    expect(describeScalarChange("3s", "30s", "string")).toEqual({
      kind: "duration",
      before: "3s",
      after: "30s",
      ratio: "×10",
    });
    expect(describeScalarChange("10s", "5s", "string")).toMatchObject({ ratio: "×0.5" });
    expect(describeScalarChange("0s", "5s", "string")).toEqual({
      kind: "duration",
      before: "0s",
      after: "5s",
    });
    // The schema's format promotes a string that is not obviously a duration.
    expect(describeScalarChange("abc", "5s", "string", "go-duration").kind).toBe("duration");
  });

  it("marks the differing span of a short string and flags long or multi-line ones", () => {
    expect(describeScalarChange("db-primary.internal", "db-replica.internal", "string")).toEqual({
      kind: "string",
      before: "db-primary.internal",
      after: "db-replica.internal",
      common: { prefix: 3, suffix: 9 },
      long: false,
    });
    expect(describeScalarChange("a".repeat(81), "b", "string")).toMatchObject({ long: true });
    expect(describeScalarChange("x\ny", "x", "string")).toMatchObject({ long: true });
    expect(describeScalarChange(undefined, "new", "string")).toEqual({
      kind: "string",
      before: undefined,
      after: "new",
      common: null,
      long: false,
    });
  });

  it("describes booleans and binary sizes", () => {
    expect(describeScalarChange("true", "false", "boolean")).toEqual({
      kind: "boolean",
      before: "true",
      after: "false",
    });
    expect(describeScalarChange("abcd", "abcdef", "binary")).toEqual({
      kind: "binary",
      beforeBytes: 4,
      afterBytes: 6,
    });
  });
});

describe("describeChange", () => {
  it("builds a structural diff for json and reports invalid or oversize sides", () => {
    const change = describeChange('{"a":1}', '{"a":2}', "json");
    expect(change.kind).toBe("json");
    if (change.kind !== "json") throw new Error("expected json");
    expect(change.structural?.changes).toEqual([
      { path: ["a"], kind: "changed", before: "1", after: "2" },
    ]);
    expect(change.invalid).toBe(false);
    expect(change.oversize).toBe(false);

    const invalid = describeChange("{", '{"a":2}', "json");
    if (invalid.kind !== "json") throw new Error("expected json");
    expect(invalid.invalid).toBe(true);
    expect(invalid.structural).toBeNull();

    const huge = `{"a":"${"x".repeat(STRUCTURAL_MAX_BYTES)}"}`;
    expect(overStructuralCap(huge)).toBe(true);
    const oversize = describeChange(huge, '{"a":2}', "json");
    if (oversize.kind !== "json") throw new Error("expected json");
    expect(oversize.oversize).toBe(true);
    expect(oversize.structural).toBeNull();
  });

  it("leaves one-sided json without a structural diff and dispatches scalars", () => {
    const added = describeChange(undefined, '{"beta":true}', "json");
    if (added.kind !== "json") throw new Error("expected json");
    expect(added.structural).toBeNull();
    expect(added.after).toBe('{"beta":true}');
    expect(describeChange("1", "2", "integer").kind).toBe("number");
  });
});
