// Generic text scoring, shared by the command palette (lib/palette.ts) and the
// key search the list pages run (lib/key-search.ts). Nothing here knows what a
// parameter or a palette item is: it scores a token against a string.

/** A half-open `[start, end)` slice of a string, for highlighting. */
export type MatchRange = readonly [number, number];

/** Lowercased alphanumeric runs, so `billing/timeout` tokenises to two words. */
export const wordsOf = (text: string): string[] =>
  text
    .toLowerCase()
    .split(/[^a-z0-9]+/)
    .filter(Boolean);

/** True when every character of `needle` appears in `haystack`, in order. */
export function isSubsequence(needle: string, haystack: string): boolean {
  let i = 0;
  for (const ch of haystack) {
    if (ch === needle[i]) i += 1;
    if (i === needle.length) return true;
  }
  return needle.length === 0;
}

/**
 * How well one query token matches one piece of text: exact 100, prefix 60,
 * word prefix 40, substring 15, subsequence (of a token of three characters or
 * more) 4, no match 0. The same ladder the palette scores titles with, so a
 * key search and the palette agree on what "a better match" means.
 */
export function scoreText(token: string, text: string): number {
  const needle = token.toLowerCase();
  if (!needle) return 0;
  const hay = text.toLowerCase();
  if (hay === needle) return 100;
  if (hay.startsWith(needle)) return 60;
  if (wordsOf(hay).some((word) => word.startsWith(needle))) return 40;
  if (hay.includes(needle)) return 15;
  if (needle.length >= 3 && isSubsequence(needle, hay.replace(/[^a-z0-9]/g, ""))) return 4;
  return 0;
}

/** Sorts and merges overlapping or touching ranges. */
function mergeRanges(ranges: Array<[number, number]>): MatchRange[] {
  if (ranges.length < 2) return ranges;
  ranges.sort((a, b) => a[0] - b[0] || a[1] - b[1]);
  const merged: Array<[number, number]> = [ranges[0] as [number, number]];
  for (const range of ranges.slice(1)) {
    const last = merged[merged.length - 1] as [number, number];
    if (range[0] <= last[1]) last[1] = Math.max(last[1], range[1]);
    else merged.push([...range] as [number, number]);
  }
  return merged;
}

/**
 * Every literal occurrence of every token in `lower` (which must already be
 * lowercased), merged. A token that only matched as a subsequence contributes
 * nothing: a highlight has to point at the characters the operator typed, or it
 * lies about why the row is on screen.
 */
export function matchRangesLower(lower: string, tokens: readonly string[]): MatchRange[] {
  const ranges: Array<[number, number]> = [];
  for (const token of tokens) {
    if (!token) continue;
    for (let from = 0; ; ) {
      const at = lower.indexOf(token, from);
      if (at < 0) break;
      ranges.push([at, at + token.length]);
      from = at + 1;
    }
  }
  return mergeRanges(ranges);
}

/** `matchRangesLower` against the raw text and a raw query. */
export function matchRanges(text: string, query: string): MatchRange[] {
  const tokens = wordsOf(query);
  if (tokens.length === 0) return [];
  return matchRangesLower(text.toLowerCase(), tokens);
}
