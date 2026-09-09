import { Fragment } from "react";
import type { MatchRange } from "@/lib/fuzzy";

/**
 * `text` with every range wrapped in a `<mark>`. Ranges come from
 * `lib/fuzzy`'s `matchRanges`, which only ever points at characters the
 * operator actually typed.
 */
export function Highlight({ text, ranges }: { text: string; ranges?: readonly MatchRange[] }) {
  if (!ranges || ranges.length === 0) return <>{text}</>;
  const parts: React.ReactNode[] = [];
  let at = 0;
  ranges.forEach(([start, end], index) => {
    const from = Math.max(at, Math.min(start, text.length));
    const to = Math.max(from, Math.min(end, text.length));
    if (from > at) parts.push(<Fragment key={`t${index}`}>{text.slice(at, from)}</Fragment>);
    if (to > from) parts.push(<mark key={`m${index}`}>{text.slice(from, to)}</mark>);
    at = to;
  });
  if (at < text.length) parts.push(<Fragment key="tail">{text.slice(at)}</Fragment>);
  return <>{parts}</>;
}

/** Characters of the value shown either side of the first match. */
const DEFAULT_CONTEXT = 40;

/**
 * An excerpt of `text` around its first match, so a row that only matched in
 * its value says why it is on screen. Ellipses mark what was cut.
 */
export function Snippet({
  text,
  ranges,
  context = DEFAULT_CONTEXT,
}: {
  text: string;
  ranges: readonly MatchRange[];
  context?: number;
}) {
  const first = ranges[0];
  if (!first) return null;
  const start = Math.max(0, first[0] - context);
  const end = Math.min(text.length, (ranges[ranges.length - 1]?.[1] ?? first[1]) + context);
  const slice = text.slice(start, end);
  const shifted = ranges
    .filter(([from, to]) => to > start && from < end)
    .map(([from, to]) => [Math.max(0, from - start), Math.min(slice.length, to - start)] as const);
  return (
    <span className="search-snippet">
      {start > 0 ? "…" : null}
      <Highlight text={slice} ranges={shifted} />
      {end < text.length ? "…" : null}
    </span>
  );
}
