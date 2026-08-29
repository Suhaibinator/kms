import { type ReactNode, useMemo, useState } from "react";
import { JsonLine } from "@/components/JsonHighlight";
import { formatJson } from "@/lib/json-text";
import { DIFF_MAX_BYTES, diffLines, foldUnchanged, toSideBySide } from "@/lib/line-diff";
import { cn } from "@/lib/utils";
import { formatBytes } from "@/lib/validation";

export interface JsonDiffProps {
  before: string;
  after: string;
  /** Column headings, e.g. `v2` and `v3`. */
  beforeLabel?: ReactNode;
  afterLabel?: ReactNode;
  /** `json` values are pretty-printed before comparing, so a minified store never hides a change. */
  contentType?: string;
  /** Hide long unchanged stretches behind an expander. */
  fold?: boolean;
  /** CSS length; the table scrolls beyond it. */
  maxHeight?: string;
  className?: string;
}

/**
 * A side-by-side line diff of two values. Rows are paired so the i-th
 * removed line sits beside the i-th added one; the numbers are each side's
 * own line numbers. Colour-coded when the values are JSON.
 */
export function JsonDiff({
  before,
  after,
  beforeLabel = "Before",
  afterLabel = "After",
  contentType,
  fold = true,
  maxHeight,
  className,
}: JsonDiffProps) {
  const json = contentType === "json";
  const left = json ? (formatJson(before) ?? before) : before;
  const right = json ? (formatJson(after) ?? after) : after;
  const result = useMemo(() => diffLines(left, right), [left, right]);
  const rows = useMemo(() => toSideBySide(result.lines), [result]);
  const folded = useMemo(() => (fold ? foldUnchanged(rows) : null), [fold, rows]);
  const [expanded, setExpanded] = useState<ReadonlySet<number>>(() => new Set());
  const identical = result.added === 0 && result.removed === 0;

  return (
    <div className={cn("json-diff", className)} data-testid="json-diff">
      <div className="json-diff-toolbar">
        <span className="json-diff-summary text-xs" role="status">
          {identical ? (
            <span className="faint">No differences.</span>
          ) : (
            <>
              <span className="json-diff-added">+{result.added}</span>{" "}
              <span className="json-diff-removed">−{result.removed}</span>
              <span className="faint">
                {" "}
                {result.added === 1 && result.removed === 0
                  ? "line"
                  : result.removed === 1 && result.added === 0
                    ? "line"
                    : "lines"}
              </span>
            </>
          )}
        </span>
        {result.truncated ? (
          <span className="faint text-xs">
            Too large to align line by line above {formatBytes(DIFF_MAX_BYTES)}; shown as a
            replacement.
          </span>
        ) : null}
      </div>
      {identical ? null : (
        <div className="json-diff-scroll" style={maxHeight ? { maxHeight } : undefined}>
          <table className="json-diff-table">
            <thead>
              <tr>
                <th colSpan={2} scope="colgroup">
                  {beforeLabel}
                </th>
                <th colSpan={2} scope="colgroup">
                  {afterLabel}
                </th>
              </tr>
            </thead>
            <tbody>
              {(folded ?? rows.map((row) => ({ kind: "row" as const, row }))).flatMap(
                (entry, index) => {
                  if (entry.kind === "fold") {
                    if (expanded.has(entry.at)) {
                      return rows
                        .slice(entry.at, entry.at + entry.count)
                        .map((row, offset) => (
                          <DiffRowView key={`${entry.at}-${offset}`} row={row} highlight={json} />
                        ));
                    }
                    return [
                      <tr key={`fold-${entry.at}`} className="json-diff-fold">
                        <td colSpan={4}>
                          <button
                            type="button"
                            className="json-diff-fold-button"
                            onClick={() => setExpanded((current) => new Set(current).add(entry.at))}
                          >
                            Show {entry.count} unchanged {entry.count === 1 ? "line" : "lines"}
                          </button>
                        </td>
                      </tr>,
                    ];
                  }
                  return [<DiffRowView key={index} row={entry.row} highlight={json} />];
                },
              )}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}

function DiffRowView({
  row,
  highlight,
}: {
  row: ReturnType<typeof toSideBySide>[number];
  highlight: boolean;
}) {
  const leftOp = row.kind === "same" ? "same" : row.left ? "del" : "empty";
  const rightOp = row.kind === "same" ? "same" : row.right ? "add" : "empty";
  return (
    <tr className="json-diff-row" data-kind={row.kind}>
      <td className="json-diff-num" data-op={leftOp}>
        {row.left ? row.left.line : ""}
      </td>
      <td className="json-diff-text" data-op={leftOp}>
        {row.left ? <JsonLine text={row.left.text} plain={!highlight} /> : null}
      </td>
      <td className="json-diff-num" data-op={rightOp}>
        {row.right ? row.right.line : ""}
      </td>
      <td className="json-diff-text" data-op={rightOp}>
        {row.right ? <JsonLine text={row.right.text} plain={!highlight} /> : null}
      </td>
    </tr>
  );
}
