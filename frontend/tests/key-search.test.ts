import { describe, expect, it } from "vitest";
import {
  buildSearchIndex,
  MAX_SEARCHED_TEXT_CHARS,
  matchesSearch,
  type SearchDoc,
  searchIndex,
} from "@/lib/key-search";

interface Row {
  key: string;
  text?: string;
}

function toDoc(row: Row): SearchDoc {
  return { key: row.key, text: row.text };
}

function indexOf(rows: Row[]) {
  return buildSearchIndex(rows, toDoc);
}

/** The ranked matches alone, for the cases that only care about order. */
function matchesFor(rows: Row[] | ReturnType<typeof indexOf>, query: string, limit?: number) {
  const index = Array.isArray(rows) ? indexOf(rows) : rows;
  return searchIndex(index, query, limit).matches;
}

describe("searchIndex", () => {
  it("finds a key by a word-prefix token", () => {
    const index = indexOf([{ key: "billing/timeout" }]);
    const { matches, total } = searchIndex(index, "timeout");
    expect(matches.map((r) => r.item.key)).toEqual(["billing/timeout"]);
    expect(matches[0]?.score).toBe(40);
    expect(total).toBe(1);
  });

  it("requires every token to match, ANDed across the query", () => {
    const index = indexOf([{ key: "billing/timeout" }]);
    expect(matchesFor(index, "bill out").map((r) => r.item.key)).toEqual(["billing/timeout"]);
    expect(matchesFor(index, "bill zzz")).toEqual([]);
  });

  it("ranks a key hit above a value-only hit", () => {
    const index = indexOf([
      { key: "apps/shared-secret" },
      { key: "apps/other", text: "the shared secret lives here" },
    ]);
    const results = matchesFor(index, "shared");
    expect(results.map((r) => r.item.key)).toEqual(["apps/shared-secret", "apps/other"]);
    const [keyHit, valueHit] = results;
    expect(keyHit?.score).toBeGreaterThan(valueHit?.score ?? Number.POSITIVE_INFINITY);
    // The key hit is highlighted on the key; the value hit has no key ranges
    // but does have text ranges to drive the snippet.
    expect(keyHit?.keyRanges.length).toBeGreaterThan(0);
    expect(keyHit?.textRanges).toEqual([]);
    expect(valueHit?.keyRanges).toEqual([]);
    expect(valueHit?.textRanges.length).toBeGreaterThan(0);
  });

  it("does not search a value longer than MAX_SEARCHED_TEXT_CHARS", () => {
    const longText = `${"x".repeat(MAX_SEARCHED_TEXT_CHARS + 1)} uniqueword`;
    const index = indexOf([{ key: "apps/big", text: longText }]);
    expect(matchesFor(index, "uniqueword")).toEqual([]);

    // The same word, in a value under the cap, is found: the cap is why the
    // long value above did not match, not the word itself.
    const shortIndex = indexOf([{ key: "apps/small", text: "uniqueword" }]);
    expect(matchesFor(shortIndex, "uniqueword").map((r) => r.item.key)).toEqual(["apps/small"]);
  });

  it("caps the returned matches at limit but reports the true total", () => {
    const rows = Array.from({ length: 5 }, (_, i) => ({ key: `apps/token-${i}` }));
    const { matches, total } = searchIndex(indexOf(rows), "token", 2);
    expect(matches).toHaveLength(2);
    // The caller's footer counts this, not the cut list: five rows matched.
    expect(total).toBe(5);
  });

  it("reports a total equal to the match count when nothing was cut", () => {
    const rows = Array.from({ length: 3 }, (_, i) => ({ key: `apps/token-${i}` }));
    const { matches, total } = searchIndex(indexOf(rows), "token", 3);
    expect(matches).toHaveLength(3);
    expect(total).toBe(3);
  });

  it("breaks score ties deterministically by ascending key order", () => {
    // All three keys end in "-token", so a query of "token" scores each of
    // them identically as a word-prefix match.
    const rows = [{ key: "gamma-token" }, { key: "alpha-token" }, { key: "beta-token" }];
    const results = matchesFor(rows, "token");
    const scores = new Set(results.map((r) => r.score));
    expect(scores.size).toBe(1);
    expect(results.map((r) => r.item.key)).toEqual(["alpha-token", "beta-token", "gamma-token"]);
  });
});

describe("matchesSearch", () => {
  it("is true for an empty or whitespace-only query", () => {
    expect(matchesSearch({ key: "billing/timeout" }, "")).toBe(true);
    expect(matchesSearch({ key: "billing/timeout" }, "   ")).toBe(true);
  });

  it("applies the same multi-token AND semantics as searchIndex", () => {
    const doc: SearchDoc = { key: "billing/timeout" };
    expect(matchesSearch(doc, "bill out")).toBe(true);
    expect(matchesSearch(doc, "bill zzz")).toBe(false);
  });

  it("matches on the doc's text as well as its key", () => {
    const doc: SearchDoc = { key: "apps/other", text: "the shared secret lives here" };
    expect(matchesSearch(doc, "shared")).toBe(true);
    expect(matchesSearch(doc, "shared zzz")).toBe(false);
  });
});
