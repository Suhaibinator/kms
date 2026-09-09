import type { MatchRange } from "@/lib/fuzzy";
import { matchValueRow } from "@/lib/key-search";
import { storedValue } from "@/lib/overview";
import type { OverviewValue } from "@/lib/types";

/** The excerpt of a stored value that explains why its row is on screen. */
export interface ValueSnippet {
  text: string;
  ranges: MatchRange[];
}

export interface ValueFilterResult {
  /** The rows that matched, in their original order. */
  shown: OverviewValue[];
  /** Alias → excerpt, for the rows the value alone put on screen. */
  snippets: Map<string, ValueSnippet>;
}

/**
 * The contract values matching `filter`, searching each row's alias, resolved
 * key and stored value. Shared by the pipeline column and the environment
 * page's table so one filter box means the same thing in both.
 */
export function valueMatches(
  rows: readonly OverviewValue[],
  values: ReadonlyMap<string, string> | undefined,
  filter: string,
): ValueFilterResult {
  const snippets = new Map<string, ValueSnippet>();
  const shown = rows.filter((row) => {
    const text = storedValue(values, row);
    const match = matchValueRow(
      { key: row.key ?? row.alias, alias: row.alias, value: text },
      filter,
    );
    if (!match) return false;
    if (match.valueRanges.length > 0 && text !== undefined) {
      snippets.set(row.alias, { text, ranges: match.valueRanges });
    }
    return true;
  });
  return { shown, snippets };
}
