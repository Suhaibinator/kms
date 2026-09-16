import { describe, expect, it } from "vitest";
import { parseJsonTree } from "@/components/JsonTree";
import {
  describeChange,
  describeLeafChange,
  describeScalarChange,
  detectMoves,
  fieldCounts,
  formatValuePath,
  isGoDuration,
  isSubtreeText,
  nodeText,
  overStructuralCap,
  parseGoDurationMs,
  STRUCTURAL_MAX_BYTES,
  structuralDiff,
  type ValueChange,
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

describe("moved detection", () => {
  it("merges a removed and an added leaf with the same non-trivial text into one moved change at the destination", () => {
    const diff = structuralDiff(
      '{"legacy_endpoint":"https://old.internal:8443/api","x":1}',
      '{"endpoints":{"legacy":"https://old.internal:8443/api"},"x":1}',
    );
    expect(diff?.changes).toEqual([
      {
        path: ["endpoints", "legacy"],
        kind: "moved",
        fromPath: ["legacy_endpoint"],
        before: '"https://old.internal:8443/api"',
        after: '"https://old.internal:8443/api"',
      },
    ]);
    expect(diff?.unchangedLeaves).toBe(1);
    expect(fieldCounts(diff as NonNullable<typeof diff>)).toEqual({
      added: 0,
      removed: 0,
      changed: 0,
      moved: 1,
    });
  });

  it("does not merge when the text is not unique on either side", () => {
    expect(structuralDiff('{"a":"x1234","b":"x1234"}', '{"c":"x1234"}')?.changes).toEqual([
      { path: ["a"], kind: "removed", before: '"x1234"' },
      { path: ["b"], kind: "removed", before: '"x1234"' },
      { path: ["c"], kind: "added", after: '"x1234"' },
    ]);
    expect(structuralDiff('{"a":"x1234"}', '{"b":"x1234","c":"x1234"}')?.changes).toEqual([
      { path: ["a"], kind: "removed", before: '"x1234"' },
      { path: ["b"], kind: "added", after: '"x1234"' },
      { path: ["c"], kind: "added", after: '"x1234"' },
    ]);
  });

  it("ignores trivial literals and pairs anything longer", () => {
    const stays = ["true", "false", "null", "1", "42", '"ab"', "{}", "[]"];
    for (const text of stays) {
      const changes = structuralDiff(`{"a":${text}}`, `{"b":${text}}`)?.changes;
      expect(
        changes?.map((change) => change.kind),
        text,
      ).toEqual(["removed", "added"]);
    }
    for (const text of ["100", '"abc"', "1.5", '{"k":1}', "[1,2]"]) {
      const changes = structuralDiff(`{"a":${text}}`, `{"b":${text}}`)?.changes;
      expect(
        changes?.map((change) => change.kind),
        text,
      ).toEqual(["moved"]);
    }
  });

  it("moves a renamed subtree whole, and a wrapped one field by field", () => {
    expect(
      structuralDiff(
        '{"db":{"host":"db-primary","port":5432}}',
        '{"database":{"host":"db-primary","port":5432}}',
      )?.changes,
    ).toEqual([
      {
        path: ["database"],
        kind: "moved",
        fromPath: ["db"],
        before: '{"host":"db-primary","port":5432}',
        after: '{"host":"db-primary","port":5432}',
      },
    ]);
    // `storage` is new, so it is one added subtree until its leaves are found
    // under the removed `db`; then it splits into the fields that moved.
    expect(
      structuralDiff(
        '{"db":{"host":"db-primary","port":5432}}',
        '{"storage":{"db":{"host":"db-primary","port":5432}}}',
      )?.changes,
    ).toEqual([
      {
        path: ["storage", "db", "host"],
        kind: "moved",
        fromPath: ["db", "host"],
        before: '"db-primary"',
        after: '"db-primary"',
      },
      {
        path: ["storage", "db", "port"],
        kind: "moved",
        fromPath: ["db", "port"],
        before: "5432",
        after: "5432",
      },
    ]);
  });

  it("splits an added subtree only down to the branches that hold a move", () => {
    const diff = structuralDiff(
      '{"anthropic_base_url":"https://api.anthropic.com/v1","anthropic_model":"claude-opus-5","x":1}',
      '{"providers":{"anthropic":{"base_url":"https://api.anthropic.com/v1","model":"claude-opus-5","timeout":"30s"},"openai":{"base_url":"https://api.openai.com/v1","model":"gpt-5"}},"x":1}',
    );
    expect(diff?.changes).toEqual([
      {
        path: ["providers", "anthropic", "base_url"],
        kind: "moved",
        fromPath: ["anthropic_base_url"],
        before: '"https://api.anthropic.com/v1"',
        after: '"https://api.anthropic.com/v1"',
      },
      {
        path: ["providers", "anthropic", "model"],
        kind: "moved",
        fromPath: ["anthropic_model"],
        before: '"claude-opus-5"',
        after: '"claude-opus-5"',
      },
      { path: ["providers", "anthropic", "timeout"], kind: "added", after: '"30s"' },
      {
        path: ["providers", "openai"],
        kind: "added",
        after: '{"base_url":"https://api.openai.com/v1","model":"gpt-5"}',
      },
    ]);
  });

  it("finds an element that left an array and reappeared under a key, but never a reorder", () => {
    expect(
      structuralDiff('{"hosts":["alpha","beta"],"x":1}', '{"hosts":["alpha"],"backup":"beta"}')
        ?.changes,
    ).toEqual([
      {
        path: ["backup"],
        kind: "moved",
        fromPath: ["hosts", "[1]"],
        before: '"beta"',
        after: '"beta"',
      },
      { path: ["x"], kind: "removed", before: "1" },
    ]);
    expect(
      structuralDiff('["alpha","beta","gamma"]', '["beta","gamma","alpha"]')?.changes.map(
        (change) => change.kind,
      ),
    ).toEqual(["changed", "changed", "changed"]);
  });

  it("leaves ambiguous lists in walk order and never touches changed leaves", () => {
    const changes: ValueChange[] = [
      { path: ["a"], kind: "removed", before: '"same-value"' },
      { path: ["m"], kind: "changed", before: '"same-value"', after: '"other"' },
      { path: ["z"], kind: "removed", before: '"same-value"' },
      { path: ["n"], kind: "added", after: '"same-value"' },
    ];
    expect(detectMoves(changes)).toEqual(changes);
    expect(isSubtreeText(' {"a":1}')).toBe(true);
    expect(isSubtreeText("[1]")).toBe(true);
    expect(isSubtreeText('"[1]"')).toBe(false);
  });
});

describe("describeLeafChange", () => {
  it("types a leaf from its JSON token and reuses the scalar deltas", () => {
    expect(describeLeafChange("50", "5")).toEqual({
      kind: "number",
      before: "50",
      after: "5",
      delta: "−45",
      percent: "−90 %",
    });
    expect(describeLeafChange("0.5", "0.75")).toMatchObject({ kind: "number", delta: "+0.25" });
    expect(describeLeafChange('"30s"', '"5s"')).toEqual({
      kind: "duration",
      before: "30s",
      after: "5s",
      ratio: "×0.17",
    });
    expect(describeLeafChange('"db-primary"', '"db-replica"')).toEqual({
      kind: "string",
      before: "db-primary",
      after: "db-replica",
      common: { prefix: 3, suffix: 0 },
      long: false,
    });
    expect(describeLeafChange("true", "false")).toEqual({
      kind: "boolean",
      before: "true",
      after: "false",
    });
    expect(describeLeafChange(undefined, "true")).toEqual({
      kind: "boolean",
      before: undefined,
      after: "true",
    });
  });

  it("reports null, mixed kinds and subtrees without a delta", () => {
    expect(describeLeafChange("null", "null")).toMatchObject({ kind: "null" });
    expect(describeLeafChange("1", "null")).toMatchObject({
      kind: "mixed",
      before: "1",
      after: "null",
    });
    expect(describeLeafChange('"30"', "30")).toMatchObject({ kind: "mixed" });
    expect(describeLeafChange('{"a":1}', undefined)).toMatchObject({
      kind: "subtree",
      before: '{"a":1}',
    });
    expect(describeLeafChange('{"a":1}', "[1]")).toMatchObject({ kind: "subtree" });
    expect(describeLeafChange("not json", "1")).toMatchObject({ kind: "mixed" });
  });
});
