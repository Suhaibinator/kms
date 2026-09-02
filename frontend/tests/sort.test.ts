import { describe, expect, it } from "vitest";
import {
  ariaSort,
  compareValues,
  nextSort,
  parseSort,
  type SortColumn,
  SORT_DIRECTION_KEY,
  SORT_KEY,
  sortQuery,
  sortRows,
} from "@/lib/sort";

interface Row {
  key: string;
  version: number;
  type: string | null;
  disabled: boolean;
}

function row(key: string, version: number, type: string | null, disabled = false): Row {
  return { key, version, type, disabled };
}

const COLUMNS: ReadonlyArray<SortColumn<Row>> = [
  { id: "key", label: "Key", value: (r) => r.key },
  { id: "version", label: "Version", value: (r) => r.version },
  { id: "type", label: "Type", value: (r) => r.type },
  { id: "disabled", label: "Status", value: (r) => r.disabled },
  { id: "labels", label: "Labels" },
];

describe("compareValues", () => {
  it("orders numbers numerically rather than as text", () => {
    expect(compareValues(2, 10)).toBeLessThan(0);
    expect(compareValues(10, 2)).toBeGreaterThan(0);
    expect(compareValues(2, 2)).toBe(0);
    expect(compareValues(2, 10, "desc")).toBeGreaterThan(0);
  });

  it("compares strings locale-aware, with embedded numbers in numeric order", () => {
    expect(compareValues("alpha", "beta")).toBeLessThan(0);
    expect(compareValues("v2", "v10")).toBeLessThan(0);
    // Case alone is not a difference worth reordering rows over.
    expect(compareValues("alpha", "ALPHA")).toBe(0);
    expect(compareValues("alpha", "beta", "desc")).toBeGreaterThan(0);
  });

  it("puts false before true, so one state groups at one end", () => {
    expect(compareValues(false, true)).toBeLessThan(0);
    expect(compareValues(false, true, "desc")).toBeGreaterThan(0);
  });

  it("sinks null, undefined and empty strings to the bottom in both directions", () => {
    for (const missing of [null, undefined, ""] as const) {
      expect(compareValues(missing, "alpha")).toBeGreaterThan(0);
      expect(compareValues("alpha", missing)).toBeLessThan(0);
      // The direction flips the present values, never the missing ones.
      expect(compareValues(missing, "alpha", "desc")).toBeGreaterThan(0);
      expect(compareValues("alpha", missing, "desc")).toBeLessThan(0);
    }
    expect(compareValues(null, undefined)).toBe(0);
  });
});

describe("sortRows", () => {
  const rows = [row("beta", 2, "json"), row("alpha", 10, null), row("gamma", 2, "string")];

  it("returns a copy in the loaded order when nothing is sorted", () => {
    const result = sortRows(rows, COLUMNS, null);
    expect(result.map((r) => r.key)).toEqual(["beta", "alpha", "gamma"]);
    expect(result).not.toBe(rows);
  });

  it("orders by a column in both directions", () => {
    expect(sortRows(rows, COLUMNS, { column: "key", direction: "asc" }).map((r) => r.key)).toEqual([
      "alpha",
      "beta",
      "gamma",
    ]);
    expect(sortRows(rows, COLUMNS, { column: "key", direction: "desc" }).map((r) => r.key)).toEqual(
      ["gamma", "beta", "alpha"],
    );
    expect(
      sortRows(rows, COLUMNS, { column: "version", direction: "asc" }).map((r) => r.version),
    ).toEqual([2, 2, 10]);
  });

  it("is stable: equal rows keep the order they were loaded in", () => {
    const ties = [row("delta", 1, "a"), row("charlie", 1, "a"), row("echo", 1, "a")];
    for (const direction of ["asc", "desc"] as const) {
      expect(sortRows(ties, COLUMNS, { column: "version", direction }).map((r) => r.key)).toEqual([
        "delta",
        "charlie",
        "echo",
      ]);
    }
  });

  it("keeps rows with no value last however the column is sorted", () => {
    expect(sortRows(rows, COLUMNS, { column: "type", direction: "asc" }).map((r) => r.key)).toEqual(
      ["beta", "gamma", "alpha"],
    );
    expect(
      sortRows(rows, COLUMNS, { column: "type", direction: "desc" }).map((r) => r.key),
    ).toEqual(["gamma", "beta", "alpha"]);
  });

  it("leaves the order alone for an unknown or unorderable column", () => {
    const untouched = ["beta", "alpha", "gamma"];
    expect(
      sortRows(rows, COLUMNS, { column: "labels", direction: "asc" }).map((r) => r.key),
    ).toEqual(untouched);
    expect(
      sortRows(rows, COLUMNS, { column: "nonsense", direction: "asc" }).map((r) => r.key),
    ).toEqual(untouched);
  });
});

describe("sort state in the URL", () => {
  it("cycles a column ascending, descending, then back to the page's own order", () => {
    const first = nextSort(null, "key");
    expect(first).toEqual({ column: "key", direction: "asc" });
    const second = nextSort(first, "key");
    expect(second).toEqual({ column: "key", direction: "desc" });
    expect(nextSort(second, "key")).toBeNull();
    // A different column always starts ascending.
    expect(nextSort(second, "version")).toEqual({ column: "version", direction: "asc" });
  });

  it("round-trips through the query keys", () => {
    for (const state of [
      { column: "key", direction: "asc" },
      { column: "version", direction: "desc" },
    ] as const) {
      const query = sortQuery(state);
      expect(query).toEqual({ [SORT_KEY]: state.column, [SORT_DIRECTION_KEY]: state.direction });
      expect(parseSort(COLUMNS, query[SORT_KEY], query[SORT_DIRECTION_KEY])).toEqual(state);
    }
  });

  it("empties both keys when nothing is sorted, so the URL loses them", () => {
    expect(sortQuery(null)).toEqual({ [SORT_KEY]: "", [SORT_DIRECTION_KEY]: "" });
    expect(parseSort(COLUMNS, "", "")).toBeNull();
    expect(parseSort(COLUMNS, null, null)).toBeNull();
  });

  it("ignores a URL naming a column this table cannot order by", () => {
    expect(parseSort(COLUMNS, "labels", "asc")).toBeNull();
    expect(parseSort(COLUMNS, "made-up", "asc")).toBeNull();
  });

  it("treats any direction but desc as ascending", () => {
    expect(parseSort(COLUMNS, "key", "sideways")).toEqual({ column: "key", direction: "asc" });
    expect(parseSort(COLUMNS, "key", undefined)).toEqual({ column: "key", direction: "asc" });
  });

  it("announces the ordered column and only that one", () => {
    expect(ariaSort({ column: "key", direction: "asc" }, "key")).toBe("ascending");
    expect(ariaSort({ column: "key", direction: "desc" }, "key")).toBe("descending");
    expect(ariaSort({ column: "key", direction: "asc" }, "version")).toBe("none");
    expect(ariaSort(null, "key")).toBe("none");
  });
});
