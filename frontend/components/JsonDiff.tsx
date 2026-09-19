import { type ReactNode, useMemo, useState } from "react";
import { JsonLine } from "@/components/JsonHighlight";
import { formatJson } from "@/lib/json-text";
import {
  DIFF_MAX_BYTES,
  type DiffResult,
  type DiffRow,
  diffLines,
  foldUnchanged,
  toSideBySide,
  toUnified,
  type UnifiedRow,
} from "@/lib/line-diff";
import { cn } from "@/lib/utils";
import { formatBytes } from "@/lib/validation";

export type JsonDiffLayout = "split" | "unified";

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
  /** Two columns paired line by line (default), or one GitHub-style sequence with a sign column. */
  layout?: JsonDiffLayout;
  className?: string;
}

/**
 * A line diff of two values, side by side or unified. Split rows are paired
 * so the i-th removed line sits beside the i-th added one; unified rows keep
 * the sequence and mark each line in a sign column. The numbers are each
 * side's own line numbers. Colour-coded when the values are JSON.
 */
export function JsonDiff({
  before,
  after,
  beforeLabel = "Before",
  afterLabel = "After",
  contentType,
  fold = true,
  maxHeight,
  layout = "split",
  className,
}: JsonDiffProps) {
  const json = contentType === "json";
  const left = json ? (formatJson(before) ?? before) : before;
  const right = json ? (formatJson(after) ?? after) : after;
  const result = useMemo(() => diffLines(left, right), [left, right]);
  const identical = result.added === 0 && result.removed === 0;

  return (
    <div className={cn("json-diff", className)} data-testid="json-diff" data-layout={layout}>
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
          {/* Fold state is keyed by row index, which differs per layout: a
              layout change remounts the table so no stale index survives. */}
          {layout === "unified" ? (
            <UnifiedTable
              key="unified"
              result={result}
              fold={fold}
              highlight={json}
              beforeLabel={beforeLabel}
              afterLabel={afterLabel}
            />
          ) : (
            <SplitTable
              key="split"
              result={result}
              fold={fold}
              highlight={json}
              beforeLabel={beforeLabel}
              afterLabel={afterLabel}
            />
          )}
        </div>
      )}
    </div>
  );
}

interface TableProps {
  result: DiffResult;
  fold: boolean;
  highlight: boolean;
  beforeLabel: ReactNode;
  afterLabel: ReactNode;
}

/** The folded rows with the expanded folds opened back up, and the fold rows to render. */
function useFoldedRows<T extends { kind: "same" | "change" }>(rows: T[], fold: boolean) {
  const folded = useMemo(() => (fold ? foldUnchanged(rows) : null), [fold, rows]);
  const [expanded, setExpanded] = useState<ReadonlySet<number>>(() => new Set());
  const entries = folded ?? rows.map((row) => ({ kind: "row" as const, row }));
  const expand = (at: number) => setExpanded((current) => new Set(current).add(at));
  return { entries, expanded, expand };
}

function FoldRow({
  count,
  colSpan,
  onExpand,
}: {
  count: number;
  colSpan: number;
  onExpand: () => void;
}) {
  return (
    <tr className="json-diff-fold">
      <td colSpan={colSpan}>
        <button type="button" className="json-diff-fold-button" onClick={onExpand}>
          Show {count} unchanged {count === 1 ? "line" : "lines"}
        </button>
      </td>
    </tr>
  );
}

function SplitTable({ result, fold, highlight, beforeLabel, afterLabel }: TableProps) {
  const rows = useMemo(() => toSideBySide(result.lines), [result]);
  const { entries, expanded, expand } = useFoldedRows(rows, fold);
  return (
    <table className="json-diff-table" data-layout="split">
      {/* `table-layout: fixed` reads its widths from `<col>` or from the
          first row's cells; the header row is two auto `colSpan={2}`
          cells, so without this the two line-number gutters took half
          the table. */}
      <colgroup>
        <col className="json-diff-col-num" />
        <col />
        <col className="json-diff-col-num" />
        <col />
      </colgroup>
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
        {entries.flatMap((entry, index) => {
          if (entry.kind === "fold") {
            if (expanded.has(entry.at)) {
              return rows
                .slice(entry.at, entry.at + entry.count)
                .map((row, offset) => (
                  <SplitRowView key={`${entry.at}-${offset}`} row={row} highlight={highlight} />
                ));
            }
            return [
              <FoldRow
                key={`fold-${entry.at}`}
                count={entry.count}
                colSpan={4}
                onExpand={() => expand(entry.at)}
              />,
            ];
          }
          return [<SplitRowView key={index} row={entry.row} highlight={highlight} />];
        })}
      </tbody>
    </table>
  );
}

function SplitRowView({ row, highlight }: { row: DiffRow; highlight: boolean }) {
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

function UnifiedTable({ result, fold, highlight, beforeLabel, afterLabel }: TableProps) {
  const rows = useMemo(() => toUnified(result.lines), [result]);
  const { entries, expanded, expand } = useFoldedRows(rows, fold);
  return (
    <table className="json-diff-table" data-layout="unified">
      <colgroup>
        <col className="json-diff-col-num" />
        <col className="json-diff-col-num" />
        <col className="json-diff-col-sign" />
        <col />
      </colgroup>
      <thead>
        <tr>
          <th colSpan={4} scope="colgroup">
            {beforeLabel}{" "}
            <span className="release-diff-arrow" aria-hidden>
              →
            </span>{" "}
            {afterLabel}
          </th>
        </tr>
      </thead>
      <tbody>
        {entries.flatMap((entry, index) => {
          if (entry.kind === "fold") {
            if (expanded.has(entry.at)) {
              return rows
                .slice(entry.at, entry.at + entry.count)
                .map((row, offset) => (
                  <UnifiedRowView key={`${entry.at}-${offset}`} row={row} highlight={highlight} />
                ));
            }
            return [
              <FoldRow
                key={`fold-${entry.at}`}
                count={entry.count}
                colSpan={4}
                onExpand={() => expand(entry.at)}
              />,
            ];
          }
          return [<UnifiedRowView key={index} row={entry.row} highlight={highlight} />];
        })}
      </tbody>
    </table>
  );
}

const SIGN: Record<UnifiedRow["op"], string> = { add: "+", del: "−", same: "" };

function UnifiedRowView({ row, highlight }: { row: UnifiedRow; highlight: boolean }) {
  return (
    <tr className="json-diff-row" data-kind={row.kind}>
      <td className="json-diff-num" data-op={row.op}>
        {row.left ?? ""}
      </td>
      <td className="json-diff-num" data-op={row.op}>
        {row.right ?? ""}
      </td>
      <td className="json-diff-sign" data-op={row.op} aria-hidden>
        {SIGN[row.op]}
      </td>
      <td className="json-diff-text" data-op={row.op}>
        <JsonLine text={row.text} plain={!highlight} />
      </td>
    </tr>
  );
}
