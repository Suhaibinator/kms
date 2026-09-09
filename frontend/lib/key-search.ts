// The ranker behind every search box in the console: the parameter and secret
// lists, and the application page's value filter. Frontend-only — the API has
// no search endpoint, so the pages load the namespace (see useNamespaceIndex)
// and rank it here.

import { type MatchRange, matchRangesLower, scoreText, wordsOf } from "./fuzzy";

/** Best matches kept per query; more than this and the answer is "keep typing". */
export const SEARCH_RESULT_LIMIT = 200;

/** How long the box waits after the last keystroke before the URL is rewritten. */
export const SEARCH_DEBOUNCE_MS = 200;

/**
 * Values longer than this are not searched at all. `Parameter.value` is inline
 * in the list response and can run to about a megabyte; scanning every one of
 * them on every keystroke is not worth the two hits it would add.
 */
export const MAX_SEARCHED_TEXT_CHARS = 16_384;

/** Page size the index loader asks for (the API clamps at 1000). */
export const INDEX_PAGE_SIZE = 1000;

/** The honest bound on the index: 5,000 keys, values included. */
export const INDEX_MAX_PAGES = 5;

/** What a row contributes to the index: its key, and any text to match on. */
export interface SearchDoc {
  key: string;
  text?: string;
}

interface IndexEntry<T> {
  item: T;
  key: string;
  lowerKey: string;
  /** The row's searchable text, lowercased once; "" when there is none. */
  lowerText: string;
}

export interface SearchIndex<T> {
  entries: ReadonlyArray<IndexEntry<T>>;
}

export interface SearchMatch<T> {
  item: T;
  score: number;
  /** Where the query hit the key, for `<Highlight>`. */
  keyRanges: MatchRange[];
  /** Where it hit the text, for `<Snippet>`; empty when the key carried the match. */
  textRanges: MatchRange[];
}

/** A token that only matched a row's value, well below the weakest key hit. */
const TEXT_MATCH_SCORE = 6;

/**
 * Lowercases every row once, so a keystroke only compares. Rebuild this when
 * the rows change, never per query.
 */
export function buildSearchIndex<T>(
  rows: readonly T[],
  toDoc: (row: T) => SearchDoc,
): SearchIndex<T> {
  const entries: Array<IndexEntry<T>> = [];
  for (const item of rows) {
    const doc = toDoc(item);
    const text = doc.text ?? "";
    entries.push({
      item,
      key: doc.key,
      lowerKey: doc.key.toLowerCase(),
      lowerText: text.length > MAX_SEARCHED_TEXT_CHARS ? "" : text.toLowerCase(),
    });
  }
  return { entries };
}

/** The per-entry score, or null when a token matched neither key nor text. */
function scoreEntry<T>(entry: IndexEntry<T>, tokens: readonly string[]): number | null {
  let total = 0;
  for (const token of tokens) {
    const keyScore = scoreText(token, entry.lowerKey);
    if (keyScore > 0) {
      total += keyScore;
      continue;
    }
    // An empty text is simply never a hit, so no guard is needed here.
    if (entry.lowerText.includes(token)) {
      total += TEXT_MATCH_SCORE;
      continue;
    }
    return null;
  }
  return total;
}

/**
 * The best `limit` rows for `query`, strongest first and then by key, so the
 * order is stable for the same input. Every whitespace token has to match the
 * key or the text; key hits always outrank value-only hits.
 */
export function searchIndex<T>(
  index: SearchIndex<T>,
  query: string,
  limit: number = SEARCH_RESULT_LIMIT,
): Array<SearchMatch<T>> {
  const tokens = wordsOf(query);
  const scored: Array<{ entry: IndexEntry<T>; score: number }> = [];
  if (tokens.length === 0) {
    for (const entry of index.entries) scored.push({ entry, score: 0 });
  } else {
    for (const entry of index.entries) {
      const score = scoreEntry(entry, tokens);
      if (score !== null) scored.push({ entry, score });
    }
  }
  scored.sort((a, b) => b.score - a.score || (a.entry.key < b.entry.key ? -1 : 1));
  // Ranges are only worth computing for the rows that made the cut.
  return scored.slice(0, limit).map(({ entry, score }) => {
    const keyRanges = matchRangesLower(entry.lowerKey, tokens);
    return {
      item: entry.item,
      score,
      keyRanges,
      textRanges:
        keyRanges.length === 0 && entry.lowerText ? matchRangesLower(entry.lowerText, tokens) : [],
    };
  });
}

/** The same multi-token semantics as a boolean, for the local filters. */
export function matchesSearch(doc: SearchDoc, query: string): boolean {
  const tokens = wordsOf(query);
  if (tokens.length === 0) return true;
  const lowerKey = doc.key.toLowerCase();
  const text = doc.text ?? "";
  const lowerText = text.length > MAX_SEARCHED_TEXT_CHARS ? "" : text.toLowerCase();
  for (const token of tokens) {
    if (scoreText(token, lowerKey) > 0) continue;
    if (lowerText.includes(token)) continue;
    return false;
  }
  return true;
}
