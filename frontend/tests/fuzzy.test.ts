import { describe, expect, it } from "vitest";
import { matchRanges, scoreText } from "@/lib/fuzzy";

describe("scoreText", () => {
  it("scores an exact match highest", () => {
    expect(scoreText("hello", "hello")).toBe(100);
  });

  it("scores a prefix match below an exact match", () => {
    expect(scoreText("hell", "hello world")).toBe(60);
  });

  it("scores a word-prefix match below a full prefix", () => {
    // The haystack does not start with "wor", but its second word does.
    expect(scoreText("wor", "hello world")).toBe(40);
  });

  it("scores a plain substring below a word-prefix match", () => {
    // "orl" sits inside "world" but does not start any word.
    expect(scoreText("orl", "hello world")).toBe(15);
  });

  it("scores a subsequence match, only for tokens of three characters or more, below a substring", () => {
    // Documented example: "bto" is not literally in "billing-timeout", but its
    // letters appear in order.
    expect(scoreText("bto", "billing-timeout")).toBe(4);
    // Two characters is not enough to bother with a subsequence scan.
    expect(scoreText("bt", "billing-timeout")).toBe(0);
  });

  it("scores no match as zero", () => {
    expect(scoreText("zzz", "hello world")).toBe(0);
    expect(scoreText("", "hello world")).toBe(0);
  });

  it("orders the full ladder as documented: exact > prefix > word-prefix > substring > subsequence > none", () => {
    const scores = [
      scoreText("hello", "hello"),
      scoreText("hell", "hello world"),
      scoreText("wor", "hello world"),
      scoreText("orl", "hello world"),
      scoreText("hlwrd", "hello world"),
      scoreText("zzz", "hello world"),
    ];
    expect(scores).toEqual([100, 60, 40, 15, 4, 0]);
  });

  it("is case-insensitive on both the query and the text", () => {
    expect(scoreText("HELLO", "hello")).toBe(100);
    expect(scoreText("hello", "HELLO")).toBe(100);
    expect(scoreText("HELL", "Hello World")).toBe(60);
    expect(scoreText("WOR", "Hello World")).toBe(40);
    expect(scoreText("ORL", "Hello World")).toBe(15);
    expect(scoreText("BTO", "Billing-Timeout")).toBe(4);
  });
});

describe("matchRanges", () => {
  it("finds every literal occurrence of a token", () => {
    expect(matchRanges("billing/timeout", "billing")).toEqual([[0, 7]]);
  });

  it("merges overlapping ranges", () => {
    // "bcd" -> [1,4), "cde" -> [2,5); they overlap and merge into one span.
    expect(matchRanges("abcdef", "cde bcd")).toEqual([[1, 5]]);
  });

  it("merges touching (adjacent) ranges", () => {
    // "abc" -> [0,3), "def" -> [3,6); they touch at 3 and merge.
    expect(matchRanges("abcdef", "abc def")).toEqual([[0, 6]]);
  });

  it("returns non-overlapping ranges sorted ascending, regardless of token order in the query", () => {
    // Query tokens are given in the reverse of where they occur in the text.
    expect(matchRanges("abcdefghij", "fgh abc")).toEqual([
      [0, 3],
      [5, 8],
    ]);
  });

  it("returns no ranges when the query only matched as a subsequence", () => {
    // Same pair as the scoreText subsequence example: a real (non-zero) score,
    // but nothing literal to highlight.
    expect(scoreText("bto", "billing-timeout")).toBe(4);
    expect(matchRanges("billing-timeout", "bto")).toEqual([]);
  });

  it("returns no ranges for an empty or whitespace-only query", () => {
    expect(matchRanges("billing-timeout", "")).toEqual([]);
    expect(matchRanges("billing-timeout", "   ")).toEqual([]);
  });
});
